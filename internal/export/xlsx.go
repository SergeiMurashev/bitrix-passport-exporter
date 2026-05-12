package export

import (
	"fmt"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
	"github.com/xuri/excelize/v2"
)

func BuildResultXLSX(projects []model.ProjectRow, tasks []model.TaskRow) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	_ = f.DeleteSheet("Sheet1")

	passport := "Паспорт проекта"
	idx, _ := f.NewSheet(passport)
	f.SetActiveSheet(idx)
	headers := []string{"№", "Секция", "Название сделки", "Местоположение", "Инвестор", "Описание", "Ход реализации", "Мера поддержки", "Срок реализации", "Рабочие места", "Объем инвестиций (план)"}
	for i, h := range headers {
		cellName, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(passport, cellName, h)
	}
	_ = f.SetColWidth(passport, "A", "A", 5)
	_ = f.SetColWidth(passport, "B", "B", 22)
	_ = f.SetColWidth(passport, "C", "C", 28)
	_ = f.SetColWidth(passport, "D", "D", 20)
	_ = f.SetColWidth(passport, "E", "E", 20)
	_ = f.SetColWidth(passport, "F", "H", 28)
	_ = f.SetColWidth(passport, "I", "K", 16)
	for i, p := range projects {
		r := i + 2
		f.SetCellValue(passport, fmt.Sprintf("A%d", r), p.Seq)
		f.SetCellValue(passport, fmt.Sprintf("B%d", r), p.Section)
		f.SetCellValue(passport, fmt.Sprintf("C%d", r), p.DealTitle)
		f.SetCellValue(passport, fmt.Sprintf("D%d", r), p.Location)
		f.SetCellValue(passport, fmt.Sprintf("E%d", r), p.Investor)
		f.SetCellValue(passport, fmt.Sprintf("F%d", r), p.Description)
		f.SetCellValue(passport, fmt.Sprintf("G%d", r), p.Progress)
		f.SetCellValue(passport, fmt.Sprintf("H%d", r), p.Support)
		f.SetCellValue(passport, fmt.Sprintf("I%d", r), p.DateRange)
		f.SetCellValue(passport, fmt.Sprintf("J%d", r), p.Jobs)
		f.SetCellValue(passport, fmt.Sprintf("K%d", r), p.InvestPlan)
	}
	if err := stylePassportSheet(f, passport, len(projects)+1); err != nil {
		return nil, err
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
	if err := styleTasksSheet(f, tasksSheet, len(tasks)+1); err != nil {
		return nil, err
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func stylePassportSheet(f *excelize.File, sheet string, lastRow int) error {
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
	centerColsStyle, err := f.NewStyle(&excelize.Style{
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "top", WrapText: true},
		Border:    []excelize.Border{{Type: "left", Color: "000000", Style: 1}, {Type: "top", Color: "000000", Style: 1}, {Type: "right", Color: "000000", Style: 1}, {Type: "bottom", Color: "000000", Style: 1}},
	})
	if err != nil {
		return err
	}

	_ = f.SetRowHeight(sheet, 1, 30)
	_ = f.SetCellStyle(sheet, "A1", "K1", headerStyle)
	if lastRow >= 2 {
		_ = f.SetCellStyle(sheet, "A2", fmt.Sprintf("K%d", lastRow), bodyStyle)
		_ = f.SetCellStyle(sheet, "A2", fmt.Sprintf("A%d", lastRow), centerColsStyle)
		_ = f.SetCellStyle(sheet, "I2", fmt.Sprintf("K%d", lastRow), centerColsStyle)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, Split: false, XSplit: 0, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	return nil
}

func styleTasksSheet(f *excelize.File, sheet string, lastRow int) error {
	_ = f.SetColWidth(sheet, "A", "A", 10)
	_ = f.SetColWidth(sheet, "B", "B", 28)
	_ = f.SetColWidth(sheet, "C", "D", 14)
	_ = f.SetColWidth(sheet, "E", "F", 16)
	_ = f.SetColWidth(sheet, "G", "I", 20)
	_ = f.SetColWidth(sheet, "J", "J", 32)

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

	_ = f.SetRowHeight(sheet, 1, 24)
	_ = f.SetCellStyle(sheet, "A1", "J1", headerStyle)
	if lastRow >= 2 {
		_ = f.SetCellStyle(sheet, "A2", fmt.Sprintf("J%d", lastRow), bodyStyle)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, Split: false, XSplit: 0, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"})
	return nil
}
