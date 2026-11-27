package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func widgetRequest(family string) wire.UIParams {
	return wire.UIParams{Contribution: widgetID, Placement: wire.PlacementWidget, Size: family, UIVersion: 4}
}

func TestWidgetFamiliesProtocol(t *testing.T) {
	families := []string{"small", "medium", "accessory_circular", "accessory_rectangular", "accessory_inline"}
	for _, n := range []int{0, 750, 2250} {
		for _, family := range families {
			t.Run(fmt.Sprintf("%s/%d", family, n), func(t *testing.T) {
				now := time.Now().UTC()
				s := state{Zone: "UTC", Sequence: 2, Records: []record{{ID: "yesterday", At: now.AddDate(0, 0, -1), ML: 1500}}}
				if n > 0 {
					s.Records = append(s.Records, record{ID: "today", At: now, ML: n})
				}
				h := exttest.StartWith(t, manifest, exttest.Serve(t, program()), func(p *wire.InitializeParams) {
					if err := save(filepath.Join(p.Extension.DataDir, "water.json"), s); err != nil {
						t.Fatal(err)
					}
				})
				var v wire.View
				if err := h.Call("ui.render", widgetRequest(family), &v); err != nil {
					t.Fatal(err)
				}
				if err := wire.CheckView(&v); err != nil {
					t.Fatal(err)
				}
				if len(v.Sections) != 1 || v.RefreshAfter != 0 {
					t.Fatalf("widget envelope: %+v", v)
				}
				items := v.Sections[0].Items
				wantTypes := "progress"
				switch family {
				case "small":
					wantTypes = "metric,progress"
				case "medium":
					wantTypes = "metric,progress,chart"
				case "accessory_inline":
					wantTypes = "text"
				}
				var types []string
				for _, item := range items {
					types = append(types, item.Type)
					if item.Action != nil {
						t.Fatal("widget contains an action")
					}
					if item.Type != "chart" {
						wantTone := ""
						if n >= 2000 {
							wantTone = "positive"
						}
						if item.Tone != wantTone {
							t.Fatalf("tone %q, want %q", item.Tone, wantTone)
						}
					}
				}
				if strings.Join(types, ",") != wantTypes {
					t.Fatalf("types %v, want %s", types, wantTypes)
				}
				detail := fmt.Sprintf("还差约 %d 杯", cups(defaults(), n))
				if n >= 2000 {
					detail = "目标达成"
				}
				note := fmt.Sprintf("约%d杯", cups(defaults(), n))
				if n >= 2000 {
					note = "达标"
				}
				summary := fmt.Sprintf("%d/2000毫升·%s", n, note)
				for _, item := range items {
					switch item.Type {
					case "metric":
						if item.Label != "今天喝水" || item.Value != fmt.Sprintf("%d / 2000 毫升", n) {
							t.Fatalf("metric %+v", item)
						}
					case "progress":
						wantLabel := detail
						if family == "accessory_circular" {
							wantLabel = "喝水"
						}
						if family == "accessory_rectangular" {
							wantLabel = summary
						}
						wantStyle := ""
						if family == "small" || family == "medium" {
							wantStyle = "ring"
						}
						if item.Style != wantStyle {
							t.Fatalf("progress style %q, want %q", item.Style, wantStyle)
						}
						if item.Ratio == nil || *item.Ratio != min(1, float64(n)/2000) || item.Detail != detail || item.Label != wantLabel {
							t.Fatalf("progress %+v", item)
						}
					case "text":
						want := fmt.Sprintf("喝水 %d/2000 毫升", n)
						if n >= 2000 {
							want = "喝水 已达标"
						}
						if item.Text != want || strings.Contains(item.Text, "\n") {
							t.Fatalf("inline %+v", item)
						}
					case "chart":
						if item.Kind != "bar" || item.Unit != "毫升" || len(item.Series) != 1 || len(item.Series[0].Points) != 7 {
							t.Fatalf("chart %+v", item)
						}
						for i, point := range item.Series[0].Points {
							want := 0
							if i == 5 {
								want = 1500
							}
							if i == 6 {
								want = n
							}
							if point.Value != float64(want) || point.Label != now.AddDate(0, 0, i-6).Format("01-02") {
								t.Fatalf("point %d %+v", i, point)
							}
						}
					}
				}
				b, _ := json.Marshal(v)
				if strings.Contains(string(b), "下次提醒") {
					t.Fatal("widget includes reminder time")
				}
				t.Logf("view=%s", b)
			})
		}
	}
}

