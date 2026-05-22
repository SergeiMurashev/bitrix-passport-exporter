package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/auth"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/export"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/service"
	log "github.com/sirupsen/logrus"
)

// export godoc
// @Summary      Запуск выгрузки паспорта проекта
// @Description  Формирует XLSX/DOCX и возвращает JSON с метаданными готового файла; файл скачивается через /api/export/download-last
// @Tags         Export
// @Accept       mpfd
// @Produce      json
// @Security     CookieAuth
// @Security     ApiKeyAuth
// @Security     BearerAuth
// @Param        deal_ids  formData  string  false  "ID сделок через запятую"
// @Param        file      formData  file    false  "Файл сделок (.xls/.xlsx/.html)"
// @Param        format    formData  string  false  "Формат файла (xlsx|docx)"  Enums(xlsx,docx)
// @Success      200       {object}  SwaggerExportResponse
// @Failure      400       {object}  SwaggerErrorResponse
// @Failure      401       {object}  SwaggerErrorResponse
// @Failure      409       {object}  SwaggerErrorResponse
// @Failure      429       {object}  SwaggerErrorResponse
// @Failure      500       {object}  SwaggerErrorResponse
// @Failure      502       {object}  SwaggerErrorResponse
// @Router       /api/export [post]
func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	clientIP := clientIPFromRequest(r)
	if h.exportLimiter != nil && !h.exportLimiter.Allow(clientIP) {
		writeAPIErrorSimple(w, http.StatusTooManyRequests, "EXPORT_RATE_LIMITED", "Слишком много запросов на выгрузку", "too many export requests, try again later")
		return
	}
	reqContentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.Contains(reqContentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			writeAPIErrorSimple(w, http.StatusBadRequest, "INVALID_MULTIPART_FORM", "Некорректная multipart-форма", "invalid multipart form: "+err.Error())
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeAPIErrorSimple(w, http.StatusBadRequest, "INVALID_FORM", "Некорректные параметры запроса", "invalid form: "+err.Error())
			return
		}
	}

	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		writeAPIErrorSimple(w, http.StatusInternalServerError, "WEBHOOK_EMPTY", "Сервер не настроен: не указан webhook Bitrix24", "server is not configured: BITRIX_WEBHOOK_URL is empty")
		return
	}
	projectField := projectFieldCode

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		writeAPIErrorSimple(w, http.StatusBadRequest, "WEBHOOK_INVALID", "Некорректный webhook Bitrix24", "invalid webhook: "+err.Error())
		return
	}
	bClient.ConfigureSupportMapping(h.cfg.SupportLinkDealField, h.cfg.SupportMeasureValueField)

	dealIDs, err := parseDealIDs(r.FormValue("deal_ids"), r.FormValue("deal_id"))
	if err != nil {
		writeAPIErrorSimple(w, http.StatusBadRequest, "INVALID_DEAL_IDS", "Некорректный список ID сделок", err.Error())
		return
	}
	exportFormat, err := parseExportFormat(r.FormValue("format"))
	if err != nil {
		writeAPIErrorSimple(w, http.StatusBadRequest, "UNSUPPORTED_EXPORT_FORMAT", "Неподдерживаемый формат выгрузки", err.Error())
		return
	}
	exportStartedAt := time.Now()
	claims, _ := h.sessionClaimsFromRequest(r)
	exportMode := detectExportMode(r, dealIDs)
	sourceLabel := "unknown"
	auditSuccess := false
	auditErrText := ""
	var stats service.ExportStats
	defer func() {
		if auditSuccess {
			auditErrText = ""
		}
		if !auditSuccess && strings.TrimSpace(auditErrText) == "" {
			auditErrText = "request failed"
		}
		h.recordExportAudit(context.Background(), auth.ExportAuditRecord{
			UserID:               userIDFromClaims(claims),
			UserLogin:            userLoginFromClaims(claims),
			ClientIP:             clientIP,
			Source:               sourceLabel,
			Mode:                 exportMode,
			Format:               exportFormat,
			Success:              auditSuccess,
			ErrorText:            auditErrText,
			DurationMs:           time.Since(exportStartedAt).Milliseconds(),
			DealsTotal:           stats.DealsTotal,
			TasksTotal:           stats.TasksTotal,
			DealsWithSupport:     stats.DealsWithSupport,
			SupportMeasuresTotal: stats.SupportMeasuresTotal,
		})
	}()

	isFullExport := len(dealIDs) == 0
	fullExportLocked := false
	if isFullExport {
		if !h.tryStartFullExport() {
			writeAPIErrorSimple(w, http.StatusTooManyRequests, "EXPORT_ALREADY_RUNNING", "Полная выгрузка уже выполняется", "full export is already running; please wait and retry")
			return
		}
		fullExportLocked = true
		defer func() {
			if fullExportLocked {
				h.finishFullExport()
			}
		}()
	}

	exportTimeout := 12 * time.Minute
	if isFullExport {
		exportTimeout = 35 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()
	h.setExportCancel(cancel)
	defer h.clearExportCancel()

	h.setStatus(func(s *exportStatus) {
		s.Running = true
		s.CanCancel = true
		s.CancelRequested = false
		s.Phase = "passport"
		s.DealsProcessed = 0
		s.DealsTotal = 0
		s.TasksTotal = 0
		s.DealsWithSupport = 0
		s.SupportMeasuresTotal = 0
		s.StartedAt = time.Now().UTC()
		s.FinishedAt = time.Time{}
		s.LastError = ""
	})

	projects, sourceLabelValue, err := h.loadProjects(ctx, r, bClient, dealIDs)
	if err != nil {
		if isCanceledErr(err) {
			h.markStatusCanceledByUser()
			auditErrText = "export canceled by user"
			writeAPIErrorSimple(w, http.StatusConflict, "EXPORT_CANCELED", "Выгрузка отменена пользователем", "export canceled by user")
			return
		}
		h.markStatusError(err.Error())
		auditErrText = err.Error()
		log.WithError(err).WithField("deal_ids", dealIDs).Error("export load projects failed")
		writeAPIErrorSimple(w, http.StatusBadRequest, "PROJECTS_LOAD_FAILED", "Не удалось загрузить сделки", err.Error())
		return
	}
	sourceLabel = sourceLabelValue
	if len(projects) == 0 && sourceLabel != "bitrix_api" {
		h.markStatusError("no projects found")
		auditErrText = "no projects found"
		writeAPIErrorSimple(w, http.StatusBadRequest, "NO_PROJECTS_FOUND", "Сделки не найдены", "no projects found")
		return
	}
	log.WithFields(log.Fields{
		"source":        sourceLabel,
		"deals":         len(projects),
		"project_field": projectField,
		"deal_ids":      dealIDs,
	}).Info("export request")

	svc := service.NewExporter(bClient, h.cfg.TaskWorkers, h.cfg.TaskStrategy)
	isFullBitrixExport := sourceLabel == "bitrix_api"
	allowTitleFallback := true
	if isFullBitrixExport {
		allowTitleFallback = false
	}
	var (
		tasks  []model.TaskRow
		issues []string
	)
	if isFullBitrixExport {
		const pageSize = 200
		start := 0
		processed := 0
		var allProjects []model.ProjectRow
		log.WithField("source", "bitrix_api").Info("export phase=passport started")
		for {
			page, pageErr := bClient.GetDealsPage(ctx, start, pageSize)
			if pageErr != nil {
				if isCanceledErr(pageErr) {
					h.markStatusCanceledByUser()
					writeAPIErrorSimple(w, http.StatusConflict, "EXPORT_CANCELED", "Выгрузка отменена пользователем", "export canceled by user")
					return
				}
				h.markStatusError("failed to load deals page: " + pageErr.Error())
				writeAPIErrorSimple(w, http.StatusInternalServerError, "DEALS_PAGE_LOAD_FAILED", "Не удалось загрузить страницу сделок из Bitrix24", "failed to load deals page: "+pageErr.Error())
				return
			}
			if len(page.Rows) == 0 {
				break
			}
			allProjects = append(allProjects, page.Rows...)
			processed += len(page.Rows)
			pageDealsWithSupport, pageSupportMeasures := collectSupportStats(page.Rows)
			log.WithFields(log.Fields{
				"phase":           "passport",
				"deals_processed": processed,
				"next":            page.Next,
			}).Info("export progress")
			h.setStatus(func(s *exportStatus) {
				s.Phase = "passport"
				s.DealsProcessed = processed
				s.DealsWithSupport += pageDealsWithSupport
				s.SupportMeasuresTotal += pageSupportMeasures
			})
			if page.Next == 0 || page.Next <= start {
				break
			}
			start = page.Next
		}
		log.WithFields(log.Fields{
			"phase":       "passport",
			"deals_total": len(allProjects),
		}).Info("export phase finished")
		h.setStatus(func(s *exportStatus) {
			s.Phase = "tasks"
			s.DealsTotal = len(allProjects)
		})

		log.WithFields(log.Fields{
			"phase":       "tasks",
			"deals_total": len(allProjects),
		}).Info("export phase started")
		if strings.EqualFold(h.cfg.TaskStrategy, "bulk") {
			allTasks, allStats, allIssues, allErr := svc.BuildTasks(ctx, allProjects, projectField, allowTitleFallback)
			if allErr != nil {
				if isCanceledErr(allErr) {
					h.markStatusCanceledByUser()
					auditErrText = "export canceled by user"
					writeAPIErrorSimple(w, http.StatusConflict, "EXPORT_CANCELED", "Выгрузка отменена пользователем", "export canceled by user")
					return
				}
				auditErrText = "failed to collect tasks: " + allErr.Error()
				h.markStatusError("failed to collect tasks: " + allErr.Error())
				if strings.Contains(allErr.Error(), "webhook auth failed") {
					writeAPIErrorSimple(w, http.StatusBadGateway, "WEBHOOK_AUTH_FAILED", "Webhook Bitrix24 недействителен или истек", "bitrix webhook is invalid or expired")
					return
				}
				writeAPIErrorSimple(w, http.StatusInternalServerError, "TASKS_COLLECT_FAILED", "Не удалось собрать задачи по сделкам", "failed to collect tasks: "+allErr.Error())
				return
			}
			tasks = allTasks
			issues = allIssues
			stats = mergeExportStats(stats, allStats)
			log.WithFields(log.Fields{
				"phase":           "tasks",
				"deals_processed": len(allProjects),
				"tasks_total":     len(tasks),
			}).Info("export progress")
			h.setStatus(func(s *exportStatus) {
				s.Phase = "tasks"
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
				chunkTasks, chunkStats, chunkIssues, chunkErr := svc.BuildTasks(ctx, chunk, projectField, allowTitleFallback)
				if chunkErr != nil {
					if isCanceledErr(chunkErr) {
						h.markStatusCanceledByUser()
						auditErrText = "export canceled by user"
						writeAPIErrorSimple(w, http.StatusConflict, "EXPORT_CANCELED", "Выгрузка отменена пользователем", "export canceled by user")
						return
					}
					auditErrText = "failed to collect tasks: " + chunkErr.Error()
					h.markStatusError("failed to collect tasks: " + chunkErr.Error())
					if strings.Contains(chunkErr.Error(), "webhook auth failed") {
						writeAPIErrorSimple(w, http.StatusBadGateway, "WEBHOOK_AUTH_FAILED", "Webhook Bitrix24 недействителен или истек", "bitrix webhook is invalid or expired")
						return
					}
					writeAPIErrorSimple(w, http.StatusInternalServerError, "TASKS_COLLECT_FAILED", "Не удалось собрать задачи по сделкам", "failed to collect tasks: "+chunkErr.Error())
					return
				}
				tasks = append(tasks, chunkTasks...)
				issues = append(issues, chunkIssues...)
				stats = mergeExportStats(stats, chunkStats)
				log.WithFields(log.Fields{
					"phase":           "tasks",
					"deals_processed": end,
					"tasks_total":     len(tasks),
				}).Info("export progress")
				h.setStatus(func(s *exportStatus) {
					s.Phase = "tasks"
					s.DealsProcessed = end
					s.TasksTotal = len(tasks)
				})
			}
		}
		log.WithFields(log.Fields{
			"phase":       "tasks",
			"tasks_total": len(tasks),
		}).Info("export phase finished")

		for i := range allProjects {
			allProjects[i].Seq = i + 1
		}
		projects = allProjects
	} else {
		supportDeals, supportMeasures := collectSupportStats(projects)
		h.setStatus(func(s *exportStatus) {
			s.DealsWithSupport = supportDeals
			s.SupportMeasuresTotal = supportMeasures
		})
		var callErr error
		tasks, stats, issues, callErr = svc.BuildTasks(ctx, projects, projectField, allowTitleFallback)
		if callErr != nil {
			if isCanceledErr(callErr) {
				h.markStatusCanceledByUser()
				auditErrText = "export canceled by user"
				writeAPIErrorSimple(w, http.StatusConflict, "EXPORT_CANCELED", "Выгрузка отменена пользователем", "export canceled by user")
				return
			}
			auditErrText = "failed to collect tasks: " + callErr.Error()
			h.markStatusError("failed to collect tasks: " + callErr.Error())
			if strings.Contains(callErr.Error(), "webhook auth failed") {
				writeAPIErrorSimple(w, http.StatusBadGateway, "WEBHOOK_AUTH_FAILED", "Webhook Bitrix24 недействителен или истек", "bitrix webhook is invalid or expired")
				return
			}
			writeAPIErrorSimple(w, http.StatusInternalServerError, "TASKS_COLLECT_FAILED", "Не удалось собрать задачи по сделкам", "failed to collect tasks: "+callErr.Error())
			return
		}
	}
	supportDeals, supportMeasures := collectSupportStats(projects)
	stats.DealsWithSupport = supportDeals
	stats.SupportMeasuresTotal = supportMeasures
	log.WithFields(log.Fields{
		"deals_total":            stats.DealsTotal,
		"deals_with_project":     stats.DealsWithProject,
		"deals_without_project":  stats.DealsWithoutProject,
		"deals_with_support":     stats.DealsWithSupport,
		"support_measures_total": stats.SupportMeasuresTotal,
		"resolve_errors":         stats.DealsResolveErrors,
		"projects_with_tasks":    stats.ProjectsWithTasks,
		"projects_without_tasks": stats.ProjectsWithoutTasks,
		"task_load_errors":       stats.TaskLoadErrors,
		"tasks_total":            stats.TasksTotal,
	}).Info("export stats")
	for _, issue := range issues {
		log.WithField("issue", issue).Warn("export issue")
	}

	var (
		result      []byte
		contentType string
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
		auditErrText = "failed to build export file: " + err.Error()
		h.markStatusError("failed to build export file: " + err.Error())
		writeAPIErrorSimple(w, http.StatusInternalServerError, "EXPORT_BUILD_FAILED", "Не удалось сформировать файл выгрузки", "failed to build export file: "+err.Error())
		return
	}
	if fullExportLocked {
		h.finishFullExport()
		fullExportLocked = false
	}
	filename := buildDownloadFilename(sourceLabel, dealIDs, exportFormat)
	h.storeLastResult(result, filename, contentType)
	h.setStatus(func(s *exportStatus) {
		s.Running = false
		s.CanCancel = false
		s.CancelRequested = false
		s.Phase = "idle"
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
	writeAPISuccess(w, http.StatusOK, "export_completed", "Выгрузка завершена, файл готов к скачиванию", model.ExportCompletedData{
		FileName:    filename,
		ContentType: contentType,
		SizeBytes:   len(result),
		DownloadURL: "/api/export/download-last",
		Format:      exportFormat,
		Source:      sourceLabel,
		IssuesCount: len(issues),
		Stats: model.ExportStatsData{
			DealsTotal:           stats.DealsTotal,
			TasksTotal:           stats.TasksTotal,
			DealsWithSupport:     stats.DealsWithSupport,
			SupportMeasuresTotal: stats.SupportMeasuresTotal,
		},
	}, nil)
	auditSuccess = true
}

func (h *Handler) tryStartFullExport() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fullExportBusy {
		return false
	}
	h.fullExportBusy = true
	return true
}

func (h *Handler) finishFullExport() {
	h.mu.Lock()
	h.fullExportBusy = false
	h.mu.Unlock()
}

func (h *Handler) setExportCancel(cancel context.CancelFunc) {
	h.mu.Lock()
	h.fullExportCancel = cancel
	h.cancelRequestedByUser = false
	h.mu.Unlock()
}

func (h *Handler) clearExportCancel() {
	h.mu.Lock()
	h.fullExportCancel = nil
	h.cancelRequestedByUser = false
	h.mu.Unlock()
}

func (h *Handler) setStatus(update func(*exportStatus)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	update(&h.status)
}

func (h *Handler) storeLastResult(data []byte, filename, contentType string) {
	if len(data) == 0 {
		return
	}
	baseDir := filepath.Join(os.TempDir(), "bitrix-passport-exporter")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		log.WithError(err).Warn("failed to prepare temp dir for last export")
		return
	}
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(filename)))
	if ext == "" {
		ext = ".bin"
	}
	tmpPath := filepath.Join(baseDir, fmt.Sprintf("last-export-%d%s", time.Now().UnixNano(), ext))
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		log.WithError(err).Warn("failed to persist last export to disk")
		return
	}

	h.mu.Lock()
	oldPath := h.lastResultPath
	defer h.mu.Unlock()
	h.lastResultPath = tmpPath
	h.lastFilename = filename
	h.lastContentType = strings.TrimSpace(contentType)
	h.lastUpdatedAt = time.Now().UTC()
	h.status.HasLastResult = true
	h.status.LastFileName = filename
	if strings.TrimSpace(oldPath) != "" && oldPath != tmpPath {
		_ = os.Remove(oldPath)
	}
}

