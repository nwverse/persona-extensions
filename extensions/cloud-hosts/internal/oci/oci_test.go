package oci

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

var custom = map[wire.CloudCustomActionID]wire.CloudSafety{CustomKeepalive: wire.CloudSafetyRead}

type env struct {
	f       *fakeOCI
	cfg     Config
	ad      core.Adapter
	p       *core.Provider
	secrets []string
	clock   *clock
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func configText(t *testing.T, withBroken bool) (string, string, []string) {
	_, k2 := keys(t)
	pem2 := pemPKCS8(t, k2)
	escaped := strings.ReplaceAll(strings.TrimSpace(pem2), "\n", `\n`)
	text := "# a synthetic OCI config\n[DEFAULT]\nuser=" + user1 + "\nfingerprint=" + fp1 + "\ntenancy=" + tenancy1 +
		"\nregion=us-ashburn-1\nkey_file=~/.oci/oci_api_key.pem\n\n[SECOND]\nuser = " + user2 + "\nfingerprint = " + fp2 +
		"\ntenancy = " + tenancy2 + "\nregion = eu-frankfurt-1\nkey_content = " + escaped + "\n"
	if withBroken {
		text += "\n[BROKEN]\nuser=" + user3 + "\nfingerprint=" + fp3 + "\ntenancy=" + tenancy3 + "\nregion=us-phoenix-1\n"
	}
	lines := strings.Split(strings.TrimSpace(pem2), "\n")
	return text, escaped, []string{lines[3], lines[len(lines)-3]}
}

func vnic(id string, primary bool, public, private string, v6 ...string) map[string]any {
	return map[string]any{"id": id, "isPrimary": primary, "publicIp": public, "privateIp": private, "ipv6Addresses": v6,
		"lifecycleState": "AVAILABLE", "hostnameLabel": "host"}
}

func seed(f *fakeOCI) {
	f.add("us-ashburn-1", tenancy1, map[string]any{"id": idRunning, "displayName": "free-micro", "lifecycleState": "RUNNING",
		"availabilityDomain": "Abcd:US-ASHBURN-AD-1", "region": "iad", "shape": "VM.Standard.E2.1.Micro",
		"shapeConfig": map[string]any{"ocpus": 1, "vcpus": 2, "memoryInGBs": 1, "networkingBandwidthInGbps": 0.48, "processorDescription": "2.0 GHz AMD EPYC 7551"}},
		vnic("ocid1.vnic.oc1.iad.primary01", true, "203.0.113.10", "10.0.0.10", "2001:db8::10"),
		vnic("ocid1.vnic.oc1.iad.second01", false, "198.51.100.20", "10.0.1.20"))
	f.add("us-ashburn-1", tenancy1, map[string]any{"id": idStopped, "displayName": "free-arm", "lifecycleState": "STOPPED",
		"availabilityDomain": "Abcd:US-ASHBURN-AD-2", "shape": "VM.Standard.A1.Flex",
		"shapeConfig": map[string]any{"ocpus": 4, "memoryInGBs": 24, "networkingBandwidthInGbps": 4}},
		vnic("ocid1.vnic.oc1.iad.primary02", true, "192.0.2.50", "10.0.0.50"))
	f.add("us-ashburn-1", tenancy1, map[string]any{"id": idTerminated, "displayName": "old", "lifecycleState": "TERMINATED",
		"shape": "VM.Standard.E4.Flex"})
	f.add("us-ashburn-1", tenancy1, map[string]any{"id": idPreempted, "displayName": "batch", "lifecycleState": "TERMINATED",
		"shape": "VM.Standard.E4.Flex", "preemptibleInstanceConfig": map[string]any{"preemptionAction": map[string]any{"type": "TERMINATE", "preserveBootVolume": false}}})
	f.add("us-ashburn-1", tenancy1, map[string]any{"id": idStarting, "lifecycleState": "PROVISIONING", "shape": "VM.Standard.E4.Flex"},
		vnic("ocid1.vnic.oc1.iad.primary05", true, "", "10.0.0.55"))
	f.add("eu-frankfurt-1", tenancy2, map[string]any{"id": idMoving, "displayName": "mover", "lifecycleState": "MOVING", "shape": "VM.Standard.E4.Flex"},
		vnic("ocid1.vnic.oc1.eu-frankfurt-1.primary06", true, "203.0.113.60", "172.16.0.60"))
	f.add("eu-frankfurt-1", tenancy2, map[string]any{"id": idSpot, "displayName": "spot", "lifecycleState": "RUNNING", "shape": "VM.Standard.E4.Flex",
		"shapeConfig":               map[string]any{"ocpus": 2, "vcpus": 4, "memoryInGBs": 16},
		"preemptibleInstanceConfig": map[string]any{"preemptionAction": map[string]any{"type": "TERMINATE"}}},
		vnic("ocid1.vnic.oc1.eu-frankfurt-1.primary07", true, "198.51.100.70", "192.168.0.70"))
}

func newEnv(t *testing.T, withBroken bool, dataDir string) *env {
	t.Helper()
	k1, k2 := keys(t)
	f := newFake(t)
	f.pubs[tenancy1+"/"+user1+"/"+fp1] = &k1.PublicKey
	f.pubs[tenancy2+"/"+user2+"/"+fp2] = &k2.PublicKey
	f.pubs[tenancy3+"/"+user3+"/"+fp3] = &k1.PublicKey
	seed(f)
	text, escaped, pemLines := configText(t, withBroken)
	pem1 := pemPKCS1(k1)
	l1 := strings.Split(strings.TrimSpace(pem1), "\n")
	c := &clock{t: testNow}
	cfg := Config{ConfigFile: text, PrivateKey: pem1, DataDir: dataDir, Endpoint: f.endpoint, Now: c.now}
	ad, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	secrets := append([]string{tenancy1, tenancy2, tenancy3, user1, user2, user3, fp1, fp2, fp3, escaped,
		tenancy1 + "/" + user1 + "/" + fp1, l1[2], l1[len(l1)-2]}, pemLines...)
	p := coretest.Provider(ad, custom)
	return &env{f: f, cfg: cfg, ad: ad, p: p, secrets: secrets, clock: c}
}

func byID(list []wire.CloudInstance) map[string]wire.CloudInstance {
	m := map[string]wire.CloudInstance{}
	for _, in := range list {
		m[string(in.ID)] = in
	}
	return m
}

func kinds(k ...wire.CloudActionKind) []wire.CloudActionKind { return k }

var (
	runningActions = kinds("stop", "reboot", "hard_reset", "force_stop", "sync_ip", "console")
	stoppedActions = kinds("start", "sync_ip", "console")
	readActions    = kinds("sync_ip", "console")
)

func TestInstancesAcrossAccounts(t *testing.T) {
	e := newEnv(t, true, "")
	e.f.hook = func(service, region, method, path string, q url.Values) (int, string, bool) {
		if region == "us-phoenix-1" {
			// the vendor echoes the key id back: it must not leave the program
			return 401, `{"code":"NotAuthenticated","message":"The key ` + tenancy3 + "/" + user3 + "/" + fp3 + ` is not valid"}`, true
		}
		return 0, "", false
	}
	list, err := e.p.Instances(context.Background(), wire.CloudInstancesParams{Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", list)
	coretest.NoLeak(t, string(raw), e.secrets...)
	if !list.Partial || !strings.Contains(list.Detail, "broken") || strings.Contains(list.Detail, "default") {
		t.Fatalf("partial %v detail %q", list.Partial, list.Detail)
	}
	got := byID(list.Instances)
	if len(got) != 7 {
		t.Fatalf("%d instances: %s", len(got), raw)
	}
	if n := len(e.f.callsMatching("GET iaas/us-ashburn-1 /20160918/instances?")); n != 3 {
		t.Fatalf("%d list pages for 5 instances at 2 a page", n)
	}
	if c := e.f.callsMatching("instanceId=" + idTerminated); len(c) != 0 {
		t.Fatalf("addresses read for a terminated instance: %v", c)
	}
	if c := e.f.callsMatching("instanceId=" + idPreempted); len(c) != 0 {
		t.Fatalf("addresses read for a terminated instance: %v", c)
	}

	r := got[idRunning]
	if r.Account != "default" || r.State != "running" || r.DisplayName != "free-micro" || r.Region != "us-ashburn-1" || r.Zone != "Abcd:US-ASHBURN-AD-1" {
		t.Fatalf("running: %+v", r)
	}
	if !reflect.DeepEqual(r.PublicIps, []string{"203.0.113.10", "198.51.100.20", "2001:db8::10"}) || !reflect.DeepEqual(r.PrivateIps, []string{"10.0.0.10", "10.0.1.20"}) {
		t.Fatalf("addresses %v %v", r.PublicIps, r.PrivateIps)
	}
	if *r.Spec != (wire.CloudSpec{Size: "VM.Standard.E2.1.Micro", Cpu: 2, MemoryMb: 1024, Arch: "amd64"}) {
		t.Fatalf("spec %+v", *r.Spec)
	}
	if !reflect.DeepEqual(r.Actions, runningActions) || !reflect.DeepEqual(r.CustomActions, []wire.CloudCustomActionID{CustomKeepalive}) {
		t.Fatalf("actions %v %v", r.Actions, r.CustomActions)
	}
	if r.Lifecycle == nil || r.Lifecycle.Spot || !r.Lifecycle.KeepaliveAt.Equal(testNow.Add(7*24*time.Hour)) {
		t.Fatalf("lifecycle %+v", r.Lifecycle)
	}
	if r.ConsoleURL != "https://cloud.oracle.com/compute/instances/"+idRunning+"?region=us-ashburn-1" || !r.ObservedAt.Equal(testNow) {
		t.Fatalf("console %q observed %v", r.ConsoleURL, r.ObservedAt)
	}

	s := got[idStopped]
	if s.State != "stopped" || !reflect.DeepEqual(s.Actions, stoppedActions) || *s.Spec != (wire.CloudSpec{Size: "VM.Standard.A1.Flex", Cpu: 4, MemoryMb: 24576, Arch: "arm64"}) {
		t.Fatalf("stopped: %+v %+v", s, s.Spec)
	}
	if len(s.CustomActions) != 1 || s.Lifecycle == nil || s.Lifecycle.KeepaliveAt.IsZero() {
		t.Fatalf("A1 keepalive: %+v", s)
	}
	if tt := got[idTerminated]; tt.State != "terminated" || len(tt.PublicIps) != 0 || !reflect.DeepEqual(tt.Actions, readActions) || tt.Lifecycle != nil || tt.StateDetail != "" {
		t.Fatalf("terminated: %+v", tt)
	}
	if pp := got[idPreempted]; pp.State != "terminated" || pp.StateDetail == "" || pp.Lifecycle == nil || !pp.Lifecycle.Spot || !reflect.DeepEqual(pp.Actions, readActions) {
		t.Fatalf("terminated preemptible: %+v", pp)
	}
	if st := got[idStarting]; st.State != "starting" || st.DisplayName != idStarting || len(st.PublicIps) != 0 || !reflect.DeepEqual(st.PrivateIps, []string{"10.0.0.55"}) {
		t.Fatalf("starting: %+v", st)
	}
	if mv := got[idMoving]; mv.Account != "second" || mv.State != "unknown" || !strings.Contains(mv.StateDetail, "MOVING") || mv.Region != "eu-frankfurt-1" {
		t.Fatalf("moving: %+v", mv)
	}
	// Oracle: preemptible instances cannot be started, stopped or rebooted
	if sp := got[idSpot]; sp.State != "running" || sp.Lifecycle == nil || !sp.Lifecycle.Spot || len(sp.CustomActions) != 0 || sp.Spec.Cpu != 4 ||
		!reflect.DeepEqual(sp.Actions, readActions) {
		t.Fatalf("spot: %+v", sp)
	}
}

func TestInstancesEveryAccountFails(t *testing.T) {
	e := newEnv(t, false, "")
	e.f.hook = func(string, string, string, string, url.Values) (int, string, bool) {
		return 401, `{"code":"NotAuthenticated","message":"bad key ` + fp1 + `"}`, true
	}
	_, err := e.p.Instances(context.Background(), wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("err %v", err)
	}
	coretest.NoLeak(t, err.Error(), e.secrets...)
}

func TestCompartments(t *testing.T) {
	e := newEnv(t, false, "")
	compA := "ocid1.compartment.oc1..aaaacompa"
	compB := "ocid1.compartment.oc1..aaaacompb"
	e.f.add("us-ashburn-1", compA, map[string]any{"id": "ocid1.instance.oc1.iad.aaaaincompa", "lifecycleState": "STOPPED", "shape": "VM.Standard.E4.Flex"})
	e.f.add("eu-frankfurt-1", compB, map[string]any{"id": "ocid1.instance.oc1.eu-frankfurt-1.aaaaincompb", "lifecycleState": "STOPPED", "shape": "VM.Standard.E4.Flex"})
	cfg := e.cfg
	cfg.Compartments = []string{compA, compB}
	ad, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	list, problems, err := ad.Instances(context.Background())
	if err != nil || len(problems) != 0 || len(list) != 2 {
		t.Fatalf("list %v problems %v err %v", list, problems, err)
	}
	accounts := map[string]string{}
	for _, in := range list {
		accounts[string(in.ID)] = string(in.Account)
	}
	if accounts["ocid1.instance.oc1.iad.aaaaincompa"] != "default" || accounts["ocid1.instance.oc1.eu-frankfurt-1.aaaaincompb"] != "second" {
		t.Fatalf("accounts %v", accounts)
	}
	// a compartment no profile can see is reported
	cfg.Compartments = []string{compA, "ocid1.compartment.oc1..aaaanowhere"}
	ad, _ = New(cfg)
	_, problems, err = ad.Instances(context.Background())
	if err != nil || !reflect.DeepEqual(problems, []string{"default", "second"}) {
		t.Fatalf("problems %v err %v", problems, err)
	}
}

func TestStateMapping(t *testing.T) {
	e := newEnv(t, false, "")
	a := e.ad.(*adapter)
	p := a.profiles[0]
	pre := json.RawMessage(`{"preemptionAction":{"type":"TERMINATE"}}`)
	cases := []struct {
		state   string
		spot    bool
		want    wire.CloudInstanceState
		actions []wire.CloudActionKind
		detail  bool
	}{
		{"PROVISIONING", false, "starting", readActions, false},
		{"STARTING", false, "starting", readActions, false},
		{"RUNNING", false, "running", runningActions, false},
		{"STOPPING", false, "stopping", readActions, false},
		{"STOPPED", false, "stopped", stoppedActions, false},
		{"STOPPED", true, "stopped", readActions, false},
		{"RUNNING", true, "running", readActions, false},
		{"STARTING", true, "starting", readActions, false},
		{"TERMINATING", false, "terminated", readActions, false},
		{"TERMINATED", false, "terminated", readActions, false},
		{"TERMINATING", true, "terminated", readActions, true},
		{"TERMINATED", true, "terminated", readActions, true},
		{"MOVING", false, "unknown", readActions, true},
		{"CREATING_IMAGE", false, "unknown", readActions, true},
		{"SOMETHING_NEW", false, "unknown", readActions, true},
	}
	for _, c := range cases {
		in := ociInstance{ID: idRunning, LifecycleState: c.state, Shape: "VM.Standard.E4.Flex"}
		if c.spot {
			in.PreemptibleInstanceConfig = pre
		}
		w := a.toWire("en", p, in, nil, testNow)
		if w.State != c.want || !reflect.DeepEqual(w.Actions, c.actions) || (w.StateDetail != "") != c.detail {
			t.Errorf("%s spot=%v: %s %v %q", c.state, c.spot, w.State, w.Actions, w.StateDetail)
		}
		if c.spot != (w.Lifecycle != nil && w.Lifecycle.Spot) {
			t.Errorf("%s spot=%v: lifecycle %+v", c.state, c.spot, w.Lifecycle)
		}
		coretest.Valid(t, "cloud.instance", w)
	}
}

func TestActions(t *testing.T) {
	cases := []struct {
		kind wire.CloudActionKind
		id   string
		op   string
	}{
		{"start", idStopped, "START"},
		{"stop", idRunning, "SOFTSTOP"},
		{"reboot", idRunning, "SOFTRESET"},
		{"hard_reset", idRunning, "RESET"},
		{"force_stop", idRunning, "STOP"},
	}
	for _, c := range cases {
		e := newEnv(t, false, "")
		rq := wire.CloudRequestID("req-" + string(c.kind))
		params := wire.CloudActionParams{Locale: "en", Instance: wire.CloudInstanceRef{Account: "default", ID: wire.CloudInstanceID(c.id)}, Action: c.kind, RequestID: rq}
		res, err := e.p.Action(context.Background(), params)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		raw := coretest.Valid(t, "cloud.action", res)
		coretest.NoLeak(t, string(raw), e.secrets...)
		if res.Status != wire.CloudActionStatusAccepted || res.Instance == nil || res.Instance.ID != wire.CloudInstanceID(c.id) {
			t.Fatalf("%s: %s", c.kind, raw)
		}
		if !reflect.DeepEqual(e.f.actions, []string{c.id + " " + c.op}) {
			t.Fatalf("%s: vendor calls %v", c.kind, e.f.actions)
		}
		want := retryToken(core.Action{Ref: params.Instance, Kind: c.kind, RequestID: rq})
		if e.f.tokens[0] != want {
			t.Fatalf("%s: retry token %q, want %q", c.kind, e.f.tokens[0], want)
		}
		// the same request id again is answered from the record
		if _, err := e.p.Action(context.Background(), params); err != nil {
			t.Fatal(err)
		}
		if len(e.f.actions) != 1 {
			t.Fatalf("%s: the vendor was asked %d times for one request id", c.kind, len(e.f.actions))
		}
	}
	if a, b := retryToken(core.Action{RequestID: "a"}), retryToken(core.Action{RequestID: "b"}); a == b || len(a) > 64 {
		t.Fatalf("retry tokens %q %q", a, b)
	}
}

func TestUnsupported(t *testing.T) {
	e := newEnv(t, false, "")
	ref := wire.CloudInstanceRef{Account: "default", ID: idRunning}
	for i, p := range []wire.CloudActionParams{
		{Instance: ref, Action: wire.CloudActionKindSnapshotCreate, Name: "snap"},
		{Instance: ref, Action: wire.CloudActionKindCustom, Custom: "renew"},
	} {
		p.RequestID = wire.CloudRequestID("u" + string(rune('0'+i)))
		if _, err := e.p.Action(context.Background(), p); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("%v: %v", p.Action, err)
		}
	}
	for _, k := range []wire.CloudActionKind{wire.CloudActionKindSnapshotCreate, wire.CloudActionKindConsole} {
		if _, err := e.ad.Action(context.Background(), core.Action{Ref: ref, Kind: k, RequestID: "x"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("adapter %s: %v", k, err)
		}
	}
	if _, err := e.ad.Action(context.Background(), core.Action{Ref: ref, Kind: wire.CloudActionKindCustom, Custom: "renew", RequestID: "x"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("adapter custom: %v", err)
	}
	if _, err := e.p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Instance: ref}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("snapshots: %v", err)
	}
	if len(e.f.actions) != 0 {
		t.Fatalf("vendor asked: %v", e.f.actions)
	}
	// an unknown account or a malformed id (a slash, a percent sign, a space: never an OCID) is not_found without a
	// call, for a read and for a write
	before := len(e.f.callsMatching(""))
	for i, r := range []wire.CloudInstanceRef{{Account: "nobody", ID: idRunning}, {Account: "default", ID: "ocid1.instance/../x"},
		{Account: "default", ID: "ocid1.instance.oc1.iad.a/b%2Fc d"}} {
		if _, err := e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: r}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("%v: %v", r, err)
		}
		if _, err := e.p.Action(context.Background(), wire.CloudActionParams{Instance: r, Action: "start", RequestID: wire.CloudRequestID("nf" + string(rune('0'+i)))}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("action %v: %v", r, err)
		}
	}
	if n := len(e.f.callsMatching("")); n != before {
		t.Fatalf("%d requests for refs that cannot exist", n-before)
	}
}

