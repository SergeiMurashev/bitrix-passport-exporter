package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/export"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/parser"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/service"
	log "github.com/sirupsen/logrus"
)

type Handler struct {
	cfg            config.Config
	mu             sync.Mutex
	fullExportBusy bool
	status         exportStatus
	lastResult     []byte
	lastFilename   string
	lastUpdatedAt  time.Time
}

const projectFieldCode = "UF_CRM_PROJECT_GROUP_ID"

type exportStatus struct {
	Running        bool      `json:"running"`
	Phase          string    `json:"phase"`
	DealsProcessed int       `json:"deals_processed"`
	DealsTotal     int       `json:"deals_total"`
	TasksTotal     int       `json:"tasks_total"`
	HasLastResult  bool      `json:"has_last_result"`
	LastFileName   string    `json:"last_file_name,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
}

func New(cfg config.Config) *Handler {
	return &Handler{
		cfg: cfg,
		status: exportStatus{
			Phase: "idle",
		},
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.healthz)
	logRoute("GET", "/healthz", "Handler.healthz", 0)
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(frontendDistDir(), "assets")))))
	logRoute("GET", "/assets/*", "http.FileServer", 0)
	mux.Handle("/logo1.png", http.FileServer(http.Dir(frontendDistDir())))
	logRoute("GET", "/logo1.png", "http.FileServer", 0)
	mux.HandleFunc("/", h.ui)
	logRoute("GET", "/", "Handler.ui", 0)
	// Сервисные вызовы
	mux.HandleFunc("/api/deals/ids", h.dealIDs)
	logRoute("GET", "/api/deals/ids", "Handler.dealIDs", 0)
	mux.HandleFunc("/api/deals/fields", h.dealFields)
	logRoute("GET", "/api/deals/fields", "Handler.dealFields", 0)
	mux.HandleFunc("/api/export/status", h.exportStatus)
	logRoute("GET", "/api/export/status", "Handler.exportStatus", 0)
	mux.HandleFunc("/api/export/download-last", h.downloadLastExport)
	logRoute("GET", "/api/export/download-last", "Handler.downloadLastExport", 0)
	// Основной вызов
	mux.HandleFunc("/api/export", h.export)
	logRoute("POST", "/api/export", "Handler.export", 0)
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) ui(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	indexPath := filepath.Join(frontendDistDir(), "index.html")
	body, err := os.ReadFile(indexPath)
	if err != nil {
		http.Error(w, "frontend is not built; run frontend build", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			http.Error(w, "invalid multipart form: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		http.Error(w, "server is not configured: BITRIX_WEBHOOK_URL is empty", http.StatusInternalServerError)
		return
	}
	projectField := projectFieldCode

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		http.Error(w, "invalid webhook: "+err.Error(), http.StatusBadRequest)
		return
	}

	dealIDs, err := parseDealIDs(r.FormValue("deal_ids"), r.FormValue("deal_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	isFullExport := len(dealIDs) == 0
	fullExportLocked := false
	if isFullExport {
		if !h.tryStartFullExport() {
			http.Error(w, "full export is already running; please wait and retry", http.StatusTooManyRequests)
			return
		}
		fullExportLocked = true
		defer func() {
			if fullExportLocked {
				h.finishFullExport()
			}
		}()
		h.setStatus(func(s *exportStatus) {
			s.Running = true
			s.Phase = "passport"
			s.DealsProcessed = 0
			s.DealsTotal = 0
			s.TasksTotal = 0
			s.StartedAt = time.Now().UTC()
			s.FinishedAt = time.Time{}
			s.LastError = ""
		})
	}

	projects, sourceLabel, err := h.loadProjects(r, bClient, dealIDs)
	if err != nil {
		if isFullExport {
			h.markStatusError(err.Error())
		}
		log.WithError(err).WithField("deal_ids", dealIDs).Error("export load projects failed")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(projects) == 0 && sourceLabel != "bitrix_api" {
		http.Error(w, "no projects found", http.StatusBadRequest)
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
	exportTimeout := 12 * time.Minute
	allowTitleFallback := true
	if isFullBitrixExport {
		// Полный экспорт по тысячам сделок: даем больше времени и отключаем дорогой fallback поиска проекта по названию.
		exportTimeout = 35 * time.Minute
		allowTitleFallback = false
	}
	// Не привязывайте длинный экспорт к запросу отмены из браузера/клиента.
	// В противном случае прерывания загрузки/выгрузки отменяют вызовы Битрикса в процессе выполнения.
	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()
	var (
		tasks  []model.TaskRow
		stats  service.ExportStats
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
				h.markStatusError("failed to load deals page: " + pageErr.Error())
				http.Error(w, "failed to load deals page: "+pageErr.Error(), http.StatusInternalServerError)
				return
			}
			if len(page.Rows) == 0 {
				break
			}
			allProjects = append(allProjects, page.Rows...)
			processed += len(page.Rows)
			log.WithFields(log.Fields{
				"phase":           "passport",
				"deals_processed": processed,
				"next":            page.Next,
			}).Info("export progress")
			h.setStatus(func(s *exportStatus) {
				s.Phase = "passport"
				s.DealsProcessed = processed
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
				h.markStatusError("failed to collect tasks: " + allErr.Error())
				if strings.Contains(allErr.Error(), "webhook auth failed") {
					http.Error(w, "bitrix webhook is invalid or expired", http.StatusBadGateway)
					return
				}
				http.Error(w, "failed to collect tasks: "+allErr.Error(), http.StatusInternalServerError)
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
					h.markStatusError("failed to collect tasks: " + chunkErr.Error())
					if strings.Contains(chunkErr.Error(), "webhook auth failed") {
						http.Error(w, "bitrix webhook is invalid or expired", http.StatusBadGateway)
						return
					}
					http.Error(w, "failed to collect tasks: "+chunkErr.Error(), http.StatusInternalServerError)
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
		var callErr error
		tasks, stats, issues, callErr = svc.BuildTasks(ctx, projects, projectField, allowTitleFallback)
		if callErr != nil {
			if strings.Contains(callErr.Error(), "webhook auth failed") {
				http.Error(w, "bitrix webhook is invalid or expired", http.StatusBadGateway)
				return
			}
			http.Error(w, "failed to collect tasks: "+callErr.Error(), http.StatusInternalServerError)
			return
		}
	}
	log.WithFields(log.Fields{
		"deals_total":            stats.DealsTotal,
		"deals_with_project":     stats.DealsWithProject,
		"deals_without_project":  stats.DealsWithoutProject,
		"resolve_errors":         stats.DealsResolveErrors,
		"projects_with_tasks":    stats.ProjectsWithTasks,
		"projects_without_tasks": stats.ProjectsWithoutTasks,
		"task_load_errors":       stats.TaskLoadErrors,
		"tasks_total":            stats.TasksTotal,
	}).Info("export stats")
	for _, issue := range issues {
		log.WithField("issue", issue).Warn("export issue")
	}

	result, err := export.BuildResultXLSX(projects, tasks)
	if err != nil {
		if isFullExport {
			h.markStatusError("failed to build xlsx: " + err.Error())
		}
		http.Error(w, "failed to build xlsx: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Unlock before writing response, so long file transfer does not block next run.
	if fullExportLocked {
		h.finishFullExport()
		fullExportLocked = false
	}
	filename := buildDownloadFilename(sourceLabel, dealIDs)
	h.storeLastResult(result, filename)
	if isFullExport {
		h.setStatus(func(s *exportStatus) {
			s.Running = false
			s.Phase = "idle"
			s.FinishedAt = time.Now().UTC()
			s.LastError = ""
		})
	}
	w.Header().Set("X-Export-Success", "true")
	w.Header().Set("X-Export-Source", sourceLabel)
	w.Header().Set("X-Export-Deals-Total", strconv.Itoa(stats.DealsTotal))
	w.Header().Set("X-Export-Tasks-Total", strconv.Itoa(stats.TasksTotal))
	w.Header().Set("X-Export-Issues-Count", strconv.Itoa(len(issues)))
	w.Header().Set("X-Export-File-Name", filename)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, filename, url.PathEscape(filename)))
	w.WriteHeader(http.StatusOK)
	if _, writeErr := w.Write(result); writeErr != nil {
		log.WithError(writeErr).Warn("export response write failed")
	}
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

func (h *Handler) setStatus(update func(*exportStatus)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	update(&h.status)
}

func (h *Handler) storeLastResult(data []byte, filename string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastResult = append([]byte(nil), data...)
	h.lastFilename = filename
	h.lastUpdatedAt = time.Now().UTC()
	h.status.HasLastResult = true
	h.status.LastFileName = filename
}

func (h *Handler) markStatusError(message string) {
	h.setStatus(func(s *exportStatus) {
		s.Running = false
		s.Phase = "error"
		s.LastError = message
		s.FinishedAt = time.Now().UTC()
	})
}

func (h *Handler) exportStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.mu.Lock()
	status := h.status
	h.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(status)
}

func (h *Handler) downloadLastExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.mu.Lock()
	data := append([]byte(nil), h.lastResult...)
	filename := h.lastFilename
	h.mu.Unlock()
	if len(data) == 0 {
		http.Error(w, "no ready export file", http.StatusNotFound)
		return
	}
	if strings.TrimSpace(filename) == "" {
		filename = "bitrix_last_export_passport_and_tasks.xlsx"
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, filename, url.PathEscape(filename)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		log.WithError(err).Warn("download last export write failed")
	}
}

func mergeExportStats(a, b service.ExportStats) service.ExportStats {
	a.DealsTotal += b.DealsTotal
	a.DealsWithProject += b.DealsWithProject
	a.DealsWithoutProject += b.DealsWithoutProject
	a.DealsResolveErrors += b.DealsResolveErrors
	a.ProjectsWithTasks += b.ProjectsWithTasks
	a.ProjectsWithoutTasks += b.ProjectsWithoutTasks
	a.TasksTotal += b.TasksTotal
	a.TaskLoadErrors += b.TaskLoadErrors
	return a
}

func (h *Handler) dealIDs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		http.Error(w, "server is not configured: BITRIX_WEBHOOK_URL is empty", http.StatusInternalServerError)
		return
	}

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		http.Error(w, "invalid webhook: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	deals, err := bClient.ListDealIDs(ctx)
	if err != nil {
		log.WithError(err).Error("deal ids load failed")
		http.Error(w, "failed to load deal ids: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count": len(deals),
		"deals": deals,
	})
}

func (h *Handler) dealFields(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		http.Error(w, "server is not configured: BITRIX_WEBHOOK_URL is empty", http.StatusInternalServerError)
		return
	}
	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		http.Error(w, "invalid webhook: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fields, err := bClient.ListDealFields(ctx)
	if err != nil {
		log.WithError(err).Error("deal fields load failed")
		http.Error(w, "failed to load deal fields: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count":  len(fields),
		"fields": fields,
	})
}

func (h *Handler) loadProjects(r *http.Request, bClient *bitrix.Client, dealIDs []int) ([]model.ProjectRow, string, error) {
	file, fh, err := r.FormFile("file")
	if err == nil {
		defer file.Close()
		projects, parseErr := parser.ParseDealsInput(file)
		if parseErr != nil {
			return nil, "", fmt.Errorf("failed to parse input: %w", parseErr)
		}
		return projects, fh.Filename, nil
	}

	if err != nil && err != http.ErrMissingFile {
		return nil, "", fmt.Errorf("invalid file field: %w", err)
	}
	if len(dealIDs) == 0 {
		// Для полного экспорта сделки будут загружены чанками в основном обработчике.
		return nil, "bitrix_api", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	projects, err := bClient.GetDealsByIDs(ctx, dealIDs)
	if err != nil {
		if bitrix.IsAuthError(err) {
			return nil, "", fmt.Errorf("bitrix webhook is invalid or expired")
		}
		return nil, "", fmt.Errorf("failed to load deals from bitrix: %w", err)
	}
	label := "bitrix_api"
	if len(dealIDs) == 1 {
		label = fmt.Sprintf("bitrix_deal_%d", dealIDs[0])
	}
	if len(dealIDs) > 1 {
		label = fmt.Sprintf("bitrix_deals_%d", len(dealIDs))
	}
	return projects, label, nil
}

func parseDealIDs(dealIDsRaw, dealIDRaw string) ([]int, error) {
	parts := make([]string, 0, 8)
	if strings.TrimSpace(dealIDsRaw) != "" {
		parts = append(parts, strings.Split(dealIDsRaw, ",")...)
	} else if strings.TrimSpace(dealIDRaw) != "" {
		parts = append(parts, strings.TrimSpace(dealIDRaw))
	}
	if len(parts) == 0 {
		return nil, nil
	}
	seen := map[int]struct{}{}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		id, err := strconv.Atoi(s)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("deal_ids must contain positive integers, got %q", s)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func buildDownloadFilename(src string, dealIDs []int) string {
	base := strings.TrimSpace(src)
	base = strings.TrimSuffix(base, ".xlsx")
	base = strings.TrimSuffix(base, ".xls")
	base = strings.TrimSuffix(base, ".html")
	base = sanitizeASCII(base)
	if base == "" {
		base = "bitrix_export"
	}
	if len(dealIDs) == 0 && strings.EqualFold(src, "bitrix_api") {
		base = "bitrix_all_deals"
	}
	if len(dealIDs) == 1 {
		base = fmt.Sprintf("deal_%d", dealIDs[0])
	}
	if len(dealIDs) > 1 {
		base = fmt.Sprintf("deals_%d", len(dealIDs))
	}
	return base + "_passport_and_tasks.xlsx"
}

func sanitizeASCII(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	re := regexp.MustCompile(`[^a-z0-9._-]+`)
	s = re.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._-")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func frontendDistDir() string {
	return filepath.Clean(filepath.Join("..", "frontend", "dist"))
}

func logRoute(method, path, handler string, middlewares int) {
	log.Infof("[HTTP-debug] %-6s %s --> %s (%d handlers)", method, path, handler, middlewares+1)
}
