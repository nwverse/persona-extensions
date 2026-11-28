package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

var customs = map[wire.CloudCustomActionID]wire.CloudSafety{
	"renew": wire.CloudSafetyConfirm,
	"probe": wire.CloudSafetyRead,
	"wipe":  wire.CloudSafetyDestructive,
}

func TestActionSameRequestIDCallsOnce(t *testing.T) {
	n := 0
	f := &fake{list: []wire.CloudInstance{inst(refA, "192.0.2.10")}, action: func(a Action) (wire.CloudActionResult, error) {
		n++
		return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted, Message: fmt.Sprintf("call %d", n)}, nil
	}}
	p := newProvider(f, newClock(), customs)
	ctx := context.Background()
	first, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStart, "req-1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStart, "req-1"))
	if err != nil {
		t.Fatal(err)
	}
	if f.actionCalls() != 1 {
		t.Fatalf("adapter called %d times, want 1", f.actionCalls())
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) || first.Message != "call 1" {
		t.Fatalf("answers differ: %s vs %s", a, b)
	}
	valid(t, "cloud.action", second)
	if got := f.actions[0]; got.RequestID != "req-1" || got.Kind != wire.CloudActionKindStart || got.Ref != refA || got.Locale != "en" {
		t.Fatalf("the adapter heard %+v", got)
	}
	// Another id is another request.
	if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStart, "req-2")); err != nil {
		t.Fatal(err)
	}
	if f.actionCalls() != 2 {
		t.Fatalf("adapter called %d times, want 2", f.actionCalls())
	}
}

func TestActionConcurrentDuplicatesCallOnce(t *testing.T) {
	const N = 32
	f := &fake{gate: make(chan struct{}), entered: make(chan struct{}, N)}
	c := newLookupClock()
	p := newProvider(f, nil, customs)
	p.now = c.Now
	var wg sync.WaitGroup
	results := make([]wire.CloudActionResult, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = p.Action(context.Background(), actParams(refA, wire.CloudActionKindReboot, "dup"))
		}(i)
	}
	<-f.entered // one call reached the adapter
	// The owner reads the clock twice (lookup, record), every other call once for its lookup under the lock. The
	// owner is held at the gate, so all N-1 others found its unfinished record and wait on it.
	c.wait(N + 1)
	close(f.gate)
	wg.Wait()
	if n := f.actionCalls(); n != 1 {
		t.Fatalf("adapter called %d times, want 1", n)
	}
	for i := range results {
		if errs[i] != nil || results[i].Status != wire.CloudActionStatusAccepted {
			t.Fatalf("call %d: %+v %v", i, results[i], errs[i])
		}
	}
}

func TestActionSameIDDifferentRequestFails(t *testing.T) {
	f := &fake{}
	p := newProvider(f, nil, customs)
	ctx := context.Background()
	if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStart, "r")); err != nil {
		t.Fatal(err)
	}
	for name, params := range map[string]wire.CloudActionParams{
		"other kind":     actParams(refA, wire.CloudActionKindStop, "r"),
		"other instance": actParams(refB, wire.CloudActionKindStart, "r"),
	} {
		_, err := p.Action(ctx, params)
		wantCode(t, err, wire.CloudErrorCodeFailed)
		if !strings.Contains(err.Error(), "already used") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	snap := actParams(refA, wire.CloudActionKindSnapshotCreate, "s")
	snap.Name = "before upgrade"
	if _, err := p.Action(ctx, snap); err != nil {
		t.Fatal(err)
	}
	snap.Name = "another name"
	_, err := p.Action(ctx, snap)
	wantCode(t, err, wire.CloudErrorCodeFailed)
	custom := actParams(refA, wire.CloudActionKindCustom, "c")
	custom.Custom = "renew"
	if _, err := p.Action(ctx, custom); err != nil {
		t.Fatal(err)
	}
	custom.Custom = "wipe"
	_, err = p.Action(ctx, custom)
	wantCode(t, err, wire.CloudErrorCodeFailed)
	if n := f.actionCalls(); n != 3 {
		t.Fatalf("adapter called %d times, want 3", n)
	}
}

// A write that timed out, or that the vendor answered with a server error, may have been done: the same request_id
// is answered with the same failure and the vendor is not asked twice.
func TestActionUncertainFailureIsRecorded(t *testing.T) {
	for name, uncertain := range map[string]error{
		"deadline":         context.DeadlineExceeded,
		"wrapped canceled": fmt.Errorf("asking: %w", context.Canceled),
		"5xx":              StatusError("Example", 503, "busy"),
	} {
		t.Run(name, func(t *testing.T) {
			f := &fake{action: func(Action) (wire.CloudActionResult, error) { return wire.CloudActionResult{}, uncertain }}
			p := newProvider(f, nil, customs)
			for i := 0; i < 2; i++ {
				_, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindReboot, "u"))
				wantCode(t, err, wire.CloudErrorCodeFailed)
			}
			if n := f.actionCalls(); n != 1 {
				t.Fatalf("adapter called %d times, want 1", n)
			}
		})
	}
}

