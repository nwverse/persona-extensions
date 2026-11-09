package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type pageState struct {
	Query     string `json:"q,omitempty"`
	Page      int    `json:"p,omitempty"`
	ArticleID int64  `json:"a,omitempty"`
	Mode      string `json:"m,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.Page < 0 || st.Page > 5 || len(st.Query) > 200 || st.ArticleID < 0 {
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
		st.Query, st.Page, st.Mode = r.Values["q"], 0, "list"
	case "more":
		st.Page = min(st.Page+1, 5)
	case "detail":
		id, err := strconv.ParseInt(r.Values["id"], 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("invalid article ID")
		}
		st.ArticleID, st.Mode = id, "detail"
	case "back":
		st.Mode, st.ArticleID = "list", 0
	default:
		return nil, fmt.Errorf("unknown page action %q", r.Action)
	}
	v, err := a.page(ctx, r, st)
	return &wire.ActionResult{View: v}, err
}

func (a *app) page(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if st.Mode == "detail" {
		item, err := a.client.article(ctx, st.ArticleID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		comments, err := a.client.comments(ctx, st.ArticleID)
		if err != nil {
			return nil, err
		}
		return detailView(r, st, item, comments), nil
	}
	var items []article
	for n := 0; n <= st.Page; n++ {
		batch, err := a.client.articles(ctx, "", n+1)
		if err != nil {
			a.record(err)
			return nil, err
		}
		items = append(items, batch...)
		if len(batch) < 30 {
			break
		}
	}
	a.record(nil)
	items = filterArticles(items, a.cfg, st.Query, 0)
	v := &wire.View{Title: t.articles, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}}
	section := wire.Section{Title: fmt.Sprintf(t.results, len(items))}
	for _, item := range items {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "article-" + strconv.FormatInt(item.ID, 10), Title: clipText(item.Title, 2000), Subtitle: fmt.Sprintf("%d min · %d comments · %s", item.ReadingMinutes, item.CommentsCount, strings.Join(item.tagNames(), ", ")), Action: &wire.Action{ID: "detail", Values: map[string]string{"id": strconv.FormatInt(item.ID, 10)}}})
	}
	if len(items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noArticles})
	}
	if len(items) >= 30*(st.Page+1) && st.Page < 5 {
		section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more"}}
	}
	v.Sections = []wire.Section{section}
	return v, nil
}

func detailView(r *ext.UIRequest, st pageState, item article, comments []comment) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{
		{Type: wire.ComponentText, Text: item.User.Name + " · " + fmt.Sprintf("%d min read", item.ReadingMinutes), Style: wire.TextStyleSecondary},
		{Type: wire.ComponentLink, Title: t.open, URL: item.URL},
		{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	}
	v := &wire.View{Title: clipText(item.Title, 2000), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.read, Items: items}}}
	if item.Description != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.summary, Items: textComponents(item.Description)})
	}
	if item.BodyMarkdown != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.read, Items: textComponents(item.BodyMarkdown)})
	}
	if len(comments) > 0 {
		section := wire.Section{Title: t.comments}
		addCommentViews(&section, comments, 0)
		v.Sections = append(v.Sections, section)
	}
	return v
}

func addCommentViews(section *wire.Section, comments []comment, depth int) {
	if depth > 6 {
		return
	}
	for _, c := range comments {
		text, err := plainHTML(c.BodyHTML)
		if err != nil || text == "" {
			continue
		}
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: strings.Repeat("  ", depth) + c.User.Name, Style: wire.TextStyleSecondary})
		section.Items = append(section.Items, textComponents(text)...)
		addCommentViews(section, c.Children, depth+1)
	}
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
