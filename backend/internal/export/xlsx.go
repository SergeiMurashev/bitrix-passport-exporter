package export

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
	"github.com/xuri/excelize/v2"
)

func BuildResultXLSX(projects []model.ProjectRow, tasks []model.TaskRow) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	_ = f.DeleteSheet("Sheet1")

	sheet := "Паспорт проекта"
	idx, _ := f.NewSheet(sheet)
	f.SetActiveSheet(idx)

	headers := []string{"№ п/п", "Инвестиционный проект", "Стадия", "Задача проекта"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, h)
	}

	_ = f.SetColWidth(sheet, "A", "A", 8)
	_ = f.SetColWidth(sheet, "B", "B", 52)
	_ = f.SetColWidth(sheet, "C", "C", 52)
	_ = f.SetColWidth(sheet, "D", "D", 52)

	tasksByDeal := buildTasksByDeal(tasks)
	sections := groupBySection(projects)

	row := 2
	num := 1
	for _, sec := range sections {
		if len(sec.projects) == 0 {
			continue
		}

		// Строка раздела (как в docApp) — объединение по B:D
		f.SetCellValue(sheet, fmt.Sprintf("B%d", row), sec.name)
		_ = f.MergeCell(sheet, fmt.Sprintf("B%d", row), fmt.Sprintf("D%d", row))
		row++

		for _, p := range sec.projects {
			f.SetCellValue(sheet, fmt.Sprintf("A%d", row), num)
			addLabeledRichText(f, sheet, fmt.Sprintf("B%d", row), []textPart{
				{value: strings.TrimSpace(p.DealTitle)},
				{label: "Местоположение: ", value: strings.TrimSpace(p.Location)},
				{label: "Организация-инвестор: ", value: strings.TrimSpace(p.Investor)},
				{label: "Объём инвестиций: ", value: strings.TrimSpace(p.InvestPlan)},
				{label: "Срок реализации: ", value: strings.TrimSpace(p.DateRange)},
				{label: "Количество рабочих мест: ", value: strings.TrimSpace(p.Jobs)},
			})
			addLabeledRichText(f, sheet, fmt.Sprintf("C%d", row), []textPart{
				{label: "Проектом предполагается:\n", value: strings.TrimSpace(p.Description)},
				{label: "Информация о стадии и ходе реализации:\n", value: strings.TrimSpace(p.Progress)},
				{label: "Предоставленная мера поддержки:\n", value: strings.TrimSpace(p.Support)},
			})
			f.SetCellValue(sheet, fmt.Sprintf("D%d", row), tasksByDeal[p.DealID])
			num++
			row++
		}
	}

	if err := styleRegistrySheet(f, sheet, row-1); err != nil {
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
	projects []model.ProjectRow
}

func groupBySection(projects []model.ProjectRow) []sectionGroup {
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
		ordered = append(ordered, sectionGroup{name: name, projects: []model.ProjectRow{p}})
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

func buildTasksByDeal(tasks []model.TaskRow) map[int]string {
	grouped := make(map[int][]model.TaskRow)
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
		out[dealID] = strings.Join(lines, "\n")
	}
	return out
}

func styleRegistrySheet(f *excelize.File, sheet string, lastRow int) error {
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
		Border:    []excelize.Border{{Type: "left", Color: "000000", Style: 1}, {Type: "top", Color: "000000", Style: 1}, {Type: "right", Color: "000000", Style: 1}, {Type: "bottom", Color: "000000", Style: 1}},
	})
	if err != nil {
		return err
	}

	_ = f.SetRowHeight(sheet, 1, 32)
	_ = f.SetCellStyle(sheet, "A1", "D1", headerStyle)
	if lastRow >= 2 {
		_ = f.SetCellStyle(sheet, "A2", fmt.Sprintf("D%d", lastRow), bodyStyle)
		_ = f.SetCellStyle(sheet, "A2", fmt.Sprintf("A%d", lastRow), centerStyle)

		for r := 2; r <= lastRow; r++ {
			bVal, _ := f.GetCellValue(sheet, fmt.Sprintf("B%d", r))
			cVal, _ := f.GetCellValue(sheet, fmt.Sprintf("C%d", r))
			dVal, _ := f.GetCellValue(sheet, fmt.Sprintf("D%d", r))
			if strings.TrimSpace(bVal) != "" && strings.TrimSpace(cVal) == "" && strings.TrimSpace(dVal) == "" {
				_ = f.SetCellStyle(sheet, fmt.Sprintf("B%d", r), fmt.Sprintf("D%d", r), sectionStyle)
				_ = f.SetRowHeight(sheet, r, 22)
				continue
			}
			_ = f.SetRowHeight(sheet, r, 112)
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
				Font: &excelize.Font{Bold: true, Family: "Arial", Size: 11},
			})
		}
		if strings.TrimSpace(p.value) != "" {
			runs = append(runs, excelize.RichTextRun{
				Text: p.value,
				Font: &excelize.Font{Family: "Arial", Size: 11},
			})
		}
		if i < len(parts)-1 {
			runs = append(runs, excelize.RichTextRun{
				Text: "\n",
				Font: &excelize.Font{Family: "Arial", Size: 11},
			})
		}
	}
	if len(runs) > 0 {
		_ = f.SetCellRichText(sheet, cell, runs)
	}
}

type textPart struct {
	label string
	value string
}
