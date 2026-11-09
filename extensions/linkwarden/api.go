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
	errToken       = errors.New("Linkwarden rejected the API key; replace it in extension settings")
	errUnavailable = errors.New("Linkwarden could not be reached; try again later")
	errNotFound    = errors.New("Linkwarden could not find that link")
	errRateLimited = errors.New("Linkwarden rate limit reached; try again later")
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
		return errors.New("Linkwarden API key is not configured")
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
		return errors.New("Linkwarden returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out == nil || len(data) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Linkwarden returned invalid JSON")
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
		return errors.New("Linkwarden rejected the link fields")
	default:
		return fmt.Errorf("Linkwarden returned HTTP %d", res.StatusCode)
	}
}

func (c *client) search(ctx context.Context, query string, collectionID int64) ([]link, error) {
	q := url.Values{}
	if query != "" {
		q.Set("q", query)
	}
	if collectionID > 0 {
		q.Set("collectionId", strconv.FormatInt(collectionID, 10))
	}
	var out struct {
		Links []link `json:"links"`
		Data  []link `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/search", q, nil, &out); err != nil {
		return nil, err
	}
	if out.Links != nil {
		return out.Links, nil
	}
	return out.Data, nil
}

func (c *client) collections(ctx context.Context) ([]collection, error) {
	var out []collection
	err := c.do(ctx, http.MethodGet, "/collections", nil, nil, &out)
	return out, err
}

func (c *client) link(ctx context.Context, id int64) (link, error) {
	var out link
	err := c.do(ctx, http.MethodGet, "/links/"+strconv.FormatInt(id, 10), nil, nil, &out)
	return out, err
}

func (c *client) highlights(ctx context.Context, id int64) ([]highlight, error) {
	var out []highlight
	err := c.do(ctx, http.MethodGet, "/links/"+strconv.FormatInt(id, 10)+"/highlights", nil, nil, &out)
	return out, err
}

func (c *client) save(ctx context.Context, rawURL string) (link, error) {
	var out link
	err := c.do(ctx, http.MethodPost, "/links", nil, map[string]string{"url": rawURL}, &out)
	return out, err
}

func (c *client) archive(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodPost, "/links/"+strconv.FormatInt(id, 10)+"/archive", nil, nil, nil)
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
