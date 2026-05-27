package export

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/models"
	"github.com/xuri/excelize/v2"
)

// BuildResultXLSX сборщик XLSX-файл с результатами экспорта паспортов проектов из Битрикс24.
func BuildResultXLSX(
	projects []models.ProjectRow,
	tasks []models.TaskRow) ([]byte, error) {
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
			f.SetCellValue(sheet, fmt.Sprintf("B%d", row), renderLabeledText(projectParts))
			f.SetCellValue(sheet, fmt.Sprintf("C%d", row), renderLabeledText(stageParts))
			if taskText != "" {
				f.SetCellValue(sheet, fmt.Sprintf("D%d", row), taskText)
			}
			if support != "" {
				f.SetCellValue(sheet, fmt.Sprintf("E%d", row), support)
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

// Регистрация стилей для листа с реестром проектов и задач.
func styleRegistrySheet(
	f *excelize.File,
	sheet string,
	lastRow int,
	sectionRows map[int]struct{}) error {
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
	_ = f.SetPanes(sheet, &excelize.Panes{
		Freeze:      true,
		Split:       false,
		XSplit:      0,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft"},
	)
	return nil
}

type textPart struct {
	label string
	value string
	bold  bool
}

func renderLabeledText(parts []textPart) string {
	lines := make([]string, 0, len(parts))
	for _, p := range parts {
		label := p.label
		value := strings.TrimSpace(p.value)
		if label == "" && value == "" {
			continue
		}
		line := label + value
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
