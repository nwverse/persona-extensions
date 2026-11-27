package model

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// At most Concurrent turns at once and PerMinute turns a Window, whatever the callers ask (design v2 §4.7: ≤4, ≤20).
func TestLimiterBounds(t *testing.T) {
	l := &Limiter{Concurrent: 4, PerMinute: 20, Window: 300 * time.Millisecond}
	var cur, most, total int32
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := l.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			n := atomic.AddInt32(&cur, 1)
			for {
				m := atomic.LoadInt32(&most)
				if n <= m || atomic.CompareAndSwapInt32(&most, m, n) {
					break
				}
			}
			atomic.AddInt32(&total, 1)
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&cur, -1)
			release()
		}()
	}
	wg.Wait()
	if most > 4 {
		t.Errorf("%d at once", most)
	}
	if total != 30 {
		t.Errorf("%d calls", total)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Errorf("30 calls within one window of 20: %v", time.Since(start))
	}
}

// A low-priority call (a rerun) never takes a free turn while a normal one (the daily pass) waits.
func TestLimiterPriority(t *testing.T) {
	l := &Limiter{Concurrent: 1, PerMinute: 100, Window: time.Second}
	hold, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	order := make(chan string, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r, _ := l.Acquire(WithPriority(context.Background(), PriorityLow))
		order <- "rerun"
		r()
	}()
	time.Sleep(20 * time.Millisecond) // the rerun waits first
	go func() {
		defer wg.Done()
		r, _ := l.Acquire(context.Background())
		order <- "daily"
		r()
	}()
	time.Sleep(20 * time.Millisecond)
	hold()
	wg.Wait()
	if first := <-order; first != "daily" {
		t.Fatalf("%s went first", first)
	}
}

// A call whose context ends while it waits takes no turn and leaves none taken.
func TestLimiterCancel(t *testing.T) {
	l := &Limiter{Concurrent: 1, PerMinute: 100, Window: time.Second}
	hold, _ := l.Acquire(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(ctx); err == nil {
		t.Fatal("got a turn past its context")
	}
	hold()
	if out, waiting, _ := l.Stats(); out != 0 || waiting != 0 {
		t.Fatalf("out %d waiting %d", out, waiting)
	}
}

// The cost line: per day, per model, the reruns apart, and the totals.
func TestCostLines(t *testing.T) {
	s := OpenStats(t.TempDir(), time.Now())
	s.Loc = time.UTC
	s.add(Call{At: "2026-03-14T10:00:00Z", Answered: "gemini-x", In: 1000, Out: 200})
	s.add(Call{At: "2026-03-14T11:00:00Z", Answered: "claude-haiku-5-5", FellBack: true, In: 800, Out: 100, Class: ClassRerun})
	s.add(Call{At: "2026-03-14T12:00:00Z", Class: ClassRerun})                           // unanswered
	s.add(Call{At: "2026-03-14T12:30:00Z", Paused: true, Until: "2026-03-14T12:31:00Z"}) // held: not a call
	c := s.Snapshot()
	d := c.Days["2026-03-14"]
	if d == nil || d.All.Calls != 3 || d.All.In != 1800 || d.All.FellBack != 1 || d.All.Failed != 1 || d.Rerun.Calls != 2 ||
		d.All.Models["gemini-x"].Out != 200 {
		t.Fatalf("day %+v", d)
	}
	if c.Rerun.Calls != 2 || c.Total.Calls != 3 {
		t.Fatalf("totals %+v %+v", c.Total, c.Rerun)
	}
	lines := c.CostLines("2026-03-14")
	if len(lines) != 3 || lines[0] == "" {
		t.Fatalf("%q", lines)
	}
}
