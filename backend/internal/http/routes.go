package httpapi

import (
	"net/http"
	"path/filepath"
)

func (h *Handler) Register(mux *http.ServeMux) {
	guard := h.withAccessControl
	h.regHealthRoutes(mux)
	h.regStaticRoutes(mux)
	h.regAuthRoutes(mux, guard)
	h.regDealRoutes(mux, guard)
	h.regExportRoutes(mux, guard)
}

/* Группа ручек для контроля работы сервера */
func (h *Handler) regHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.healthz)
	logRoute("GET", "/healthz", "Handler.healthz", 0)
	mux.HandleFunc("/readyz", h.readyz)
	logRoute("GET", "/readyz", "Handler.readyz", 0)
}

/* Группа ручек статичных маршрутов */
func (h *Handler) regStaticRoutes(mux *http.ServeMux) {
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
}

/* Группа ручек авторизаций bitrix-exporters */
func (h *Handler) regAuthRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.HandleFunc("/api/auth/login", h.authLogin)
	logRoute("POST", "/api/auth/login", "Handler.authLogin", 0)
	mux.Handle("/api/auth/me", guard(http.HandlerFunc(h.authMe)))
	logRoute("GET", "/api/auth/me", "Handler.authMe", 0)
	mux.Handle("/api/auth/logout", guard(http.HandlerFunc(h.authLogout)))
	logRoute("POST", "/api/auth/logout", "Handler.authLogout", 0)
}

/* Группа ручек для работы с сделками Bitrix24 */
func (h *Handler) regDealRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("/api/deals/ids", guard(http.HandlerFunc(h.dealIDs)))
	logRoute("GET", "/api/deals/ids", "Handler.dealIDs", 0)
	mux.Handle("/api/deals/fields", guard(http.HandlerFunc(h.dealFields)))
	logRoute("GET", "/api/deals/fields", "Handler.dealFields", 0)
}

/* Группа ручек для работы с экспортом файла */
func (h *Handler) regExportRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("/api/export/status", guard(http.HandlerFunc(h.exportStatus)))
	logRoute("GET", "/api/export/status", "Handler.exportStatus", 0)
	mux.Handle("/api/export/download-last", guard(http.HandlerFunc(h.downloadLastExport)))
	logRoute("GET", "/api/export/download-last", "Handler.downloadLastExport", 0)
	mux.Handle("/api/export/cancel", guard(http.HandlerFunc(h.cancelExport)))
	logRoute("POST", "/api/export/cancel", "Handler.cancelExport", 0)
	mux.Handle("/api/export", guard(http.HandlerFunc(h.export)))
	logRoute("POST", "/api/export", "Handler.export", 0)
}

func frontendDistDir() string {
	return filepath.Clean(filepath.Join("..", "frontend", "dist"))
}
