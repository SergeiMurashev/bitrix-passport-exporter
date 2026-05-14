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

var columnAliases = map[string][]string{
	"Название сделки": {"Название сделки"},
	"Стадия сделки":   {"Стадия сделки"},
	"ID":              {"ID", "Идентификатор", "Идентификатор.", "проверка поля идентификатор 01.04"},
	"Месторасположение, адрес": {"Месторасположение, адрес", "Местоположение", "Адрес"},
	"МО": {"МО"},
	"Рабочие места (постоянные) - факт": {"Рабочие места (постоянные) - факт", "Рабочие места (постоянные)-факт"},
	"Рабочие места (постоянные) - план": {"Рабочие места (постоянные) - план", "Рабочие места (постоянные)-план"},
	"Инвестор-инициатор":                {"Инвестор-инициатор"},
	"Компания":                          {"Компания"},
	"Контакт":                           {"Контакт"},
	"Клиент":                            {"Клиент"},
	"Описание проекта":                  {"Описание проекта", "История идеи/проекта", "Цель проекта"},
	"Ход реализации проекта":            {"Ход реализации проекта", "Стадия проекта"},
	"Меры поддержки по проекту":         {"Меры поддержки по проекту", "Меры поддержки"},
	"Старт проекта":                     {"Старт проекта", "Дата начала проекта"},
	"Окончание проекта":                 {"Окончание проекта", "Дата окончания проекта"},
	"Общий объем инвестиций, план":      {"Общий объем инвестиций, план", "Общий объём инвестиций, план", "Объем инвестиций"},
	"Собственные вложения , план":       {"Собственные вложения , план", "Собственные вложения, план"},
	"Заемные средства, план":            {"Заемные средства, план", "Заемные средства , план", "Заёмные средства, план"},
}

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
	req := []string{"Название сделки", "Стадия сделки"}
	for _, c := range req {
		if !hasAnyColumn(rows[0], c) {
			return nil, fmt.Errorf("required column not found: %s", c)
		}
	}

	bySection := map[string][]model.ProjectRow{}
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
		sectionKey := detectSection(stage, getCell(r, "Окончание проекта"), cleanSpaces(getCell(r, "Ход реализации проекта")))
		investorRaw := pickFirstNonEmpty(
			getCell(r, "Инвестор-инициатор"),
			getCell(r, "Компания"),
			getCell(r, "Контакт"),
			getCell(r, "Клиент"),
		)
		bySection[sectionKey] = append(bySection[sectionKey], model.ProjectRow{
			DealID:       extractFirstInt(getCell(r, "ID")),
			DealTitle:    title,
			Location:     address,
			Investor:     investorShort(investorRaw),
			Description:  cleanDescription(cleanSpaces(getCell(r, "Описание проекта"))),
			Progress:     cleanProgress(cleanSpaces(getCell(r, "Ход реализации проекта"))),
			Support:      cleanSpaces(getCell(r, "Меры поддержки по проекту")),
			DateRange:    formatDateRange(getCell(r, "Старт проекта"), getCell(r, "Окончание проекта")),
			Jobs:         agreeJobs(jobsRaw),
			InvestPlan:   getCell(r, "Общий объем инвестиций, план"),
			OwnPlan:      getCell(r, "Собственные вложения , план"),
			LoanPlan:     getCell(r, "Заемные средства, план"),
			ProjectStage: stage,
		})
	}

	order, labels := buildSectionOrder(bySection)
	seq := 1
	out := make([]model.ProjectRow, 0, len(rows))
	for _, key := range order {
		projects := bySection[key]
		sort.SliceStable(projects, func(i, j int) bool {
			return projects[i].DealTitle < projects[j].DealTitle
		})
		for _, p := range projects {
			p.Seq = seq
			p.Section = labels[key]
			out = append(out, p)
			seq++
		}
	}
	return out, nil
}

func pickFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
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

func hasAnyColumn(m map[string]string, key string) bool {
	aliases, ok := columnAliases[key]
	if !ok {
		_, exists := m[key]
		return exists
	}
	for _, alias := range aliases {
		if _, exists := m[alias]; exists {
			return true
		}
	}
	return false
}

