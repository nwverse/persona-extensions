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

const apiBase = "https://api.lunchmoney.dev/v2"

var (
	errToken       = errors.New("Lunch Money rejected the API token; replace it in extension settings")
	errUnavailable = errors.New("Lunch Money could not be reached; try again later")
	errNotFound    = errors.New("Lunch Money could not find that resource")
	errRateLimited = errors.New("Lunch Money rate limit reached; try again later")
)

type client struct {
	token string
	http  *http.Client
}

func newClient(token string) *client {
	return &client{token: strings.TrimSpace(token), http: &http.Client{
		Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	if c.token == "" {
		return errors.New("Lunch Money API token is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
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
		return errors.New("Lunch Money returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(data, out); err != nil {
			return errors.New("Lunch Money returned invalid JSON")
		}
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return errToken
	case http.StatusNotFound:
		return errNotFound
	case http.StatusTooManyRequests:
		return errRateLimited
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return errors.New("Lunch Money rejected the query; check dates, category and pagination")
	default:
		return fmt.Errorf("Lunch Money returned HTTP %d", res.StatusCode)
	}
}

func (c *client) transactions(ctx context.Context, start, end string, categoryID int64, offset int) (transactionPage, error) {
	if offset < 0 || offset > 100000000 {
		return transactionPage{}, errors.New("invalid pagination offset")
	}
	q := url.Values{"limit": {"50"}, "offset": {strconv.Itoa(offset)}}
	if start != "" || end != "" {
		if start == "" || end == "" {
			return transactionPage{}, errors.New("start_date and end_date must be supplied together")
		}
		startDate, err := time.Parse("2006-01-02", start)
		if err != nil {
			return transactionPage{}, errors.New("start date must be YYYY-MM-DD")
		}
		endDate, err := time.Parse("2006-01-02", end)
		if err != nil || endDate.Before(startDate) {
			return transactionPage{}, errors.New("end date must be YYYY-MM-DD and not before start date")
		}
		q.Set("start_date", start)
		q.Set("end_date", end)
	}
	if categoryID < 0 {
		return transactionPage{}, errors.New("category ID must not be negative")
	}
	if categoryID > 0 {
		q.Set("category_id", strconv.FormatInt(categoryID, 10))
	}
	var out transactionPage
	if err := c.get(ctx, "/transactions", q, &out); err != nil {
		return transactionPage{}, err
	}
	if out.Error != "" {
		return transactionPage{}, errors.New("Lunch Money returned a transaction query error")
	}
	return out, nil
}

func (c *client) accounts(ctx context.Context, kind string) ([]account, error) {
	if kind != "manual_accounts" && kind != "plaid_accounts" {
		return nil, errors.New("invalid account type")
	}
	var out struct {
		Accounts []account `json:"accounts"`
	}
	var manual struct {
		Accounts []account `json:"manual_accounts"`
	}
	if kind == "manual_accounts" {
		if err := c.get(ctx, "/manual_accounts", nil, &manual); err != nil {
			return nil, err
		}
		return manual.Accounts, nil
	}
	if err := c.get(ctx, "/plaid_accounts", nil, &out); err != nil {
		return nil, err
	}
	return out.Accounts, nil
}

func (c *client) categories(ctx context.Context) ([]category, error) {
	var out struct {
		Categories []category `json:"categories"`
	}
	err := c.get(ctx, "/categories", url.Values{"format": {"flattened"}}, &out)
	return out.Categories, err
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
