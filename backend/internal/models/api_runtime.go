package models

type AuthLoginRequest struct {
	Login    string `json:"login"`
	Password string `json:"password"`
}

type ResponseMeta struct {
	GeneratedAt string `json:"generated_at,omitempty"`
}

type DealShort struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

type DealField struct {
	Code  string `json:"code"`
	Title string `json:"title"`
	Type  string `json:"type"`
}
