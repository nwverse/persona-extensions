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
	WorkID      int64  `json:"i,omitempty"`
	Offset      int    `json:"o,omitempty"`
	ProjectHref string `json:"p,omitempty"`
	ProjectName string `json:"n,omitempty"`
	TypeHref    string `json:"t,omitempty"`
	TypeName    string `json:"y,omitempty"`
	DraftID     string `json:"d,omitempty"`
	Subject     string `json:"a,omitempty"`
	Description string `json:"b,omitempty"`
	StartDate   string `json:"s,omitempty"`
	DueDate     string `json:"e,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.WorkID < 0 || st.Offset < 0 || len(st.ProjectHref) > 100 || len(st.ProjectName) > 1000 || len(st.TypeHref) > 100 || len(st.TypeName) > 500 || len(st.DraftID) > 128 || len(st.Subject) > 600 || len(st.Description) > 31000 || len(st.StartDate) > 20 || len(st.DueDate) > 20 {
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
	case "more":
		st.Offset++
	case "detail":
		id, err := strconv.ParseInt(r.Values["id"], 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("invalid work package ID")
		}
		st.Mode, st.WorkID = "detail", id
	case "projects":
		st = pageState{Mode: "projects"}
	case "create":
		if st.Mode != "review-create" {
			st = pageState{Mode: "create", ProjectHref: st.ProjectHref, ProjectName: st.ProjectName, TypeHref: st.TypeHref, TypeName: st.TypeName, DraftID: randomID()}
		} else {
			st.Mode = "create"
		}
	case "back":
		st.Mode, st.WorkID, st.DraftID, st.Subject, st.Description = "", 0, "", "", ""
	case "review-create":
		in := createArgs{ProjectHref: st.ProjectHref, TypeHref: st.TypeHref, Subject: r.Values["subject"], Description: r.Values["description"], StartDate: r.Values["start_date"], DueDate: r.Values["due_date"]}
		if err := in.validate(); err != nil {
			return nil, err
		}
		st.Subject, st.Description, st.StartDate, st.DueDate, st.Mode = in.Subject, in.Description, in.StartDate, in.DueDate, "review-create"
	case "confirm-create":
		raw, _ := json.Marshal(createArgs{ProjectHref: st.ProjectHref, TypeHref: st.TypeHref, Subject: st.Subject, Description: st.Description, StartDate: st.StartDate, DueDate: st.DueDate})
		out, err := a.doAction(ctx, "ui-create:"+st.DraftID, "openproject_create_work_package", raw)
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
		return a.createView(ctx, r, st)
	}
	if st.Mode == "projects" {
		page, err := a.client.projects(ctx, 1)
		a.record(err)
		if err != nil {
			return nil, err
		}
		types, err := a.client.types(ctx, 1)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.projects}
		for _, item := range page.Embedded.Elements {
			if !item.Active || item.Name == "" {
				continue
			}
			typeChoice := defaultType(types.Embedded.Elements)
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "project-" + fmt.Sprint(item.ID), Title: clipText(item.Name, 1800), Subtitle: clipText(typeChoice.Name, 500), Action: &wire.Action{ID: "create", Values: map[string]string{"project_href": item.Links.Self.Href, "project_name": item.Name, "type_href": typeChoice.Links.Self.Href, "type_name": typeChoice.Name}}})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noProjects})
		}
		return &wire.View{Title: t.projects, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode == "detail" {
		item, err := a.client.workPackage(ctx, st.WorkID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		return detailView(r, st, item), nil
	}
	if st.Mode != "" {
		return nil, errors.New("invalid work package view")
	}
	me, err := a.client.currentUser(ctx)
	if err != nil {
		a.record(err)
		return nil, err
	}
	st.Offset = max(1, st.Offset)
	page, err := a.client.workPackages(ctx, me.ID, st.Offset)
	a.record(err)
	if err != nil {
		return nil, err
	}
	section := wire.Section{Title: t.work}
	for _, item := range page.Embedded.Elements {
		if item.ID <= 0 || item.Subject == "" {
			continue
		}
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "work-" + fmt.Sprint(item.ID), Title: clipText(item.Subject, 1800), Subtitle: clipText(item.Links.Project.Title+" · "+item.Links.Status.Title+" · "+item.DueDate, 1800), Action: &wire.Action{ID: "detail", Values: map[string]string{"id": fmt.Sprint(item.ID)}}})
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noWork})
	}
	if page.Links.NextByOffset.Href != "" {
		section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more"}}
	}
	v := &wire.View{Title: t.work, State: ext.EncodeState(st), Sections: []wire.Section{section, {Title: t.work, Items: []wire.Component{
		{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "projects", Push: &wire.Push{Title: t.projects, State: ext.EncodeState(pageState{Mode: "projects"})}}},
	}}}}
	return v, nil
}

func (a *app) createView(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if st.Mode == "review-create" {
		in := createArgs{ProjectHref: st.ProjectHref, TypeHref: st.TypeHref, Subject: st.Subject, Description: st.Description, StartDate: st.StartDate, DueDate: st.DueDate}
		if err := in.validate(); err != nil {
			return nil, err
		}
		items := []wire.Component{{Type: wire.ComponentText, Text: clipText(st.Subject, 1800)}}
		if st.Description != "" {
			items = append(items, textComponents(st.Description)...)
		}
		items = append(items, wire.Component{Type: wire.ComponentText, Text: t.project + ": " + st.ProjectName + " · " + t.typeLabel + ": " + st.TypeName}, wire.Component{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "confirm-create", Confirm: t.create + "\n" + st.Subject}}, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "create"}})
		return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.create, Items: items}}}, nil
	}
	return &wire.View{Title: t.create, State: ext.EncodeState(st), Sections: []wire.Section{{Title: st.ProjectName + " · " + st.TypeName, Items: []wire.Component{
		{Type: wire.ComponentField, Kind: "text", Name: "subject", Label: t.subject, Value: st.Subject},
		{Type: wire.ComponentField, Kind: "text", Name: "description", Label: t.description, Value: st.Description},
		{Type: wire.ComponentField, Kind: "text", Name: "start_date", Label: t.start, Value: st.StartDate},
		{Type: wire.ComponentField, Kind: "text", Name: "due_date", Label: t.due, Value: st.DueDate},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-create", Submit: true}},
		{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	}}}}, nil
}

func detailView(r *ext.UIRequest, st pageState, item workPackage) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{
		{Type: wire.ComponentText, Text: clipText(t.project+": "+item.Links.Project.Title+" · "+item.Links.Type.Title+" · "+item.Links.Status.Title, 1800)},
		{Type: wire.ComponentText, Text: t.start + ": " + item.StartDate + " · " + t.due + ": " + item.DueDate},
		{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	}
	v := &wire.View{Title: clipText(item.Subject, 1800), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.work, Items: items}}}
	if item.Description.Raw != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.description, Items: textComponents(clipText(item.Description.Raw, 60000))})
	}
	return v
}

func defaultType(types []workType) workType {
	for _, item := range types {
		if item.IsDefault && !item.IsMilestone {
			return item
		}
	}
	for _, item := range types {
		if !item.IsMilestone {
			return item
		}
	}
	return workType{}
}

func textComponents(text string) []wire.Component {
	var out []wire.Component
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > 0 {
			n := min(len(r), 1800)
			out = append(out, wire.Component{Type: wire.ComponentText, Text: string(r[:n])})
			r = r[n:]
			if len(out) >= 240 {
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
