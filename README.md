# bitrix-passport-exporter

Мини-сервер на Go для выгрузки "Паспорта проекта" из Bitrix24 с задачами по сделкам.

## Архитектура
Структура проекта в backend-стиле (`cmd` + `internal`):
- `cmd/server` — entrypoint приложения
- `internal/http` — HTTP-роуты и обработчики
- `internal/service` — бизнес-логика экспорта
- `internal/bitrix` — клиент Bitrix24 (через `bixgo`)
- `internal/parser` — парсинг входной выгрузки (`.xlsx` и html-xls)
- `internal/export` — сборка итогового XLSX
- `internal/model` — доменные модели
- `internal/config` — конфиг окружения

## Что реализовано
- UI в браузере: `GET /`
- Healthcheck: `GET /healthz`
- Export endpoint: `POST /api/export`
- Для каждой сделки:
  - поиск проекта по `UF_CRM_PROJECT_GROUP_ID` (fallback по названию)
  - выгрузка задач проекта (`tasks.task.list`) с пагинацией
  - привязка задач к нужной сделке/проекту
- Выход: один XLSX с листами:
  - `Паспорт проекта`
  - `Задачи проекта`

## Запуск
```bash
cd /Users/sergeimurashev/GolandProjects/bitrix-passport-exporter
go run ./cmd/server
```

По умолчанию слушает `:8080` (`ADDR` можно переопределить).

## API
`POST /api/export` (`multipart/form-data`):
- `file` — файл выгрузки сделок (`.xlsx` или html-xls)
- `webhook` — входящий webhook Bitrix24
- `project_field_code` — опционально, по умолчанию `UF_CRM_PROJECT_GROUP_ID`

Пример:
```bash
curl -X POST 'http://localhost:8080/api/export' \
  -F 'file=@/absolute/path/deals.xls' \
  -F 'webhook=https://<portal>.bitrix24.ru/rest/<user_id>/<webhook_key>/' \
  --output passport_tasks.xlsx
```
