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
		client, err := bitrix.NewFromPortalSession(
			session.Domain,
			session.AccessToken,
			session.RefreshToken,
			expiresAt,
			h.cfg.BitrixAppClientID,
			h.cfg.BitrixAppClientSecret,
		)
		if err == nil {
			return client, true
		}
		writeMappedError(w, errPortalContextMissing, "invalid portal session: "+err.Error())
		return nil, false
	}

	writeMappedError(w, errPortalSessionRequired, "portal session is required")
	return nil, false
}
