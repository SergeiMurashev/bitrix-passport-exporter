package httpapi

import "net/http"

var (
	errFrontendNotBuilt = apiErrorDef{
		HTTPCode: http.StatusServiceUnavailable,
		Status:   "service_unavailable",
		Message:  "Фронтенд не собран",
		Code:     "FRONTEND_NOT_BUILT",
	}
	errPortalContextMissing = apiErrorDef{
		HTTPCode: http.StatusBadRequest,
		Status:   "bad_request",
		Message:  "Не передан контекст портала Bitrix24",
		Code:     "PORTAL_CONTEXT_MISSING",
	}
	errPortalSessionRequired = apiErrorDef{
		HTTPCode: http.StatusUnauthorized,
		Status:   "unauthorized",
		Message:  "Требуется активный контекст портала Bitrix24",
		Code:     "PORTAL_SESSION_REQUIRED",
	}
	errDealIDsLoadFailed = apiErrorDef{
		HTTPCode: http.StatusBadGateway,
		Status:   "upstream_error",
		Message:  "Не удалось загрузить ID сделок",
		Code:     "DEAL_IDS_LOAD_FAILED",
	}
	errDealFieldsLoadFailed = apiErrorDef{
		HTTPCode: http.StatusBadGateway,
		Status:   "upstream_error",
		Message:  "Не удалось загрузить поля сделок",
		Code:     "DEAL_FIELDS_LOAD_FAILED",
	}
	errExportAlreadyRunning = apiErrorDef{
		HTTPCode: http.StatusTooManyRequests,
		Status:   "rate_limited",
		Message:  "Полная выгрузка уже выполняется",
		Code:     "EXPORT_ALREADY_RUNNING",
	}
	errNoExportRunning = apiErrorDef{
		HTTPCode: http.StatusConflict,
		Status:   "conflict",
		Message:  "Нет активной выгрузки для отмены",
		Code:     "NO_EXPORT_RUNNING",
	}
	errExportFileNotFound = apiErrorDef{
		HTTPCode: http.StatusNotFound,
		Status:   "not_found",
		Message:  "Готовый файл выгрузки не найден",
		Code:     "EXPORT_FILE_NOT_FOUND",
	}
	errExportRateLimited = apiErrorDef{
		HTTPCode: http.StatusTooManyRequests,
		Status:   "rate_limited",
		Message:  "Слишком много запросов на выгрузку",
		Code:     "EXPORT_RATE_LIMITED",
	}
	errInvalidMultipartForm = apiErrorDef{
		HTTPCode: http.StatusBadRequest,
		Status:   "bad_request",
		Message:  "Некорректная multipart-форма",
		Code:     "INVALID_MULTIPART_FORM",
	}
	errInvalidForm = apiErrorDef{
		HTTPCode: http.StatusBadRequest,
		Status:   "bad_request",
		Message:  "Некорректные параметры запроса",
		Code:     "INVALID_FORM",
	}
	errInvalidDealIDs = apiErrorDef{
		HTTPCode: http.StatusBadRequest,
		Status:   "bad_request",
		Message:  "Некорректный список ID сделок",
		Code:     "INVALID_DEAL_IDS",
	}
	errUnsupportedExportFormat = apiErrorDef{
		HTTPCode: http.StatusBadRequest,
		Status:   "bad_request",
		Message:  "Неподдерживаемый формат выгрузки",
		Code:     "UNSUPPORTED_EXPORT_FORMAT",
	}
	errExportCanceled = apiErrorDef{
		HTTPCode: http.StatusConflict,
		Status:   "conflict",
		Message:  "Выгрузка отменена пользователем",
		Code:     "EXPORT_CANCELED",
	}
	errProjectsLoadFailed = apiErrorDef{
		HTTPCode: http.StatusBadRequest,
		Status:   "bad_request",
		Message:  "Не удалось загрузить сделки",
		Code:     "PROJECTS_LOAD_FAILED",
	}
	errNoProjectsFound = apiErrorDef{
		HTTPCode: http.StatusBadRequest,
		Status:   "bad_request",
		Message:  "Сделки не найдены",
		Code:     "NO_PROJECTS_FOUND",
	}
	errDealsPageLoadFailed = apiErrorDef{
		HTTPCode: http.StatusInternalServerError,
		Status:   "internal_error",
		Message:  "Не удалось загрузить страницу сделок из Bitrix24",
		Code:     "DEALS_PAGE_LOAD_FAILED",
	}
	errBitrixAuthFailed = apiErrorDef{
		HTTPCode: http.StatusBadGateway,
		Status:   "upstream_error",
		Message:  "Токен Bitrix24 недействителен или истек",
		Code:     "BITRIX_AUTH_FAILED",
	}
	errTasksCollectFailed = apiErrorDef{
		HTTPCode: http.StatusInternalServerError,
		Status:   "internal_error",
		Message:  "Не удалось собрать задачи по сделкам",
		Code:     "TASKS_COLLECT_FAILED",
	}
	errExportBuildFailed = apiErrorDef{
		HTTPCode: http.StatusInternalServerError,
		Status:   "internal_error",
		Message:  "Не удалось сформировать файл выгрузки",
		Code:     "EXPORT_BUILD_FAILED",
	}
)

func writeMappedError(w http.ResponseWriter, def apiErrorDef, detail string) {
	writeAPIErrorDefSimple(w, def, detail)
}
