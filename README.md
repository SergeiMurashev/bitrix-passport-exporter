# bitrix24-project-passport-exporter

Мини-сервер на Go для выгрузки "Паспорта проекта" из Bitrix24 с задачами по сделкам.

## Что реализовано
- Backend API `POST /api/export`.
- Входные форматы: `xlsx` и HTML-таблица (`.xls` из Bitrix как HTML).
- Парсинг реестра сделок в структуру "паспорта" (колонки, секции, нормализация полей).
- Для каждой сделки:
    - поиск связанного проекта (`UF_CRM_PROJECT_GROUP_ID`),
    - fallback по имени проекта (`sonet_group.get`),
    - выгрузка задач проекта (`tasks.task.list`) с пагинацией,
    - обогащение ответственными (`user.get`).
- Выход: один XLSX с листами:
    - `Паспорт проекта`
    - `Задачи проекта`
- Интеграция с Bitrix через библиотеку `github.com/kurerid/bixgo`.

## Запуск
```bash
cd /Users/sergeimurashev/GolandProjects/docapp-go
go run .
```

Слушает `:8080` (можно переопределить `ADDR`).

## API
`POST /api/export` (`multipart/form-data`)
- `file` — исходный файл выгрузки сделок (`.xlsx` или html-xls)
- `webhook` — входящий webhook Bitrix24
- `project_field_code` — опционально, по умолчанию `UF_CRM_PROJECT_GROUP_ID`

Пример:
```bash
curl -X POST 'http://localhost:8080/api/export' \
  -F 'file=@/absolute/path/deals.xls' \
  -F 'webhook=https://<portal>.bitrix24.ru/rest/<user_id>/<webhook_key>/' \
  --output passport_tasks.xlsx
```

## Проверка
- `GET /healthz` -> `ok`