func TestActionTransientErrorIsNotRecorded(t *testing.T) {
	for name, transient := range map[string]error{
		"rate_limited": RateLimited("slow down"),
		"transport":    &Error{Code: wire.CloudErrorCodeFailed, Message: "api.example.com could not be reached", Transient: true},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			f := &fake{action: func(a Action) (wire.CloudActionResult, error) {
				calls++
				if calls == 1 {
					return wire.CloudActionResult{}, transient
				}
				return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}, nil
			}}
			p := newProvider(f, nil, customs)
			_, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindStart, "t"))
			want := AsError(transient).Code
			wantCode(t, err, want)
			res, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindStart, "t"))
			if err != nil || res.Status != wire.CloudActionStatusAccepted {
				t.Fatalf("retry: %+v %v", res, err)
			}
			// Now decided: a third try is answered from the record.
			if _, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindStart, "t")); err != nil {
				t.Fatal(err)
			}
			if f.actionCalls() != 2 {
				t.Fatalf("adapter called %d times, want 2", f.actionCalls())
			}
		})
	}
}

func TestActionWaiterRetriesAfterTransientFirst(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	f := &fake{gate: make(chan struct{}), entered: make(chan struct{}, 4)}
	f.action = func(a Action) (wire.CloudActionResult, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return wire.CloudActionResult{}, RateLimited("throttled")
		}
		return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}, nil
	}
	c := newLookupClock()
	p := newProvider(f, nil, customs)
	p.now = c.Now
	var wg sync.WaitGroup
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, firstErr = p.Action(context.Background(), actParams(refA, wire.CloudActionKindStart, "w"))
	}()
	<-f.entered
	c.wait(2) // the owner's lookup and its record
	var second wire.CloudActionResult
	var secondErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		second, secondErr = p.Action(context.Background(), actParams(refA, wire.CloudActionKindStart, "w"))
	}()
	// The second call's lookup, under the provider's lock, while the owner is held at the gate: it found the
	// record and waits on it.
	c.wait(1)
	close(f.gate)
	wg.Wait()
	wantCode(t, firstErr, wire.CloudErrorCodeRateLimited)
	if secondErr != nil || second.Status != wire.CloudActionStatusAccepted {
		t.Fatalf("the waiter: %+v %v", second, secondErr)
	}
	if n := f.actionCalls(); n != 2 {
		t.Fatalf("adapter called %d times, want 2", n)
	}
	// The waiter looked again after the transient answer and made its own record: 2 + 1 + 2.
	if n := c.count(); n != 5 {
		t.Fatalf("%d clock reads, want 5 (the waiter did not retry through its own record)", n)
	}
}

func TestActionWaiterHonoursItsContext(t *testing.T) {
	f := &fake{gate: make(chan struct{}), entered: make(chan struct{}, 2)}
	p := newProvider(f, nil, customs)
	done := make(chan error, 1)
	go func() {
		_, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindStart, "x"))
		done <- err
	}()
	<-f.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStart, "x"))
	wantCode(t, err, wire.CloudErrorCodeFailed)
	close(f.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := f.actionCalls(); n != 1 {
		t.Fatalf("adapter called %d times, want 1", n)
	}
}

func TestActionDefiniteErrorIsRecorded(t *testing.T) {
	for _, e := range []*Error{
		InvalidState("it is already running"), Capacity("no capacity in region-1a"), Refused("the plan forbids it"),
		NotFound("gone"), Unsupported("not on this plan"), NotSignedIn("the key expired"),
		Failed("something else"), StatusError("Example", 400, "bad request"),
	} {
		t.Run(string(e.Code), func(t *testing.T) {
			f := &fake{action: func(Action) (wire.CloudActionResult, error) { return wire.CloudActionResult{}, e }}
			p := newProvider(f, nil, customs)
			for i := 0; i < 3; i++ {
				_, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindStop, "d"))
				wantCode(t, err, e.Code)
				if err.Error() != e.Message {
					t.Fatalf("message %q, want %q", err.Error(), e.Message)
				}
			}
			if n := f.actionCalls(); n != 1 {
				t.Fatalf("adapter called %d times, want 1", n)
			}
		})
	}
	// A refused answer (not an error) is recorded as well.
	f := &fake{action: func(Action) (wire.CloudActionResult, error) {
		return wire.CloudActionResult{Status: wire.CloudActionStatusRefused, Message: "the plan forbids it"}, nil
	}}
	p := newProvider(f, nil, customs)
	for i := 0; i < 2; i++ {
		res, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindStop, "r"))
		if err != nil || res.Status != wire.CloudActionStatusRefused || res.Message != "the plan forbids it" {
			t.Fatalf("%+v %v", res, err)
		}
	}
	if f.actionCalls() != 1 {
		t.Fatalf("adapter called %d times", f.actionCalls())
	}
}