func (h *Handler) markStatusError(message string) {
	h.setStatus(func(s *exportStatus) {
		s.Running = false
		s.CanCancel = false
		s.CancelRequested = false
		s.Phase = "error"
		s.LastError = message
		s.FinishedAt = time.Now().UTC()
	})
}

func (h *Handler) markStatusCanceledByUser() {
	h.setStatus(func(s *exportStatus) {
		s.Running = false
		s.CanCancel = false
		s.CancelRequested = false
		s.Phase = "canceled"
		s.LastError = "export canceled by user"
		s.FinishedAt = time.Now().UTC()
	})
}

// exportStatus godoc
// @Summary      Статус выгрузки
// @Description  Возвращает текущее состояние процесса выгрузки
// @Tags         Export
// @Produce      json
// @Security     CookieAuth
// @Security     ApiKeyAuth
// @Security     BearerAuth
// @Success      200  {object}  SwaggerExportStatusResponse
// @Failure      401  {object}  SwaggerErrorResponse
// @Router       /api/export/status [get]
func (h *Handler) exportStatus(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	h.mu.Lock()
	status := h.status
	h.mu.Unlock()
	writeAPISuccess(w, http.StatusOK, "export_status", "Текущий статус выгрузки", status, map[string]any{
		"generated_at": time.Now().UTC(),
	})
}