func TestWidgetInvalidateActionsProtocol(t *testing.T) {
	for _, source := range []string{"page", "notification"} {
		t.Run(source, func(t *testing.T) {
			h := exttest.Start(t, manifest, exttest.Serve(t, program()))
			r := request("drink", "", nil)
			if source == "notification" {
				r.State = fmt.Sprintf("r=%d", time.Now().UnixMilli())
			}
			checkEvents := func(want int) {
				t.Helper()
				awaitWidgetCondition(t, "widget event was not received", func() bool { return len(h.Events()) >= want })
				events := h.Events()
				if len(events) != want {
					t.Fatalf("events %+v, want %d", events, want)
				}
				for _, e := range events {
					var p map[string]string
					if err := json.Unmarshal(e.Params, &p); err != nil {
						t.Fatal(err)
					}
					if e.Name != "ui.invalidate" || p["contribution"] != widgetID || len(p) != 1 {
						t.Fatalf("event %+v", e)
					}
				}
				t.Logf("action=%s events=%d", r.Action, len(events))
			}
			call := func() {
				t.Helper()
				var out wire.ActionResult
				if err := h.Call("ui.action", r.UIParams, &out); err != nil {
					t.Fatal(err)
				}
			}
			call()
			checkEvents(1)
			if source == "notification" {
				call()
				checkEvents(1)
			}
			r.Action, r.Values = "log", map[string]string{"text": "300"}
			call()
			checkEvents(2)
			if source == "notification" {
				call()
				checkEvents(2)
			}
			r.Action, r.Values = "undo", map[string]string{"id": "d1"}
			call()
			checkEvents(3)
			call()
			checkEvents(3)
			r.Action, r.Values = "log", map[string]string{"text": "invalid"}
			call()
			checkEvents(3)
			r.Action = "snooze"
			call()
			checkEvents(3)
		})
	}
}

func TestWidgetInvalidateMidnight(t *testing.T) {
	// Tokyo midnight is not UTC midnight. Even a zero-total day must refresh.
	for _, ml := range []int{0, 750} {
		now := instant("2026-11-16T14:59:59Z")
		c := defaults()
		c.Remind = false
		a := testApp(t, c, newHub("", "", nil), now)
		a.s.Zone = "Asia/Tokyo"
		if ml > 0 {
			a.s.Records = []record{{ID: "d1", At: now, ML: ml}}
		}
		a.widgetKey = a.progressKey(now)
		a.widgetPending = a.widgetKey
		events := make(chan string, 4)
		a.invalidate = func(id string) error { events <- id; return nil }
		startWidgetSender(t, a)
		for _, at := range []time.Time{now, now, now.Add(time.Second), now.Add(time.Second), now.Add(30 * time.Second)} {
			a.mu.Lock()
			err := a.tick(context.Background(), at)
			a.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			awaitWidgetClean(t, a)
			want := 0
			if at.After(now) {
				want = 1
			}
			if len(events) != want {
				t.Fatalf("at %s: events %v", at, events)
			}
		}
		if id := <-events; id != widgetID {
			t.Fatal(id)
		}
		v, err := a.widget("small", now.Add(time.Second))
		if err != nil || v.Sections[0].Items[0].Value != "0 / 2000 毫升" {
			t.Fatalf("midnight view %v, %v", v, err)
		}
		// An action at the next midnight emits once; its following tick does not repeat it.
		next := now.Add(24*time.Hour + time.Second)
		a.now = func() time.Time { return next }
		mustAction(t, a, "drink", "", nil)
		a.mu.Lock()
		err = a.tick(context.Background(), next)
		a.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		awaitWidgetClean(t, a)
		if len(events) != 1 {
			t.Fatalf("action and tick duplicated: %v", events)
		}
	}
}

