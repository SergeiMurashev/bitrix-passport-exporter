package httpapi

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

func (h *Handler) withAccessControl(next http.Handler) http.Handler {
	token := strings.TrimSpace(h.cfg.APIAccessToken)
	useToken := token != ""
	unauthorized := func(w http.ResponseWriter) {
		writeAPIErrorDefSimple(w, errUnauthorized, "unauthorized")
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
