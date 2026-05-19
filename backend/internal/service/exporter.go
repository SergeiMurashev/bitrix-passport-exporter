package service

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/bitrix"
	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
)

type Exporter struct {
	bitrix      *bitrix.Client
	taskWorkers int
	strategy    string
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

func NewExporter(client *bitrix.Client, taskWorkers int, strategy string) *Exporter {
	if taskWorkers <= 0 {
		taskWorkers = 8
	}
	if strategy == "" {
		strategy = "per_deal"
	}
	return &Exporter{bitrix: client, taskWorkers: taskWorkers, strategy: strings.ToLower(strings.TrimSpace(strategy))}
}

func (e *Exporter) BuildTasks(ctx context.Context, projects []model.ProjectRow, projectField string, allowTitleFallback bool) ([]model.TaskRow, ExportStats, []string, error) {
	if e.strategy == "bulk" {
		return e.buildTasksBulk(ctx, projects, projectField, allowTitleFallback)
	}
	return e.buildTasksPerDeal(ctx, projects, projectField, allowTitleFallback)
}

func (e *Exporter) buildTasksPerDeal(ctx context.Context, projects []model.ProjectRow, projectField string, allowTitleFallback bool) ([]model.TaskRow, ExportStats, []string, error) {
	userCache := map[int]string{}
	userMu := sync.Mutex{}
	var tasks []model.TaskRow
	var issues []string
	var tasksMu sync.Mutex
	var issuesMu sync.Mutex
	var statsMu sync.Mutex
	stats := ExportStats{DealsTotal: len(projects)}

	workerCount := e.taskWorkers
	if len(projects) < workerCount {
		workerCount = len(projects)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	jobs := make(chan model.ProjectRow)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	process := func(p model.ProjectRow) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		projectID := p.ProjectID
		source := "deal.binding"

		dealTasks, err := e.bitrix.GetDealTasks(ctx, p.DealID)
		if err != nil {
			if bitrix.IsAuthError(err) {
				return fmt.Errorf("bitrix webhook auth failed: %w", err)
			}
			if bitrix.IsTimeoutError(err) || ctx.Err() != nil {
				return fmt.Errorf("bitrix timeout while loading deal tasks for deal_id=%d: %w", p.DealID, err)
			}
			statsMu.Lock()
			stats.TaskLoadErrors++
			statsMu.Unlock()
			issuesMu.Lock()
			issues = append(issues, fmt.Sprintf("deal_id=%d title=%q load deal-bound tasks error: %v", p.DealID, p.DealTitle, err))
			issuesMu.Unlock()
			dealTasks = nil
		}
		if projectID == 0 {
			resolvedProjectID, resolvedSource, resolveErr := e.bitrix.ResolveProjectForDeal(ctx, p, projectField, allowTitleFallback)
			if resolveErr != nil {
				if bitrix.IsAuthError(resolveErr) {
					return fmt.Errorf("bitrix webhook auth failed: %w", resolveErr)
				}
				if bitrix.IsTimeoutError(resolveErr) || ctx.Err() != nil {
					return fmt.Errorf("bitrix timeout while resolving project for deal_id=%d: %w", p.DealID, resolveErr)
				}
				statsMu.Lock()
				stats.DealsResolveErrors++
				statsMu.Unlock()
				issuesMu.Lock()
				issues = append(issues, fmt.Sprintf("deal_id=%d title=%q resolve project error: %v", p.DealID, p.DealTitle, resolveErr))
				issuesMu.Unlock()
			} else {
				projectID = resolvedProjectID
				source = resolvedSource
			}
		}

		var projectTasks []map[string]any
		if projectID > 0 {
			statsMu.Lock()
			stats.DealsWithProject++
			statsMu.Unlock()
			projectTasks, err = e.bitrix.GetProjectTasks(ctx, projectID)
			if err != nil {
				if bitrix.IsTimeoutError(err) || ctx.Err() != nil {
					return fmt.Errorf("bitrix timeout while loading project tasks for deal_id=%d project_id=%d: %w", p.DealID, projectID, err)
				}
				statsMu.Lock()
				stats.TaskLoadErrors++
				statsMu.Unlock()
				issuesMu.Lock()
				issues = append(issues, fmt.Sprintf("deal_id=%d project_id=%d load project tasks error: %v", p.DealID, projectID, err))
				issuesMu.Unlock()
				projectTasks = nil
			}
		} else {
			statsMu.Lock()
			stats.DealsWithoutProject++
			statsMu.Unlock()
		}

		taskPool := mergeTaskPools(dealTasks, projectTasks)
		if len(taskPool) == 0 {
			if projectID > 0 && len(projectTasks) == 0 {
				statsMu.Lock()
				stats.ProjectsWithoutTasks++
				statsMu.Unlock()
				issuesMu.Lock()
				issues = append(issues, fmt.Sprintf("deal_id=%d project_id=%d has no tasks", p.DealID, projectID))
				issuesMu.Unlock()
			}
			if len(dealTasks) == 0 {
				issuesMu.Lock()
				issues = append(issues, fmt.Sprintf("deal_id=%d title=%q has no deal-bound tasks", p.DealID, p.DealTitle))
				issuesMu.Unlock()
			}
			return nil
		}
		if projectID > 0 {
			statsMu.Lock()
			stats.ProjectsWithTasks++
			statsMu.Unlock()
		}

		localTasks := make([]model.TaskRow, 0, len(taskPool))
		for _, t := range taskPool {
			respID := toInt(anyMapGet(t, "responsibleId", "RESPONSIBLE_ID"))
			responsible := ""
			if respID > 0 {
				userMu.Lock()
				v, ok := userCache[respID]
				userMu.Unlock()
				if ok {
					responsible = v
				} else {
					name, _ := e.bitrix.GetUserName(ctx, respID)
					responsible = name
					userMu.Lock()
					userCache[respID] = name
					userMu.Unlock()
				}
			}
			statusCode := toInt(anyMapGet(t, "status", "STATUS"))
			localTasks = append(localTasks, model.TaskRow{
				DealID:      p.DealID,
				DealTitle:   p.DealTitle,
				ProjectID:   projectID,
				LinkSource:  humanizeLinkSource(source),
				TaskID:      toString(anyMapGet(t, "id", "ID")),
				TaskTitle:   toString(anyMapGet(t, "title", "TITLE")),
				Responsible: responsible,
				Deadline:    normalizeDeadline(toString(anyMapGet(t, "deadline", "DEADLINE"))),
				Status:      taskStatus(statusCode),
				Comment:     stripHTML(toString(anyMapGet(t, "description", "DESCRIPTION"))),
			})
		}
		tasksMu.Lock()
		tasks = append(tasks, localTasks...)
		tasksMu.Unlock()
		return nil
	}

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if err := process(p); err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
			}
		}()
	}

	for _, p := range projects {
		select {
		case err := <-errCh:
			close(jobs)
			wg.Wait()
			return nil, stats, issues, err
		default:
		}
		jobs <- p
	}
	close(jobs)
	wg.Wait()

	select {
	case err := <-errCh:
		return nil, stats, issues, err
	default:
	}

	stats.TasksTotal = len(tasks)
	return tasks, stats, issues, nil
}

