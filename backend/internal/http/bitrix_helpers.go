package httpapi

import (
	"net/http"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
)

// newBitrixClientFromRequest создаёт клиент Битрикс из активной portal-сессии.
func (h *Handler) newBitrixClientFromRequest(
	w http.ResponseWriter,
	r *http.Request) (*bitrix.Client, bool) {
	if session, ok := h.portalSessionFromRequest(r); ok {
		expiresAt := session.ExpiresAt
		if expiresAt.IsZero() {
			expiresAt = time.Now().Add(1 * time.Hour)
		}
		clientID, clientSecret, credsOK := h.cfg.ResolvePortalApp(session.Domain)
		if !credsOK {
			writeMappedError(
				w,
				errPortalContextMissing,
				"portal app credentials are not configured for domain: "+session.Domain,
			)
			return nil, false
		}

		client, err := bitrix.NewFromPortalSession(
			session.Domain,
			session.AccessToken,
			session.RefreshToken,
			expiresAt,
			clientID,
			clientSecret,
		)
		if err == nil {
			return client, true
		}
		writeMappedError(
			w,
			errPortalContextMissing,
			"invalid portal session: "+err.Error(),
		)
		return nil, false
	}

	writeMappedError(w, errPortalSessionRequired, "portal session is required")
	return nil, false
}
