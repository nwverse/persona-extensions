package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type pageState struct {
	Mode        string `json:"m,omitempty"`
	Query       string `json:"q,omitempty"`
	Page        int    `json:"o,omitempty"`
	TaskID      int64  `json:"i,omitempty"`
	ProjectID   int64  `json:"p,omitempty"`
	DraftID     string `json:"d,omitempty"`
	Title       string `json:"t,omitempty"`
	Description string `json:"b,omitempty"`
	DueDate     string `json:"e,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.Page < 0 || st.TaskID < 0 || st.ProjectID < 0 || len(st.Query) > 250 || len(st.DraftID) > 128 || len(st.Title) > 600 || len(st.Description) > 11000 || len(st.DueDate) > 64 {
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
	case "search":
		st.Query, st.Page = strings.TrimSpace(clipText(r.Values["q"], 200)), 1
	case "more":
		st.Page, err = strconv.Atoi(r.Values["page"])
		if err != nil || st.Page < 2 {
			return nil, errors.New("invalid next page")
		}
	case "detail":
		st.TaskID, err = strconv.ParseInt(r.Values["id"], 10, 64)
		if err != nil || st.TaskID <= 0 {
			return nil, errors.New("invalid task ID")
		}
		st.Mode = "detail"
	case "back":
		st.Mode, st.TaskID, st.DraftID = "", 0, ""
	case "projects":
		st = pageState{Mode: "projects"}
	case "create":
		if st.Mode != "review-create" {
			st.ProjectID, err = strconv.ParseInt(r.Values["project_id"], 10, 64)
			if err != nil || st.ProjectID <= 0 {
				return nil, errors.New("invalid project ID")
			}
			st = pageState{Mode: "create", ProjectID: st.ProjectID, DraftID: randomID()}
		} else {
			st.Mode = "create"
		}
	case "review-create":
		in := createArgs{ProjectID: st.ProjectID, Title: strings.TrimSpace(r.Values["title"]), Description: strings.TrimSpace(r.Values["description"]), DueDate: strings.TrimSpace(r.Values["due_date"])}
		if err := in.validate(); err != nil || len(in.Description) > 5000 {
			return nil, errors.New("the Web task must be valid and its description at most 5000 UTF-8 bytes")
		}
		st.Title, st.Description, st.DueDate, st.Mode = in.Title, in.Description, in.DueDate, "review-create"
	case "confirm-create":
		in := createArgs{ProjectID: st.ProjectID, Title: st.Title, Description: st.Description, DueDate: st.DueDate}
		if err := in.validate(); err != nil || st.Mode != "review-create" || st.DraftID == "" || len(st.Description) > 5000 {
			return nil, errors.New("a valid reviewed task draft is required")
		}
		raw, _ := json.Marshal(in)
		out, err := a.doAction(ctx, "ui-create:"+st.DraftID, "vikunja_create_task", raw)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	case "complete":
		if st.Mode != "detail" || st.TaskID <= 0 || st.DraftID == "" {
			return nil, errors.New("a task completion draft is required")
		}
		raw, _ := json.Marshal(map[string]int64{"task_id": st.TaskID})
		out, err := a.doAction(ctx, "ui-complete:"+st.DraftID, "vikunja_complete_task", raw)
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
	if a.client.base == "" || a.client.token == "" {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingConfig}}}}}, nil
	}
	if st.Mode == "create" || st.Mode == "review-create" {
		return createView(r, st), nil
	}
	if st.Mode == "projects" {
		items, err := a.client.projects(ctx, 1)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.projects}
		for _, item := range items {
			if item.ID <= 0 || item.IsArchived || item.DeletedAt != "" {
				continue
			}
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: fmt.Sprintf("project-%d", item.ID), Title: clipText(item.Title, 1800), Subtitle: item.Identifier, Action: &wire.Action{ID: "create", Values: map[string]string{"project_id": fmt.Sprint(item.ID)}}})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noProjects})
		}
		return &wire.View{Title: t.projects, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode == "detail" {
		item, err := a.client.task(ctx, st.TaskID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		st.DraftID = randomID()
		return detailView(r, st, item), nil
	}
	if st.Mode != "" {
		return nil, errors.New("invalid task view")
	}
	me, err := a.client.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if st.Page == 0 {
		st.Page = 1
	}
	page, err := a.client.tasks(ctx, me.Username, st.Query, st.Page)
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.tasks, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}}
	section := wire.Section{Title: t.tasks}
	for _, item := range page.Items {
		if item.ID <= 0 || item.Title == "" {
			continue
		}
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: fmt.Sprintf("task-%d", item.ID), Title: clipText(item.Title, 1800), Subtitle: clipText(item.Identifier+" · "+item.DueDate, 1800), Action: &wire.Action{ID: "detail", Values: map[string]string{"id": fmt.Sprint(item.ID)}}})
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noTasks})
	}
	if page.HasMore {
		section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"page": fmt.Sprint(page.NextPage)}}}
	}
	v.Sections = []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "projects", Push: &wire.Push{Title: t.projects, State: ext.EncodeState(pageState{Mode: "projects"})}}}}}}
	return v, nil
}

func createView(r *ext.UIRequest, st pageState) *wire.View {
	t := textsFor(r.Locale)
	if st.Mode == "review-create" {
		items := []wire.Component{{Type: wire.ComponentText, Text: clipText(st.Title, 1800)}}
		if st.DueDate != "" {
			items = append(items, wire.Component{Type: wire.ComponentText, Text: st.DueDate})
		}
		if st.Description != "" {
			items = append(items, textComponents(st.Description)...)
		}
		items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "confirm-create", Confirm: t.create + "\n" + st.Title}}, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "create"}})
		return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.create, Items: items}}}
	}
	return &wire.View{Title: t.create, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.create, Items: []wire.Component{
		{Type: wire.ComponentField, Kind: "text", Name: "title", Label: t.title, Value: st.Title},
		{Type: wire.ComponentField, Kind: "text", Name: "description", Label: t.description, Value: st.Description},
		{Type: wire.ComponentField, Kind: "text", Name: "due_date", Label: t.due, Value: st.DueDate},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-create", Submit: true}},
		{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "projects"}},
	}}}}
}

func detailView(r *ext.UIRequest, st pageState, item task) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{{Type: wire.ComponentText, Text: clipText(item.Identifier+" · "+item.DueDate, 1800)}}
	if !item.Done {
		items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.complete, Action: &wire.Action{ID: "complete", Confirm: t.complete + "\n" + item.Title}})
	}
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}})
	v := &wire.View{Title: clipText(item.Title, 1800), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.tasks, Items: items}}}
	if item.Description != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.description, Items: textComponents(clipText(item.Description, 40000))})
	}
	section := wire.Section{Title: t.comments}
	for _, c := range item.Comments {
		name := c.Author.Name
		if name == "" {
			name = c.Author.Username
		}
		if name == "" {
			name = t.user
		}
		if c.Comment != "" {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: clipText(name+": "+c.Comment, 1800)})
		}
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: t.noComments, Style: wire.TextStyleSecondary})
	}
	v.Sections = append(v.Sections, section)
	return v
}

func textComponents(text string) []wire.Component {
	var out []wire.Component
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > 0 {
			n := min(len(r), 1800)
			out = append(out, wire.Component{Type: wire.ComponentText, Text: string(r[:n])})
			r = r[n:]
			if len(out) >= 220 {
				out[len(out)-1].Text = clipText(out[len(out)-1].Text, 1700) + " […]"
				return out
			}
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
