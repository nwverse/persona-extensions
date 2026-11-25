package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"

	"github.com/nwverse/persona-extensions/extensions/token-usage/internal/budget"
	"github.com/nwverse/persona-extensions/extensions/token-usage/internal/hub"
	"github.com/nwverse/persona-extensions/extensions/token-usage/internal/store"
)

var loc = time.FixedZone("UTC+8", 8*3600)

func fixedNow() time.Time {
	t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-13 12:00", loc)
	return t
}

// filledStore has two machines, two models and a launcher over two days.
func filledStore() *store.Store {
	s := store.Open("")
	at := func(date string, hour int) int64 {
		t, _ := time.ParseInLocation("2006-01-02", date, loc)
		return t.Add(time.Duration(hour) * time.Hour).Unix()
	}
	_, _ = s.Merge("m-a", []store.Bucket{
		{Minute: at("2026-11-13", 9), Model: "alpha", Launcher: "cli", Input: 1000, Output: 100, Requests: 2},
		{Minute: at("2026-11-13", 10), Model: "beta", Input: 400, Requests: 1},
		{Minute: at("2026-11-12", 9), Model: "alpha", Launcher: "cli", Input: 300, Requests: 1},
	}, loc)
	_, _ = s.Merge("m-b", []store.Bucket{
		{Minute: at("2026-11-13", 11), Model: "alpha", Input: 200, Requests: 1},
	}, loc)
	return s
}

func newApp(t *testing.T, withGateway bool) *App {
	t.Helper()
	call := func(ctx context.Context, method string, out any) error {
		return errNoGateway
	}
	if withGateway {
		call = func(ctx context.Context, method string, out any) error {
			ratio := 0.4
			*(out.(*hub.GatewayAccounts)) = hub.GatewayAccounts{Accounts: []hub.GatewayAccount{
				{Provider: "demo", Name: "账号一", Status: "active", Windows: []hub.GatewayWindow{{Kind: "weekly", UsedRatio: &ratio}}},
			}}
			return nil
		}
	}
	bf := budget.Open("")
	if err := bf.Set(budget.Budget{Span: budget.Daily, Limit: 5000}); err != nil {
		t.Fatal(err)
	}
	return New(filledStore(), bf, hub.New("http://hub.example", "tok", call), fixedNow, func() *time.Location { return loc })
}

var errNoGateway = &noGatewayError{}

type noGatewayError struct{}

func (noGatewayError) Error() string { return "unknown method" }

func render(t *testing.T, a *App, state string) *wire.View {
	t.Helper()
	v, err := a.Page(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{State: state}})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatalf("view rejected: %v", err)
	}
	return v
}

func TestOverviewShowsTotalsTrendAndRankings(t *testing.T) {
	a := newApp(t, true)
	v := render(t, a, "")
	if v.Tabs == nil || len(v.Tabs.Items) != 3 {
		t.Fatal("the page has no three tabs")
	}
	text := flatten(v)
	if !strings.Contains(text, "1.7K") { // today: 1000+100+400+200
		t.Fatalf("today's total missing from:\n%s", text)
	}
	if !strings.Contains(text, "alpha") || !strings.Contains(text, "m-a") {
		t.Fatalf("rankings missing from:\n%s", text)
	}
	if !strings.Contains(text, "其他") { // beta has no launcher
		t.Fatalf("the empty launcher is not shown as 其他:\n%s", text)
	}
	if !strings.Contains(text, "账号一") {
		t.Fatalf("the gateway accounts are missing:\n%s", text)
	}
}

func TestGatewaySectionHiddenWhenUnavailable(t *testing.T) {
	v := render(t, newApp(t, false), "")
	if strings.Contains(flatten(v), "网关账号额度") {
		t.Fatal("the gateway section shows although the hub has no such method")
	}
}

func TestDetailFiltersAndExports(t *testing.T) {
	a := newApp(t, false)
	v := render(t, a, `{"tab":"detail","model":"alpha"}`)
	text := flatten(v)
	if strings.Contains(text, "beta") {
		t.Fatalf("the model filter did not apply:\n%s", text)
	}
	res, err := a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "export", State: `{"tab":"detail","days":7,"model":"beta"}`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Toast == "" {
		t.Fatal("the export answered nothing")
	}
}

func TestBudgetSetAndDelete(t *testing.T) {
	a := newApp(t, false)
	res, err := a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "budget-set", Values: map[string]string{"model": "alpha", "span": "week", "limit": "100000"},
	}})
	if err != nil || !strings.Contains(res.Toast, "已保存") {
		t.Fatalf("set: %+v %v", res, err)
	}
	v := render(t, a, `{"tab":"budget"}`)
	if !strings.Contains(flatten(v), "alpha") {
		t.Fatal("the saved budget is not on the page")
	}
	res, err = a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "budget-delete", Values: map[string]string{"span": "week", "model": "alpha"},
	}})
	if err != nil || !strings.Contains(res.Toast, "已删除") {
		t.Fatalf("delete: %+v %v", res, err)
	}
}

func TestCardShowsTodayAgainstTheWeek(t *testing.T) {
	a := newApp(t, false)
	v, err := a.Card(&ext.UIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatalf("card rejected: %v", err)
	}
	text := flatten(v)
	if !strings.Contains(text, "1.7K") || !strings.Contains(text, "alpha") {
		t.Fatalf("card:\n%s", text)
	}
}

