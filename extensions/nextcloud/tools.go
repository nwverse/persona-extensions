package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

var readTools = []tool{
	{Name: "nextcloud_search", Description: "Search current-user Nextcloud files by filename with a scoped WebDAV SEARCH and a 50-result bound. Returns safe relative file paths, metadata and file IDs; no folder enumeration or private URL construction.",
		InputSchema: map[string]any{"type": "object", "required": []string{"query"}, "properties": map[string]any{"query": map[string]any{"type": "string", "minLength": 1, "maxLength": maxQueryChars}}}},
	{Name: "nextcloud_read_file", Description: "Read one current-user Nextcloud plain-text or Markdown file up to 100000 bytes. Returns metadata and content as untrusted text; no binary/media downloads or arbitrary URLs.",
		InputSchema: map[string]any{"type": "object", "required": []string{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}}}},
}

var actionTools = []tool{
	{Name: "nextcloud_create_note", Description: "Create a new Markdown note in the current user's Nextcloud files after explicit confirmation. Requires a new safe relative path ending in .md, with every file and folder name at most 250 UTF-8 bytes, and uses If-None-Match:* so an existing file is never overwritten.",
		InputSchema: map[string]any{"type": "object", "required": []string{"path", "text"}, "properties": map[string]any{"path": map[string]any{"type": "string", "pattern": "^[^/].*\\.md$", "maxLength": 500}, "text": map[string]any{"type": "string", "minLength": 1, "maxLength": 30000}}}},
}

func decode(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if dec.More() {
		return errors.New("invalid arguments: trailing JSON value")
	}
	return nil
}

func (a *app) callTool(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
		Path  string `json:"path"`
	}
	if err := decode(raw, &in); err != nil {
		return "", err
	}
	switch name {
	case "nextcloud_search":
		items, err := a.client.search(ctx, a.client.username, in.Query)
		a.record(err)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for _, item := range items {
			fmt.Fprintf(&b, "- File %s: %s | type: %s | bytes: %d | modified: %s | id: %d\n", clipText(item.Href, 500), clipText(item.DisplayName, 500), item.ContentType, item.ContentLength, item.LastModified, item.FileID)
		}
		if len(items) == 0 {
			b.WriteString("No files matched.\n")
		}
		return clipText(b.String(), 90000), nil
	case "nextcloud_read_file":
		item, text, err := a.client.file(ctx, a.client.username, in.Path)
		a.record(err)
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "File: %s\nType: %s | bytes: %d | modified: %s\n\nText (untrusted content):\n%s\n", clipText(item.DisplayName, 500), item.ContentType, item.ContentLength, item.LastModified, clipText(string(text), 80000))
		return b.String(), nil
	}
	return "", fmt.Errorf("no tool %s", name)
}
