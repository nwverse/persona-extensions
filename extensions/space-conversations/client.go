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

// Track is the core's stable, headless space record. The extension never
// duplicates it in a private data file.
type Track struct {
	Slug              string   `json:"slug"`
	Kind              string   `json:"kind"`
	Title             string   `json:"title"`
	Icon              string   `json:"icon"`
	Description       string   `json:"description"`
	DefaultCategories []string `json:"default_categories"`
	Archived          bool     `json:"archived"`
	Builtin           bool     `json:"builtin"`
	Revision          int64    `json:"revision"`
	Config            struct {
		Sidebar struct {
			Group string `json:"group"`
			Icon  string `json:"icon"`
		} `json:"sidebar"`
	} `json:"config"`
}

type Conversation struct {
	SessionID string `json:"session_id"`
	MachineID string `json:"machine_id"`
	Title     string `json:"title"`
	LastText  string `json:"last_text"`
	UpdatedAt string `json:"updated_at"`
	Space     string `json:"space"`
}

type SpaceMD struct {
	Content string `json:"content"`
	Digest  string `json:"digest"`
	State   string `json:"state"`
}

type Client struct {
	base, token string
	http        *http.Client
}

func NewClient(base, token string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	if c == nil || c.base == "" || c.token == "" {
		return errors.New("空间对话：Hub 尚未提供访问凭据")
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := e.Error
		if msg == "" {
			msg = e.Message
		}
		return fmt.Errorf("Hub 返回 HTTP %d：%s", resp.StatusCode, msg)
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) Tracks(ctx context.Context) ([]Track, error) {
	var out struct {
		Tracks []Track `json:"tracks"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/v1/assistant/tracks", nil, &out); err != nil {
		return nil, err
	}
	return out.Tracks, nil
}

func (c *Client) Conversations(ctx context.Context, slug string) (string, []Conversation, error) {
	var out struct {
		MachineID     string         `json:"machine_id"`
		Conversations []Conversation `json:"conversations"`
	}
	err := c.call(ctx, http.MethodGet, "/api/v1/assistant?space="+url.QueryEscape(slug), nil, &out)
	return out.MachineID, out.Conversations, err
}

func (c *Client) SpaceMD(ctx context.Context, slug string) (SpaceMD, error) {
	var out SpaceMD
	err := c.call(ctx, http.MethodGet, "/api/v1/assistant/tracks/"+url.PathEscape(slug)+"/space-md", nil, &out)
	return out, err
}

func (c *Client) PutSpaceMD(ctx context.Context, slug, content, digest string) error {
	return c.call(ctx, http.MethodPut, "/api/v1/assistant/tracks/"+url.PathEscape(slug)+"/space-md", map[string]string{"content": content, "base_digest": digest}, nil)
}

func (c *Client) Create(ctx context.Context, title, slug, description string, categories []string) (Track, error) {
	var out Track
	err := c.call(ctx, http.MethodPost, "/api/v1/assistant/tracks", map[string]any{
		"kind": "space", "slug": slug, "title": title, "description": description, "default_categories": categories,
	}, &out)
	return out, err
}

func (c *Client) Archive(ctx context.Context, track Track, archived bool) error {
	return c.call(ctx, http.MethodPut, "/api/v1/assistant/tracks/"+url.PathEscape(track.Slug), map[string]any{"revision": track.Revision, "archived": archived}, nil)
}

func (c *Client) Start(ctx context.Context, slug, prompt string) (string, string, error) {
	var out struct {
		Session Conversation `json:"session"`
	}
	err := c.call(ctx, http.MethodPost, "/api/v1/assistant/conversations?space="+url.QueryEscape(slug), map[string]any{"space": slug, "prompt": prompt}, &out)
	return out.Session.MachineID, out.Session.SessionID, err
}
