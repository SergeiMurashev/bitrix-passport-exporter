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
	mux.HandleFunc("/readyz", h.readyz)
}

/* Группа ручек статичных маршрутов */
func (h *Handler) regStaticRoutes(mux *http.ServeMux) {
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(frontendDistDir(), "assets")))))
	mux.Handle("/invest-agent-logo-dark.png", http.FileServer(http.Dir(frontendDistDir())))
	mux.Handle("/invest-agent-logo-light.png", http.FileServer(http.Dir(frontendDistDir())))
	mux.Handle("/logo1.png", http.FileServer(http.Dir(frontendDistDir())))
	mux.Handle("/", http.HandlerFunc(h.ui))
}

/* Группа ручек авторизаций bitrix-exporters */
func (h *Handler) regAuthRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.HandleFunc("/api/auth/login", h.authLogin)
	mux.Handle("/api/auth/me", guard(http.HandlerFunc(h.authMe)))
	mux.Handle("/api/auth/logout", guard(http.HandlerFunc(h.authLogout)))
}

/* Группа ручек для работы с сделками Bitrix24 */
func (h *Handler) regDealRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("/api/deals/ids", guard(http.HandlerFunc(h.dealIDs)))
	mux.Handle("/api/deals/fields", guard(http.HandlerFunc(h.dealFields)))
}

/* Группа ручек для работы с экспортом файла */
func (h *Handler) regExportRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("/api/export/status", guard(http.HandlerFunc(h.exportStatus)))
	mux.Handle("/api/export/download-last", guard(http.HandlerFunc(h.downloadLastExport)))
	mux.Handle("/api/export/cancel", guard(http.HandlerFunc(h.cancelExport)))
	mux.Handle("/api/export", guard(http.HandlerFunc(h.export)))
}

func frontendDistDir() string {
	return filepath.Clean(filepath.Join("..", "frontend", "dist"))
}
