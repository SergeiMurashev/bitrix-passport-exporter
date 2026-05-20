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
	endpoint                 string
	http                     *http.Client
	supportLinkDealField     string
	supportMeasureValueField string
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

type stageMeta struct {
	Name      string
	Semantics string
}

type DealsPage struct {
	Rows  []model.ProjectRow
	Next  int
	Total int
}

const (
	dealFieldIndustryRange   = "UF_CRM_1744702843092"
	dealFieldIndustry        = "UF_CRM_1744702862817"
	dealFieldSupportMeasure  = "UF_CRM_1744702884242"
	dealFieldMunicipality    = "UF_CRM_1744714512203"
	dealFieldInvestor        = "UF_CRM_1744714565237"
	dealFieldDescription     = "UF_CRM_1744714620"
	dealFieldJobsPlan        = "UF_CRM_1744714707223"
	dealFieldAddress         = "UF_CRM_1744715247985"
	dealFieldIdentifier      = "UF_CRM_1744715286104"
	dealFieldInvestTotalPlan = "UF_CRM_1750245213304"
	dealFieldOwnPlan         = "UF_CRM_1750245220917"
	dealFieldLoanPlan        = "UF_CRM_1750245230997"
)

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
			Timeout: 90 * time.Second,
		},
		supportMeasureValueField: dealFieldSupportMeasure,
	}, nil
}

func (c *Client) ConfigureSupportMapping(linkDealField, measureValueField string) {
	c.supportLinkDealField = strings.TrimSpace(linkDealField)
	if strings.TrimSpace(measureValueField) != "" {
		c.supportMeasureValueField = strings.TrimSpace(measureValueField)
	}
}

