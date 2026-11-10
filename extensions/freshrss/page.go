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
	Mode         string `json:"m,omitempty"`
	View         string `json:"v,omitempty"`
	Continuation string `json:"c,omitempty"`
	ArticleID    string `json:"i,omitempty"`
	DraftID      string `json:"d,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || len(st.Continuation) > 64 || len(st.ArticleID) > 128 || len(st.DraftID) > 128 || len(st.View) > 30 || len(st.Mode) > 30 {
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
		if r.Values["view"] != "unread" && r.Values["view"] != "starred" {
			return nil, errors.New("invalid reading view")
		}
		st.View, st.Continuation = r.Values["view"], ""
	case "more":
		if r.Values["continuation"] == "" || r.Values["continuation"] != st.Continuation {
			return nil, errors.New("invalid continuation")
		}
	case "detail":
		if !validItemID(r.Values["id"]) {
			return nil, errors.New("invalid article ID")
		}
		st.Mode, st.ArticleID = "detail", r.Values["id"]
	case "back":
		st.Mode, st.ArticleID, st.DraftID = "", "", ""
	case "read":
		if st.Mode != "detail" || !validItemID(st.ArticleID) || st.DraftID == "" {
			return nil, errors.New("a read draft is required")
		}
		raw, _ := json.Marshal(readArgs{ItemID: st.ArticleID})
		out, err := a.doAction(ctx, "ui-read:"+st.DraftID, "freshrss_mark_read", raw)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Toast: out["text"].(string), Pop: true}, nil
	case "subscriptions":
		st = pageState{Mode: "subscriptions"}
	default:
		return nil, fmt.Errorf("unknown page action %q", r.Action)
	}
	v, err := a.page(ctx, r, st)
	return &wire.ActionResult{View: v}, err
}

func (a *app) page(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if a.client.base == "" || a.client.username == "" || a.client.password == "" {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingConfig}}}}}, nil
	}
	if st.Mode == "detail" {
		page, err := a.client.articles(ctx, st.View, "")
		a.record(err)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.ID == st.ArticleID {
				st.DraftID = randomID()
				return detailView(r, st, item), nil
			}
		}
		return nil, errors.New("the article is not in the current reading view")
	}
	if st.Mode == "subscriptions" {
		items, err := a.client.subscriptions(ctx)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.subscriptions}
		for _, item := range items {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: clipText(item.Title+" · "+item.ID, 1800)})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noSubscriptions})
		}
		return &wire.View{Title: t.subscriptions, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode != "" {
		return nil, errors.New("invalid reading view")
	}
	if st.View == "" {
		st.View = "unread"
	}
	page, err := a.client.articles(ctx, st.View, st.Continuation)
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.reading, State: ext.EncodeState(st), Filter: &wire.Filter{Name: "view", Title: t.reading, Value: st.View, Action: wire.Action{ID: "view"}, Choices: []wire.Choice{{Value: "unread", Title: t.unread}, {Value: "starred", Title: t.starred}}}}
	section := wire.Section{Title: t.articles}
	for _, item := range page.Items {
		if !validItemID(item.ID) || item.Title == "" {
			continue
		}
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "article-" + item.ID, Title: clipText(item.Title, 1800), Subtitle: clipText(item.Origin.Title+" · "+item.Author, 1800), Action: &wire.Action{ID: "detail", Values: map[string]string{"id": item.ID}}})
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noArticles})
	}
	if page.Continuation != "" {
		st.Continuation = page.Continuation
		v.State = ext.EncodeState(st)
		section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"continuation": st.Continuation}}}
	}
	v.Sections = []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.subscriptions, Action: &wire.Action{ID: "subscriptions", Push: &wire.Push{Title: t.subscriptions, State: ext.EncodeState(pageState{Mode: "subscriptions"})}}}}}}
	return v, nil
}

func detailView(r *ext.UIRequest, st pageState, item article) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{{Type: wire.ComponentText, Text: clipText(item.Origin.Title+" · "+item.Author, 1800)}}
	if link := articleLink(item); link != "" {
		items = append(items, wire.Component{Type: wire.ComponentLink, Title: t.open, URL: link})
	}
	read := false
	for _, category := range item.Categories {
		if category == "user/-/state/com.google/read" {
			read = true
		}
	}
	if !read {
		items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.markRead, Action: &wire.Action{ID: "read", Confirm: t.markRead + "\n" + item.Title}})
	}
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}})
	v := &wire.View{Title: clipText(item.Title, 1800), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.reading, Items: items}}}
	if text := plainText(articleText(item)); text != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.content, Items: textComponents(clipText(text, 40000))})
	} else {
		v.Sections = append(v.Sections, wire.Section{Title: t.content, Items: []wire.Component{{Type: wire.ComponentText, Text: t.noContent, Style: wire.TextStyleSecondary}}})
	}
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
