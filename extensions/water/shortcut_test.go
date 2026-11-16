package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestShortcutProtocol(t *testing.T) {
	var path string
	c := defaults()
	c.Remind, c.Cup, c.Goal = false, 200, 500
	h := exttest.StartWith(t, manifest, exttest.Serve(t, program()), func(p *wire.InitializeParams) {
		path = filepath.Join(p.Extension.DataDir, "water.json")
		p.Config, _ = json.Marshal(c)
	})
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if h.Init.Version != "0.3.0" || m.Version != "0.3.0" {
		t.Fatalf("versions: program=%s manifest=%s", h.Init.Version, m.Version)
	}
	want := []wire.ShortcutItem{{ID: "log_water", Title: "记一杯水", Description: "记一杯水；填了毫升数就按填的量记。", UI: "water",
		Params: []wire.ShortcutParam{{Name: "amount", Title: "毫升", Kind: "number"}}}}
	var advertised []wire.ShortcutItem
	if err := json.Unmarshal(h.Init.Contributes["shortcuts"], &advertised); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Contributes.Shortcuts, want) || !reflect.DeepEqual(advertised, want) {
		t.Fatalf("shortcuts: manifest=%+v program=%+v", m.Contributes.Shortcuts, advertised)
	}
	read := func() state {
		t.Helper()
		s, err := load(path, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	count := 0
	for _, tc := range []struct {
		name, tz, zone string
		values         map[string]string
		ml             int
	}{
		{"cup", "UTC", "UTC", map[string]string{}, 200},
		{"amount", "Asia/Tokyo", "Asia/Tokyo", map[string]string{"amount": "300"}, 300},
		{"repeat", "", "Asia/Tokyo", map[string]string{"amount": "300"}, 300},
		{"zero", "Invalid/Zone", "Asia/Tokyo", map[string]string{"amount": "0"}, 0},
		{"too-large", "", "Asia/Tokyo", map[string]string{"amount": "5000"}, 0},
		{"letters", "", "Asia/Tokyo", map[string]string{"amount": "abc"}, 0},
		{"empty", "", "Asia/Tokyo", map[string]string{"amount": ""}, 0},
		{"units", "UTC", "UTC", map[string]string{"amount": " 1 毫升 "}, 1},
		{"maximum", "Invalid/Zone", "UTC", map[string]string{"amount": "3000ml"}, 3000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := read()
			var res wire.ShortcutResult
			if err := h.Call("shortcut.run", wire.ShortcutRunParams{Contribution: "log_water", Values: tc.values, TZ: tc.tz, UIVersion: 4}, &res); err != nil {
				t.Fatal(err)
			}
			s := read()
			if s.Zone != tc.zone {
				t.Fatalf("persisted zone %q, want %q", s.Zone, tc.zone)
			}
			if tc.ml == 0 {
				if res.Text != "请填 1 到 3000 之间的毫升数" || !reflect.DeepEqual(before.Records, s.Records) || s.Sequence != before.Sequence {
					t.Fatalf("invalid amount changed records: %+v, %s", s, res.Text)
				}
				return
			}
			count++
			if len(s.Records) != count || s.Sequence != uint64(count) || s.Records[count-1].ML != tc.ml {
				t.Fatalf("persisted drink: %+v", s)
			}
			r := s.Records[count-1]
			n := total(s, r.At, s.location())
			want := fmt.Sprintf("已记 %d 毫升，今天 %d / %d", tc.ml, n, c.Goal)
			if n >= c.Goal {
				want += "，今天的目标达成了"
			}
			if res.Text != want || res.View == nil {
				t.Fatalf("result %+v, want %s", res, want)
			}
			if err := wire.CheckView(res.View); err != nil {
				t.Fatal(err)
			}
			items := res.View.Sections[0].Items
			if len(items) != 2 || items[0].Type != "metric" || items[0].Value != fmt.Sprintf("%d / %d 毫升", n, c.Goal) || items[1].Type != "progress" {
				t.Fatalf("card %+v", items)
			}
			awaitWidgetCondition(t, "shortcut did not invalidate widget", func() bool { return len(h.Events()) >= count })
			for _, e := range h.Events() {
				var p map[string]string
				if err := json.Unmarshal(e.Params, &p); err != nil {
					t.Fatal(err)
				}
				if e.Name != "ui.invalidate" || p["contribution"] != widgetID || len(p) != 1 {
					t.Fatalf("event %+v", e)
				}
			}
			t.Logf("records=%d ml=%d zone=%s text=%s", count, tc.ml, s.Zone, res.Text)
		})
	}
	if err := h.Call("shortcut.run", wire.ShortcutRunParams{Contribution: "missing", Values: map[string]string{}}, nil); err == nil {
		t.Fatal("unknown shortcut succeeded")
	}
}

