package main

import (
	"fmt"
	"time"
)

type config struct {
	Remind   bool   `json:"remind"`
	Goal     int    `json:"goal_ml"`
	Cup      int    `json:"cup_ml"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Interval int    `json:"interval_minutes"`
	Weekends bool   `json:"weekends"`
	Stop     bool   `json:"stop_when_done"`
}

func defaults() config { return config{true, 2000, 250, "09:00", "21:00", 60, true, true} }

func (c config) validate() error {
	if c.Goal < 500 || c.Goal > 6000 || c.Cup < 50 || c.Cup > 1000 || c.Interval < 15 || c.Interval > 240 {
		return fmt.Errorf("喝水设置超出允许范围")
	}
	return nil
}

func clock(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	return t.Hour()*60 + t.Minute(), err == nil && t.Format("15:04") == s
}

func zone(name string) *time.Location {
	if name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			return l
		}
	}
	return time.Local
}

// location prefers the persisted UI zone, then the device zone read at this start.
func (s state) location() *time.Location {
	for _, name := range []string{s.Zone, s.HubZone} {
		if name != "" {
			if l, err := time.LoadLocation(name); err == nil {
				return l
			}
		}
	}
	return time.Local
}

func day(t time.Time, l *time.Location) string { return t.In(l).Format("2006-01-02") }
func at(t time.Time, minutes int) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), minutes/60, minutes%60, 0, 0, t.Location())
}

func total(s state, now time.Time, l *time.Location) int {
	n := 0
	for _, r := range s.Records {
		if day(r.At, l) == day(now, l) {
			n += r.ML
		}
	}
	return n
}

type plan struct {
	Next   time.Time
	Reason string
}

// schedule calculates one next occurrence. Overdue occurrences collapse into one send at now.
// Calendar dates and window boundaries use the saved plan zone, including DST days.
func schedule(c config, s state, now time.Time) plan {
	l := s.location()
	now = now.In(l)
	start, ok := clock(c.Start)
	end, okEnd := clock(c.End)
	if !ok || !okEnd || start >= end {
		return plan{Reason: "invalid"}
	}
	if !c.Remind {
		return plan{Reason: "off"}
	}
	nextDay := func(reason string) plan {
		d := now.AddDate(0, 0, 1)
		for !c.Weekends && (d.Weekday() == time.Saturday || d.Weekday() == time.Sunday) {
			d = d.AddDate(0, 0, 1)
		}
		return plan{at(d, start), reason}
	}
	if !c.Weekends && (now.Weekday() == time.Saturday || now.Weekday() == time.Sunday) {
		return nextDay("weekend")
	}
	if c.Stop && total(s, now, l) >= c.Goal {
		return nextDay("done")
	}
	if now.After(at(now, end)) {
		return nextDay("closed")
	}
	next := at(now, start)
	last := s.LastReminder
	for _, r := range s.Records {
		if r.At.After(last) {
			last = r.At
		}
	}
	if !last.IsZero() {
		candidate := last.In(l).Add(time.Duration(c.Interval) * time.Minute)
		if candidate.After(next) {
			next = candidate
		}
	}
	if !s.Snooze.IsZero() {
		next = s.Snooze.In(l)
		if next.Before(at(now, start)) {
			next = at(now, start)
		}
	}
	if next.After(at(now, end)) {
		return nextDay("closed")
	}
	if next.Before(now) {
		next = now
	}
	return plan{Next: next}
}
