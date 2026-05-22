package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/auth"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/export"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/parser"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/service"
	log "github.com/sirupsen/logrus"
)

type Handler struct {
	cfg                   config.Config
	auth                  *auth.Manager
	loginLimiter          *rateWindowLimiter
	exportLimiter         *rateWindowLimiter
	mu                    sync.Mutex
	fullExportBusy        bool
	fullExportCancel      context.CancelFunc
	cancelRequestedByUser bool
	status                exportStatus
	lastResultPath        string
	lastFilename          string
	lastContentType       string
	lastUpdatedAt         time.Time
}

const projectFieldCode = "UF_CRM_PROJECT_GROUP_ID"

type exportStatus struct {
	Running              bool      `json:"running"`
	CanCancel            bool      `json:"can_cancel"`
	CancelRequested      bool      `json:"cancel_requested"`
	Phase                string    `json:"phase"`
	DealsProcessed       int       `json:"deals_processed"`
	DealsTotal           int       `json:"deals_total"`
	TasksTotal           int       `json:"tasks_total"`
	DealsWithSupport     int       `json:"deals_with_support"`
	SupportMeasuresTotal int       `json:"support_measures_total"`
	HasLastResult        bool      `json:"has_last_result"`
	LastFileName         string    `json:"last_file_name,omitempty"`
	StartedAt            time.Time `json:"started_at,omitempty"`
	FinishedAt           time.Time `json:"finished_at,omitempty"`
	LastError            string    `json:"last_error,omitempty"`
}

type apiErrorPayload struct {
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type apiResponse struct {
	OK      bool             `json:"ok"`
	Status  string           `json:"status"`
	Message string           `json:"message,omitempty"`
	Data    any              `json:"data,omitempty"`
	Error   *apiErrorPayload `json:"error,omitempty"`
	Meta    map[string]any   `json:"meta,omitempty"`
}

func writeAPIResponse(w http.ResponseWriter, httpCode int, body apiResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(body)
}

func writeAPISuccess(w http.ResponseWriter, httpCode int, status, message string, data any, meta map[string]any) {
	if strings.TrimSpace(status) == "" {
		status = "success"
	}
	writeAPIResponse(w, httpCode, apiResponse{
		OK:      true,
		Status:  status,
		Message: strings.TrimSpace(message),
		Data:    data,
		Meta:    meta,
	})
}

func writeAPIError(w http.ResponseWriter, httpCode int, status, message, code, detail string, meta map[string]any) {
	if strings.TrimSpace(status) == "" {
		status = "error"
	}
	writeAPIResponse(w, httpCode, apiResponse{
		OK:      false,
		Status:  status,
		Message: strings.TrimSpace(message),
		Error: &apiErrorPayload{
			Code:   strings.TrimSpace(code),
			Detail: strings.TrimSpace(detail),
		},
		Meta: meta,
	})
}

func New(cfg config.Config) (*Handler, error) {
	var authManager *auth.Manager
	if cfg.AuthEnabled {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		authManager, err = auth.New(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("init auth manager: %w", err)
		}
	}
	return &Handler{
		cfg:          cfg,
		auth:         authManager,
		loginLimiter: newRateWindowLimiter(cfg.RateLimitLoginPerMinute, time.Minute),
		exportLimiter: newRateWindowLimiter(
			cfg.RateLimitExportPerMinute,
			time.Minute,
		),
		status: exportStatus{
			Phase: "idle",
		},
	}, nil
}

func (h *Handler) Close() error {
	if h == nil || h.auth == nil {
		return nil
	}
	return h.auth.Close()
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.healthz)
	logRoute("GET", "/healthz", "Handler.healthz", 0)
	mux.HandleFunc("/readyz", h.readyz)
	logRoute("GET", "/readyz", "Handler.readyz", 0)
	guard := h.withAccessControl
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(frontendDistDir(), "assets")))))
	logRoute("GET", "/assets/*", "http.FileServer", 0)
	mux.Handle("/invest-agent-logo-dark.png", http.FileServer(http.Dir(frontendDistDir())))
	logRoute("GET", "/invest-agent-logo-dark.png", "http.FileServer", 0)
	mux.Handle("/invest-agent-logo-light.png", http.FileServer(http.Dir(frontendDistDir())))
	logRoute("GET", "/invest-agent-logo-light.png", "http.FileServer", 0)
	mux.Handle("/logo1.png", http.FileServer(http.Dir(frontendDistDir())))
	logRoute("GET", "/logo1.png", "http.FileServer", 0)
	mux.Handle("/", http.HandlerFunc(h.ui))
	logRoute("GET", "/", "Handler.ui", 0)
	mux.HandleFunc("/api/auth/login", h.authLogin)
	logRoute("POST", "/api/auth/login", "Handler.authLogin", 0)
	mux.Handle("/api/auth/me", guard(http.HandlerFunc(h.authMe)))
	logRoute("GET", "/api/auth/me", "Handler.authMe", 0)
	mux.Handle("/api/auth/logout", guard(http.HandlerFunc(h.authLogout)))
	logRoute("POST", "/api/auth/logout", "Handler.authLogout", 0)
	// Сервисные вызовы
	mux.Handle("/api/deals/ids", guard(http.HandlerFunc(h.dealIDs)))
	logRoute("GET", "/api/deals/ids", "Handler.dealIDs", 0)
	mux.Handle("/api/deals/fields", guard(http.HandlerFunc(h.dealFields)))
	logRoute("GET", "/api/deals/fields", "Handler.dealFields", 0)
	mux.Handle("/api/export/status", guard(http.HandlerFunc(h.exportStatus)))
	logRoute("GET", "/api/export/status", "Handler.exportStatus", 0)
	mux.Handle("/api/export/download-last", guard(http.HandlerFunc(h.downloadLastExport)))
	logRoute("GET", "/api/export/download-last", "Handler.downloadLastExport", 0)
	mux.Handle("/api/export/cancel", guard(http.HandlerFunc(h.cancelExport)))
	logRoute("POST", "/api/export/cancel", "Handler.cancelExport", 0)
	// Основной вызов
	mux.Handle("/api/export", guard(http.HandlerFunc(h.export)))
	logRoute("POST", "/api/export", "Handler.export", 0)
}

