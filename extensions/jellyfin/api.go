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
	errToken       = errors.New("Jellyfin rejected the API key; replace it in extension settings")
	errUnavailable = errors.New("Jellyfin could not be reached; try again later")
	errNotFound    = errors.New("Jellyfin could not find that media item")
	errRateLimited = errors.New("Jellyfin rate limit reached; try again later")
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

func (c *client) do(ctx context.Context, method, path string, query url.Values, out any) error {
	if c.token == "" {
		return errors.New("Jellyfin API key is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Token", c.token)
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
		return errors.New("Jellyfin returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return errors.New("Jellyfin returned invalid JSON")
			}
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
	default:
		return fmt.Errorf("Jellyfin returned HTTP %d", res.StatusCode)
	}
}

func (c *client) currentUser(ctx context.Context) (user, error) {
	var out user
	err := c.do(ctx, http.MethodGet, "/Users/Me", nil, &out)
	if out.ID == "" || !validMediaID(out.ID) {
		return user{}, errors.New("Jellyfin returned an invalid current user ID")
	}
	return out, err
}

func (c *client) items(ctx context.Context, userID, view, query string, start int) (itemPage, error) {
	if !validMediaID(userID) || start < 0 || start > 1000000 {
		return itemPage{}, errors.New("invalid user or start index")
	}
	path := "/Items"
	q := url.Values{"userId": {userID}, "startIndex": {strconv.Itoa(start)}, "limit": {"30"}, "enableUserData": {"true"}, "fields": {"Overview,SeriesName,SeasonName"}}
	switch view {
	case "resume":
		path = "/UserItems/Resume"
	case "favorites":
		q.Set("filters", "IsFavorite")
		q.Set("sortBy", "SortName")
		q.Set("sortOrder", "Ascending")
	case "search":
		query = strings.TrimSpace(query)
		if query == "" || len([]rune(query)) > 200 {
			return itemPage{}, errors.New("search requires at most 200 characters")
		}
		q.Set("searchTerm", query)
		q.Set("sortBy", "SortName")
		q.Set("sortOrder", "Ascending")
	default:
		return itemPage{}, errors.New("invalid media library view")
	}
	var out itemPage
	err := c.do(ctx, http.MethodGet, path, q, &out)
	return out, err
}

func (c *client) item(ctx context.Context, userID, itemID string) (mediaItem, error) {
	if !validMediaID(userID) || !validMediaID(itemID) {
		return mediaItem{}, errors.New("invalid media item ID")
	}
	var out mediaItem
	err := c.do(ctx, http.MethodGet, "/Users/"+userID+"/Items/"+itemID, nil, &out)
	return out, err
}

func (c *client) favorite(ctx context.Context, userID, itemID string, favorite bool) error {
	if !validMediaID(userID) || !validMediaID(itemID) {
		return errors.New("invalid media item ID")
	}
	method := http.MethodPost
	if !favorite {
		method = http.MethodDelete
	}
	q := url.Values{"userId": {userID}}
	return c.do(ctx, method, "/UserFavoriteItems/"+itemID, q, nil)
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