func TestReadActionsAreNeverDeduplicated(t *testing.T) {
	f := &fake{list: []wire.CloudInstance{inst(refA, "192.0.2.10")}}
	p := newProvider(f, nil, customs)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindSyncIp, "same")); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindConsole, "same")); err != nil {
			t.Fatal(err)
		}
		probe := actParams(refA, wire.CloudActionKindCustom, "same")
		probe.Custom = "probe"
		if _, err := p.Action(ctx, probe); err != nil {
			t.Fatal(err)
		}
	}
	_, nInstance, nAction, _ := f.counts()
	if nInstance != 6 || nAction != 3 {
		t.Fatalf("instance %d (want 6), action %d (want 3)", nInstance, nAction)
	}
	// A confirm custom action is deduplicated, a destructive one too.
	for _, id := range []wire.CloudCustomActionID{"renew", "wipe"} {
		for i := 0; i < 2; i++ {
			c := actParams(refA, wire.CloudActionKindCustom, "c-"+string(id))
			c.Custom = id
			if _, err := p.Action(ctx, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	if f.actionCalls() != 5 {
		t.Fatalf("action %d, want 5", f.actionCalls())
	}
	if got := f.actions[4]; got.Kind != wire.CloudActionKindCustom || got.Custom != "wipe" {
		t.Fatalf("the adapter heard %+v", got)
	}
}

func TestActionRecordsExpire(t *testing.T) {
	c := newClock()
	f := &fake{}
	p := newProvider(f, c, customs)
	ctx := context.Background()
	call := func(id string) {
		t.Helper()
		if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStart, id)); err != nil {
			t.Fatal(err)
		}
	}
	call("e")
	c.Add(requestTTL - time.Second)
	call("e")
	if f.actionCalls() != 1 {
		t.Fatalf("within the TTL the adapter was called %d times", f.actionCalls())
	}
	c.Add(2 * time.Second)
	call("e")
	if f.actionCalls() != 2 {
		t.Fatalf("past the TTL the adapter was called %d times, want 2", f.actionCalls())
	}
}

func TestActionRecordsCapEvictsOldest(t *testing.T) {
	c := newClock()
	f := &fake{}
	p := newProvider(f, c, customs)
	ctx := context.Background()
	call := func(id string) {
		t.Helper()
		if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStart, id)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < maxRecords; i++ {
		call(fmt.Sprint("id-", i))
		c.Add(time.Millisecond)
	}
	if f.actionCalls() != maxRecords {
		t.Fatalf("calls %d", f.actionCalls())
	}
	call("id-new") // the cap is reached: id-0 goes
	p.mu.Lock()
	n := len(p.records)
	_, kept0 := p.records["id-0"]
	_, kept1 := p.records["id-1"]
	p.mu.Unlock()
	if n > maxRecords || kept0 || !kept1 {
		t.Fatalf("records %d, id-0 kept %v, id-1 kept %v", n, kept0, kept1)
	}
	before := f.actionCalls()
	call("id-0") // forgotten: asked again
	if f.actionCalls() != before+1 {
		t.Fatal("an evicted id was answered from a record")
	}
	call("id-5") // still remembered
	if f.actionCalls() != before+1 {
		t.Fatal("a recent id was asked again")
	}
}

func TestSyncIPReadsTheVendorEachTime(t *testing.T) {
	var mu sync.Mutex
	ip := "192.0.2.10"
	f := &fake{}
	f.list = []wire.CloudInstance{inst(refA, ip)}
	f.instance = func(ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
		mu.Lock()
		defer mu.Unlock()
		return inst(ref, ip), nil
	}
	p := newProvider(f, newClock(), customs)
	ctx := context.Background()
	if _, err := p.Instances(ctx, wire.CloudInstancesParams{Locale: "en"}); err != nil { // fills the list cache
		t.Fatal(err)
	}
	res, err := p.Action(ctx, actParams(refA, wire.CloudActionKindSyncIp, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "cloud.action", res)
	if res.Status != wire.CloudActionStatusDone || res.Instance == nil || res.Instance.PublicIps[0] != "192.0.2.10" {
		t.Fatalf("%+v", res)
	}
	// A start gave it a new address.
	if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStart, "start-1")); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	ip = "198.51.100.20"
	mu.Unlock()
	res, err = p.Action(ctx, actParams(refA, wire.CloudActionKindSyncIp, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Instance == nil || res.Instance.PublicIps[0] != "198.51.100.20" {
		t.Fatalf("sync_ip answered %+v, want the new address", res.Instance)
	}
	in, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: refA})
	if err != nil || in.PublicIps[0] != "198.51.100.20" {
		t.Fatalf("Instance answered %+v %v", in, err)
	}
	valid(t, "cloud.instance", in)
	if f.actions[0].Kind != wire.CloudActionKindStart || f.actionCalls() != 1 {
		t.Fatalf("the adapter's Action heard %+v", f.actions)
	}
}

