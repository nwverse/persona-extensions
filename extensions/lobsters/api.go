package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const siteBase = "https://lobste.rs"

var errUnavailable = errors.New("Lobsters could not be reached; try again later")
var errNotFound = errors.New("Lobsters could not find that story")

type client struct {
	base  string
	http  *http.Client
	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	data []byte
	at   time.Time
}

func newClient(base string) *client {
	return &client{base: strings.TrimRight(base, "/"), http: &http.Client{
		Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, cache: map[string]cached{}}
}

func (c *client) get(ctx context.Context, path string, fresh time.Duration) ([]byte, error) {
	c.mu.Lock()
	old, ok := c.cache[path]
	c.mu.Unlock()
	if ok && time.Since(old.at) < fresh {
		return old.data, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		if ok {
			return old.data, fmt.Errorf("cached Lobsters data is stale: %w", errUnavailable)
		}
		return nil, fmt.Errorf("%w: %v", errUnavailable, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20+1))
	if err != nil {
		return nil, errUnavailable
	}
	if len(data) > 4<<20 {
		return nil, errors.New("Lobsters returned an oversized response")
	}
	if res.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if res.StatusCode != http.StatusOK {
		if ok {
			return old.data, fmt.Errorf("cached Lobsters data is stale: HTTP %d", res.StatusCode)
		}
		return nil, fmt.Errorf("Lobsters returned HTTP %d", res.StatusCode)
	}
	if !json.Valid(data) {
		return nil, errors.New("Lobsters returned invalid JSON")
	}
	c.mu.Lock()
	c.cache[path] = cached{data: data, at: time.Now()}
	if len(c.cache) > 64 {
		var oldest string
		var at time.Time
		for path, entry := range c.cache {
			if oldest == "" || entry.at.Before(at) {
				oldest, at = path, entry.at
			}
		}
		delete(c.cache, oldest)
	}
	c.mu.Unlock()
	return data, nil
}

func (c *client) stories(ctx context.Context, newest bool) ([]story, error) {
	path := "/hottest.json"
	if newest {
		path = "/newest.json"
	}
	data, err := c.get(ctx, path, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	var out []story
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, errors.New("Lobsters returned an unexpected story list")
	}
	return out, nil
}

func (c *client) story(ctx context.Context, id string) (story, error) {
	if err := validID(id); err != nil {
		return story{}, err
	}
	data, err := c.get(ctx, "/s/"+id+".json", 5*time.Minute)
	if err != nil {
		return story{}, err
	}
	var out story
	if err := json.Unmarshal(data, &out); err != nil {
		return story{}, errors.New("Lobsters returned an unexpected story")
	}
	return out, nil
}

func validID(id string) error {
	if len(id) == 0 || len(id) > 32 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\x00\r\n/") {
		return errors.New("invalid Lobsters story ID")
	}
	return nil
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}

var _ = fmt.Sprintf
