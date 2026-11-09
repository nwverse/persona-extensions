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
	Mode        string   `json:"m,omitempty"`
	ProjectID   string   `json:"p,omitempty"`
	ProjectName string   `json:"n,omitempty"`
	Identifier  string   `json:"r,omitempty"`
	WorkItemID  string   `json:"i,omitempty"`
	Sequence    int64    `json:"s,omitempty"`
	Cursor      string   `json:"c,omitempty"`
	History     []string `json:"h,omitempty"`
	DraftID     string   `json:"d,omitempty"`
	Name        string   `json:"t,omitempty"`
	Description string   `json:"b,omitempty"`
	Priority    string   `json:"y,omitempty"`
	StateID     string   `json:"g,omitempty"`
	TargetDate  string   `json:"e,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || len(st.ProjectID) > 128 || len(st.ProjectName) > 1000 || len(st.WorkItemID) > 128 || len(st.DraftID) > 128 || len(st.Name) > 1024 || len(st.Description) > 12000 || len(st.History) > 10 {
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
	case "project":
		if err := validID(r.Values["id"]); err != nil {
			return nil, err
		}
		if r.Values["name"] == "" || r.Values["identifier"] == "" || len(r.Values["name"]) > 255 || len(r.Values["identifier"]) > 32 {
			return nil, errors.New("invalid project metadata")
		}
		st = pageState{Mode: "items", ProjectID: r.Values["id"], ProjectName: r.Values["name"], Identifier: r.Values["identifier"]}
	case "projects":
		st = pageState{}
	case "more":
		if err := validCursor(st.Cursor); err != nil || st.Cursor == "" {
			return nil, errors.New("no valid next cursor")
		}
		st.History = append(st.History, st.Cursor)
		if len(st.History) > 10 {
			st.History = st.History[len(st.History)-10:]
		}
		st.Cursor = r.Values["cursor"]
		if err := validCursor(st.Cursor); err != nil || st.Cursor == "" {
			return nil, errors.New("invalid next cursor")
		}
	case "previous":
		if len(st.History) < 2 {
			st.Cursor = ""
		} else {
			st.Cursor = st.History[len(st.History)-2]
		}
		st.History = st.History[:max(0, len(st.History)-1)]
	case "detail":
		if err := validID(r.Values["id"]); err != nil {
			return nil, err
		}
		st.Mode, st.WorkItemID = "detail", r.Values["id"]
	case "create":
		if st.Mode != "review-create" {
			st = pageState{Mode: "create", ProjectID: st.ProjectID, ProjectName: st.ProjectName, Identifier: st.Identifier, DraftID: randomID()}
		} else {
			st.Mode = "create"
		}
	case "edit":
		st.Mode = "edit"
		if st.DraftID == "" {
			st.DraftID = randomID()
		}
	case "back":
		st.Mode, st.WorkItemID, st.DraftID = "items", "", ""
		st.Name, st.Description, st.Priority, st.StateID, st.TargetDate = "", "", "", "", ""
	case "review-create":
		in := createArgs{ProjectID: st.ProjectID, Name: r.Values["name"], Description: r.Values["description"], Priority: r.Values["priority"], StateID: r.Values["state_id"], TargetDate: r.Values["target_date"]}
		if err := in.validate(); err != nil {
			return nil, err
		}
		st.Name, st.Description, st.Priority, st.StateID, st.TargetDate = in.Name, in.Description, in.Priority, in.StateID, in.TargetDate
		st.Mode = "review-create"
	case "review-edit":
		in := updateArgs{ProjectID: st.ProjectID, WorkItemID: st.WorkItemID, Priority: r.Values["priority"], StateID: r.Values["state_id"]}
		if err := in.validate(); err != nil {
			return nil, err
		}
		st.Priority, st.StateID, st.Mode = in.Priority, in.StateID, "review-edit"
	case "confirm-create":
		in := createArgs{ProjectID: st.ProjectID, Name: st.Name, Description: st.Description, Priority: st.Priority, StateID: st.StateID, TargetDate: st.TargetDate}
		raw, _ := json.Marshal(in)
		out, err := a.doAction(ctx, "ui-create:"+st.DraftID, "plane_create_work_item", raw)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	case "confirm-edit":
		in := updateArgs{ProjectID: st.ProjectID, WorkItemID: st.WorkItemID, Priority: st.Priority, StateID: st.StateID}
		raw, _ := json.Marshal(in)
		out, err := a.doAction(ctx, "ui-edit:"+st.DraftID, "plane_update_work_item", raw)
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
	if a.client.base == "" || a.client.token == "" || a.workspace == "" {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingConfig}}}}}, nil
	}
	switch st.Mode {
	case "items", "create", "review-create", "detail", "edit", "review-edit":
		if err := validID(st.ProjectID); err != nil {
			return nil, err
		}
	default:
		page, err := a.client.projects(ctx, a.workspace, st.Cursor)
		a.record(err)
		if err != nil {
			return nil, err
		}
		v := &wire.View{Title: t.projects, State: ext.EncodeState(st)}
		section := wire.Section{Title: t.projects}
		for _, project := range page.Results {
			if project.ID == "" || project.Name == "" {
				continue
			}
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "project-" + project.ID, Title: clipText(project.Name, 1800), Subtitle: project.Identifier, Action: &wire.Action{ID: "project", Values: map[string]string{"id": project.ID, "name": project.Name, "identifier": project.Identifier}}})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noProjects})
		}
		if page.NextPageResults && page.NextCursor != "" {
			section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"cursor": page.NextCursor}}}
		}
		v.Sections = []wire.Section{section}
		return v, nil
	}
	if st.Mode == "create" || st.Mode == "review-create" {
		return a.createView(ctx, r, st)
	}
	if st.Mode == "items" {
		page, err := a.client.workItems(ctx, a.workspace, st.ProjectID, st.Cursor)
		a.record(err)
		if err != nil {
			return nil, err
		}
		v := &wire.View{Title: clipText(st.ProjectName, 1800), State: ext.EncodeState(st)}
		section := wire.Section{Title: fmt.Sprintf(t.items, len(page.Results))}
		for _, item := range page.Results {
			if item.ID == "" || item.Name == "" {
				continue
			}
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: itemKey(item.SequenceID), Title: clipText(item.Name, 1800), Subtitle: fmt.Sprintf("#%d · %s · %s", item.SequenceID, clipText(item.State.Name, 100), item.Priority), Action: &wire.Action{ID: "detail", Values: map[string]string{"id": item.ID}}})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noItems})
		}
		if page.NextPageResults && page.NextCursor != "" {
			section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"cursor": page.NextCursor}}}
		}
		v.Sections = []wire.Section{section, {Title: t.work, Items: []wire.Component{
			{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "create", Push: &wire.Push{Title: t.create, State: ext.EncodeState(pageState{Mode: "create", ProjectID: st.ProjectID, ProjectName: st.ProjectName, Identifier: st.Identifier, DraftID: randomID()})}}},
			{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "projects"}},
		}}}
		return v, nil
	}
	item, err := a.client.workItem(ctx, a.workspace, st.ProjectID, st.WorkItemID)
	a.record(err)
	if err != nil {
		return nil, err
	}
	if st.Mode == "edit" {
		st.Priority, st.StateID = item.Priority, item.State.ID
	}
	if st.Mode == "edit" || st.Mode == "review-edit" {
		states, err := a.client.states(ctx, a.workspace, st.ProjectID)
		if err != nil {
			return nil, err
		}
		return editView(r, st, states), nil
	}
	st.Sequence, st.Identifier = item.SequenceID, item.ProjectIdentifier
	return detailView(r, st, item, a.client.itemURL(item)), nil
}

func (a *app) createView(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if st.Mode == "review-create" {
		items := []wire.Component{{Type: wire.ComponentText, Text: clipText(st.Name, 1800)}}
		if st.Description != "" {
			items = append(items, textComponents(st.Description)...)
		}
		items = append(items, wire.Component{Type: wire.ComponentText, Text: t.priority + ": " + st.Priority + " · " + t.due + ": " + st.TargetDate})
		items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "confirm-create", Confirm: t.create + "\n" + st.Name}}, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "create"}})
		return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.create, Items: items}}}, nil
	}
	states, err := a.client.states(ctx, a.workspace, st.ProjectID)
	if err != nil {
		return nil, err
	}
	items := []wire.Component{
		{Type: wire.ComponentField, Kind: "text", Name: "name", Label: t.name, Value: st.Name},
		{Type: wire.ComponentField, Kind: "text", Name: "description", Label: t.body, Value: st.Description},
		{Type: wire.ComponentField, Kind: "choice", Name: "priority", Label: t.priority, Value: priorityValue(st.Priority), Choices: priorityChoices(t)},
		{Type: wire.ComponentField, Kind: "text", Name: "target_date", Label: t.due + " (YYYY-MM-DD)", Value: st.TargetDate},
	}
	if state := stateChoices(states); len(state) > 0 {
		items = append(items, wire.Component{Type: wire.ComponentField, Kind: "choice", Name: "state_id", Label: t.state, Value: stateValue(st.StateID, states), Choices: state})
	}
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-create", Submit: true}}, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}})
	return &wire.View{Title: t.create, State: ext.EncodeState(st), Sections: []wire.Section{{Items: items}}}, nil
}

func editView(r *ext.UIRequest, st pageState, states []workflowState) *wire.View {
	t := textsFor(r.Locale)
	if st.Mode == "review-edit" {
		stateName := st.StateID
		for _, state := range states {
			if state.ID == st.StateID {
				stateName = state.Name
			}
		}
		return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Items: []wire.Component{
			{Type: wire.ComponentText, Text: t.state + ": " + clipText(stateName, 1800)}, {Type: wire.ComponentText, Text: t.priority + ": " + st.Priority},
			{Type: wire.ComponentButton, Title: t.save, Action: &wire.Action{ID: "confirm-edit", Confirm: t.save + "\n" + t.state + ": " + stateName + "\n" + t.priority + ": " + st.Priority}},
			{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "edit"}},
		}}}}
	}
	choices := stateChoices(states)
	if len(choices) == 0 {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.state}}}}}
	}
	return &wire.View{Title: t.edit, State: ext.EncodeState(st), Sections: []wire.Section{{Items: []wire.Component{
		{Type: wire.ComponentField, Kind: "choice", Name: "state_id", Label: t.state, Value: stateValue(st.StateID, states), Choices: choices},
		{Type: wire.ComponentField, Kind: "choice", Name: "priority", Label: t.priority, Value: priorityValue(st.Priority), Choices: priorityChoices(t)},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-edit", Submit: true}},
		{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	}}}}
}

func detailView(r *ext.UIRequest, st pageState, item workItem, link string) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{
		{Type: wire.ComponentText, Text: t.state + ": " + clipText(item.State.Name, 1800) + " · " + t.priority + ": " + item.Priority},
		{Type: wire.ComponentText, Text: t.due + ": " + clipText(item.TargetDate, 100)},
	}
	if link != "" {
		items = append(items, wire.Component{Type: wire.ComponentLink, Title: t.open, URL: link})
	}
	items = append(items,
		wire.Component{Type: wire.ComponentButton, Title: t.edit, Action: &wire.Action{ID: "edit", Push: &wire.Push{Title: t.edit, State: ext.EncodeState(pageState{Mode: "edit", ProjectID: st.ProjectID, ProjectName: st.ProjectName, Identifier: st.Identifier, WorkItemID: st.WorkItemID, Priority: item.Priority, StateID: item.State.ID, DraftID: randomID()})}}},
		wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	)
	sections := []wire.Section{{Title: fmt.Sprintf("#%d", item.SequenceID), Items: items}}
	if text := itemDescription(item); text != "" {
		sections = append(sections, wire.Section{Title: t.description, Items: textComponents(clipText(text, 60000))})
	} else {
		sections = append(sections, wire.Section{Title: t.description, Items: []wire.Component{{Type: wire.ComponentText, Text: t.noDescription, Style: wire.TextStyleSecondary}}})
	}
	return &wire.View{Title: clipText(item.Name, 1800), State: ext.EncodeState(st), Sections: sections}
}

func stateChoices(states []workflowState) []wire.Choice {
	out := []wire.Choice{}
	for _, state := range states {
		if state.ID != "" && state.Name != "" && len(out) < 50 {
			out = append(out, wire.Choice{Value: state.ID, Title: clipText(state.Name, 100)})
		}
	}
	return out
}

func stateValue(selected string, states []workflowState) string {
	for _, state := range states {
		if state.ID == selected {
			return selected
		}
	}
	for _, state := range states {
		if state.ID != "" && state.Name != "" {
			return state.ID
		}
	}
	return ""
}

func priorityValue(value string) string {
	if validPriority(value) {
		return value
	}
	return "none"
}

func priorityChoices(t *texts) []wire.Choice {
	return []wire.Choice{{Value: "urgent", Title: t.urgent}, {Value: "high", Title: t.high}, {Value: "medium", Title: t.medium}, {Value: "low", Title: t.low}, {Value: "none", Title: t.none}}
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
