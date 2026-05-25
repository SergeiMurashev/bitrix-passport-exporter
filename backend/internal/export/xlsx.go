package export

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/models"
	"github.com/xuri/excelize/v2"
)

var (
	richFontNormal = &excelize.Font{Family: "Arial", Size: 11}
	richFontBold   = &excelize.Font{Bold: true, Family: "Arial", Size: 11}
)

func BuildResultXLSX(projects []models.ProjectRow, tasks []models.TaskRow) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	_ = f.DeleteSheet("Sheet1")

	sheet := "Паспорт проекта"
	idx, _ := f.NewSheet(sheet)
	f.SetActiveSheet(idx)

	headers := []string{"№ п/п", "Инвестиционный проект", "Стадия", "Задача проекта", "Меры поддержки по проекту"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
	}

	_ = f.SetColWidth(sheet, "A", "A", 8)
	_ = f.SetColWidth(sheet, "B", "B", 52)
	_ = f.SetColWidth(sheet, "C", "C", 52)
	_ = f.SetColWidth(sheet, "D", "D", 48)
	_ = f.SetColWidth(sheet, "E", "E", 42)

	tasksByDeal := buildTasksByDeal(tasks)
	sections := groupBySection(projects)

	row := 2
	num := 1
	sectionRows := make(map[int]struct{}, len(sections))
	for _, sec := range sections {
		if len(sec.projects) == 0 {
			continue
		}

		f.SetCellValue(sheet, fmt.Sprintf("B%d", row), sec.name)
		_ = f.MergeCell(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("E%d", row))
		sectionRows[row] = struct{}{}
		row++

		for _, p := range sec.projects {
			dealTitle := strings.TrimSpace(p.DealTitle)
			location := strings.TrimSpace(p.Location)
			investor := strings.TrimSpace(p.Investor)
			investPlan := strings.TrimSpace(p.InvestPlan)
			dateRange := strings.TrimSpace(p.DateRange)
			jobs := strings.TrimSpace(p.Jobs)
			description := strings.TrimSpace(p.Description)
			progress := strings.TrimSpace(p.Progress)
			support := strings.TrimSpace(p.Support)
			taskText := tasksByDeal[p.DealID]

			f.SetCellValue(sheet, fmt.Sprintf("A%d", row), num)
			projectParts := []textPart{
				{value: dealTitle, bold: true},
				{label: "Местоположение: ", value: location},
				{label: "Организация-инвестор: ", value: investor},
				{label: "Объём инвестиций: ", value: investPlan},
				{label: "Срок реализации: ", value: dateRange},
				{label: "Количество рабочих мест: ", value: jobs},
			}
			stageParts := []textPart{
				{label: "Проектом предполагается:\n", value: description},
				{label: "Информация о стадии и ходе реализации:\n", value: progress},
			}
			addLabeledRichText(f, sheet, fmt.Sprintf("B%d", row), projectParts)
			addLabeledRichText(f, sheet, fmt.Sprintf("C%d", row), stageParts)
			if taskText != "" {
				f.SetCellValue(sheet, fmt.Sprintf("D%d", row), taskText)
			}
			if support != "" {
				f.SetCellValue(sheet, fmt.Sprintf("E%d", row), support)
			}
			h := estimateRowHeight(projectParts, stageParts, taskText, support)
			if err := f.SetRowHeight(sheet, row, h); err != nil {
				// Защита от ограничений Excel/Numbers по высоте строки.
				_ = f.SetRowHeight(sheet, row, 409)
			}
			num++
			row++
		}
	}

	if err := styleRegistrySheet(f, sheet, row-1, sectionRows); err != nil {
		return nil, err
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type sectionGroup struct {
	name     string
	projects []models.ProjectRow
}

func groupBySection(projects []models.ProjectRow) []sectionGroup {
	ordered := make([]sectionGroup, 0, 12)
	idx := make(map[string]int, 12)
	for _, p := range projects {
		name := strings.TrimSpace(p.Section)
		if name == "" {
			name = "Прочие"
		}
		if i, ok := idx[name]; ok {
			ordered[i].projects = append(ordered[i].projects, p)
			continue
		}
		idx[name] = len(ordered)
		ordered = append(ordered, sectionGroup{name: name, projects: []models.ProjectRow{p}})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		wi, yi := sectionOrderKey(ordered[i].name)
		wj, yj := sectionOrderKey(ordered[j].name)
		if wi != wj {
			return wi < wj
		}
		if yi != yj {
			return yi < yj
		}
		return ordered[i].name < ordered[j].name
	})
	return ordered
}