func TestTimezoneFallbackProtocol(t *testing.T) {
	for _, tc := range []struct{ name, saved, hub, want string }{
		{"saved UI", "America/New_York", "Asia/Tokyo", "America/New_York"},
		{"hub only", "", "Asia/Tokyo", "Asia/Tokyo"},
		{"local only", "", "", time.Local.String()},
		{"invalid saved", "bad/zone", "Asia/Tokyo", "Asia/Tokyo"},
		{"invalid hub", "", "bad/zone", time.Local.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := exttest.StartWith(t, manifest, exttest.Serve(t, program()), func(p *wire.InitializeParams) {
				p.Hub.Timezone = tc.hub
				if err := save(filepath.Join(p.Extension.DataDir, "water.json"), state{Zone: tc.saved}); err != nil {
					t.Fatal(err)
				}
			})
			var v wire.View
			if err := h.Call("ui.render", request("", "", nil).UIParams, &v); err != nil {
				t.Fatal(err)
			}
			if got := v.Sections[0].Items[2].Text; got != "按 "+tc.want+" 时间提醒和记录" {
				t.Fatal(got)
			}
		})
	}
}

func TestHubTimezoneReadAtEachStart(t *testing.T) {
	now := instant("2026-11-16T00:30:00Z")
	path := filepath.Join(t.TempDir(), "water.json")
	for _, tz := range []string{"America/New_York", "Asia/Tokyo", ""} {
		a, err := openApp(path, defaults(), newHub("", "", nil), now, tz)
		if err != nil {
			t.Fatal(err)
		}
		if a.s.Zone != "" || a.s.location().String() != zone(tz).String() {
			t.Fatalf("fallback persisted or ignored: %+v", a.s)
		}
		a.now = func() time.Time { return now }
		mustAction(t, a, "drink", "", nil)
		if a.s.Zone != "" {
			t.Fatal("hub zone persisted as UI zone")
		}
	}
}

func TestTimezoneSharedCalendar(t *testing.T) {
	now := instant("2026-11-16T00:30:00Z")
	for _, tc := range []struct {
		saved, hub, wantDay string
		wantTotal           int
	}{
		{"America/New_York", "Asia/Tokyo", "2026-11-15", 750},
		{"", "Asia/Tokyo", "2026-11-16", 250},
	} {
		path := filepath.Join(t.TempDir(), "water.json")
		if err := save(path, state{Zone: tc.saved, Records: []record{
			{ID: "prior", At: instant("2026-11-15T12:00:00Z"), ML: 500},
			{ID: "today", At: now, ML: 250},
		}}); err != nil {
			t.Fatal(err)
		}
		a, err := openApp(path, defaults(), newHub("", "", nil), now, tc.hub)
		if err != nil {
			t.Fatal(err)
		}
		if day(now, a.s.location()) != tc.wantDay {
			t.Fatal("wrong calendar day")
		}
		v, err := a.widget("small", now)
		if err != nil {
			t.Fatal(err)
		}
		if v.Sections[0].Items[0].Value != fmt.Sprintf("%d / 2000 毫升", tc.wantTotal) {
			t.Fatalf("widget total: %+v", v)
		}
		if got := progressToast(a.cfg, a.s, now); got != fmt.Sprintf("今天 %d / 2000 毫升", tc.wantTotal) {
			t.Fatal(got)
		}
		if got := notification(a.cfg, a.s, now).Body; !strings.Contains(got, fmt.Sprintf("%d / 2000", tc.wantTotal)) {
			t.Fatal(got)
		}
		if schedule(a.cfg, a.s, now).Next.Location().String() != a.s.location().String() {
			t.Fatal("schedule ignored fallback")
		}
	}
}

func TestWidgetNoInvalidateAfterFailedSave(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	events := make(chan string, 4)
	a.invalidate = func(id string) error { events <- id; return nil }
	startWidgetSender(t, a)
	a.path = filepath.Join(a.path, "child")
	if _, err := a.action(context.Background(), request("drink", "", nil)); err == nil {
		t.Fatal("failed save accepted")
	}
	awaitWidgetClean(t, a)
	if len(events) != 0 {
		t.Fatal("failed save invalidated widget")
	}
}
