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

## API
- `GET /healthz`
- `GET /api/deals/ids`
- `GET /api/deals/fields`
- `POST /api/export`