func sectionOrderKey(name string) (weight int, year int) {
	s := strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(s, "реализован") {
		re := regexp.MustCompile(`(20\d{2})`)
		if m := re.FindStringSubmatch(s); len(m) == 2 {
			y, _ := strconv.Atoi(m[1])
			return 0, y
		}
		return 0, 9999
	}
	switch {
	case strings.Contains(s, "сопровожда"):
		return 1, 0
	case strings.Contains(s, "реализуем"):
		return 2, 0
	case strings.Contains(s, "планируем"):
		return 3, 0
	case strings.Contains(s, "исключ"):
		return 4, 0
	default:
		return 5, 0
	}
}

func buildTasksByDeal(tasks []models.TaskRow) map[int]string {
	grouped := make(map[int][]models.TaskRow)
	for _, t := range tasks {
		grouped[t.DealID] = append(grouped[t.DealID], t)
	}

	out := make(map[int]string, len(grouped))
	for dealID, list := range grouped {
		sort.SliceStable(list, func(i, j int) bool {
			return list[i].TaskID < list[j].TaskID
		})
		lines := make([]string, 0, len(list))
		seen := make(map[string]struct{}, len(list))
		for _, t := range list {
			title := strings.TrimSpace(t.TaskTitle)
			if title == "" {
				continue
			}
			line := title
			if strings.TrimSpace(t.Responsible) != "" {
				line += " (ответственный: " + strings.TrimSpace(t.Responsible) + ")"
			}
			if strings.TrimSpace(t.Deadline) != "" {
				line += " (срок: " + strings.TrimSpace(t.Deadline) + ")"
			}
			if strings.TrimSpace(t.Status) != "" {
				line += " [" + strings.TrimSpace(t.Status) + "]"
			}
			if _, ok := seen[line]; ok {
				continue
			}
			seen[line] = struct{}{}
			lines = append(lines, line)
		}
		out[dealID] = strings.Join(lines, "\n\n")
	}
	return out
}

func styleRegistrySheet(f *excelize.File, sheet string, lastRow int, sectionRows map[int]struct{}) error {
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#E6E6E6"}, Pattern: 1},
		Border:    []excelize.Border{{Type: "left", Color: "000000", Style: 1}, {Type: "top", Color: "000000", Style: 1}, {Type: "right", Color: "000000", Style: 1}, {Type: "bottom", Color: "000000", Style: 1}},
	})
	if err != nil {
		return err
	}
	bodyStyle, err := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
		Border:    []excelize.Border{{Type: "left", Color: "000000", Style: 1}, {Type: "top", Color: "000000", Style: 1}, {Type: "right", Color: "000000", Style: 1}, {Type: "bottom", Color: "000000", Style: 1}},
	})
	if err != nil {
		return err
	}
	centerStyle, err := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "top", WrapText: true},
		Border:    []excelize.Border{{Type: "left", Color: "000000", Style: 1}, {Type: "top", Color: "000000", Style: 1}, {Type: "right", Color: "000000", Style: 1}, {Type: "bottom", Color: "000000", Style: 1}},
	})
	if err != nil {
		return err
	}
	sectionStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center", WrapText: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"#F2F2F2"}, Pattern: 1},
		Border:    []excelize.Border{{Type: "left", Color: "000000", Style: 1}, {Type: "top", Color: "000000", Style: 1}, {Type: "right", Color: "000000", Style: 1}, {Type: "bottom", Color: "000000", Style: 1}},
	})
	if err != nil {
		return err
	}

	_ = f.SetRowHeight(sheet, 1, 32)
	_ = f.SetCellStyle(sheet, "A1", "E1", headerStyle)
	if lastRow >= 2 {
		_ = f.SetCellStyle(sheet, "A2", fmt.Sprintf("E%d", lastRow), bodyStyle)
		_ = f.SetCellStyle(sheet, "A2", fmt.Sprintf("A%d", lastRow), centerStyle)

		for r := 2; r <= lastRow; r++ {
			if _, ok := sectionRows[r]; !ok {
				continue
			}
			_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", r), fmt.Sprintf("E%d", r), sectionStyle)
			_ = f.SetRowHeight(sheet, r, 22)
		}
	}
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, Split: false, XSplit: 0, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	return nil
}

