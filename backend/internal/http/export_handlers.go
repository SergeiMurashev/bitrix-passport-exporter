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
	if isFullExport {
		if !h.tryStartFullExport() {
			writeMappedError(w, errExportAlreadyRunning, "full export is already running; please wait and retry")
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

	projects, tasks, issues, computedStats, loadedSourceLabel, ok := h.collectExportData(ctx, w, r, req, &exportErrText)
	if !ok {
		return
	}
	stats = computedStats
	sourceLabel = loadedSourceLabel
	for _, issue := range issues {
		log.WithField("issue", issue).Debug("export issue")
	}

	result, contentType, ok := h.buildExportBinary(w, req.exportFormat, projects, tasks, &exportErrText)
	if !ok {
		return
	}

	if fullExportLocked {
		h.finishFullExport()
		fullExportLocked = false
	}
	h.completeExportSuccess(w, req.dealIDs, req.exportFormat, sourceLabel, result, contentType, issues, stats, exportStartedAt)
}

func (h *Handler) tryStartFullExport() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fullExportBusy {
		// Сеть безопасности: флаг занятости всегда должен соответствовать активному статусу.
		// Если статус не работает, блокировка устарела и ее можно восстановить.
		if !h.status.Running {
			log.WithFields(log.Fields{
				"phase":         h.status.Phase,
				"started_at":    h.status.StartedAt,
				"finished_at":   h.status.FinishedAt,
				"last_error":    h.status.LastError,
				"last_file":     h.status.LastFileName,
				"cancel_marked": h.cancelRequestedByUser,
			}).Warn("stale full export lock detected, resetting")
			h.fullExportBusy = false
			h.fullExportCancel = nil
			h.cancelRequestedByUser = false
		}
	}
	if h.fullExportBusy {
		log.WithFields(log.Fields{
			"phase":       h.status.Phase,
			"started_at":  h.status.StartedAt,
			"cancel_mark": h.cancelRequestedByUser,
		}).Info("full export start rejected: already running")
		return false
	}
	h.fullExportBusy = true
	log.WithField("started_at", time.Now().UTC()).Info("full export lock acquired")
	return true
}

func (h *Handler) finishFullExport() {
	h.mu.Lock()
	wasBusy := h.fullExportBusy
	h.fullExportBusy = false
	h.mu.Unlock()
	if wasBusy {
		log.WithField("finished_at", time.Now().UTC()).Info("full export lock released")
	}
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
	h.mu.Lock()
	cancel := h.fullExportCancel
	if !h.status.Running || cancel == nil {
		h.mu.Unlock()
		writeMappedError(w, errNoExportRunning, "no export is running")
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

	writeAPISuccess(
		w,
		http.StatusAccepted,
		"cancel_requested",
		"Запрос на отмену выгрузки отправлен",
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
	h.mu.Lock()
	path := strings.TrimSpace(h.lastResultPath)
	filename := h.lastFilename
	contentType := strings.TrimSpace(h.lastContentType)
	h.mu.Unlock()
	if path == "" {
		writeMappedError(w, errExportFileNotFound, "no ready export file")
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
		writeMappedError(w, errExportFileNotFound, "no ready export file")
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