func TestEmptyStateStillDraws(t *testing.T) {
	a := New(store.Open(""), budget.Open(""), hub.New("", "", nil), fixedNow, func() *time.Location { return loc })
	for _, state := range []string{"", `{"tab":"detail"}`, `{"tab":"budget"}`} {
		render(t, a, state)
	}
	if _, err := a.Card(&ext.UIRequest{}); err != nil {
		t.Fatal(err)
	}
}

// flatten is every string of a view, for assertions that do not care where a number sits.
func flatten(v *wire.View) string {
	var b strings.Builder
	for _, s := range v.Sections {
		b.WriteString(s.Title + "\n")
		for _, it := range s.Items {
			b.WriteString(strings.Join([]string{it.Text, it.Label, it.Value, it.Detail, it.Title}, " ") + "\n")
			for _, st := range it.Stats {
				b.WriteString(st.Label + " " + st.Value + " " + st.Detail + "\n")
			}
			for _, row := range it.Rows {
				b.WriteString(strings.Join(row, " ") + "\n")
			}
		}
	}
	return b.String()
}

func TestActReturnsUpdatedViewsForNavigationFiltersAndBudgets(t *testing.T) {
	a := newApp(t, false)

	// Switch to detail tab.
	res, err := a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "tab", Values: map[string]string{"tab": tabDetail},
	}})
	if err != nil {
		t.Fatalf("tab action error: %v", err)
	}
	if res.View == nil {
		t.Fatalf("tab action returned nil View; state change would be discarded by SDK")
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatalf("detail view invalid: %v", err)
	}
	if res.View.Tabs == nil || res.View.Tabs.Value != tabDetail {
		t.Fatalf("tab view Tabs.Value = %#v, want %q", res.View.Tabs, tabDetail)
	}

	// Change range on detail tab -> preserves tab and updates days.
	res, err = a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "range", State: res.View.State, Values: map[string]string{"days": "7"},
	}})
	if err != nil || res.View == nil {
		t.Fatalf("range action = %#v, %v", res, err)
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatalf("range view invalid: %v", err)
	}
	var ps pageState
	if err := (&ext.UIRequest{UIParams: wire.UIParams{State: res.View.State}}).DecodeState(&ps); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if ps.Tab != tabDetail || ps.Days != 7 {
		t.Fatalf("after range: state = %+v, want tab=detail days=7", ps)
	}

	// Filter updates keep previously set filter dimensions when only one key is sent.
	res, err = a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "filter", State: res.View.State, Values: map[string]string{"model": "alpha"},
	}})
	if err != nil || res.View == nil {
		t.Fatalf("filter model = %#v, %v", res, err)
	}
	res, err = a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "filter", State: res.View.State, Values: map[string]string{"machine": "m-a"},
	}})
	if err != nil || res.View == nil {
		t.Fatalf("filter machine = %#v, %v", res, err)
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatalf("filtered view invalid: %v", err)
	}
	ps = pageState{}
	_ = (&ext.UIRequest{UIParams: wire.UIParams{State: res.View.State}}).DecodeState(&ps)
	if ps.Model != "alpha" || ps.Machine != "m-a" {
		t.Fatalf("after filters: state = %+v, want model=alpha machine=m-a", ps)
	}

	// Switch budget form span to weekly, then save without repeating span in submit values.
	res, err = a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "budget-span", State: `{"tab":"budget"}`, Values: map[string]string{"span": budget.Weekly},
	}})
	if err != nil || res.View == nil {
		t.Fatalf("budget-span = %#v, %v", res, err)
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatalf("budget-span view invalid: %v", err)
	}
	foundWeekly := false
	for _, it := range res.View.Sections[1].Items {
		if it.Type == wire.ComponentSegmented && it.Name == "span" && it.Value == budget.Weekly {
			foundWeekly = true
		}
	}
	if !foundWeekly {
		t.Fatalf("budget-span did not update segmented control value to %q", budget.Weekly)
	}

	res, err = a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "budget-set", State: res.View.State, Values: map[string]string{"model": "alpha", "limit": "50000"},
	}})
	if err != nil || res.View == nil {
		t.Fatalf("budget-set must return refreshed View, got res=%#v err=%v", res, err)
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatalf("budget-set view invalid: %v", err)
	}
	if !strings.Contains(flatten(res.View), "每周 · alpha") {
		t.Fatalf("budget-set refreshed view missing saved weekly alpha budget:\n%s", flatten(res.View))
	}

	// Delete budget returns refreshed View without deleted budget.
	res, err = a.Act(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		Action: "budget-delete", State: res.View.State, Values: map[string]string{"span": budget.Weekly, "model": "alpha"},
	}})
	if err != nil || res.View == nil {
		t.Fatalf("budget-delete must return refreshed View, got res=%#v err=%v", res, err)
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatalf("budget-delete view invalid: %v", err)
	}
	if strings.Contains(flatten(res.View), "每周 · alpha") {
		t.Fatalf("budget-delete refreshed view still shows deleted budget:\n%s", flatten(res.View))
	}
}

func TestOverviewHandlesEmptyModelName(t *testing.T) {
	s := store.Open("")
	t0, _ := time.ParseInLocation("2006-01-02 15", "2026-11-13 09", loc)
	_, _ = s.Merge("m-a", []store.Bucket{
		{Minute: t0.Unix(), Model: "", Input: 500, Requests: 1},
	}, loc)
	a := New(s, budget.Open(""), hub.New("", "", nil), fixedNow, func() *time.Location { return loc })
	v := render(t, a, "")
	if !strings.Contains(flatten(v), "（未命名）") {
		t.Fatalf("empty model should render as （未命名）:\n%s", flatten(v))
	}
}