func TestShortcutRestartsReminderProtocol(t *testing.T) {
	var path string
	c := defaults()
	c.Start, c.End = "00:00", "23:59"
	seedTime := time.Now().UTC()
	h := exttest.StartWith(t, manifest, exttest.Serve(t, program()), func(p *wire.InitializeParams) {
		path = filepath.Join(p.Extension.DataDir, "water.json")
		p.Config, _ = json.Marshal(c)
		s := state{Zone: "UTC", Snooze: seedTime.Add(5 * time.Minute), Pending: &pending{At: seedTime, RetryAt: seedTime.Add(30 * time.Second), Attempts: 1,
			Notice: notice{UI: "water", ID: "water-reminder"}}}
		if err := save(path, s); err != nil {
			t.Fatal(err)
		}
	})
	var res wire.ShortcutResult
	if err := h.Call("shortcut.run", wire.ShortcutRunParams{Contribution: "log_water", Values: map[string]string{}}, &res); err != nil {
		t.Fatal(err)
	}
	s, err := load(path, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Records) != 1 || !s.Snooze.IsZero() || s.Pending != nil {
		t.Fatalf("shortcut did not clear reminder state: %+v", s)
	}
	r := s.Records[0]
	want := r.At.Add(time.Duration(c.Interval) * time.Minute)
	if want.After(at(r.At.UTC(), 23*60+59)) {
		want = at(r.At.UTC().AddDate(0, 0, 1), 0)
	}
	if next := schedule(c, s, r.At).Next; !next.Equal(want) {
		t.Fatalf("next reminder %s, want %s", next, want)
	}
	t.Logf("drink=%s next=%s snooze/pending cleared", r.At, want)
}

func TestShortcutFailedSave(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	before := a.s
	a.path = filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(a.path, 0700); err != nil {
		t.Fatal(err)
	}
	if res, err := a.shortcut(context.Background(), &ext.ShortcutRequest{}); err == nil || res != nil {
		t.Fatalf("failed save reported success: %+v %v", res, err)
	}
	if !reflect.DeepEqual(before, a.s) || len(a.widgetWake) != 0 {
		t.Fatal("failed save changed state or queued refresh")
	}
}

func TestShortcutUsesPageAmountParser(t *testing.T) {
	for _, v := range []string{"1", "3000", " 300 ml ", "300 毫升", "0", "5000", "abc", "", "1.5", "1e2"} {
		t.Run(v, func(t *testing.T) {
			a := testApp(t, defaults(), newHub("", "", nil), instant("2026-11-16T10:00:00Z"))
			ml, valid := amount(v)
			res, err := a.shortcut(context.Background(), &ext.ShortcutRequest{ShortcutRunParams: wire.ShortcutRunParams{Values: map[string]string{"amount": v}}})
			if err != nil {
				t.Fatal(err)
			}
			if valid {
				if len(a.s.Records) != 1 || a.s.Records[0].ML != ml {
					t.Fatalf("parser mismatch: %+v", a.s)
				}
			} else if len(a.s.Records) != 0 || !strings.Contains(res.Text, "1 到 3000") {
				t.Fatalf("invalid amount accepted: %+v", res)
			}
		})
	}
}

func TestShortcutClockAndWidgetBackpressure(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	a := testApp(t, defaults(), newHub("", "", nil), now)
	a.now = func() time.Time {
		if a.mu.TryLock() {
			a.mu.Unlock()
			t.Error("shortcut read clock without state lock")
		}
		return now
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a.invalidate = func(string) error {
		close(entered)
		<-release
		return nil
	}
	startWidgetSender(t, a)
	result := make(chan error, 1)
	go func() {
		_, err := a.shortcut(context.Background(), &ext.ShortcutRequest{})
		result <- err
	}()
	within(t, entered, "shortcut did not wake widget sender")
	if err := within(t, result, "shortcut blocked on widget output"); err != nil {
		t.Fatal(err)
	}
	// Widget output remains parked while another operation takes the state lock.
	locked := make(chan struct{})
	go func() { a.mu.Lock(); a.mu.Unlock(); close(locked) }()
	within(t, locked, "widget sender held state lock during output")
}