func TestSyncIPOfAnotherInstanceFails(t *testing.T) {
	f := &fake{instance: func(ref wire.CloudInstanceRef) (wire.CloudInstance, error) { return inst(refB, "192.0.2.10"), nil }}
	p := newProvider(f, nil, customs)
	_, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindSyncIp, "s"))
	wantCode(t, err, wire.CloudErrorCodeNotFound)
	f.instance = func(ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
		return wire.CloudInstance{}, NotSignedIn("expired")
	}
	_, err = p.Action(context.Background(), actParams(refA, wire.CloudActionKindSyncIp, "s"))
	wantCode(t, err, wire.CloudErrorCodeNotSignedIn)
}

func TestConsoleViaConsoler(t *testing.T) {
	exp := time.Date(2026, 3, 1, 12, 5, 0, 0, time.FixedZone("x", 3600))
	c := &consoleFake{fake: &fake{}}
	c.console = func(ref wire.CloudInstanceRef, locale string) (wire.CloudActionResult, error) {
		return wire.CloudActionResult{URL: "https://console.example.com/session?signature=one-time-0001", ExpiresAt: exp}, nil
	}
	p := newProvider(c, nil, customs, "test-secret-key-0001")
	res, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindConsole, "c"))
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "cloud.action", res)
	if res.Status != wire.CloudActionStatusDone || res.URL != "https://console.example.com/session?signature=one-time-0001" ||
		!res.ExpiresAt.Equal(exp) || res.ExpiresAt.Location() != time.UTC {
		t.Fatalf("%+v", res)
	}
	_, nInstance, nAction, _ := c.counts()
	if nInstance != 0 || nAction != 0 || c.nConsole != 1 {
		t.Fatalf("instance %d action %d console %d", nInstance, nAction, c.nConsole)
	}
	for name, u := range map[string]string{
		"http":     "http://console.example.com/session",
		"userinfo": "https://user:pass@console.example.com/",
		"secret":   "https://console.example.com/?key=test-secret-key-0001",
		"empty":    "",
	} {
		c.console = func(wire.CloudInstanceRef, string) (wire.CloudActionResult, error) {
			return wire.CloudActionResult{URL: u, ExpiresAt: exp}, nil
		}
		_, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindConsole, "c"))
		wantCode(t, err, wire.CloudErrorCodeUnsupported)
		noLeak(t, err.Error(), "test-secret-key-0001")
		_ = name
	}
	c.console = func(wire.CloudInstanceRef, string) (wire.CloudActionResult, error) {
		return wire.CloudActionResult{}, NotFound("no such server")
	}
	_, err = p.Action(context.Background(), actParams(refA, wire.CloudActionKindConsole, "c"))
	wantCode(t, err, wire.CloudErrorCodeNotFound)
	// With a Consoler, an instance without console_url still offers console.
	in := inst(refA, "192.0.2.10")
	in.ConsoleURL = ""
	c.list = []wire.CloudInstance{in}
	got, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if err != nil || !Offers(got.Actions, wire.CloudActionKindConsole) {
		t.Fatalf("%+v %v", got.Actions, err)
	}
}

func TestConsoleFallsBackToConsoleURL(t *testing.T) {
	f := &fake{list: []wire.CloudInstance{inst(refA, "192.0.2.10")}}
	p := newProvider(f, nil, customs, "test-secret-key-0001")
	res, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindConsole, "c"))
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "cloud.action", res)
	if res.Status != wire.CloudActionStatusDone || res.URL != "https://console.example.com/instances/i-0001" {
		t.Fatalf("%+v", res)
	}
	for _, u := range []string{"", "http://console.example.com/x", "https://console.example.com/?k=test-secret-key-0001",
		"https://u:p@console.example.com/", "https://console.example.com/a b"} {
		in := inst(refA, "192.0.2.10")
		in.ConsoleURL = u
		f.mu.Lock()
		f.list = []wire.CloudInstance{in}
		f.mu.Unlock()
		_, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindConsole, "c"))
		wantCode(t, err, wire.CloudErrorCodeUnsupported)
		got, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
		if err != nil || got.ConsoleURL != "" || Offers(got.Actions, wire.CloudActionKindConsole) {
			t.Fatalf("%q: %+v %v", u, got, err)
		}
	}
}