func (h *Handler) withAccessControl(next http.Handler) http.Handler {
	token := strings.TrimSpace(h.cfg.APIAccessToken)
	useToken := token != ""
	useAuth := h.auth != nil
	if !useToken && !useAuth {
		return next
	}

	unauthorized := func(w http.ResponseWriter) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Требуется авторизация", "AUTH_REQUIRED", "unauthorized", nil)
	}

	tokenAllowed := func(r *http.Request) bool {
		candidates := []string{
			strings.TrimSpace(r.Header.Get("X-API-Key")),
			strings.TrimSpace(r.Header.Get("X-Access-Token")),
		}
		if c, err := r.Cookie("bp_api_token"); err == nil {
			candidates = append(candidates, strings.TrimSpace(c.Value))
		}
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			candidates = append(candidates, strings.TrimSpace(authHeader[7:]))
		}
		for _, c := range candidates {
			if c == "" {
				continue
			}
			if subtle.ConstantTimeCompare([]byte(c), []byte(token)) == 1 {
				return true
			}
		}
		return false
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if useToken && tokenAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		if useAuth {
			if _, ok := h.sessionClaimsFromRequest(r); ok {
				next.ServeHTTP(w, r)
				return
			}
		}
		unauthorized(w)
	})
}

func (h *Handler) sessionClaimsFromRequest(r *http.Request) (*auth.Claims, bool) {
	if h.auth == nil {
		return nil, false
	}
	candidates := make([]string, 0, 2)
	if c, err := r.Cookie(auth.CookieName()); err == nil {
		candidates = append(candidates, strings.TrimSpace(c.Value))
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		candidates = append(candidates, strings.TrimSpace(authHeader[7:]))
	}
	for _, t := range candidates {
		if t == "" {
			continue
		}
		claims, err := h.auth.ParseToken(t)
		if err == nil && claims != nil {
			return claims, true
		}
	}
	return nil, false
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	ttl := int(time.Until(expiresAt).Seconds())
	if ttl <= 0 {
		ttl = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName(),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		MaxAge:   ttl,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName(),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		MaxAge:   -1,
	})
}

