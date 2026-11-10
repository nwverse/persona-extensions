package main

import (
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
	errToken       = errors.New("Paperless-ngx rejected the API token; replace it in extension settings")
	errUnavailable = errors.New("Paperless-ngx could not be reached; try again later")
	errNotFound    = errors.New("Paperless-ngx could not find that document")
	errRateLimited = errors.New("Paperless-ngx rate limit reached; try again later")
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

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	if c.token == "" {
		return errors.New("Paperless-ngx API token is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json")
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
		return errors.New("Paperless-ngx returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Paperless-ngx returned invalid JSON")
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errToken
	case http.StatusNotFound:
		return errNotFound
	case http.StatusTooManyRequests:
		return errRateLimited
	default:
		return fmt.Errorf("Paperless-ngx returned HTTP %d", res.StatusCode)
	}
}

func (c *client) documents(ctx context.Context, query string, pageNumber int) (documentPage, error) {
	if pageNumber < 1 || pageNumber > 100000 {
		return documentPage{}, errors.New("invalid page number")
	}
	q := url.Values{"page": {strconv.Itoa(pageNumber)}, "page_size": {"25"}, "ordering": {"-added"}}
	if query = strings.TrimSpace(query); query != "" {
		if len([]rune(query)) > 500 {
			return documentPage{}, errors.New("search query must be at most 500 characters")
		}
		q.Set("query", query)
	}
	var raw page[document]
	if err := c.get(ctx, "/api/documents/", q, &raw); err != nil {
		return documentPage{}, err
	}
	out := documentPage{Items: raw.Results, Count: raw.Count}
	if raw.Next != nil {
		u, err := url.Parse(*raw.Next)
		if err != nil {
			return documentPage{}, errors.New("Paperless-ngx returned an invalid next link")
		}
		next, err := strconv.Atoi(u.Query().Get("page"))
		if err != nil || next <= pageNumber || next > 100000 {
			return documentPage{}, errors.New("Paperless-ngx returned a non-advancing page link")
		}
		out.HasMore, out.NextPage = true, next
	}
	return out, nil
}

func (c *client) document(ctx context.Context, id int64) (document, error) {
	if id <= 0 {
		return document{}, errors.New("document ID must be positive")
	}
	var out document
	err := c.get(ctx, "/api/documents/"+strconv.FormatInt(id, 10)+"/", nil, &out)
	return out, err
}

func (c *client) named(ctx context.Context, kind string, pageNumber int) (page[namedObject], error) {
	if kind != "tags" && kind != "correspondents" {
		return page[namedObject]{}, errors.New("invalid Paperless-ngx lookup")
	}
	if pageNumber < 1 || pageNumber > 100000 {
		return page[namedObject]{}, errors.New("invalid page number")
	}
	var out page[namedObject]
	err := c.get(ctx, "/api/"+kind+"/", url.Values{"page": {strconv.Itoa(pageNumber)}, "page_size": {"100"}}, &out)
	return out, err
}

func (c *client) originalURL(id int64) string {
	if id <= 0 || !strings.HasPrefix(c.base, "https://") {
		return ""
	}
	return c.base + "/api/documents/" + strconv.FormatInt(id, 10) + "/download/?original=true"
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
