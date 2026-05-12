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
	_ = f.SetColWidth(passport, "A", "A", 6)
	_ = f.SetColWidth(passport, "B", "B", 28)
	_ = f.SetColWidth(passport, "C", "C", 42)
	_ = f.SetColWidth(passport, "D", "D", 30)
	_ = f.SetColWidth(passport, "E", "E", 30)
	_ = f.SetColWidth(passport, "F", "H", 44)
	_ = f.SetColWidth(passport, "I", "K", 24)
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
