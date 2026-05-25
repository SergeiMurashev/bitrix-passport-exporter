package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
)

// newBitrixClientFromRequest создаёт клиент Битрикс из активной portal-сессии.
// Если portal-сессии нет, используется fallback на BITRIX_WEBHOOK_URL (локальная отладка).
func (h *Handler) newBitrixClientFromRequest(w http.ResponseWriter, r *http.Request) (*bitrix.Client, bool) {
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

	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		writeMappedError(w, errWebhookEmpty, "server is not configured: portal session and BITRIX_WEBHOOK_URL are empty")
		return nil, false
	}

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		writeMappedError(w, errWebhookInvalid, "invalid webhook: "+err.Error())
		return nil, false
	}

	return bClient, true
}
