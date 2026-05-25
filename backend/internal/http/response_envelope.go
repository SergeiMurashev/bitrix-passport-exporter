package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

type apiErrorPayload struct {
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type apiErrorDef struct {
	HTTPCode int
	Status   string
	Message  string
	Code     string
}

type apiResponse struct {
	OK      bool             `json:"ok"`
	Status  string           `json:"status"`
	Message string           `json:"message,omitempty"`
	Data    any              `json:"data,omitempty"`
	Error   *apiErrorPayload `json:"error,omitempty"`
	Meta    any              `json:"meta,omitempty"`
}

var (
	errMethodNotAllowed = apiErrorDef{
		HTTPCode: http.StatusMethodNotAllowed,
		Status:   "method_not_allowed",
		Message:  "Метод не поддерживается",
		Code:     "METHOD_NOT_ALLOWED",
	}
	errUnauthorized = apiErrorDef{
		HTTPCode: http.StatusUnauthorized,
		Status:   "unauthorized",
		Message:  "Требуется авторизация",
		Code:     "AUTH_REQUIRED",
	}
	errInvalidJSON = apiErrorDef{
		HTTPCode: http.StatusBadRequest,
		Status:   "bad_request",
		Message:  "Некорректный JSON в теле запроса",
		Code:     "INVALID_JSON",
	}
)

func writeAPIResponse(w http.ResponseWriter, httpCode int, body apiResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(body)
}

func writeAPISuccess(w http.ResponseWriter, httpCode int, status, message string, data any, meta any) {
	if strings.TrimSpace(status) == "" {
		status = "success"
	}
	writeAPIResponse(w, httpCode, apiResponse{
		OK:      true,
		Status:  status,
		Message: strings.TrimSpace(message),
		Data:    data,
		Meta:    meta,
	})
}

func writeAPIError(w http.ResponseWriter, httpCode int, status, message, code, detail string, meta any) {
	if strings.TrimSpace(status) == "" {
		status = "error"
	}
	writeAPIResponse(w, httpCode, apiResponse{
		OK:      false,
		Status:  status,
		Message: strings.TrimSpace(message),
		Error: &apiErrorPayload{
			Code:   strings.TrimSpace(code),
			Detail: strings.TrimSpace(detail),
		},
		Meta: meta,
	})
}

func writeAPIErrorDef(w http.ResponseWriter, def apiErrorDef, detail string, meta any) {
	writeAPIError(
		w,
		def.HTTPCode,
		def.Status,
		def.Message,
		def.Code,
		strings.TrimSpace(detail),
		meta,
	)
}

func writeAPIErrorDefSimple(w http.ResponseWriter, def apiErrorDef, detail string) {
	writeAPIErrorDef(w, def, detail, nil)
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	writeAPIErrorDefSimple(w, errMethodNotAllowed, "method not allowed")
	return false
}
