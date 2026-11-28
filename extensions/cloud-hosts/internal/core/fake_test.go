package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// fake is a scriptable Adapter that counts its calls.
type fake struct {
	mu sync.Mutex

	list     []wire.CloudInstance
	problems []string
	listErr  error

	// instance answers Instance; nil answers the entry of list with that ref, or not_found.
	instance func(ref wire.CloudInstanceRef) (wire.CloudInstance, error)
	// action answers Action; nil answers accepted.
	action func(a Action) (wire.CloudActionResult, error)

	snaps   []wire.CloudSnapshot
	snapErr error

	// gate, when set, holds every Action until it is closed.
	gate chan struct{}
	// entered receives once per Action call that started (buffered by the test).
	entered chan struct{}

	nList, nInstance, nAction, nSnapshots int
	actions                               []Action
}

func (f *fake) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nList++
	if f.listErr != nil {
		return nil, nil, f.listErr
	}
	out := append([]wire.CloudInstance(nil), f.list...)
	return out, append([]string(nil), f.problems...), nil
}

func (f *fake) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	f.mu.Lock()
	f.nInstance++
	fn := f.instance
	list := append([]wire.CloudInstance(nil), f.list...)
	f.mu.Unlock()
	if fn != nil {
		return fn(ref)
	}
	for _, in := range list {
		if in.Account == ref.Account && in.ID == ref.ID {
			return in, nil
		}
	}
	return wire.CloudInstance{}, NotFound("no such instance")
}

func (f *fake) Action(ctx context.Context, a Action) (wire.CloudActionResult, error) {
	f.mu.Lock()
	f.nAction++
	f.actions = append(f.actions, a)
	fn, gate, entered := f.action, f.gate, f.entered
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return wire.CloudActionResult{}, ctx.Err()
		}
	}
	if fn != nil {
		return fn(a)
	}
	return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}, nil
}

func (f *fake) Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nSnapshots++
	if f.snapErr != nil {
		return nil, f.snapErr
	}
	return append([]wire.CloudSnapshot(nil), f.snaps...), nil
}

func (f *fake) counts() (list, instance, action, snapshots int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nList, f.nInstance, f.nAction, f.nSnapshots
}

func (f *fake) actionCalls() int {
	_, _, n, _ := f.counts()
	return n
}

// consoleFake is a fake that is also a Consoler.
type consoleFake struct {
	*fake
	console  func(ref wire.CloudInstanceRef, locale string) (wire.CloudActionResult, error)
	nConsole int
}

func (c *consoleFake) Console(ctx context.Context, ref wire.CloudInstanceRef, locale string) (wire.CloudActionResult, error) {
	c.mu.Lock()
	c.nConsole++
	c.mu.Unlock()
	return c.console(ref, locale)
}

// secretFake is a fake that holds credentials.
type secretFake struct {
	*fake
	secrets []string
}

func (s *secretFake) SecretValues() []string { return s.secrets }

// clock is a settable Options.Now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var (
	refA = wire.CloudInstanceRef{Account: "prod", ID: "i-0001"}
	refB = wire.CloudInstanceRef{Account: "prod", ID: "i-0002"}
)

// inst is a well-formed running instance of ref.
func inst(ref wire.CloudInstanceRef, ip string) wire.CloudInstance {
	return wire.CloudInstance{
		Account: ref.Account, ID: ref.ID, DisplayName: "web " + string(ref.ID), State: wire.CloudInstanceStateRunning,
		Region: "region-1", Zone: "region-1a", PublicIps: []string{ip}, PrivateIps: []string{"10.0.0.5"},
		ConsoleURL: "https://console.example.com/instances/" + string(ref.ID),
		Actions: []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot,
			wire.CloudActionKindSyncIp, wire.CloudActionKindConsole},
	}
}

func newProvider(a Adapter, c *clock, custom map[wire.CloudCustomActionID]wire.CloudSafety, secrets ...string) *Provider {
	r := &Redactor{}
	r.Add(secrets...)
	o := Options{ID: "test", Custom: custom, Redactor: r}
	if c != nil {
		o.Now = c.Now
	}
	return NewProvider(a, o)
}

func actParams(ref wire.CloudInstanceRef, kind wire.CloudActionKind, id string) wire.CloudActionParams {
	return wire.CloudActionParams{Contribution: "test", Locale: "en", Instance: ref, Action: kind,
		RequestID: wire.CloudRequestID(id)}
}

// valid holds v to the result definition of call and returns its JSON.
func valid(t testing.TB, call string, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: marshal: %v", call, err)
	}
	if err := wire.ValidateResult(call, raw); err != nil {
		t.Fatalf("%s: the answer breaks the schema: %v\n%s", call, err, raw)
	}
	return raw
}

// wireCode is the code err goes on the wire with; it fails when err is not an *ext.Error.
func wireCode(t testing.TB, err error) wire.CloudErrorCode {
	t.Helper()
	if err == nil {
		return ""
	}
	var e *ext.Error
	if !errors.As(err, &e) {
		t.Fatalf("the error is %T, not *ext.Error: %v", err, err)
	}
	if strings.ContainsAny(e.Message, "\r\n") {
		t.Fatalf("the error message is not one line: %q", e.Message)
	}
	if e.Message == "" || len([]rune(e.Message)) > maxMessage {
		t.Fatalf("the error message is empty or too long: %q", e.Message)
	}
	return wire.CloudErrorCode(e.Code)
}

func wantCode(t testing.TB, err error, want wire.CloudErrorCode) {
	t.Helper()
	if got := wireCode(t, err); got != want {
		t.Fatalf("code = %q, want %q (err %v)", got, want, err)
	}
}

func noLeak(t testing.TB, what string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(what, s) {
			t.Fatalf("a credential leaked: %q appears in %q", s, what)
		}
	}
}

// lookupClock is a fixed clock that counts its reads. The provider reads its clock under its lock as it looks up
// a request id (and once more as an owner makes its record), so a test can wait until a call has looked.
type lookupClock struct {
	reads chan struct{}
	mu    sync.Mutex
	n     int
}

func newLookupClock() *lookupClock { return &lookupClock{reads: make(chan struct{}, 1024)} }

func (c *lookupClock) Now() time.Time {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	select {
	case c.reads <- struct{}{}:
	default:
	}
	return time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
}

// wait blocks until n more reads happened.
func (c *lookupClock) wait(n int) {
	for ; n > 0; n-- {
		<-c.reads
	}
}

func (c *lookupClock) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
