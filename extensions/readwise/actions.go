package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func describeAction(name string, raw json.RawMessage) (map[string]any, error) {
	var summary string
	switch name {
	case "readwise_save":
		var in struct{ URL string }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		u, err := validHTTPURL(in.URL)
		if err != nil {
			return nil, err
		}
		summary = "Save to Readwise Reader: " + u
	case "readwise_update":
		var in updateArgs
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		patch, err := in.patch()
		if err != nil {
			return nil, err
		}
		data, _ := json.Marshal(patch)
		summary = "Update Readwise document " + in.ID + "\nOnly these fields: " + string(data)
	default:
		return nil, fmt.Errorf("unknown action %q", name)
	}
	return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": summary}, nil
}

func (a *app) doAction(ctx context.Context, requestID, name string, raw json.RawMessage) (map[string]any, error) {
	var key string
	var fn func() (string, error)
	switch name {
	case "readwise_save":
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
			if item.ID == "" {
				return "", errors.New("Readwise returned no document ID")
			}
			return "Saved to Readwise Reader: " + item.ID, nil
		}
	case "readwise_update":
		var in updateArgs
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		patch, err := in.patch()
		if err != nil {
			return nil, err
		}
		keyData, _ := json.Marshal(map[string]any{"action": name, "id": in.ID, "patch": patch})
		key = string(keyData)
		fn = func() (string, error) {
			if err := a.client.update(ctx, in.ID, patch); err != nil {
				return "", err
			}
			return "Updated Readwise document " + in.ID, nil
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

func validLocation(location string) error {
	if !map[string]bool{"new": true, "later": true, "shortlist": true, "archive": true, "feed": true}[location] {
		return errors.New("invalid Reader location")
	}
	return nil
}

var _ = strings.TrimSpace
