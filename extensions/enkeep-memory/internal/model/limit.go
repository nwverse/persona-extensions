package model

import (
	"container/heap"
	"context"
	"sync"
	"time"
)

// The project's limit on model calls (unified memory design v2 §4.7, 0.2.0-beta.38): at most LimitConcurrent calls at
// once and LimitPerMinute calls in any minute, for everything the extension asks a model in one process: the daily
// and weekly passes, the reruns, the synthesis, the exits' rewriting, trials and evaluations. The model gateway is
// shared by every agent and the user; a burst from one extension fills it for all.
//
// Calls wait their turn by priority: the scheduled daily and weekly passes (and everything else not marked) are
// PriorityNormal, the reruns PriorityLow; a low call never takes a free slot while a normal one waits. Within a
// priority, first come first served.
const (
	LimitConcurrent = 4
	LimitPerMinute  = 20
)

// Priorities of a call (WithPriority).
const (
	PriorityNormal = 0
	PriorityLow    = 1
)

type ctxKey int

const (
	keyPriority ctxKey = iota
	keyClass
)

// WithPriority marks the calls made with ctx.
func WithPriority(ctx context.Context, p int) context.Context {
	return context.WithValue(ctx, keyPriority, p)
}

// PriorityOf is the priority ctx carries (PriorityNormal when none).
func PriorityOf(ctx context.Context) int {
	if p, ok := ctx.Value(keyPriority).(int); ok {
		return p
	}
	return PriorityNormal
}

// Cost classes: what a call is counted under in the daily cost line (Stats).
const (
	ClassRegular = "regular"
	ClassRerun   = "rerun"
)

// WithClass marks the calls made with ctx for the cost line.
func WithClass(ctx context.Context, class string) context.Context {
	return context.WithValue(ctx, keyClass, class)
}

// ClassOf is the cost class ctx carries (ClassRegular when none).
func ClassOf(ctx context.Context) string {
	if c, ok := ctx.Value(keyClass).(string); ok && c != "" {
		return c
	}
	return ClassRegular
}

// Limiter hands out turns: a call takes one (Acquire), makes its request, and gives it back (the func Acquire
// returns). Each turn counts once in the minute it was given.
type Limiter struct {
	Concurrent int           // at most this many turns out at once; ≤0: LimitConcurrent
	PerMinute  int           // at most this many turns given in any Window; ≤0: LimitPerMinute
	Window     time.Duration // ≤0: a minute (tests shorten it)
	Now        func() time.Time

	mu      sync.Mutex
	out     int
	given   []time.Time // when the turns of the last minute were given, oldest first
	waiting waitHeap
	seq     int
	timer   *time.Timer
}

// shared is the process's limiter: every Hub uses it unless given its own.
var shared = &Limiter{}

// Shared is the process's limiter.
func Shared() *Limiter { return shared }

type waiter struct {
	prio, seq int
	ready     chan struct{}
	index     int
}

type waitHeap []*waiter

func (h waitHeap) Len() int { return len(h) }
func (h waitHeap) Less(i, j int) bool {
	if h[i].prio != h[j].prio {
		return h[i].prio < h[j].prio
	}
	return h[i].seq < h[j].seq
}
func (h waitHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *waitHeap) Push(x any)   { w := x.(*waiter); w.index = len(*h); *h = append(*h, w) }
func (h *waitHeap) Pop() any {
	old := *h
	w := old[len(old)-1]
	*h = old[:len(old)-1]
	w.index = -1
	return w
}
func (l *Limiter) conc() int {
	if l.Concurrent > 0 {
		return l.Concurrent
	}
	return LimitConcurrent
}
func (l *Limiter) perMin() int {
	if l.PerMinute > 0 {
		return l.PerMinute
	}
	return LimitPerMinute
}
func (l *Limiter) window() time.Duration {
	if l.Window > 0 {
		return l.Window
	}
	return time.Minute
}
func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Acquire waits for a turn at ctx's priority; release gives it back. An error is ctx's: no turn was taken.
func (l *Limiter) Acquire(ctx context.Context) (release func(), err error) {
	l.mu.Lock()
	w := &waiter{prio: PriorityOf(ctx), seq: l.seq, ready: make(chan struct{})}
	l.seq++
	heap.Push(&l.waiting, w)
	l.dispatch()
	l.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
		l.mu.Lock()
		select {
		case <-w.ready: // given meanwhile: give it back
			l.mu.Unlock()
			l.release()
			return nil, ctx.Err()
		default:
		}
		if w.index >= 0 {
			heap.Remove(&l.waiting, w.index)
		}
		l.mu.Unlock()
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(l.release) }, nil
}

func (l *Limiter) release() {
	l.mu.Lock()
	l.out--
	l.dispatch()
	l.mu.Unlock()
}

// dispatch gives turns to the waiters at the front while both limits allow, and sets a timer for when the minute's
// limit next frees one. Called with l.mu held.
func (l *Limiter) dispatch() {
	now := l.now()
	cut := now.Add(-l.window())
	i := 0
	for i < len(l.given) && !l.given[i].After(cut) {
		i++
	}
	l.given = l.given[i:]
	for l.waiting.Len() > 0 && l.out < l.conc() && len(l.given) < l.perMin() {
		w := heap.Pop(&l.waiting).(*waiter)
		l.out++
		l.given = append(l.given, now)
		close(w.ready)
	}
	if l.waiting.Len() > 0 && l.out < l.conc() && len(l.given) >= l.perMin() && l.timer == nil {
		wait := l.given[0].Add(l.window()).Sub(now) + 10*time.Millisecond
		l.timer = time.AfterFunc(wait, func() {
			l.mu.Lock()
			l.timer = nil
			l.dispatch()
			l.mu.Unlock()
		})
	}
}

// Stats of the limiter, for the status tool: turns out now, waiting, given in the last minute.
func (l *Limiter) Stats() (out, waiting, lastMinute int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := l.now().Add(-l.window())
	for _, t := range l.given {
		if t.After(cut) {
			lastMinute++
		}
	}
	return l.out, l.waiting.Len(), lastMinute
}

// Limited makes every call of a model wait for a turn of Limit (nil: the shared limiter): the offline trials and
// replays, whose model is not the hub's, keep to the same limits.
type Limited struct {
	Model Model
	Limit *Limiter
}

func (l Limited) Complete(ctx context.Context, r Request) (Response, error) {
	lim := l.Limit
	if lim == nil {
		lim = shared
	}
	release, err := lim.Acquire(ctx)
	if err != nil {
		return Response{}, err
	}
	defer release()
	return l.Model.Complete(ctx, r)
}