func TestPathEscaped(t *testing.T) {
	// ids are path-escaped once; an OCID has no character escaping changes, so each reaches the vendor as one segment
	// exactly as it reads (ids that would need escaping are refused before any request: TestUnsupported)
	e := newEnv(t, false, "")
	if _, err := e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: idRunning}}); err != nil {
		t.Fatal(err)
	}
	if c := e.f.callsMatching(" /20160918/instances/" + idRunning + "?"); len(c) != 1 {
		t.Fatalf("instance path %v", e.f.callsMatching("/instances/"))
	}
	if c := e.f.callsMatching(" /20160918/vnics/ocid1.vnic.oc1.iad.primary01?"); len(c) != 1 {
		t.Fatalf("vnic path %v", e.f.callsMatching("/vnics/"))
	}
}

func TestFailureRecord(t *testing.T) {
	e := newEnv(t, false, "")
	ref := wire.CloudInstanceRef{Account: "default", ID: idStopped}
	status := 500
	e.f.hook = func(service, region, method, path string, q url.Values) (int, string, bool) {
		if method == "POST" {
			return status, `{"code":"InternalError","message":"Something went wrong."}`, true
		}
		return 0, "", false
	}
	params := wire.CloudActionParams{Locale: "en", Instance: ref, Action: "start", RequestID: "f-500"}
	for range 2 {
		if _, err := e.p.Action(context.Background(), params); coretest.Code(err) != wire.CloudErrorCodeFailed {
			t.Fatalf("500: %v", err)
		}
	}
	// a 500 may have been acted on: the same request id is answered from the record, not sent again
	if n := len(e.f.callsMatching("POST ")); n != 1 {
		t.Fatalf("500 sent %d times for one request id", n)
	}
	// a 429 proves OCI did not act: the same request id is tried again
	status, params.RequestID = 429, "f-429"
	for range 2 {
		if _, err := e.p.Action(context.Background(), params); coretest.Code(err) != wire.CloudErrorCodeRateLimited {
			t.Fatalf("429: %v", err)
		}
	}
	if n := len(e.f.callsMatching("POST ")); n != 3 {
		t.Fatalf("%d POSTs, want 1 + 2", n)
	}
	if e := apiError("en", 500, []byte(`{"code":"InternalError","message":"x"}`)); e.Transient {
		t.Fatal("a 500 is not transient")
	}
	if e := apiError("en", 503, nil); e.Transient || !strings.Contains(e.Message, "HTTP 503") {
		t.Fatalf("503: %+v", e)
	}
	// the vendor's words are not clipped before the Provider redacts them
	long := strings.Repeat("word ", 100)
	if e := apiError("en", 400, []byte(`{"code":"InvalidParameter","message":"`+long+`"}`)); !strings.Contains(e.Message, strings.TrimSpace(long)) {
		t.Fatalf("clipped: %q", e.Message)
	}
}

