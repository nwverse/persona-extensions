package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	errToken       = errors.New("Immich rejected the API key; replace it in extension settings")
	errUnavailable = errors.New("Immich could not be reached; try again later")
	errNotFound    = errors.New("Immich could not find that asset or album")
	errRateLimited = errors.New("Immich rate limit reached; try again later")
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

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	if c.token == "" {
		return errors.New("Immich API key is not configured")
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
	req.Header.Set("x-api-key", c.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
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
		return errors.New("Immich returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return errors.New("Immich returned invalid JSON")
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
		return errors.New("Immich rejected the operation; check the fields and API-key permissions")
	default:
		return fmt.Errorf("Immich returned HTTP %d", res.StatusCode)
	}
}

func (c *client) search(ctx context.Context, query string, favoritesOnly bool, cursor string) (searchResponse, error) {
	if cursor != "" && len(cursor) > 4096 {
		return searchResponse{}, errors.New("invalid search cursor")
	}
	body := map[string]any{"size": 40, "withExif": true, "orderBy": map[string]string{"field": "fileCreatedAt", "direction": "desc"}}
	if favoritesOnly {
		body["filter"] = map[string]any{"isFavorite": map[string]bool{"eq": true}}
	} else {
		query = strings.TrimSpace(query)
		if query == "" || len([]rune(query)) > 200 {
			return searchResponse{}, errors.New("search requires at most 200 characters")
		}
		body["filter"] = map[string]any{"originalFileName": map[string]string{"like": query}}
	}
	if cursor != "" {
		body["cursor"] = cursor
	}
	var out searchResponse
	err := c.do(ctx, http.MethodPost, "/search/metadata", body, &out)
	return out, err
}

func (c *client) smartSearch(ctx context.Context, query string) (searchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 200 {
		return searchResponse{}, errors.New("smart search requires at most 200 characters")
	}
	var out searchResponse
	err := c.do(ctx, http.MethodPost, "/search/smart", map[string]any{"query": query, "size": 40, "withExif": true}, &out)
	return out, err
}

func (c *client) asset(ctx context.Context, id string) (asset, error) {
	if !validAssetID(id) {
		return asset{}, errors.New("invalid Immich asset ID")
	}
	var out asset
	err := c.do(ctx, http.MethodGet, "/assets/"+id, nil, &out)
	return out, err
}

func (c *client) albums(ctx context.Context) ([]album, error) {
	var out []album
	err := c.do(ctx, http.MethodGet, "/albums", nil, &out)
	return out, err
}

func (c *client) album(ctx context.Context, id string) (album, error) {
	if !validAssetID(id) {
		return album{}, errors.New("invalid Immich album ID")
	}
	var out album
	err := c.do(ctx, http.MethodGet, "/albums/"+id, nil, &out)
	return out, err
}

func (c *client) favorite(ctx context.Context, id string, favorite bool) error {
	if !validAssetID(id) {
		return errors.New("invalid Immich asset ID")
	}
	return c.do(ctx, http.MethodPut, "/assets/"+id, map[string]bool{"isFavorite": favorite}, nil)
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
