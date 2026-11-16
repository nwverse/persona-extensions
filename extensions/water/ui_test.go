package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func request(action, state string, values map[string]string) *ext.UIRequest {
	return &ext.UIRequest{UIParams: wire.UIParams{Contribution: "water", Placement: "nav_page", Size: "full", UIVersion: 4, Action: action, State: state, Values: values}}
}

func TestPageLowDistractionAndRecentTotals(t *testing.T) {
	srv := fakeServer(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"enabled":true}`) })
	defer srv.Close()
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub(srv.URL, "test-token", []string{"notify"}), now)
	a.s.Records = []record{{ID: "today", At: now, ML: 750}, {ID: "yesterday", At: now.AddDate(0, 0, -1), ML: 2000}, {ID: "older", At: now.AddDate(0, 0, -10), ML: 2100}}
	for _, days := range []int{7, 30} {
		v, err := a.render(context.Background(), request("", fmt.Sprintf(`{"days":%d}`, days), nil))
		if err != nil {
			t.Fatal(err)
		}
		if err := wire.CheckView(v); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(v)
		if !strings.Contains(string(raw), "低打扰模式开着：提醒会攒进简报，不会响") {
			t.Fatal("missing low-distraction warning")
		}
		progress := v.Sections[0].Items[0]
		if *progress.Ratio != 0.375 || progress.Detail != "750 / 2000 毫升 · 还差约 5 杯" {
			t.Fatalf("progress %#v", progress)
		}
		recent := v.Sections[3].Items
		points := recent[1].Series[0].Points
		if len(points) != days || points[days-1].Value != 750 || points[days-2].Value != 2000 || recent[1].Unit != "毫升" || recent[1].Kind != "bar" {
			t.Fatal("chart totals")
		}
		want := "目标 2000 毫升 · 达标 1 天"
		if days == 30 {
			want = "目标 2000 毫升 · 达标 2 天"
		}
		if recent[2].Text != want {
			t.Fatal(recent[2].Text)
		}
	}
}

func TestPageStatusMessages(t *testing.T) {
	for _, tc := range []struct {
		name, now, want string
		change          func(*config, *state)
	}{
		{"next", "2026-11-16T08:00:00Z", "下次提醒 09:00", nil},
		{"closed", "2026-11-16T22:00:00Z", "今天的提醒时间过了，明天 09:00 开始", nil},
		{"weekend", "2026-11-14T10:00:00Z", "周末不提醒，周一 09:00 开始", func(c *config, s *state) { c.Weekends = false }},
		{"off", "2026-11-16T10:00:00Z", "提醒已关闭，可在扩展设置里打开", func(c *config, s *state) { c.Remind = false }},
		{"done", "2026-11-16T10:00:00Z", "今天不再提醒，已达标", func(c *config, s *state) { s.Records = []record{{At: instant("2026-11-16T10:00:00Z"), ML: 2000}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := instant(tc.now)
			a := testApp(t, defaults(), newHub("", "", nil), now)
			if tc.change != nil {
				tc.change(&a.cfg, &a.s)
			}
			if got := a.status(now); got != tc.want {
				t.Fatalf("%s", got)
			}
		})
	}
}
func mustAction(t *testing.T, a *app, action, state string, values map[string]string) *wire.ActionResult {
	t.Helper()
	res, err := a.action(context.Background(), request(action, state, values))
	if err != nil {
		t.Fatal(err)
	}
	if res.View == nil {
		t.Fatal("no updated view")
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestLogParsing(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"300", 300}, {"300ml", 300}, {"300 毫升", 300}, {" 300 ML ", 300}, {"1", 1}, {"3000", 3000},
		{"0", 0}, {"3001", 0}, {"-300", 0}, {"1.5", 0}, {"", 0}, {"999999999999999999999999", 0}, {"abc300", 0}, {"300 400", 0},
	} {
		t.Run(tc.in, func(t *testing.T) {
			n, ok := amount(tc.in)
			if ok != (tc.want > 0) || ok && n != tc.want {
				t.Fatalf("amount %d %v", n, ok)
			}
			a := testApp(t, defaults(), newHub("", "", nil), instant("2026-11-16T10:00:00Z"))
			res := mustAction(t, a, "log", "", map[string]string{"text": tc.in})
			if tc.want == 0 {
				if len(a.s.Records) != 0 || !strings.Contains(res.Toast, "1–3000") {
					t.Fatalf("invalid logged %s", res.Toast)
				}
			} else if len(a.s.Records) != 1 || a.s.Records[0].ML != tc.want {
				t.Fatal("wrong log")
			}
		})
	}
}

func TestActions(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	a.s.Snooze = now.Add(time.Hour)
	a.s.Pending = &pending{}
	res := mustAction(t, a, "drink", "", nil)
	if res.Toast != "已记 250 毫升，今天 250 / 2000 毫升" || !a.s.Snooze.IsZero() || a.s.Pending != nil {
		t.Fatalf("drink %s", res.Toast)
	}
	res = mustAction(t, a, "log", res.View.State, map[string]string{"amount": "300ml"})
	if total(a.s, now, time.UTC) != 550 {
		t.Fatal("page amount not logged")
	}
	res = mustAction(t, a, "range", res.View.State, map[string]string{"days": "30"})
	if readPage(res.View.State).Days != 30 {
		t.Fatal("range lost")
	}
	res = mustAction(t, a, "drink", res.View.State, nil)
	if readPage(res.View.State).Days != 30 {
		t.Fatal("range not preserved")
	}
	res = mustAction(t, a, "snooze", res.View.State, nil)
	if !a.s.Snooze.Equal(now.Add(30*time.Minute)) || res.Toast != "好的，30 分钟后再提醒你" {
		t.Fatalf("snooze %s", res.Toast)
	}
	id := a.s.Records[0].ID
	res = mustAction(t, a, "undo", res.View.State, map[string]string{"id": id})
	if res.Toast != "已撤销这条记录" || len(a.s.Records) != 2 {
		t.Fatal("undo failed")
	}
	res = mustAction(t, a, "undo", res.View.State, map[string]string{"id": id})
	if res.Toast != "这条记录已找不到了" {
		t.Fatal("missing undo")
	}
	res = mustAction(t, a, "undo:"+a.s.Records[0].ID, res.View.State, nil)
	if len(a.s.Records) != 1 {
		t.Fatal("old client undo failed")
	}
	// Yesterday's records cannot be removed through today's action.
	a.s.Records = append(a.s.Records, record{ID: "yesterday", At: now.AddDate(0, 0, -1), ML: 100})
	mustAction(t, a, "undo", "", map[string]string{"id": "yesterday"})
	if len(a.s.Records) != 2 {
		t.Fatal("deleted yesterday")
	}
}

func TestNotificationDedupAndExpiry(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	token := notification(a.cfg, a.s, now).State
	for i := 0; i < 2; i++ {
		mustAction(t, a, "drink", token, nil)
	}
	if len(a.s.Records) != 1 {
		t.Fatal("duplicate drink")
	}
	// A different action on the same reminder is independent, but also only applies once.
	for i := 0; i < 2; i++ {
		mustAction(t, a, "log", token, map[string]string{"text": "300"})
		mustAction(t, a, "snooze", token, nil)
	}
	if len(a.s.Records) != 2 || len(a.s.Seen) != 3 {
		t.Fatal("per action dedup failed")
	}
	// Persistence, including the dedup ledger, survives a fresh app instance.
	reopened, err := openApp(a.path, defaults(), a.hub, now)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = a.now
	mustAction(t, reopened, "drink", token, nil)
	if len(reopened.s.Records) != 2 {
		t.Fatal("dedup lost on restart")
	}
	for i := 0; i < 2; i++ {
		mustAction(t, a, "drink", "", nil)
	}
	if len(a.s.Records) != 4 {
		t.Fatal("page button deduplicated")
	}
	// Invalid log input does not consume the notification action.
	newToken := fmt.Sprintf("r=%d", now.Add(-time.Second).UnixMilli())
	mustAction(t, a, "log", newToken, map[string]string{"text": "bad"})
	mustAction(t, a, "log", newToken, map[string]string{"text": "400"})
	if len(a.s.Records) != 5 {
		t.Fatal("valid retry lost")
	}
	a.now = func() time.Time { return now.Add(24 * time.Hour) }
	res := mustAction(t, a, "drink", token, nil)
	if !strings.Contains(res.Toast, "过期") || len(a.s.Records) != 5 {
		t.Fatal("expired token accepted")
	}
}

func TestGoalSnoozeAndWarnings(t *testing.T) {
	now := instant("2026-11-16T20:45:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	res := mustAction(t, a, "snooze", "", nil)
	if !strings.Contains(res.Toast, "今天不再提醒") {
		t.Fatal(res.Toast)
	}
	for i := 0; i < 8; i++ {
		res = mustAction(t, a, "drink", "", nil)
	}
	if res.Toast != "今天的目标达成了，今天不再提醒" {
		t.Fatal(res.Toast)
	}
	progress := res.View.Sections[0].Items[0]
	if *progress.Ratio != 1 || progress.Tone != "positive" || progress.Detail != "目标达成" {
		t.Fatalf("progress %#v", progress)
	}
	a.cfg.Stop = false
	res = mustAction(t, a, "drink", "", nil)
	if res.Toast != "今天的目标达成了" {
		t.Fatal(res.Toast)
	}
	a.cfg.Start = "22:00"
	a.s.Reason = "no_device"
	v, err := a.render(context.Background(), request("", "", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	for _, want := range []string{"开始时间要早于结束时间", "caution", "没有通知权限", "暂时没有能收到通知的设备"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s", want)
		}
	}
}

func TestZoneLatestValidAndPersistence(t *testing.T) {
	now := instant("2026-11-16T00:30:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	for _, tc := range []struct{ tz, want, method string }{
		{"America/New_York", "America/New_York", "render"}, {"bad/zone", "America/New_York", "action"},
		{"", "America/New_York", "render"}, {"Asia/Tokyo", "Asia/Tokyo", "action"},
	} {
		r := request("drink", "", nil)
		r.TZ = tc.tz
		if tc.method == "render" {
			if _, err := a.render(context.Background(), r); err != nil {
				t.Fatal(err)
			}
		} else if _, err := a.action(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		s, err := load(a.path, now)
		if err != nil || s.Zone != tc.want {
			t.Fatalf("zone %q %v", s.Zone, err)
		}
	}
}

func TestLargeDayPaginationAndView(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	for i := 0; i < 100; i++ {
		a.s.Records = append(a.s.Records, record{ID: fmt.Sprint(i), At: now, ML: 1})
	}
	for _, version := range []int{1, 2, 4} {
		v := a.view(pageState{Days: 30}, version, now, nil)
		if err := wire.CheckView(v); err != nil {
			t.Fatal(err)
		}
		if len(v.Sections[2].Items) != 41 {
			t.Fatal("unbounded records")
		}
		res := mustAction(t, a, "older", v.State, nil)
		if readPage(res.View.State).Offset != 40 {
			t.Fatal("not paged")
		}
	}
}

func TestFailedSaveDoesNotApplyAction(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	a.path = filepath.Join(a.path, "child")
	if _, err := a.action(context.Background(), request("drink", "", nil)); err == nil {
		t.Fatal("reported success")
	}
	if len(a.s.Records) != 0 {
		t.Fatal("failed save changed state")
	}
}

func TestConcurrentActionsAndScheduler(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.action(context.Background(), request("drink", "", nil)); err != nil {
				t.Error(err)
			}
			if err := a.pass(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(a.s.Records) != 20 {
		t.Fatalf("lost records %d", len(a.s.Records))
	}
}

func TestProtocolAndManifest(t *testing.T) {
	h := exttest.Start(t, manifest, exttest.Serve(t, program()))
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Contributes.UI) != 2 || m.Contributes.UI[0].ID != "water" || len(h.Init.Contributes) != 1 {
		t.Fatal("contributions differ")
	}
	var ui any
	if err := json.Unmarshal(h.Init.Contributes["ui"], &ui); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m.Contributes.UI)
	var declared any
	json.Unmarshal(b, &declared)
	got, _ := json.Marshal(ui)
	want, _ := json.Marshal(declared)
	if string(got) != string(want) {
		t.Fatalf("contributions %s vs %s", got, want)
	}
	for _, version := range []int{1, 4} {
		r := request("", "", nil)
		r.UIVersion = version
		r.TZ = "Asia/Tokyo"
		var v wire.View
		if err := h.Call("ui.render", r.UIParams, &v); err != nil {
			t.Fatal(err)
		}
		if err := wire.CheckView(&v); err != nil {
			t.Fatal(err)
		}
		r.Action = "drink"
		r.State = v.State
		var res wire.ActionResult
		if err := h.Call("ui.action", r.UIParams, &res); err != nil {
			t.Fatal(err)
		}
		if res.View == nil || !strings.Contains(res.Toast, "已记 250 毫升") {
			t.Fatalf("action %#v", res)
		}
		if err := wire.CheckView(res.View); err != nil {
			t.Fatal(err)
		}
		// Simulate the old hub stripping unsupported components; every section still has content.
		if version < 2 {
			for _, s := range res.View.Sections {
				n := 0
				for _, c := range s.Items {
					if c.Type != "chart" && c.Type != "segmented" {
						n++
					}
				}
				if n == 0 {
					t.Fatal("empty old-client section")
				}
			}
		}
	}
}

func TestConformance(t *testing.T) { exttest.Check(t, manifest, exttest.Serve(t, program())) }

func TestRunStopsOnCancellation(t *testing.T) {
	a := testApp(t, defaults(), newHub("", "", nil), time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop")
	}
}

func TestManifestDefaults(t *testing.T) {
	b, err := os.ReadFile("persona-extension.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Configuration struct {
			Properties map[string]struct{ Default json.RawMessage }
		}
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	values := map[string]json.RawMessage{}
	for k, v := range m.Configuration.Properties {
		values[k] = v.Default
	}
	raw, _ := json.Marshal(values)
	var c config
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c != defaults() {
		t.Fatalf("default drift %#v", c)
	}
}
