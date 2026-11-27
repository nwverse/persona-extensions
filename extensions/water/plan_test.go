package main

import (
	"testing"
	"time"
)

func instant(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestScheduleRules(t *testing.T) {
	base := "2026-11-16T10:00:00Z"
	tests := []struct {
		name, now, want, reason string
		change                  func(*config, *state)
	}{
		{"R1 saved zone", base, "2026-11-16T11:00:00Z", "", func(c *config, s *state) { s.Zone = "Asia/Tokyo"; c.Start = "20:00"; c.End = "22:00" }},
		{"R2 invalid order", base, "", "invalid", func(c *config, s *state) { c.Start = "22:00" }},
		{"R2 equal", base, "", "invalid", func(c *config, s *state) { c.End = c.Start }},
		{"R2 bad clock", base, "", "invalid", func(c *config, s *state) { c.Start = "9:00" }},
		{"R2 inclusive start", "2026-11-16T09:00:00Z", "2026-11-16T09:00:00Z", "", nil},
		{"R2 inclusive end", "2026-11-16T21:00:00Z", "2026-11-16T21:00:00Z", "", nil},
		{"R3 first at start", "2026-11-16T07:00:00Z", "2026-11-16T09:00:00Z", "", nil},
		{"R3 drink restarts interval", base, "2026-11-16T10:30:00Z", "", func(c *config, s *state) { s.Records = []record{{At: instant("2026-11-16T09:30:00Z"), ML: 250}} }},
		{"R3 reminder is later", base, "2026-11-16T10:45:00Z", "", func(c *config, s *state) {
			s.LastReminder = instant("2026-11-16T09:45:00Z")
			s.Records = []record{{At: instant("2026-11-16T09:30:00Z"), ML: 250}}
		}},
		{"R3 drink is later", base, "2026-11-16T10:50:00Z", "", func(c *config, s *state) {
			s.LastReminder = instant("2026-11-16T09:45:00Z")
			s.Records = []record{{At: instant("2026-11-16T09:50:00Z"), ML: 250}}
		}},
		{"R3 old events do not delay morning", "2026-11-16T08:00:00Z", "2026-11-16T09:00:00Z", "", func(c *config, s *state) { s.LastReminder = instant("2026-11-15T21:00:00Z") }},
		{"R4 snooze overrides interval", base, "2026-11-16T10:10:00Z", "", func(c *config, s *state) {
			s.LastReminder = instant("2026-11-16T09:55:00Z")
			s.Snooze = instant("2026-11-16T10:10:00Z")
		}},
		{"R5 computed after end", "2026-11-16T20:30:00Z", "2026-11-17T09:00:00Z", "closed", func(c *config, s *state) { s.LastReminder = instant("2026-11-16T20:20:00Z") }},
		{"R5 snooze after end", "2026-11-16T20:45:00Z", "2026-11-17T09:00:00Z", "closed", func(c *config, s *state) { s.Snooze = instant("2026-11-16T21:15:00Z") }},
		{"R6 Saturday", "2026-11-14T12:00:00Z", "2026-11-16T09:00:00Z", "weekend", func(c *config, s *state) { c.Weekends = false }},
		{"R6 Sunday", "2026-11-15T12:00:00Z", "2026-11-16T09:00:00Z", "weekend", func(c *config, s *state) { c.Weekends = false }},
		{"R6 Friday overflow", "2026-11-13T21:30:00Z", "2026-11-16T09:00:00Z", "closed", func(c *config, s *state) { c.Weekends = false }},
		{"R7 goal reached", base, "2026-11-17T09:00:00Z", "done", func(c *config, s *state) { s.Records = []record{{At: instant(base), ML: 2000}} }},
		{"R7 goal ignored when disabled", base, "2026-11-16T11:00:00Z", "", func(c *config, s *state) { c.Stop = false; s.Records = []record{{At: instant(base), ML: 2000}} }},
		{"R7 Friday goal skips weekend", "2026-11-13T10:00:00Z", "2026-11-16T09:00:00Z", "done", func(c *config, s *state) {
			c.Weekends = false
			s.Records = []record{{At: instant("2026-11-13T09:30:00Z"), ML: 2100}}
		}},
		{"R8 disabled", base, "", "off", func(c *config, s *state) { c.Remind = false }},
		{"R9 one catchup", base, base, "", func(c *config, s *state) { s.LastReminder = instant("2026-11-15T10:00:00Z") }},
		{"R9 no catchup after window", "2026-11-16T22:00:00Z", "2026-11-17T09:00:00Z", "closed", nil},
		{"R10 spring DST window", "2026-03-08T06:30:00Z", "2026-03-08T13:00:00Z", "", func(c *config, s *state) { s.Zone = "America/New_York" }},
		{"R10 fall DST window", "2026-11-01T05:30:00Z", "2026-11-01T14:00:00Z", "", func(c *config, s *state) { s.Zone = "America/New_York" }},
		{"R10 local midnight excludes yesterday", "2026-03-09T04:00:00Z", "2026-03-09T13:00:00Z", "", func(c *config, s *state) {
			s.Zone = "America/New_York"
			s.Records = []record{{At: instant("2026-03-09T03:59:59Z"), ML: 2000}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := defaults()
			s := state{Zone: "UTC"}
			if tt.change != nil {
				tt.change(&c, &s)
			}
			got := schedule(c, s, instant(tt.now))
			if got.Reason != tt.reason {
				t.Fatalf("reason %q, want %q", got.Reason, tt.reason)
			}
			if tt.want == "" {
				if !got.Next.IsZero() {
					t.Fatalf("unexpected next %v", got.Next)
				}
			} else if !got.Next.Equal(instant(tt.want)) {
				t.Fatalf("next %v, want %s", got.Next, tt.want)
			}
		})
	}
}

func TestFallbackZone(t *testing.T) {
	for _, name := range []string{"", "invalid/zone"} {
		if zone(name) != time.Local {
			t.Fatal("not the process local zone")
		}
		now := time.Date(2026, 11, 16, 8, 0, 0, 0, time.Local)
		p := schedule(defaults(), state{Zone: name}, now)
		if !p.Next.Equal(at(now, 9*60)) {
			t.Fatalf("local plan: %v", p)
		}
	}
}

func TestCalendarTotalsDST(t *testing.T) {
	for _, tc := range []struct {
		name, start, end string
		hours            int
	}{
		{"spring", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z", 23},
		{"fall", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z", 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end := instant(tc.start), instant(tc.end)
			l := zone("America/New_York")
			s := state{Records: []record{{At: start.Add(-time.Second), ML: 100}, {At: start, ML: 200}, {At: end.Add(-time.Second), ML: 300}, {At: end, ML: 400}}}
			if got := total(s, start, l); got != 500 {
				t.Fatalf("total %d", got)
			}
			if end.Sub(start) != time.Duration(tc.hours)*time.Hour {
				t.Fatal("bad fixture")
			}
		})
	}
}
