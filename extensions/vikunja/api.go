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
	errToken       = errors.New("Vikunja rejected the API token; replace it in extension settings")
	errUnavailable = errors.New("Vikunja could not be reached; try again later")
	errNotFound    = errors.New("Vikunja could not find that task or project")
	errRateLimited = errors.New("Vikunja rate limit reached; try again later")
)

type client struct {
	base, token string
	http        *http.Client
}

func newClient(base, token string) *client {
	base = strings.TrimRight(base, "/")
	if base != "" && !strings.HasSuffix(base, "/api/v1") {
		base += "/api/v1"
	}
	return &client{base: base, token: strings.TrimSpace(token), http: &http.Client{
		Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) do(ctx context.Context, method, path string, query url.Values, in, out any) (http.Header, error) {
	if c.token == "" {
		return nil, errors.New("Vikunja API token is not configured")
	}
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if query != nil {
		req.URL.RawQuery = query.Encode()
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUnavailable, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20+1))
	if err != nil {
		return nil, errUnavailable
	}
	if len(data) > 4<<20 {
		return nil, errors.New("Vikunja returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return nil, errors.New("Vikunja returned invalid JSON")
			}
		}
		return res.Header, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errToken
	case http.StatusNotFound:
		return nil, errNotFound
	case http.StatusTooManyRequests:
		return nil, errRateLimited
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return nil, errors.New("Vikunja rejected the operation; check the fields and permissions")
	default:
		return nil, fmt.Errorf("Vikunja returned HTTP %d", res.StatusCode)
	}
}

func (c *client) currentUser(ctx context.Context) (user, error) {
	var out user
	_, err := c.do(ctx, http.MethodGet, "/user", nil, nil, &out)
	return out, err
}

func (c *client) tasks(ctx context.Context, username, query string, page int) (taskPage, error) {
	if page < 1 || page > 100000 {
		return taskPage{}, errors.New("invalid page number")
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {"50"}}
	if query = strings.TrimSpace(query); query != "" {
		if len([]rune(query)) > 200 {
			return taskPage{}, errors.New("search query must be at most 200 characters")
		}
		q.Set("s", query)
	} else {
		if username == "" || len(username) > 250 || strings.ContainsAny(username, "'\\,\r\n") {
			return taskPage{}, errors.New("Vikunja returned an invalid current username")
		}
		q.Set("filter", "done = false && assignees = '"+username+"'")
		q.Set("sort_by", "due_date")
		q.Set("order_by", "asc")
	}
	var out taskPage
	h, err := c.do(ctx, http.MethodGet, "/tasks", q, nil, &out.Items)
	if err != nil {
		return taskPage{}, err
	}
	out.Total, _ = strconv.Atoi(h.Get("X-Pagination-Total"))
	out.PageCount, _ = strconv.Atoi(h.Get("X-Pagination-Total-Pages"))
	out.HasMore, out.NextPage = page < out.PageCount, page+1
	return out, nil
}

func (c *client) task(ctx context.Context, id int64) (task, error) {
	if id <= 0 {
		return task{}, errors.New("task ID must be positive")
	}
	var out task
	_, err := c.do(ctx, http.MethodGet, "/tasks/"+strconv.FormatInt(id, 10), url.Values{"expand": {"comments"}}, nil, &out)
	return out, err
}

func (c *client) projects(ctx context.Context, page int) ([]project, error) {
	if page < 1 || page > 100000 {
		return nil, errors.New("invalid page number")
	}
	var out []project
	_, err := c.do(ctx, http.MethodGet, "/projects", url.Values{"page": {strconv.Itoa(page)}, "per_page": {"100"}}, nil, &out)
	return out, err
}

func (c *client) createTask(ctx context.Context, projectID int64, title, description, dueDate string) (task, error) {
	if projectID <= 0 {
		return task{}, errors.New("project ID must be positive")
	}
	body := map[string]any{"title": title, "description": description}
	if dueDate != "" {
		body["due_date"] = dueDate
	}
	var out task
	_, err := c.do(ctx, http.MethodPut, "/projects/"+strconv.FormatInt(projectID, 10)+"/tasks", nil, body, &out)
	if out.ID == 0 && err == nil {
		return task{}, errors.New("Vikunja did not return the created task ID")
	}
	return out, err
}

func (c *client) completeTask(ctx context.Context, id int64) (task, error) {
	if id <= 0 {
		return task{}, errors.New("task ID must be positive")
	}
	var out task
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+strconv.FormatInt(id, 10), nil, map[string]bool{"done": true}, &out)
	return out, err
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
