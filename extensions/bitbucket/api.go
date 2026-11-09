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
	errToken       = errors.New("Bitbucket rejected the personal access token; replace it in extension settings")
	errUnavailable = errors.New("Bitbucket could not be reached; try again later")
	errNotFound    = errors.New("Bitbucket could not find that pull request")
	errRateLimited = errors.New("Bitbucket rate limit reached; try again later")
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
		return errors.New("Bitbucket personal access token is not configured")
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
		return errors.New("Bitbucket returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out == nil || len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Bitbucket returned invalid JSON")
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
		return errors.New("Bitbucket rejected the review operation")
	default:
		return fmt.Errorf("Bitbucket returned HTTP %d", res.StatusCode)
	}
}

func (c *client) pullRequests(ctx context.Context, project, repo string, start int) (prPage, error) {
	if start < 0 {
		return prPage{}, errors.New("invalid pagination start")
	}
	q := url.Values{"state": {"OPEN"}, "start": {strconv.Itoa(start)}, "limit": {"50"}, "withAttributes": {"true"}}
	var out prPage
	err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(project)+"/repos/"+url.PathEscape(repo)+"/pull-requests", q, nil, &out)
	return out, err
}

func (c *client) pullRequest(ctx context.Context, project, repo string, id int64) (pullRequest, error) {
	var out pullRequest
	err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(project)+"/repos/"+url.PathEscape(repo)+"/pull-requests/"+strconv.FormatInt(id, 10), nil, nil, &out)
	return out, err
}

func (c *client) activities(ctx context.Context, project, repo string, id int64) ([]activity, error) {
	var out struct {
		Values []activity `json:"values"`
	}
	err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(project)+"/repos/"+url.PathEscape(repo)+"/pull-requests/"+strconv.FormatInt(id, 10)+"/activities", url.Values{"limit": {"100"}}, nil, &out)
	return out.Values, err
}

func (c *client) approve(ctx context.Context, project, repo string, id int64) error {
	return c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(project)+"/repos/"+url.PathEscape(repo)+"/pull-requests/"+strconv.FormatInt(id, 10)+"/approve", nil, nil, nil)
}

func (c *client) unapprove(ctx context.Context, project, repo string, id int64) error {
	return c.do(ctx, http.MethodDelete, "/projects/"+url.PathEscape(project)+"/repos/"+url.PathEscape(repo)+"/pull-requests/"+strconv.FormatInt(id, 10)+"/approve", nil, nil, nil)
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
