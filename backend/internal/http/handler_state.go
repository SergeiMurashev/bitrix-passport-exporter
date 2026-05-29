package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/models"
)

const global = "global"

type Handler struct {
	cfg            config.Config
	exportLimiter  *rateWindowLimiter
	portalSessions map[string]portalSession
	mu             sync.Mutex
	exportStates   map[string]*portalExportState
}

const projectFieldCode = "UF_CRM_PROJECT_GROUP_ID"

type exportStatus struct {
	Running              bool      `json:"running"`
	CanCancel            bool      `json:"can_cancel"`
	CancelRequested      bool      `json:"cancel_requested"`
	Phase                string    `json:"phase"`
	DealsProcessed       int       `json:"deals_processed"`
	DealsTotal           int       `json:"deals_total"`
	TasksTotal           int       `json:"tasks_total"`
	DealsWithSupport     int       `json:"deals_with_support"`
	SupportMeasuresTotal int       `json:"support_measures_total"`
	HasLastResult        bool      `json:"has_last_result"`
	LastFileName         string    `json:"last_file_name,omitempty"`
	StartedAt            time.Time `json:"started_at,omitempty"`
	FinishedAt           time.Time `json:"finished_at,omitempty"`
	LastError            string    `json:"last_error,omitempty"`
}

type portalExportState struct {
	fullExportBusy        bool
	fullExportCancel      context.CancelFunc
	cancelRequestedByUser bool
	status                exportStatus
	lastResultPath        string
	lastFilename          string
	lastContentType       string
	lastUpdatedAt         time.Time
}

func New(cfg config.Config) (*Handler, error) {
	return &Handler{
		cfg: cfg,
		exportLimiter: newRateWindowLimiter(
			cfg.RateLimitExportPerMinute,
			time.Minute,
		),
		portalSessions: make(map[string]portalSession),
		exportStates:   make(map[string]*portalExportState),
	}, nil
}

func (h *Handler) Close() error {
	return nil
}

func (h *Handler) portalScopeKey(r *http.Request) string {
	if session, ok := h.portalSessionFromRequest(r); ok {
		domain := strings.TrimSpace(strings.ToLower(session.Domain))
		if domain != "" {
			return domain
		}
	}
	return "global"
}

func (h *Handler) ensureExportStateLocked(scope string) *portalExportState {
	key := strings.TrimSpace(strings.ToLower(scope))
	if key == "" {
		key = global
	}
	if st, ok := h.exportStates[key]; ok && st != nil {
		return st
	}
	st := &portalExportState{
		status: exportStatus{
			Phase: models.PhaseIdle,
		},
	}
	h.exportStates[key] = st
	return st
}
