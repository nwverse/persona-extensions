package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type readArgs struct {
	ItemID string `json:"item_id"`
}

func (in readArgs) validate() error {
	if !validItemID(in.ItemID) {
		return errors.New("item ID must be an exact reader item ID returned by FreshRSS")
	}
	return nil
}

func describeAction(name string, raw json.RawMessage) (map[string]any, error) {
	if name != "freshrss_mark_read" {
		return nil, fmt.Errorf("unknown action %q", name)
	}
	var in readArgs
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": "Mark FreshRSS article as read\nItem: " + in.ItemID}, nil
}

func (a *app) doAction(ctx context.Context, requestID, name string, raw json.RawMessage) (map[string]any, error) {
	if name != "freshrss_mark_read" {
		return nil, fmt.Errorf("unknown action %q", name)
	}
	var in readArgs
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	keyData, _ := json.Marshal(raw)
	text, err := a.receipts.commit(requestID, string(keyData), func() (string, error) {
		if err := a.client.markRead(ctx, in.ItemID); err != nil {
			return "", err
		}
		return "Marked FreshRSS article as read", nil
	})
	if err != nil {
		return nil, err
	}
	a.record(nil)
	if a.hub != nil {
		_ = a.hub.Invalidate(pageUI)
	}
	return map[string]any{"text": text}, nil
}
