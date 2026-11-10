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
	errToken       = errors.New("Mattermost rejected the API token; replace it in extension settings")
	errUnavailable = errors.New("Mattermost could not be reached; try again later")
	errNotFound    = errors.New("Mattermost could not find that channel or user")
	errRateLimited = errors.New("Mattermost rate limit reached; try again later")
)

type client struct {
	base, token string
	http        *http.Client
}

func newClient(base, token string) *client {
	base = strings.TrimRight(base, "/")
	if base != "" && !strings.HasSuffix(base, "/api/v4") {
		base += "/api/v4"
	}
	return &client{base: base, token: strings.TrimSpace(token), http: &http.Client{
		Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	if c.token == "" {
		return errors.New("Mattermost API token is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
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
		return errors.New("Mattermost returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Mattermost returned invalid JSON")
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errToken
	case http.StatusNotFound:
		return errNotFound
	case http.StatusTooManyRequests:
		return errRateLimited
	default:
		return fmt.Errorf("Mattermost returned HTTP %d", res.StatusCode)
	}
}

func (c *client) currentUser(ctx context.Context) (user, error) {
	var out user
	err := c.get(ctx, "/users/me", nil, &out)
	return out, err
}

func (c *client) user(ctx context.Context, id string) (user, error) {
	if !validID(id) {
		return user{}, errors.New("invalid Mattermost user ID")
	}
	var out user
	err := c.get(ctx, "/users/"+id, nil, &out)
	return out, err
}

func (c *client) teams(ctx context.Context) ([]team, error) {
	var out []team
	err := c.get(ctx, "/users/me/teams", nil, &out)
	return out, err
}

func (c *client) channels(ctx context.Context, teamID string) ([]channel, error) {
	if !validID(teamID) {
		return nil, errors.New("invalid Mattermost team ID")
	}
	var out []channel
	err := c.get(ctx, "/users/me/teams/"+teamID+"/channels", nil, &out)
	return out, err
}

func (c *client) unread(ctx context.Context, channelID string) (unread, error) {
	if !validID(channelID) {
		return unread{}, errors.New("invalid Mattermost channel ID")
	}
	var out unread
	err := c.get(ctx, "/users/me/channels/"+channelID+"/unread", nil, &out)
	return out, err
}

func (c *client) posts(ctx context.Context, channelID, before string, page int) (postList, error) {
	if !validID(channelID) {
		return postList{}, errors.New("invalid Mattermost channel ID")
	}
	q := url.Values{"per_page": {"30"}}
	if before != "" {
		if !validID(before) {
			return postList{}, errors.New("invalid before post ID")
		}
		q.Set("before", before)
	} else {
		if page < 0 || page > 100000 {
			return postList{}, errors.New("invalid page number")
		}
		q.Set("page", strconv.Itoa(page))
	}
	var out postList
	err := c.get(ctx, "/channels/"+channelID+"/posts", q, &out)
	return out, err
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
