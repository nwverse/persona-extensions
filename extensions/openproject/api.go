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
	errToken       = errors.New("OpenProject rejected the API token; replace it in extension settings")
	errUnavailable = errors.New("OpenProject could not be reached; try again later")
	errNotFound    = errors.New("OpenProject could not find that work package, project or type")
	errRateLimited = errors.New("OpenProject rate limit reached; try again later")
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
		return errors.New("OpenProject API token is not configured")
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
	req.Header.Set("Accept", "application/hal+json")
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
		return errors.New("OpenProject returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out == nil || len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("OpenProject returned invalid HAL JSON")
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
		return errors.New("OpenProject rejected the operation; check the fields and permissions")
	default:
		return fmt.Errorf("OpenProject returned HTTP %d", res.StatusCode)
	}
}

func (c *client) currentUser(ctx context.Context) (currentUser, error) {
	var out currentUser
	err := c.do(ctx, http.MethodGet, "/api/v3/users/me", nil, nil, &out)
	return out, err
}

func (c *client) workPackages(ctx context.Context, userID int64, offset int) (collection[workPackage], error) {
	if userID <= 0 || offset < 1 {
		return collection[workPackage]{}, errors.New("invalid user or offset")
	}
	filter, _ := json.Marshal([]map[string]any{{"assigned_to": map[string]any{"operator": "=", "values": []string{strconv.FormatInt(userID, 10)}}}})
	q := url.Values{"filters": {string(filter)}, "pageSize": {"50"}, "offset": {strconv.Itoa(offset)}, "sortBy": {`[["updatedAt","desc"]]`}}
	var out collection[workPackage]
	err := c.do(ctx, http.MethodGet, "/api/v3/work_packages", q, nil, &out)
	return out, err
}

func (c *client) workPackage(ctx context.Context, id int64) (workPackage, error) {
	if id <= 0 {
		return workPackage{}, errors.New("work package ID must be positive")
	}
	var out workPackage
	err := c.do(ctx, http.MethodGet, "/api/v3/work_packages/"+strconv.FormatInt(id, 10), nil, nil, &out)
	return out, err
}

func (c *client) projects(ctx context.Context, offset int) (collection[project], error) {
	if offset < 1 {
		return collection[project]{}, errors.New("offset must be positive")
	}
	q := url.Values{"pageSize": {"100"}, "offset": {strconv.Itoa(offset)}}
	var out collection[project]
	err := c.do(ctx, http.MethodGet, "/api/v3/projects", q, nil, &out)
	return out, err
}

func (c *client) types(ctx context.Context, offset int) (collection[workType], error) {
	if offset < 1 {
		return collection[workType]{}, errors.New("offset must be positive")
	}
	q := url.Values{"pageSize": {"100"}, "offset": {strconv.Itoa(offset)}}
	var out collection[workType]
	err := c.do(ctx, http.MethodGet, "/api/v3/types", q, nil, &out)
	return out, err
}

func (c *client) createWorkPackage(ctx context.Context, projectHref, typeHref, subject, description, startDate, dueDate string) (workPackage, error) {
	if projectHref == "" || typeHref == "" {
		return workPackage{}, errors.New("project and type links are required")
	}
	body := map[string]any{"subject": subject, "description": map[string]string{"raw": description}, "_links": map[string]any{"project": map[string]string{"href": projectHref}, "type": map[string]string{"href": typeHref}}}
	if startDate != "" {
		body["startDate"] = startDate
	}
	if dueDate != "" {
		body["dueDate"] = dueDate
	}
	var out workPackage
	err := c.do(ctx, http.MethodPost, "/api/v3/work_packages", nil, body, &out)
	return out, err
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
