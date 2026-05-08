package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/kurerid/bixgo"
	"github.com/xuri/excelize/v2"
)

type projectRow struct {
	Seq          int
	Section      string
	DealID       int
	DealTitle    string
	Location     string
	Investor     string
	Description  string
	Progress     string
	Support      string
	DateRange    string
	Jobs         string
	InvestPlan   string
	OwnPlan      string
	LoanPlan     string
	ProjectStage string
}

type taskRow struct {
	DealID      int
	DealTitle   string
	ProjectID   int
	LinkSource  string
	TaskID      string
	TaskTitle   string
	Responsible string
	Deadline    string
	Status      string
	Comment     string
}

var taskStatusMap = map[int]string{
	1: "Новая",
	2: "Ждет выполнения",
	3: "Выполняется",
	4: "Ожидает контроля",
	5: "Завершена",
	6: "Отложена",
	7: "Отклонена",
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/", uiHandler)
	mux.HandleFunc("/api/export", exportHandler)

	addr := env("ADDR", ":8080")
	log.Printf("bitrix-passport-exporter listen on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func uiHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
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

      <label for="webhook">Webhook Bitrix24</label>
      <input id="webhook" name="webhook" type="text" placeholder="https://portal.bitrix24.ru/rest/<user>/<key>/" required>
      <div class="hint">Нужны права CRM + Задачи + Рабочие группы + Пользователи.</div>

      <label for="project_field_code">Код поля связи сделка → проект (опционально)</label>
      <input id="project_field_code" name="project_field_code" type="text" value="UF_CRM_PROJECT_GROUP_ID">

      <button type="submit">Сформировать XLSX</button>
    </form>
  </div>
</body>
</html>`))
}

func exportHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "invalid multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	webhook := strings.TrimSpace(r.FormValue("webhook"))
	if webhook == "" {
		http.Error(w, "webhook is required", http.StatusBadRequest)
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

	projects, err := parseDealsInput(file)
	if err != nil {
		http.Error(w, "failed to parse input: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(projects) == 0 {
		http.Error(w, "no projects found", http.StatusBadRequest)
		return
	}

	client, err := newBitrixClientFromWebhook(webhook)
	if err != nil {
		http.Error(w, "invalid webhook: "+err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
	defer cancel()

	userCache := map[int]string{}
	var tasks []taskRow
	for _, p := range projects {
		projectID, source, err := resolveProjectForDeal(ctx, client, p, projectField)
		if err != nil {
			tasks = append(tasks, taskRow{DealID: p.DealID, DealTitle: p.DealTitle, Comment: "Ошибка: " + err.Error()})
			continue
		}
		if projectID == 0 {
			tasks = append(tasks, taskRow{DealID: p.DealID, DealTitle: p.DealTitle, Comment: "Не найден связанный проект"})
			continue
		}

		projectTasks, err := getProjectTasks(ctx, client, projectID)
		if err != nil {
			tasks = append(tasks, taskRow{DealID: p.DealID, DealTitle: p.DealTitle, ProjectID: projectID, LinkSource: source, Comment: "Ошибка задач: " + err.Error()})
			continue
		}
		if len(projectTasks) == 0 {
			tasks = append(tasks, taskRow{DealID: p.DealID, DealTitle: p.DealTitle, ProjectID: projectID, LinkSource: source, Comment: "Задачи не найдены"})
			continue
		}

		for _, t := range projectTasks {
			respID := toInt(anyMapGet(t, "responsibleId", "RESPONSIBLE_ID"))
			responsible := ""
			if respID > 0 {
				if v, ok := userCache[respID]; ok {
					responsible = v
				} else {
					name, _ := getUserName(ctx, client, respID)
					responsible = name
					userCache[respID] = name
				}
			}
			statusCode := toInt(anyMapGet(t, "status", "STATUS"))
			tasks = append(tasks, taskRow{
				DealID:      p.DealID,
				DealTitle:   p.DealTitle,
				ProjectID:   projectID,
				LinkSource:  source,
				TaskID:      toString(anyMapGet(t, "id", "ID")),
				TaskTitle:   toString(anyMapGet(t, "title", "TITLE")),
				Responsible: responsible,
				Deadline:    normalizeDeadline(toString(anyMapGet(t, "deadline", "DEADLINE"))),
				Status:      taskStatusMap[statusCode],
				Comment:     stripHTML(toString(anyMapGet(t, "description", "DESCRIPTION"))),
			})
		}
	}

	result, err := buildResultXLSX(projects, tasks)
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

func parseDealsInput(r io.Reader) ([]projectRow, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(data, []byte("PK")) {
		return parseDealsXLSX(data)
	}
	return parseDealsHTML(data)
}

func parseDealsXLSX(data []byte) ([]projectRow, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sheet := f.GetSheetName(0)
	if sheet == "" {
		return nil, errors.New("xlsx has no sheets")
	}
	rows, err := f.GetRows(sheet)
	if err != nil {
		return nil, err
	}
	return buildProjectsFromTable(rowsToMaps(rows))
}

func parseDealsHTML(data []byte) ([]projectRow, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var headers []string
	var rows []map[string]string
	doc.Find("table").First().Find("tr").Each(func(i int, tr *goquery.Selection) {
		var cols []string
		tr.Find("th,td").Each(func(_ int, c *goquery.Selection) {
			cols = append(cols, strings.TrimSpace(c.Text()))
		})
		if len(cols) == 0 {
			return
		}
		if i == 0 {
			headers = cols
			return
		}
		m := map[string]string{}
		for j, h := range headers {
			if j < len(cols) {
				m[h] = strings.TrimSpace(cols[j])
			}
		}
		rows = append(rows, m)
	})
	if len(rows) == 0 {
		return nil, errors.New("no rows in html table")
	}
	return buildProjectsFromTable(rows)
}

func rowsToMaps(rows [][]string) []map[string]string {
	if len(rows) < 2 {
		return nil
	}
	headers := rows[0]
	var out []map[string]string
	for _, row := range rows[1:] {
		m := map[string]string{}
		nonEmpty := false
		for i, h := range headers {
			v := cell(row, i)
			m[h] = v
			if strings.TrimSpace(v) != "" {
				nonEmpty = true
			}
		}
		if nonEmpty {
			out = append(out, m)
		}
	}
	return out
}

func buildProjectsFromTable(rows []map[string]string) ([]projectRow, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	req := []string{"Название сделки", "Стадия сделки", "Инвестор-инициатор"}
	for _, c := range req {
		if !hasColumn(rows[0], c) {
			return nil, fmt.Errorf("required column not found: %s", c)
		}
	}

	seq := 1
	var out []projectRow
	for _, r := range rows {
		title := getCell(r, "Название сделки")
		if title == "" {
			continue
		}
		stage := getCell(r, "Стадия сделки")
		address := strings.TrimSpace(regexp.MustCompile(`\s*\(,\s*\)\s*$`).ReplaceAllString(getCell(r, "Месторасположение, адрес"), ""))
		if address == "" {
			address = getCell(r, "МО")
		}
		jobsRaw := getCell(r, "Рабочие места (постоянные) - факт")
		if jobsRaw == "" || jobsRaw == "0" {
			jobsRaw = getCell(r, "Рабочие места (постоянные) - план")
		}
		out = append(out, projectRow{
			Seq:          seq,
			Section:      detectSection(stage, getCell(r, "Ход реализации проекта"), getCell(r, "Окончание проекта")),
			DealID:       extractFirstInt(getCell(r, "ID")),
			DealTitle:    title,
			Location:     address,
			Investor:     investorShort(getCell(r, "Инвестор-инициатор")),
			Description:  cleanSpaces(getCell(r, "Описание проекта")),
			Progress:     cleanSpaces(getCell(r, "Ход реализации проекта")),
			Support:      cleanSpaces(getCell(r, "Меры поддержки по проекту")),
			DateRange:    formatDateRange(getCell(r, "Старт проекта"), getCell(r, "Окончание проекта")),
			Jobs:         agreeJobs(jobsRaw),
			InvestPlan:   getCell(r, "Общий объем инвестиций, план"),
			OwnPlan:      getCell(r, "Собственные вложения , план"),
			LoanPlan:     getCell(r, "Заемные средства, план"),
			ProjectStage: stage,
		})
		seq++
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Section == out[j].Section {
			return out[i].DealTitle < out[j].DealTitle
		}
		return out[i].Section < out[j].Section
	})
	return out, nil
}

func resolveProjectForDeal(ctx context.Context, c *bixgo.Client, p projectRow, projectField string) (int, string, error) {
	if p.DealID > 0 {
		var resp bixgo.Response[map[string]any]
		err := c.Call(ctx, "crm.deal.get", bixgo.Params{"id": p.DealID}, &resp)
		if err == nil {
			if gid := extractFirstInt(toString(resp.Result[projectField])); gid > 0 {
				return gid, "deal." + projectField, nil
			}
		}
	}
	var grpResp bixgo.Response[[]map[string]any]
	err := c.Call(ctx, "sonet_group.get", bixgo.Params{
		"FILTER": bixgo.Params{"NAME": p.DealTitle},
		"SELECT": []string{"ID", "NAME"},
	}, &grpResp)
	if err != nil {
		return 0, "", err
	}
	if len(grpResp.Result) == 0 {
		return 0, "", nil
	}
	return extractFirstInt(toString(grpResp.Result[0]["ID"])), "sonet_group.get(NAME)", nil
}

func getProjectTasks(ctx context.Context, c *bixgo.Client, groupID int) ([]map[string]any, error) {
	start := 0
	var all []map[string]any
	for {
		var resp bixgo.Response[map[string]any]
		err := c.Call(ctx, "tasks.task.list", bixgo.Params{
			"filter": bixgo.Params{"GROUP_ID": groupID},
			"select": []string{"ID", "TITLE", "RESPONSIBLE_ID", "DEADLINE", "STATUS", "DESCRIPTION"},
			"start":  start,
		}, &resp)
		if err != nil {
			return nil, err
		}
		chunk := toSliceMap(resp.Result["tasks"])
		if len(chunk) == 0 {
			chunk = toSliceMap(resp.Result["items"])
		}
		all = append(all, chunk...)
		next := toInt(resp.Result["next"])
		if next == 0 || len(chunk) == 0 {
			break
		}
		start = next
	}
	return all, nil
}

func getUserName(ctx context.Context, c *bixgo.Client, userID int) (string, error) {
	var resp bixgo.Response[[]map[string]any]
	err := c.Call(ctx, "user.get", bixgo.Params{"FILTER": bixgo.Params{"ID": userID}}, &resp)
	if err != nil || len(resp.Result) == 0 {
		return strconv.Itoa(userID), err
	}
	u := resp.Result[0]
	name := strings.TrimSpace(strings.Join([]string{toString(u["LAST_NAME"]), toString(u["NAME"]), toString(u["SECOND_NAME"])}, " "))
	if name == "" {
		return strconv.Itoa(userID), nil
	}
	return name, nil
}

func buildResultXLSX(projects []projectRow, tasks []taskRow) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	passport := "Паспорт проекта"
	idx, _ := f.NewSheet(passport)
	f.SetActiveSheet(idx)
	headers := []string{"№", "Секция", "ID сделки", "Название сделки", "Местоположение", "Инвестор", "Описание", "Ход реализации", "Мера поддержки", "Срок реализации", "Рабочие места", "Объем инвестиций (план)", "Собственные вложения (план)", "Заемные средства (план)", "Стадия сделки"}
	for i, h := range headers {
		cellName, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(passport, cellName, h)
	}
	for i, p := range projects {
		r := i + 2
		f.SetCellValue(passport, fmt.Sprintf("A%d", r), p.Seq)
		f.SetCellValue(passport, fmt.Sprintf("B%d", r), p.Section)
		f.SetCellValue(passport, fmt.Sprintf("C%d", r), p.DealID)
		f.SetCellValue(passport, fmt.Sprintf("D%d", r), p.DealTitle)
		f.SetCellValue(passport, fmt.Sprintf("E%d", r), p.Location)
		f.SetCellValue(passport, fmt.Sprintf("F%d", r), p.Investor)
		f.SetCellValue(passport, fmt.Sprintf("G%d", r), p.Description)
		f.SetCellValue(passport, fmt.Sprintf("H%d", r), p.Progress)
		f.SetCellValue(passport, fmt.Sprintf("I%d", r), p.Support)
		f.SetCellValue(passport, fmt.Sprintf("J%d", r), p.DateRange)
		f.SetCellValue(passport, fmt.Sprintf("K%d", r), p.Jobs)
		f.SetCellValue(passport, fmt.Sprintf("L%d", r), p.InvestPlan)
		f.SetCellValue(passport, fmt.Sprintf("M%d", r), p.OwnPlan)
		f.SetCellValue(passport, fmt.Sprintf("N%d", r), p.LoanPlan)
		f.SetCellValue(passport, fmt.Sprintf("O%d", r), p.ProjectStage)
	}

	tasksSheet := "Задачи проекта"
	_, _ = f.NewSheet(tasksSheet)
	th := []string{"ID сделки", "Сделка", "ID проекта", "Источник связи", "ID задачи", "Название", "Ответственный", "Срок", "Статус", "Комментарий"}
	for i, h := range th {
		cellName, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(tasksSheet, cellName, h)
	}
	for i, t := range tasks {
		r := i + 2
		f.SetCellValue(tasksSheet, fmt.Sprintf("A%d", r), t.DealID)
		f.SetCellValue(tasksSheet, fmt.Sprintf("B%d", r), t.DealTitle)
		f.SetCellValue(tasksSheet, fmt.Sprintf("C%d", r), t.ProjectID)
		f.SetCellValue(tasksSheet, fmt.Sprintf("D%d", r), t.LinkSource)
		f.SetCellValue(tasksSheet, fmt.Sprintf("E%d", r), t.TaskID)
		f.SetCellValue(tasksSheet, fmt.Sprintf("F%d", r), t.TaskTitle)
		f.SetCellValue(tasksSheet, fmt.Sprintf("G%d", r), t.Responsible)
		f.SetCellValue(tasksSheet, fmt.Sprintf("H%d", r), t.Deadline)
		f.SetCellValue(tasksSheet, fmt.Sprintf("I%d", r), t.Status)
		f.SetCellValue(tasksSheet, fmt.Sprintf("J%d", r), t.Comment)
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func newBitrixClientFromWebhook(webhook string) (*bixgo.Client, error) {
	u, err := url.Parse(webhook)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "rest" {
		return nil, fmt.Errorf("unexpected webhook path: %s", u.Path)
	}
	authToken := parts[1] + "/" + parts[2]
	baseURL := u.Scheme + "://" + u.Host
	auth := bixgo.NewClientAuth(authToken, "", time.Now().Add(365*24*time.Hour), "", "")
	return bixgo.NewClient(baseURL, auth), nil
}

func normalizeDeadline(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("02.01.2006 15:04")
	}
	return s
}

func stripHTML(s string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	return strings.TrimSpace(re.ReplaceAllString(s, ""))
}

func cell(row []string, idx int) string {
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

func extractFirstInt(s string) int {
	m := regexp.MustCompile(`\d+`).FindString(s)
	if m == "" {
		return 0
	}
	n, _ := strconv.Atoi(m)
	return n
}

func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.Itoa(int(t))
	case int:
		return strconv.Itoa(t)
	case map[string]any:
		if vv, ok := t["value"]; ok {
			return toString(vv)
		}
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func toInt(v any) int { return extractFirstInt(toString(v)) }

func anyMapGet(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func toSliceMap(v any) []map[string]any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func hasColumn(m map[string]string, key string) bool {
	_, ok := m[key]
	return ok
}

func getCell(m map[string]string, key string) string { return strings.TrimSpace(m[key]) }

func cleanSpaces(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(stripHTML(s)), " "))
}

func investorShort(s string) string {
	re := regexp.MustCompile(`\s*([\d\-\+\(\)]{7,}|@\S+|\d{10,})\s*.*`)
	t := strings.TrimSpace(re.ReplaceAllString(s, ""))
	parts := strings.Split(t, ",")
	if len(parts) > 0 {
		return strings.TrimSpace(parts[0])
	}
	return t
}

func detectSection(stage, _progress, endDate string) string {
	s := strings.ToLower(stage)
	if strings.Contains(s, "реализован") {
		if y := regexp.MustCompile(`(\d{4})$`).FindString(strings.TrimSpace(endDate)); y != "" {
			return y
		}
		return strconv.Itoa(time.Now().Year())
	}
	switch {
	case strings.Contains(s, "сопровожда"):
		return "2026_сопровождение"
	case strings.Contains(s, "реализуем"):
		return "2026_реализуемые"
	case strings.Contains(s, "планиру"):
		return "2026_планируемые"
	case strings.Contains(s, "исключ"):
		return "исключённые"
	default:
		return "прочие"
	}
}

func formatDateRange(start, end string) string {
	year := func(s string) int {
		m := regexp.MustCompile(`(\d{4})$`).FindString(strings.TrimSpace(s))
		if m == "" {
			return 0
		}
		n, _ := strconv.Atoi(m)
		return n
	}
	sy, ey := year(start), year(end)
	if sy > 0 && ey > 0 {
		if sy > ey {
			return strconv.Itoa(ey)
		}
		if sy == ey {
			return strconv.Itoa(sy)
		}
		return fmt.Sprintf("%d - %d", sy, ey)
	}
	if ey > 0 {
		return strconv.Itoa(ey)
	}
	if sy > 0 {
		return strconv.Itoa(sy)
	}
	return ""
}

func agreeJobs(v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	if s == "" || s == "0" || strings.Contains(s, "не план") || strings.Contains(s, "не предусмотр") {
		return ""
	}
	n := extractFirstInt(s)
	if n == 0 {
		return ""
	}
	word := "рабочих мест"
	if n%10 == 1 && n%100 != 11 {
		word = "рабочее место"
	} else if n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20) {
		word = "рабочих места"
	}
	return fmt.Sprintf("%d %s", n, word)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
