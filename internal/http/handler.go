package httpapi

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
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

const projectFieldCode = "UF_CRM_PROJECT_GROUP_ID"

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
	projectField := projectFieldCode

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
	log.Printf("export request: file=%q deals=%d project_field=%s", fh.Filename, len(projects), projectField)

	bClient, err := bitrix.NewFromWebhook(webhook)
	if err != nil {
		http.Error(w, "invalid webhook: "+err.Error(), http.StatusBadRequest)
		return
	}

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
	filename := buildDownloadFilename(fh.Filename)
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, filename, url.PathEscape(filename)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(result)
}

func buildDownloadFilename(src string) string {
	base := strings.TrimSpace(src)
	base = strings.TrimSuffix(base, ".xlsx")
	base = strings.TrimSuffix(base, ".xls")
	base = strings.TrimSuffix(base, ".html")
	base = sanitizeASCII(base)
	if base == "" {
		base = "deals_export"
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
    <p>Загрузите файл сделок из Bitrix24 и получите итоговый XLSX.</p>
    <form id="exportForm" method="post" action="/api/export" enctype="multipart/form-data">
      <label for="file">Файл выгрузки сделок (.xls/.xlsx/.html)</label>
      <input id="file" name="file" type="file" required>
      <div id="fileStatus" class="status muted">Файл не выбран.</div>

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
          fileStatus.textContent = 'Файл не выбран.';
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
        if (!fileInput.files || fileInput.files.length === 0) {
          fileStatus.textContent = 'Сначала выберите файл.';
          fileStatus.className = 'status';
          return;
        }
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