func (e *Exporter) buildTasksBulk(ctx context.Context, projects []model.ProjectRow, projectField string, allowTitleFallback bool) ([]model.TaskRow, ExportStats, []string, error) {
	_ = allowTitleFallback
	stats := ExportStats{DealsTotal: len(projects)}
	var tasks []model.TaskRow
	var issues []string
	userCache := map[int]string{}

	allTasks, err := e.bitrix.GetAllTasks(ctx)
	if err != nil {
		if bitrix.IsTimeoutError(err) || ctx.Err() != nil {
			return nil, stats, issues, fmt.Errorf("bitrix timeout while loading all tasks: %w", err)
		}
		return nil, stats, issues, fmt.Errorf("failed to load all tasks: %w", err)
	}
	dealTaskIndex := map[int][]map[string]any{}
	projectTaskIndex := map[int][]map[string]any{}
	for _, t := range allTasks {
		for _, did := range extractDealBindings(t) {
			dealTaskIndex[did] = append(dealTaskIndex[did], t)
		}
		gid := toInt(anyMapGet(t, "groupId", "GROUP_ID"))
		if gid > 0 {
			projectTaskIndex[gid] = append(projectTaskIndex[gid], t)
		}
	}

	projectToDeals := map[int][]model.ProjectRow{}
	var unresolved []model.ProjectRow
	for _, p := range projects {
		if p.ProjectID > 0 {
			projectToDeals[p.ProjectID] = append(projectToDeals[p.ProjectID], p)
		} else {
			unresolved = append(unresolved, p)
		}
	}

	for projectID, deals := range projectToDeals {
		projectHasTasks := false
		for _, d := range deals {
			projectTasks := projectTaskIndex[projectID]
			dealTasks := dealTaskIndex[d.DealID]
			taskPool := mergeTaskPools(dealTasks, projectTasks)
			if len(taskPool) == 0 {
				stats.DealsWithoutProject++
				issues = append(issues, fmt.Sprintf("deal_id=%d project_id=%d has no tasks", d.DealID, projectID))
				continue
			}
			projectHasTasks = true
			stats.DealsWithProject++
			for _, t := range taskPool {
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
					DealID:      d.DealID,
					DealTitle:   d.DealTitle,
					ProjectID:   projectID,
					LinkSource:  detectLinkSource(t, d.DealID, projectID, projectField),
					TaskID:      toString(anyMapGet(t, "id", "ID")),
					TaskTitle:   toString(anyMapGet(t, "title", "TITLE")),
					Responsible: responsible,
					Deadline:    normalizeDeadline(toString(anyMapGet(t, "deadline", "DEADLINE"))),
					Status:      taskStatus(statusCode),
					Comment:     stripHTML(toString(anyMapGet(t, "description", "DESCRIPTION"))),
				})
			}
		}
		if projectHasTasks {
			stats.ProjectsWithTasks++
		} else {
			stats.ProjectsWithoutTasks++
		}
	}

	// Для bulk-режима больше не делаем per-deal fallback с дополнительными API-вызовами:
	// матчим только по уже загруженному общему пулу задач портала.
	if len(unresolved) > 0 {
		for _, d := range unresolved {
			dealTasks := dealTaskIndex[d.DealID]
			if len(dealTasks) == 0 {
				stats.DealsWithoutProject++
				continue
			}
			for _, t := range dealTasks {
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
					DealID:      d.DealID,
					DealTitle:   d.DealTitle,
					ProjectID:   0,
					LinkSource:  humanizeLinkSource("deal.binding"),
					TaskID:      toString(anyMapGet(t, "id", "ID")),
					TaskTitle:   toString(anyMapGet(t, "title", "TITLE")),
					Responsible: responsible,
					Deadline:    normalizeDeadline(toString(anyMapGet(t, "deadline", "DEADLINE"))),
					Status:      taskStatus(statusCode),
					Comment:     stripHTML(toString(anyMapGet(t, "description", "DESCRIPTION"))),
				})
			}
		}
	}

	stats.TasksTotal = len(tasks)
	return tasks, stats, issues, nil
}