func TestChineseWords(t *testing.T) {
	e := newEnv(t, false, "")
	in, err := e.p.Instance(context.Background(), wire.CloudInstanceParams{Locale: "zh-CN", Instance: wire.CloudInstanceRef{Account: "default", ID: idPreempted}})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	if in.State != "terminated" || !strings.Contains(in.StateDetail, "抢占式实例") {
		t.Fatalf("zh detail %q", in.StateDetail)
	}
	in, _ = e.p.Instance(context.Background(), wire.CloudInstanceParams{Locale: "en", Instance: wire.CloudInstanceRef{Account: "default", ID: idPreempted}})
	if !strings.HasPrefix(in.StateDetail, "Oracle Cloud ended this preemptible instance") {
		t.Fatalf("en detail %q", in.StateDetail)
	}
	_, err = e.p.Instance(context.Background(), wire.CloudInstanceParams{Locale: "zh-CN", Instance: wire.CloudInstanceRef{Account: "default", ID: "ocid1.instance.oc1.iad.aaaagone"}})
	if coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Fatalf("gone: %v", err)
	}
	if _, err := e.p.Snapshots(context.Background(), wire.CloudSnapshotsParams{Locale: "zh-CN", Instance: wire.CloudInstanceRef{Account: "default", ID: idRunning}}); err == nil || !strings.Contains(err.Error(), "快照") {
		t.Fatalf("zh snapshots: %v", err)
	}
}

