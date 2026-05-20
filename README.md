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
- `BITRIX_WEBHOOK_URL` (обязателен)
- `TASK_WORKERS` (default `10`)
- `TASK_STRATEGY` (`per_deal` или `bulk`)
- `SUPPORT_LINK_DEAL_FIELD` (default `UF_CRM_1770268007`)
- `SUPPORT_MEASURE_VALUE_FIELD` (default `UF_CRM_1744702884242`)
- `API_ACCESS_TOKEN` (опционально, защищает `/api/*`)

Если задан `API_ACCESS_TOKEN`, доступ к `/api/*` разрешен только с токеном.
`/healthz` и UI остаются доступными без авторизации.
При `API_ACCESS_TOKEN` UI работает автоматически: сервер выставляет `HttpOnly` cookie для запросов к `/api/*`.

## API
- `GET /healthz`
- `GET /api/deals/ids`
- `GET /api/deals/fields`
- `POST /api/export`
