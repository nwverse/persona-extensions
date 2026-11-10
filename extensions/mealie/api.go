package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	errToken       = errors.New("Mealie rejected the API token; replace it in extension settings")
	errUnavailable = errors.New("Mealie could not be reached; try again later")
	errNotFound    = errors.New("Mealie could not find that recipe")
	errRateLimited = errors.New("Mealie rate limit reached; try again later")
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
		Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	if c.token == "" {
		return errors.New("Mealie API token is not configured")
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
		return errors.New("Mealie returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return errors.New("Mealie returned invalid JSON")
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
		return errors.New("Mealie rejected the operation; check the URL, fields and permissions")
	default:
		return fmt.Errorf("Mealie returned HTTP %d", res.StatusCode)
	}
}

func (c *client) recipes(ctx context.Context, query string, page int) (recipePage, error) {
	if page < 1 || page > 100000 {
		return recipePage{}, errors.New("invalid page number")
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "perPage": {"30"}, "orderBy": {"date_updated"}, "orderDirection": {"desc"}}
	if query = strings.TrimSpace(query); query != "" {
		if len([]rune(query)) > 200 {
			return recipePage{}, errors.New("search query must be at most 200 characters")
		}
		q.Set("search", query)
	}
	var out recipePage
	err := c.do(ctx, http.MethodGet, "/recipes", q, nil, &out)
	return out, err
}

func (c *client) recipe(ctx context.Context, slug string) (recipe, error) {
	if !validRecipeSlug(slug) {
		return recipe{}, errors.New("invalid Mealie recipe slug")
	}
	var out recipe
	err := c.do(ctx, http.MethodGet, "/recipes/"+slug, nil, nil, &out)
	return out, err
}

func (c *client) importRecipe(ctx context.Context, rawURL string) (string, error) {
	var slug string
	if err := c.do(ctx, http.MethodPost, "/recipes/create/url", nil, map[string]string{"url": rawURL}, &slug); err != nil {
		return "", err
	}
	if !validRecipeSlug(slug) {
		return "", errors.New("Mealie returned an invalid imported recipe slug")
	}
	return slug, nil
}

func publicImportURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(raw) > 2048 {
		return "", errors.New("import URL must be a clean public HTTP(S) address of at most 2048 characters")
	}
	hostname := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if hostname == "localhost" || strings.HasSuffix(hostname, ".local") || strings.HasSuffix(hostname, ".internal") {
		return "", errors.New("import URL must not target a local or private hostname")
	}
	if ip := net.ParseIP(hostname); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()) {
		return "", errors.New("import URL must not target a local or private IP address")
	}
	return u.String(), nil
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