func TestErrors(t *testing.T) {
	e := newEnv(t, false, "")
	leak := e.secrets[len(e.secrets)-1] // a line of a private key
	keyID := tenancy1 + "/" + user1 + "/" + fp1
	cases := []struct {
		status int
		body   string
		want   wire.CloudErrorCode
	}{
		{401, `{"code":"NotAuthenticated","message":"The required information to complete authentication was not provided: ` + keyID + `"}`, "not_signed_in"},
		{401, `{"code":"SignUpRequired","message":"sign up first"}`, "not_signed_in"},
		{404, `{"code":"NotAuthorizedOrNotFound","message":"Authorization failed or requested resource not found."}`, "not_found"},
		{409, `{"code":"IncorrectState","message":"Instance is in an incorrect state ` + leak + `"}`, "invalid_state"},
		{409, `{"code":"InvalidatedRetryToken","message":"The retry token was used for another request."}`, "invalid_state"},
		{409, `{"code":"Conflict","message":"Resource is not in a valid state for this operation."}`, "invalid_state"},
		{429, `{"code":"TooManyRequests","message":"Too many requests for the tenancy ` + tenancy1 + `"}`, "rate_limited"},
		{500, `{"code":"InternalError","message":"Out of host capacity."}`, "capacity"},
		{400, `{"code":"LimitExceeded","message":"The limit for vm-standard-a1-core-count has been exceeded."}`, "capacity"},
		{400, `{"code":"QuotaExceeded","message":"Quota exceeded."}`, "capacity"},
		{400, `{"code":"InvalidParameter","message":"Something with ` + fp1 + `"}`, "failed"},
		{500, `not json at all`, "failed"},
		{403, `{"code":"Forbidden","message":"nope"}`, "not_signed_in"},
	}
	for i, c := range cases {
		e.f.hook = func(service, region, method, path string, q url.Values) (int, string, bool) {
			if method == "POST" {
				return c.status, c.body, true
			}
			return 0, "", false
		}
		_, err := e.p.Action(context.Background(), wire.CloudActionParams{Locale: "en", Action: "start", RequestID: wire.CloudRequestID("e" + string(rune('a'+i))),
			Instance: wire.CloudInstanceRef{Account: "default", ID: idStopped}})
		if coretest.Code(err) != c.want {
			t.Errorf("%d %s: %v (%s), want %s", c.status, c.body, coretest.Code(err), err, c.want)
			continue
		}
		coretest.NoLeak(t, err.Error(), e.secrets...)
	}
	// reading an instance that is gone
	e.f.hook = nil
	if _, err := e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "default", ID: "ocid1.instance.oc1.iad.aaaagone"}}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Fatalf("gone: %v", err)
	}
	if _, err := e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: wire.CloudInstanceRef{Account: "second", ID: idRunning}}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Fatalf("another region: %v", err)
	}
}

