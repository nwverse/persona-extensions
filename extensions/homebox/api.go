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
	errToken       = errors.New("Homebox rejected the API token; replace it in extension settings")
	errUnavailable = errors.New("Homebox could not be reached; try again later")
	errNotFound    = errors.New("Homebox could not find that entity")
	errRateLimited = errors.New("Homebox rate limit reached; try again later")
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
		return errors.New("Homebox API token is not configured")
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
		return errors.New("Homebox returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return errors.New("Homebox returned invalid JSON")
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
		return errors.New("Homebox rejected the operation; check the fields and permissions")
	default:
		return fmt.Errorf("Homebox returned HTTP %d", res.StatusCode)
	}
}

func (c *client) entities(ctx context.Context, query string, page int) (entityPage, error) {
	if page < 1 || page > 100000 {
		return entityPage{}, errors.New("invalid page number")
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {"30"}, "orderBy": {"updatedAt"}, "orderDirection": {"desc"}}
	if query = strings.TrimSpace(query); query != "" {
		if len([]rune(query)) > 200 {
			return entityPage{}, errors.New("search query must be at most 200 characters")
		}
		q.Set("q", query)
	}
	var out entityPage
	err := c.do(ctx, http.MethodGet, "/v1/entities", q, nil, &out)
	return out, err
}

func (c *client) entity(ctx context.Context, id string) (entity, error) {
	if !validEntityID(id) {
		return entity{}, errors.New("invalid Homebox entity ID")
	}
	var out entity
	err := c.do(ctx, http.MethodGet, "/v1/entities/"+id, nil, nil, &out)
	return out, err
}

func (c *client) entityTypes(ctx context.Context) ([]entityType, error) {
	var out []entityType
	err := c.do(ctx, http.MethodGet, "/v1/entity-types", nil, nil, &out)
	return out, err
}

func (c *client) locations(ctx context.Context) ([]treeItem, error) {
	var out []treeItem
	err := c.do(ctx, http.MethodGet, "/v1/entities/tree", url.Values{"withItems": {"false"}}, nil, &out)
	return out, err
}

func (c *client) tags(ctx context.Context) ([]tag, error) {
	var out []tag
	err := c.do(ctx, http.MethodGet, "/v1/tags", nil, nil, &out)
	return out, err
}

func (c *client) createItem(ctx context.Context, typeID, locationID, name, description string, quantity float64, tagIDs []string) (entity, error) {
	if !validEntityID(typeID) || !validEntityID(locationID) {
		return entity{}, errors.New("a discovered type and location are required")
	}
	body := map[string]any{"entityTypeId": typeID, "parentId": locationID, "name": name, "description": description, "quantity": quantity, "tagIds": tagIDs}
	var out entity
	err := c.do(ctx, http.MethodPost, "/v1/entities", nil, body, &out)
	if !validEntityID(out.ID) && err == nil {
		return entity{}, errors.New("Homebox returned an invalid created entity ID")
	}
	return out, err
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
