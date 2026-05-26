package httpapi

import (
	"net/http"
	"path/filepath"
)

func (h *Handler) Register(mux *http.ServeMux) {
	guard := h.withAccessControl
	h.regHealthRoutes(mux)
	h.regStaticRoutes(mux)
	h.regPortalRoutes(mux, guard)
	h.regDealRoutes(mux, guard)
	h.regExportRoutes(mux, guard)
}

// regHealthRoutes регистрирует конечные точки работоспособности/готовности.
func (h *Handler) regHealthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/readyz", h.readyz)
}

// regStaticRoutes регистрирует статические файлы и точку входа пользовательского интерфейса.
func (h *Handler) regStaticRoutes(mux *http.ServeMux) {
	mux.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir(filepath.Join(frontendDistDir(), "assets")))))
	mux.Handle("/invest-agent-logo-dark.png", http.FileServer(http.Dir(frontendDistDir())))
	mux.Handle("/invest-agent-logo-light.png", http.FileServer(http.Dir(frontendDistDir())))
	mux.Handle("/logo1.png", http.FileServer(http.Dir(frontendDistDir())))
	/* Маршруты для устанвоки на портал приложения */
	mux.Handle("/bitrix/app", http.HandlerFunc(h.ui))
	mux.Handle("/bitrix/install", http.HandlerFunc(h.ui))
	/* Внутренний маршрут приложения */
	mux.Handle("/", http.HandlerFunc(h.ui))
}

// regPortalRoutes регистрирует точки инициализации сессии Bitrix24 portal app.
func (h *Handler) regPortalRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.HandleFunc("/api/portal/session", h.portalSessionBootstrap)
	mux.Handle("/api/portal/me", guard(http.HandlerFunc(h.portalMe)))
}

// regDealRoutes регистрирует конечные точки метаданных сделки.
func (h *Handler) regDealRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("/api/deals/ids", guard(http.HandlerFunc(h.dealIDs)))
	mux.Handle("/api/deals/fields", guard(http.HandlerFunc(h.dealFields)))
}

// regExportRoutes регистрирует конечные точки жизненного цикла экспорта.
func (h *Handler) regExportRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("/api/export/status", guard(http.HandlerFunc(h.exportStatus)))
	mux.Handle("/api/export/download-last", guard(http.HandlerFunc(h.downloadLastExport)))
	mux.Handle("/api/export/cancel", guard(http.HandlerFunc(h.cancelExport)))
	mux.Handle("/api/export", guard(http.HandlerFunc(h.export)))
}

func frontendDistDir() string {
	return filepath.Clean(filepath.Join("..", "frontend", "dist"))
}