func TestNewAddressAfterStart(t *testing.T) {
	e := newEnv(t, false, "")
	ref := wire.CloudInstanceRef{Account: "default", ID: idStopped}
	in, err := e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	if in.State != "stopped" || !reflect.DeepEqual(in.PublicIps, []string{"192.0.2.50"}) {
		t.Fatalf("before: %+v", in)
	}
	res, err := e.p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: "start", RequestID: "start-1"})
	if err != nil || res.Instance == nil || res.Instance.State != "starting" {
		t.Fatalf("start: %+v %v", res, err)
	}
	e.f.set(idStopped, func(in *fakeInst) {
		in.data["lifecycleState"] = "RUNNING"
		in.vnics[0]["publicIp"] = "192.0.2.77"
	})
	in, err = e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instance", in)
	coretest.NoLeak(t, string(raw), e.secrets...)
	if in.State != "running" || !reflect.DeepEqual(in.PublicIps, []string{"192.0.2.77"}) || !reflect.DeepEqual(in.Actions, runningActions) {
		t.Fatalf("after: %s", raw)
	}
	sync, err := e.p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: "sync_ip", RequestID: "sync-1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", sync)
	if sync.Status != "done" || sync.Instance == nil || !reflect.DeepEqual(sync.Instance.PublicIps, []string{"192.0.2.77"}) {
		t.Fatalf("sync_ip: %+v", sync)
	}
	e.f.set(idStopped, func(in *fakeInst) { in.vnics[0]["publicIp"] = "192.0.2.78" })
	sync, _ = e.p.Action(context.Background(), wire.CloudActionParams{Instance: ref, Action: "sync_ip", RequestID: "sync-1"})
	if sync.Instance == nil || sync.Instance.PublicIps[0] != "192.0.2.78" {
		t.Fatalf("sync_ip answered from a cache: %+v", sync.Instance)
	}
}