func addLabeledRichText(f *excelize.File, sheet, cell string, parts []textPart) {
	runs := make([]excelize.RichTextRun, 0, len(parts)*2)
	for i, p := range parts {
		if strings.TrimSpace(p.label) != "" {
			runs = append(runs, excelize.RichTextRun{
				Text: p.label,
				Font: richFontBold,
			})
		}
		valueText := strings.TrimSpace(p.value)
		if i < len(parts)-1 {
			valueText += "\n"
		}
		runs = append(runs, excelize.RichTextRun{
			Text: valueText,
			Font: choosePartFont(p.bold),
		})
	}
	if len(runs) > 0 {
		_ = f.SetCellRichText(sheet, cell, runs)
	}
}

func choosePartFont(isBold bool) *excelize.Font {
	if isBold {
		return richFontBold
	}
	return richFontNormal
}

type textPart struct {
	label string
	value string
	bold  bool
}

// Функция по оценки высоты строк
func estimateRowHeight(projectParts, stageParts []textPart, taskText, supportText string) float64 {
	// Приблизительно округляю строки по ширине столбца, чтобы пользователям не приходилось растягивать их вручную.
	// В Numbers/Excel авто-подбор высоты для rich text работает по-разному,
	// поэтому берем более "консервативную" оценку и допускаем большие значения.
	lines := wrappedTextLinesParts(projectParts, 42)
	if v := wrappedTextLinesParts(stageParts, 42); v > lines {
		lines = v
	}
	if v := wrappedTextLines(taskText, 42); v > lines {
		lines = v
	}
	if v := wrappedTextLines(supportText, 34); v > lines {
		lines = v
	}
	if lines < 3 {
		lines = 3
	}
	if lines > 30 {
		lines = 30
	}
	h := float64(lines)*13 + 8
	if h > 409 {
		return 409
	}
	return h
}

func wrappedTextLinesParts(parts []textPart, charsPerLine int) int {
	if charsPerLine <= 0 {
		charsPerLine = 52
	}
	total := 0
	for _, p := range parts {
		line := strings.TrimSpace(p.label) + strings.TrimSpace(p.value)
		total += wrappedLineSegments(line, charsPerLine)
	}
	if total < 1 {
		return 1
	}
	return total
}

func wrappedTextLines(s string, charsPerLine int) int {
	if charsPerLine <= 0 {
		charsPerLine = 52
	}
	if strings.TrimSpace(s) == "" {
		return 0
	}
	total := 0
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '\n' {
			continue
		}
		line := strings.TrimSpace(s[start:i])
		total += wrappedLineSegments(line, charsPerLine)
		start = i + 1
	}
	if total < 1 {
		return 1
	}
	return total
}

func wrappedLineSegments(line string, charsPerLine int) int {
	if strings.TrimSpace(line) == "" {
		return 1
	}
	runeCount := utf8.RuneCountInString(line)
	segments := (runeCount + charsPerLine - 1) / charsPerLine
	if segments < 1 {
		return 1
	}
	return segments
}
