package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type pageState struct {
	Mode    string `json:"m,omitempty"`
	Query   string `json:"q,omitempty"`
	Page    int    `json:"p,omitempty"`
	Slug    string `json:"s,omitempty"`
	DraftID string `json:"d,omitempty"`
	URL     string `json:"u,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.Page < 0 || len(st.Query) > 250 || len(st.Slug) > 260 || len(st.DraftID) > 128 || len(st.URL) > 2100 {
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
		st.Query, st.Page = strings.TrimSpace(clipText(r.Values["q"], 200)), 1
	case "more":
		st.Page, err = strconv.Atoi(r.Values["page"])
		if err != nil || st.Page < 2 {
			return nil, errors.New("invalid next page")
		}
	case "detail":
		if !validRecipeSlug(r.Values["slug"]) {
			return nil, errors.New("invalid recipe slug")
		}
		st.Mode, st.Slug = "detail", r.Values["slug"]
	case "back":
		st.Mode, st.Slug, st.DraftID, st.URL = "", "", "", ""
	case "import":
		if st.Mode == "review-import" {
			st.Mode = "import"
		} else {
			st = pageState{Mode: "import", DraftID: randomID()}
		}
	case "review-import":
		st.URL, err = publicImportURL(r.Values["url"])
		if err != nil {
			return nil, err
		}
		st.Mode = "review-import"
	case "confirm-import":
		clean, err := publicImportURL(st.URL)
		if err != nil || st.Mode != "review-import" || st.DraftID == "" {
			return nil, errors.New("a valid reviewed import URL is required")
		}
		raw, _ := json.Marshal(map[string]string{"url": clean})
		out, err := a.doAction(ctx, "ui-import:"+st.DraftID, "mealie_import_recipe", raw)
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
	if st.Mode == "import" || st.Mode == "review-import" {
		return importView(r, st), nil
	}
	if st.Mode == "detail" {
		item, err := a.client.recipe(ctx, st.Slug)
		a.record(err)
		if err != nil {
			return nil, err
		}
		return detailView(r, st, item), nil
	}
	if st.Mode != "" {
		return nil, errors.New("invalid recipe view")
	}
	if st.Page == 0 {
		st.Page = 1
	}
	page, err := a.client.recipes(ctx, st.Query, st.Page)
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.recipes, State: ext.EncodeState(st), Search: &wire.Search{Placeholder: t.search, Value: st.Query, Action: wire.Action{ID: "search"}}}
	section := wire.Section{Title: fmt.Sprintf(t.results, page.Total)}
	for _, item := range page.Items {
		if !validRecipeSlug(item.Slug) || item.Name == "" {
			continue
		}
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "recipe-" + item.Slug, Title: clipText(item.Name, 1800), Subtitle: clipText(fmt.Sprintf("%s · %g %s", item.TotalTime, item.RecipeServings, t.servings), 1800), Action: &wire.Action{ID: "detail", Values: map[string]string{"slug": item.Slug}}})
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noRecipes})
	}
	if page.Page < page.TotalPages {
		section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"page": fmt.Sprint(page.Page + 1)}}}
	}
	v.Sections = []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.importRecipe, Action: &wire.Action{ID: "import", Push: &wire.Push{Title: t.importRecipe, State: ext.EncodeState(pageState{Mode: "import", DraftID: randomID()})}}}}}}
	return v, nil
}

func importView(r *ext.UIRequest, st pageState) *wire.View {
	t := textsFor(r.Locale)
	if st.Mode == "review-import" {
		return &wire.View{Title: t.review, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.importRecipe, Items: []wire.Component{
			{Type: wire.ComponentText, Text: clipText(st.URL, 1800)},
			{Type: wire.ComponentText, Text: t.importNotice},
			{Type: wire.ComponentButton, Title: t.importRecipe, Action: &wire.Action{ID: "confirm-import", Confirm: t.importRecipe + "\n" + st.URL}},
			{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "import"}},
		}}}}
	}
	return &wire.View{Title: t.importRecipe, State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.importNotice, Items: []wire.Component{
		{Type: wire.ComponentField, Kind: "text", Name: "url", Label: t.url, Value: st.URL},
		{Type: wire.ComponentButton, Title: t.review, Action: &wire.Action{ID: "review-import", Submit: true}},
	}}}}
}

func detailView(r *ext.UIRequest, st pageState, item recipe) *wire.View {
	t := textsFor(r.Locale)
	items := []wire.Component{{Type: wire.ComponentText, Text: clipText(fmt.Sprintf("%s · %s · %g %s", item.TotalTime, item.PrepTime, item.RecipeServings, t.servings), 1800)}}
	if source := safeSource(item.OriginalURL); source != "" {
		items = append(items, wire.Component{Type: wire.ComponentLink, Title: t.source, URL: source})
	}
	items = append(items, wire.Component{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}})
	v := &wire.View{Title: clipText(item.Name, 1800), State: ext.EncodeState(st), Sections: []wire.Section{{Title: t.recipes, Items: items}}}
	if item.Description != "" {
		v.Sections = append(v.Sections, wire.Section{Title: t.description, Items: textComponents(clipText(item.Description, 12000))})
	}
	ingredients := wire.Section{Title: t.ingredients}
	for _, ingredient := range item.RecipeIngredient {
		if ingredient.Title != "" {
			ingredients.Items = append(ingredients.Items, wire.Component{Type: wire.ComponentText, Text: clipText(ingredient.Title, 1800)})
		}
		text := ingredient.Display
		if text == "" {
			text = ingredient.OriginalText
		}
		if text != "" {
			ingredients.Items = append(ingredients.Items, wire.Component{Type: wire.ComponentText, Text: clipText(text, 1800)})
		}
	}
	if len(ingredients.Items) == 0 {
		ingredients.Items = append(ingredients.Items, wire.Component{Type: wire.ComponentText, Text: t.noIngredients, Style: wire.TextStyleSecondary})
	}
	v.Sections = append(v.Sections, ingredients)
	steps := wire.Section{Title: t.instructions}
	for i, step := range item.RecipeInstructions {
		if step.Text != "" {
			steps.Items = append(steps.Items, wire.Component{Type: wire.ComponentText, Text: clipText(fmt.Sprintf("%d. %s", i+1, step.Text), 1800)})
		}
	}
	if len(steps.Items) == 0 {
		steps.Items = append(steps.Items, wire.Component{Type: wire.ComponentText, Text: t.noInstructions, Style: wire.TextStyleSecondary})
	}
	v.Sections = append(v.Sections, steps)
	return v
}

func safeSource(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
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