func TestConsole(t *testing.T) {
	e := newEnv(t, false, "")
	res, err := e.p.Action(context.Background(), wire.CloudActionParams{Instance: wire.CloudInstanceRef{Account: "second", ID: idSpot}, Action: "console", RequestID: "c-1"})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.action", res)
	coretest.NoLeak(t, string(raw), e.secrets...)
	if res.URL != "https://cloud.oracle.com/compute/instances/"+idSpot+"?region=eu-frankfurt-1" || strings.Contains(res.URL, "@") {
		t.Fatalf("console %q", res.URL)
	}
}

func TestKeepalive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	e := newEnv(t, false, dir)
	ref := wire.CloudInstanceRef{Account: "default", ID: idStopped} // A1.Flex
	e.f.metrics["CpuUtilization"] = []float64{1, 2, 3, 4, 5}
	e.f.metrics["MemoryUtilization"] = []float64{10, 12}
	// rate() answers bytes per second: 2e6 B/s on the 4 Gbps of this A1 shape is 0.4%
	e.f.metrics["NetworksBytesIn"] = []float64{1e6, 2e6}
	params := wire.CloudActionParams{Locale: "en", Instance: ref, Action: "custom", Custom: CustomKeepalive, RequestID: "k-1"}
	res, err := e.p.Action(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.action", res)
	coretest.NoLeak(t, string(raw), e.secrets...)
	if res.Status != "done" || !strings.HasPrefix(res.Message, "At risk") || !strings.Contains(res.Message, "CPU p95 5.0%") ||
		!strings.Contains(res.Message, "memory p95 12.0%") || !strings.Contains(res.Message, "network p95 0.4%") {
		t.Fatalf("probe: %s", raw)
	}
	if len(e.f.actions) != 0 {
		t.Fatalf("a read action changed something: %v", e.f.actions)
	}
	rq := e.f.metricRq[0]
	want := map[string]string{"namespace": "oci_computeagent", "query": `CpuUtilization[1h]{resourceId = "` + idStopped + `"}.percentile(0.95)`,
		"startTime": "2026-11-13T10:00:00Z", "endTime": "2026-11-20T10:00:00Z", "resolution": "1h", "compartmentId": tenancy1}
	if !reflect.DeepEqual(rq, want) {
		t.Fatalf("metrics request %v", rq)
	}
	if !slices.ContainsFunc(e.f.metricRq, func(m map[string]string) bool { return strings.HasPrefix(m["query"], "MemoryUtilization[1h]") }) ||
		!slices.ContainsFunc(e.f.metricRq, func(m map[string]string) bool {
			return m["query"] == `NetworksBytesOut[1h]{resourceId = "`+idStopped+`"}.rate()`
		}) {
		t.Fatalf("metric requests %v", e.f.metricRq)
	}
	// not active: keepalive_at is the observation time + 7 days
	in, _ := e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
	if !in.Lifecycle.KeepaliveAt.Equal(testNow.Add(7 * 24 * time.Hour)) {
		t.Fatalf("keepalive_at %v", in.Lifecycle.KeepaliveAt)
	}

	// an active probe, in Chinese
	e.f.metrics["CpuUtilization"] = []float64{30, 40, 35}
	params.Locale, params.RequestID = "zh-CN", "k-2"
	res, err = e.p.Action(context.Background(), params)
	if err != nil || !strings.HasPrefix(res.Message, "看起来足够活跃") || !strings.Contains(res.Message, "CPU p95 40.0%") {
		t.Fatalf("active probe: %+v %v", res, err)
	}
	probedAt := testNow
	e.clock.add(48 * time.Hour)
	in, _ = e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
	coretest.Valid(t, "cloud.instance", in)
	if !in.Lifecycle.KeepaliveAt.Equal(probedAt.Add(7 * 24 * time.Hour)) {
		t.Fatalf("keepalive_at after an active probe %v", in.Lifecycle.KeepaliveAt)
	}

	// kept on disk with 0600 and without credentials, and read back by a new adapter
	path := filepath.Join(dir, "oci-keepalive.json")
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("probe file %v %v", st, err)
	}
	file, _ := os.ReadFile(path)
	coretest.NoLeak(t, string(file), e.secrets...)
	ad2, err := New(e.cfg)
	if err != nil {
		t.Fatal(err)
	}
	in2, _ := ad2.Instance(context.Background(), ref)
	if !in2.Lifecycle.KeepaliveAt.Equal(probedAt.Add(7 * 24 * time.Hour)) {
		t.Fatalf("keepalive_at from the file %v", in2.Lifecycle.KeepaliveAt)
	}
	// once the active probe is older than 7 days, the observation time counts again
	e.clock.add(6 * 24 * time.Hour)
	in2, _ = ad2.Instance(context.Background(), ref)
	if !in2.Lifecycle.KeepaliveAt.Equal(e.clock.now().Add(7 * 24 * time.Hour)) {
		t.Fatalf("stale keepalive_at %v", in2.Lifecycle.KeepaliveAt)
	}

	// active by network alone: 1.2e8 B/s out on 4 Gbps is 24% (a counter reset can make rate() dip below zero)
	e.f.metrics = map[string][]float64{"CpuUtilization": {1, 2}, "NetworksBytesOut": {1.2e8, 1.2e8, -5e9, 1.2e8}}
	params.Locale, params.RequestID = "en", "k-net"
	if res, err = e.p.Action(context.Background(), params); err != nil || !strings.HasPrefix(res.Message, "Looks active") || !strings.Contains(res.Message, "network p95 24.0%") {
		t.Fatalf("network active: %+v %v", res, err)
	}

	// no data
	e.f.metrics = map[string][]float64{}
	params.Locale, params.RequestID = "en", "k-3"
	if res, err = e.p.Action(context.Background(), params); err != nil || !strings.HasPrefix(res.Message, "No CPU data") {
		t.Fatalf("no data: %+v %v", res, err)
	}
	// a shape the free tier does not reclaim
	params.Instance, params.RequestID = wire.CloudInstanceRef{Account: "second", ID: idSpot}, "k-4"
	if _, err := e.p.Action(context.Background(), params); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("not free: %v", err)
	}
	// a monitoring failure
	e.f.hook = func(service, region, method, path string, q url.Values) (int, string, bool) {
		if service == "telemetry" {
			return 429, `{"code":"TooManyRequests","message":"slow down"}`, true
		}
		return 0, "", false
	}
	params.Instance, params.RequestID = ref, "k-5"
	if _, err := e.p.Action(context.Background(), params); coretest.Code(err) != wire.CloudErrorCodeRateLimited {
		t.Fatalf("monitoring throttled: %v", err)
	}
}

