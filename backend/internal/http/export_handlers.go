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
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/models"
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
	req, ok := h.parseExportRequest(w, r)
	if !ok {
		return
	}

	exportStartedAt := time.Now()
	sourceLabel := "unknown"
	exportErrText := ""
	var stats service.ExportStats

	isFullExport := len(req.dealIDs) == 0
	fullExportLocked := false
	scope := h.portalScopeKey(r)
	if isFullExport {
		if !h.tryStartFullExport(scope) {
			writeMappedError(w, errExportAlreadyRunning, "full export is already running; please wait and retry")
			return
		}
		fullExportLocked = true
		defer func() {
			if fullExportLocked {
				h.finishFullExport(scope)
			}
		}()
	}

	exportTimeout := 12 * time.Minute
	if isFullExport {
		exportTimeout = 35 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()
	h.setExportCancel(scope, cancel)
	defer h.clearExportCancel(scope)

	h.setStatus(scope, func(s *exportStatus) {
		s.Running = true
		s.CanCancel = true
		s.CancelRequested = false
		s.Phase = models.PhasePassport
		s.DealsProcessed = 0
		s.DealsTotal = 0
		s.TasksTotal = 0
		s.DealsWithSupport = 0
		s.SupportMeasuresTotal = 0
		s.StartedAt = time.Now().UTC()
		s.FinishedAt = time.Time{}
		s.LastError = ""
	})

	projects, tasks, issues, computedStats, loadedSourceLabel, ok := h.collectExportData(
		ctx,
		w,
		r,
		scope,
		req,
		&exportErrText,
	)
	if !ok {
		return
	}
	stats = computedStats
	sourceLabel = loadedSourceLabel
	for _, issue := range issues {
		log.WithField("issue", issue).Debug("export issue")
	}

	result, contentType, ok := h.buildExportBinary(
		scope,
		w,
		req.exportFormat,
		projects,
		tasks,
		&exportErrText,
	)
	if !ok {
		return
	}

	if fullExportLocked {
		h.finishFullExport(scope)
		fullExportLocked = false
	}
	h.completeExportSuccess(
		w,
		scope,
		req.dealIDs,
		req.exportFormat,
		sourceLabel,
		result,
		contentType,
		issues,
		stats,
		exportStartedAt,
	)
}

func (h *Handler) tryStartFullExport(scope string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.ensureExportStateLocked(scope)
	if st.fullExportBusy {
		if !st.status.Running {
			log.WithFields(log.Fields{
				"scope":         scope,
				"phase":         st.status.Phase,
				"started_at":    st.status.StartedAt,
				"finished_at":   st.status.FinishedAt,
				"last_error":    st.status.LastError,
				"last_file":     st.status.LastFileName,
				"cancel_marked": st.cancelRequestedByUser,
			}).Warn("stale full export lock detected, resetting")
			st.fullExportBusy = false
			st.fullExportCancel = nil
			st.cancelRequestedByUser = false
		}
	}
	if st.fullExportBusy {
		log.WithFields(log.Fields{
			"scope":       scope,
			"phase":       st.status.Phase,
			"started_at":  st.status.StartedAt,
			"cancel_mark": st.cancelRequestedByUser,
		}).Info("full export start rejected: already running")
		return false
	}
	st.fullExportBusy = true
	log.WithFields(log.Fields{"scope": scope, "started_at": time.Now().UTC()}).Info("full export lock acquired")
	return true
}

func (h *Handler) finishFullExport(scope string) {
	h.mu.Lock()
	st := h.ensureExportStateLocked(scope)
	wasBusy := st.fullExportBusy
	st.fullExportBusy = false
	h.mu.Unlock()
	if wasBusy {
		log.WithFields(log.Fields{"scope": scope, "finished_at": time.Now().UTC()}).Info("full export lock released")
	}
}

func (h *Handler) setExportCancel(
	scope string,
	cancel context.CancelFunc) {
	h.mu.Lock()
	st := h.ensureExportStateLocked(scope)
	st.fullExportCancel = cancel
	st.cancelRequestedByUser = false
	h.mu.Unlock()
}

func (h *Handler) clearExportCancel(scope string) {
	h.mu.Lock()
	st := h.ensureExportStateLocked(scope)
	st.fullExportCancel = nil
	st.cancelRequestedByUser = false
	h.mu.Unlock()
}

func (h *Handler) setStatus(scope string, update func(*exportStatus)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.ensureExportStateLocked(scope)
	update(&st.status)
}

func (h *Handler) storeLastResult(
	scope string,
	data []byte,
	filename,
	contentType string) {
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
	st := h.ensureExportStateLocked(scope)
	oldPath := st.lastResultPath
	defer h.mu.Unlock()
	st.lastResultPath = tmpPath
	st.lastFilename = filename
	st.lastContentType = strings.TrimSpace(contentType)
	st.lastUpdatedAt = time.Now().UTC()
	st.status.HasLastResult = true
	st.status.LastFileName = filename
	if strings.TrimSpace(oldPath) != "" && oldPath != tmpPath {
		_ = os.Remove(oldPath)
	}
}

func (h *Handler) markStatusError(scope, message string) {
	h.setStatus(scope, func(s *exportStatus) {
		s.Running = false
		s.CanCancel = false
		s.CancelRequested = false
		s.Phase = models.PhaseError
		s.LastError = message
		s.FinishedAt = time.Now().UTC()
	})
}