func TestActionRefusedBeforeTheAdapter(t *testing.T) {
	f := &fake{list: []wire.CloudInstance{inst(refA, "192.0.2.10")}}
	p := newProvider(f, nil, customs)
	ctx := context.Background()
	cases := []struct {
		name   string
		params wire.CloudActionParams
		code   wire.CloudErrorCode
	}{
		{"snapshot_list", actParams(refA, wire.CloudActionKindSnapshotList, "a"), wire.CloudErrorCodeUnsupported},
		{"unknown kind", actParams(refA, "explode", "a"), wire.CloudErrorCodeUnsupported},
		{"empty kind", actParams(refA, "", "a"), wire.CloudErrorCodeUnsupported},
		{"undeclared custom", func() wire.CloudActionParams {
			p := actParams(refA, wire.CloudActionKindCustom, "a")
			p.Custom = "launch_missiles"
			return p
		}(), wire.CloudErrorCodeUnsupported},
		{"custom without id", actParams(refA, wire.CloudActionKindCustom, "a"), wire.CloudErrorCodeUnsupported},
		{"standard kind with custom id", func() wire.CloudActionParams {
			p := actParams(refA, wire.CloudActionKindStart, "a")
			p.Custom = "renew"
			return p
		}(), wire.CloudErrorCodeUnsupported},
		{"no request id", actParams(refA, wire.CloudActionKindStart, ""), wire.CloudErrorCodeFailed},
		{"long request id", actParams(refA, wire.CloudActionKindStart, strings.Repeat("r", 257)), wire.CloudErrorCodeFailed},
		{"no account", actParams(wire.CloudInstanceRef{ID: "i-1"}, wire.CloudActionKindStart, "a"), wire.CloudErrorCodeNotFound},
		{"no id", actParams(wire.CloudInstanceRef{Account: "prod"}, wire.CloudActionKindStart, "a"), wire.CloudErrorCodeNotFound},
		{"control char", actParams(wire.CloudInstanceRef{Account: "prod", ID: "i-1\n"}, wire.CloudActionKindStart, "a"), wire.CloudErrorCodeNotFound},
		{"long id", actParams(wire.CloudInstanceRef{Account: "prod", ID: wire.CloudInstanceID(strings.Repeat("i", 513))}, wire.CloudActionKindStart, "a"), wire.CloudErrorCodeNotFound},
	}
	for _, c := range cases {
		_, err := p.Action(ctx, c.params)
		if got := wireCode(t, err); got != c.code {
			t.Errorf("%s: code %q, want %q", c.name, got, c.code)
		}
	}
	// zh words for a Chinese locale.
	zh := actParams(refA, wire.CloudActionKindSnapshotList, "a")
	zh.Locale = "zh-CN"
	_, err := p.Action(ctx, zh)
	if err == nil || err.Error() != "不支持这个操作。" {
		t.Fatalf("zh: %v", err)
	}
	_, nInstance, nAction, _ := f.counts()
	if nInstance != 0 || nAction != 0 {
		t.Fatalf("the adapter was asked: instance %d action %d", nInstance, nAction)
	}
	// 64 characters, the schema's CloudRequestID maxLength, is still a usable id.
	if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStart, strings.Repeat("r", 64))); err != nil {
		t.Fatal(err)
	}
}

func TestBugRequestIDLongerThanSchemaAccepted(t *testing.T) {
	f := &fake{}
	p := newProvider(f, nil, customs)
	_, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindStart, strings.Repeat("r", 65)))
	wantCode(t, err, wire.CloudErrorCodeFailed)
	if n := f.actionCalls(); n != 0 {
		t.Fatalf("the adapter was asked %d times", n)
	}
}

func TestActionPassesNameOnlyForSnapshotCreate(t *testing.T) {
	f := &fake{}
	p := newProvider(f, nil, customs)
	s := actParams(refA, wire.CloudActionKindSnapshotCreate, "s")
	s.Name = "  before\nupgrade\t" + strings.Repeat("x", 300)
	if _, err := p.Action(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	st := actParams(refA, wire.CloudActionKindStart, "t")
	st.Name = "ignored"
	if _, err := p.Action(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	got := f.actions[0].Name
	if strings.ContainsAny(got, "\n\t") || len([]rune(got)) != maxRequestName || !strings.HasPrefix(got, "before upgrade x") {
		t.Fatalf("snapshot name %q", got)
	}
	if f.actions[1].Name != "" {
		t.Fatalf("start heard name %q", f.actions[1].Name)
	}
}

func TestActionResultIsCleaned(t *testing.T) {
	f := &fake{}
	p := newProvider(f, nil, customs, "test-secret-key-0001")
	other := inst(refB, "192.0.2.10")
	mine := inst(refA, "192.0.2.11")
	f.action = func(a Action) (wire.CloudActionResult, error) {
		res := wire.CloudActionResult{Status: "maybe", URL: "https://console.example.com/x", ExpiresAt: time.Now(),
			Snapshot: &wire.CloudSnapshot{ID: "snap-1", State: wire.CloudSnapshotStateCreating},
			Message:  "line one\nline two test-secret-key-0001 " + strings.Repeat("y", 400)}
		if a.RequestID == "other" {
			res.Instance = &other
		} else {
			res.Instance = &mine
		}
		return res, nil
	}
	res, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindStart, "other"))
	if err != nil {
		t.Fatal(err)
	}
	raw := valid(t, "cloud.action", res)
	noLeak(t, string(raw), "test-secret-key-0001")
	if res.Status != wire.CloudActionStatusAccepted || res.Instance != nil || res.Snapshot != nil || res.URL != "" ||
		!res.ExpiresAt.IsZero() || strings.Contains(res.Message, "\n") || len([]rune(res.Message)) > maxMessage {
		t.Fatalf("%+v", res)
	}
	res, err = p.Action(context.Background(), actParams(refA, wire.CloudActionKindStart, "mine"))
	if err != nil || res.Instance == nil || res.Instance.ID != refA.ID {
		t.Fatalf("%+v %v", res, err)
	}
	// snapshot_create keeps its snapshot.
	res, err = p.Action(context.Background(), actParams(refA, wire.CloudActionKindSnapshotCreate, "snap"))
	if err != nil || res.Snapshot == nil || res.Snapshot.ID != "snap-1" {
		t.Fatalf("%+v %v", res, err)
	}
	valid(t, "cloud.action", res)
}

