package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStoreRoundTripPrivateAtomic(t *testing.T) {
	now := instant("2026-11-16T10:00:00Z")
	path := filepath.Join(t.TempDir(), "data", "water.json")
	s := state{Zone: "UTC", Sequence: 3, Records: []record{{ID: "d3", At: now, ML: 250}}, LastReminder: now, Snooze: now.Add(time.Minute),
		Seen: map[string]time.Time{"r=example:drink": now}, Pending: &pending{At: now, Notice: notification(defaults(), state{}, now), Attempts: 2, RetryAt: now.Add(time.Minute)}}
	if err := save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := load(path, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("roundtrip: %#v", got)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v %v", fi, err)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".water-*"))
	if len(files) != 0 {
		t.Fatalf("temp files left: %v", files)
	}
	// An unsuccessful replacement cannot overwrite the old file.
	bad := filepath.Join(path, "child")
	if err := save(bad, s); err == nil {
		t.Fatal("expected failure")
	}
	again, _ := load(path, now)
	if !reflect.DeepEqual(again, s) {
		t.Fatal("old state changed")
	}
}

func TestRetention400CalendarDaysAnd24Hours(t *testing.T) {
	l := zone("America/New_York")
	now := instant("2026-11-01T17:00:00Z")
	cut := at(now.In(l), 0).AddDate(0, 0, -399)
	s := state{Zone: l.String(), Records: []record{{ID: "old", At: cut.Add(-time.Nanosecond), ML: 1}, {ID: "edge", At: cut, ML: 1}, {ID: "now", At: now, ML: 1}},
		Seen: map[string]time.Time{"old": now.Add(-24 * time.Hour), "keep": now.Add(-24*time.Hour + time.Nanosecond)}}
	s.prune(now)
	if len(s.Records) != 2 || s.Records[0].ID != "edge" || len(s.Seen) != 1 {
		t.Fatalf("pruned: %#v", s)
	}
}

func TestCorruptRecovery(t *testing.T) {
	for _, raw := range []string{"{broken", `{"records":[{"id":"d1","at":"2026-11-16T10:00:00Z","ml":-1}]}`, `{"pending":{"attempts":-1}}`} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "water.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			a, err := openApp(path, defaults(), newHub("", "", nil), instant("2026-11-16T10:00:00Z"))
			if err != nil {
				t.Fatal(err)
			}
			if len(a.s.Records) != 0 {
				t.Fatal("not empty")
			}
			backups, _ := filepath.Glob(path + ".corrupt-*")
			if len(backups) != 1 {
				t.Fatalf("backups %v", backups)
			}
			b, _ := os.ReadFile(backups[0])
			if string(b) != raw {
				t.Fatal("backup differs")
			}
			if _, err := load(path, time.Now()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
