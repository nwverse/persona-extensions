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
	errToken       = errors.New("Readeck rejected the API token; replace it in extension settings")
	errUnavailable = errors.New("Readeck could not be reached; try again later")
	errNotFound    = errors.New("Readeck could not find that bookmark")
	errRateLimited = errors.New("Readeck rate limit reached; try again later")
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

func (c *client) do(ctx context.Context, method, path string, query url.Values, in, out any) (http.Header, error) {
	if c.token == "" {
		return nil, errors.New("Readeck API token is not configured")
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
	if body != nil {
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
		return nil, errors.New("Readeck returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
		if out == nil || len(data) == 0 {
			return res.Header, nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return nil, errors.New("Readeck returned invalid JSON")
		}
		return res.Header, nil
	case http.StatusNoContent:
		return res.Header, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errToken
	case http.StatusNotFound:
		return nil, errNotFound
	case http.StatusTooManyRequests:
		return nil, errRateLimited
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return nil, errors.New("Readeck rejected the operation; check the fields and permissions")
	default:
		return nil, fmt.Errorf("Readeck returned HTTP %d", res.StatusCode)
	}
}

func (c *client) bookmarks(ctx context.Context, view, query string, offset int) (bookmarkPage, error) {
	if offset < 0 || offset > 1000000 {
		return bookmarkPage{}, errors.New("invalid pagination offset")
	}
	q := url.Values{"limit": {"30"}, "offset": {strconv.Itoa(offset)}, "sort": {"-created"}, "is_archived": {"false"}}
	switch view {
	case "unread":
		q.Set("read_status", "unread")
	case "favorites":
		q.Set("is_marked", "true")
	case "search":
		if strings.TrimSpace(query) == "" || len([]rune(query)) > 200 {
			return bookmarkPage{}, errors.New("search requires at most 200 characters")
		}
		q.Set("search", query)
	default:
		return bookmarkPage{}, errors.New("invalid reading view")
	}
	var out bookmarkPage
	h, err := c.do(ctx, http.MethodGet, "/bookmarks", q, nil, &out.Items)
	if err != nil {
		return bookmarkPage{}, err
	}
	out.Total, _ = strconv.Atoi(h.Get("Total-Count"))
	for _, part := range strings.Split(h.Get("Link"), ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		start, end := strings.Index(part, "<"), strings.Index(part, ">")
		if start < 0 || end <= start {
			return bookmarkPage{}, errors.New("Readeck returned an invalid pagination link")
		}
		u, err := url.Parse(part[start+1 : end])
		if err != nil {
			return bookmarkPage{}, errors.New("Readeck returned an invalid pagination link")
		}
		next, err := strconv.Atoi(u.Query().Get("offset"))
		if err != nil || next <= offset || next > 1000000 {
			return bookmarkPage{}, errors.New("Readeck returned an invalid next offset")
		}
		out.HasMore, out.NextPage = true, next
	}
	return out, nil
}

func (c *client) bookmark(ctx context.Context, id string) (bookmark, error) {
	if !validBookmarkID(id) {
		return bookmark{}, errors.New("invalid Readeck bookmark ID")
	}
	var out bookmark
	_, err := c.do(ctx, http.MethodGet, "/bookmarks/"+id, nil, nil, &out)
	return out, err
}

func (c *client) article(ctx context.Context, id string) (string, error) {
	if !validBookmarkID(id) {
		return "", errors.New("invalid Readeck bookmark ID")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/bookmarks/"+id+"/article.md", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "text/markdown")
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errUnavailable, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		return "", errors.New("Readeck article is too large or could not be read")
	}
	if res.StatusCode == http.StatusNotFound {
		return "", errNotFound
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", errToken
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(res.Header.Get("Content-Type")), "text/markdown") {
		return "", errors.New("Readeck did not return the saved Markdown article")
	}
	return string(data), nil
}

func (c *client) highlights(ctx context.Context, id string) ([]highlight, error) {
	if !validBookmarkID(id) {
		return nil, errors.New("invalid Readeck bookmark ID")
	}
	var out []highlight
	_, err := c.do(ctx, http.MethodGet, "/bookmarks/"+id+"/annotations", nil, nil, &out)
	return out, err
}

func (c *client) save(ctx context.Context, rawURL, title string, labels []string) (string, error) {
	h, err := c.do(ctx, http.MethodPost, "/bookmarks", nil, map[string]any{"url": rawURL, "title": title, "labels": labels}, nil)
	if err != nil {
		return "", err
	}
	id := h.Get("Bookmark-Id")
	if !validBookmarkID(id) {
		return "", errors.New("Readeck accepted the bookmark without a valid Bookmark-Id")
	}
	return id, nil
}

func (c *client) updateReading(ctx context.Context, id string, progress *int, marked *bool, note *string) error {
	if !validBookmarkID(id) {
		return errors.New("invalid Readeck bookmark ID")
	}
	body := map[string]any{}
	if progress != nil {
		body["read_progress"] = *progress
	}
	if marked != nil {
		body["is_marked"] = *marked
	}
	if note != nil {
		body["note"] = *note
	}
	if len(body) == 0 {
		return errors.New("a reading update is required")
	}
	_, err := c.do(ctx, http.MethodPatch, "/bookmarks/"+id, nil, body, nil)
	return err
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
