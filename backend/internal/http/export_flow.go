package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/export"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/models"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/service"
	log "github.com/sirupsen/logrus"
)

type exportRequest struct {
	clientIP     string
	dealIDs      []int
	exportFormat string
	projectField string
}

func (h *Handler) parseExportRequest(
	w http.ResponseWriter,
	r *http.Request) (exportRequest, bool) {
	req := exportRequest{
		clientIP:     clientIPFromRequest(r),
		projectField: projectFieldCode,
	}

	if h.exportLimiter != nil && !h.exportLimiter.Allow(req.clientIP) {
		writeMappedError(w, errExportRateLimited, "too many export requests, try again later")
		return req, false
	}

	reqContentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.Contains(reqContentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			writeMappedError(w, errInvalidMultipartForm, "invalid multipart form: "+err.Error())
			return req, false
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeMappedError(w, errInvalidForm, "invalid form: "+err.Error())
			return req, false
		}
	}

	dealIDs, err := parseDealIDs(r.FormValue("deal_ids"), r.FormValue("deal_id"))
	if err != nil {
		writeMappedError(w, errInvalidDealIDs, err.Error())
		return req, false
	}
	req.dealIDs = dealIDs

	exportFormat, err := parseExportFormat(r.FormValue("format"))
	if err != nil {
		writeMappedError(w, errUnsupportedExportFormat, err.Error())
		return req, false
	}
	req.exportFormat = exportFormat

	return req, true
}

