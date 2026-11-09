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
	Project string `json:"p,omitempty"`
	Repo    string `json:"r,omitempty"`
	Query   string `json:"q,omitempty"`
	Start   int    `json:"s,omitempty"`
	PRID    int64  `json:"i,omitempty"`
	Mode    string `json:"m,omitempty"`
	DraftID string `json:"d,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.Start < 0 || st.PRID < 0 || len(st.Project) > 128 || len(st.Repo) > 128 || len(st.DraftID) > 128 {
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
		st.Query, st.Start, st.Mode = r.Values["q"], 0, "list"
	case "scope":
		if err := validProjectRepo(r.Values["project"], r.Values["repo"]); err != nil {
			return nil, err
		}
		st.Project, st.Repo, st.Start, st.Mode = r.Values["project"], r.Values["repo"], 0, "list"
	case "more":
		st.Start += 50
	case "detail":
		id, err := strconv.ParseInt(r.Values["id"], 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("invalid PR ID")
		}
		st.PRID, st.Mode = id, "detail"
	case "back":
		st.Mode, st.PRID = "list", 0
	case "approve", "unapprove":
		if st.PRID <= 0 {
			return nil, errors.New("invalid PR ID")
		}
		raw := []byte(`{"project":` + quote(st.Project) + `,"repo":` + quote(st.Repo) + `,"id":` + strconv.FormatInt(st.PRID, 10) + `}`)
		out, err := a.doAction(ctx, "ui-"+r.Action+":"+st.DraftID, "bitbucket_"+r.Action, raw)
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
	if st.Mode == "detail" {
		if st.Project == "" || st.Repo == "" {
			return nil, errors.New("project and repository are required for PR details")
		}
		item, err := a.client.pullRequest(ctx, st.Project, st.Repo, st.PRID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		activities, err := a.client.activities(ctx, st.Project, st.Repo, st.PRID)
		if err != nil {
			return nil, err
		}
		return detailView(r, st, item, activities), nil
	}
	page, err := a.client.pullRequests(ctx, st.Project, st.Repo, st.Start)
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.reviews, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}}
	section := wire.Section{Title: fmt.Sprintf(t.results, len(page.Values))}
	for _, item := range page.Values {
		if st.Query != "" && !strings.Contains(strings.ToLower(item.Title), strings.ToLower(st.Query)) {
			continue
		}
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "pr-" + strconv.FormatInt(item.ID, 10), Title: fmt.Sprintf("#%d %s", item.ID, clipText(item.Title, 1800)), Subtitle: item.Author.User.DisplayName + " · " + item.FromRef.DisplayID, Action: &wire.Action{ID: "detail", Values: map[string]string{"id": strconv.FormatInt(item.ID, 10)}}})
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noPRs})
	}
	if !page.IsLastPage && page.NextPageStart != nil {
		section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more"}}
	}
	v.Sections = []wire.Section{section}
	return v, nil
}

func detailView(r *ext.UIRequest, st pageState, item pullRequest, activities []activity) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{
		{Type: wire.ComponentText, Text: item.Title},
		{Type: wire.ComponentText, Text: t.author + ": " + item.Author.User.DisplayName + " · " + item.FromRef.DisplayID + " → " + item.ToRef.DisplayID},
	}
	if len(item.Links.Self) > 0 {
		items = append(items, wire.Component{Type: wire.ComponentLink, Title: t.openPR, URL: item.Links.Self[0].Href})
	}
	items = append(items,
		wire.Component{Type: wire.ComponentButton, Title: t.approve, Action: &wire.Action{ID: "approve", Confirm: t.approve + "\n" + item.Title}},
		wire.Component{Type: wire.ComponentButton, Title: t.unapprove, Action: &wire.Action{ID: "unapprove", Confirm: t.unapprove + "\n" + item.Title}},
		wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}},
	)
	v := &wire.View{Title: fmt.Sprintf("PR #%d", item.ID), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.reviews, Items: items}}}
	if item.Description != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.description, Items: textComponents(item.Description)})
	}
	if len(item.Reviewers) > 0 {
		section := wire.Section{Title: t.reviewers}
		for _, reviewer := range item.Reviewers {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Title: reviewer.User.DisplayName, Subtitle: fmt.Sprintf("approved=%v · %s", reviewer.Approved, reviewer.Status)})
		}
		v.Sections = append(v.Sections, section)
	}
	if len(activities) > 0 {
		section := wire.Section{Title: t.comments}
		for _, activity := range activities {
			if activity.Comment.Text == "" {
				continue
			}
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: activity.Comment.Author.DisplayName, Style: wire.TextStyleSecondary})
			section.Items = append(section.Items, textComponents(activity.Comment.Text)...)
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
