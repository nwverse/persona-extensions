package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/curate"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/kbsrc"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/store"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// within runs f and fails the test unless it answers at once: a page or action that waits for the run in progress
// is a 502 on the hub after 20 seconds.
func within(t *testing.T, what string, f func() (any, error)) string {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		v, err := f()
		if err != nil {
			done <- "error: " + err.Error()
			return
		}
		b, _ := json.Marshal(v)
		done <- string(b)
	}()
	select {
	case s := <-done:
		return s
	case <-time.After(3 * time.Second):
		t.Fatalf("%s waits for the run in progress", what)
		return ""
	}
}

func uiReq(s page, action string) *ext.UIRequest {
	return &ext.UIRequest{UIParams: wire.UIParams{State: ext.EncodeState(s), Action: action}}
}

// The main page (every tab and what it opens) and the overview card read the snapshot: they open while a daily pass
// holds the state, with what the last run left and the run's progress. Changes wait for the end of the run, said at
// once; after it, a change is on the next page drawn.
func TestMainPageAndCardDuringARun(t *testing.T) {
	fx, err := kbsrc.LoadFixture("testdata/sample")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &gated{waiting: make(chan struct{}, 1)}
	cfg := curate.Defaults()
	cfg.AdmitAllBelowTokens = 0
	a := &app{c: &curate.Curator{Src: fx, Model: g, St: st, Cfg: cfg, Loc: time.UTC, Live: &curate.Live{}}}
	ctx := context.Background()
	if _, err := a.c.RunDaily(ctx, "2026-11-03", false); err != nil {
		t.Fatal(err)
	}
	snap, done := a.read()
	done()
	var prop, item, run string
	for _, p := range snap.Proposals {
		if p.Status == "pending" {
			prop = p.ID
		}
	}
	for id := range snap.Items {
		item = id
	}
	run = snap.Runs[len(snap.Runs)-1].ID
	if prop == "" || item == "" {
		t.Fatalf("the first run left no proposal (%q) or item (%q)", prop, item)
	}

	hold := make(chan struct{})
	g.mu.Lock()
	g.hold = hold
	g.mu.Unlock()
	if !a.start(ctx, "daily", "2026-11-04") {
		t.Fatal("the run did not start")
	}
	select {
	case <-g.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never reached a daily call")
	}
	home := func(s page) func() (any, error) { return func() (any, error) { return a.home(ctx, uiReq(s, "")) } }
	review := within(t, "the review tab", home(page{Tab: tabReview}))
	for _, s := range []string{"正在整理：日级 2026-11-04", "下面是上次整理完的结果", prop} {
		if !strings.Contains(review, s) {
			t.Errorf("the review tab does not show %q: %s", s, review)
		}
	}
	for _, tab := range []string{"memory", "fact", "practice", "knowledge", "graph", "reports", tabStart, tabFacets, tabPeople, tabCheck, tabSwitches, tabOlder} {
		within(t, "the "+tab+" tab", home(page{Tab: tab}))
	}
	if p := within(t, "a proposal", home(page{Tab: "review", Open: prop})); !strings.Contains(p, prop) {
		t.Errorf("the proposal page: %s", p)
	}
	within(t, "an item", home(page{Tab: "fact", Open: item}))
	within(t, "a node", home(page{Tab: "graph", Open: "me"}))
	if p := within(t, "a report", home(page{Tab: "reports", Open: run})); !strings.Contains(p, "日级整理 2026-11-03") {
		t.Errorf("the report page: %s", p)
	}
	if p := within(t, "the overview card", func() (any, error) { return a.today(ctx, &ext.UIRequest{}) }); !strings.Contains(p, "2026-11-03") {
		t.Errorf("the overview card does not show the last daily run: %s", p)
	}
	busy := within(t, "a change", func() (any, error) { return a.homeAction(ctx, uiReq(page{Tab: "review", Open: prop}, "later")) })
	if !strings.Contains(busy, "跑完再改") {
		t.Errorf("a change during a run: %s", busy)
	}
	within(t, "moving to another tab", func() (any, error) { return a.homeAction(ctx, uiReq(page{}, "tab")) })

	g.mu.Lock()
	g.hold = nil
	g.mu.Unlock()
	close(hold)
	deadline := time.Now().Add(10 * time.Second)
	for {
		a.mu.Lock()
		running := a.running
		a.mu.Unlock()
		if running == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p := within(t, "the overview card", func() (any, error) { return a.today(ctx, &ext.UIRequest{}) }); !strings.Contains(p, "2026-11-04") {
		t.Errorf("after the run the card is not the new snapshot: %s", p)
	}
	st.Lock()
	if p := st.S.Proposal(prop); p == nil || p.Status != "pending" {
		st.Unlock()
		t.Fatalf("the change refused during the run was made: %+v", p)
	} else if p.Shown == "" {
		st.Unlock()
		t.Fatalf("proposal %s displayed during the run was not marked shown before the run finished", prop)
	}
	st.Unlock()
	res := within(t, "a change", func() (any, error) { return a.homeAction(ctx, uiReq(page{Tab: "review", Open: prop}, "later")) })
	if !strings.Contains(res, "稍后再看") || !strings.Contains(res, "稍后") {
		t.Errorf("a change after the run: %s", res)
	}
	snap, done = a.read()
	done()
	if p := snap.Proposal(prop); p == nil || p.Status != "later" {
		t.Fatalf("the snapshot after the change: %+v", p)
	}
}

func TestReviewTabMarksShownWhenLockHeldOutsideRun(t *testing.T) {
	a := v4App(t)
	a.c.St.Lock()
	locked := true
	defer func() {
		if locked {
			a.c.St.Unlock()
		}
		a.shownWG.Wait()
	}()
	a.c.St.S.Proposals = append(a.c.St.S.Proposals, &store.Proposal{
		ID:     "K-0001",
		Kind:   "pin_fact",
		Title:  "Synthetic proposal for project-x",
		Risk:   "low",
		Status: "pending",
	})
	a.c.Publish()
	// Keep the store lock held while no curation run owns a.running (e.g. startup recovery/migration).
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		within(t, "review tab while store locked", func() (any, error) {
			return a.home(ctx, uiReq(page{Tab: tabReview}, ""))
		})
	}
	a.mu.Lock()
	flushing := a.flushingShown
	queued := len(a.pendingShown)
	a.mu.Unlock()
	if !flushing {
		t.Fatal("expected single deduplicated background flusher while store lock is held outside a run")
	}
	// The worker may already have popped K-0001 and be waiting on c.St.Lock(), so pendingShown holds 0 or 1 entry.
	if queued > 1 {
		t.Fatalf("expected deduplicated pendingShown queue, got %d entries", queued)
	}
	locked = false
	a.c.St.Unlock()
	a.shownWG.Wait()

	reopened, err := store.Open(a.c.St.Dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if p := reopened.S.Proposal("K-0001"); p == nil || p.Shown == "" {
		t.Fatalf("proposal K-0001 was not marked shown after store lock released: %+v", p)
	}
}

// The overview card counts the entries per facet that may go out (the exits' gate), under the plugin page's facet
// names, beside the pending proposals; the old items' 事实/做事方式/知识 are gone.
func TestOverviewCardCountsEntries(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id string, f entry.Facet, status entry.Status) *entry.Entry {
		return &entry.Entry{ID: id, Facet: f, Grain: entry.Class, Text: "占位 " + id, Status: status, Origin: entry.Behavior, Count: 2}
	}
	st.S.Entries = map[string]*entry.Entry{}
	for _, e := range []*entry.Entry{
		mk("w-1", entry.Workflow, entry.Stable), mk("w-2", entry.Workflow, entry.Stable), mk("w-3", entry.Workflow, entry.Candidate),
		mk("o-1", entry.World, entry.Stable), mk("i-1", entry.Interaction, entry.Stable), mk("p-1", entry.People, entry.Stable),
	} {
		st.S.Entries[e.ID] = e
	}
	wrong := mk("i-2", entry.Interaction, entry.Stable)
	wrong.Verdicts = []entry.Verdict{{Mark: "wrong", By: "user"}}
	st.S.Entries[wrong.ID] = wrong
	st.S.Proposals = []*store.Proposal{{ID: "p1", Status: "pending"}}
	a := &app{c: &curate.Curator{St: st, Cfg: curate.Defaults(), Loc: time.UTC}}
	v, err := a.today(t.Context(), &ext.UIRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	var labels []string
	for _, c := range v.Sections[0].Items {
		for _, s := range c.Stats {
			got[s.Label] = s.Value
			labels = append(labels, s.Label)
		}
	}
	want := map[string]string{"待确认": "1", "① 和 AI 的交互": "1", "② AI 和工具": "0", "③ 默认判断": "0", "④ 处理方式": "2", "最近一次日级": "还没运行过"}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q (card %v)", k, got[k], w, got)
		}
	}
	if len(labels) != 8 || labels[0] != "① 和 AI 的交互" || labels[5] != "⑥ 关于你" || labels[6] != "待确认" {
		t.Errorf("labels %v", labels)
	}
	for _, old := range []string{"事实", "做事方式", "知识"} {
		if _, ok := got[old]; ok {
			t.Errorf("old label %q still there", old)
		}
	}
}
