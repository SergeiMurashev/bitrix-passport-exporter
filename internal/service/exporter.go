package service

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
)

type Exporter struct {
	bitrix *bitrix.Client
}

type ExportStats struct {
	DealsTotal           int
	DealsWithProject     int
	DealsWithoutProject  int
	DealsResolveErrors   int
	ProjectsWithTasks    int
	ProjectsWithoutTasks int
	TasksTotal           int
	TaskLoadErrors       int
}

func NewExporter(client *bitrix.Client) *Exporter {
	return &Exporter{bitrix: client}
}

func (e *Exporter) BuildTasks(ctx context.Context, projects []model.ProjectRow, projectField string) ([]model.TaskRow, ExportStats, []string, error) {
	userCache := map[int]string{}
	var tasks []model.TaskRow
	var issues []string
	stats := ExportStats{DealsTotal: len(projects)}

	for _, p := range projects {
		projectID := 0
		source := "deal.binding"

		projectTasks, err := e.bitrix.GetDealTasks(ctx, p.DealID)
		if err != nil {
			if bitrix.IsAuthError(err) {
				return nil, stats, issues, fmt.Errorf("bitrix webhook auth failed: %w", err)
			}
			stats.TaskLoadErrors++
			issues = append(issues, fmt.Sprintf("deal_id=%d title=%q load deal-bound tasks error: %v", p.DealID, p.DealTitle, err))
			projectTasks = nil
		}

		if len(projectTasks) == 0 {
			resolvedProjectID, resolvedSource, resolveErr := e.bitrix.ResolveProjectForDeal(ctx, p, projectField)
			if resolveErr != nil {
				if bitrix.IsAuthError(resolveErr) {
					return nil, stats, issues, fmt.Errorf("bitrix webhook auth failed: %w", resolveErr)
				}
				stats.DealsResolveErrors++
				issues = append(issues, fmt.Sprintf("deal_id=%d title=%q resolve project error: %v", p.DealID, p.DealTitle, resolveErr))
				continue
			}

			projectID = resolvedProjectID
			source = resolvedSource
			if projectID == 0 {
				stats.DealsWithoutProject++
				issues = append(issues, fmt.Sprintf("deal_id=%d title=%q linked project not found and deal-bound tasks are empty", p.DealID, p.DealTitle))
				continue
			}
			stats.DealsWithProject++

			projectTasks, err = e.bitrix.GetProjectTasks(ctx, projectID)
			if err != nil {
				stats.TaskLoadErrors++
				issues = append(issues, fmt.Sprintf("deal_id=%d project_id=%d load project tasks error: %v", p.DealID, projectID, err))
				continue
			}
		}

		if len(projectTasks) == 0 {
			if projectID > 0 {
				stats.ProjectsWithoutTasks++
				issues = append(issues, fmt.Sprintf("deal_id=%d project_id=%d has no tasks", p.DealID, projectID))
			} else {
				issues = append(issues, fmt.Sprintf("deal_id=%d title=%q has no deal-bound tasks", p.DealID, p.DealTitle))
			}
			continue
		}
		if projectID > 0 {
			stats.ProjectsWithTasks++
		}

		for _, t := range projectTasks {
			respID := toInt(anyMapGet(t, "responsibleId", "RESPONSIBLE_ID"))
			responsible := ""
			if respID > 0 {
				if v, ok := userCache[respID]; ok {
					responsible = v
				} else {
					name, _ := e.bitrix.GetUserName(ctx, respID)
					responsible = name
					userCache[respID] = name
				}
			}
			statusCode := toInt(anyMapGet(t, "status", "STATUS"))
			tasks = append(tasks, model.TaskRow{
				DealID:      p.DealID,
				DealTitle:   p.DealTitle,
				ProjectID:   projectID,
				LinkSource:  source,
				TaskID:      toString(anyMapGet(t, "id", "ID")),
				TaskTitle:   toString(anyMapGet(t, "title", "TITLE")),
				Responsible: responsible,
				Deadline:    normalizeDeadline(toString(anyMapGet(t, "deadline", "DEADLINE"))),
				Status:      taskStatus(statusCode),
				Comment:     stripHTML(toString(anyMapGet(t, "description", "DESCRIPTION"))),
			})
		}
	}
	stats.TasksTotal = len(tasks)
	return tasks, stats, issues, nil
}

func taskStatus(code int) string {
	m := map[int]string{1: "Новая", 2: "Ждет выполнения", 3: "Выполняется", 4: "Ожидает контроля", 5: "Завершена", 6: "Отложена", 7: "Отклонена"}
	if status, ok := m[code]; ok {
		return status
	}
	if code == 0 {
		return ""
	}
	return strconv.Itoa(code)
}

func normalizeDeadline(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return s
}

func stripHTML(s string) string {
	re := regexp.MustCompile(`<[^>]+>`)
	return strings.TrimSpace(re.ReplaceAllString(s, ""))
}

func anyMapGet(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func toInt(v any) int {
	s := toString(v)
	m := regexp.MustCompile(`\d+`).FindString(s)
	if m == "" {
		return 0
	}
	n, _ := strconv.Atoi(m)
	return n
}