func TestWriteInvalidatesTheList(t *testing.T) {
	c := newClock()
	f := &fake{list: []wire.CloudInstance{inst(refA, "192.0.2.10")}}
	p := newProvider(f, c, customs)
	ctx := context.Background()
	list := func() {
		t.Helper()
		if _, err := p.Instances(ctx, wire.CloudInstancesParams{Locale: "en"}); err != nil {
			t.Fatal(err)
		}
	}
	list()
	list()
	if n, _, _, _ := f.counts(); n != 1 {
		t.Fatalf("list asked %d times", n)
	}
	if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindSyncIp, "r")); err != nil { // a read keeps it
		t.Fatal(err)
	}
	list()
	if n, _, _, _ := f.counts(); n != 1 {
		t.Fatalf("a read dropped the list: %d", n)
	}
	if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStop, "w")); err != nil {
		t.Fatal(err)
	}
	list()
	if n, _, _, _ := f.counts(); n != 2 {
		t.Fatalf("after a write the list was asked %d times, want 2", n)
	}
	// A failed write invalidates too: the vendor may have done part of it.
	f.action = func(Action) (wire.CloudActionResult, error) { return wire.CloudActionResult{}, InvalidState("no") }
	if _, err := p.Action(ctx, actParams(refA, wire.CloudActionKindStop, "w2")); err == nil {
		t.Fatal("want an error")
	}
	list()
	if n, _, _, _ := f.counts(); n != 3 {
		t.Fatalf("after a failed write the list was asked %d times, want 3", n)
	}
}

func TestWriteDuringListingIsNotCached(t *testing.T) {
	f := &fake{list: []wire.CloudInstance{inst(refA, "192.0.2.10")}}
	p := newProvider(f, newClock(), customs)
	// Simulate a write that lands while the list is read: bump the generation inside Instances.
	g := &listHook{fake: f, during: func() { p.forget() }}
	p.adapter = g
	if _, err := p.Instances(context.Background(), wire.CloudInstancesParams{}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	cached := p.list != nil
	p.mu.Unlock()
	if cached {
		t.Fatal("a list read across a write was cached")
	}
}

type listHook struct {
	*fake
	during func()
}

func (l *listHook) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	l.during()
	return l.fake.Instances(ctx)
}

func TestInstancesCache(t *testing.T) {
	c := newClock()
	f := &fake{list: []wire.CloudInstance{inst(refA, "192.0.2.10"), inst(refB, "192.0.2.11")}}
	p := newProvider(f, c, customs)
	ctx := context.Background()
	get := func(refresh bool) wire.CloudInstanceList {
		t.Helper()
		l, err := p.Instances(ctx, wire.CloudInstancesParams{Locale: "en", Refresh: refresh})
		if err != nil {
			t.Fatal(err)
		}
		valid(t, "cloud.instances", l)
		return l
	}
	first := get(false)
	if len(first.Instances) != 2 || first.Partial || first.Detail != "" {
		t.Fatalf("%+v", first)
	}
	c.Add(14 * time.Second)
	get(false)
	if n, _, _, _ := f.counts(); n != 1 {
		t.Fatalf("asked %d times within 15 s", n)
	}
	get(true)
	if n, _, _, _ := f.counts(); n != 2 {
		t.Fatalf("refresh did not ask: %d", n)
	}
	c.Add(14 * time.Second)
	get(false) // the refreshed list is fresh
	if n, _, _, _ := f.counts(); n != 2 {
		t.Fatalf("asked %d times", n)
	}
	c.Add(time.Second)
	get(false)
	if n, _, _, _ := f.counts(); n != 3 {
		t.Fatalf("a 15 s old list was answered: %d", n)
	}
}

