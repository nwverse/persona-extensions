package core

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

func TestPlainIP(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.1":                "192.0.2.1",
		" 198.51.100.7 ":           "198.51.100.7",
		"2001:db8::1":              "2001:db8::1",
		"2001:DB8:0:0:0:0:0:1":     "2001:db8::1",
		"10.0.0.1":                 "10.0.0.1",
		"192.0.2.1:22":             "",
		"[2001:db8::1]":            "",
		"[2001:db8::1]:22":         "",
		"192.0.2.0/24":             "",
		"2001:db8::/32":            "",
		"fe80::1%eth0":             "",
		"host.example.com":         "",
		"0.0.0.0":                  "",
		"::":                       "",
		"":                         "",
		"192.0.2.256":              "",
		"192.0.2":                  "",
		"https://192.0.2.1/":       "",
		"192.0.2.1\n198.51.100.1":  "",
		"0192.0.2.1":               "",
		"192.0.02.1":               "",
		"203.0.113.9 203.0.113.10": "",
	} {
		if got := PlainIP(in); got != want {
			t.Errorf("PlainIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlainIPs(t *testing.T) {
	var in []string
	in = append(in, "bad", "192.0.2.1", "192.0.2.1", "::ffff:192.0.2.1", "2001:db8::1")
	for i := 0; i < 40; i++ {
		in = append(in, fmt.Sprintf("198.51.100.%d", i))
	}
	got := PlainIPs(in...)
	if len(got) != maxIPs || got[0] != "192.0.2.1" || got[1] != "2001:db8::1" || got[2] != "198.51.100.0" {
		t.Fatalf("%v", got)
	}
	if PlainIPs() != nil || PlainIPs("x", "") != nil {
		t.Fatal("no address is nil")
	}
}

func TestHTTPSURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://console.example.com/i/1":                          "https://console.example.com/i/1",
		" https://console.example.com/i/1?x=1 ":                    "https://console.example.com/i/1?x=1",
		"https://console.example.com:8443/#/vm/":                   "https://console.example.com:8443/#/vm/",
		"https://[2001:db8::1]/":                                   "https://[2001:db8::1]/",
		"http://console.example.com/":                              "",
		"HTTPS://console.example.com/":                             "https://console.example.com/",
		"https://user@console.example.com/":                        "",
		"https://user:pw@console.example.com/":                     "",
		"https://:@console.example.com/":                           "",
		"https:///nohost":                                          "",
		"https:opaque":                                             "",
		"https://console.example.com/a b":                          "",
		"https://console.example.com/a\tb":                         "",
		"https://console.example.com/\x7f":                         "",
		"javascript:alert(1)":                                      "",
		"//console.example.com/":                                   "",
		"":                                                         "",
		"https://console.example.com/" + strings.Repeat("a", 2100): "",
	} {
		if got := HTTPSURL(in, maxConsoleURL); got != want {
			t.Errorf("HTTPSURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstanceIsCleaned(t *testing.T) {
	secret := "test-secret-key-0001"
	var ips []string
	ips = append(ips, "192.0.2.1:22", "192.0.2.0/24", "fe80::1%eth0", "[2001:db8::2]", "host.example.com", "0.0.0.0",
		"::ffff:203.0.113.4", "203.0.113.4", "2001:db8::1")
	for i := 0; i < 30; i++ {
		ips = append(ips, fmt.Sprintf("198.51.100.%d", i))
	}
	obs := time.Date(2026, 2, 3, 4, 5, 6, 789, time.FixedZone("z", 3600))
	raw := wire.CloudInstance{
		Account: refA.Account, ID: refA.ID,
		DisplayName: "web\n" + secret + "\x00" + strings.Repeat("n", 400),
		State:       "exploding",
		StateDetail: strings.Repeat("d\r\n", 400),
		Region:      strings.Repeat("r", 200), Zone: "zone\none",
		PublicIps:  ips,
		PrivateIps: []string{"10.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1:80"},
		Spec:       &wire.CloudSpec{Size: strings.Repeat("s", 100), Cpu: -1, MemoryMb: -5, DiskGb: -1, Arch: strings.Repeat("a", 50), OS: "os\nx"},
		Lifecycle:  &wire.CloudLifecycle{Spot: true, ExpiresAt: obs},
		ConsoleURL: "https://console.example.com/?k=" + secret,
		Actions: []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindStop, wire.CloudActionKindCustom, "teleport",
			wire.CloudActionKindSyncIp, wire.CloudActionKindConsole, wire.CloudActionKindSnapshotList},
		CustomActions: []wire.CloudCustomActionID{"renew", "renew", "launch_missiles", "probe"},
		ObservedAt:    obs,
	}
	f := &fake{list: []wire.CloudInstance{raw}}
	p := newProvider(f, newClock(), customs, secret)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if err != nil {
		t.Fatal(err)
	}
	js := valid(t, "cloud.instance", in)
	noLeak(t, string(js), secret)
	if in.State != wire.CloudInstanceStateUnknown {
		t.Errorf("state %q", in.State)
	}
	if want := []string{"203.0.113.4", "2001:db8::1", "198.51.100.0"}; len(in.PublicIps) != maxIPs || in.PublicIps[0] != want[0] || in.PublicIps[1] != want[1] || in.PublicIps[2] != want[2] {
		t.Errorf("public ips %v", in.PublicIps)
	}
	if fmt.Sprint(in.PrivateIps) != "[10.0.0.1 172.16.0.1]" {
		t.Errorf("private ips %v", in.PrivateIps)
	}
	if in.ConsoleURL != "" {
		t.Errorf("a console url with a credential was kept: %q", in.ConsoleURL)
	}
	if fmt.Sprint(in.Actions) != "[stop sync_ip snapshot_list]" {
		t.Errorf("actions %v (console must go: no link)", in.Actions)
	}
	if fmt.Sprint(in.CustomActions) != "[renew probe]" {
		t.Errorf("custom actions %v", in.CustomActions)
	}
	for name, s := range map[string]string{"display_name": in.DisplayName, "state_detail": in.StateDetail, "region": in.Region,
		"zone": in.Zone, "size": in.Spec.Size, "arch": in.Spec.Arch, "os": in.Spec.OS} {
		if strings.ContainsFunc(s, func(r rune) bool { return r < ' ' }) {
			t.Errorf("%s has a control character: %q", name, s)
		}
	}
	if !strings.HasPrefix(in.DisplayName, "web [redacted] n") || len([]rune(in.DisplayName)) != maxDisplayName {
		t.Errorf("display name %q", in.DisplayName)
	}
	if in.Spec.Cpu != 0 || in.Spec.MemoryMb != 0 || in.Spec.DiskGb != 0 {
		t.Errorf("spec %+v", in.Spec)
	}
	if !in.ObservedAt.Equal(obs.Truncate(time.Second)) || in.ObservedAt.Location() != time.UTC || in.Lifecycle.ExpiresAt.Location() != time.UTC {
		t.Errorf("times %v %v", in.ObservedAt, in.Lifecycle.ExpiresAt)
	}
}

func TestInstanceDefaults(t *testing.T) {
	c := newClock()
	f := &fake{list: []wire.CloudInstance{{Account: refA.Account, ID: refA.ID, State: wire.CloudInstanceStateStopped,
		Spec: &wire.CloudSpec{Cpu: -2}, Lifecycle: &wire.CloudLifecycle{}}}}
	p := newProvider(f, c, customs)
	in, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "cloud.instance", in)
	if in.Spec != nil || in.Lifecycle != nil {
		t.Errorf("an empty spec or lifecycle was kept: %+v %+v", in.Spec, in.Lifecycle)
	}
	if !in.ObservedAt.Equal(c.Now()) {
		t.Errorf("observed_at %v, want now", in.ObservedAt)
	}
	if in.Actions != nil || in.CustomActions != nil || in.PublicIps != nil {
		t.Errorf("%+v", in)
	}
	// A cpu over the schema's maximum is dropped; a fraction is kept.
	f.list[0].Spec = &wire.CloudSpec{Cpu: 5000, MemoryMb: 1024}
	in, _ = p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if in.Spec == nil || in.Spec.Cpu != 0 || in.Spec.MemoryMb != 1024 {
		t.Errorf("spec %+v", in.Spec)
	}
	f.list[0].Spec = &wire.CloudSpec{Cpu: 0.5}
	in, _ = p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if in.Spec == nil || in.Spec.Cpu != 0.5 {
		t.Errorf("spec %+v", in.Spec)
	}
}

// hostileString is one of a set of strings that break naive code.
func hostileString(r *rand.Rand) string {
	pool := []string{
		"", " ", "\n", "\x00", "\x7f", "line\nline", "\t\r\n tab", strings.Repeat("x", 5000), strings.Repeat("é", 400),
		strings.Repeat("😀", 300), "\xff\xfe bad utf8", "‮ rtl", "  sep  ", "test-secret-key-0001",
		"prefix test-secret-key-0001 suffix", "https://console.example.com/ok", "http://console.example.com/x",
		"https://u:p@console.example.com/", "https://console.example.com/?k=test-secret-key-0001", "https://",
		"https://console.example.com/" + strings.Repeat("p", 3000), "192.0.2.1", "2001:db8::1", "::ffff:198.51.100.1",
		"192.0.2.1:80", "[2001:db8::1]", "0.0.0.0", "fe80::1%en0", "10.1.2.3/8", "host.example.com",
	}
	return pool[r.Intn(len(pool))]
}

// plainHostile is hostileString without the credentials: for the fields the core does not redact (see
// TestBugPlaceAndSpecNotRedacted).
func plainHostile(r *rand.Rand) string {
	for {
		if s := hostileString(r); !strings.Contains(s, "test-secret-key-0001") {
			return s
		}
	}
}

// hostileInstance is a random hostile instance of ref. full also uses the inputs the core is known to mishandle
// (see the TestBug* tests): credentials in region, zone and spec, NaN and Inf numbers, and a year-9999 local time.
func hostileInstance(r *rand.Rand, ref wire.CloudInstanceRef, full bool) wire.CloudInstance {
	place := plainHostile
	if full {
		place = hostileString
	}
	kinds := append(append([]wire.CloudActionKind(nil), wire.CloudActionKinds...), "", "teleport", "START")
	var actions []wire.CloudActionKind
	for i := r.Intn(30); i > 0; i-- {
		actions = append(actions, kinds[r.Intn(len(kinds))])
	}
	ids := []wire.CloudCustomActionID{"renew", "probe", "wipe", "", "nope", wire.CloudCustomActionID(strings.Repeat("c", 64))}
	var custom []wire.CloudCustomActionID
	for i := r.Intn(20); i > 0; i-- {
		custom = append(custom, ids[r.Intn(len(ids))])
	}
	var pub, priv []string
	for i := r.Intn(50); i > 0; i-- {
		pub = append(pub, hostileString(r))
		priv = append(priv, fmt.Sprintf("10.0.%d.%d", r.Intn(3), r.Intn(3)))
	}
	states := append(append([]wire.CloudInstanceState(nil), wire.CloudInstanceStates...), "", "RUNNING", "melting")
	in := wire.CloudInstance{
		Account: ref.Account, ID: ref.ID, DisplayName: hostileString(r), State: states[r.Intn(len(states))],
		StateDetail: hostileString(r), Region: place(r), Zone: place(r), PublicIps: pub, PrivateIps: priv,
		ConsoleURL: hostileString(r), Actions: actions, CustomActions: custom,
	}
	cpus := []float64{-1, 0, 0.25, 4096, 4097, 1e9}
	if full {
		cpus = append(cpus, math.NaN(), math.Inf(1), math.Inf(-1))
	}
	if r.Intn(2) == 0 {
		in.Spec = &wire.CloudSpec{Size: place(r), Cpu: cpus[r.Intn(len(cpus))],
			MemoryMb: []int64{-1, 0, 1 << 40}[r.Intn(3)], DiskGb: []int64{-1, 0, 100}[r.Intn(3)], Arch: place(r), OS: place(r)}
	}
	if r.Intn(2) == 0 {
		in.Lifecycle = &wire.CloudLifecycle{Spot: r.Intn(2) == 0, AutoRenew: r.Intn(2) == 0,
			ExpiresAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.FixedZone("q", r.Intn(20)*1800))}
		if full && r.Intn(3) == 0 {
			// A panel's "never expires": 9999-12-31 local time west of UTC is year 10000 in UTC.
			in.Lifecycle.ExpiresAt = time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("never", -5*3600))
		}
	}
	if r.Intn(2) == 0 {
		in.ObservedAt = time.Date(2026, 1, 1, 0, 0, 0, r.Intn(1e9), time.FixedZone("w", -3600))
	}
	return in
}

