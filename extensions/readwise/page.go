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
	Location   string `json:"l,omitempty"`
	Cursor     string `json:"c,omitempty"`
	Page       int    `json:"p,omitempty"`
	Query      string `json:"q,omitempty"`
	DocumentID string `json:"d,omitempty"`
	Mode       string `json:"m,omitempty"`
	DraftID    string `json:"r,omitempty"`
	SaveURL    string `json:"u,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || len(st.Cursor) > 256 || len(st.DocumentID) > 128 || len(st.DraftID) > 128 || len(st.SaveURL) > 8192 {
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
		st.Query, st.Cursor, st.Page, st.Mode = r.Values["q"], "", 0, "list"
	case "location":
		if err := validLocation(r.Values["location"]); err != nil {
			return nil, err
		}
		st.Location, st.Cursor, st.Mode = r.Values["location"], "", "list"
		st.Page, st.Query = 0, ""
	case "more":
		if st.Cursor == "" {
			return nil, errors.New("no further reading page")
		}
		st.Page = min(st.Page+1, 3)
	case "detail":
		if err := validDocumentID(r.Values["id"]); err != nil {
			return nil, err
		}
		st.DocumentID, st.Mode = r.Values["id"], "detail"
		st.DraftID = randomID()
	case "back":
		st.Mode, st.DocumentID = "list", ""
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
			return nil, errors.New("the save form expired; reopen it")
		}
		out, err := a.doAction(ctx, "ui-save:"+st.DraftID, "readwise_save", []byte(`{"url":`+quote(st.SaveURL)+`}`))
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	case "shortlist":
		if err := validDocumentID(st.DocumentID); err != nil {
			return nil, err
		}
		out, err := a.doAction(ctx, "ui-move:"+st.DraftID, "readwise_update", []byte(`{"id":`+quote(st.DocumentID)+`,"location":"shortlist"}`))
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
	if st.Mode == "save" {
		return saveView(r, st), nil
	}
	if st.Mode == "review-save" {
		return reviewSaveView(r, st), nil
	}
	if st.Mode == "detail" {
		page, err := a.client.list(ctx, listParams{ID: st.DocumentID, HTML: true})
		a.record(err)
		if err != nil {
			return nil, err
		}
		if len(page.Results) == 0 {
			return nil, errNotFound
		}
		item := page.Results[0]
		highlights, err := a.client.highlights(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		return detailView(r, st, item, highlights)
	}
	if st.Location == "" {
		st.Location = "new"
	}
	page, err := a.readQueue(ctx, st)
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.new, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}}
	v.Filter = &wire.Filter{Name: "location", Title: t.read, Value: st.Location, Action: wire.Action{ID: "location"}, Choices: []wire.Choice{
		{Value: "new", Title: t.new}, {Value: "later", Title: t.later}, {Value: "shortlist", Title: t.shortlist}, {Value: "archive", Title: t.archive}, {Value: "feed", Title: t.feed},
	}}
	section := wire.Section{Title: fmt.Sprintf(t.results, page.Count), Items: []wire.Component{{
		Type: wire.ComponentButton, Title: t.save, Action: &wire.Action{ID: "save-form", Push: &wire.Push{State: ext.EncodeState(pageState{Mode: "save", DraftID: randomID()}), Title: t.save}},
	}}}
	for _, item := range page.Results {
		section.Items = append(section.Items, documentRow(item))
	}
	if page.Cursor != "" && st.Page < 3 {
		section.More = &wire.More{Title: textsFor(r.Locale).more, Action: wire.Action{ID: "more"}}
	}
	if len(page.Results) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noDocuments})
	}
	v.Sections = []wire.Section{section}
	return v, nil
}

func (a *app) readQueue(ctx context.Context, st pageState) (listPage, error) {
	var out listPage
	cursor, seen := "", map[string]bool{}
	for n := 0; n <= st.Page; n++ {
		page, err := a.client.list(ctx, listParams{Location: st.Location, Cursor: cursor})
		if err != nil {
			return listPage{}, err
		}
		for _, item := range page.Results {
			if st.Query != "" && !strings.Contains(strings.ToLower(item.Title+" "+item.Author+" "+item.SiteName+" "+item.Notes), strings.ToLower(st.Query)) {
				continue
			}
			out.Results = append(out.Results, item)
		}
		out.Count = len(out.Results)
		if page.Cursor == "" {
			return out, nil
		}
		if seen[page.Cursor] {
			return listPage{}, errors.New("Readwise repeated its pagination cursor")
		}
		seen[page.Cursor], cursor = true, page.Cursor
		out.Cursor = page.Cursor
	}
	out.Count = len(out.Results)
	if st.Query != "" {
		out.Count = len(out.Results)
	}
	return out, nil
}

func documentRow(item document) wire.Component {
	subtitle := item.SiteName
	if item.Author != "" {
		subtitle += " · " + item.Author
	}
	subtitle += fmt.Sprintf(" · %.0f%%", item.ReadingProgress*100)
	return wire.Component{Type: wire.ComponentRow, Key: "document-" + item.ID, Title: clipText(item.Title, 2000), Subtitle: clipText(subtitle, 2000), Action: &wire.Action{ID: "detail", Values: map[string]string{"id": item.ID}}}
}

func detailView(r *ext.UIRequest, st pageState, item document, highlights []document) (*wire.View, error) {
	t := textsFor(r.Locale)
	text, err := articleText(item.HTMLContent)
	if err != nil {
		return nil, err
	}
	items := []wire.Component{
		{Type: wire.ComponentText, Text: item.Author + " · " + item.SiteName, Style: wire.TextStyleSecondary},
		{Type: wire.ComponentProgress, Label: t.progress, Ratio: &item.ReadingProgress},
		{Type: wire.ComponentButton, Title: t.move, Action: &wire.Action{ID: "shortlist", Confirm: t.move + "\n" + item.Title}},
		{Type: wire.ComponentLink, Title: t.open, URL: item.SourceURL},
		{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	}
	v := &wire.View{Title: clipText(item.Title, 2000), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.read, Items: items}}}
	if item.Notes != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.note, Items: textComponents(item.Notes)})
	}
	if len(highlights) > 0 {
		section := wire.Section{Title: t.highlights}
		for _, h := range highlights {
			section.Items = append(section.Items, textComponents(h.Content)...)
			section.Items = append(section.Items, textComponents(h.Notes)...)
		}
		v.Sections = append(v.Sections, section)
	}
	if text != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.read, Items: textComponents(text)})
	} else {
		v.Sections = append(v.Sections, wire.Section{Items: []wire.Component{{Type: wire.ComponentText, Text: t.noText, Style: wire.TextStyleSecondary}}})
	}
	return v, nil
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