func TestInstancesPartialIsNotCachedAndProblemsSorted(t *testing.T) {
	f := &fake{list: []wire.CloudInstance{inst(refA, "192.0.2.10")}, problems: []string{"staging", "eu-west", "lab"}}
	p := newProvider(f, newClock(), customs, "test-secret-key-0001")
	ctx := context.Background()
	l, err := p.Instances(ctx, wire.CloudInstancesParams{Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "cloud.instances", l)
	if !l.Partial || l.Detail != "Could not read: eu-west, lab, staging" || len(l.Instances) != 1 {
		t.Fatalf("%+v", l)
	}
	if _, err := p.Instances(ctx, wire.CloudInstancesParams{Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	if n, _, _, _ := f.counts(); n != 2 {
		t.Fatalf("a partial list was cached: %d", n)
	}
	l, _ = p.Instances(ctx, wire.CloudInstancesParams{Locale: "zh-Hans"})
	if l.Detail != "未能读取：eu-west, lab, staging" {
		t.Fatalf("zh detail %q", l.Detail)
	}
	// A problem that carries a credential is redacted.
	f.problems = []string{"acct test-secret-key-0001"}
	l, _ = p.Instances(ctx, wire.CloudInstancesParams{Locale: "en"})
	noLeak(t, l.Detail, "test-secret-key-0001")
	// Many problems are clipped to the schema.
	f.problems = nil
	for i := 0; i < 100; i++ {
		f.problems = append(f.problems, fmt.Sprint("account-", i))
	}
	l, _ = p.Instances(ctx, wire.CloudInstancesParams{Locale: "en"})
	valid(t, "cloud.instances", l)
}

func TestInstancesDropsDuplicatesAndBadRefsAndClips(t *testing.T) {
	var list []wire.CloudInstance
	list = append(list, inst(refA, "192.0.2.10"), inst(refA, "192.0.2.99"))
	list = append(list, wire.CloudInstance{ID: "no-account", State: wire.CloudInstanceStateRunning})
	list = append(list, wire.CloudInstance{Account: "prod", State: wire.CloudInstanceStateRunning})
	list = append(list, wire.CloudInstance{Account: "prod", ID: "bad\x00id", State: wire.CloudInstanceStateRunning})
	for i := 0; i < 600; i++ {
		list = append(list, inst(wire.CloudInstanceRef{Account: "bulk", ID: wire.CloudInstanceID(fmt.Sprint("i-", i))}, "203.0.113.5"))
	}
	f := &fake{list: list}
	p := newProvider(f, newClock(), customs)
	l, err := p.Instances(context.Background(), wire.CloudInstancesParams{Locale: "en"})
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "cloud.instances", l)
	if len(l.Instances) != MaxInstances || !l.Partial || l.Detail != "Only the first 500 servers are listed." {
		t.Fatalf("%d instances, partial %v, detail %q", len(l.Instances), l.Partial, l.Detail)
	}
	if l.Instances[0].PublicIps[0] != "192.0.2.10" || l.Instances[1].Account != "bulk" {
		t.Fatalf("the duplicate or a bad ref was kept: %+v", l.Instances[:2])
	}
	f.problems = []string{"lab"}
	l, _ = p.Instances(context.Background(), wire.CloudInstancesParams{Locale: "zh"})
	if l.Detail != "只列出前 500 台服务器。 未能读取：lab" {
		t.Fatalf("detail %q", l.Detail)
	}
}

func TestInstancesError(t *testing.T) {
	f := &fake{listErr: NotSignedIn("the key test-secret-key-0001 was refused\nretry")}
	p := newProvider(f, nil, customs, "test-secret-key-0001")
	_, err := p.Instances(context.Background(), wire.CloudInstancesParams{})
	wantCode(t, err, wire.CloudErrorCodeNotSignedIn)
	noLeak(t, err.Error(), "test-secret-key-0001")
	if err.Error() != "the key [redacted] was refused retry" {
		t.Fatalf("%q", err.Error())
	}
}

func TestInstanceIsHeldToItsRef(t *testing.T) {
	f := &fake{}
	p := newProvider(f, nil, customs)
	ctx := context.Background()
	for name, answer := range map[string]wire.CloudInstance{
		"other id":      inst(refB, "192.0.2.10"),
		"other account": inst(wire.CloudInstanceRef{Account: "staging", ID: refA.ID}, "192.0.2.10"),
		"empty":         {},
	} {
		f.instance = func(wire.CloudInstanceRef) (wire.CloudInstance, error) { return answer, nil }
		_, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: refA, Locale: "en"})
		wantCode(t, err, wire.CloudErrorCodeNotFound)
		_ = name
	}
	f.instance = nil
	before := f.nInstance
	for _, ref := range []wire.CloudInstanceRef{{}, {Account: "prod"}, {ID: "i-1"}, {Account: "pr\tod", ID: "i"},
		{Account: "prod", ID: "i\x7f"}, {Account: wire.CloudAccountID(strings.Repeat("a", 513)), ID: "i"}} {
		_, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
		wantCode(t, err, wire.CloudErrorCodeNotFound)
		_, err = p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: ref})
		wantCode(t, err, wire.CloudErrorCodeNotFound)
	}
	if f.nInstance != before || f.nSnapshots != 0 {
		t.Fatal("an invalid ref reached the adapter")
	}
	f.instance = func(wire.CloudInstanceRef) (wire.CloudInstance, error) {
		return wire.CloudInstance{}, Capacity("none left")
	}
	_, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: refA})
	wantCode(t, err, wire.CloudErrorCodeCapacity)
}

