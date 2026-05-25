package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	portalSessionCookieName = "bp_portal_session"
	portalSessionTTL        = 24 * time.Hour
)

type portalSession struct {
	SessionID    string
	Domain       string
	AccessToken  string
	RefreshToken string
	MemberID     string
	UserID       int64
	UserLogin    string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

type portalAuthPayload struct {
	Domain       string `json:"domain"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	MemberID     string `json:"member_id"`
	UserID       string `json:"user_id"`
	UserLogin    string `json:"user_login"`
	ExpiresIn    string `json:"expires_in"`
	ExpiresAt    string `json:"expires_at"`
}

// bootstrapPortalSessionFromRequest создает portalSession из данных запроса и установить cookie. Возвращает true, если сессия успешно создана.
func (h *Handler) bootstrapPortalSessionFromRequest(w http.ResponseWriter, r *http.Request) bool {
	payload, hasPayload, err := readPortalAuthPayload(r)
	if err != nil {
		return false
	}
	if !hasPayload {
		return false
	}

	domain := normalizePortalDomain(payload.Domain)
	accessToken := strings.TrimSpace(payload.AccessToken)
	if domain == "" || accessToken == "" {
		return false
	}

	expiresAt := parsePortalExpiresAt(payload.ExpiresAt, payload.ExpiresIn)
	sid, err := generatePortalSessionID()
	if err != nil {
		return false
	}
	now := time.Now().UTC()
	session := portalSession{
		SessionID:    sid,
		Domain:       domain,
		AccessToken:  accessToken,
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
		MemberID:     strings.TrimSpace(payload.MemberID),
		UserID:       parseInt64(strings.TrimSpace(payload.UserID)),
		UserLogin:    strings.TrimSpace(payload.UserLogin),
		ExpiresAt:    expiresAt,
		CreatedAt:    now,
	}

	h.mu.Lock()
	h.portalSessions[sid] = session
	h.cleanupExpiredPortalSessionsLocked(now)
	h.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     portalSessionCookieName,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		MaxAge:   int(portalSessionTTL.Seconds()),
	})

	return true
}

// portalSessionFromRequest извлекает portalSession из cookie запроса. Если сессия не найдена или истекла, возвращает false.
func (h *Handler) portalSessionFromRequest(r *http.Request) (portalSession, bool) {
	if r == nil {
		return portalSession{}, false
	}
	cookie, err := r.Cookie(portalSessionCookieName)
	if err != nil {
		return portalSession{}, false
	}
	sid := strings.TrimSpace(cookie.Value)
	if sid == "" {
		return portalSession{}, false
	}

	now := time.Now().UTC()
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.portalSessions[sid]
	if !ok {
		return portalSession{}, false
	}
	if s.ExpiresAt.Before(now.Add(-5 * time.Minute)) {
		delete(h.portalSessions, sid)
		return portalSession{}, false
	}
	return s, true
}

func (h *Handler) clearPortalSession(w http.ResponseWriter, r *http.Request) {
	if r != nil {
		if c, err := r.Cookie(portalSessionCookieName); err == nil {
			sid := strings.TrimSpace(c.Value)
			if sid != "" {
				h.mu.Lock()
				delete(h.portalSessions, sid)
				h.mu.Unlock()
			}
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     portalSessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r != nil && r.TLS != nil,
		MaxAge:   -1,
	})
}

func (h *Handler) cleanupExpiredPortalSessionsLocked(now time.Time) {
	if len(h.portalSessions) == 0 {
		return
	}
	cutoff := now.Add(-5 * time.Minute)
	for sid, session := range h.portalSessions {
		if session.ExpiresAt.Before(cutoff) || session.CreatedAt.Add(7*24*time.Hour).Before(now) {
			delete(h.portalSessions, sid)
		}
	}
}

func readPortalAuthPayload(r *http.Request) (portalAuthPayload, bool, error) {
	payload := portalAuthPayload{}
	if r == nil {
		return payload, false, nil
	}

	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "application/json") {
		var fromJSON portalAuthPayload
		if err := json.NewDecoder(r.Body).Decode(&fromJSON); err == nil {
			if strings.TrimSpace(fromJSON.AccessToken) != "" || strings.TrimSpace(fromJSON.Domain) != "" {
				return fromJSON, true, nil
			}
		}
	}

	if err := r.ParseForm(); err != nil {
		return payload, false, err
	}
	v := func(keys ...string) string {
		for _, key := range keys {
			if value := strings.TrimSpace(r.FormValue(key)); value != "" {
				return value
			}
		}
		return ""
	}
	payload = portalAuthPayload{
		Domain: v("domain", "DOMAIN", "auth[domain]", "AUTH[DOMAIN]"),
		AccessToken: v(
			"access_token",
			"AUTH_ID",
			"auth_id",
			"auth[access_token]",
			"auth[AUTH_ID]",
			"AUTH[ID]",
			"AUTH[access_token]",
		),
		RefreshToken: v(
			"refresh_token",
			"REFRESH_ID",
			"refresh_id",
			"auth[refresh_token]",
			"AUTH[REFRESH_ID]",
			"AUTH[refresh_token]",
		),
		MemberID: v("member_id", "MEMBER_ID", "auth[member_id]", "AUTH[MEMBER_ID]"),
		UserID:   v("user_id", "USER_ID", "auth[user_id]", "AUTH[USER_ID]"),
		UserLogin: v(
			"user_login",
			"USER_LOGIN",
			"auth[user_login]",
			"AUTH[USER_LOGIN]",
		),
		ExpiresIn: v("expires_in", "AUTH_EXPIRES", "auth[expires]", "AUTH[AUTH_EXPIRES]"),
		ExpiresAt: v("expires_at", "auth[expires_at]", "AUTH[EXPIRES_AT]"),
	}
	has := strings.TrimSpace(payload.AccessToken) != "" || strings.TrimSpace(payload.Domain) != ""
	return payload, has, nil
}

func parsePortalExpiresAt(expiresAtRaw, expiresInRaw string) time.Time {
	now := time.Now().UTC()
	if v := strings.TrimSpace(expiresAtRaw); v != "" {
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
			if ts > 0 && ts < 1_000_000_000_000 {
				return time.Unix(ts, 0).UTC()
			}
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC()
		}
	}
	if v := strings.TrimSpace(expiresInRaw); v != "" {
		if secs, err := strconv.ParseInt(v, 10, 64); err == nil && secs > 0 {
			if secs < 31_536_000 {
				return now.Add(time.Duration(secs) * time.Second)
			}
			return time.Unix(secs, 0).UTC()
		}
	}
	return now.Add(1 * time.Hour)
}

func normalizePortalDomain(domain string) string {
	value := strings.TrimSpace(domain)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return strings.TrimRight(value, "/")
	}
	return "https://" + strings.TrimRight(value, "/")
}

func generatePortalSessionID() (string, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return hex.EncodeToString(random), nil
}

func parseInt64(v string) int64 {
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