func hostileSnapshot(r *rand.Rand, full bool) wire.CloudSnapshot {
	states := append(append([]wire.CloudSnapshotState(nil), wire.CloudSnapshotStates...), "", "gone")
	id := wire.CloudSnapshotID(fmt.Sprint("snap-", r.Intn(300)))
	if r.Intn(10) == 0 {
		id = ""
	}
	sizes := []float64{-1, 0, 2.5}
	if full {
		sizes = append(sizes, math.NaN(), math.Inf(1))
		switch r.Intn(6) {
		case 0:
			id = wire.CloudSnapshotID(strings.Repeat("s", 300))
		case 1:
			id = wire.CloudSnapshotID(fmt.Sprint("snap-\n", r.Intn(300), "\x00"))
		}
	}
	return wire.CloudSnapshot{ID: id, Name: hostileString(r), State: states[r.Intn(len(states))],
		SizeGb: sizes[r.Intn(len(sizes))], CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(r.Intn(1e6)) * time.Minute)}
}

func TestHostileAdapterAnswersAlwaysValidate(t *testing.T) { hostileRounds(t, false) }

func TestBugHostileAdapterUnfiltered(t *testing.T) {
	hostileRounds(t, true)
}

// hostileRounds feeds the provider random hostile adapter answers and holds every answer to the schema. full
// adds the inputs the core is known to mishandle (see hostileInstance and hostileSnapshot).
func hostileRounds(t *testing.T, full bool) {
	secret := "test-secret-key-0001"
	r := rand.New(rand.NewSource(1))
	for round := 0; round < 20; round++ {
		var list []wire.CloudInstance
		n := r.Intn(700)
		for i := 0; i < n; i++ {
			id := wire.CloudInstanceID(fmt.Sprint("i-", r.Intn(800)))
			account := wire.CloudAccountID("prod")
			if full && r.Intn(20) == 0 {
				id = wire.CloudInstanceID(fmt.Sprint(strings.Repeat("i", 300), i))
				account = wire.CloudAccountID(strings.Repeat("a", 200))
			}
			list = append(list, hostileInstance(r, wire.CloudInstanceRef{Account: account, ID: id}, full))
		}
		var snaps []wire.CloudSnapshot
		for i := r.Intn(400); i > 0; i-- {
			snaps = append(snaps, hostileSnapshot(r, full))
		}
		var problems []string
		for i := r.Intn(5); i > 0; i-- {
			problems = append(problems, hostileString(r))
		}
		one := hostileInstance(r, refA, full)
		other := hostileInstance(r, refB, full)
		snap := hostileSnapshot(r, full)
		result := wire.CloudActionResult{Status: []wire.CloudActionStatus{"", "done", "accepted", "refused", "bogus"}[r.Intn(5)],
			URL: hostileString(r), ExpiresAt: time.Now().In(time.FixedZone("e", 7200)), Message: hostileString(r), Snapshot: &snap}
		if r.Intn(2) == 0 {
			result.Instance = &one
		} else {
			result.Instance = &other
		}
		f := &fake{list: list, problems: problems, snaps: snaps,
			instance: func(wire.CloudInstanceRef) (wire.CloudInstance, error) { return one, nil },
			action:   func(Action) (wire.CloudActionResult, error) { return result, nil }}
		var a Adapter = f
		if r.Intn(2) == 0 {
			a = &consoleFake{fake: f, console: func(wire.CloudInstanceRef, string) (wire.CloudActionResult, error) { return result, nil }}
		}
		p := newProvider(a, nil, customs, secret)
		ctx := context.Background()
		check := func(call string, v any, err error) {
			t.Helper()
			if err != nil {
				wireCode(t, err)
				noLeak(t, err.Error(), secret)
				return
			}
			raw := valid(t, call, v)
			noLeak(t, string(raw), secret)
		}
		l, err := p.Instances(ctx, wire.CloudInstancesParams{Locale: "en", Refresh: true})
		check("cloud.instances", l, err)
		in, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: refA})
		check("cloud.instance", in, err)
		s, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: refA})
		check("cloud.snapshots", s, err)
		for _, k := range wire.CloudActionKinds {
			params := actParams(refA, k, fmt.Sprint("r-", round, k))
			if k == wire.CloudActionKindCustom {
				params.Custom = []wire.CloudCustomActionID{"renew", "probe", "wipe"}[r.Intn(3)]
			}
			params.Name = hostileString(r)
			res, err := p.Action(ctx, params)
			check("cloud.action", res, err)
		}
	}
}

