package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type pageState struct {
	Mode          string   `json:"m,omitempty"`
	WorkspaceGID  string   `json:"w,omitempty"`
	WorkspaceName string   `json:"n,omitempty"`
	TaskGID       string   `json:"t,omitempty"`
	Offset        string   `json:"o,omitempty"`
	History       []string `json:"h,omitempty"`
	DraftID       string   `json:"d,omitempty"`
	Name          string   `json:"a,omitempty"`
	Notes         string   `json:"b,omitempty"`
	DueOn         string   `json:"e,omitempty"`
	ProjectGID    string   `json:"p,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || len(st.WorkspaceGID) > 40 || len(st.TaskGID) > 40 || len(st.WorkspaceName) > 1000 || len(st.DraftID) > 128 || len(st.Name) > 600 || len(st.Notes) > 11000 || len(st.ProjectGID) > 40 || len(st.Offset) > 2048 || len(st.History) > 10 {
		return pageState{}, errors.New("invalid page state")
	}
	return st, nil
}

func (a *app) render(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
	st, err := readState(r)
	if err != nil {
		return nil, err
	}
	return a.page(ctx, r, st)
}

func (a *app) action(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
	st, err := readState(r)
	if err != nil {
		return nil, err
	}
	switch r.Action {
	case "workspace":
		if err := validGID(r.Values["gid"]); err != nil {
			return nil, err
		}
		if r.Values["name"] == "" || len(r.Values["name"]) > 500 {
			return nil, errors.New("invalid workspace name")
		}
		st = pageState{Mode: "tasks", WorkspaceGID: r.Values["gid"], WorkspaceName: r.Values["name"]}
	case "workspaces":
		st = pageState{}
	case "more":
		if err := validOffset(r.Values["offset"]); err != nil || r.Values["offset"] == "" {
			return nil, errors.New("invalid next offset")
		}
		st.History = append(st.History, st.Offset)
		if len(st.History) > 10 {
			st.History = st.History[len(st.History)-10:]
		}
		st.Offset = r.Values["offset"]
	case "previous":
		if len(st.History) < 2 {
			st.Offset = ""
		} else {
			st.Offset = st.History[len(st.History)-2]
		}
		st.History = st.History[:max(0, len(st.History)-1)]
	case "detail":
		if err := validGID(r.Values["gid"]); err != nil {
			return nil, err
		}
		st.Mode, st.TaskGID = "detail", r.Values["gid"]
	case "create":
		if st.Mode != "review-create" {
			st = pageState{Mode: "create", WorkspaceGID: st.WorkspaceGID, WorkspaceName: st.WorkspaceName, DraftID: randomID()}
		} else {
			st.Mode = "create"
		}
	case "back":
		st.Mode, st.TaskGID, st.DraftID = "tasks", "", ""
		st.Name, st.Notes, st.ProjectGID, st.DueOn = "", "", "", ""
	case "review-create":
		in := createArgs{WorkspaceGID: st.WorkspaceGID, ProjectGID: r.Values["project_gid"], Name: r.Values["name"], Notes: r.Values["notes"], DueOn: r.Values["due_on"]}
		if err := in.validate(); err != nil {
			return nil, err
		}
		st.ProjectGID, st.Name, st.Notes, st.DueOn = in.ProjectGID, in.Name, in.Notes, in.DueOn
		st.Mode = "review-create"
	case "confirm-create":
		in := createArgs{WorkspaceGID: st.WorkspaceGID, ProjectGID: st.ProjectGID, Name: st.Name, Notes: st.Notes, DueOn: st.DueOn}
		raw, _ := json.Marshal(in)
		out, err := a.doAction(ctx, "ui-create:"+st.DraftID, "asana_create_task", raw)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	case "complete":
		if st.TaskGID == "" || st.DraftID == "" {
			return nil, errors.New("task and draft are required")
		}
		raw, _ := json.Marshal(completeArgs{TaskGID: st.TaskGID})
		out, err := a.doAction(ctx, "ui-complete:"+st.DraftID, "asana_complete_task", raw)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	default:
		return nil, fmt.Errorf("unknown page action %q", r.Action)
	}
	v, err := a.page(ctx, r, st)
	return &wire.ActionResult{View: v}, err
}

func (a *app) page(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if a.client.token == "" {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingConfig}}}}}, nil
	}
	if st.Mode == "" {
		page, err := a.client.workspaces(ctx, st.Offset)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.workspaces}
		for _, item := range page.Data {
			if item.GID == "" || item.Name == "" {
				continue
			}
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "workspace-" + item.GID, Title: clipText(item.Name, 1800), Subtitle: item.GID, Action: &wire.Action{ID: "workspace", Values: map[string]string{"gid": item.GID, "name": item.Name}}})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noWorkspaces})
		}
		v := &wire.View{Title: t.tasks, State: ext.EncodeState(st), Sections: []wire.Section{section}}
		if next := nextOffset(page.NextPage); next != "" {
			v.Sections[0].More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"offset": next}}}
		}
		return v, nil
	}
	if err := validGID(st.WorkspaceGID); err != nil {
		return nil, err
	}
	if st.Mode == "create" || st.Mode == "review-create" {
		return a.createView(ctx, r, st)
	}
	if st.Mode == "tasks" {
		page, err := a.client.tasks(ctx, st.WorkspaceGID, st.Offset)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.tasks}
		for _, item := range page.Data {
			if item.GID == "" || item.Name == "" {
				continue
			}
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: pageKey(item.GID), Title: clipText(item.Name, 1800), Subtitle: clipText(taskSummary(item), 1800), Action: &wire.Action{ID: "detail", Values: map[string]string{"gid": item.GID}}})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noTasks})
		}
		if next := nextOffset(page.NextPage); next != "" {
			section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"offset": next}}}
		}
		v := &wire.View{Title: clipText(st.WorkspaceName, 1800), State: ext.EncodeState(st), Sections: []wire.Section{section, {Title: t.tasks, Items: []wire.Component{
			{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "create", Push: &wire.Push{Title: t.create, State: ext.EncodeState(pageState{Mode: "create", WorkspaceGID: st.WorkspaceGID, WorkspaceName: st.WorkspaceName, DraftID: randomID()})}}},
			{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "workspaces"}},
		}}}}
		return v, nil
	}
	if st.Mode != "detail" {
		return nil, errors.New("invalid task view")
	}
	item, err := a.client.task(ctx, st.TaskGID)
	a.record(err)
	if err != nil {
		return nil, err
	}
	st.DraftID = randomID()
	return detailView(r, st, item), nil
}

func (a *app) createView(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if st.Mode == "review-create" {
		in := createArgs{WorkspaceGID: st.WorkspaceGID, ProjectGID: st.ProjectGID, Name: st.Name, Notes: st.Notes, DueOn: st.DueOn}
		if err := in.validate(); err != nil {
			return nil, err
		}
		items := []wire.Component{{Type: wire.ComponentText, Text: clipText(st.Name, 1800)}}
		if st.Notes != "" {
			items = append(items, textComponents(st.Notes)...)
		}
		items = append(items, wire.Component{Type: wire.ComponentText, Text: t.due + ": " + st.DueOn}, wire.Component{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "confirm-create", Confirm: t.create + "\n" + st.Name}}, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "create"}})
		return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.create, Items: items}}}, nil
	}
	projects, err := a.client.projects(ctx, st.WorkspaceGID, "")
	if err != nil {
		return nil, err
	}
	choices := []wire.Choice{{Value: "", Title: t.none}}
	for _, project := range projects.Data {
		if project.GID != "" && project.Name != "" && len(choices) < 50 {
			choices = append(choices, wire.Choice{Value: project.GID, Title: clipText(project.Name, 100)})
		}
	}
	items := []wire.Component{
		{Type: wire.ComponentField, Kind: "text", Name: "name", Label: t.name, Value: st.Name},
		{Type: wire.ComponentField, Kind: "text", Name: "notes", Label: t.notes, Value: st.Notes},
		{Type: wire.ComponentField, Kind: "choice", Name: "project_gid", Label: t.project, Value: selectedProject(st.ProjectGID, choices), Choices: choices},
		{Type: wire.ComponentField, Kind: "text", Name: "due_on", Label: t.due, Value: st.DueOn},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-create", Submit: true}},
		{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	}
	return &wire.View{Title: t.create, State: ext.EncodeState(st), Sections: []wire.Section{{Items: items}}}, nil
}

func detailView(r *ext.UIRequest, st pageState, item task) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{{Type: wire.ComponentText, Text: clipText(taskSummary(item), 1800)}}
	if link := safeTaskLink(item.PermalinkURL); link != "" {
		items = append(items, wire.Component{Type: wire.ComponentLink, Title: t.open, URL: link})
	}
	if !item.Completed {
		items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.complete, Action: &wire.Action{ID: "complete", Confirm: t.complete + "\n" + item.Name}})
	}
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}})
	sections := []wire.Section{{Title: t.tasks, Items: items}}
	if item.Notes != "" {
		sections = append(sections, wire.Section{Title: t.description, Items: textComponents(clipText(item.Notes, 60000))})
	} else {
		sections = append(sections, wire.Section{Title: t.description, Items: []wire.Component{{Type: wire.ComponentText, Text: t.noNotes, Style: wire.TextStyleSecondary}}})
	}
	return &wire.View{Title: clipText(item.Name, 1800), State: ext.EncodeState(st), Sections: sections}
}

func taskSummary(item task) string {
	var parts []string
	if item.DueOn != "" || item.DueAt != "" {
		parts = append(parts, item.DueOn+item.DueAt)
	}
	if item.Completed {
		parts = append(parts, "completed")
	}
	for _, project := range item.Projects {
		parts = append(parts, project.Name)
	}
	return strings.Join(parts, " · ")
}

func selectedProject(value string, choices []wire.Choice) string {
	for _, choice := range choices {
		if choice.Value == value {
			return value
		}
	}
	return ""
}

func nextOffset(next *struct {
	Offset string `json:"offset"`
	URI    string `json:"uri"`
}) string {
	if next == nil || validOffset(next.Offset) != nil {
		return ""
	}
	return next.Offset
}

func textComponents(text string) []wire.Component {
	var out []wire.Component
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > 0 {
			n := min(len(r), 1800)
			out = append(out, wire.Component{Type: wire.ComponentText, Text: string(r[:n])})
			r = r[n:]
		}
	}
	if len(out) == 0 {
		out = append(out, wire.Component{Type: wire.ComponentText, Text: " ", Style: wire.TextStyleSecondary})
	}
	return out
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