func extractDealBindings(task map[string]any) []int {
	out := make([]int, 0, 2)
	addOne := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		for _, m := range regexp.MustCompile(`D_(\d+)`).FindAllStringSubmatch(v, -1) {
			if len(m) < 2 {
				continue
			}
			id, err := strconv.Atoi(m[1])
			if err == nil && id > 0 {
				out = append(out, id)
			}
		}
	}
	addAny := func(raw any) {
		switch v := raw.(type) {
		case []any:
			for _, x := range v {
				addOne(toString(x))
			}
		case []string:
			for _, x := range v {
				addOne(x)
			}
		default:
			addOne(toString(v))
		}
	}
	addAny(anyMapGet(task, "UF_CRM_TASK", "ufCrmTask"))
	addAny(anyMapGet(task, "CRM_BINDING", "crmBinding"))
	return uniqueInts(out)
}

func uniqueInts(items []int) []int {
	if len(items) == 0 {
		return items
	}
	seen := map[int]struct{}{}
	out := make([]int, 0, len(items))
	for _, v := range items {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func mergeTaskPools(a, b []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(a)+len(b))
	seen := map[string]struct{}{}
	push := func(items []map[string]any) {
		for _, t := range items {
			id := toString(anyMapGet(t, "id", "ID"))
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, t)
		}
	}
	push(a)
	push(b)
	return out
}

func detectLinkSource(task map[string]any, dealID int, projectID int, projectField string) string {
	for _, d := range extractDealBindings(task) {
		if d == dealID {
			return humanizeLinkSource("deal.binding")
		}
	}
	if projectID > 0 {
		return humanizeLinkSource("deal." + projectField)
	}
	return humanizeLinkSource("deal.binding")
}

func humanizeLinkSource(source string) string {
	s := strings.TrimSpace(strings.ToLower(source))
	switch {
	case s == "deal.binding":
		return "Связь через сделку"
	case strings.HasPrefix(s, "deal."):
		return "Связь через проект, привязанный к сделке"
	case strings.HasPrefix(s, "sonet_group.get"):
		return "Связь через проект по названию сделки"
	default:
		return "Связь через сделку"
	}
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
