package httpapi

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/models"
)

func (h *Handler) withAccessControl(next http.Handler) http.Handler {
	token := strings.TrimSpace(h.cfg.APIAccessToken)
	useToken := token != ""
	unauthorized := func(w http.ResponseWriter) {
		writeAPIErrorDefSimple(
			w,
			errUnauthorized,
			models.StatusUnauthorized,
		)
	}

	tokenAllowed := func(r *http.Request) bool {
		candidates := []string{
			strings.TrimSpace(r.Header.Get(models.XApiKey)),
			strings.TrimSpace(r.Header.Get(models.XAccessToken)),
		}
		authHeader := strings.TrimSpace(r.Header.Get(models.Auth))
		if strings.HasPrefix(strings.ToLower(authHeader), models.Bearer) {
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
		if _, ok := h.portalSessionFromRequest(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		if useToken && tokenAllowed(r) {
			next.ServeHTTP(w, r)
			return
		}
		unauthorized(w)
	})
}

func clientIPFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if forwarded := strings.TrimSpace(r.Header.Get(models.XApiKey)); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if realIP := strings.TrimSpace(r.Header.Get(models.XRealIP)); realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
