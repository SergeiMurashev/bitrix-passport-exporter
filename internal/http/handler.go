package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/export"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/parser"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/service"
)

type Handler struct {
	cfg config.Config
}

func New(cfg config.Config) *Handler { return &Handler{cfg: cfg} }

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/", h.ui)
	mux.HandleFunc("/api/export", h.export)
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) ui(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "invalid multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		http.Error(w, "server is not configured: BITRIX_WEBHOOK_URL is empty", http.StatusInternalServerError)
		return
	}
	projectField := strings.TrimSpace(r.FormValue("project_field_code"))
	if projectField == "" {
		projectField = "UF_CRM_PROJECT_GROUP_ID"
	}

	file, fh, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	projects, err := parser.ParseDealsInput(file)
	if err != nil {
		http.Error(w, "failed to parse input: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(projects) == 0 {
		http.Error(w, "no projects found", http.StatusBadRequest)
		return
	}

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		http.Error(w, "invalid webhook: "+err.Error(), http.StatusBadRequest)
		return
	}

	svc := service.NewExporter(bClient)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
	defer cancel()
	tasks, err := svc.BuildTasks(ctx, projects, projectField)
	if err != nil {
		http.Error(w, "failed to collect tasks: "+err.Error(), http.StatusInternalServerError)
		return
	}

	result, err := export.BuildResultXLSX(projects, tasks)
	if err != nil {
		http.Error(w, "failed to build xlsx: "+err.Error(), http.StatusInternalServerError)
		return
	}
	filename := strings.TrimSuffix(strings.TrimSuffix(fh.Filename, ".xlsx"), ".xls") + "_паспорт_и_задачи.xlsx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

const indexHTML = `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>WAK-1238: Паспорт проекта</title>
  <style>
    body{font-family:Arial,sans-serif;background:#f5f7fb;margin:0;padding:24px;color:#1f2937}
    .card{max-width:760px;margin:0 auto;background:#fff;border:1px solid #e5e7eb;border-radius:12px;padding:20px}
    h1{font-size:22px;margin:0 0 8px}
    p{margin:0 0 16px;color:#4b5563}
    label{display:block;margin:12px 0 6px;font-weight:600}
    input[type="text"],input[type="file"]{width:100%;padding:10px;border:1px solid #d1d5db;border-radius:8px;box-sizing:border-box}
    button{margin-top:16px;background:#2563eb;color:#fff;border:0;border-radius:8px;padding:11px 16px;font-size:14px;cursor:pointer}
    button:hover{background:#1d4ed8}
    .hint{font-size:12px;color:#6b7280;margin-top:6px}
  </style>
</head>
<body>
  <div class="card">
    <h1>Выгрузка “Паспорта проекта” + задач</h1>
    <p>Загрузите файл сделок из Bitrix24 и получите итоговый XLSX.</p>
    <form method="post" action="/api/export" enctype="multipart/form-data">
      <label for="file">Файл выгрузки сделок (.xls/.xlsx/.html)</label>
      <input id="file" name="file" type="file" required>

      <div class="hint">Webhook берется из конфигурации сервера (BITRIX_WEBHOOK_URL).</div>

      <label for="project_field_code">Код поля связи сделка → проект (опционально)</label>
      <input id="project_field_code" name="project_field_code" type="text" value="UF_CRM_PROJECT_GROUP_ID">

      <button type="submit">Сформировать XLSX</button>
    </form>
  </div>
</body>
</html>`
