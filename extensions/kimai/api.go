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
	errToken       = errors.New("Kimai rejected the API token; replace it in extension settings")
	errUnavailable = errors.New("Kimai could not be reached; try again later")
	errNotFound    = errors.New("Kimai could not find that timesheet")
	errRateLimited = errors.New("Kimai rate limit reached; try again later")
)

type client struct {
	base, token string
	http        *http.Client
}

func newClient(base, token string) *client {
	base = strings.TrimRight(base, "/")
	if base != "" && !strings.HasSuffix(base, "/api") {
		base += "/api"
	}
	return &client{base: base, token: strings.TrimSpace(token), http: &http.Client{
		Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	if c.token == "" {
		return errors.New("Kimai API token is not configured")
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
	if in != nil {
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
		return errors.New("Kimai returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return errors.New("Kimai returned invalid JSON")
			}
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errToken
	case http.StatusNotFound:
		return errNotFound
	case http.StatusTooManyRequests:
		return errRateLimited
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return errors.New("Kimai rejected the operation; check timestamps, fields and permissions")
	default:
		return fmt.Errorf("Kimai returned HTTP %d", res.StatusCode)
	}
}

func (c *client) timesheets(ctx context.Context, view string, page int) ([]timesheet, error) {
	if page < 1 || page > 100000 {
		return nil, errors.New("invalid page number")
	}
	path := "/timesheets"
	q := url.Values{"size": {"30"}, "full": {"true"}, "orderBy": {"begin"}, "order": {"DESC"}}
	switch view {
	case "recent":
		path = "/timesheets/recent"
		q.Set("size", "30")
	case "active":
		path = "/timesheets/active"
	case "timesheets":
		q.Set("page", strconv.Itoa(page))
	default:
		return nil, errors.New("invalid timesheet view")
	}
	var out []timesheet
	err := c.do(ctx, http.MethodGet, path, q, nil, &out)
	return out, err
}

func (c *client) projects(ctx context.Context) ([]namedResource, error) {
	var out []namedResource
	err := c.do(ctx, http.MethodGet, "/projects", url.Values{"visible": {"1"}, "ignoreDates": {"1"}}, nil, &out)
	return out, err
}

func (c *client) activities(ctx context.Context, projectID int64) ([]namedResource, error) {
	if projectID <= 0 {
		return nil, errors.New("project ID must be positive")
	}
	var out []namedResource
	err := c.do(ctx, http.MethodGet, "/activities", url.Values{"visible": {"1"}, "project": {strconv.FormatInt(projectID, 10)}}, nil, &out)
	return out, err
}

func (c *client) createTimesheet(ctx context.Context, projectID, activityID int64, begin, end, description string) (timesheet, error) {
	if projectID <= 0 || activityID <= 0 {
		return timesheet{}, errors.New("discovered project and activity IDs are required")
	}
	body := map[string]any{"project": projectID, "activity": activityID, "begin": begin, "description": description}
	if end != "" {
		body["end"] = end
	}
	var out timesheet
	err := c.do(ctx, http.MethodPost, "/timesheets", url.Values{"full": {"true"}}, body, &out)
	if out.ID <= 0 && err == nil {
		return timesheet{}, errors.New("Kimai did not return the created timesheet ID")
	}
	return out, err
}

func (c *client) stopTimesheet(ctx context.Context, id int64) (timesheet, error) {
	if id <= 0 {
		return timesheet{}, errors.New("timesheet ID must be positive")
	}
	var out timesheet
	err := c.do(ctx, http.MethodPatch, "/timesheets/"+strconv.FormatInt(id, 10)+"/stop", nil, nil, &out)
	return out, err
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
