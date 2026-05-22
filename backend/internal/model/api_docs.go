package model

type APIError struct {
	Code   string `json:"code" example:"AUTH_REQUIRED"`
	Detail string `json:"detail" example:"unauthorized"`
}

type APIErrorResponse struct {
	OK      bool           `json:"ok" example:"false"`
	Status  string         `json:"status" example:"unauthorized"`
	Message string         `json:"message" example:"Требуется авторизация"`
	Error   APIError       `json:"error"`
	Meta    map[string]any `json:"meta,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

type APIUser struct {
	ID    int64  `json:"id" example:"2"`
	Login string `json:"login" example:"InvestClient"`
}

type AuthLoginData struct {
	ExpiresAt string  `json:"expires_at" example:"2026-05-29T07:00:32Z"`
	Session   string  `json:"session" example:"cookie"`
	User      APIUser `json:"user"`
}

type AuthLoginResponse struct {
	OK      bool          `json:"ok" example:"true"`
	Status  string        `json:"status" example:"authorized"`
	Message string        `json:"message" example:"Вход выполнен"`
	Data    AuthLoginData `json:"data"`
}

type AuthMeData struct {
	ExpiresAt string  `json:"expires_at" example:"2026-05-29T07:00:32Z"`
	User      APIUser `json:"user"`
}

type AuthMeResponse struct {
	OK      bool       `json:"ok" example:"true"`
	Status  string     `json:"status" example:"authorized"`
	Message string     `json:"message" example:"Сессия активна"`
	Data    AuthMeData `json:"data"`
}

type LogoutData struct {
	LoggedOut bool `json:"logged_out" example:"true"`
}

type LogoutResponse struct {
	OK      bool       `json:"ok" example:"true"`
	Status  string     `json:"status" example:"logged_out"`
	Message string     `json:"message" example:"Выход выполнен"`
	Data    LogoutData `json:"data"`
}

type ExportStatsData struct {
	DealsTotal           int `json:"deals_total" example:"1"`
	TasksTotal           int `json:"tasks_total" example:"0"`
	DealsWithSupport     int `json:"deals_with_support" example:"1"`
	SupportMeasuresTotal int `json:"support_measures_total" example:"1"`
}

type ExportCompletedData struct {
	FileName    string          `json:"file_name" example:"deal_28605_passport_and_tasks.docx"`
	ContentType string          `json:"content_type" example:"application/vnd.openxmlformats-officedocument.wordprocessingml.document"`
	SizeBytes   int             `json:"size_bytes" example:"3597"`
	DownloadURL string          `json:"download_url" example:"/api/export/download-last"`
	Format      string          `json:"format" example:"docx"`
	Source      string          `json:"source" example:"bitrix_deal_28605"`
	IssuesCount int             `json:"issues_count" example:"0"`
	Stats       ExportStatsData `json:"stats"`
}

type ExportCompletedResponse struct {
	OK      bool                `json:"ok" example:"true"`
	Status  string              `json:"status" example:"export_completed"`
	Message string              `json:"message" example:"Выгрузка завершена, файл готов к скачиванию"`
	Data    ExportCompletedData `json:"data"`
}

type ExportStatusData struct {
	Running              bool   `json:"running"`
	CanCancel            bool   `json:"can_cancel"`
	CancelRequested      bool   `json:"cancel_requested"`
	Phase                string `json:"phase"`
	DealsProcessed       int    `json:"deals_processed"`
	DealsTotal           int    `json:"deals_total"`
	TasksTotal           int    `json:"tasks_total"`
	DealsWithSupport     int    `json:"deals_with_support"`
	SupportMeasuresTotal int    `json:"support_measures_total"`
	HasLastResult        bool   `json:"has_last_result"`
	LastFileName         string `json:"last_file_name,omitempty"`
	StartedAt            string `json:"started_at,omitempty"`
	FinishedAt           string `json:"finished_at,omitempty"`
	LastError            string `json:"last_error,omitempty"`
}

type ExportStatusResponse struct {
	OK      bool             `json:"ok" example:"true"`
	Status  string           `json:"status" example:"export_status"`
	Message string           `json:"message" example:"Текущий статус выгрузки"`
	Data    ExportStatusData `json:"data"`
}

type CancelExportData struct {
	CancelRequested bool `json:"cancel_requested" example:"true"`
}

type CancelExportResponse struct {
	OK      bool             `json:"ok" example:"true"`
	Status  string           `json:"status" example:"cancel_requested"`
	Message string           `json:"message" example:"Запрос на отмену выгрузки отправлен"`
	Data    CancelExportData `json:"data"`
}

type DealIDsData struct {
	Count int   `json:"count" example:"9854"`
	Deals []int `json:"deals"`
}

type DealIDsResponse struct {
	OK      bool        `json:"ok" example:"true"`
	Status  string      `json:"status" example:"deal_ids_loaded"`
	Message string      `json:"message" example:"Список ID сделок загружен"`
	Data    DealIDsData `json:"data"`
}

type DealFieldsData struct {
	Count  int           `json:"count" example:"120"`
	Fields []interface{} `json:"fields"`
}

type DealFieldsResponse struct {
	OK      bool           `json:"ok" example:"true"`
	Status  string         `json:"status" example:"deal_fields_loaded"`
	Message string         `json:"message" example:"Поля сделок загружены"`
	Data    DealFieldsData `json:"data"`
}
