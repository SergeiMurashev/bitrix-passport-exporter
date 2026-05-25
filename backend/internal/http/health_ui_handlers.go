package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// healthz godoc
// @Summary      Liveness probe
// @Description  Проверка, что процесс жив
// @Tags         Health
// @Produce      plain
// @Success      200  {string}  string  "ok"
// @Router       /healthz [get]
func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

// readyz godoc
// @Summary      Readiness probe
// @Description  Проверка готовности сервиса
// @Tags         Health
// @Produce      plain
// @Success      200  {string}  string  "ready"
// @Router       /readyz [get]
func (h *Handler) readyz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ready"))
}

func (h *Handler) ui(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeAPIErrorDefSimple(w, errMethodNotAllowed, "method not allowed")
		return
	}
	_ = h.bootstrapPortalSessionFromRequest(w, r)
	indexPath := filepath.Join(frontendDistDir(), "index.html")
	body, err := os.ReadFile(indexPath)
	if err != nil {
		writeMappedError(w, errFrontendNotBuilt, "frontend is not built; run frontend build")
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
