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
		writeAPIErrorSimple(
			w,
			http.StatusInternalServerError,
			"WEBHOOK_EMPTY",
			"Сервер не настроен: не указан webhook Bitrix24",
			"server is not configured: BITRIX_WEBHOOK_URL is empty",
		)
		return nil, false
	}

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		writeAPIErrorSimple(
			w,
			http.StatusBadRequest,
			"WEBHOOK_INVALID",
			"Некорректный webhook Bitrix24",
			"invalid webhook: "+err.Error(),
		)
		return nil, false
	}

	return bClient, true
}