// cancelExport godoc
// @Summary      Отмена активной выгрузки
// @Description  Отправляет запрос на остановку текущей выгрузки
// @Tags         Export
// @Produce      json
// @Security     CookieAuth
// @Security     ApiKeyAuth
// @Security     BearerAuth
// @Success      202  {object}  SwaggerCancelExportResponse
// @Failure      401  {object}  SwaggerErrorResponse
// @Failure      409  {object}  SwaggerErrorResponse
// @Router       /api/export/cancel [post]
func (h *Handler) cancelExport(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	h.mu.Lock()
	cancel := h.fullExportCancel
	if !h.status.Running || cancel == nil {
		h.mu.Unlock()
		writeAPIErrorSimple(w, http.StatusConflict, "NO_EXPORT_RUNNING", "Нет активной выгрузки для отмены", "no export is running")
		return
	}
	alreadyRequested := h.cancelRequestedByUser
	h.cancelRequestedByUser = true
	h.status.CancelRequested = true
	h.mu.Unlock()

	if !alreadyRequested {
		cancel()
		log.Info("full export cancel requested by user")
	}

	writeAPISuccess(w, http.StatusAccepted, "cancel_requested", "Запрос на отмену выгрузки отправлен", model.CancelExportData{CancelRequested: true}, nil)
}

func isCanceledErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "context canceled")
}

// downloadLastExport godoc
// @Summary      Скачать последний сформированный файл
// @Description  Возвращает бинарный файл последней успешной выгрузки
// @Tags         Export
// @Produce      application/vnd.openxmlformats-officedocument.spreadsheetml.sheet
// @Produce      application/vnd.openxmlformats-officedocument.wordprocessingml.document
// @Security     CookieAuth
// @Security     ApiKeyAuth
// @Security     BearerAuth
// @Success      200  {file}    file
// @Failure      401  {object}  SwaggerErrorResponse
// @Failure      404  {object}  SwaggerErrorResponse
// @Router       /api/export/download-last [get]
func (h *Handler) downloadLastExport(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	h.mu.Lock()
	path := strings.TrimSpace(h.lastResultPath)
	filename := h.lastFilename
	contentType := strings.TrimSpace(h.lastContentType)
	h.mu.Unlock()
	if path == "" {
		writeAPIErrorSimple(w, http.StatusNotFound, "EXPORT_FILE_NOT_FOUND", "Готовый файл выгрузки не найден", "no ready export file")
		return
	}
	if strings.TrimSpace(filename) == "" {
		filename = "bitrix_last_export_passport_and_tasks.xlsx"
	}
	if contentType == "" {
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(filename)), ".docx") {
			contentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		} else {
			contentType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		}
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		writeAPIErrorSimple(w, http.StatusNotFound, "EXPORT_FILE_NOT_FOUND", "Готовый файл выгрузки не найден", "no ready export file")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, filename, url.PathEscape(filename)))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		log.WithError(err).Warn("download last export write failed")
	}
}