// The tests below show bugs in the core (see the report): they are written to pass once the bug is fixed.

func TestBugRefLongerThanSchemaBreaksAnswer(t *testing.T) {
	long := wire.CloudInstanceRef{Account: wire.CloudAccountID(strings.Repeat("a", 200)), ID: wire.CloudInstanceID(strings.Repeat("i", 300))}
	f := &fake{list: []wire.CloudInstance{inst(long, "192.0.2.1")}}
	p := newProvider(f, nil, customs)
	l, err := p.Instances(context.Background(), wire.CloudInstancesParams{})
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "cloud.instances", l)
}

func TestBugNonFiniteNumbersAndFarTimesBreakMarshal(t *testing.T) {
	far := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	in := inst(refA, "192.0.2.1")
	in.Spec = &wire.CloudSpec{Cpu: math.NaN()}
	in.Lifecycle = &wire.CloudLifecycle{ExpiresAt: far}
	f := &fake{list: []wire.CloudInstance{in}, snaps: []wire.CloudSnapshot{{ID: "s", State: wire.CloudSnapshotStateReady, SizeGb: math.Inf(1)}}}
	p := newProvider(f, nil, customs)
	got, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("instance: %v", err)
	}
	s, _ := p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: refA})
	if _, err := json.Marshal(s); err != nil {
		t.Fatalf("snapshots: %v", err)
	}
}

