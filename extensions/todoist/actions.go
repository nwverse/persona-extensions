package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

type createArgs struct {
	Content     string   `json:"content"`
	Description string   `json:"description"`
	Priority    int      `json:"priority"`
	DueString   string   `json:"due_string"`
	DueDate     string   `json:"due_date"`
	DueDatetime string   `json:"due_datetime"`
	Labels      []string `json:"labels"`
	ProjectID   string   `json:"project_id"`
	SectionID   string   `json:"section_id"`
}

func (in createArgs) validate() error {
	if err := validText(in.Content, 500, false); err != nil || strings.TrimSpace(in.Content) == "" {
		return errors.New("content is required, nonempty and at most 500 characters")
	}
	if err := validText(in.Description, 16384, true); err != nil {
		return err
	}
	if in.Priority < 0 || in.Priority > 4 || len(in.Labels) > 20 {
		return errors.New("priority or label count is invalid")
	}
	if in.DueString != "" && (in.DueDate != "" || in.DueDatetime != "") || in.DueDate != "" && in.DueDatetime != "" {
		return errors.New("supply only one of due_string, due_date or due_datetime")
	}
	if in.DueString != "" {
		if err := validText(in.DueString, 150, false); err != nil {
			return err
		}
	}
	if in.DueDate != "" {
		if _, err := time.Parse("2006-01-02", in.DueDate); err != nil {
			return errors.New("due_date must be a valid YYYY-MM-DD date")
		}
	}
	if in.DueDatetime != "" {
		if _, err := time.Parse(time.RFC3339, in.DueDatetime); err != nil {
			return errors.New("due_datetime must be an RFC3339 timestamp")
		}
	}
	seen := map[string]bool{}
	for _, label := range in.Labels {
		if err := validText(label, 60, false); err != nil || strings.TrimSpace(label) == "" {
			return errors.New("each label must be nonempty, one line and at most 60 characters")
		}
		key := strings.ToLower(strings.TrimSpace(label))
		if seen[key] {
			return errors.New("duplicate label " + label)
		}
		seen[key] = true
	}
	for _, id := range []string{in.ProjectID, in.SectionID} {
		if id != "" {
			if err := validID(id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (in createArgs) body() map[string]any {
	out := map[string]any{"content": in.Content}
	for key, value := range map[string]string{
		"description": in.Description, "due_string": in.DueString, "due_date": in.DueDate,
		"due_datetime": in.DueDatetime, "project_id": in.ProjectID, "section_id": in.SectionID,
	} {
		if value != "" {
			out[key] = value
		}
	}
	if in.Priority > 0 {
		out["priority"] = in.Priority
	}
	if in.Labels != nil {
		out["labels"] = in.Labels
	}
	return out
}

func validID(id string) error {
	if len(id) == 0 || len(id) > 128 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\x00\r\n/") {
		return errors.New("task, project or section ID is invalid")
	}
	return nil
}

func validText(text string, limit int, multiline bool) error {
	if len([]rune(text)) > limit {
		return errors.New("task text exceeds its documented limit")
	}
	for _, r := range text {
		if r == 0 || unicode.IsControl(r) && (!multiline || r != '\n' && r != '\t') {
			return errors.New("task text contains control characters")
		}
	}
	return nil
}

func (a *app) describe(name string, raw json.RawMessage) (map[string]any, error) {
	var summary string
	switch name {
	case "todoist_create":
		var in createArgs
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := in.validate(); err != nil {
			return nil, err
		}
		summary = "Create Todoist task: " + in.Content
		if in.ProjectID != "" {
			summary += "\nProject ID: " + in.ProjectID
		}
		if in.SectionID != "" {
			summary += "\nSection ID: " + in.SectionID
		}
		summary += dueSummary(in.DueString, in.DueDate, in.DueDatetime)
	case "todoist_update":
		var in updateArgs
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if _, err := in.patch(); err != nil {
			return nil, err
		}
		data, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		summary = "Update Todoist task " + in.ID + "\nOnly these fields: " + string(data)
	case "todoist_complete":
		var in struct{ ID, Content string }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := validID(in.ID); err != nil {
			return nil, err
		}
		summary = "Complete Todoist task " + in.ID
		if in.Content != "" {
			if err := validText(in.Content, 500, false); err != nil {
				return nil, err
			}
			summary = "Complete Todoist task: " + in.Content
		}
	default:
		return nil, fmt.Errorf("unknown action %q", name)
	}
	return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": summary}, nil
}

func dueSummary(natural, date, datetime string) string {
	for _, item := range []struct{ name, value string }{{"Due", natural}, {"Due date", date}, {"Due at", datetime}} {
		if item.value != "" {
			return "\n" + item.name + ": " + item.value
		}
	}
	return ""
}

type updateArgs struct {
	ID          string    `json:"id"`
	Content     *string   `json:"content"`
	Description *string   `json:"description"`
	Priority    *int      `json:"priority"`
	DueString   *string   `json:"due_string"`
	DueDate     *string   `json:"due_date"`
	DueDatetime *string   `json:"due_datetime"`
	ClearDue    bool      `json:"clear_due"`
	Labels      *[]string `json:"labels"`
	ProjectID   *string   `json:"project_id"`
	SectionID   *string   `json:"section_id"`
}

func (in updateArgs) patch() (taskPatch, error) {
	if err := validID(in.ID); err != nil {
		return taskPatch{}, err
	}
	var patch taskPatch
	if in.Content != nil {
		if err := validText(*in.Content, 500, false); err != nil || strings.TrimSpace(*in.Content) == "" {
			return patch, errors.New("content must be nonempty and at most 500 characters")
		}
		patch.Content = in.Content
	}
	if in.Description != nil {
		if err := validText(*in.Description, 16384, true); err != nil {
			return patch, err
		}
		patch.Description = in.Description
	}
	if in.Priority != nil {
		if *in.Priority < 1 || *in.Priority > 4 {
			return patch, errors.New("priority must be 1–4")
		}
		patch.Priority = in.Priority
	}
	if in.ClearDue && (in.DueString != nil || in.DueDate != nil || in.DueDatetime != nil) {
		return patch, errors.New("clear_due cannot be combined with a new date")
	}
	if in.ClearDue {
		patch.DueDate = new("")
		patch.DueDatetime = new("")
		patch.DueString = new("")
	}
	count := 0
	if in.DueString != nil {
		count++
	}
	if in.DueDate != nil {
		count++
	}
	if in.DueDatetime != nil {
		count++
	}
	if count > 1 {
		return patch, errors.New("supply only one due field")
	}
	if in.DueString != nil {
		if err := validText(*in.DueString, 150, false); err != nil {
			return patch, err
		}
		patch.DueString = in.DueString
	}
	if in.DueDate != nil {
		if _, err := time.Parse("2006-01-02", *in.DueDate); err != nil {
			return patch, errors.New("due_date must be YYYY-MM-DD")
		}
		patch.DueDate = in.DueDate
	}
	if in.DueDatetime != nil {
		if _, err := time.Parse(time.RFC3339, *in.DueDatetime); err != nil {
			return patch, errors.New("due_datetime must be RFC3339")
		}
		patch.DueDatetime = in.DueDatetime
	}
	if in.Labels != nil {
		if len(*in.Labels) > 20 {
			return patch, errors.New("too many labels")
		}
		for _, label := range *in.Labels {
			if err := validText(label, 60, false); err != nil || strings.TrimSpace(label) == "" {
				return patch, errors.New("invalid label")
			}
		}
		patch.Labels = in.Labels
	}
	if in.ProjectID != nil {
		if err := validID(*in.ProjectID); err != nil {
			return patch, err
		}
		patch.ProjectID = in.ProjectID
	}
	if in.SectionID != nil {
		if err := validID(*in.SectionID); err != nil {
			return patch, err
		}
		patch.SectionID = in.SectionID
	}
	if patch.empty() {
		return patch, errors.New("supply at least one field to change; omitted fields remain unchanged")
	}
	return patch, nil
}

func (a *app) doAction(ctx context.Context, requestID, name string, raw json.RawMessage) (map[string]any, error) {
	var key string
	var fn func() (string, error)
	switch name {
	case "todoist_create":
		var in createArgs
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := in.validate(); err != nil {
			return nil, err
		}
		keyData, _ := json.Marshal(map[string]any{"action": name, "args": in.body()})
		key = string(keyData)
		fn = func() (string, error) {
			item, err := a.client.create(ctx, in.body())
			if err != nil {
				return "", err
			}
			if item.ID == "" {
				return "", errors.New("Todoist returned no task ID")
			}
			return fmt.Sprintf("Created Todoist task %s: %s", item.ID, item.Content), nil
		}
	case "todoist_update":
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
			item, err := a.client.update(ctx, in.ID, patch)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Updated Todoist task %s: %s", item.ID, item.Content), nil
		}
	case "todoist_complete":
		var in struct{ ID string }
		if err := decode(raw, &in); err != nil {
			return nil, err
		}
		if err := validID(in.ID); err != nil {
			return nil, err
		}
		keyData, _ := json.Marshal(map[string]any{"action": name, "id": in.ID})
		key = string(keyData)
		fn = func() (string, error) {
			if err := a.client.complete(ctx, in.ID); err != nil {
				return "", err
			}
			return "Closed Todoist task " + in.ID, nil
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
