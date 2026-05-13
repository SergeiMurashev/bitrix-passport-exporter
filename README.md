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
- Deals IDs endpoint: `GET /api/deals/ids`
- Deal fields endpoint: `GET /api/deals/fields`
- Export endpoint: `POST /api/export`
- Источник сделок:
  - прямой импорт из Bitrix24 API (по webhook, без пользовательского файла)
  - совместимый fallback: загрузка файла `.xlsx` или html-xls
- Для каждой сделки:
  - поиск проекта по `UF_CRM_PROJECT_GROUP_ID` (fallback по названию)
  - выгрузка задач проекта (`tasks.task.list`) с пагинацией
  - привязка задач к нужной сделке/проекту
- Выход: один XLSX с листами:
  - `Паспорт проекта`
  - `Задачи проекта`

## Конфигурация
Через переменные окружения:
- `ADDR` — адрес сервера (по умолчанию `:8080`)
- `BITRIX_WEBHOOK_URL` — webhook Bitrix24 (обязательный)

Пример:
```bash
export ADDR=:8080
export BITRIX_WEBHOOK_URL='https://<portal>.bitrix24.ru/rest/<user_id>/<webhook_key>/'
```

## Запуск
```bash
cd /Users/sergeimurashev/GolandProjects/bitrix-passport-exporter
go run ./cmd/server
```

## API
`GET /api/deals/ids`:
- возвращает список всех CRM сделок с полями `id` и `title`
- удобно, чтобы взять правильный `deal_id` для точечной выгрузки

Пример:
```bash
curl 'http://localhost:8080/api/deals/ids'
```

`GET /api/deals/fields`:
- возвращает поля сделки (`code`, `title`, `type`)
- нужно для сверки, какие `UF_CRM_*` маппить в паспорт

Пример:
```bash
curl 'http://localhost:8080/api/deals/fields'
```

`POST /api/export` (`multipart/form-data`):
- `deal_ids` (опционально) — один ID или несколько CRM ID через запятую
- `deal_id` (опционально, legacy) — один CRM ID
- `file` (опционально) — файл выгрузки сделок (`.xlsx` или html-xls)
- если `file` не передан, сделки будут загружены напрямую из Bitrix24 API
- код поля связи сделка → проект фиксирован в backend: `UF_CRM_PROJECT_GROUP_ID`

Пример:
```bash
curl -X POST 'http://localhost:8080/api/export' \
  -F 'file=@/absolute/path/deals.xls' \
  --output passport_tasks.xlsx
```

Пример без файла (все сделки из Bitrix):
```bash
curl -X POST 'http://localhost:8080/api/export' \
  --output passport_tasks.xlsx
```

Пример без файла (одна сделка):
```bash
curl -X POST 'http://localhost:8080/api/export' \
  -F 'deal_id=12345' \
  --output passport_tasks.xlsx
```

Пример без файла (несколько сделок):
```bash
curl -X POST 'http://localhost:8080/api/export' \
  -F 'deal_ids=12345,12346,12347' \
  --output passport_tasks.xlsx
```