func TestSnapshots(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var snaps []wire.CloudSnapshot
	snaps = append(snaps, wire.CloudSnapshot{Name: "no id", State: wire.CloudSnapshotStateReady})
	for i := 0; i < 250; i++ {
		snaps = append(snaps, wire.CloudSnapshot{ID: wire.CloudSnapshotID(fmt.Sprint("snap-", i)), Name: fmt.Sprint("n", i),
			State: wire.CloudSnapshotStateReady, CreatedAt: base.Add(time.Duration(i) * time.Hour), SizeGb: 10})
	}
	snaps = append(snaps, wire.CloudSnapshot{ID: "snap-249", Name: "dup", State: wire.CloudSnapshotStateReady, CreatedAt: base.Add(9999 * time.Hour)})
	snaps = append(snaps, wire.CloudSnapshot{ID: "odd", Name: "a\nb test-secret-key-0001 " + strings.Repeat("z", 300), State: "weird", SizeGb: -3,
		CreatedAt: base.Add(5000 * time.Hour).In(time.FixedZone("y", -7200))})
	f := &fake{snaps: snaps}
	p := newProvider(f, nil, customs, "test-secret-key-0001")
	l, err := p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: refA})
	if err != nil {
		t.Fatal(err)
	}
	raw := valid(t, "cloud.snapshots", l)
	noLeak(t, string(raw), "test-secret-key-0001")
	if len(l.Snapshots) != MaxSnapshots {
		t.Fatalf("%d snapshots", len(l.Snapshots))
	}
	odd := l.Snapshots[0]
	if odd.ID != "odd" || odd.State != wire.CloudSnapshotStateCreating || odd.SizeGb != 0 || odd.CreatedAt.Location() != time.UTC ||
		strings.Contains(odd.Name, "\n") || len([]rune(odd.Name)) > maxSnapshotName {
		t.Fatalf("%+v", odd)
	}
	if l.Snapshots[1].ID != "snap-249" || l.Snapshots[1].Name != "n249" {
		t.Fatalf("the duplicate replaced the first: %+v", l.Snapshots[1])
	}
	for i := 1; i < len(l.Snapshots); i++ {
		if l.Snapshots[i].CreatedAt.After(l.Snapshots[i-1].CreatedAt) || l.Snapshots[i].ID == "" {
			t.Fatalf("not newest first at %d", i)
		}
	}
	f.snapErr = Unsupported("no snapshots here")
	_, err = p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: refA})
	wantCode(t, err, wire.CloudErrorCodeUnsupported)
	f.snapErr = nil
	f.snaps = nil
	l, err = p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: refA})
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "cloud.snapshots", l)
}

func TestErrorsLeaveAsExtErrors(t *testing.T) {
	secret := "test-secret-key-0001"
	pem := "-----BEGIN TEST KEY-----\nQUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo=\nYWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo=\n-----END TEST KEY-----\n"
	cases := []struct {
		err  error
		code wire.CloudErrorCode
		msg  string
	}{
		{Failed("key %s refused\nsecond line", secret), wire.CloudErrorCodeFailed, "key [redacted] refused second line"},
		{&Error{Code: "made_up", Message: "odd"}, wire.CloudErrorCodeFailed, "odd"},
		{&Error{Code: wire.CloudErrorCodeRefused}, wire.CloudErrorCodeRefused, "refused"},
		{errors.New("plain " + secret), wire.CloudErrorCodeFailed, "plain [redacted]"},
		{fmt.Errorf("wrapped: %w", InvalidState("busy")), wire.CloudErrorCodeInvalidState, "busy"},
		{Failed("body: YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo= end"), wire.CloudErrorCodeFailed, "body: [redacted] end"},
		{Failed("%s", strings.Repeat("long ", 200)), wire.CloudErrorCodeFailed, ""},
	}
	for _, c := range cases {
		f := &fake{action: func(Action) (wire.CloudActionResult, error) { return wire.CloudActionResult{}, c.err }}
		s := &secretFake{fake: f, secrets: []string{pem}}
		p := newProvider(s, nil, customs, secret)
		_, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindStop, "e"))
		var e *ext.Error
		if !errors.As(err, &e) {
			t.Fatalf("%T", err)
		}
		wantCode(t, err, c.code)
		noLeak(t, e.Message, secret, "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo=", "YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo=")
		if c.msg != "" && e.Message != c.msg {
			t.Errorf("message %q, want %q", e.Message, c.msg)
		}
		if len(e.Data) != 0 {
			t.Errorf("data %s", e.Data)
		}
	}
}

func TestProviderMethodsAndID(t *testing.T) {
	p := newProvider(&fake{}, nil, nil)
	if p.ID() != "test" {
		t.Fatal(p.ID())
	}
	m := p.Methods()
	for _, name := range []string{"instances", "instance", "action", "snapshots"} {
		if m[name] == nil {
			t.Fatalf("no method %s", name)
		}
	}
	// A nil Redactor and nil Custom are fine.
	q := NewProvider(&secretFake{fake: &fake{action: func(Action) (wire.CloudActionResult, error) {
		return wire.CloudActionResult{}, Failed("echo test-secret-key-0002")
	}}, secrets: []string{"test-secret-key-0002"}}, Options{})
	_, err := q.Action(context.Background(), actParams(refA, wire.CloudActionKindStop, "x"))
	noLeak(t, err.Error(), "test-secret-key-0002")
	c := actParams(refA, wire.CloudActionKindCustom, "y")
	c.Custom = "renew"
	_, err = q.Action(context.Background(), c)
	wantCode(t, err, wire.CloudErrorCodeUnsupported)
}
