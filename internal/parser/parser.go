package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
	"github.com/xuri/excelize/v2"
)

func ParseDealsInput(r io.Reader) ([]model.ProjectRow, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(data, []byte("PK")) {
		return parseDealsXLSX(data)
	}
	return parseDealsHTML(data)
}

func parseDealsXLSX(data []byte) ([]model.ProjectRow, error) {
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

func parseDealsHTML(data []byte) ([]model.ProjectRow, error) {
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

func buildProjectsFromTable(rows []map[string]string) ([]model.ProjectRow, error) {
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
	var out []model.ProjectRow
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
		out = append(out, model.ProjectRow{
			Seq:          seq,
			Section:      detectSection(stage, getCell(r, "Окончание проекта")),
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

func cell(row []string, idx int) string {
	if idx < 0 || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

func hasColumn(m map[string]string, key string) bool {
	_, ok := m[key]
	return ok
}

func getCell(m map[string]string, key string) string { return strings.TrimSpace(m[key]) }

func cleanSpaces(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(stripHTML(s)), " "))
}

func stripHTML(s string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	return strings.TrimSpace(re.ReplaceAllString(s, ""))
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

func detectSection(stage, endDate string) string {
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

func extractFirstInt(s string) int {
	m := regexp.MustCompile(`\d+`).FindString(s)
	if m == "" {
		return 0
	}
	n, _ := strconv.Atoi(m)
	return n
}
