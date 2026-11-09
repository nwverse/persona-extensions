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
	Team    string     `json:"t,omitempty"`
	Query   string     `json:"q,omitempty"`
	Cursor  string     `json:"c,omitempty"`
	IssueID string     `json:"i,omitempty"`
	Mode    string     `json:"m,omitempty"`
	DraftID string     `json:"d,omitempty"`
	Create  createArgs `json:"n,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || len(st.Cursor) > 256 || len(st.IssueID) > 128 || len(st.DraftID) > 128 {
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
		st.Query, st.Cursor, st.Mode = r.Values["q"], "", "list"
	case "team":
		st.Team, st.Cursor, st.Mode = r.Values["team"], "", "list"
	case "more":
		if st.Cursor == "" {
			return nil, errors.New("no further issue page")
		}
	case "detail":
		if err := validID(r.Values["id"]); err != nil {
			return nil, err
		}
		st.IssueID, st.Mode = r.Values["id"], "detail"
	case "back":
		st.Mode, st.IssueID = "list", ""
	case "create":
		st.Mode, st.DraftID = "create", randomID()
	case "review-create":
		teamID, title := r.Values["team_id"], r.Values["title"]
		if err := validID(teamID); err != nil {
			return nil, err
		}
		if err := validText(title, 255, false); err != nil || strings.TrimSpace(title) == "" {
			return nil, errors.New("title is required")
		}
		st.Create = createArgs{TeamID: teamID, Title: title, Description: r.Values["description"]}
		st.Mode = "review"
	case "confirm-create":
		if st.DraftID == "" {
			return nil, errors.New("the create form expired")
		}
		raw, _ := json.Marshal(st.Create)
		out, err := a.doAction(ctx, "ui-create:"+st.DraftID, "linear_create", raw)
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
	if a.client.key == "" {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingToken}}}}}, nil
	}
	if st.Mode == "create" {
		return a.createView(ctx, r, st)
	}
	if st.Mode == "review" {
		return a.reviewCreateView(ctx, r, st)
	}
	if st.Mode == "detail" {
		item, err := a.client.issue(ctx, st.IssueID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		return detailView(r, st, item), nil
	}
	page, err := a.client.assigned(ctx, st.Cursor)
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.assigned, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}}
	section := wire.Section{Title: fmt.Sprintf(t.results, len(page.Nodes)), Items: []wire.Component{{
		Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "create", Push: &wire.Push{State: ext.EncodeState(pageState{Mode: "create", DraftID: randomID()}), Title: t.create}},
	}}}
	for _, item := range page.Nodes {
		if st.Team != "" && !strings.EqualFold(item.Team.Key, st.Team) && !strings.EqualFold(item.Team.Name, st.Team) {
			continue
		}
		if st.Query != "" && !strings.Contains(strings.ToLower(item.Title+" "+item.Identifier), strings.ToLower(st.Query)) {
			continue
		}
		section.Items = append(section.Items, issueRow(item))
	}
	if len(section.Items) == 1 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noIssues})
	}
	if page.PageInfo.HasNextPage && page.PageInfo.EndCursor != "" {
		st.Cursor = page.PageInfo.EndCursor
		v.State = ext.EncodeState(st)
		section.More = &wire.More{Title: "Load more", Action: wire.Action{ID: "more"}}
	}
	v.Sections = []wire.Section{section}
	return v, nil
}

func issueRow(item issue) wire.Component {
	subtitle := item.Team.Key + " · " + item.State.Name
	if item.DueDate != "" {
		subtitle += " · " + item.DueDate
	}
	return wire.Component{Type: wire.ComponentRow, Key: "issue-" + item.ID, Title: item.Identifier + " " + clipText(item.Title, 1800), Subtitle: subtitle, Action: &wire.Action{ID: "detail", Values: map[string]string{"id": item.ID}}}
}

func detailView(r *ext.UIRequest, st pageState, item issue) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{
		{Type: wire.ComponentText, Text: item.Title},
		{Type: wire.ComponentText, Text: t.team + ": " + item.Team.Name + " · " + t.state + ": " + item.State.Name + " (" + item.State.Type + ")"},
	}
	if item.Assignee != nil {
		items = append(items, wire.Component{Type: wire.ComponentText, Text: t.assignee + ": " + item.Assignee.DisplayName})
	}
	if item.DueDate != "" {
		items = append(items, wire.Component{Type: wire.ComponentText, Text: t.due + ": " + item.DueDate})
	}
	items = append(items, wire.Component{Type: wire.ComponentLink, Title: t.open, URL: item.URL}, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}})
	v := &wire.View{Title: item.Identifier, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.assigned, Items: items}}}
	if item.Description != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.description, Items: textComponents(item.Description)})
	}
	if len(item.Comments.Nodes) > 0 {
		section := wire.Section{Title: t.comments}
		for _, c := range item.Comments.Nodes {
			author := ""
			if c.User != nil {
				author = c.User.DisplayName + " · "
			}
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: author + c.CreatedAt, Style: wire.TextStyleSecondary})
			section.Items = append(section.Items, textComponents(c.Body)...)
		}
		v.Sections = append(v.Sections, section)
	}
	return v
}

func (a *app) createView(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	teams, err := a.teamsRead(ctx)
	if err != nil {
		return nil, err
	}
	choices := []wire.Choice{}
	for _, team := range teams {
		choices = append(choices, wire.Choice{Value: team.ID, Title: team.Name})
	}
	if len(choices) == 0 {
		return nil, errors.New("Linear has no accessible team")
	}
	items := []wire.Component{
		{Type: wire.ComponentField, Kind: "choice", Name: "team_id", Label: t.team, Value: choices[0].Value, Choices: choices},
		{Type: wire.ComponentField, Kind: "text", Name: "title", Label: t.assigned},
		{Type: wire.ComponentField, Kind: "text", Name: "description", Label: t.description},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-create", Submit: true}},
	}
	return &wire.View{Title: t.create, State: ext.EncodeState(st), Sections: []wire.Section{{Items: items}}}, nil
}

func (a *app) reviewCreateView(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	teams, err := a.teamsRead(ctx)
	if err != nil {
		return nil, err
	}
	teamName := st.Create.TeamID
	for _, team := range teams {
		if team.ID == st.Create.TeamID {
			teamName = team.Name
		}
	}
	items := []wire.Component{{Type: wire.ComponentText, Text: st.Create.Title}, {Type: wire.ComponentText, Text: t.team + ": " + teamName}}
	items = append(items, textComponents(st.Create.Description)...)
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.confirm, Action: &wire.Action{ID: "confirm-create", Confirm: t.create + "\n" + st.Create.Title}})
	return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Items: items}}}, nil
}

func textComponents(text string) []wire.Component {
	var out []wire.Component
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > 0 {
			n := min(len(r), 2000)
			out = append(out, wire.Component{Type: wire.ComponentText, Text: string(r[:n])})
			r = r[n:]
		}
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