func TestBugHTTPSURLGrowsPastItsLimit(t *testing.T) {
	u := "https://console.example.com/" + strings.Repeat("é", 1000) // 2028 bytes in, about 6028 out
	got := HTTPSURL(u, maxConsoleURL)
	if len(got) > maxConsoleURL {
		t.Fatalf("%d bytes, over %d", len(got), maxConsoleURL)
	}
}

func TestBugMappedUnspecifiedAddressPasses(t *testing.T) {
	if got := PlainIP("::ffff:0.0.0.0"); got != "" {
		t.Fatalf("PlainIP(::ffff:0.0.0.0) = %q", got)
	}
}

func TestBugSnapshotIDNotHeldToSchema(t *testing.T) {
	f := &fake{snaps: []wire.CloudSnapshot{{ID: wire.CloudSnapshotID(strings.Repeat("s", 300)), State: wire.CloudSnapshotStateReady}}}
	p := newProvider(f, nil, customs)
	l, err := p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: refA})
	if err != nil {
		t.Fatal(err)
	}
	valid(t, "cloud.snapshots", l)
}

func TestBugConsolerInstanceNotHeldToRef(t *testing.T) {
	other := inst(refB, "192.0.2.1")
	c := &consoleFake{fake: &fake{}, console: func(wire.CloudInstanceRef, string) (wire.CloudActionResult, error) {
		return wire.CloudActionResult{URL: "https://console.example.com/x", Instance: &other,
			Snapshot: &wire.CloudSnapshot{ID: "s", State: wire.CloudSnapshotStateReady}}, nil
	}}
	p := newProvider(c, nil, customs)
	res, err := p.Action(context.Background(), actParams(refA, wire.CloudActionKindConsole, "c"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Instance != nil || res.Snapshot != nil {
		t.Fatalf("console answered another instance or a snapshot: %+v", res)
	}
}

func TestBugCachedListSharesSlices(t *testing.T) {
	f := &fake{list: []wire.CloudInstance{inst(refA, "192.0.2.1")}}
	p := newProvider(f, newClock(), customs)
	l, _ := p.Instances(context.Background(), wire.CloudInstancesParams{})
	l.Instances[0].DisplayName = "edited"
	l2, _ := p.Instances(context.Background(), wire.CloudInstancesParams{})
	if l2.Instances[0].DisplayName == "edited" {
		t.Fatal("the cache was edited through an answer")
	}
}

func TestBugPlaceAndSpecNotRedacted(t *testing.T) {
	secret := "test-secret-key-0001"
	in := inst(refA, "192.0.2.1")
	in.Region, in.Zone = "r "+secret, "z "+secret
	in.Spec = &wire.CloudSpec{Size: "s " + secret, Arch: secret, OS: "os " + secret}
	p := newProvider(&fake{list: []wire.CloudInstance{in}}, nil, customs, secret)
	got, err := p.Instance(context.Background(), wire.CloudInstanceParams{Instance: refA})
	if err != nil {
		t.Fatal(err)
	}
	noLeak(t, string(valid(t, "cloud.instance", got)), secret)
}

func TestBugMappedAddressIsKept(t *testing.T) {
	for _, in := range []string{"::ffff:192.0.2.1", "::ffff:203.0.113.4"} {
		if got := PlainIP(in); got != "" {
			t.Errorf("PlainIP(%q) = %q, want it dropped", in, got)
		}
	}
	if got := PlainIPs("::ffff:192.0.2.1", "192.0.2.9"); len(got) != 1 || got[0] != "192.0.2.9" {
		t.Errorf("PlainIPs = %v", got)
	}
}
