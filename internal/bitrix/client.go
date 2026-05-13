package bitrix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
)

type Client struct {
	endpoint string
	http     *http.Client
}

type DealShort struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

type DealField struct {
	Code  string `json:"code"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type bitrixResponse struct {
	Result any    `json:"result"`
	Next   any    `json:"next"`
	Error  string `json:"error"`
	Desc   string `json:"error_description"`
}

func NewFromWebhook(webhook string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(webhook))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "rest" {
		return nil, fmt.Errorf("unexpected webhook path: %s", u.Path)
	}
	endpoint := fmt.Sprintf("%s://%s/rest/%s/%s", u.Scheme, u.Host, parts[1], parts[2])
	return &Client{
		endpoint: endpoint,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func (c *Client) ResolveProjectForDeal(ctx context.Context, p model.ProjectRow, projectField string) (int, string, error) {
	if p.ProjectID > 0 {
		return p.ProjectID, "deal." + projectField, nil
	}
	if p.DealID > 0 {
		resp, err := c.callWithRetry(ctx, "crm.deal.get", map[string]any{"id": p.DealID})
		if err == nil {
			resultMap, _ := resp.Result.(map[string]any)
			if gid := toInt(fmt.Sprintf("%v", resultMap[projectField])); gid > 0 {
				return gid, "deal." + projectField, nil
			}
		} else if IsAuthError(err) {
			return 0, "", err
		}
	}

	resp, err := c.callWithRetry(ctx, "sonet_group.get", map[string]any{
		"FILTER": map[string]any{"NAME": p.DealTitle},
		"SELECT": []string{"ID", "NAME"},
	})
	if err != nil {
		return 0, "", err
	}
	groups := toSliceMap(resp.Result)
	if len(groups) == 0 {
		return 0, "", nil
	}
	if len(groups) > 1 {
		return 0, "", fmt.Errorf("found %d projects by title %q; ambiguous fallback", len(groups), p.DealTitle)
	}
	return toInt(fmt.Sprintf("%v", groups[0]["ID"])), "sonet_group.get(NAME)", nil
}

func (c *Client) GetDeals(ctx context.Context, dealID int) ([]model.ProjectRow, error) {
	if dealID > 0 {
		return c.GetDealsByIDs(ctx, []int{dealID})
	}
	return c.GetDealsByIDs(ctx, nil)
}

func (c *Client) GetDealsByIDs(ctx context.Context, ids []int) ([]model.ProjectRow, error) {
	if len(ids) > 0 {
		out := make([]model.ProjectRow, 0, len(ids))
		for _, id := range ids {
			if id <= 0 {
				continue
			}
			resp, err := c.callWithRetry(ctx, "crm.deal.get", map[string]any{"id": id})
			if err != nil {
				return nil, err
			}
			deal, _ := resp.Result.(map[string]any)
			if len(deal) == 0 {
				continue
			}
			out = append(out, mapDealToProjectRow(deal))
		}
		for i := range out {
			out[i].Seq = i + 1
		}
		return out, nil
	}

	start := 0
	const maxPages = 10000
	page := 0
	var out []model.ProjectRow
	for {
		page++
		if page > maxPages {
			return nil, fmt.Errorf("deals pagination exceeded %d pages", maxPages)
		}
		resp, err := c.callWithRetry(ctx, "crm.deal.list", map[string]any{
			"select": []string{
				"ID",
				"TITLE",
				"STAGE_ID",
				"COMMENTS",
				"UF_CRM_PROJECT_GROUP_ID",
				"UF_CRM_1739951854", // fallback: field from XLS export often used as "Ход реализации проекта"
				"BEGINDATE",
				"CLOSEDATE",
			},
			"order": map[string]string{"ID": "ASC"},
			"start": start,
		})
		if err != nil {
			return nil, err
		}

		items := toSliceMap(resp.Result)
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			out = append(out, mapDealToProjectRow(item))
		}

		next := toInt(fmt.Sprintf("%v", resp.Next))
		if next == 0 || next <= start {
			break
		}
		start = next
	}

	for i := range out {
		out[i].Seq = i + 1
	}
	return out, nil
}

func (c *Client) ListDealIDs(ctx context.Context) ([]DealShort, error) {
	start := 0
	const maxPages = 10000
	page := 0
	out := make([]DealShort, 0, 256)

	for {
		page++
		if page > maxPages {
			return nil, fmt.Errorf("deals ids pagination exceeded %d pages", maxPages)
		}

		resp, err := c.callWithRetry(ctx, "crm.deal.list", map[string]any{
			"select": []string{"ID", "TITLE"},
			"order":  map[string]string{"ID": "ASC"},
			"start":  start,
		})
		if err != nil {
			return nil, err
		}

		items := toSliceMap(resp.Result)
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			id := toInt(toString(anyMapGet(item, "ID", "id")))
			if id <= 0 {
				continue
			}
			out = append(out, DealShort{
				ID:    id,
				Title: strings.TrimSpace(toString(anyMapGet(item, "TITLE", "title"))),
			})
		}

		next := toInt(fmt.Sprintf("%v", resp.Next))
		if next == 0 || next <= start {
			break
		}
		start = next
	}

	return out, nil
}

func (c *Client) ListDealFields(ctx context.Context) ([]DealField, error) {
	resp, err := c.callWithRetry(ctx, "crm.deal.fields", nil)
	if err != nil {
		return nil, err
	}
	fieldsMap, ok := resp.Result.(map[string]any)
	if !ok {
		return nil, nil
	}
	out := make([]DealField, 0, len(fieldsMap))
	for code, raw := range fieldsMap {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		title := strings.TrimSpace(toString(item["title"]))
		if title == "" {
			title = strings.TrimSpace(toString(item["formLabel"]))
		}
		out = append(out, DealField{
			Code:  code,
			Title: title,
			Type:  strings.TrimSpace(toString(item["type"])),
		})
	}
	return out, nil
}

func (c *Client) GetProjectTasks(ctx context.Context, groupID int) ([]map[string]any, error) {
	start := 0
	const maxPages = 10000
	page := 0
	var all []map[string]any

	for {
		page++
		if page > maxPages {
			return nil, fmt.Errorf("project tasks pagination exceeded %d pages", maxPages)
		}
		resp, err := c.callWithRetry(ctx, "tasks.task.list", map[string]any{
			"filter": map[string]any{"GROUP_ID": groupID},
			"select": []string{"ID", "TITLE", "RESPONSIBLE_ID", "DEADLINE", "STATUS", "DESCRIPTION"},
			"start":  start,
		})
		if err != nil {
			return nil, err
		}

		resultMap, _ := resp.Result.(map[string]any)
		chunk := toSliceMap(resultMap["tasks"])
		if len(chunk) == 0 {
			chunk = toSliceMap(resultMap["items"])
		}
		all = append(all, chunk...)

		next := toInt(fmt.Sprintf("%v", resultMap["next"]))
		if next == 0 || next <= start || len(chunk) == 0 {
			break
		}
		start = next
	}

	return all, nil
}

func (c *Client) GetDealTasks(ctx context.Context, dealID int) ([]map[string]any, error) {
	if dealID <= 0 {
		return nil, nil
	}

	// Bitrix Portal различаются: некоторые используют UF_CRM_TASK, некоторые полагаются на фильтры привязки CRM.
	variants := []map[string]any{
		{"UF_CRM_TASK": fmt.Sprintf("D_%d", dealID)},
		{"CRM_BINDING": fmt.Sprintf("D_%d", dealID)},
		{"UF_CRM_TASK": dealID},
	}

	merged := make([]map[string]any, 0)
	seen := make(map[string]struct{})
	targetBinding := fmt.Sprintf("D_%d", dealID)
	for _, filter := range variants {
		chunk, err := c.getTasksByFilter(ctx, filter)
		if err != nil {
			return nil, err
		}
		for _, t := range chunk {
			if !taskBoundToDeal(t, targetBinding) {
				continue
			}
			id := toString(anyMapGet(t, "id", "ID"))
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			merged = append(merged, t)
		}
	}

	return merged, nil
}

func (c *Client) getTasksByFilter(ctx context.Context, filter map[string]any) ([]map[string]any, error) {
	start := 0
	const maxPages = 10000
	page := 0
	var all []map[string]any

	for {
		page++
		if page > maxPages {
			return nil, fmt.Errorf("tasks pagination exceeded %d pages", maxPages)
		}
		resp, err := c.callWithRetry(ctx, "tasks.task.list", map[string]any{
			"filter": filter,
			"select": []string{"ID", "TITLE", "RESPONSIBLE_ID", "DEADLINE", "STATUS", "DESCRIPTION", "UF_CRM_TASK", "CRM_BINDING"},
			"start":  start,
		})
		if err != nil {
			return nil, err
		}

		resultMap, _ := resp.Result.(map[string]any)
		chunk := toSliceMap(resultMap["tasks"])
		if len(chunk) == 0 {
			chunk = toSliceMap(resultMap["items"])
		}
		all = append(all, chunk...)

		next := toInt(fmt.Sprintf("%v", resultMap["next"]))
		if next == 0 || next <= start || len(chunk) == 0 {
			break
		}
		start = next
	}

	return all, nil
}

func (c *Client) GetUserName(ctx context.Context, userID int) (string, error) {
	resp, err := c.callWithRetry(ctx, "user.get", map[string]any{
		"FILTER": map[string]any{"ID": userID},
	})
	if err != nil {
		return strconv.Itoa(userID), err
	}

	users := toSliceMap(resp.Result)
	if len(users) == 0 {
		return strconv.Itoa(userID), nil
	}
	u := users[0]
	name := strings.TrimSpace(strings.Join([]string{toString(u["LAST_NAME"]), toString(u["NAME"]), toString(u["SECOND_NAME"])}, " "))
	if name == "" {
		return strconv.Itoa(userID), nil
	}
	return name, nil
}

func (c *Client) callWithRetry(ctx context.Context, method string, params map[string]any) (*bitrixResponse, error) {
	const maxAttempts = 5
	backoff := 300 * time.Millisecond
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := c.call(ctx, method, params)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if IsAuthError(err) || !isRateLimitError(err) || attempt == maxAttempts {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
		}
	}

	return nil, lastErr
}

func (c *Client) call(ctx context.Context, method string, params map[string]any) (*bitrixResponse, error) {
	endpoint := fmt.Sprintf("%s/%s.json", c.endpoint, method)

	var body io.Reader
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params: %w", err)
		}
		body = bytes.NewBuffer(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var out bitrixResponse
	_ = json.Unmarshal(payload, &out)
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" || out.Desc != "" {
			return nil, fmt.Errorf("bitrix %s http %d: %s (%s)", method, resp.StatusCode, out.Error, out.Desc)
		}
		return nil, fmt.Errorf("bitrix %s http %d: %s", method, resp.StatusCode, string(payload))
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("decode response for %s: %w", method, err)
	}
	if out.Error != "" {
		if out.Desc != "" {
			return nil, fmt.Errorf("bitrix %s error: %s (%s)", method, out.Error, out.Desc)
		}
		return nil, fmt.Errorf("bitrix %s error: %s", method, out.Error)
	}

	return &out, nil
}

func IsAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "invalid_token") ||
		strings.Contains(s, "unable to get application by token") ||
		strings.Contains(s, "invalid_credentials")
}

func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "query_limit_exceeded") || strings.Contains(s, "too many requests")
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

func toString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func toInt(s string) int {
	m := regexp.MustCompile(`\d+`).FindString(s)
	if m == "" {
		return 0
	}
	n, _ := strconv.Atoi(m)
	return n
}

func anyMapGet(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func mapDealToProjectRow(deal map[string]any) model.ProjectRow {
	stage := strings.TrimSpace(toString(anyMapGet(deal, "STAGE_ID", "stageId")))
	progress := strings.TrimSpace(toString(anyMapGet(deal, "UF_CRM_1739951854")))
	if progress == "" {
		progress = strings.TrimSpace(toString(anyMapGet(deal, "COMMENTS", "comments")))
	}

	return model.ProjectRow{
		DealID:       toInt(toString(anyMapGet(deal, "ID", "id"))),
		ProjectID:    toInt(toString(anyMapGet(deal, "UF_CRM_PROJECT_GROUP_ID"))),
		DealTitle:    strings.TrimSpace(toString(anyMapGet(deal, "TITLE", "title"))),
		Description:  strings.TrimSpace(toString(anyMapGet(deal, "COMMENTS", "comments"))),
		ProjectStage: stage,
		Progress:     progress,
		DateRange: strings.TrimSpace(strings.TrimSpace(toString(anyMapGet(deal, "BEGINDATE"))) +
			func() string {
				end := strings.TrimSpace(toString(anyMapGet(deal, "CLOSEDATE")))
				if end == "" {
					return ""
				}
				return " - " + end
			}()),
	}
}

func taskBoundToDeal(task map[string]any, binding string) bool {
	binding = strings.ToUpper(strings.TrimSpace(binding))
	if binding == "" {
		return false
	}

	candidates := []any{
		anyMapGet(task, "ufCrmTask", "UF_CRM_TASK"),
		anyMapGet(task, "crmBinding", "CRM_BINDING"),
	}

	for _, c := range candidates {
		for _, token := range extractBindingTokens(c) {
			if strings.EqualFold(token, binding) {
				return true
			}
		}
	}

	return false
}

func extractBindingTokens(v any) []string {
	if v == nil {
		return nil
	}

	switch vv := v.(type) {
	case []any:
		var out []string
		for _, item := range vv {
			out = append(out, extractBindingTokens(item)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, item := range vv {
			out = append(out, extractBindingTokens(item)...)
		}
		return out
	default:
		s := strings.ToUpper(toString(vv))
		re := regexp.MustCompile(`[A-Z]_\d+`)
		return re.FindAllString(s, -1)
	}
}