func TestNew(t *testing.T) {
	k1, _ := keys(t)
	pem1 := pemPKCS1(k1)
	text, _, _ := configText(t, false)
	cases := []struct {
		name string
		cfg  Config
		want string
	}{
		{"empty", Config{PrivateKey: pem1}, "config file"},
		{"missing", Config{ConfigFile: "[DEFAULT]\nuser=" + user1 + "\nregion=us-ashburn-1\n", PrivateKey: pem1}, "fingerprint, tenancy"},
		{"no key", Config{ConfigFile: "[DEFAULT]\nuser=" + user1 + "\nfingerprint=" + fp1 + "\ntenancy=" + tenancy1 + "\nregion=us-ashburn-1\n"}, "no private key"},
		{"bad key", Config{ConfigFile: "[DEFAULT]\nuser=" + user1 + "\nfingerprint=" + fp1 + "\ntenancy=" + tenancy1 + "\nregion=us-ashburn-1\n", PrivateKey: "test-not-a-key-0001"}, "PEM"},
		{"bad region", Config{ConfigFile: "[DEFAULT]\nuser=" + user1 + "\nfingerprint=" + fp1 + "\ntenancy=" + tenancy1 + "\nregion=evil.example.com/x\n", PrivateKey: pem1}, "region"},
	}
	for _, c := range cases {
		_, err := New(c.cfg)
		if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		coretest.NoLeak(t, err.Error(), user1, fp1, tenancy1, "test-not-a-key-0001", strings.Split(pem1, "\n")[2])
	}
	// the second profile has its own key, so it works without Config.PrivateKey; DEFAULT then is one that fails
	ad, err := New(Config{ConfigFile: text})
	if err != nil {
		t.Fatal(err)
	}
	a := ad.(*adapter)
	if len(a.profiles) != 1 || a.profiles[0].account != "second" || !reflect.DeepEqual(a.broken, []string{"default"}) {
		t.Fatalf("profiles %v broken %v", a.profiles, a.broken)
	}
	// a DEFAULT holding only shared values is not an account of its own, and the others inherit from it
	shared := "[DEFAULT]\nregion=us-ashburn-1\nfingerprint=" + fp1 + "\n[Work]\nuser=" + user1 + "\ntenancy=" + tenancy1 + "\n"
	ad, err = New(Config{ConfigFile: shared, PrivateKey: pem1})
	if err != nil {
		t.Fatal(err)
	}
	a = ad.(*adapter)
	if len(a.profiles) != 1 || a.profiles[0].account != "work" || a.profiles[0].region != "us-ashburn-1" || len(a.broken) != 0 {
		t.Fatalf("profiles %+v broken %v", a.profiles[0], a.broken)
	}
	sv := strings.Join(a.SecretValues(), "\n")
	for _, s := range []string{user1, fp1, tenancy1, tenancy1 + "/" + user1 + "/" + fp1} {
		if !strings.Contains(sv, s) {
			t.Fatalf("SecretValues lacks %q", s)
		}
	}
}

