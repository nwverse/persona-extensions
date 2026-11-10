package main

import (
	"context"
	"encoding/json"
	"fmt"
)

func describeAction(name string, raw json.RawMessage) (map[string]any, error) {
	if name != "mealie_import_recipe" {
		return nil, fmt.Errorf("unknown action %q", name)
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	clean, err := publicImportURL(in.URL)
	if err != nil {
		return nil, err
	}
	return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": "Import a recipe into Mealie\nPublic webpage: " + clean}, nil
}

func (a *app) doAction(ctx context.Context, requestID, name string, raw json.RawMessage) (map[string]any, error) {
	if name != "mealie_import_recipe" {
		return nil, fmt.Errorf("unknown action %q", name)
	}
	var in struct {
		URL string `json:"url"`
	}
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	clean, err := publicImportURL(in.URL)
	if err != nil {
		return nil, err
	}
	keyData, _ := json.Marshal(map[string]string{"url": clean})
	text, err := a.receipts.commit(requestID, string(keyData), func() (string, error) {
		slug, err := a.client.importRecipe(ctx, clean)
		if err != nil {
			return "", err
		}
		return "Imported Mealie recipe: " + slug, nil
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
