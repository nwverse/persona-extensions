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
	Mode       string `json:"m,omitempty"`
	StartDate  string `json:"s,omitempty"`
	EndDate    string `json:"e,omitempty"`
	CategoryID int64  `json:"c,omitempty"`
	Offset     int    `json:"o,omitempty"`
	Kind       string `json:"k,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.Offset < 0 || st.CategoryID < 0 || len(st.StartDate) > 20 || len(st.EndDate) > 20 || len(st.Mode) > 30 || len(st.Kind) > 30 {
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
	case "filter":
		st.StartDate, st.EndDate, st.Offset = strings.TrimSpace(r.Values["start_date"]), strings.TrimSpace(r.Values["end_date"]), 0
		st.CategoryID, err = strconv.ParseInt(strings.TrimSpace(r.Values["category_id"]), 10, 64)
		if r.Values["category_id"] == "" {
			st.CategoryID, err = 0, nil
		}
		if err != nil || st.CategoryID < 0 {
			return nil, errors.New("invalid category ID")
		}
	case "more":
		st.Offset, err = strconv.Atoi(r.Values["offset"])
		if err != nil || st.Offset < 50 {
			return nil, errors.New("invalid next offset")
		}
	case "categories":
		st = pageState{Mode: "categories"}
	case "accounts":
		if r.Values["kind"] != "manual_accounts" && r.Values["kind"] != "plaid_accounts" {
			return nil, errors.New("invalid account type")
		}
		st = pageState{Mode: "accounts", Kind: r.Values["kind"]}
	case "back":
		st.Mode, st.Kind = "", ""
	default:
		return nil, fmt.Errorf("unknown page action %q", r.Action)
	}
	v, err := a.page(ctx, r, st)
	return &wire.ActionResult{View: v}, err
}

func (a *app) page(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if a.client.token == "" {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingConfig}}}}}, nil
	}
	if st.Mode == "categories" {
		items, err := a.client.categories(ctx)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.categories}
		for _, item := range items {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: clipText(fmt.Sprintf("%s · %d", item.Name, item.ID), 1800)})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noCategories})
		}
		return &wire.View{Title: t.categories, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode == "accounts" {
		items, err := a.client.accounts(ctx, st.Kind)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.accounts}
		for _, item := range items {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: clipText(fmt.Sprintf("%s · %s %s · %s", item.Name, item.Balance, item.Currency, item.BalanceAsOf), 1800)})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noAccounts})
		}
		return &wire.View{Title: t.accounts, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode != "" {
		return nil, errors.New("invalid finance view")
	}
	page, err := a.client.transactions(ctx, st.StartDate, st.EndDate, st.CategoryID, st.Offset)
	a.record(err)
	if err != nil {
		return nil, err
	}
	v := &wire.View{Title: t.finance, State: ext.EncodeState(st)}
	section := wire.Section{Title: t.transactions}
	for _, item := range page.Transactions {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: clipText(fmt.Sprintf("%s · %s %s · %s", item.Payee, item.Amount, item.Currency, item.Date), 1800)})
		if item.Notes != "" {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentText, Text: clipText(item.Notes, 1800), Style: wire.TextStyleSecondary})
		}
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noTransactions})
	}
	if page.HasMore {
		section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"offset": fmt.Sprint(st.Offset + 50)}}}
	}
	v.Sections = []wire.Section{{Title: t.filter, Items: []wire.Component{
		{Type: wire.ComponentField, Kind: "text", Name: "start_date", Label: t.start, Value: st.StartDate},
		{Type: wire.ComponentField, Kind: "text", Name: "end_date", Label: t.end, Value: st.EndDate},
		{Type: wire.ComponentField, Kind: "text", Name: "category_id", Label: t.category, Value: categoryText(st.CategoryID)},
		{Type: wire.ComponentButton, Title: t.apply, Action: &wire.Action{ID: "filter", Submit: true}},
		{Type: wire.ComponentButton, Title: t.categories, Action: &wire.Action{ID: "categories", Push: &wire.Push{Title: t.categories, State: ext.EncodeState(pageState{Mode: "categories"})}}},
	}}, section, {Title: t.accounts, Items: []wire.Component{
		{Type: wire.ComponentButton, Title: t.manual, Action: &wire.Action{ID: "accounts", Values: map[string]string{"kind": "manual_accounts"}, Push: &wire.Push{Title: t.manual, State: ext.EncodeState(pageState{Mode: "accounts", Kind: "manual_accounts"})}}},
		{Type: wire.ComponentButton, Title: t.synced, Action: &wire.Action{ID: "accounts", Values: map[string]string{"kind": "plaid_accounts"}, Push: &wire.Push{Title: t.synced, State: ext.EncodeState(pageState{Mode: "accounts", Kind: "plaid_accounts"})}}},
	}}}
	return v, nil
}

func categoryText(id int64) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprint(id)
}
