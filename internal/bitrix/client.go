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

func (c *Client) GetProjectTasks(ctx context.Context, groupID int) ([]map[string]any, error) {
	start := 0
	var all []map[string]any

	for {
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
		if next == 0 || len(chunk) == 0 {
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http error %d: %s", resp.StatusCode, string(payload))
	}

	var out bitrixResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if out.Error != "" {
		if out.Desc != "" {
			return nil, fmt.Errorf("bitrix error: %s (%s)", out.Error, out.Desc)
		}
		return nil, fmt.Errorf("bitrix error: %s", out.Error)
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
