package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type pageState struct {
	Filter  string `json:"f,omitempty"`
	Query   string `json:"q,omitempty"`
	StoryID string `json:"s,omitempty"`
	Mode    string `json:"m,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || len(st.StoryID) > 32 || len(st.Query) > 200 {
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
	case "filter":
		if r.Values["filter"] != "hot" && r.Values["filter"] != "new" {
			return nil, errors.New("invalid feed")
		}
		st.Filter, st.Mode = r.Values["filter"], "list"
	case "detail":
		if err := validID(r.Values["id"]); err != nil {
			return nil, err
		}
		st.StoryID, st.Mode = r.Values["id"], "detail"
	case "back":
		st.Mode, st.StoryID = "list", ""
	default:
		return nil, fmt.Errorf("unknown page action %q", r.Action)
	}
	v, err := a.page(ctx, r, st)
	return &wire.ActionResult{View: v}, err
}

func (a *app) page(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if st.Mode == "detail" {
		item, err := a.client.story(ctx, st.StoryID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		return detailView(r, st, item), nil
	}
	if st.Filter == "" {
		st.Filter = "hot"
	}
	items, err := a.client.stories(ctx, st.Filter == "new")
	a.record(err)
	if err != nil {
		return nil, err
	}
	items = filterStories(items, a.cfg, nil, st.Query, 0)
	v := &wire.View{Title: t.hot, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}}
	v.Filter = &wire.Filter{Name: "filter", Title: t.read, Value: st.Filter, Action: wire.Action{ID: "filter"}, Choices: []wire.Choice{{Value: "hot", Title: t.hot}, {Value: "new", Title: t.newest}}}
	section := wire.Section{Title: fmt.Sprintf(t.results, len(items))}
	for _, item := range items {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "story-" + item.ShortID, Title: clipText(item.Title, 2000), Subtitle: fmt.Sprintf("%d · %d comments · %s", item.Score, item.CommentCount, strings.Join(item.Tags, ", ")), Action: &wire.Action{ID: "detail", Values: map[string]string{"id": item.ShortID}}})
	}
	if len(items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noStories})
	}
	v.Sections = []wire.Section{section}
	return v, nil
}

func detailView(r *ext.UIRequest, st pageState, item story) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{
		{Type: wire.ComponentText, Text: fmt.Sprintf("%d points · %d comments · by %s", item.Score, item.CommentCount, item.Submitter)},
	}
	if len(item.Tags) > 0 {
		items = append(items, wire.Component{Type: wire.ComponentText, Text: strings.Join(item.Tags, ", "), Style: wire.TextStyleSecondary})
	}
	if item.URL != "" {
		items = append(items, wire.Component{Type: wire.ComponentLink, Title: t.open, URL: item.URL})
	}
	if item.CommentsURL != "" {
		items = append(items, wire.Component{Type: wire.ComponentLink, Title: t.discussion, URL: item.CommentsURL})
	}
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}})
	v := &wire.View{Title: clipText(item.Title, 2000), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.read, Items: items}}}
	if item.DescriptionPlain != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.description, Items: textComponents(item.DescriptionPlain)})
	}
	if len(item.Comments) > 0 {
		section := wire.Section{Title: t.comments}
		for _, c := range item.Comments {
			if c.IsDeleted || c.IsModerated {
				continue
			}
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: strings.Repeat("  ", min(c.Depth, 8)) + c.User + " · " + fmt.Sprint(c.Score), Style: wire.TextStyleSecondary})
			section.Items = append(section.Items, textComponents(c.Text)...)
		}
		v.Sections = append(v.Sections, section)
	}
	return v
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
