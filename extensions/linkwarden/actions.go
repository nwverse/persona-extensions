package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func validHTTPURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("url must be an absolute http(s) URL without embedded credentials")
	}
	u.Fragment = ""
	if len(u.String()) > 8192 {
		return "", errors.New("url is too long")
	}
	return u.String(), nil
}

func describeAction(name string, raw json.RawMessage) (map[string]any, error) {
	var summary string
	switch name {
	case "linkwarden_save":
		var in struct{ URL string }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		u, err := validHTTPURL(in.URL)
		if err != nil {
			return nil, err
		}
		summary = "Save to Linkwarden: " + u
	case "linkwarden_archive":
		var in struct{ ID int64 }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if in.ID <= 0 {
			return nil, errors.New("link ID must be positive")
		}
		summary = fmt.Sprintf("Archive Linkwarden link %d", in.ID)
	default:
		return nil, fmt.Errorf("unknown action %q", name)
	}
	return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": summary}, nil
}

func (a *app) doAction(ctx context.Context, requestID, name string, raw json.RawMessage) (map[string]any, error) {
	var key string
	var fn func() (string, error)
	switch name {
	case "linkwarden_save":
		var in struct{ URL string }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		u, err := validHTTPURL(in.URL)
		if err != nil {
			return nil, err
		}
		keyData, _ := json.Marshal(map[string]any{"action": name, "url": u})
		key = string(keyData)
		fn = func() (string, error) {
			item, err := a.client.save(ctx, u)
			if err != nil {
				return "", err
			}
			if item.ID <= 0 {
				return "", errors.New("Linkwarden returned no link ID")
			}
			return fmt.Sprintf("Saved to Linkwarden: %d", item.ID), nil
		}
	case "linkwarden_archive":
		var in struct{ ID int64 }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if in.ID <= 0 {
			return nil, errors.New("link ID must be positive")
		}
		keyData, _ := json.Marshal(map[string]any{"action": name, "id": in.ID})
		key = string(keyData)
		fn = func() (string, error) {
			if err := a.client.archive(ctx, in.ID); err != nil {
				return "", err
			}
			return fmt.Sprintf("Archived Linkwarden link %d", in.ID), nil
		}
	default:
		return nil, fmt.Errorf("unknown action %q", name)
	}
	text, err := a.receipts.commit(requestID, key, fn)
	if err != nil {
		return nil, err
	}
	a.record(nil)
	if a.hub != nil {
		_ = a.hub.Invalidate(pageUI)
	}
	return map[string]any{"text": text}, nil
}

var _ = strings.TrimSpace