func (h *Handler) collectExportData(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	scope string,
	req exportRequest,
	auditErrText *string) ([]models.ProjectRow, []models.TaskRow, []string, service.ExportStats, string, bool) {
	bClient, ok := h.newBitrixClientFromRequest(w, r)
	if !ok {
		return nil, nil, nil, service.ExportStats{}, "", false
	}
	bClient.ConfigureSupportMapping(h.cfg.SupportLinkDealField, h.cfg.SupportMeasureValueField)

	projects, sourceLabel, err := h.loadProjects(
		ctx,
		r,
		bClient,
		req.dealIDs,
	)
	if err != nil {
		if isCanceledErr(err) {
			h.respondExportCanceled(
				scope,
				w,
				auditErrText,
			)
			return nil, nil, nil, service.ExportStats{}, "", false
		}
		h.markStatusError(scope, err.Error())
		*auditErrText = err.Error()
		log.WithError(err).Error("export load projects failed")
		writeMappedError(
			w,
			errProjectsLoadFailed,
			err.Error())
		return nil, nil, nil, service.ExportStats{}, "", false
	}
	if len(projects) == 0 && sourceLabel != models.BitrixApi {
		h.markStatusError(scope, "no projects found")
		*auditErrText = "no projects found"
		writeMappedError(w, errNoProjectsFound, "no projects found")
		return nil, nil, nil, service.ExportStats{}, "", false
	}

	svc := service.NewExporter(
		bClient,
		h.cfg.TaskWorkers,
		h.cfg.TaskStrategy,
	)
	isFullBitrixExport := sourceLabel == models.BitrixApi
	allowTitleFallback := !isFullBitrixExport

	var (
		tasks  []models.TaskRow
		stats  service.ExportStats
		issues []string
	)

	if isFullBitrixExport {
		const pageSize = 200
		start := 0
		processed := 0
		var allProjects []models.ProjectRow

		for {
			page, pageErr := bClient.GetDealsPage(
				ctx,
				start,
				pageSize,
			)
			if pageErr != nil {
				if isCanceledErr(pageErr) {
					h.respondExportCanceled(
						scope,
						w,
						nil,
					)
					return nil, nil, nil, service.ExportStats{}, "", false
				}
				h.markStatusError(scope, "failed to load deals page: "+pageErr.Error())
				writeMappedError(w, errDealsPageLoadFailed, "failed to load deals page: "+pageErr.Error())
				return nil, nil, nil, service.ExportStats{}, "", false
			}
			if len(page.Rows) == 0 {
				break
			}

			allProjects = append(allProjects, page.Rows...)
			processed += len(page.Rows)
			pageDealsWithSupport, pageSupportMeasures := collectSupportStats(page.Rows)
			log.WithFields(log.Fields{
				"phase":                 models.PhasePassport,
				"deals_page_start":      start,
				"deals_page_size":       len(page.Rows),
				"deals_processed":       processed,
				"deals_total_reported":  page.Total,
				"deals_page_next":       page.Next,
				"deals_with_support":    pageDealsWithSupport,
				"support_measures_page": pageSupportMeasures,
			}).Info("full export deals page processed")
			h.setStatus(scope, func(s *exportStatus) {
				s.Phase = models.PhasePassport
				s.DealsProcessed = processed
				s.DealsWithSupport += pageDealsWithSupport
				s.SupportMeasuresTotal += pageSupportMeasures
			})

			if page.Next == 0 || page.Next <= start {
				break
			}
			start = page.Next
		}

		h.setStatus(scope, func(s *exportStatus) {
			s.Phase = models.PhaseTasks
			s.DealsTotal = len(allProjects)
		})

		if strings.EqualFold(h.cfg.TaskStrategy, models.StrategyBulk) {
			allTasks, allStats, allIssues, allErr := svc.BuildTasks(
				ctx,
				allProjects,
				req.projectField,
				allowTitleFallback)
			if allErr != nil {
				h.handleTasksCollectError(scope, w, allErr, auditErrText)
				return nil, nil, nil, service.ExportStats{}, "", false
			}
			tasks = allTasks
			issues = allIssues
			stats = mergeExportStats(stats, allStats)
			h.setStatus(scope, func(s *exportStatus) {
				s.Phase = models.PhaseTasks
				s.DealsProcessed = len(allProjects)
				s.TasksTotal = len(tasks)
			})
		} else {
			for i := 0; i < len(allProjects); i += pageSize {
				end := i + pageSize
				if end > len(allProjects) {
					end = len(allProjects)
				}
				chunk := allProjects[i:end]
				chunkTasks, chunkStats, chunkIssues, chunkErr := svc.BuildTasks(
					ctx,
					chunk,
					req.projectField,
					allowTitleFallback,
				)
				if chunkErr != nil {
					h.handleTasksCollectError(
						scope,
						w,
						chunkErr,
						auditErrText,
					)
					return nil, nil, nil, service.ExportStats{}, "", false
				}
				tasks = append(tasks, chunkTasks...)
				issues = append(issues, chunkIssues...)
				stats = mergeExportStats(stats, chunkStats)
				h.setStatus(scope, func(s *exportStatus) {
					s.Phase = models.PhaseTasks
					s.DealsProcessed = end
					s.TasksTotal = len(tasks)
				})
			}
		}

		for i := range allProjects {
			allProjects[i].Seq = i + 1
		}
		projects = allProjects
	} else {
		supportDeals, supportMeasures := collectSupportStats(projects)
		h.setStatus(scope, func(s *exportStatus) {
			s.DealsWithSupport = supportDeals
			s.SupportMeasuresTotal = supportMeasures
		})

		var callErr error
		tasks, stats, issues, callErr = svc.BuildTasks(
			ctx,
			projects,
			req.projectField,
			allowTitleFallback,
		)
		if callErr != nil {
			h.handleTasksCollectError(
				scope,
				w,
				callErr,
				auditErrText,
			)
			return nil, nil, nil, service.ExportStats{}, "", false
		}
	}

	supportDeals, supportMeasures := collectSupportStats(projects)
	stats.DealsWithSupport = supportDeals
	stats.SupportMeasuresTotal = supportMeasures

	return projects, tasks, issues, stats, sourceLabel, true
}

