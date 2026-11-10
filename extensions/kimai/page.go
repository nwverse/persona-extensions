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
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type pageState struct {
	Mode        string `json:"m,omitempty"`
	View        string `json:"v,omitempty"`
	Page        int    `json:"p,omitempty"`
	TimesheetID int64  `json:"i,omitempty"`
	ProjectID   int64  `json:"j,omitempty"`
	ActivityID  int64  `json:"a,omitempty"`
	DraftID     string `json:"d,omitempty"`
	Begin       string `json:"b,omitempty"`
	End         string `json:"e,omitempty"`
	Description string `json:"t,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.Page < 0 || st.TimesheetID < 0 || st.ProjectID < 0 || st.ActivityID < 0 || len(st.DraftID) > 128 || len(st.Begin) > 64 || len(st.End) > 64 || len(st.Description) > 5100 || len(st.Mode) > 30 || len(st.View) > 30 {
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
	case "view":
		if r.Values["view"] != "recent" && r.Values["view"] != "active" && r.Values["view"] != "timesheets" {
			return nil, errors.New("invalid timesheet view")
		}
		st.View, st.Page = r.Values["view"], 1
	case "more":
		st.Page, err = strconv.Atoi(r.Values["page"])
		if err != nil || st.Page < 2 {
			return nil, errors.New("invalid next page")
		}
	case "projects":
		st = pageState{Mode: "projects"}
	case "activities":
		st.ProjectID, err = strconv.ParseInt(r.Values["project_id"], 10, 64)
		if err != nil || st.ProjectID <= 0 {
			return nil, errors.New("invalid project ID")
		}
		st = pageState{Mode: "activities", ProjectID: st.ProjectID}
	case "create":
		if st.Mode != "review-create" {
			st.ActivityID, err = strconv.ParseInt(r.Values["activity_id"], 10, 64)
			if err != nil || st.ActivityID <= 0 || st.ProjectID <= 0 {
				return nil, errors.New("invalid activity ID")
			}
			st = pageState{Mode: "create", ProjectID: st.ProjectID, ActivityID: st.ActivityID, DraftID: randomID(), Begin: timeNowRFC3339()}
		} else {
			st.Mode = "create"
		}
	case "review-create":
		in := createArgs{ProjectID: st.ProjectID, ActivityID: st.ActivityID, Begin: strings.TrimSpace(r.Values["begin"]), End: strings.TrimSpace(r.Values["end"]), Description: strings.TrimSpace(r.Values["description"])}
		if err := in.validate(); err != nil {
			return nil, err
		}
		st.Begin, st.End, st.Description, st.Mode = in.Begin, in.End, in.Description, "review-create"
	case "confirm-create":
		in := createArgs{ProjectID: st.ProjectID, ActivityID: st.ActivityID, Begin: st.Begin, End: st.End, Description: st.Description}
		if err := in.validate(); err != nil || st.Mode != "review-create" || st.DraftID == "" {
			return nil, errors.New("a valid reviewed time entry is required")
		}
		raw, _ := json.Marshal(in)
		out, err := a.doAction(ctx, "ui-create:"+st.DraftID, "kimai_create_timesheet", raw)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	case "stop":
		if st.Mode != "active" || st.DraftID == "" {
			return nil, errors.New("a running timer stop draft is required")
		}
		id, err := strconv.ParseInt(r.Values["id"], 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("invalid timesheet ID")
		}
		st.TimesheetID = id
		raw, _ := json.Marshal(map[string]int64{"timesheet_id": st.TimesheetID})
		out, err := a.doAction(ctx, "ui-stop:"+st.DraftID, "kimai_stop_timesheet", raw)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	case "back":
		st.Mode, st.DraftID, st.TimesheetID = "", "", 0
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
		items, err := a.client.projects(ctx)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.projects}
		for _, item := range items {
			if item.Visible {
				section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: fmt.Sprintf("project-%d", item.ID), Title: clipText(item.Name, 1800), Action: &wire.Action{ID: "activities", Values: map[string]string{"project_id": fmt.Sprint(item.ID)}}})
			}
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noProjects})
		}
		return &wire.View{Title: t.projects, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode == "activities" {
		items, err := a.client.activities(ctx, st.ProjectID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.activities}
		for _, item := range items {
			if item.Visible {
				section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: fmt.Sprintf("activity-%d", item.ID), Title: clipText(item.Name, 1800), Action: &wire.Action{ID: "create", Values: map[string]string{"activity_id": fmt.Sprint(item.ID)}}})
			}
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noActivities})
		}
		return &wire.View{Title: t.activities, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "projects"}}}}}}, nil
	}
	if st.Mode != "" && st.Mode != "active" {
		return nil, errors.New("invalid time view")
	}
	view := st.View
	if st.Mode == "active" {
		view = "active"
	}
	if view == "" {
		view = "recent"
	}
	if st.Page == 0 {
		st.Page = 1
	}
	items, err := a.client.timesheets(ctx, view, st.Page)
	a.record(err)
	if err != nil {
		return nil, err
	}
	if st.Mode == "active" {
		return activeView(r, st, items), nil
	}
	v := &wire.View{Title: t.time, State: ext.EncodeState(st), Filter: &wire.Filter{Name: "view", Title: t.time, Value: view, Action: wire.Action{ID: "view"}, Choices: []wire.Choice{{Value: "recent", Title: t.recent}, {Value: "active", Title: t.active}, {Value: "timesheets", Title: t.history}}}}
	section := wire.Section{Title: t.entries}
	for _, item := range items {
		if item.ID <= 0 {
			continue
		}
		section.Items = append(section.Items, timesheetComponents(item, t)...)
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noEntries})
	}
	if view == "timesheets" && len(items) == 30 {
		section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"page": fmt.Sprint(st.Page + 1)}}}
	}
	v.Sections = []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "projects", Push: &wire.Push{Title: t.projects, State: ext.EncodeState(pageState{Mode: "projects"})}}}}}}
	return v, nil
}

func activeView(r *ext.UIRequest, st pageState, items []timesheet) *wire.View {
	t := textsFor(r.Locale)
	st.DraftID = randomID()
	v := &wire.View{Title: t.active, State: ext.EncodeState(st), Filter: &wire.Filter{Name: "view", Title: t.time, Value: "active", Action: wire.Action{ID: "view"}, Choices: []wire.Choice{{Value: "recent", Title: t.recent}, {Value: "active", Title: t.active}, {Value: "timesheets", Title: t.history}}}}
	section := wire.Section{Title: t.active}
	for _, item := range items {
		if item.ID <= 0 {
			continue
		}
		section.Items = append(section.Items, timesheetComponents(item, t)...)
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentButton, Title: t.stop, Action: &wire.Action{ID: "stop", Confirm: t.stop + "\n" + item.Begin, Values: map[string]string{"id": fmt.Sprint(item.ID)}}})
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noActive})
	}
	v.Sections = []wire.Section{section}
	return v
}

func timesheetComponents(item timesheet, t *texts) []wire.Component {
	project, activity := "", ""
	if item.Project != nil {
		project = item.Project.Name
	}
	if item.Activity != nil {
		activity = item.Activity.Name
	}
	return []wire.Component{{Type: wire.ComponentText, Text: clipText(fmt.Sprintf("%s · %s · %s", project, activity, item.Begin), 1800)}, {Type: wire.ComponentText, Text: clipText(item.Description+" · "+endText(item.End), 1800), Style: wire.TextStyleSecondary}}
}

func createView(r *ext.UIRequest, st pageState) *wire.View {
	t := textsFor(r.Locale)
	if st.Mode == "review-create" {
		items := []wire.Component{{Type: wire.ComponentText, Text: st.Begin + " → " + endText(&st.End)}}
		if st.Description != "" {
			items = append(items, textComponents(st.Description)...)
		}
		items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "confirm-create", Confirm: t.create + "\n" + st.Begin}}, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "create"}})
		return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.create, Items: items}}}
	}
	return &wire.View{Title: t.create, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.timerNotice, Items: []wire.Component{
		{Type: wire.ComponentField, Kind: "text", Name: "begin", Label: t.begin, Value: st.Begin},
		{Type: wire.ComponentField, Kind: "text", Name: "end", Label: t.end, Value: st.End},
		{Type: wire.ComponentField, Kind: "text", Name: "description", Label: t.description, Value: st.Description},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-create", Submit: true}},
		{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	}}}}
}

func timeNowRFC3339() string { return time.Now().Format(time.RFC3339) }

func textComponents(text string) []wire.Component {
	var out []wire.Component
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > 0 {
			n := min(len(r), 1800)
			out = append(out, wire.Component{Type: wire.ComponentText, Text: string(r[:n])})
			r = r[n:]
			if len(out) >= 200 {
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
