package bitrix

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SergeiMurashev/bitrix-passport-exporter/internal/model"
	"github.com/kurerid/bixgo"
)

type Client struct {
	api *bixgo.Client
}

func NewFromWebhook(webhook string) (*Client, error) {
	u, err := url.Parse(webhook)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "rest" {
		return nil, fmt.Errorf("unexpected webhook path: %s", u.Path)
	}
	authToken := parts[1] + "/" + parts[2]
	baseURL := u.Scheme + "://" + u.Host
	auth := bixgo.NewClientAuth(authToken, "", time.Now().Add(365*24*time.Hour), "", "")
	return &Client{api: bixgo.NewClient(baseURL, auth)}, nil
}

func (c *Client) ResolveProjectForDeal(ctx context.Context, p model.ProjectRow, projectField string) (int, string, error) {
	if p.DealID > 0 {
		var resp bixgo.Response[map[string]any]
		err := c.api.Call(ctx, "crm.deal.get", bixgo.Params{"id": p.DealID}, &resp)
		if err == nil {
			if gid := toInt(fmt.Sprintf("%v", resp.Result[projectField])); gid > 0 {
				return gid, "deal." + projectField, nil
			}
		}
	}

	var grpResp bixgo.Response[[]map[string]any]
	err := c.api.Call(ctx, "sonet_group.get", bixgo.Params{
		"FILTER": bixgo.Params{"NAME": p.DealTitle},
		"SELECT": []string{"ID", "NAME"},
	}, &grpResp)
	if err != nil {
		return 0, "", err
	}
	if len(grpResp.Result) == 0 {
		return 0, "", nil
	}
	if len(grpResp.Result) > 1 {
		return 0, "", fmt.Errorf("found %d projects by title %q; ambiguous fallback", len(grpResp.Result), p.DealTitle)
	}
	return toInt(fmt.Sprintf("%v", grpResp.Result[0]["ID"])), "sonet_group.get(NAME)", nil
}

func (c *Client) GetProjectTasks(ctx context.Context, groupID int) ([]map[string]any, error) {
	start := 0
	var all []map[string]any
	for {
		var resp bixgo.Response[map[string]any]
		err := c.api.Call(ctx, "tasks.task.list", bixgo.Params{
			"filter": bixgo.Params{"GROUP_ID": groupID},
			"select": []string{"ID", "TITLE", "RESPONSIBLE_ID", "DEADLINE", "STATUS", "DESCRIPTION"},
			"start":  start,
		}, &resp)
		if err != nil {
			return nil, err
		}

		chunk := toSliceMap(resp.Result["tasks"])
		if len(chunk) == 0 {
			chunk = toSliceMap(resp.Result["items"])
		}
		all = append(all, chunk...)
		next := toInt(fmt.Sprintf("%v", resp.Result["next"]))
		if next == 0 || len(chunk) == 0 {
			break
		}
		start = next
	}
	return all, nil
}

func (c *Client) GetUserName(ctx context.Context, userID int) (string, error) {
	var resp bixgo.Response[[]map[string]any]
	err := c.api.Call(ctx, "user.get", bixgo.Params{"FILTER": bixgo.Params{"ID": userID}}, &resp)
	if err != nil || len(resp.Result) == 0 {
		return strconv.Itoa(userID), err
	}
	u := resp.Result[0]
	name := strings.TrimSpace(strings.Join([]string{toString(u["LAST_NAME"]), toString(u["NAME"]), toString(u["SECOND_NAME"])}, " "))
	if name == "" {
		return strconv.Itoa(userID), nil
	}
	return name, nil
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
