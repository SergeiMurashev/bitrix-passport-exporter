package httpapi

import (
	"net/http"
	"strings"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
)

// newBitrixClientFromConfig создаёт клиент Битрикс из конфига сервера.
// Он записывает ответ об ошибке API и возвращает ok=false, если вебхук недействителен.
func (h *Handler) newBitrixClientFromConfig(w http.ResponseWriter) (*bitrix.Client, bool) {
	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		writeMappedError(w, errWebhookEmpty, "server is not configured: BITRIX_WEBHOOK_URL is empty")
		return nil, false
	}

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		writeMappedError(w, errWebhookInvalid, "invalid webhook: "+err.Error())
		return nil, false
	}

	return bClient, true
}