func getCell(m map[string]string, key string) string {
	aliases, ok := columnAliases[key]
	if !ok {
		return strings.TrimSpace(m[key])
	}
	for _, alias := range aliases {
		if v, exists := m[alias]; exists && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	for _, alias := range aliases {
		if v, exists := m[alias]; exists {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

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

func detectSection(stage, endDate, progress string) string {
	s := strings.ToLower(stage)
	currentYear := time.Now().Year()
	if strings.Contains(s, "реализован") {
		if y := regexp.MustCompile(`(\d{4})$`).FindString(strings.TrimSpace(endDate)); y != "" {
			return y
		}
		if years := regexp.MustCompile(`\d{2}[./]\d{2}[./](\d{4})`).FindAllStringSubmatch(progress, -1); len(years) > 0 {
			return years[len(years)-1][1]
		}
		if years := regexp.MustCompile(`\d{4}`).FindAllString(progress, -1); len(years) > 0 {
			return years[len(years)-1]
		}
		return strconv.Itoa(currentYear)
	}
	switch {
	case strings.Contains(s, "сопровожда"):
		return fmt.Sprintf("%d_сопровождение", currentYear)
	case strings.Contains(s, "реализуем"):
		return fmt.Sprintf("%d_реализуемые", currentYear)
	case strings.Contains(s, "планиру"):
		return fmt.Sprintf("%d_планируемые", currentYear)
	case strings.Contains(s, "исключ"):
		return "исключённые"
	default:
		return "прочие"
	}
}

func buildSectionOrder(groups map[string][]model.ProjectRow) ([]string, map[string]string) {
	currentYear := time.Now().Year()
	fixed := []string{
		fmt.Sprintf("%d_сопровождение", currentYear),
		fmt.Sprintf("%d_реализуемые", currentYear),
		fmt.Sprintf("%d_планируемые", currentYear),
		"исключённые",
		"прочие",
	}
	fixedSet := map[string]bool{}
	for _, k := range fixed {
		fixedSet[k] = true
	}

	var yearKeys []string
	for k, v := range groups {
		if len(v) == 0 || fixedSet[k] {
			continue
		}
		if regexp.MustCompile(`^\d{4}$`).MatchString(k) {
			yearKeys = append(yearKeys, k)
		}
	}
	sort.Slice(yearKeys, func(i, j int) bool {
		iy, _ := strconv.Atoi(yearKeys[i])
		jy, _ := strconv.Atoi(yearKeys[j])
		return iy < jy
	})

	order := append([]string{}, yearKeys...)
	for _, k := range fixed {
		if len(groups[k]) > 0 {
			order = append(order, k)
		}
	}

	labels := map[string]string{}
	for _, y := range yearKeys {
		labels[y] = fmt.Sprintf("Реализованные в %s г.", y)
	}
	labels[fmt.Sprintf("%d_сопровождение", currentYear)] = fmt.Sprintf("Сопровождаемые в %d г.", currentYear)
	labels[fmt.Sprintf("%d_реализуемые", currentYear)] = fmt.Sprintf("Реализуемые в %d г.", currentYear)
	labels[fmt.Sprintf("%d_планируемые", currentYear)] = fmt.Sprintf("Планируемые к реализации в %d г.", currentYear)
	labels["исключённые"] = "Исключённые из реестра"
	labels["прочие"] = "Прочие"

	return order, labels
}

func cleanDescription(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	prefixes := []string{
		`(?i)^проектом предполагается\s*:?\s*`,
		`(?i)^в рамках реализации проекта\s*:?\s*`,
		`(?i)^в рамках проекта\s*:?\s*`,
	}
	result := strings.TrimSpace(text)
	for _, p := range prefixes {
		result = regexp.MustCompile(p).ReplaceAllString(result, "")
	}
	result = strings.TrimSpace(strings.TrimLeft(result, ":;, "))
	if result == "" {
		return ""
	}
	return strings.ToUpper(result[:1]) + result[1:]
}

func cleanProgress(text string) string {
	return cleanDescription(text)
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
