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
	Mode     string `json:"m,omitempty"`
	View     string `json:"v,omitempty"`
	Query    string `json:"q,omitempty"`
	Cursor   string `json:"c,omitempty"`
	AssetID  string `json:"i,omitempty"`
	DraftID  string `json:"d,omitempty"`
	Favorite bool   `json:"f,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || len(st.Query) > 250 || len(st.Cursor) > 4096 || len(st.AssetID) > 128 || len(st.DraftID) > 128 || len(st.View) > 30 || len(st.Mode) > 30 {
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
		if r.Values["view"] != "favorites" && r.Values["view"] != "search" && r.Values["view"] != "smart" {
			return nil, errors.New("invalid library view")
		}
		st.View, st.Cursor = r.Values["view"], ""
	case "search":
		st.Query, st.Cursor = strings.TrimSpace(clipText(r.Values["q"], 200)), ""
		if st.Query == "" {
			st.View = "favorites"
		} else if st.View != "smart" {
			st.View = "search"
		}
	case "more":
		if r.Values["cursor"] == "" || r.Values["cursor"] != st.Cursor {
			return nil, errors.New("invalid next cursor")
		}
	case "detail":
		if !validAssetID(r.Values["id"]) {
			return nil, errors.New("invalid asset ID")
		}
		st.Mode, st.AssetID = "detail", r.Values["id"]
	case "back":
		st.Mode, st.AssetID, st.DraftID = "", "", ""
	case "favorite":
		if st.Mode != "detail" || !validAssetID(st.AssetID) || st.DraftID == "" {
			return nil, errors.New("an asset favorite draft is required")
		}
		favorite, err := strconv.ParseBool(r.Values["favorite"])
		if err != nil {
			return nil, errors.New("invalid favorite value")
		}
		raw, _ := json.Marshal(favoriteArgs{AssetID: st.AssetID, Favorite: favorite})
		out, err := a.doAction(ctx, "ui-favorite:"+st.DraftID, "immich_set_favorite", raw)
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
	if st.Mode == "detail" {
		item, err := a.client.asset(ctx, st.AssetID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		st.DraftID = randomID()
		return detailView(r, st, item), nil
	}
	if st.Mode != "" {
		return nil, errors.New("invalid library view")
	}
	if st.View == "" {
		st.View = "favorites"
	}
	var page searchResponse
	var err error
	if st.View == "smart" {
		page, err = a.client.smartSearch(ctx, st.Query)
	} else {
		page, err = a.client.search(ctx, st.Query, st.View == "favorites", st.Cursor)
	}
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.library, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}, Filter: &wire.Filter{Name: "view", Title: t.library, Value: st.View, Action: wire.Action{ID: "view"}, Choices: []wire.Choice{{Value: "favorites", Title: t.favorites}, {Value: "search", Title: t.filename}, {Value: "smart", Title: t.smart}}}}
	section := wire.Section{Title: t.results}
	for _, item := range page.Assets.Items {
		if !validAssetID(item.ID) || item.OriginalFileName == "" || item.IsTrashed {
			continue
		}
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "asset-" + item.ID, Title: clipText(item.OriginalFileName, 1800), Subtitle: clipText(item.Type+" · "+item.FileCreatedAt, 1800), Action: &wire.Action{ID: "detail", Values: map[string]string{"id": item.ID}}})
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noAssets})
	}
	if st.View != "smart" && page.Assets.NextCursor != nil && *page.Assets.NextCursor != "" {
		st.Cursor = *page.Assets.NextCursor
		v.State = ext.EncodeState(st)
		section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"cursor": st.Cursor}}}
	}
	v.Sections = []wire.Section{section}
	return v, nil
}

func detailView(r *ext.UIRequest, st pageState, item asset) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{{Type: wire.ComponentText, Text: clipText(item.Type+" · "+item.FileCreatedAt, 1800)}}
	if item.Exif.City != "" || item.Exif.Country != "" {
		items = append(items, wire.Component{Type: wire.ComponentText, Text: clipText(item.Exif.City+", "+item.Exif.Country, 1800)})
	}
	if item.Exif.Make != "" || item.Exif.Model != "" {
		items = append(items, wire.Component{Type: wire.ComponentText, Text: clipText(item.Exif.Make+" "+item.Exif.Model, 1800)})
	}
	title := t.favorite
	favorite := true
	if item.IsFavorite {
		title, favorite = t.unfavorite, false
	}
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: title, Action: &wire.Action{ID: "favorite", Values: map[string]string{"favorite": strconv.FormatBool(favorite)}, Confirm: title + "\n" + item.OriginalFileName}}, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}})
	v := &wire.View{Title: clipText(item.OriginalFileName, 1800), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.asset, Items: items}}}
	if item.Exif.Description != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.description, Items: textComponents(clipText(item.Exif.Description, 12000))})
	}
	var people []string
	for _, person := range item.People {
		if person.Name != "" {
			people = append(people, person.Name)
		}
	}
	if len(people) > 0 {
		v.Sections = append(v.Sections, wire.Section{Title: t.people, Items: []wire.Component{{Type: wire.ComponentText, Text: clipText(strings.Join(people, ", "), 1800)}}})
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