func (h *Handler) markStatusCanceledByUser(scope string) {
	h.setStatus(scope, func(s *exportStatus) {
		s.Running = false
		s.CanCancel = false
		s.CancelRequested = false
		s.Phase = models.PhaseCanceled
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
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	scope := h.portalScopeKey(r)
	h.mu.Lock()
	st := h.ensureExportStateLocked(scope)
	status := st.status
	h.mu.Unlock()

	meta := models.ResponseMeta{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	writeAPISuccess(
		w,
		http.StatusOK,
		"export_status",
		"Текущий статус выгрузки",
		mapExportStatusToDTO(status),
		meta,
	)
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
	forceRequested := false
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("force"))) {
	case "1", "true", "yes", "y", "on":
		forceRequested = true
	}

	scope := h.portalScopeKey(r)
	h.mu.Lock()
	st := h.ensureExportStateLocked(scope)
	cancel := st.fullExportCancel
	busy := st.fullExportBusy
	running := st.status.Running
	alreadyRequested := st.cancelRequestedByUser
	phase := st.status.Phase
	active := running || busy || strings.EqualFold(phase, models.PhaseTasks) || strings.EqualFold(phase, models.PhasePassport)
	st.cancelRequestedByUser = true
	st.status.CancelRequested = true

	if forceRequested {
		st.fullExportBusy = false
		st.fullExportCancel = nil
		st.status.Running = false
		st.status.CanCancel = false
		st.status.CancelRequested = false
		st.status.Phase = models.PhaseCanceled
		st.status.LastError = "export canceled by user (forced)"
		st.status.FinishedAt = time.Now().UTC()
		h.mu.Unlock()

		if cancel != nil {
			cancel()
		}
		log.WithFields(log.Fields{
			"scope":  scope,
			"phase":  phase,
			"active": active,
		}).Warn("full export force-cancel requested by user")
		writeAPISuccess(
			w,
			http.StatusAccepted,
			"cancel_requested",
			"Принудительная отмена выгрузки выполнена",
			models.CancelExportData{CancelRequested: true},
			nil,
		)
		return
	}

	if busy && !running && cancel == nil {
		st.fullExportBusy = false
		st.status.Running = false
		st.status.CanCancel = false
		st.status.Phase = models.PhaseCanceled
		st.status.LastError = "export canceled by user (forced reset)"
		st.status.FinishedAt = time.Now().UTC()
		log.WithFields(log.Fields{
			"scope": scope,
			"phase": phase,
		}).Warn("forced full export lock reset on cancel request")
	}
	h.mu.Unlock()

	if cancel != nil && !alreadyRequested {
		cancel()
		log.WithField("scope", scope).Info("full export cancel requested by user")
	}

	message := "Запрос на отмену выгрузки отправлен"
	if !active && !alreadyRequested {
		message = "Активная выгрузка не обнаружена, состояние отмены зафиксировано"
	}
	writeAPISuccess(
		w,
		http.StatusAccepted,
		"cancel_requested",
		message,
		models.CancelExportData{CancelRequested: true},
		nil,
	)
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
	scope := h.portalScopeKey(r)
	h.mu.Lock()
	st := h.ensureExportStateLocked(scope)
	path := strings.TrimSpace(st.lastResultPath)
	filename := st.lastFilename
	contentType := strings.TrimSpace(st.lastContentType)
	h.mu.Unlock()
	if path == "" {
		writeMappedError(
			w,
			errExportFileNotFound,
			"no ready export file",
		)
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
		writeMappedError(
			w,
			errExportFileNotFound,
			"no ready export file",
		)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(
			`attachment; filename="%s"; filename*=UTF-8''%s`,
			filename, url.PathEscape(filename)),
	)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		log.WithError(err).Warn("download last export write failed")
		return
	}
	if h.cfg.DeleteLastExportAfterDownload {
		h.consumeLastResult(scope, path)
	}
}

func (h *Handler) consumeLastResult(scope, path string) {
	cleanPath := strings.TrimSpace(path)
	if cleanPath == "" {
		return
	}

	h.mu.Lock()
	st := h.ensureExportStateLocked(scope)
	if st.lastResultPath != cleanPath {
		h.mu.Unlock()
		return
	}
	st.lastResultPath = ""
	st.lastFilename = ""
	st.lastContentType = ""
	st.lastUpdatedAt = time.Time{}
	st.status.HasLastResult = false
	st.status.LastFileName = ""
	h.mu.Unlock()

	if err := os.Remove(cleanPath); err != nil && !os.IsNotExist(err) {
		log.WithError(err).WithField("path", cleanPath).Warn("failed to remove consumed last export file")
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

func mapExportStatusToDTO(s exportStatus) models.ExportStatusData {
	return models.ExportStatusData{
		Running:              s.Running,
		CanCancel:            s.CanCancel,
		CancelRequested:      s.CancelRequested,
		Phase:                s.Phase,
		DealsProcessed:       s.DealsProcessed,
		DealsTotal:           s.DealsTotal,
		TasksTotal:           s.TasksTotal,
		DealsWithSupport:     s.DealsWithSupport,
		SupportMeasuresTotal: s.SupportMeasuresTotal,
		HasLastResult:        s.HasLastResult,
		LastFileName:         s.LastFileName,
		StartedAt:            formatTimeUTC(s.StartedAt),
		FinishedAt:           formatTimeUTC(s.FinishedAt),
		LastError:            s.LastError,
	}
}

func formatTimeUTC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
