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

const apiBase = "https://readwise.io/api/v3"

var (
	errToken       = errors.New("Readwise rejected the access token; replace it in extension settings")
	errUnavailable = errors.New("Readwise could not be reached; try again later")
	errNotFound    = errors.New("Readwise could not find that document")
	errRateLimited = errors.New("Readwise rate limit reached; try again later")
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
		return errors.New("Readwise access token is not configured")
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
	req.Header.Set("Authorization", "Token "+c.token)
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
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20+1))
	if err != nil {
		return errUnavailable
	}
	if len(data) > 8<<20 {
		return errors.New("Readwise returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out == nil || len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Readwise returned invalid JSON")
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
	case http.StatusBadRequest:
		return errors.New("Readwise rejected the document fields")
	default:
		return fmt.Errorf("Readwise returned HTTP %d", res.StatusCode)
	}
}

func (c *client) list(ctx context.Context, params listParams) (listPage, error) {
	q := url.Values{}
	if params.ID != "" {
		q.Set("id", params.ID)
	}
	if params.Location != "" {
		q.Set("location", params.Location)
	}
	if params.Category != "" {
		q.Set("category", params.Category)
	}
	if params.HTML {
		q.Set("withHtmlContent", "true")
	}
	if params.Cursor != "" {
		q.Set("pageCursor", params.Cursor)
	}
	var page listPage
	err := c.do(ctx, http.MethodGet, "/list/", q, nil, &page)
	return page, err
}

func (c *client) highlights(ctx context.Context, parentID string) ([]document, error) {
	var out []document
	cursor, seen := "", map[string]bool{}
	for range 4 {
		page, err := c.list(ctx, listParams{Category: "highlight", Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, item := range page.Results {
			if item.ParentID == parentID {
				out = append(out, item)
			}
		}
		if page.Cursor == "" {
			break
		}
		if seen[page.Cursor] {
			return nil, errors.New("Readwise repeated its pagination cursor")
		}
		seen[page.Cursor], cursor = true, page.Cursor
	}
	return out, nil
}

func (c *client) save(ctx context.Context, rawURL string) (document, error) {
	var out document
	err := c.do(ctx, http.MethodPost, "/save/", nil, map[string]string{"url": rawURL}, &out)
	return out, err
}

func (c *client) update(ctx context.Context, id string, patch updatePatch) error {
	return c.do(ctx, http.MethodPatch, "/update/"+url.PathEscape(id)+"/", nil, patch, nil)
}
