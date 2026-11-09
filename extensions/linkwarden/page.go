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
	Query        string `json:"q,omitempty"`
	CollectionID int64  `json:"c,omitempty"`
	LinkID       int64  `json:"l,omitempty"`
	Mode         string `json:"m,omitempty"`
	DraftID      string `json:"d,omitempty"`
	SaveURL      string `json:"u,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.CollectionID < 0 || st.LinkID < 0 || len(st.DraftID) > 128 || len(st.SaveURL) > 8192 {
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
		st.Query, st.Mode = r.Values["q"], "list"
	case "collection":
		id, err := strconv.ParseInt(r.Values["collection"], 10, 64)
		if err != nil || id < 0 {
			return nil, errors.New("invalid collection ID")
		}
		st.CollectionID, st.Mode = id, "list"
	case "detail":
		id, err := strconv.ParseInt(r.Values["id"], 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("invalid link ID")
		}
		st.LinkID, st.Mode = id, "detail"
	case "back":
		st.Mode, st.LinkID = "list", 0
	case "save-form":
		st.Mode, st.DraftID = "save", randomID()
	case "review-save":
		u, err := validHTTPURL(r.Values["url"])
		if err != nil {
			return nil, err
		}
		st.SaveURL, st.Mode = u, "review-save"
	case "save":
		if st.DraftID == "" {
			return nil, errors.New("the save form expired")
		}
		out, err := a.doAction(ctx, "ui-save:"+st.DraftID, "linkwarden_save", []byte(`{"url":`+quote(st.SaveURL)+`}`))
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	case "archive":
		if st.LinkID <= 0 {
			return nil, errors.New("invalid link ID")
		}
		raw := []byte(`{"id":` + strconv.FormatInt(st.LinkID, 10) + `}`)
		out, err := a.doAction(ctx, "ui-archive:"+st.DraftID, "linkwarden_archive", raw)
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
	if a.client.base == "" {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingConfig}}}}}, nil
	}
	if st.Mode == "save" {
		return saveView(r, st), nil
	}
	if st.Mode == "review-save" {
		return reviewSaveView(r, st), nil
	}
	if st.Mode == "detail" {
		item, err := a.client.link(ctx, st.LinkID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		highlights, err := a.client.highlights(ctx, st.LinkID)
		if err != nil {
			return nil, err
		}
		return detailView(r, st, item, highlights), nil
	}
	items, err := a.client.search(ctx, st.Query, st.CollectionID)
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.links, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}}
	if collections, err := a.client.collections(ctx); err == nil {
		choices := []wire.Choice{{Value: "0", Title: t.all}}
		for _, c := range collections {
			choices = append(choices, wire.Choice{Value: strconv.FormatInt(c.ID, 10), Title: c.Name})
		}
		v.Filter = &wire.Filter{Name: "collection", Title: t.collections, Value: strconv.FormatInt(st.CollectionID, 10), Choices: choices, Action: wire.Action{ID: "collection"}}
	}
	section := wire.Section{Title: fmt.Sprintf(t.results, len(items)), Items: []wire.Component{{
		Type: wire.ComponentButton, Title: t.save, Action: &wire.Action{ID: "save-form", Push: &wire.Push{State: ext.EncodeState(pageState{Mode: "save", DraftID: randomID()}), Title: t.save}},
	}}}
	for _, item := range items {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "link-" + strconv.FormatInt(item.ID, 10), Title: clipText(item.Name, 2000), Subtitle: item.URL, Action: &wire.Action{ID: "detail", Values: map[string]string{"id": strconv.FormatInt(item.ID, 10)}}})
	}
	if len(items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noLinks})
	}
	v.Sections = []wire.Section{section}
	return v, nil
}

func detailView(r *ext.UIRequest, st pageState, item link, highlights []highlight) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{}
	if item.URL != "" {
		items = append(items, wire.Component{Type: wire.ComponentLink, Title: t.open, URL: item.URL})
	}
	if item.Collection != nil {
		items = append(items, wire.Component{Type: wire.ComponentText, Text: item.Collection.Name, Style: wire.TextStyleSecondary})
	}
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.archive, Action: &wire.Action{ID: "archive", Confirm: t.archive + "\n" + item.Name}})
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}})
	v := &wire.View{Title: clipText(item.Name, 2000), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.read, Items: items}}}
	if item.Note != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.note, Items: textComponents(item.Note)})
	}
	if item.Description != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.read, Items: textComponents(item.Description)})
	}
	if len(highlights) > 0 {
		section := wire.Section{Title: t.highlights}
		for _, h := range highlights {
			section.Items = append(section.Items, textComponents(h.Text)...)
			section.Items = append(section.Items, textComponents(h.Note)...)
		}
		v.Sections = append(v.Sections, section)
	}
	return v
}

func saveView(r *ext.UIRequest, st pageState) *wire.View {
	t := textsFor(r.Locale)
	return &wire.View{Title: t.save, State: ext.EncodeState(st), Sections: []wire.Section{{Items: []wire.Component{
		{Type: wire.ComponentField, Kind: "text", Name: "url", Label: "URL", Placeholder: "https://example.com/article"},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-save", Submit: true}},
	}}}}
}

func reviewSaveView(r *ext.UIRequest, st pageState) *wire.View {
	t := textsFor(r.Locale)
	return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Items: []wire.Component{
		{Type: wire.ComponentText, Text: st.SaveURL},
		{Type: wire.ComponentButton, Title: t.confirm, Action: &wire.Action{ID: "save", Confirm: t.save + "\n" + st.SaveURL}},
	}}}}
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

func quote(text string) string {
	out, _ := json.Marshal(text)
	return string(out)
}
