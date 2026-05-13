package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/config"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/export"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/parser"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/service"
)

type Handler struct {
	cfg config.Config
}

const projectFieldCode = "UF_CRM_PROJECT_GROUP_ID"

func New(cfg config.Config) *Handler { return &Handler{cfg: cfg} }

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/", h.ui)
	// Сервисные вызовы
	mux.HandleFunc("/api/deals/ids", h.dealIDs)
	mux.HandleFunc("/api/deals/fields", h.dealFields)
	// Основной вызов
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
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.Contains(contentType, "multipart/form-data") {
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			http.Error(w, "invalid multipart form: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		http.Error(w, "server is not configured: BITRIX_WEBHOOK_URL is empty", http.StatusInternalServerError)
		return
	}
	projectField := projectFieldCode

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		http.Error(w, "invalid webhook: "+err.Error(), http.StatusBadRequest)
		return
	}

	dealIDs, err := parseDealIDs(r.FormValue("deal_ids"), r.FormValue("deal_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	projects, sourceLabel, err := h.loadProjects(r, bClient, dealIDs)
	if err != nil {
		log.Printf("export load projects failed: deal_ids=%v err=%v", dealIDs, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(projects) == 0 {
		http.Error(w, "no projects found", http.StatusBadRequest)
		return
	}
	log.Printf("export request: source=%s deals=%d project_field=%s deal_ids=%v", sourceLabel, len(projects), projectField, dealIDs)

	svc := service.NewExporter(bClient)
	// Не привязывайте длинный экспорт к запросу отмены из браузера/клиента.
	// В противном случае прерывания загрузки/выгрузки отменяют вызовы Битрикса в процессе выполнения.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	tasks, stats, issues, err := svc.BuildTasks(ctx, projects, projectField)
	if err != nil {
		if strings.Contains(err.Error(), "webhook auth failed") {
			http.Error(w, "bitrix webhook is invalid or expired", http.StatusBadGateway)
			return
		}
		http.Error(w, "failed to collect tasks: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf(
		"export stats: deals_total=%d deals_with_project=%d deals_without_project=%d resolve_errors=%d projects_with_tasks=%d projects_without_tasks=%d task_load_errors=%d tasks_total=%d",
		stats.DealsTotal,
		stats.DealsWithProject,
		stats.DealsWithoutProject,
		stats.DealsResolveErrors,
		stats.ProjectsWithTasks,
		stats.ProjectsWithoutTasks,
		stats.TaskLoadErrors,
		stats.TasksTotal,
	)
	for _, issue := range issues {
		log.Printf("export issue: %s", issue)
	}

	result, err := export.BuildResultXLSX(projects, tasks)
	if err != nil {
		http.Error(w, "failed to build xlsx: "+err.Error(), http.StatusInternalServerError)
		return
	}
	filename := buildDownloadFilename(sourceLabel, dealIDs)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, filename, url.PathEscape(filename)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

func (h *Handler) dealIDs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		http.Error(w, "server is not configured: BITRIX_WEBHOOK_URL is empty", http.StatusInternalServerError)
		return
	}

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		http.Error(w, "invalid webhook: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	deals, err := bClient.ListDealIDs(ctx)
	if err != nil {
		log.Printf("deal ids load failed: err=%v", err)
		http.Error(w, "failed to load deal ids: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count": len(deals),
		"deals": deals,
	})
}

func (h *Handler) dealFields(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	webhook := strings.TrimSpace(h.cfg.Webhook)
	if webhook == "" {
		http.Error(w, "server is not configured: BITRIX_WEBHOOK_URL is empty", http.StatusInternalServerError)
		return
	}
	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		http.Error(w, "invalid webhook: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fields, err := bClient.ListDealFields(ctx)
	if err != nil {
		log.Printf("deal fields load failed: err=%v", err)
		http.Error(w, "failed to load deal fields: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"count":  len(fields),
		"fields": fields,
	})
}

func (h *Handler) loadProjects(r *http.Request, bClient *bitrix.Client, dealIDs []int) ([]model.ProjectRow, string, error) {
	file, fh, err := r.FormFile("file")
	if err == nil {
		defer file.Close()
		projects, parseErr := parser.ParseDealsInput(file)
		if parseErr != nil {
			return nil, "", fmt.Errorf("failed to parse input: %w", parseErr)
		}
		return projects, fh.Filename, nil
	}

	if err != nil && err != http.ErrMissingFile {
		return nil, "", fmt.Errorf("invalid file field: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	projects, err := bClient.GetDealsByIDs(ctx, dealIDs)
	if err != nil {
		if bitrix.IsAuthError(err) {
			return nil, "", fmt.Errorf("bitrix webhook is invalid or expired")
		}
		return nil, "", fmt.Errorf("failed to load deals from bitrix: %w", err)
	}
	label := "bitrix_api"
	if len(dealIDs) == 1 {
		label = fmt.Sprintf("bitrix_deal_%d", dealIDs[0])
	}
	if len(dealIDs) > 1 {
		label = fmt.Sprintf("bitrix_deals_%d", len(dealIDs))
	}
	return projects, label, nil
}

func parseDealIDs(dealIDsRaw, dealIDRaw string) ([]int, error) {
	parts := make([]string, 0, 8)
	if strings.TrimSpace(dealIDsRaw) != "" {
		parts = append(parts, strings.Split(dealIDsRaw, ",")...)
	} else if strings.TrimSpace(dealIDRaw) != "" {
		parts = append(parts, strings.TrimSpace(dealIDRaw))
	}
	if len(parts) == 0 {
		return nil, nil
	}
	seen := map[int]struct{}{}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		id, err := strconv.Atoi(s)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("deal_ids must contain positive integers, got %q", s)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func buildDownloadFilename(src string, dealIDs []int) string {
	base := strings.TrimSpace(src)
	base = strings.TrimSuffix(base, ".xlsx")
	base = strings.TrimSuffix(base, ".xls")
	base = strings.TrimSuffix(base, ".html")
	base = sanitizeASCII(base)
	if base == "" {
		base = "bitrix_export"
	}
	if len(dealIDs) == 1 {
		base = fmt.Sprintf("deal_%d", dealIDs[0])
	}
	if len(dealIDs) > 1 {
		base = fmt.Sprintf("deals_%d", len(dealIDs))
	}
	return base + "_passport_and_tasks.xlsx"
}

func sanitizeASCII(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	re := regexp.MustCompile(`[^a-z0-9._-]+`)
	s = re.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._-")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

const indexHTML = `<!doctype html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Паспорт проекта</title>
  <style>
    body{font-family:Arial,sans-serif;background:#f5f7fb;margin:0;padding:24px;color:#1f2937}
    .card{max-width:760px;margin:0 auto;background:#fff;border:1px solid #e5e7eb;border-radius:12px;padding:20px}
    h1{font-size:22px;margin:0 0 8px}
    p{margin:0 0 16px;color:#4b5563}
    label{display:block;margin:12px 0 6px;font-weight:600}
    input[type="text"],input[type="file"]{width:100%;padding:10px;border:1px solid #d1d5db;border-radius:8px;box-sizing:border-box}
    button{margin-top:16px;background:#2563eb;color:#fff;border:0;border-radius:8px;padding:11px 16px;font-size:14px;cursor:pointer}
    button:hover{background:#1d4ed8}
    button[disabled]{opacity:.65;cursor:not-allowed}
    .hint{font-size:12px;color:#6b7280;margin-top:6px}
    .status{margin-top:10px;font-size:13px}
    .status.muted{color:#6b7280}
    .status.work{color:#1d4ed8;font-weight:600}
  </style>
</head>
<body>
  <div class="card">
    <h1>Выгрузка “Паспорта проекта” + задач</h1>
    <p>Можно загрузить файл сделок, либо сформировать сразу из Bitrix24 по webhook.</p>
    <form id="exportForm" method="post" action="/api/export" enctype="multipart/form-data">
      <label for="deal_ids">ID сделок CRM (необязательно)</label>
      <input id="deal_ids" name="deal_ids" type="text" inputmode="text" placeholder="Например, 74331,74332,74333">
      <div class="hint">Указывать CRM ID из URL сделки: /crm/deal/details/74331/ → 74331. Значения из колонки "Идентификатор" (например 111, 222) обычно не подходят. Можно один ID или несколько через запятую.</div>

      <label for="file">Файл выгрузки сделок (.xls/.xlsx/.html, необязательно)</label>
      <input id="file" name="file" type="file">
      <div id="fileStatus" class="status muted">Файл не выбран: будет использован прямой запрос в Bitrix24.</div>

      <div class="hint">Webhook берется из конфигурации сервера (BITRIX_WEBHOOK_URL).</div>

      <button id="submitBtn" type="submit">Сформировать XLSX</button>
      <div id="progressStatus" class="status muted"></div>
    </form>
  </div>
  <script>
    (function () {
      const form = document.getElementById('exportForm');
      const fileInput = document.getElementById('file');
      const fileStatus = document.getElementById('fileStatus');
      const progressStatus = document.getElementById('progressStatus');
      const submitBtn = document.getElementById('submitBtn');

      fileInput.addEventListener('change', function () {
        if (fileInput.files && fileInput.files.length > 0) {
          const file = fileInput.files[0];
          fileStatus.textContent = 'Выбран файл: ' + file.name;
          fileStatus.className = 'status';
        } else {
          fileStatus.textContent = 'Файл не выбран: будет использован прямой запрос в Bitrix24.';
          fileStatus.className = 'status muted';
        }
      });

      function setWorkingState() {
        submitBtn.disabled = true;
        submitBtn.textContent = 'Формируем...';
        progressStatus.textContent = 'Генерируем паспорт проекта и подтягиваем задачи из Bitrix24. Это может занять 1-3 минуты.';
        progressStatus.className = 'status work';
      }

      function resetState(doneText) {
        submitBtn.disabled = false;
        submitBtn.textContent = 'Сформировать XLSX';
        progressStatus.textContent = doneText || '';
        progressStatus.className = doneText ? 'status' : 'status muted';
      }

      function parseFileName(contentDisposition) {
        if (!contentDisposition) return 'passport_tasks.xlsx';
        const utf = contentDisposition.match(/filename\\*=UTF-8''([^;]+)/i);
        if (utf && utf[1]) {
          const name = decodeURIComponent(utf[1]);
          if (!looksMojibake(name)) return name;
        }
        const plain = contentDisposition.match(/filename=\"?([^\";]+)\"?/i);
        if (plain && plain[1] && !looksMojibake(plain[1])) return plain[1];
        return 'passport_tasks.xlsx';
      }

      function looksMojibake(name) {
        return /Ð|Ñ|�|Ñ|\uFFFD/.test(name || '');
      }

      form.addEventListener('submit', async function (e) {
        e.preventDefault();
        setWorkingState();
        try {
          const formData = new FormData(form);
          const response = await fetch(form.action, {
            method: 'POST',
            body: formData
          });

          if (!response.ok) {
            const errText = await response.text();
            throw new Error(errText || ('HTTP ' + response.status));
          }

          const blob = await response.blob();
          const filename = parseFileName(response.headers.get('Content-Disposition'));
          const url = URL.createObjectURL(blob);
          const a = document.createElement('a');
          a.href = url;
          a.download = filename;
          document.body.appendChild(a);
          a.click();
          a.remove();
          URL.revokeObjectURL(url);

          resetState('Готово. Файл сформирован и скачан.');
        } catch (err) {
          console.error(err);
          resetState('Ошибка формирования файла. Проверьте логи сервера.');
        }
      });
    })();
  </script>
</body>
</html>`