func (h *Handler) buildExportBinary(
	scope string,
	w http.ResponseWriter,
	exportFormat string,
	projects []models.ProjectRow,
	tasks []models.TaskRow,
	auditErrText *string,
) ([]byte, string, bool) {
	var (
		result      []byte
		contentType string
		err         error
	)
	switch exportFormat {
	case "docx":
		result, err = export.BuildResultDOCX(projects, tasks)
		contentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	default:
		result, err = export.BuildResultXLSX(projects, tasks)
		contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	if err != nil {
		*auditErrText = "failed to build export file: " + err.Error()
		h.markStatusError(scope, "failed to build export file: "+err.Error())
		writeMappedError(w, errExportBuildFailed, "failed to build export file: "+err.Error())
		return nil, "", false
	}
	return result, contentType, true
}

func (h *Handler) respondExportCanceled(
	scope string,
	w http.ResponseWriter,
	auditErrText *string) {
	h.markStatusCanceledByUser(scope)
	if auditErrText != nil {
		*auditErrText = "export canceled by user"
	}
	writeMappedError(w, errExportCanceled, "export canceled by user")
}

func (h *Handler) handleTasksCollectError(
	scope string,
	w http.ResponseWriter,
	err error,
	auditErrText *string) {
	if err == nil {
		return
	}
	if isCanceledErr(err) {
		h.respondExportCanceled(
			scope,
			w,
			auditErrText,
		)
		return
	}

	detail := "failed to collect tasks: " + err.Error()
	if auditErrText != nil {
		*auditErrText = detail
	}
	h.markStatusError(scope, detail)
	if strings.Contains(strings.ToLower(err.Error()), "bitrix auth failed") {
		writeMappedError(
			w,
			errBitrixAuthFailed,
			"bitrix auth token is invalid or expired",
		)
		return
	}

	writeMappedError(
		w,
		errTasksCollectFailed,
		detail,
	)
}

func (h *Handler) completeExportSuccess(
	w http.ResponseWriter,
	scope string,
	dealIDs []int,
	exportFormat string,
	sourceLabel string,
	result []byte,
	contentType string,
	issues []string,
	stats service.ExportStats,
	exportStartedAt time.Time,
) {
	filename := buildDownloadFilename(
		sourceLabel,
		dealIDs,
		exportFormat,
	)
	h.storeLastResult(
		scope,
		result,
		filename,
		contentType,
	)
	h.setStatus(scope, func(s *exportStatus) {
		s.Running = false
		s.CanCancel = false
		s.CancelRequested = false
		s.Phase = models.PhaseIdle
		s.DealsWithSupport = stats.DealsWithSupport
		s.SupportMeasuresTotal = stats.SupportMeasuresTotal
		s.FinishedAt = time.Now().UTC()
		s.LastError = ""
	})
	w.Header().Set("X-Export-Success", "true")
	w.Header().Set("X-Export-Source", sourceLabel)
	w.Header().Set("X-Export-Deals-Total", strconv.Itoa(stats.DealsTotal))
	w.Header().Set("X-Export-Tasks-Total", strconv.Itoa(stats.TasksTotal))
	w.Header().Set("X-Export-Deals-With-Support", strconv.Itoa(stats.DealsWithSupport))
	w.Header().Set("X-Export-Support-Measures-Total", strconv.Itoa(stats.SupportMeasuresTotal))
	w.Header().Set("X-Export-Issues-Count", strconv.Itoa(len(issues)))
	w.Header().Set("X-Export-File-Name", filename)
	writeAPISuccess(
		w,
		http.StatusOK,
		"export_completed",
		"Выгрузка завершена, файл готов к скачиванию",
		models.ExportCompletedData{
			FileName:    filename,
			ContentType: contentType,
			SizeBytes:   len(result),
			DownloadURL: "/api/export/download-last",
			Format:      exportFormat,
			Source:      sourceLabel,
			IssuesCount: len(issues),
			Stats: models.ExportStatsData{
				DealsTotal:           stats.DealsTotal,
				TasksTotal:           stats.TasksTotal,
				DealsWithSupport:     stats.DealsWithSupport,
				SupportMeasuresTotal: stats.SupportMeasuresTotal,
			},
		},
		nil,
	)
	log.WithFields(log.Fields{
		"source":                 sourceLabel,
		"format":                 exportFormat,
		"duration_ms":            time.Since(exportStartedAt).Milliseconds(),
		"deals_total":            stats.DealsTotal,
		"tasks_total":            stats.TasksTotal,
		"deals_with_support":     stats.DealsWithSupport,
		"support_measures_total": stats.SupportMeasuresTotal,
		"issues_count":           len(issues),
		"file_name":              filename,
	}).Info("export completed")
}
