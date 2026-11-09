package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type updateArgs struct {
	ID         string  `json:"id"`
	Note       *string `json:"note"`
	Favourited *bool   `json:"favourited"`
	Archived   *bool   `json:"archived"`
}

func (in updateArgs) patch() (updatePatch, error) {
	if err := validID(in.ID); err != nil {
		return updatePatch{}, err
	}
	var out updatePatch
	if in.Note != nil {
		if len([]rune(*in.Note)) > 10000 {
			return out, errors.New("note exceeds 10000 characters")
		}
		out.Note = in.Note
	}
	if in.Favourited != nil {
		out.Favourited = in.Favourited
	}
	if in.Archived != nil {
		out.Archived = in.Archived
	}
	if out.empty() {
		return out, errors.New("supply at least one field to update; omitted fields remain unchanged")
	}
	return out, nil
}

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
	case "karakeep_save":
		var in struct{ URL string }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		u, err := validHTTPURL(in.URL)
		if err != nil {
			return nil, err
		}
		summary = "Save to Karakeep: " + u
	case "karakeep_update":
		var in updateArgs
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		patch, err := in.patch()
		if err != nil {
			return nil, err
		}
		data, _ := json.Marshal(patch)
		summary = "Update Karakeep bookmark " + in.ID + "\nOnly these fields: " + string(data)
	default:
		return nil, fmt.Errorf("unknown action %q", name)
	}
	return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": summary}, nil
}

func (a *app) doAction(ctx context.Context, requestID, name string, raw json.RawMessage) (map[string]any, error) {
	var key string
	var fn func() (string, error)
	switch name {
	case "karakeep_save":
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
				return "", errors.New("Karakeep returned no bookmark ID")
			}
			return "Saved to Karakeep: " + item.ID, nil
		}
	case "karakeep_update":
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
			return "Updated Karakeep bookmark " + in.ID, nil
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