func (c *Client) dealSelectFields(extra ...string) []string {
	fields := []string{
		"ID",
		"TITLE",
		"STAGE_ID",
		"COMMENTS",
		"UF_CRM_PROJECT_GROUP_ID",
		"UF_CRM_1739951854",
		dealFieldIndustryRange,
		dealFieldIndustry,
		dealFieldSupportMeasure,
		dealFieldMunicipality,
		dealFieldInvestor,
		dealFieldDescription,
		dealFieldJobsPlan,
		dealFieldAddress,
		dealFieldIdentifier,
		dealFieldInvestTotalPlan,
		dealFieldOwnPlan,
		dealFieldLoanPlan,
		"BEGINDATE",
		"CLOSEDATE",
	}
	if v := strings.TrimSpace(c.supportLinkDealField); v != "" {
		fields = append(fields, v)
	}
	fields = append(fields, extra...)
	uniq := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		key := strings.TrimSpace(f)
		if key == "" {
			continue
		}
		if _, ok := uniq[key]; ok {
			continue
		}
		uniq[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func (c *Client) ResolveProjectForDeal(ctx context.Context, p model.ProjectRow, projectField string, allowTitleFallback bool) (int, string, error) {
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

	if !allowTitleFallback {
		return 0, "", nil
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

func (c *Client) GetDealsPage(ctx context.Context, start int, limit int) (DealsPage, error) {
	enumLabels, _ := c.loadEnumLabels(ctx)
	stageLabels, _ := c.loadDealStageMeta(ctx)
	if limit <= 0 {
		limit = 200
	}
	resp, err := c.callWithRetry(ctx, "crm.deal.list", map[string]any{
		"select": c.dealSelectFields(),
		"order":  map[string]string{"ID": "ASC"},
		"start":  start,
	})
	if err != nil {
		return DealsPage{}, err
	}
	items := toSliceMap(resp.Result)
	rows := make([]model.ProjectRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, mapDealToProjectRow(item, enumLabels, stageLabels))
	}
	c.applySupportFromLinkedDeals(ctx, items, rows, enumLabels)
	next := toInt(fmt.Sprintf("%v", resp.Next))
	total := 0
	if m, ok := resp.Result.(map[string]any); ok {
		_ = m
	}
	return DealsPage{Rows: rows, Next: next, Total: total}, nil
}

func (c *Client) GetDealsByIDs(ctx context.Context, ids []int) ([]model.ProjectRow, error) {
	enumLabels, _ := c.loadEnumLabels(ctx)
	stageLabels, _ := c.loadDealStageMeta(ctx)

	if len(ids) > 0 {
		cleanIDs := make([]int, 0, len(ids))
		for _, id := range ids {
			if id > 0 {
				cleanIDs = append(cleanIDs, id)
			}
		}
		if len(cleanIDs) == 0 {
			return nil, nil
		}

		out := make([]model.ProjectRow, 0, len(cleanIDs))
		found := make(map[int]struct{}, len(cleanIDs))
		const chunkSize = 50
		for i := 0; i < len(cleanIDs); i += chunkSize {
			end := i + chunkSize
			if end > len(cleanIDs) {
				end = len(cleanIDs)
			}
			chunk := cleanIDs[i:end]
			idValues := make([]string, 0, len(chunk))
			for _, id := range chunk {
				idValues = append(idValues, strconv.Itoa(id))
			}

			resp, err := c.callWithRetry(ctx, "crm.deal.list", map[string]any{
				"filter": map[string]any{
					"ID": idValues,
				},
				"select": c.dealSelectFields(),
				"order":  map[string]string{"ID": "ASC"},
			})
			if err != nil {
				return nil, err
			}
			items := toSliceMap(resp.Result)
			localRows := make([]model.ProjectRow, 0, len(items))
			for _, item := range items {
				row := mapDealToProjectRow(item, enumLabels, stageLabels)
				if row.DealID > 0 {
					found[row.DealID] = struct{}{}
				}
				localRows = append(localRows, row)
			}
			c.applySupportFromLinkedDeals(ctx, items, localRows, enumLabels)
			out = append(out, localRows...)
		}

		missing := make([]int, 0)
		for _, id := range cleanIDs {
			if _, ok := found[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			fallbackRows, fallbackFound, fbErr := c.findDealsByIdentifierFields(ctx, missing)
			if fbErr != nil {
				return nil, fmt.Errorf("deal_ids not found in crm.deal.list: %v; fallback by identifier fields failed: %w", missing, fbErr)
			}
			for _, row := range fallbackRows {
				if row.DealID > 0 {
					found[row.DealID] = struct{}{}
					out = append(out, row)
				}
			}
			missing2 := make([]int, 0)
			for _, id := range missing {
				if _, ok := fallbackFound[id]; !ok {
					missing2 = append(missing2, id)
				}
			}
			if len(missing2) > 0 {
				return nil, fmt.Errorf("deal_ids not found in crm.deal.list and identifier fields: %v", missing2)
			}
		}
		byID := make(map[int]model.ProjectRow, len(out))
		for _, row := range out {
			byID[row.DealID] = row
		}
		ordered := make([]model.ProjectRow, 0, len(cleanIDs))
		for _, id := range cleanIDs {
			if row, ok := byID[id]; ok {
				ordered = append(ordered, row)
			}
		}
		for i := range ordered {
			ordered[i].Seq = i + 1
		}
		return ordered, nil
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
			"select": c.dealSelectFields(),
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
		localRows := make([]model.ProjectRow, 0, len(items))
		for _, item := range items {
			localRows = append(localRows, mapDealToProjectRow(item, enumLabels, stageLabels))
		}
		c.applySupportFromLinkedDeals(ctx, items, localRows, enumLabels)
		out = append(out, localRows...)

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

func (c *Client) findDealsByIdentifierFields(ctx context.Context, identifiers []int) ([]model.ProjectRow, map[int]struct{}, error) {
	enumLabels, _ := c.loadEnumLabels(ctx)
	stageLabels, _ := c.loadDealStageMeta(ctx)
	fields, err := c.ListDealFields(ctx)
	if err != nil {
		return nil, nil, err
	}
	candidates := make([]string, 0, 8)
	for _, f := range fields {
		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(f.Code)), "UF_CRM_") {
			continue
		}
		title := strings.ToLower(strings.TrimSpace(f.Title))
		if strings.Contains(title, "идентификатор") || strings.Contains(title, "identifier") {
			candidates = append(candidates, f.Code)
		}
	}
	if len(candidates) == 0 {
		return nil, map[int]struct{}{}, nil
	}

	lookup := make(map[int]struct{}, len(identifiers))
	vals := make([]string, 0, len(identifiers))
	for _, id := range identifiers {
		lookup[id] = struct{}{}
		vals = append(vals, strconv.Itoa(id))
	}

	rows := make([]model.ProjectRow, 0, len(identifiers))
	foundIdentifiers := make(map[int]struct{}, len(identifiers))
	seenDeal := make(map[int]struct{}, len(identifiers))

	for _, fieldCode := range candidates {
		resp, callErr := c.callWithRetry(ctx, "crm.deal.list", map[string]any{
			"filter": map[string]any{
				fieldCode: vals,
			},
			"select": c.dealSelectFields(fieldCode),
			"order":  map[string]string{"ID": "ASC"},
		})
		if callErr != nil {
			continue
		}
		items := toSliceMap(resp.Result)
		for _, item := range items {
			row := mapDealToProjectRow(item, enumLabels, stageLabels)
			if row.DealID <= 0 {
				continue
			}
			if _, ok := seenDeal[row.DealID]; ok {
				continue
			}
			seenDeal[row.DealID] = struct{}{}
			rows = append(rows, row)

			raw := strings.TrimSpace(toString(anyMapGet(item, fieldCode)))
			m := regexp.MustCompile(`\d+`).FindString(raw)
			if m == "" {
				continue
			}
			v, convErr := strconv.Atoi(m)
			if convErr != nil {
				continue
			}
			if _, ok := lookup[v]; ok {
				foundIdentifiers[v] = struct{}{}
			}
		}
	}

	return rows, foundIdentifiers, nil
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

func (c *Client) GetAllTasks(ctx context.Context) ([]map[string]any, error) {
	start := 0
	const maxPages = 10000
	page := 0
	var all []map[string]any

	for {
		page++
		if page > maxPages {
			return nil, fmt.Errorf("all tasks pagination exceeded %d pages", maxPages)
		}
		resp, err := c.callWithRetry(ctx, "tasks.task.list", map[string]any{
			"select": []string{"ID", "TITLE", "RESPONSIBLE_ID", "DEADLINE", "STATUS", "DESCRIPTION", "UF_CRM_TASK", "CRM_BINDING", "GROUP_ID"},
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

		next := toInt(toString(resp.Next))
		if next == 0 {
			next = toInt(fmt.Sprintf("%v", resultMap["next"]))
		}
		if next == 0 || next <= start || len(chunk) == 0 {
			break
		}
		start = next
	}
	return all, nil
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

		next := toInt(toString(resp.Next))
		if next == 0 {
			next = toInt(fmt.Sprintf("%v", resultMap["next"]))
		}
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
		if IsAuthError(err) || (!isRateLimitError(err) && !isTransientError(err)) || attempt == maxAttempts {
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

func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "context deadline exceeded") ||
		strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "timeout awaiting response headers") ||
		strings.Contains(s, "connection reset by peer") ||
		strings.Contains(s, "temporary failure")
}

func IsTimeoutError(err error) bool {
	return isTransientError(err)
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

func mapDealToProjectRow(deal map[string]any, enumLabels map[string]map[string]string, stageLabels map[string]stageMeta) model.ProjectRow {
	stage := strings.TrimSpace(toString(anyMapGet(deal, "STAGE_ID", "stageId")))
	stageName := stage
	if meta, ok := stageLabels[stage]; ok {
		if strings.TrimSpace(meta.Name) != "" {
			stageName = strings.TrimSpace(meta.Name)
		}
	}
	progress := strings.TrimSpace(toString(anyMapGet(deal, "UF_CRM_1739951854")))
	if progress == "" {
		progress = enumValue(enumLabels, dealFieldIndustry, toString(anyMapGet(deal, dealFieldIndustry)))
	}

	description := strings.TrimSpace(toString(anyMapGet(deal, dealFieldDescription)))
	if description == "" {
		description = strings.TrimSpace(toString(anyMapGet(deal, "COMMENTS", "comments")))
	}

	location := extractAddress(toString(anyMapGet(deal, dealFieldAddress)))
	if location == "" {
		location = enumValue(enumLabels, dealFieldMunicipality, toString(anyMapGet(deal, dealFieldMunicipality)))
	}

	return model.ProjectRow{
		DealID:       toInt(toString(anyMapGet(deal, "ID", "id"))),
		Section:      firstNonEmpty(stageName, stage),
		ProjectID:    toInt(toString(anyMapGet(deal, "UF_CRM_PROJECT_GROUP_ID"))),
		DealTitle:    strings.TrimSpace(toString(anyMapGet(deal, "TITLE", "title"))),
		Location:     location,
		Investor:     strings.TrimSpace(toString(anyMapGet(deal, dealFieldInvestor))),
		Description:  description,
		ProjectStage: stageName,
		Progress:     progress,
		// "Меры поддержки по проекту" в итоговом паспорте должны заполняться только
		// из поля-связки на сделки (UF_CRM_1770268007), без fallback на enum-поле.
		Support: "",
		DateRange: strings.TrimSpace(strings.TrimSpace(toString(anyMapGet(deal, "BEGINDATE"))) +
			func() string {
				end := strings.TrimSpace(toString(anyMapGet(deal, "CLOSEDATE")))
				if end == "" {
					return ""
				}
				return " - " + end
			}()),
		Jobs: strings.TrimSpace(toString(anyMapGet(deal, dealFieldJobsPlan))),
		InvestPlan: firstNonEmpty(
			strings.TrimSpace(toString(anyMapGet(deal, dealFieldInvestTotalPlan))),
			normalizeMoney(toString(anyMapGet(deal, "OPPORTUNITY", "opportunity"))),
		),
		OwnPlan:  strings.TrimSpace(toString(anyMapGet(deal, dealFieldOwnPlan))),
		LoanPlan: strings.TrimSpace(toString(anyMapGet(deal, dealFieldLoanPlan))),
	}
}

func (c *Client) applySupportFromLinkedDeals(ctx context.Context, items []map[string]any, rows []model.ProjectRow, enumLabels map[string]map[string]string) {
	linkField := strings.TrimSpace(c.supportLinkDealField)
	if linkField == "" || len(items) == 0 || len(rows) == 0 {
		return
	}
	ids := make([]int, 0, 64)
	perDealLinks := make(map[int][]int, len(rows))
	for _, item := range items {
		dealID := toInt(toString(anyMapGet(item, "ID", "id")))
		if dealID <= 0 {
			continue
		}
		linked := extractIDsFromAny(anyMapGet(item, linkField))
		if len(linked) == 0 {
			continue
		}
		perDealLinks[dealID] = linked
		ids = append(ids, linked...)
	}
	ids = uniqueInts(ids)
	if len(ids) == 0 {
		return
	}
	supportByID := c.loadSupportValuesByDealIDs(ctx, ids, enumLabels)
	if len(supportByID) == 0 {
		return
	}
	for i := range rows {
		links := perDealLinks[rows[i].DealID]
		if len(links) == 0 {
			continue
		}
		values := make([]string, 0, len(links))
		for _, linkedID := range links {
			if v := strings.TrimSpace(supportByID[linkedID]); v != "" {
				values = append(values, v)
			}
		}
		if len(values) > 0 {
			rows[i].Support = strings.Join(values, "\n")
		}
	}
}

func (c *Client) loadSupportValuesByDealIDs(ctx context.Context, ids []int, enumLabels map[string]map[string]string) map[int]string {
	if len(ids) == 0 {
		return nil
	}
	stageLabels, _ := c.loadDealStageMeta(ctx)
	out := make(map[int]string, len(ids))
	const chunkSize = 50
	field := strings.TrimSpace(c.supportMeasureValueField)
	if field == "" {
		field = dealFieldSupportMeasure
	}
	for i := 0; i < len(ids); i += chunkSize {
		end := i + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		idValues := make([]string, 0, end-i)
		for _, id := range ids[i:end] {
			idValues = append(idValues, strconv.Itoa(id))
		}
		resp, err := c.callWithRetry(ctx, "crm.deal.list", map[string]any{
			"filter": map[string]any{"ID": idValues},
			"select": []string{"ID", "TITLE", "STAGE_ID", field},
		})
		if err != nil {
			continue
		}
		for _, item := range toSliceMap(resp.Result) {
			dealID := toInt(toString(anyMapGet(item, "ID", "id")))
			if dealID <= 0 {
				continue
			}
			title := strings.TrimSpace(toString(anyMapGet(item, "TITLE", "title")))
			stageID := strings.TrimSpace(toString(anyMapGet(item, "STAGE_ID", "stageId")))
			stageName := stageID
			if meta, ok := stageLabels[stageID]; ok && strings.TrimSpace(meta.Name) != "" {
				stageName = strings.TrimSpace(meta.Name)
			}
			measure := strings.TrimSpace(enumValue(enumLabels, field, toString(anyMapGet(item, field))))

			parts := make([]string, 0, 4)
			if title != "" {
				parts = append(parts, title)
			}
			if measure != "" && !strings.EqualFold(measure, title) {
				parts = append(parts, "мера: "+measure)
			}
			if stageName != "" {
				parts = append(parts, "стадия: "+stageName)
			}
			out[dealID] = strings.Join(parts, " | ")
		}
	}
	return out
}

func extractIDsFromAny(raw any) []int {
	values := make([]string, 0, 4)
	switch v := raw.(type) {
	case []any:
		for _, x := range v {
			values = append(values, toString(x))
		}
	case []string:
		values = append(values, v...)
	default:
		values = append(values, toString(v))
	}
	out := make([]int, 0, len(values))
	for _, s := range values {
		for _, m := range regexp.MustCompile(`\d+`).FindAllString(s, -1) {
			id, err := strconv.Atoi(m)
			if err == nil && id > 0 {
				out = append(out, id)
			}
		}
	}
	return uniqueInts(out)
}

func (c *Client) loadEnumLabels(ctx context.Context) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string)
	resp, err := c.callWithRetry(ctx, "crm.deal.fields", nil)
	if err != nil {
		return nil, err
	}
	fieldsMap, ok := resp.Result.(map[string]any)
	if !ok {
		return out, nil
	}
	for code, raw := range fieldsMap {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		items, ok := m["items"].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		labels := make(map[string]string, len(items))
		for _, it := range items {
			im, ok := it.(map[string]any)
			if !ok {
				continue
			}
			id := strings.TrimSpace(toString(im["ID"]))
			val := strings.TrimSpace(toString(im["VALUE"]))
			if id != "" && val != "" {
				labels[id] = val
			}
		}
		if len(labels) > 0 {
			out[code] = labels
		}
	}
	return out, nil
}

func enumValue(enumLabels map[string]map[string]string, fieldCode, raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if enumLabels == nil {
		return v
	}
	if byField, ok := enumLabels[fieldCode]; ok {
		if label, ok := byField[v]; ok {
			return label
		}
		// Some Bitrix enum fields can return numeric IDs with insignificant formatting differences.
		// Try normalized integer key as a safe fallback.
		if iv := toInt(v); iv > 0 {
			if label, ok := byField[strconv.Itoa(iv)]; ok {
				return label
			}
		}
	}
	return v
}

func extractAddress(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "|;|"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(strings.TrimPrefix(s, ", ,"))
	s = strings.Trim(s, " ,")
	return s
}

func normalizeMoney(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	if err != nil {
		return s
	}
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(f, 'f', 2, 64), "0"), ".")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (c *Client) loadDealStageMeta(ctx context.Context) (map[string]stageMeta, error) {
	resp, err := c.callWithRetry(ctx, "crm.status.list", map[string]any{
		"order": map[string]string{"SORT": "ASC"},
	})
	if err != nil {
		return nil, err
	}
	items := toSliceMap(resp.Result)
	out := make(map[string]stageMeta, len(items))
	for _, item := range items {
		entityID := strings.TrimSpace(toString(anyMapGet(item, "ENTITY_ID", "entityId")))
		if !strings.HasPrefix(strings.ToUpper(entityID), "DEAL_STAGE") {
			continue
		}
		statusID := strings.TrimSpace(toString(anyMapGet(item, "STATUS_ID", "statusId")))
		if statusID == "" {
			continue
		}
		semantics := strings.TrimSpace(strings.ToLower(toString(anyMapGet(item, "SEMANTICS", "semantics"))))
		if semantics == "" {
			if extra, ok := anyMapGet(item, "EXTRA", "extra").(map[string]any); ok {
				semantics = strings.TrimSpace(strings.ToLower(toString(anyMapGet(extra, "SEMANTICS", "semantics"))))
			}
		}
		out[statusID] = stageMeta{
			Name:      strings.TrimSpace(toString(anyMapGet(item, "NAME", "name"))),
			Semantics: semantics,
		}
	}
	return out, nil
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