func (h *Handler) authLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается", "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	if h.loginLimiter != nil && !h.loginLimiter.Allow(clientIPFromRequest(r)) {
		writeAPIError(w, http.StatusTooManyRequests, "rate_limited", "Слишком много попыток входа", "LOGIN_RATE_LIMITED", "too many login attempts, try again later", nil)
		return
	}
	if h.auth == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "auth_disabled", "Авторизация отключена на сервере", "AUTH_DISABLED", "auth is disabled", nil)
		return
	}

	var in struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "Некорректный JSON в теле запроса", "INVALID_JSON", "invalid json body", nil)
		return
	}
	login := strings.TrimSpace(in.Login)
	if login == "" || strings.TrimSpace(in.Password) == "" {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "Логин и пароль обязательны", "AUTH_REQUIRED_FIELDS", "login and password are required", nil)
		return
	}

	user, err := h.auth.Authenticate(r.Context(), login, in.Password)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Неверный логин или пароль", "INVALID_CREDENTIALS", "invalid credentials", nil)
		return
	}
	token, expiresAt, err := h.auth.IssueToken(user)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "Не удалось создать сессию", "TOKEN_ISSUE_FAILED", "failed to issue token", nil)
		return
	}
	h.setSessionCookie(w, r, token, expiresAt)
	w.Header().Set("Cache-Control", "no-store")
	writeAPISuccess(w, http.StatusOK, "authorized", "Вход выполнен", map[string]any{
		"expires_at": expiresAt.UTC(),
		"session":    "cookie",
		"user":       user,
	}, nil)
}

func (h *Handler) authMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается", "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	if h.auth == nil {
		writeAPISuccess(w, http.StatusOK, "authorized", "Сессия активна", map[string]any{
			"user": map[string]any{
				"id":    0,
				"login": "system",
			},
		}, nil)
		return
	}
	claims, ok := h.sessionClaimsFromRequest(r)
	if !ok || claims == nil {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Сессия отсутствует или истекла", "AUTH_REQUIRED", "unauthorized", nil)
		return
	}
	writeAPISuccess(w, http.StatusOK, "authorized", "Сессия активна", map[string]any{
		"user": map[string]any{
			"id":    claims.UserID,
			"login": claims.Login,
		},
		"expires_at": claims.ExpiresAt.Time.UTC(),
	}, nil)
}

