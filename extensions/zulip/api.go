package main

import (
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

var (
	errToken       = errors.New("Zulip rejected the API email or key; replace them in extension settings")
	errUnavailable = errors.New("Zulip could not be reached; try again later")
	errNotFound    = errors.New("Zulip could not find that stream or topic")
	errRateLimited = errors.New("Zulip rate limit reached; try again later")
)

type client struct {
	base, email, token string
	http               *http.Client
}

func newClient(base, email, token string) *client {
	base = strings.TrimRight(base, "/")
	if base != "" && !strings.HasSuffix(base, "/api/v1") {
		base += "/api/v1"
	}
	return &client{base: base, email: strings.TrimSpace(email), token: strings.TrimSpace(token), http: &http.Client{
		Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	if c.email == "" || c.token == "" {
		return errors.New("Zulip API email and key are required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.email, c.token)
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
		return errors.New("Zulip returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK:
		var status apiError
		if err := json.Unmarshal(data, &status); err != nil || status.Result != "success" {
			return errors.New("Zulip returned an unsuccessful or invalid JSON response")
		}
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return errors.New("Zulip returned an invalid response schema")
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
		return errors.New("Zulip rejected the query; check stream/topic and pagination")
	default:
		return fmt.Errorf("Zulip returned HTTP %d", res.StatusCode)
	}
}

func (c *client) streams(ctx context.Context) ([]stream, error) {
	var out struct {
		Streams []stream `json:"streams"`
	}
	err := c.get(ctx, "/users/me/subscriptions", nil, &out)
	return out.Streams, err
}

func (c *client) topics(ctx context.Context, streamID int64) ([]topic, error) {
	if streamID <= 0 {
		return nil, errors.New("stream ID must be positive")
	}
	var out struct {
		Topics []topic `json:"topics"`
	}
	err := c.get(ctx, "/users/me/subscriptions/"+fmt.Sprint(streamID)+"/topics", nil, &out)
	return out.Topics, err
}

func (c *client) messages(ctx context.Context, streamID int64, topicName string, anchor int64, firstUnread bool) (messagePage, error) {
	if anchor < 0 {
		return messagePage{}, errors.New("invalid message anchor")
	}
	narrow := []map[string]any{}
	if streamID > 0 {
		narrow = append(narrow, map[string]any{"operator": "stream", "operand": streamID})
	}
	if topicName != "" {
		if len([]rune(topicName)) > 500 || strings.ContainsAny(topicName, "\x00\r\n") {
			return messagePage{}, errors.New("invalid topic name")
		}
		narrow = append(narrow, map[string]any{"operator": "topic", "operand": topicName})
	}
	encoded, _ := json.Marshal(narrow)
	q := url.Values{"narrow": {string(encoded)}, "num_before": {"15"}, "num_after": {"15"}, "apply_markdown": {"false"}, "allow_empty_topic_name": {"true"}}
	if firstUnread {
		q.Set("use_first_unread_anchor", "true")
	} else {
		q.Set("anchor", fmt.Sprint(anchor))
	}
	var out messagePage
	err := c.get(ctx, "/messages", q, &out)
	return out, err
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
