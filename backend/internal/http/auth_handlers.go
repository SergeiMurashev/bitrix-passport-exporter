package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/auth"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/models"
)

func (h *Handler) withAccessControl(next http.Handler) http.Handler {
	token := strings.TrimSpace(h.cfg.APIAccessToken)
	useToken := token != ""
	useAuth := h.auth != nil
	usePortal := true
	if !useToken && !useAuth && !usePortal {
		return next
	}

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
		if usePortal {
			if _, ok := h.portalSessionFromRequest(r); ok {
				next.ServeHTTP(w, r)
				return
			}
		}
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

// authLogin godoc
// @Summary      Вход в систему
// @Description  Проверяет логин/пароль и создает сессионную cookie bp_session (HttpOnly)
// @Tags         Auth
// @Accept       json
// @Produce      json
// @Param        body  body      object{login=string,password=string}  true  "Login payload"
// @Success      200   {object}  SwaggerAuthLoginResponse
// @Failure      400   {object}  SwaggerErrorResponse
// @Failure      401   {object}  SwaggerErrorResponse
// @Failure      429   {object}  SwaggerErrorResponse
// @Router       /api/auth/login [post]
func (h *Handler) authLogin(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if h.loginLimiter != nil && !h.loginLimiter.Allow(clientIPFromRequest(r)) {
		writeMappedError(w, errLoginRateLimited, "too many login attempts, try again later")
		return
	}
	if h.auth == nil {
		writeMappedError(w, errAuthDisabled, "auth is disabled")
		return
	}

	var in models.AuthLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeAPIErrorDefSimple(w, errInvalidJSON, "invalid json body")
		return
	}
	login := strings.TrimSpace(in.Login)
	if login == "" || strings.TrimSpace(in.Password) == "" {
		writeMappedError(w, errAuthRequiredFields, "login and password are required")
		return
	}

	user, err := h.auth.Authenticate(r.Context(), login, in.Password)
	if err != nil {
		writeMappedError(w, errInvalidCredentials, "invalid credentials")
		return
	}
	token, expiresAt, err := h.auth.IssueToken(user)
	if err != nil {
		writeMappedError(w, errTokenIssueFailed, "failed to issue token")
		return
	}
	h.setSessionCookie(w, r, token, expiresAt)
	w.Header().Set("Cache-Control", "no-store")
	writeAPISuccess(
		w,
		http.StatusOK,
		"authorized",
		"Вход выполнен",
		models.AuthLoginData{
			ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano),
			Session:   "cookie",
			User: models.APIUser{
				ID:    user.ID,
				Login: user.Login,
			},
		}, nil)
}

// authMe godoc
// @Summary      Текущая сессия
// @Description  Возвращает текущего авторизованного пользователя
// @Tags         Auth
// @Produce      json
// @Security     CookieAuth
// @Security     ApiKeyAuth
// @Security     BearerAuth
// @Success      200  {object}  SwaggerAuthMeResponse
// @Failure      401  {object}  SwaggerErrorResponse
// @Router       /api/auth/me [get]
func (h *Handler) authMe(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	if session, ok := h.portalSessionFromRequest(r); ok {
		writeAPISuccess(
			w,
			http.StatusOK,
			"authorized",
			"Сессия портала активна",
			models.AuthMeData{
				User: models.APIUser{
					ID:    session.UserID,
					Login: strings.TrimSpace(session.UserLogin),
				},
				ExpiresAt: session.ExpiresAt.UTC().Format(time.RFC3339Nano),
			},
			nil,
		)
		return
	}
	if h.auth == nil {
		writeAPISuccess(
			w,
			http.StatusOK,
			"authorized",
			"Сессия активна",
			models.AuthMeData{
				User: models.APIUser{ID: 0, Login: "system"},
			},
			nil,
		)
		return
	}
	claims, ok := h.sessionClaimsFromRequest(r)
	if !ok || claims == nil {
		writeMappedError(w, errSessionRequired, "unauthorized")
		return
	}
	writeAPISuccess(
		w,
		http.StatusOK,
		"authorized",
		"Сессия активна",
		models.AuthMeData{
			User:      models.APIUser{ID: claims.UserID, Login: claims.Login},
			ExpiresAt: claims.ExpiresAt.Time.UTC().Format(time.RFC3339Nano),
		}, nil)
}

// authLogout godoc
// @Summary      Выход из системы
// @Description  Очищает cookie сессии bp_session
// @Tags         Auth
// @Produce      json
// @Security     CookieAuth
// @Security     ApiKeyAuth
// @Security     BearerAuth
// @Success      200  {object}  SwaggerLogoutResponse
// @Router       /api/auth/logout [post]
func (h *Handler) authLogout(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	h.clearPortalSession(w, r)
	h.clearSessionCookie(w, r)
	writeAPISuccess(
		w,
		http.StatusOK,
		"logged_out",
		"Выход выполнен",
		models.LogoutData{LoggedOut: true}, nil)
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
