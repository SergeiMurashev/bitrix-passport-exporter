package httpapi

import (
	"context"
	"sync"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
)

type Handler struct {
	cfg                   config.Config
	exportLimiter         *rateWindowLimiter
	portalSessions        map[string]portalSession
	mu                    sync.Mutex
	fullExportBusy        bool
	fullExportCancel      context.CancelFunc
	cancelRequestedByUser bool
	status                exportStatus
	lastResultPath        string
	lastFilename          string
	lastContentType       string
	lastUpdatedAt         time.Time
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

func New(cfg config.Config) (*Handler, error) {
	return &Handler{
		cfg: cfg,
		exportLimiter: newRateWindowLimiter(
			cfg.RateLimitExportPerMinute,
			time.Minute,
		),
		portalSessions: make(map[string]portalSession),
		status:         exportStatus{Phase: "idle"},
	}, nil
}

func (h *Handler) Close() error {
	return nil
}
