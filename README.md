# bitrix-passport-exporter

Сервис выгрузки “Паспорта проекта” и задач из Bitrix24.

## Структура
- `backend/` — Go API + XLSX сборка
- `frontend/` — TS (React + Vite) web-интерфейс

## Локальный запуск

### 1) Backend
```bash
cd /Users/sergeimurashev/GolandProjects/bitrix-passport-exporter/backend
go run ./cmd/server
```

### 2) Frontend (dev)
```bash
cd /Users/sergeimurashev/GolandProjects/bitrix-passport-exporter/frontend
npm install
npm run dev
```

По умолчанию dev frontend проксирует `/api` на `http://localhost:25504`.

## Production-подход
1. Собрать фронт:
```bash
cd /Users/sergeimurashev/GolandProjects/bitrix-passport-exporter/frontend
npm run build
```
2. Запустить backend:
```bash
cd /Users/sergeimurashev/GolandProjects/bitrix-passport-exporter/backend
go run ./cmd/server
```
Backend отдает `../frontend/dist` как UI.

## Docker
```bash
cd /Users/sergeimurashev/GolandProjects/bitrix-passport-exporter
cp .env.example .env
docker compose up -d --build
```

## Переменные окружения
- `ADDR` (default `:25504`)
- `BITRIX_APP_CLIENT_ID` (обязателен, Application ID portal app)
- `BITRIX_APP_CLIENT_SECRET` (обязателен, Application key portal app)
- `TASK_WORKERS` (default `10`)
- `TASK_STRATEGY` (`per_deal` или `bulk`)
- `HTTP_READ_TIMEOUT_SECONDS` (default `20`)
- `HTTP_WRITE_TIMEOUT_SECONDS` (default `3600`)
- `HTTP_IDLE_TIMEOUT_SECONDS` (default `120`)
- `HTTP_SHUTDOWN_TIMEOUT_SECONDS` (default `20`)
- `RATE_LIMIT_EXPORT_PER_MINUTE` (default `6`, `0` = выключено)
- `SUPPORT_LINK_DEAL_FIELD` (default `UF_CRM_1770268007`)
- `SUPPORT_MEASURE_VALUE_FIELD` (default `UF_CRM_1744702884242`)
- `API_ACCESS_TOKEN` (опционально, служебный доступ к `/api/*`)

Если задан `API_ACCESS_TOKEN`, доступ к `/api/*` разрешен только с токеном.
`/healthz` и UI остаются доступными без авторизации.
При `API_ACCESS_TOKEN` UI работает автоматически: сервер выставляет `HttpOnly` cookie для запросов к `/api/*`.
Основной рабочий сценарий: открытие UI из Bitrix24 portal app, где контекст портала (`auth`) передается автоматически.

## API
- `GET /healthz`
- `GET /readyz`
- `POST /api/portal/session`
- `GET /api/portal/me`
- `GET /api/deals/ids`
- `GET /api/deals/fields`
- `POST /api/export` (`format=xlsx|docx`, default `xlsx`) — возвращает JSON-метаданные готового файла
- `GET /api/export/status`
- `POST /api/export/cancel`
- `GET /api/export/download-last` — скачивание файла

## OpenAPI
- Спецификация: [docs/openapi.yaml](docs/openapi.yaml)
- Быстрый просмотр:
  1. Откройте [Swagger Editor](https://editor.swagger.io/)
  2. `File -> Import File` и выберите `docs/openapi.yaml`
- Swagger-аннотации в коде:
  - Общая мета-информация: `backend/cmd/server/docs.go`
  - Аннотации по endpoint: `backend/internal/http/handler.go`
  - Модели ответов/ошибок: `backend/internal/model/api_docs.go`
- Генерация swagger из комментариев (swaggo):
  1. `go install github.com/swaggo/swag/cmd/swag@latest`
  2. `cd /Users/sergeimurashev/GolandProjects/bitrix-passport-exporter/backend`
  3. `swag init -g cmd/server/main.go`
