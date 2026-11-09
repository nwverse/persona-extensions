package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type createArgs struct {
	ProjectID   string `json:"project_id"`
	IssueTypeID string `json:"issuetype_id"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	AssigneeID  string `json:"assignee_id"`
}

func (in createArgs) validate() error {
	for _, id := range []string{in.ProjectID, in.IssueTypeID} {
		if len(id) == 0 || len(id) > 64 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\x00\r\n") {
			return errors.New("invalid project or issue type ID")
		}
	}
	if len([]rune(in.Summary)) == 0 || strings.TrimSpace(in.Summary) == "" || len([]rune(in.Summary)) > 255 {
		return errors.New("summary is required and at most 255 characters")
	}
	if len([]rune(in.Description)) > 100000 {
		return errors.New("description is too long")
	}
	if in.AssigneeID != "" && (len(in.AssigneeID) > 128 || strings.ContainsAny(in.AssigneeID, "\x00\r\n")) {
		return errors.New("invalid assignee ID")
	}
	return nil
}

func describeAction(name string, raw json.RawMessage) (map[string]any, error) {
	var summary string
	switch name {
	case "jira_create":
		var in createArgs
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := in.validate(); err != nil {
			return nil, err
		}
		summary = "Create Jira issue: " + in.Summary + "\nProject ID: " + in.ProjectID + "\nIssue type ID: " + in.IssueTypeID
	case "jira_comment":
		var in struct{ Key, Body string }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := validKey(in.Key); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Body) == "" || len([]rune(in.Body)) > 100000 {
			return nil, errors.New("comment body is required and bounded")
		}
		summary = "Comment on Jira issue " + in.Key + ":\n" + in.Body
	default:
		return nil, fmt.Errorf("unknown action %q", name)
	}
	return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": summary}, nil
}

func (a *app) doAction(ctx context.Context, requestID, name string, raw json.RawMessage) (map[string]any, error) {
	var key string
	var fn func() (string, error)
	switch name {
	case "jira_create":
		var in createArgs
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := in.validate(); err != nil {
			return nil, err
		}
		if in.ProjectID == "" || in.IssueTypeID == "" {
			return nil, errors.New("project and issue type are required")
		}
		types, err := a.client.issueTypes(ctx, in.ProjectID)
		if err != nil {
			return nil, err
		}
		valid := false
		for _, typ := range types {
			if typ.ID == in.IssueTypeID && !typ.Subtask {
				valid = true
			}
		}
		if !valid {
			return nil, errors.New("issue type does not belong to this project or is a subtask")
		}
		fields := map[string]any{"project": map[string]string{"id": in.ProjectID}, "issuetype": map[string]string{"id": in.IssueTypeID}, "summary": in.Summary}
		if in.Description != "" {
			fields["description"] = in.Description
		}
		if in.AssigneeID != "" {
			fields["assignee"] = map[string]string{"accountId": in.AssigneeID}
		}
		keyData, _ := json.Marshal(map[string]any{"action": name, "fields": fields})
		key = string(keyData)
		fn = func() (string, error) {
			item, err := a.client.create(ctx, map[string]any{"fields": fields})
			if err != nil {
				return "", err
			}
			if item.Key == "" {
				return "", errors.New("Jira returned no issue key")
			}
			return "Created Jira issue " + item.Key, nil
		}
	case "jira_comment":
		var in struct{ Key, Body string }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := validKey(in.Key); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Body) == "" || len([]rune(in.Body)) > 100000 {
			return nil, errors.New("comment body is required and bounded")
		}
		keyData, _ := json.Marshal(map[string]any{"action": name, "key": in.Key, "body": in.Body})
		key = string(keyData)
		fn = func() (string, error) {
			if err := a.client.comment(ctx, in.Key, in.Body); err != nil {
				return "", err
			}
			return "Commented on Jira issue " + in.Key, nil
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
