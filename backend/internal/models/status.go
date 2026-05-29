package models

const (
	// HTTP-заголовки и аутентификация.
	XApiKey      = "X-API-Key"
	XRealIP      = "X-Real-IP"
	XAccessToken = "X-Access-Token"
	Auth         = "Authorization"
	Bearer       = "bearer "

	// Параметры запроса/метки источника.
	FormatFile = "file"
	BitrixApi  = "bitrix_api"

	// Стратегия batch запросов сделок
	StrategyPerDeal = "per_deal"
	StrategyBulk    = "bulk"

	// Статусы ответов.
	StatusSuccess            = "success"
	StatusError              = "error"
	StatusUnauthorized       = "unauthorized"
	StatusMethodNotAllowed   = "method_not_allowed"
	StatusServiceUnavailable = "service_unavailable"
	StatusBadRequest         = "bad_request"
	StatusUpstreamError      = "upstream_error"
	StatusRateLimited        = "rate_limited"
	StatusConflict           = "conflict"
	StatusNotFound           = "not_found"
	StatusInternalError      = "internal_error"
	// Статусы сессий портала
	StatusReadyPortalSession  = "portal_session_ready"
	StatusActivePortalSession = "portal_session_active"
	// Статусы загрузки данных
	StatusDealIdsLoaded    = "deal_ids_loaded"
	StatusDealFieldsLoaded = "deal_fields_loaded"

	// Экспортные этапы.
	PhaseIdle     = "idle"
	PhasePassport = "passport"
	PhaseTasks    = "tasks"
	PhaseError    = "error"
	PhaseCanceled = "canceled"

	// Типы контента HTTP.
	ContentTypeJSONUTF8 = "application/json; charset=utf-8"
)