func detectExportMode(r *http.Request, dealIDs []int) string {
	if r != nil && r.MultipartForm != nil {
		if files := r.MultipartForm.File["file"]; len(files) > 0 {
			return "file"
		}
	}
	if len(dealIDs) > 0 {
		return "ids"
	}
	return "all"
}

func (h *Handler) recordExportAudit(ctx context.Context, rec auth.ExportAuditRecord) {
	if h == nil || h.auth == nil {
		return
	}
	if strings.TrimSpace(rec.UserLogin) == "" {
		rec.UserLogin = "api"
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := h.auth.RecordExportAudit(writeCtx, rec); err != nil {
		log.WithError(err).Warn("failed to persist export audit")
	}
}

func mergeExportStats(a, b service.ExportStats) service.ExportStats {
	a.DealsTotal += b.DealsTotal
	a.DealsWithProject += b.DealsWithProject
	a.DealsWithoutProject += b.DealsWithoutProject
	a.DealsResolveErrors += b.DealsResolveErrors
	a.DealsWithSupport += b.DealsWithSupport
	a.SupportMeasuresTotal += b.SupportMeasuresTotal
	a.ProjectsWithTasks += b.ProjectsWithTasks
	a.ProjectsWithoutTasks += b.ProjectsWithoutTasks
	a.TasksTotal += b.TasksTotal
	a.TaskLoadErrors += b.TaskLoadErrors
	return a
}