func (h *Handler) authLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается", "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	h.clearSessionCookie(w, r)
	writeAPISuccess(w, http.StatusOK, "logged_out", "Выход выполнен", map[string]any{"logged_out": true}, nil)
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) readyz(w http.ResponseWriter, _ *http.Request) {
	if h.auth != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := h.auth.Ready(ctx); err != nil {
			http.Error(w, "not ready: auth db unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	_, _ = w.Write([]byte("ready"))
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
	if token := strings.TrimSpace(h.cfg.APIAccessToken); token != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "bp_api_token",
			Value:    token,
			Path:     "/api",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   r.TLS != nil,
			MaxAge:   30 * 24 * 60 * 60,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clientIP := clientIPFromRequest(r)
	if h.exportLimiter != nil && !h.exportLimiter.Allow(clientIP) {
		http.Error(w, "too many export requests, try again later", http.StatusTooManyRequests)
		return
	}
	reqContentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.Contains(reqContentType, "multipart/form-data") {
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
	bClient.ConfigureSupportMapping(h.cfg.SupportLinkDealField, h.cfg.SupportMeasureValueField)

	dealIDs, err := parseDealIDs(r.FormValue("deal_ids"), r.FormValue("deal_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	exportFormat, err := parseExportFormat(r.FormValue("format"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
			http.Error(w, "full export is already running; please wait and retry", http.StatusTooManyRequests)
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
		// Полный экспорт по тысячам сделок: даем больше времени.
		exportTimeout = 35 * time.Minute
	}
	// Не привязывайте длинный экспорт к запросу отмены из браузера/клиента.
	// В противном случае прерывания загрузки/выгрузки отменяют вызовы Битрикса в процессе выполнения.
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
			http.Error(w, "export canceled by user", http.StatusConflict)
			return
		}
		h.markStatusError(err.Error())
		auditErrText = err.Error()
		log.WithError(err).WithField("deal_ids", dealIDs).Error("export load projects failed")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sourceLabel = sourceLabelValue
	if len(projects) == 0 && sourceLabel != "bitrix_api" {
		h.markStatusError("no projects found")
		auditErrText = "no projects found"
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
	allowTitleFallback := true
	if isFullBitrixExport {
		// Полный экспорт по тысячам сделок: отключаем дорогой fallback поиска проекта по названию.
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
					http.Error(w, "export canceled by user", http.StatusConflict)
					return
				}
				h.markStatusError("failed to load deals page: " + pageErr.Error())
				http.Error(w, "failed to load deals page: "+pageErr.Error(), http.StatusInternalServerError)
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
					http.Error(w, "export canceled by user", http.StatusConflict)
					return
				}
				auditErrText = "failed to collect tasks: " + allErr.Error()
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
					if isCanceledErr(chunkErr) {
						h.markStatusCanceledByUser()
						auditErrText = "export canceled by user"
						http.Error(w, "export canceled by user", http.StatusConflict)
						return
					}
					auditErrText = "failed to collect tasks: " + chunkErr.Error()
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
				http.Error(w, "export canceled by user", http.StatusConflict)
				return
			}
			auditErrText = "failed to collect tasks: " + callErr.Error()
			h.markStatusError("failed to collect tasks: " + callErr.Error())
			if strings.Contains(callErr.Error(), "webhook auth failed") {
				http.Error(w, "bitrix webhook is invalid or expired", http.StatusBadGateway)
				return
			}
			http.Error(w, "failed to collect tasks: "+callErr.Error(), http.StatusInternalServerError)
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
		http.Error(w, "failed to build export file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Unlock before writing response, so long file transfer does not block next run.
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
	writeAPISuccess(w, http.StatusOK, "export_completed", "Выгрузка завершена, файл готов к скачиванию", map[string]any{
		"file_name":    filename,
		"content_type": contentType,
		"size_bytes":   len(result),
		"download_url": "/api/export/download-last",
		"format":       exportFormat,
		"source":       sourceLabel,
		"issues_count": len(issues),
		"stats": map[string]any{
			"deals_total":            stats.DealsTotal,
			"tasks_total":            stats.TasksTotal,
			"deals_with_support":     stats.DealsWithSupport,
			"support_measures_total": stats.SupportMeasuresTotal,
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

func (h *Handler) exportStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается", "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	h.mu.Lock()
	status := h.status
	h.mu.Unlock()
	writeAPISuccess(w, http.StatusOK, "export_status", "Текущий статус выгрузки", status, map[string]any{
		"generated_at": time.Now().UTC(),
	})
}

func (h *Handler) cancelExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается", "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	h.mu.Lock()
	cancel := h.fullExportCancel
	if !h.status.Running || cancel == nil {
		h.mu.Unlock()
		writeAPIError(w, http.StatusConflict, "no_running_export", "Нет активной выгрузки для отмены", "NO_EXPORT_RUNNING", "no export is running", nil)
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

	writeAPISuccess(w, http.StatusAccepted, "cancel_requested", "Запрос на отмену выгрузки отправлен", map[string]any{
		"cancel_requested": true,
	}, nil)
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

func (h *Handler) downloadLastExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.mu.Lock()
	path := strings.TrimSpace(h.lastResultPath)
	filename := h.lastFilename
	contentType := strings.TrimSpace(h.lastContentType)
	h.mu.Unlock()
	if path == "" {
		http.Error(w, "no ready export file", http.StatusNotFound)
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
		http.Error(w, "no ready export file", http.StatusNotFound)
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

func userIDFromClaims(claims *auth.Claims) int64 {
	if claims == nil {
		return 0
	}
	return claims.UserID
}

func userLoginFromClaims(claims *auth.Claims) string {
	if claims == nil {
		return ""
	}
	return strings.TrimSpace(claims.Login)
}

func clientIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
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

func collectSupportStats(projects []model.ProjectRow) (int, int) {
	dealsWithSupport := 0
	measuresTotal := 0
	for _, p := range projects {
		support := strings.TrimSpace(p.Support)
		if support == "" {
			continue
		}
		dealsWithSupport++
		measuresTotal += countSupportItems(support)
	}
	return dealsWithSupport, measuresTotal
}

func countSupportItems(s string) int {
	lines := strings.Split(s, "\n")
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func (h *Handler) dealIDs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается", "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}

	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		writeAPIError(w, http.StatusInternalServerError, "server_not_configured", "Сервер не настроен: не указан webhook Bitrix24", "WEBHOOK_EMPTY", "server is not configured: BITRIX_WEBHOOK_URL is empty", nil)
		return
	}

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_webhook", "Некорректный webhook Bitrix24", "WEBHOOK_INVALID", "invalid webhook: "+err.Error(), nil)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	deals, err := bClient.ListDealIDs(ctx)
	if err != nil {
		log.WithError(err).Error("deal ids load failed")
		writeAPIError(w, http.StatusBadGateway, "bitrix_error", "Не удалось загрузить ID сделок", "DEAL_IDS_LOAD_FAILED", "failed to load deal ids: "+err.Error(), nil)
		return
	}

	writeAPISuccess(w, http.StatusOK, "deal_ids_loaded", "Список ID сделок загружен", map[string]any{
		"count": len(deals),
		"deals": deals,
	}, nil)
}

func (h *Handler) dealFields(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается", "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		writeAPIError(w, http.StatusInternalServerError, "server_not_configured", "Сервер не настроен: не указан webhook Bitrix24", "WEBHOOK_EMPTY", "server is not configured: BITRIX_WEBHOOK_URL is empty", nil)
		return
	}
	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_webhook", "Некорректный webhook Bitrix24", "WEBHOOK_INVALID", "invalid webhook: "+err.Error(), nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fields, err := bClient.ListDealFields(ctx)
	if err != nil {
		log.WithError(err).Error("deal fields load failed")
		writeAPIError(w, http.StatusBadGateway, "bitrix_error", "Не удалось загрузить поля сделок", "DEAL_FIELDS_LOAD_FAILED", "failed to load deal fields: "+err.Error(), nil)
		return
	}
	writeAPISuccess(w, http.StatusOK, "deal_fields_loaded", "Поля сделок загружены", map[string]any{
		"count":  len(fields),
		"fields": fields,
	}, nil)
}

func (h *Handler) loadProjects(ctx context.Context, r *http.Request, bClient *bitrix.Client, dealIDs []int) ([]model.ProjectRow, string, error) {
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

func buildDownloadFilename(src string, dealIDs []int, format string) string {
	base := strings.TrimSpace(src)
	base = strings.TrimSuffix(base, ".xlsx")
	base = strings.TrimSuffix(base, ".xls")
	base = strings.TrimSuffix(base, ".docx")
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
	ext := ".xlsx"
	if strings.EqualFold(strings.TrimSpace(format), "docx") {
		ext = ".docx"
	}
	return base + "_passport_and_tasks" + ext
}

func parseExportFormat(raw string) (string, error) {
	f := strings.ToLower(strings.TrimSpace(raw))
	if f == "" || f == "xlsx" {
		return "xlsx", nil
	}
	if f == "docx" {
		return "docx", nil
	}
	return "", fmt.Errorf("unsupported export format %q, expected xlsx or docx", raw)
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
