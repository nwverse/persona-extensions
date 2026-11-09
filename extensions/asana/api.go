package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	errToken       = errors.New("Asana rejected the personal access token; replace it in extension settings")
	errUnavailable = errors.New("Asana could not be reached; try again later")
	errNotFound    = errors.New("Asana could not find that workspace, project or task")
	errRateLimited = errors.New("Asana rate limit reached; try again later")
)

type client struct {
	base, token string
	http        *http.Client
}

func newClient(base, token string) *client {
	return &client{base: strings.TrimRight(base, "/"), token: strings.TrimSpace(token), http: &http.Client{
		Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	if c.token == "" {
		return errors.New("Asana personal access token is not configured")
	}
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if query != nil {
		req.URL.RawQuery = query.Encode()
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errUnavailable, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20+1))
	if err != nil {
		return errUnavailable
	}
	if len(data) > 4<<20 {
		return errors.New("Asana returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out == nil || len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Asana returned invalid JSON")
		}
		return nil
	case http.StatusNoContent:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errToken
	case http.StatusNotFound:
		return errNotFound
	case http.StatusTooManyRequests:
		return errRateLimited
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return errors.New("Asana rejected the operation; check the fields and permissions")
	default:
		return fmt.Errorf("Asana returned HTTP %d", res.StatusCode)
	}
}

func validGID(value string) error {
	if len(value) == 0 || len(value) > 40 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n/?#") {
		return errors.New("workspace, project or task GID is invalid")
	}
	return nil
}

func validOffset(value string) error {
	if len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("invalid Asana pagination offset")
	}
	return nil
}

func pagination(offset, fields string) (url.Values, error) {
	if err := validOffset(offset); err != nil {
		return nil, err
	}
	q := url.Values{"limit": {"100"}}
	if offset != "" {
		q.Set("offset", offset)
	}
	if fields != "" {
		q.Set("opt_fields", fields)
	}
	return q, nil
}

func (c *client) workspaces(ctx context.Context, offset string) (page[workspace], error) {
	q, err := pagination(offset, "name,is_organization")
	if err != nil {
		return page[workspace]{}, err
	}
	var out page[workspace]
	err = c.do(ctx, http.MethodGet, "/workspaces", q, nil, &out)
	return out, err
}

func (c *client) projects(ctx context.Context, workspaceGID, offset string) (page[resource], error) {
	if err := validGID(workspaceGID); err != nil {
		return page[resource]{}, err
	}
	q, err := pagination(offset, "name,resource_type,archived")
	if err != nil {
		return page[resource]{}, err
	}
	q.Set("workspace", workspaceGID)
	q.Set("archived", "false")
	var out page[resource]
	err = c.do(ctx, http.MethodGet, "/projects", q, nil, &out)
	return out, err
}

func (c *client) project(ctx context.Context, gid string) (project, error) {
	if err := validGID(gid); err != nil {
		return project{}, err
	}
	var out struct {
		Data project `json:"data"`
	}
	q := url.Values{"opt_fields": {"name,archived,workspace.name"}}
	err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(gid), q, nil, &out)
	return out.Data, err
}

func (c *client) tasks(ctx context.Context, workspaceGID, offset string) (page[task], error) {
	if err := validGID(workspaceGID); err != nil {
		return page[task]{}, err
	}
	q, err := pagination(offset, "name,notes,completed,completed_at,due_on,due_at,start_on,permalink_url,resource_subtype,assignee.name,projects.name")
	if err != nil {
		return page[task]{}, err
	}
	q.Set("workspace", workspaceGID)
	q.Set("assignee", "me")
	q.Set("completed_since", "now")
	var out page[task]
	err = c.do(ctx, http.MethodGet, "/tasks", q, nil, &out)
	return out, err
}

func (c *client) task(ctx context.Context, gid string) (task, error) {
	var out struct {
		Data task `json:"data"`
	}
	if err := validGID(gid); err != nil {
		return task{}, err
	}
	q := url.Values{"opt_fields": {"name,notes,completed,completed_at,due_on,due_at,start_on,permalink_url,resource_subtype,assignee.name,projects.name,workspace.name"}}
	err := c.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(gid), q, nil, &out)
	return out.Data, err
}

func (c *client) createTask(ctx context.Context, workspaceGID, projectGID, name, notes, dueOn string) (task, error) {
	body := map[string]any{"workspace": workspaceGID, "assignee": "me", "name": name}
	if notes != "" {
		body["notes"] = notes
	}
	if dueOn != "" {
		body["due_on"] = dueOn
	}
	if projectGID != "" {
		body["projects"] = []string{projectGID}
	}
	var out struct {
		Data task `json:"data"`
	}
	err := c.do(ctx, http.MethodPost, "/tasks", nil, map[string]any{"data": body}, &out)
	return out.Data, err
}

func (c *client) completeTask(ctx context.Context, gid string) (task, error) {
	var out struct {
		Data task `json:"data"`
	}
	err := c.do(ctx, http.MethodPut, "/tasks/"+url.PathEscape(gid), nil, map[string]any{"data": map[string]any{"completed": true}}, &out)
	return out.Data, err
}

func safeTaskLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != "app.asana.com" || u.User != nil {
		return ""
	}
	return raw
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}

func pageKey(gid string) string { return "task-" + gid }
func pageLimit() string         { return strconv.Itoa(100) }
