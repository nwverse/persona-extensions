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
	"strings"
	"time"
)

const apiBase = "https://api.todoist.com/api/v1"

var (
	errToken       = errors.New("Todoist rejected the access token; replace it in extension settings")
	errNotFound    = errors.New("Todoist could not find that task or project")
	errUnavailable = errors.New("Todoist could not be reached; try again later")
	errRateLimited = errors.New("Todoist rate limit reached; try again shortly")
)

type client struct {
	base, token string
	http        *http.Client
}

func newClient(base, token string) *client {
	return &client{base: strings.TrimRight(base, "/"), token: strings.TrimSpace(token), http: &http.Client{
		Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	if c.token == "" {
		return errors.New("Todoist API token is not configured")
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
		return errors.New("Todoist returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out == nil || len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Todoist returned invalid JSON")
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
		return fmt.Errorf("Todoist rejected the task fields (HTTP %d)", res.StatusCode)
	default:
		return fmt.Errorf("Todoist returned HTTP %d", res.StatusCode)
	}
}

// page decodes one current REST API response: results plus next_cursor. No second request per page.
func (c *client) page(ctx context.Context, path, cursor string, query url.Values, out any) (string, error) {
	q := url.Values{}
	for k, values := range query {
		q[k] = values
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	q.Set("limit", "50")
	var envelope struct {
		Results    json.RawMessage `json:"results"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := c.do(ctx, http.MethodGet, path, q, nil, &envelope); err != nil {
		return "", err
	}
	if len(envelope.Results) == 0 {
		return "", errors.New("Todoist response has no results collection")
	}
	if err := json.Unmarshal(envelope.Results, out); err != nil {
		return "", errors.New("Todoist returned an invalid results collection")
	}
	return envelope.NextCursor, nil
}

func (c *client) tasks(ctx context.Context, projectID, query string, pages int) ([]task, error) {
	var out []task
	cursor, seen := "", map[string]bool{}
	for n := 0; n < max(1, min(pages, 6)); n++ {
		q := url.Values{}
		if projectID != "" {
			q.Set("project_id", projectID)
		}
		if query != "" {
			q.Set("query", query)
		}
		var batch []task
		var err error
		cursor, err = c.page(ctx, "/tasks", cursor, q, &batch)
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if cursor == "" || len(batch) == 0 {
			break
		}
		if seen[cursor] {
			return nil, errors.New("Todoist repeated its pagination cursor")
		}
		seen[cursor] = true
	}
	return out, nil
}

func (c *client) context(ctx context.Context) (contextData, error) {
	var out = contextData{Projects: map[string]project{}, Sections: map[string]section{}, Labels: map[string]label{}}
	var projects []project
	if err := c.pages(ctx, "/projects", &projects); err != nil {
		return out, err
	}
	for _, p := range projects {
		out.Projects[p.ID] = p
	}
	var sections []section
	if err := c.pages(ctx, "/sections", &sections); err != nil {
		return out, err
	}
	for _, s := range sections {
		out.Sections[s.ID] = s
	}
	var labels []label
	if err := c.pages(ctx, "/labels", &labels); err != nil {
		return out, err
	}
	for _, l := range labels {
		out.Labels[l.Name] = l
	}
	return out, nil
}

func (c *client) pages[T any](ctx context.Context, path string, out *[]T) error {
	cursor, seen := "", map[string]bool{}
	for range 6 {
		var batch []T
		var err error
		cursor, err = c.page(ctx, path, cursor, nil, &batch)
		if err != nil {
			return err
		}
		*out = append(*out, batch...)
		if cursor == "" {
			return nil
		}
		if seen[cursor] {
			return errors.New("Todoist repeated its pagination cursor")
		}
		seen[cursor] = true
	}
	return nil
}

func (c *client) task(ctx context.Context, id string) (task, error) {
	var out task
	err := c.do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(id), nil, nil, &out)
	return out, err
}

func (c *client) create(ctx context.Context, in map[string]any) (task, error) {
	var out task
	err := c.do(ctx, http.MethodPost, "/tasks", nil, in, &out)
	return out, err
}

func (c *client) update(ctx context.Context, id string, patch taskPatch) (task, error) {
	var out task
	err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(id), nil, patch, &out)
	return out, err
}

func (c *client) complete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(id)+"/close", nil, nil, nil)
}
