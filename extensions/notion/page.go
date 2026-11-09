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
	Query    string `json:"q,omitempty"`
	Cursor   string `json:"c,omitempty"`
	PageID   string `json:"p,omitempty"`
	Mode     string `json:"m,omitempty"`
	DraftID  string `json:"d,omitempty"`
	Title    string `json:"t,omitempty"`
	Content  string `json:"b,omitempty"`
	ParentID string `json:"r,omitempty"`
	Kind     string `json:"k,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || len(st.Cursor) > 256 || len(st.PageID) > 128 || len(st.DraftID) > 128 || len(st.ParentID) > 128 {
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
	case "more":
		if st.Cursor == "" {
			return nil, errors.New("no further page")
		}
	case "detail":
		if err := validNotionID(r.Values["id"]); err != nil {
			return nil, err
		}
		st.PageID, st.Mode = r.Values["id"], "detail"
	case "back":
		st.Mode, st.PageID = "list", ""
	case "create":
		st.Mode, st.DraftID = "create", randomID()
	case "review-create":
		if err := validNotionID(r.Values["parent_id"]); err != nil {
			return nil, err
		}
		if err := validNote(r.Values["title"], 500); err != nil {
			return nil, err
		}
		st.ParentID, st.Title, st.Content, st.Mode = r.Values["parent_id"], r.Values["title"], r.Values["content"], "review"
	case "confirm-create":
		if st.DraftID == "" {
			return nil, errors.New("the create form expired")
		}
		raw, _ := json.Marshal(map[string]string{"parent_id": st.ParentID, "title": st.Title, "content": st.Content})
		out, err := a.doAction(ctx, "ui-create:"+st.DraftID, "notion_create", raw)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	case "append":
		st.Mode, st.DraftID = "append", randomID()
	case "review-append":
		if err := validNotionID(st.PageID); err != nil {
			return nil, err
		}
		if r.Values["kind"] != "paragraph" && r.Values["kind"] != "todo" {
			return nil, errors.New("choose paragraph or todo")
		}
		if err := validNote(r.Values["text"], 2000); err != nil {
			return nil, err
		}
		st.Kind, st.Content, st.Mode = r.Values["kind"], r.Values["text"], "review-append"
	case "confirm-append":
		if st.DraftID == "" {
			return nil, errors.New("the note form expired")
		}
		raw, _ := json.Marshal(map[string]string{"page_id": st.PageID, "kind": st.Kind, "text": st.Content})
		out, err := a.doAction(ctx, "ui-append:"+st.DraftID, "notion_append", raw)
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
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingToken}}}}}, nil
	}
	if st.Mode == "create" {
		return a.createView(ctx, r, st)
	}
	if st.Mode == "append" {
		return a.appendView(r, st), nil
	}
	if st.Mode == "review-append" {
		return a.reviewAppendView(r, st), nil
	}
	if st.Mode == "review" {
		return a.reviewView(r, st), nil
	}
	if st.Mode == "detail" {
		page, err := a.client.page(ctx, st.PageID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		lines, err := a.readBlocks(ctx, st.PageID, 0)
		if err != nil {
			return nil, err
		}
		return detailView(r, st, page, lines), nil
	}
	page, err := a.client.search(ctx, st.Query, st.Cursor)
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.pages, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}}
	section := wire.Section{Title: fmt.Sprintf(t.results, len(page.Results)), Items: []wire.Component{{
		Type: wire.ComponentButton, Title: t.create, Action: &wire.Action{ID: "create", Push: &wire.Push{State: ext.EncodeState(pageState{Mode: "create", DraftID: randomID()}), Title: t.create}},
	}}}
	for _, item := range page.Results {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "page-" + item.ID, Title: pageTitle(item), Subtitle: item.LastEditedTime, Action: &wire.Action{ID: "detail", Values: map[string]string{"id": item.ID}}})
	}
	if len(page.Results) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noPages})
	}
	if page.HasMore && page.NextCursor != nil && *page.NextCursor != "" {
		st.Cursor = *page.NextCursor
		v.State = ext.EncodeState(st)
		section.More = &wire.More{Title: "Load more", Action: wire.Action{ID: "more"}}
	}
	v.Sections = []wire.Section{section}
	return v, nil
}

func detailView(r *ext.UIRequest, st pageState, page notionPage, lines []string) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{
		{Type: wire.ComponentLink, Title: t.open, URL: page.URL},
		{Type: wire.ComponentButton, Title: t.append, Action: &wire.Action{ID: "append", Push: &wire.Push{State: ext.EncodeState(pageState{Mode: "append", PageID: st.PageID, DraftID: randomID()}), Title: t.append}}},
		{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	}
	if len(lines) == 0 {
		items = append(items, wire.Component{Type: wire.ComponentText, Text: t.noContent, Style: wire.TextStyleSecondary})
	}
	for _, line := range lines {
		items = append(items, textComponents(line)...)
	}
	return &wire.View{Title: pageTitle(page), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.read, Items: items}}}
}

func (a *app) createView(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	pages, err := a.client.search(ctx, "", "")
	if err != nil {
		return nil, err
	}
	choices := []wire.Choice{}
	for _, item := range pages.Results {
		choices = append(choices, wire.Choice{Value: item.ID, Title: pageTitle(item)})
	}
	if len(choices) == 0 {
		return nil, errors.New("no shared Notion page can be a parent")
	}
	return &wire.View{Title: t.create, State: ext.EncodeState(st), Sections: []wire.Section{{Items: []wire.Component{
		{Type: wire.ComponentField, Kind: "choice", Name: "parent_id", Label: t.parent, Value: choices[0].Value, Choices: choices},
		{Type: wire.ComponentField, Kind: "text", Name: "title", Label: t.title},
		{Type: wire.ComponentField, Kind: "text", Name: "content", Label: t.content},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-create", Submit: true}},
	}}}}, nil
}

func (a *app) appendView(r *ext.UIRequest, st pageState) *wire.View {
	t := textsFor(r.Locale)
	return &wire.View{Title: t.append, State: ext.EncodeState(st), Sections: []wire.Section{{Items: []wire.Component{
		{Type: wire.ComponentField, Kind: "choice", Name: "kind", Label: t.content, Value: "paragraph", Choices: []wire.Choice{{Value: "paragraph", Title: t.paragraph}, {Value: "todo", Title: t.todo}}},
		{Type: wire.ComponentField, Kind: "text", Name: "text", Label: t.append},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-append", Submit: true}},
	}}}}
}

func (a *app) reviewAppendView(r *ext.UIRequest, st pageState) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{{Type: wire.ComponentText, Text: st.Content}, {Type: wire.ComponentText, Text: t.parent + ": " + st.PageID}}
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.save, Action: &wire.Action{ID: "confirm-append", Confirm: t.append + "\n" + st.Content}})
	return &wire.View{Title: t.append, State: ext.EncodeState(st), Sections: []wire.Section{{Items: items}}}
}

func (a *app) reviewView(r *ext.UIRequest, st pageState) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{{Type: wire.ComponentText, Text: st.Title}, {Type: wire.ComponentText, Text: t.parent + ": " + st.ParentID}}
	items = append(items, textComponents(st.Content)...)
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.confirm, Action: &wire.Action{ID: "confirm-create", Confirm: t.create + "\n" + st.Title}})
	return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Items: items}}}
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
