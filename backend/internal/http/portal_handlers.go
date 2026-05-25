package httpapi

import (
	"net/http"
	"strings"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/models"
)

func (h *Handler) portalSessionBootstrap(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	if ok := h.bootstrapPortalSessionFromRequest(w, r); !ok {
		writeMappedError(w, errPortalContextMissing, "bitrix portal auth payload is missing")
		return
	}
	writeAPISuccess(
		w,
		http.StatusOK,
		"portal_session_ready",
		"Контекст портала Bitrix24 сохранен",
		models.PortalSessionData{Ready: true},
		nil,
	)
}

func (h *Handler) portalMe(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	session, ok := h.portalSessionFromRequest(r)
	if !ok {
		writeMappedError(w, errPortalSessionRequired, "portal session is required")
		return
	}
	writeAPISuccess(
		w,
		http.StatusOK,
		"portal_session_active",
		"Контекст портала активен",
		models.PortalSessionData{
			Ready:    true,
			Domain:   strings.TrimSpace(session.Domain),
			MemberID: strings.TrimSpace(session.MemberID),
			User: models.APIUser{
				ID:    session.UserID,
				Login: strings.TrimSpace(session.UserLogin),
			},
		},
		nil,
	)
}