func TestAddressReadFailures(t *testing.T) {
	e := newEnv(t, false, "")
	ref := wire.CloudInstanceRef{Account: "default", ID: idRunning}
	// a secondary VNIC this user may not read (or one detached meanwhile) answers 404: the instance still exists
	e.f.hook = func(service, region, method, path string, q url.Values) (int, string, bool) {
		if strings.HasSuffix(path, "/vnics/ocid1.vnic.oc1.iad.second01") {
			return 404, `{"code":"NotAuthorizedOrNotFound","message":"Authorization failed or requested resource not found."}`, true
		}
		return 0, "", false
	}
	in, err := e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	if in.State != "running" || !reflect.DeepEqual(in.PublicIps, []string{"203.0.113.10", "2001:db8::10"}) {
		t.Fatalf("one VNIC unreadable: %+v", in)
	}
	// the attachment list answering 404 is not the instance being gone
	e.f.hook = func(service, region, method, path string, q url.Values) (int, string, bool) {
		if path == "/20160918/vnicAttachments" {
			return 404, `{"code":"NotAuthorizedOrNotFound","message":"Authorization failed or requested resource not found."}`, true
		}
		return 0, "", false
	}
	_, err = e.p.Instance(context.Background(), wire.CloudInstanceParams{Instance: ref})
	if coretest.Code(err) != wire.CloudErrorCodeFailed || !strings.Contains(err.Error(), "addresses") {
		t.Fatalf("attachments 404: %v", err)
	}
	coretest.NoLeak(t, err.Error(), e.secrets...)
}

func TestSameTenancyListedOnce(t *testing.T) {
	e := newEnv(t, false, "")
	k1, _ := keys(t)
	e.f.pubs[tenancy1+"/"+user3+"/"+fp3] = &k1.PublicKey
	text := "[DEFAULT]\nuser=" + user1 + "\nfingerprint=" + fp1 + "\ntenancy=" + tenancy1 + "\nregion=us-ashburn-1\n" +
		"[ADMIN]\nuser=" + user3 + "\nfingerprint=" + fp3 + "\ntenancy=" + tenancy1 + "\nregion=us-ashburn-1\n"
	ad, err := New(Config{ConfigFile: text, PrivateKey: pemPKCS1(k1), Endpoint: e.f.endpoint, Now: e.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	list, problems, err := ad.Instances(context.Background())
	if err != nil || len(problems) != 0 {
		t.Fatalf("problems %v err %v", problems, err)
	}
	if len(list) != 5 {
		t.Fatalf("%d instances for the 5 of one tenancy and region", len(list))
	}
	for _, in := range list {
		// profiles are taken in name order, so the first to see them is admin
		if in.Account != "admin" {
			t.Fatalf("%s listed under %s", in.ID, in.Account)
		}
	}
}
