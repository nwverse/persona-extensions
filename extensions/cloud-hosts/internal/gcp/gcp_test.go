package gcp

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

var ctx = context.Background()

func ref(project, id string) wire.CloudInstanceRef {
	return wire.CloudInstanceRef{Account: wire.CloudAccountID(project), ID: wire.CloudInstanceID(id)}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestNewNeedsUsableKeys(t *testing.T) {
	keys := rsaKeys(t)
	good := pemPKCS8(t, keys[0])
	// Not a key: a PEM block of filler bytes, made here so no key-shaped text is committed.
	garbage := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("test-not-a-key-0001 test-not-a-key-0001")}))
	cases := map[string]Config{
		"empty":        {},
		"not json":     {ServiceAccounts: "test-not-json-secret-0001"},
		"user creds":   {ServiceAccounts: `{"type":"authorized_user","client_id":"x","client_secret":"test-client-secret-0001","refresh_token":"test-refresh-0001"}`},
		"no key":       {ServiceAccounts: `{"type":"service_account","project_id":"proj-alpha","client_email":"sa-alpha@example.com"}`},
		"garbage key":  {ServiceAccounts: keyJSON(t, "proj-alpha", emailAlpha, kidAlpha, garbage)},
		"no project":   {ServiceAccounts: keyJSON(t, "", emailAlpha, kidAlpha, good)},
		"bad project":  {ServiceAccounts: keyJSON(t, "Proj_Alpha!", emailAlpha, kidAlpha, good)},
		"bad projects": {ServiceAccounts: keyJSON(t, "proj-alpha", emailAlpha, kidAlpha, good), Projects: []string{"test-not-a-project-secret-0001!"}},
		"no email":     {ServiceAccounts: keyJSON(t, "proj-alpha", "", kidAlpha, good)},
		"broken array": {ServiceAccounts: `[` + keyJSON(t, "proj-alpha", emailAlpha, kidAlpha, good)},
		"empty array":  {ServiceAccounts: `[]`},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(cfg)
			if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
				t.Fatalf("New: %v (%s), want not_signed_in", err, coretest.Code(err))
			}
			coretest.NoLeak(t, err.Error(), "test-not-json-secret-0001", "test-client-secret-0001", "test-refresh-0001",
				strings.Split(garbage, "\n")[1],
				"test-not-a-project-secret-0001", kidAlpha)
		})
	}
	if _, err := New(Config{ServiceAccounts: keyJSON(t, "proj-alpha", emailAlpha, kidAlpha, good)}); err != nil {
		t.Fatalf("a good key: %v", err)
	}
}

func TestKeyShapes(t *testing.T) {
	keys := rsaKeys(t)
	a := keyJSON(t, "proj-alpha", emailAlpha, kidAlpha, pemPKCS8(t, keys[0]))
	b := keyJSON(t, "proj-beta", emailBeta, kidBeta, pemPKCS1(keys[1]))
	for name, text := range map[string]string{"array": "[" + a + "," + b + "]", "concatenated": a + b, "lines": a + "\n\n" + b + "\n"} {
		ad, err := New(Config{ServiceAccounts: text, Projects: []string{"proj-gamma", "proj-alpha", " "}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		g := ad.(*adapter)
		var got []string
		for _, acc := range g.accounts {
			got = append(got, acc.project+"="+acc.key.email)
		}
		want := []string{"proj-alpha=" + emailAlpha, "proj-beta=" + emailBeta, "proj-gamma=" + emailAlpha}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: accounts %v, want %v", name, got, want)
		}
		sv := g.SecretValues()
		for _, s := range []string{kidAlpha, kidBeta, g.keys[0].pemText, g.keys[1].pemText} {
			if !slices.Contains(sv, s) {
				t.Fatalf("%s: SecretValues misses a credential", name)
			}
		}
		for _, s := range []string{emailAlpha, emailBeta, "proj-alpha", "proj-beta", "proj-gamma"} {
			if slices.Contains(sv, s) {
				t.Fatalf("%s: SecretValues names %q, which is not a credential", name, s)
			}
		}
	}
}

func TestInstancesAcrossProjects(t *testing.T) {
	s := newSetup(t)
	s.f.pageSize = 2
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-b", "web-2", "TERMINATED"))
	s.f.add("proj-alpha", vm("proj-alpha", "europe-west4-a", "db-1", "STAGING"))
	s.f.add("proj-gamma", vm("proj-gamma", "asia-east1-a", "batch-1", "RUNNING"))
	s.f.override = func(w http.ResponseWriter, r *http.Request, rest string) bool {
		if strings.Contains(r.URL.Path, "/proj-beta/") {
			gErr(w, http.StatusForbidden, "forbidden", "Required 'compute.instances.list' permission for 'projects/proj-beta'")
			return true
		}
		return false
	}

	list, problems, err := s.ad.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(problems, []string{"proj-beta"}) {
		t.Fatalf("problems %v", problems)
	}
	var ids []string
	for _, in := range list {
		ids = append(ids, string(in.Account)+":"+string(in.ID))
	}
	want := []string{"proj-alpha:us-central1-a/web-1", "proj-alpha:us-central1-b/web-2", "proj-alpha:europe-west4-a/db-1", "proj-gamma:asia-east1-a/batch-1"}
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("instances %v, want %v", ids, want)
	}
	if n := s.f.callCount("GET /compute/v1/projects/proj-alpha/aggregated/instances"); n != 2 {
		t.Fatalf("alpha was listed in %d pages, want 2", n)
	}
	if !slices.ContainsFunc(s.f.calls, func(c string) bool { return strings.Contains(c, "pageToken=page-2") }) {
		t.Fatalf("the second page was not asked for: %v", s.f.calls)
	}

	ans, err := s.p.Instances(ctx, wire.CloudInstancesParams{Contribution: "gcp", Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", ans)
	coretest.NoLeak(t, string(raw), s.secrets...)
	if !ans.Partial || !strings.Contains(ans.Detail, "proj-beta") || len(ans.Instances) != 4 {
		t.Fatalf("list %+v", ans)
	}
}

func TestInstancesUnreachableZone(t *testing.T) {
	s := newSetup(t, func(c *Config) { c.Projects = nil })
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	s.f.override = func(w http.ResponseWriter, r *http.Request, rest string) bool {
		if rest == "aggregated/instances" && strings.Contains(r.URL.Path, "proj-alpha") {
			writeJSON(w, map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"instances": []any{vm("proj-alpha", "us-central1-a", "web-1", "RUNNING")}}},
				"unreachables": []any{"zones/us-east1-b"}})
			return true
		}
		return false
	}
	list, problems, err := s.ad.Instances(ctx)
	if err != nil || len(list) != 1 || !slices.Equal(problems, []string{"proj-alpha"}) {
		t.Fatalf("list %d, problems %v, err %v", len(list), problems, err)
	}
}

func TestInstancesAllProjectsFail(t *testing.T) {
	s := newSetup(t)
	s.f.override = func(w http.ResponseWriter, r *http.Request, rest string) bool {
		gErr(w, http.StatusForbidden, "accessNotConfigured", "Compute Engine API has not been used in project 0 before or it is disabled.")
		return true
	}
	_, _, err := s.ad.Instances(ctx)
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("err %v", err)
	}
	_, err = s.p.Instances(ctx, wire.CloudInstancesParams{Refresh: true})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("provider err %v", err)
	}
}

func TestStateMapping(t *testing.T) {
	const (
		before = "2026-11-19T00:00:00.000-08:00" // older than lastStartTimestamp
		after  = "2026-11-25T03:00:00.000-08:00" // newer
	)
	running := []wire.CloudActionKind{"stop", "hard_reset", "sync_ip", "snapshot_list", "snapshot_create", "console"}
	off := []wire.CloudActionKind{"start", "sync_ip", "snapshot_list", "snapshot_create", "console"}
	moving := []wire.CloudActionKind{"sync_ip", "snapshot_list", "console"}
	cases := []struct {
		status  string
		spot    string // "", "spot", "preemptible"
		opAt    string // a preemption operation's insertTime
		message string
		state   wire.CloudInstanceState
		actions []wire.CloudActionKind
	}{
		{"PROVISIONING", "", "", "", "starting", moving},
		{"STAGING", "", "", "", "starting", moving},
		{"RUNNING", "", "", "", "running", running},
		{"RUNNING", "spot", after, "", "running", running},
		{"STOPPING", "", "", "", "stopping", moving},
		{"SUSPENDING", "", "", "", "stopping", moving},
		{"REPAIRING", "", "", "", "unknown", moving},
		{"TERMINATED", "", "", "", "stopped", off},
		{"TERMINATED", "", after, "", "stopped", off}, // not spot: never preempted
		{"TERMINATED", "spot", after, "", "preempted", off},
		{"TERMINATED", "preemptible", after, "", "preempted", off},
		{"TERMINATED", "spot", before, "", "stopped", off}, // preempted once, started and stopped since
		{"TERMINATED", "spot", "", "Instance was preempted.", "stopped", off},
		{"SUSPENDED", "", "", "", "stopped", off},
		{"SUSPENDED", "spot", after, "", "preempted", off},
		{"SOMETHING_NEW", "", "", "", "unknown", moving},
	}
	for _, c := range cases {
		name := c.status + "/" + c.spot + "/" + c.opAt
		t.Run(name, func(t *testing.T) {
			s := newSetup(t)
			m := vm("proj-alpha", "us-central1-a", "web-1", c.status)
			switch c.spot {
			case "spot":
				spot(m)
			case "preemptible":
				m["scheduling"] = map[string]any{"preemptible": true}
			}
			if c.message != "" {
				m["statusMessage"] = c.message
			}
			s.f.add("proj-alpha", m)
			if c.opAt != "" {
				link := selfLink("proj-alpha", "us-central1-a", "web-1")
				s.f.ops[link] = []map[string]any{
					{"kind": "compute#operation", "name": "systemevent-1", "operationType": "compute.instances.preempted",
						"targetLink": link, "status": "DONE", "insertTime": c.opAt, "user": "system"},
					{"kind": "compute#operation", "name": "systemevent-0", "operationType": "compute.instances.preempted",
						"targetLink": link, "status": "DONE", "insertTime": "2026-10-02T00:00:00.000-07:00", "user": "system"},
				}
			}
			in, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Refresh: true})
			if err != nil {
				t.Fatal(err)
			}
			coretest.Valid(t, "cloud.instance", in)
			if in.State != c.state {
				t.Fatalf("state %s, want %s", in.State, c.state)
			}
			if !slices.Equal(in.Actions, c.actions) {
				t.Fatalf("actions %v, want %v", in.Actions, c.actions)
			}
			if (c.spot != "") != (in.Lifecycle != nil && in.Lifecycle.Spot) {
				t.Fatalf("lifecycle %+v", in.Lifecycle)
			}
			if c.status == "REPAIRING" && in.StateDetail == "" {
				t.Fatal("REPAIRING has no detail")
			}
			checked := s.f.callCount("GET /compute/v1/projects/proj-alpha/zones/us-central1-a/operations")
			wantCheck := c.spot != "" && (c.status == "TERMINATED" || c.status == "SUSPENDED")
			if (checked > 0) != wantCheck {
				t.Fatalf("operations looked up %d times", checked)
			}
		})
	}
}

func TestPreemptionFallbackWhenOperationsUnreadable(t *testing.T) {
	s := newSetup(t)
	m := spot(vm("proj-alpha", "us-central1-a", "web-1", "TERMINATED"))
	m["statusMessage"] = "Instance was preempted."
	s.f.add("proj-alpha", m)
	s.f.override = func(w http.ResponseWriter, r *http.Request, rest string) bool {
		if strings.HasSuffix(rest, "/operations") {
			gErr(w, http.StatusForbidden, "forbidden", "Required 'compute.zoneOperations.list' permission")
			return true
		}
		return false
	}
	in, err := s.ad.Instance(ctx, ref("proj-alpha", "us-central1-a/web-1"))
	if err != nil || in.State != wire.CloudInstanceStatePreempted {
		t.Fatalf("state %s, err %v", in.State, err)
	}
}

func TestInstanceFields(t *testing.T) {
	s := newSetup(t)
	m := vm("proj-alpha", "us-central1-a", "web-1", "RUNNING")
	m["machineType"] = apiHost + "projects/proj-alpha/zones/us-central1-a/machineTypes/n2-custom-4-8192"
	m["networkInterfaces"] = []any{
		map[string]any{"networkIP": "10.128.0.2", "ipv6Address": "2001:db8:10::2",
			"accessConfigs":     []any{map[string]any{"natIP": "203.0.113.10"}},
			"ipv6AccessConfigs": []any{map[string]any{"externalIpv6": "2001:db8:1::5", "externalIpv6PrefixLength": 96}}},
		map[string]any{"networkIP": "172.16.0.4", "accessConfigs": []any{map[string]any{"natIP": "198.51.100.4"}}},
	}
	m["disks"] = []any{
		map[string]any{"boot": false, "source": diskLink("proj-alpha", "us-central1-a", "data"), "diskSizeGb": "500"},
		map[string]any{"boot": true, "source": diskLink("proj-alpha", "us-central1-a", "web-1"), "diskSizeGb": "50", "architecture": "ARM64",
			"licenses": []any{apiHost + "projects/ubuntu-os-cloud/global/licenses/ubuntu-2404-lts"}},
	}
	s.f.add("proj-alpha", m)
	in, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: ref("proj-alpha", "us-central1-a/web-1")})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instance", in)
	coretest.NoLeak(t, string(raw), s.secrets...)
	if !slices.Equal(in.PublicIps, []string{"203.0.113.10", "2001:db8:1::5", "198.51.100.4"}) {
		t.Fatalf("public %v", in.PublicIps)
	}
	if !slices.Equal(in.PrivateIps, []string{"10.128.0.2", "2001:db8:10::2", "172.16.0.4"}) {
		t.Fatalf("private %v", in.PrivateIps)
	}
	want := wire.CloudSpec{Size: "n2-custom-4-8192", Cpu: 4, MemoryMb: 8192, DiskGb: 50, Arch: "arm64", OS: "ubuntu-2404-lts"}
	if in.Spec == nil || *in.Spec != want {
		t.Fatalf("spec %+v", in.Spec)
	}
	if in.Region != "us-central1" || in.Zone != "us-central1-a" || in.DisplayName != "web-1" || in.Lifecycle != nil {
		t.Fatalf("instance %+v", in)
	}
	if in.ConsoleURL != "https://console.cloud.google.com/compute/instancesDetail/zones/us-central1-a/instances/web-1?project=proj-alpha" {
		t.Fatalf("console %s", in.ConsoleURL)
	}
	if !in.ObservedAt.Equal(testNow) {
		t.Fatalf("observed %v", in.ObservedAt)
	}
	for mt, want := range map[string]wire.CloudSpec{
		"e2-micro":       {Size: "e2-micro", Cpu: 0.25, MemoryMb: 1024},
		"e2-standard-8":  {Size: "e2-standard-8", Cpu: 8},
		"custom-2-4096":  {Size: "custom-2-4096", Cpu: 2, MemoryMb: 4096},
		"t2a-standard-1": {Size: "t2a-standard-1", Cpu: 1},
		"a3-highgpu-8g":  {Size: "a3-highgpu-8g"}, // the 8 counts GPUs, not vCPUs
		"a2-ultragpu-1g": {Size: "a2-ultragpu-1g"},
		"m1-ultramem-40": {Size: "m1-ultramem-40", Cpu: 40},
		"m1-whatever":    {Size: "m1-whatever"},
	} {
		if got := specFor(mt); got != want {
			t.Errorf("specFor(%s) = %+v, want %+v", mt, got, want)
		}
	}
}

func TestNoBootDiskNoSnapshots(t *testing.T) {
	s := newSetup(t)
	m := vm("proj-alpha", "us-central1-a", "web-1", "RUNNING")
	m["disks"] = []any{}
	s.f.add("proj-alpha", m)
	in, err := s.ad.Instance(ctx, ref("proj-alpha", "us-central1-a/web-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(in.Actions, []wire.CloudActionKind{"stop", "hard_reset", "sync_ip", "console"}) {
		t.Fatalf("actions %v", in.Actions)
	}
	if _, err := s.ad.Snapshots(ctx, ref("proj-alpha", "us-central1-a/web-1")); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("snapshots: %v", err)
	}
	_, err = s.ad.Action(ctx, core.Action{Ref: ref("proj-alpha", "us-central1-a/web-1"), Kind: wire.CloudActionKindSnapshotCreate, RequestID: "req-1"})
	if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("snapshot_create: %v", err)
	}
}

func TestActions(t *testing.T) {
	cases := []struct {
		kind wire.CloudActionKind
		verb string
	}{{"start", "start"}, {"stop", "stop"}, {"hard_reset", "reset"}}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			s := newSetup(t)
			s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
			res, err := s.p.Action(ctx, wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: c.kind,
				RequestID: "req-" + wire.CloudRequestID(c.kind), Locale: "en"})
			if err != nil {
				t.Fatal(err)
			}
			coretest.Valid(t, "cloud.action", res)
			if res.Status != wire.CloudActionStatusAccepted {
				t.Fatalf("status %s", res.Status)
			}
			wantPath := "POST /compute/v1/projects/proj-alpha/zones/us-central1-a/instances/web-1/" + c.verb + "?requestId="
			if n := s.f.callCount(wantPath); n != 1 {
				t.Fatalf("%s called %d times: %v", wantPath, n, s.f.calls)
			}
			wantID := requestUUID(core.Action{Ref: ref("proj-alpha", "us-central1-a/web-1"), Kind: c.kind, RequestID: "req-" + wire.CloudRequestID(c.kind)})
			if s.f.lastQuery.Get("requestId") != wantID {
				t.Fatalf("requestId %q, want %q", s.f.lastQuery.Get("requestId"), wantID)
			}
		})
	}
}

func TestActionDoneWhenOperationDone(t *testing.T) {
	s := newSetup(t)
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	s.f.override = func(w http.ResponseWriter, r *http.Request, rest string) bool {
		if r.Method == http.MethodPost {
			writeJSON(w, map[string]any{"kind": "compute#operation", "name": "operation-1", "status": "DONE", "operationType": "stop"})
			return true
		}
		return false
	}
	res, err := s.ad.Action(ctx, core.Action{Ref: ref("proj-alpha", "us-central1-a/web-1"), Kind: wire.CloudActionKindStop, RequestID: "req-1"})
	if err != nil || res.Status != wire.CloudActionStatusDone {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUnsupported(t *testing.T) {
	s := newSetup(t)
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	for _, k := range []wire.CloudActionKind{"reboot", "force_stop", "custom", "console", "something"} {
		_, err := s.ad.Action(ctx, core.Action{Ref: ref("proj-alpha", "us-central1-a/web-1"), Kind: k, RequestID: "req-1"})
		if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("%s: %v", k, err)
		}
	}
	for _, k := range []wire.CloudActionKind{"reboot", "force_stop", "snapshot_list"} {
		_, err := s.p.Action(ctx, wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: k, RequestID: "req-2-" + wire.CloudRequestID(k)})
		if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("provider %s: %v", k, err)
		}
	}
	_, err := s.p.Action(ctx, wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: "custom", Custom: "renew", RequestID: "req-3"})
	if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("custom: %v", err)
	}
	if n := s.f.callCount("POST "); n != 0 {
		t.Fatalf("the vendor was asked to act %d times", n)
	}
}

func TestRequestUUID(t *testing.T) {
	a := core.Action{Ref: ref("proj-alpha", "us-central1-a/web-1"), Kind: "start", RequestID: "req-1"}
	b := a
	b.RequestID = "req-2"
	if requestUUID(a) != requestUUID(a) || requestUUID(a) == requestUUID(b) || !uuidRe.MatchString(requestUUID(a)) {
		t.Fatalf("%s %s", requestUUID(a), requestUUID(b))
	}
}

// The case start-and-reconnect is for: a preempted spot VM without an address is started, start is accepted at
// once, and the next read says running with a new address.
func TestStartAndReconnect(t *testing.T) {
	s := newSetup(t)
	const lastKnown = "203.0.113.10"
	m := spot(vm("proj-alpha", "us-central1-a", "spot-1", "TERMINATED"))
	setIP(m, "")
	s.f.add("proj-alpha", m)
	link := selfLink("proj-alpha", "us-central1-a", "spot-1")
	s.f.ops[link] = []map[string]any{{"operationType": "compute.instances.preempted", "targetLink": link, "status": "DONE",
		"insertTime": "2026-11-26T23:00:00.000-08:00"}}
	r := ref("proj-alpha", "us-central1-a/spot-1")

	before, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: r, Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", before)
	if before.State != wire.CloudInstanceStatePreempted || len(before.PublicIps) != 0 || !core.Offers(before.Actions, "start") {
		t.Fatalf("before: %+v", before)
	}

	res, err := s.p.Action(ctx, wire.CloudActionParams{Instance: r, Action: "start", RequestID: "req-start-1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Status != wire.CloudActionStatusAccepted {
		t.Fatalf("start: %+v", res)
	}

	after, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: r, Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", after)
	if after.State != wire.CloudInstanceStateRunning || len(after.PublicIps) == 0 || after.PublicIps[0] != "203.0.113.77" || after.PublicIps[0] == lastKnown {
		t.Fatalf("after: %+v", after)
	}

	// sync_ip reads it again, and a second address change is seen at once: nothing is cached.
	s.f.mu.Lock()
	setIP(m, "203.0.113.99")
	s.f.mu.Unlock()
	res, err = s.p.Action(ctx, wire.CloudActionParams{Instance: r, Action: "sync_ip", RequestID: "req-sync-1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Status != wire.CloudActionStatusDone || res.Instance == nil || res.Instance.PublicIps[0] != "203.0.113.99" {
		t.Fatalf("sync_ip: %+v", res)
	}
}

func TestIdempotentThroughProvider(t *testing.T) {
	s := newSetup(t)
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "TERMINATED"))
	p := wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: "start", RequestID: "req-same"}
	for i := 0; i < 3; i++ {
		res, err := s.p.Action(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		coretest.Valid(t, "cloud.action", res)
	}
	if s.f.posts["start"] != 1 {
		t.Fatalf("start was sent %d times", s.f.posts["start"])
	}
	p.RequestID = "req-other"
	if _, err := s.p.Action(ctx, p); err != nil {
		t.Fatal(err)
	}
	if s.f.posts["start"] != 2 {
		t.Fatalf("start was sent %d times", s.f.posts["start"])
	}
}

func TestVendorErrors(t *testing.T) {
	keyLine := func(s *setup) string { return s.secrets[6] } // a line of the alpha private key
	cases := []struct {
		name   string
		status int
		body   func(s *setup) string
		want   wire.CloudErrorCode
	}{
		{"404", 404, func(*setup) string {
			return `{"error":{"code":404,"message":"The resource 'projects/proj-alpha/zones/us-central1-a/instances/web-1' was not found","errors":[{"reason":"notFound"}]}}`
		}, "not_found"},
		{"401 echoing the token", 401, func(*setup) string {
			return `{"error":{"code":401,"message":"Request had invalid authentication credentials: Bearer ` + tokAlpha + `","errors":[{"reason":"authError"}],"status":"UNAUTHENTICATED"}}`
		}, "not_signed_in"},
		{"403 forbidden echoing the key", 403, func(s *setup) string {
			return `{"error":{"code":403,"message":"Required 'compute.instances.start' permission; key ` + keyLine(s) + ` id ` + kidAlpha + `","errors":[{"reason":"forbidden"}]}}`
		}, "not_signed_in"},
		{"403 api off", 403, func(*setup) string {
			return `{"error":{"code":403,"message":"Compute Engine API has not been used in project 0 before or it is disabled.","errors":[{"reason":"accessNotConfigured"}],"status":"PERMISSION_DENIED"}}`
		}, "not_signed_in"},
		{"400 not ready", 400, func(*setup) string {
			return `{"error":{"code":400,"message":"The resource 'projects/proj-alpha/zones/us-central1-a/instances/web-1' is not ready","errors":[{"reason":"resourceNotReady"}]}}`
		}, "invalid_state"},
		{"412 condition", 412, func(*setup) string {
			return `{"error":{"code":412,"message":"Condition not met","errors":[{"reason":"conditionNotMet"}]}}`
		}, "invalid_state"},
		{"400 failed precondition", 400, func(*setup) string {
			return `{"error":{"code":400,"message":"Instance is in an unexpected state","status":"FAILED_PRECONDITION"}}`
		}, "invalid_state"},
		{"429", 429, func(*setup) string {
			return `{"error":{"code":429,"message":"Too many requests","status":"RESOURCE_EXHAUSTED"}}`
		}, "rate_limited"},
		{"403 rate limit", 403, func(*setup) string {
			return `{"error":{"code":403,"message":"Rate Limit Exceeded","errors":[{"reason":"rateLimitExceeded"}]}}`
		}, "rate_limited"},
		{"403 user rate limit", 403, func(*setup) string {
			return `{"error":{"code":403,"message":"User Rate Limit Exceeded","errors":[{"reason":"userRateLimitExceeded"}]}}`
		}, "rate_limited"},
		{"403 quota", 403, func(*setup) string {
			return `{"error":{"code":403,"message":"Quota 'CPUS' exceeded. Limit: 8.0 in region us-central1.","errors":[{"reason":"quotaExceeded"}]}}`
		}, "capacity"},
		{"400 stockout", 400, func(*setup) string {
			return `{"error":{"code":400,"message":"The zone 'projects/proj-alpha/zones/us-central1-a' does not have enough resources available to fulfill the request.","errors":[{"reason":"ZONE_RESOURCE_POOL_EXHAUSTED_WITH_DETAILS"}]}}`
		}, "capacity"},
		{"429 stockout", 429, func(*setup) string {
			return `{"error":{"code":429,"message":"Stockout of this machine type in the zone","status":"RESOURCE_EXHAUSTED"}}`
		}, "capacity"},
		{"401 echoing the bare token", 401, func(*setup) string {
			return `{"error":{"code":401,"message":"Token ` + tokAlpha + ` is expired or revoked","errors":[{"reason":"authError"}],"status":"UNAUTHENTICATED"}}`
		}, "not_signed_in"},
		{"409 already exists", 409, func(*setup) string {
			return `{"error":{"code":409,"message":"The resource 'projects/proj-alpha/global/snapshots/nightly' already exists","errors":[{"reason":"alreadyExists"}]}}`
		}, "refused"},
		{"500", 500, func(*setup) string { return `{"error":{"code":500,"message":"Internal error"}}` }, "failed"},
		{"html", 502, func(*setup) string { return `<html>bad gateway</html>` }, "failed"},
		{"400 other", 400, func(*setup) string {
			return `{"error":{"code":400,"message":"Invalid value for field 'resource'","errors":[{"reason":"invalid"}]}}`
		}, "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSetup(t)
			s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
			body := c.body(s)
			s.f.override = func(w http.ResponseWriter, r *http.Request, rest string) bool {
				if r.Method == http.MethodPost {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(c.status)
					_, _ = w.Write([]byte(body))
					return true
				}
				return false
			}
			_, aerr := s.ad.Action(ctx, core.Action{Ref: ref("proj-alpha", "us-central1-a/web-1"), Kind: "stop", RequestID: "req-1"})
			if coretest.Code(aerr) != c.want {
				t.Fatalf("adapter: %v (%s), want %s", aerr, coretest.Code(aerr), c.want)
			}
			coretest.NoLeak(t, errText(aerr), s.secrets...)
			_, perr := s.p.Action(ctx, wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: "stop", RequestID: "req-2"})
			if coretest.Code(perr) != c.want {
				t.Fatalf("provider: %v, want %s", perr, c.want)
			}
			coretest.NoLeak(t, errText(perr), s.secrets...)
			if strings.ContainsAny(errText(perr), "\n\r") {
				t.Fatalf("message is not one line: %q", errText(perr))
			}
		})
	}
}

func TestOperationErrorOnAnswer(t *testing.T) {
	cases := map[string]wire.CloudErrorCode{
		"ZONE_RESOURCE_POOL_EXHAUSTED": "capacity",
		"QUOTA_EXCEEDED":               "capacity",
		"RESOURCE_NOT_READY":           "invalid_state",
		"RESOURCE_NOT_FOUND":           "not_found",
		"RATE_LIMIT_EXCEEDED":          "rate_limited",
		"SOMETHING_ELSE":               "failed",
	}
	for code, want := range cases {
		t.Run(code, func(t *testing.T) {
			s := newSetup(t)
			s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "TERMINATED"))
			s.f.override = func(w http.ResponseWriter, r *http.Request, rest string) bool {
				if r.Method == http.MethodPost {
					writeJSON(w, map[string]any{"kind": "compute#operation", "name": "operation-1", "status": "DONE",
						"httpErrorStatusCode": 400, "httpErrorMessage": "BAD REQUEST",
						"error": map[string]any{"errors": []any{map[string]any{"code": code, "message": "The operation failed: " + code + " token " + tokAlpha}}}})
					return true
				}
				return false
			}
			_, err := s.p.Action(ctx, wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: "start", RequestID: "req-1"})
			if coretest.Code(err) != want {
				t.Fatalf("%v, want %s", err, want)
			}
			coretest.NoLeak(t, errText(err), s.secrets...)
		})
	}
}

func TestNotFound(t *testing.T) {
	s := newSetup(t)
	for _, r := range []wire.CloudInstanceRef{
		ref("proj-alpha", "us-central1-a/gone"),
		ref("proj-alpha", "no-slash"),
		ref("proj-alpha", "us-central1-a/Bad_Name"),
		ref("proj-alpha", "us-central1-a/web-1/extra"),
		ref("proj-unknown", "us-central1-a/web-1"),
	} {
		_, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: r})
		if coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("%v: %v", r, err)
		}
		_, err = s.p.Action(ctx, wire.CloudActionParams{Instance: r, Action: "start", RequestID: "req-" + wire.CloudRequestID(r.ID)})
		if coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("action %v: %v", r, err)
		}
	}
}

func TestTokenEndpointErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   wire.CloudErrorCode
	}{
		{"invalid_grant", 400, `{"error":"invalid_grant","error_description":"Invalid JWT: Token must be a short-lived token for key ` + kidAlpha + `"}`, "not_signed_in"},
		{"invalid_client", 401, `{"error":"invalid_client","error_description":"The OAuth client was not found."}`, "not_signed_in"},
		{"throttled", 429, `{"error":"rate_limit_exceeded"}`, "rate_limited"},
		{"broken", 503, `unavailable`, "failed"},
		{"no token", 200, `{"token_type":"Bearer"}`, "failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSetup(t)
			s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
			s.f.tokenAnswer = func(w http.ResponseWriter) bool {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
				return true
			}
			_, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: ref("proj-alpha", "us-central1-a/web-1")})
			if coretest.Code(err) != c.want {
				t.Fatalf("%v, want %s", err, c.want)
			}
			coretest.NoLeak(t, errText(err), s.secrets...)
			if s.f.callCount("GET ") != 0 {
				t.Fatal("Compute Engine was asked without a token")
			}
		})
	}
}

func TestBadSignatureIsNotSignedIn(t *testing.T) {
	s := newSetup(t)
	other := rsaKeys(t)[1]
	s.f.mu.Lock()
	s.f.pubs[emailAlpha] = &other.PublicKey // Google knows another key for this account
	s.f.mu.Unlock()
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	_, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: ref("proj-alpha", "us-central1-a/web-1")})
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("%v", err)
	}
}

func TestTokenCacheAndSingleFlight(t *testing.T) {
	s := newSetup(t)
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	s.f.add("proj-gamma", vm("proj-gamma", "us-central1-a", "web-9", "RUNNING"))
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.ad.Instance(ctx, ref("proj-alpha", "us-central1-a/web-1"))
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := s.ad.Instance(ctx, ref("proj-gamma", "us-central1-a/web-9")) // the same key
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.f.tokenCalls != 1 {
		t.Fatalf("the token was fetched %d times", s.f.tokenCalls)
	}
	s.clock.add(50 * time.Minute)
	if _, err := s.ad.Instance(ctx, ref("proj-alpha", "us-central1-a/web-1")); err != nil || s.f.tokenCalls != 1 {
		t.Fatalf("still fresh: %d calls, %v", s.f.tokenCalls, err)
	}
	s.clock.add(9 * time.Minute) // within two minutes of Google's expiry
	if _, err := s.ad.Instance(ctx, ref("proj-alpha", "us-central1-a/web-1")); err != nil || s.f.tokenCalls != 2 {
		t.Fatalf("refresh: %d calls, %v", s.f.tokenCalls, err)
	}

	// A token Google no longer accepts is dropped: the next call fetches a new one.
	s.f.mu.Lock()
	s.f.projectTok["proj-alpha"] = "test-access-token-0003"
	s.f.tokens[emailAlpha] = "test-access-token-0003"
	s.f.mu.Unlock()
	if _, err := s.ad.Instance(ctx, ref("proj-alpha", "us-central1-a/web-1")); coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := s.ad.Instance(ctx, ref("proj-alpha", "us-central1-a/web-1")); err != nil || s.f.tokenCalls != 3 {
		t.Fatalf("after revocation: %d calls, %v", s.f.tokenCalls, err)
	}
}

func TestSnapshots(t *testing.T) {
	s := newSetup(t)
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	disk := diskLink("proj-alpha", "us-central1-a", "web-1")
	s.f.snapshots = []map[string]any{
		{"kind": "compute#snapshot", "id": "1", "name": "web-1-older", "status": "READY", "sourceDisk": disk,
			"creationTimestamp": "2026-11-01T10:00:00.000-07:00", "diskSizeGb": "20", "storageBytes": "2147483648"},
		{"kind": "compute#snapshot", "id": "2", "name": "web-1-newer", "status": "CREATING", "sourceDisk": disk,
			"creationTimestamp": "2026-11-26T10:00:00.000-08:00", "diskSizeGb": "20"},
		{"kind": "compute#snapshot", "id": "3", "name": "other-disk", "status": "READY",
			"sourceDisk": diskLink("proj-alpha", "us-central1-a", "other"), "creationTimestamp": "2026-11-20T10:00:00.000-08:00"},
	}
	list, err := s.p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: ref("proj-alpha", "us-central1-a/web-1")})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.snapshots", list)
	if len(list.Snapshots) != 2 || list.Snapshots[0].ID != "web-1-newer" || list.Snapshots[0].State != wire.CloudSnapshotStateCreating ||
		list.Snapshots[1].State != wire.CloudSnapshotStateReady || list.Snapshots[1].SizeGb != 2 || list.Snapshots[0].SizeGb != 20 {
		t.Fatalf("snapshots %+v", list.Snapshots)
	}
	if !strings.Contains(s.f.calls[len(s.f.calls)-1], url.Values{"filter": {`sourceDisk="` + disk + `"`}}.Encode()) {
		t.Fatalf("filter: %s", s.f.calls[len(s.f.calls)-1])
	}

	res, err := s.p.Action(ctx, wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: "snapshot_create",
		Name: "Before the 2026 upgrade!", RequestID: "req-snap-1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Status != wire.CloudActionStatusAccepted || res.Snapshot == nil || res.Snapshot.ID != "before-the-2026-upgrade" {
		t.Fatalf("create %+v %+v", res, res.Snapshot)
	}
	if s.f.lastBody["name"] != "before-the-2026-upgrade" || s.f.lastBody["description"] != "Before the 2026 upgrade!" {
		t.Fatalf("body %v", s.f.lastBody)
	}
	if n := s.f.callCount("POST /compute/v1/projects/proj-alpha/zones/us-central1-a/disks/web-1/createSnapshot?requestId="); n != 1 {
		t.Fatalf("createSnapshot calls %d: %v", n, s.f.calls)
	}

	res, err = s.p.Action(ctx, wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: "snapshot_create", RequestID: "req-snap-2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Snapshot == nil || res.Snapshot.ID != "web-1-20261127-100000" {
		t.Fatalf("unnamed %+v", res.Snapshot)
	}
}

func TestSnapshotName(t *testing.T) {
	now := testNow
	long := strings.Repeat("a", 80)
	for _, c := range [][3]string{
		{"Nightly", "web-1", "nightly"},
		{"  --2026 release--  ", "web-1", "snap-2026-release"},
		{"!!!", "web-1", "web-1-20261127-100000"},
		{"", long, strings.Repeat("a", 47) + "-20261127-100000"},
		{long, "web-1", strings.Repeat("a", 63)},
	} {
		got := snapshotName(c[0], c[1], now)
		if got != c[2] || !nameRe.MatchString(got) {
			t.Errorf("snapshotName(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestConsole(t *testing.T) {
	s := newSetup(t)
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	res, err := s.p.Action(ctx, wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: "console", RequestID: "req-c"})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.action", res)
	coretest.NoLeak(t, string(raw), s.secrets...)
	u, err := url.Parse(res.URL)
	if err != nil || u.Scheme != "https" || u.Host != "console.cloud.google.com" || u.User != nil ||
		u.Path != "/compute/instancesDetail/zones/us-central1-a/instances/web-1" || u.Query().Encode() != "project=proj-alpha" {
		t.Fatalf("console %q", res.URL)
	}
}

func TestDecodeQuotedNumbers(t *testing.T) {
	var v struct{ A, B, C gInt }
	if err := json.Unmarshal([]byte(`{"A":"42","B":7,"C":null}`), &v); err != nil || v.A != 42 || v.B != 7 || v.C != 0 {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestCancelledContext(t *testing.T) {
	s := newSetup(t)
	c, cancel := context.WithCancel(ctx)
	cancel()
	_, err := s.ad.Instance(c, ref("proj-alpha", "us-central1-a/web-1"))
	var e *core.Error
	if !errors.As(err, &e) || e.Code != wire.CloudErrorCodeFailed {
		t.Fatalf("%v", err)
	}
}

// A suspended VM wakes only with instances.resume: start on it must send resume, never start.
func TestStartResumesSuspended(t *testing.T) {
	for _, kind := range []string{"plain", "spot"} {
		t.Run(kind, func(t *testing.T) {
			s := newSetup(t)
			m := vm("proj-alpha", "us-central1-a", "web-1", "SUSPENDED")
			want := wire.CloudInstanceStateStopped
			if kind == "spot" {
				spot(m)
				link := selfLink("proj-alpha", "us-central1-a", "web-1")
				s.f.ops[link] = []map[string]any{{"name": "systemevent-1764200000000-a", "operationType": "compute.instances.preempted",
					"targetLink": link, "status": "DONE", "insertTime": "2026-11-26T23:00:00.000-08:00"}}
				want = wire.CloudInstanceStatePreempted
			}
			s.f.add("proj-alpha", m)
			r := ref("proj-alpha", "us-central1-a/web-1")
			before, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: r, Refresh: true})
			if err != nil || before.State != want || !core.Offers(before.Actions, "start") {
				t.Fatalf("before: %+v %v", before, err)
			}
			res, err := s.p.Action(ctx, wire.CloudActionParams{Instance: r, Action: "start", RequestID: "req-wake-1"})
			if err != nil {
				t.Fatal(err)
			}
			coretest.Valid(t, "cloud.action", res)
			if res.Status != wire.CloudActionStatusAccepted {
				t.Fatalf("status %s", res.Status)
			}
			if n := s.f.callCount("POST /compute/v1/projects/proj-alpha/zones/us-central1-a/instances/web-1/resume?requestId="); n != 1 {
				t.Fatalf("resume sent %d times: %v", n, s.f.calls)
			}
			if s.f.posts["start"] != 0 {
				t.Fatalf("start sent %d times", s.f.posts["start"])
			}
			wantID := requestUUID(core.Action{Ref: r, Kind: "start", RequestID: "req-wake-1"})
			if s.f.lastQuery.Get("requestId") != wantID {
				t.Fatalf("requestId %q, want %q", s.f.lastQuery.Get("requestId"), wantID)
			}
			after, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: r, Refresh: true})
			if err != nil || after.State != wire.CloudInstanceStateRunning || after.PublicIps[0] != "203.0.113.77" {
				t.Fatalf("after: %+v %v", after, err)
			}
		})
	}
}

// Google lists operations by name (oldest first for system events) unless asked for creationTimestamp desc; with
// more preemptions than one page holds, the newest must still be the one judged.
func TestPreemptionNewestOfMany(t *testing.T) {
	s := newSetup(t)
	m := spot(vm("proj-alpha", "us-central1-a", "web-1", "TERMINATED"))
	s.f.add("proj-alpha", m)
	link := selfLink("proj-alpha", "us-central1-a", "web-1")
	var ops []map[string]any
	for i := 0; i < 60; i++ { // all before lastStartTimestamp (2026-11-20)
		ops = append(ops, map[string]any{"name": fmt.Sprintf("systemevent-17600000%05d-a", i), "operationType": "compute.instances.preempted",
			"targetLink": link, "status": "DONE", "insertTime": fmt.Sprintf("2026-10-%02dT00:00:00.000-07:00", 1+i%28)})
	}
	ops = append(ops, map[string]any{"name": "systemevent-1764200000000-z", "operationType": "compute.instances.preempted",
		"targetLink": link, "status": "DONE", "insertTime": "2026-11-26T23:00:00.000-08:00"})
	s.f.ops[link] = ops
	in, err := s.ad.Instance(ctx, ref("proj-alpha", "us-central1-a/web-1"))
	if err != nil || in.State != wire.CloudInstanceStatePreempted {
		t.Fatalf("state %s, err %v", in.State, err)
	}
}

// A refresh run by a caller that gives up does not fail the callers waiting on it whose own contexts are alive.
func TestTokenRefreshSurvivesLeaderCancel(t *testing.T) {
	s := newSetup(t)
	a := s.ad.(*adapter)
	entered, release := make(chan struct{}), make(chan struct{})
	var first sync.Once
	s.f.tokenAnswer = func(w http.ResponseWriter) bool {
		blocked := false
		first.Do(func() { blocked = true })
		if blocked {
			close(entered)
			<-release
			http.Error(w, "late", http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	leaderCtx, cancel := context.WithCancel(ctx)
	leaderErr := make(chan error, 1)
	go func() {
		_, err := a.token(leaderCtx, a.keys[0])
		leaderErr <- err
	}()
	<-entered
	joined := make(chan struct{})
	var once sync.Once
	a.onJoin = func() { once.Do(func() { close(joined) }) }
	waiter := make(chan error, 1)
	var got string
	go func() {
		tok, err := a.token(ctx, a.keys[0])
		got = tok
		waiter <- err
	}()
	<-joined
	cancel()
	if err := <-leaderErr; err == nil {
		t.Fatal("the cancelled leader got a token")
	}
	close(release)
	if err := <-waiter; err != nil || got != tokAlpha {
		t.Fatalf("waiter: %q %v", got, err)
	}
}

// An id that is not "<zone>/<name>" of Compute Engine's shapes (a slash too many, a percent sign, a space) names no
// VM: it is answered not_found without a single request, so it can never become another path.
func TestOddIDsNeverReachGoogle(t *testing.T) {
	s := newSetup(t)
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	for i, r := range []wire.CloudInstanceRef{
		ref("proj-alpha", "us-central1-a/web-1/../../proj-beta"),
		ref("proj-alpha", "us-central1-a/web%2F1"),
		ref("proj-alpha", "us-central1-a/web 1"),
		ref("proj-alpha", "us central1-a/web-1"),
		ref("proj-alpha", "us-central1-a%2Fx/web-1"),
		ref("proj alpha%2F", "us-central1-a/web-1"),
	} {
		if _, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: r}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("%v: %v", r, err)
		}
		if _, err := s.p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: r}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("snapshots %v: %v", r, err)
		}
		if _, err := s.p.Action(ctx, wire.CloudActionParams{Instance: r, Action: "stop", RequestID: wire.CloudRequestID(fmt.Sprintf("req-odd-%d", i))}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("action %v: %v", r, err)
		}
	}
	if len(s.f.calls) != 0 || s.f.tokenCalls != 0 {
		t.Fatalf("Google was asked: %v (%d token calls)", s.f.calls, s.f.tokenCalls)
	}
}

// Each value put in a path is escaped once: one with a slash, a percent sign and a space stays one segment.
func TestPathSegmentsEscaped(t *testing.T) {
	odd := "a/b%c d"
	p := instancePath("proj-alpha", "us-central1-a", odd)
	if p != "projects/proj-alpha/zones/us-central1-a/instances/a%2Fb%25c%20d" {
		t.Fatalf("path %q", p)
	}
	u, err := core.JoinURL("https://compute.example.com/compute/v1", p+"/stop", nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) != 9 || parts[7] != "a%2Fb%25c%20d" || parts[8] != "stop" {
		t.Fatalf("escaped path %q", u.EscapedPath())
	}
	if got, _ := url.PathUnescape(parts[7]); got != odd {
		t.Fatalf("segment %q", got)
	}
}

// Only a rate limit leaves a request undecided: after a 5xx, a timeout or an operation that failed, the same
// request id is answered from the Provider's record and Google is not asked again.
func TestTransientOnlyForRateLimits(t *testing.T) {
	cases := []struct {
		name      string
		answer    func(w http.ResponseWriter)
		want      wire.CloudErrorCode
		transient bool
	}{
		{"429", func(w http.ResponseWriter) {
			gErr(w, http.StatusTooManyRequests, "rateLimitExceeded", "Rate Limit Exceeded")
		}, "rate_limited", true},
		{"403 rate limit", func(w http.ResponseWriter) {
			gErr(w, http.StatusForbidden, "userRateLimitExceeded", "User Rate Limit Exceeded")
		}, "rate_limited", true},
		{"500", func(w http.ResponseWriter) { gErr(w, http.StatusInternalServerError, "backendError", "Internal error") }, "failed", false},
		{"503", func(w http.ResponseWriter) { http.Error(w, "unavailable", http.StatusServiceUnavailable) }, "failed", false},
		{"operation rate limited", func(w http.ResponseWriter) {
			writeJSON(w, map[string]any{"kind": "compute#operation", "name": "operation-1", "status": "DONE", "httpErrorStatusCode": 429,
				"error": map[string]any{"errors": []any{map[string]any{"code": "RATE_LIMIT_EXCEEDED", "message": "Rate limit exceeded"}}}})
		}, "rate_limited", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newSetup(t)
			s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
			s.f.override = func(w http.ResponseWriter, r *http.Request, rest string) bool {
				if r.Method == http.MethodPost {
					c.answer(w)
					return true
				}
				return false
			}
			_, aerr := s.ad.Action(ctx, core.Action{Ref: ref("proj-alpha", "us-central1-a/web-1"), Kind: "stop", RequestID: "req-0"})
			var e *core.Error
			if !errors.As(aerr, &e) || e.Code != c.want || e.Transient != c.transient {
				t.Fatalf("adapter: %#v, want %s transient=%v", aerr, c.want, c.transient)
			}
			p := wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: "stop", RequestID: "req-1"}
			for i := 0; i < 2; i++ {
				if _, err := s.p.Action(ctx, p); coretest.Code(err) != c.want {
					t.Fatalf("try %d: %v, want %s", i, err, c.want)
				}
			}
			wantPosts := 2 // the adapter's own call and the Provider's first
			if c.transient {
				wantPosts = 3
			}
			if n := s.f.callCount("POST "); n != wantPosts {
				t.Fatalf("stop was sent %d times, want %d", n, wantPosts)
			}
		})
	}
}

// The vendor's words are not clipped by the adapter: the Provider redacts first, then clips.
func TestVendorMessageNotClipped(t *testing.T) {
	s := newSetup(t)
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "web-1", "RUNNING"))
	long := "Invalid value " + strings.Repeat("x", 230) + " key " + kidAlpha + " end-of-message"
	s.f.override = func(w http.ResponseWriter, r *http.Request, rest string) bool {
		if r.Method == http.MethodPost {
			gErr(w, http.StatusBadRequest, "invalid", long)
			return true
		}
		return false
	}
	_, err := s.ad.Action(ctx, core.Action{Ref: ref("proj-alpha", "us-central1-a/web-1"), Kind: "stop", RequestID: "req-1"})
	if !strings.HasSuffix(errText(err), "end-of-message") {
		t.Fatalf("clipped: %q", errText(err))
	}
	_, err = s.p.Action(ctx, wire.CloudActionParams{Instance: ref("proj-alpha", "us-central1-a/web-1"), Action: "stop", RequestID: "req-2"})
	coretest.NoLeak(t, errText(err), s.secrets...)
}

// The adapter's own words follow the caller's language; the vendor's state name is passed on as it is.
func TestStateDetailLocale(t *testing.T) {
	s := newSetup(t)
	m := spot(vm("proj-alpha", "us-central1-a", "spot-1", "TERMINATED"))
	s.f.add("proj-alpha", m)
	s.f.add("proj-alpha", vm("proj-alpha", "us-central1-a", "odd-1", "SOMETHING_NEW"))
	link := selfLink("proj-alpha", "us-central1-a", "spot-1")
	s.f.ops[link] = []map[string]any{{"operationType": "compute.instances.preempted", "targetLink": link, "status": "DONE",
		"insertTime": "2026-11-26T23:00:00.000-08:00"}}
	read := func(id, locale string) wire.CloudInstance {
		in, err := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: ref("proj-alpha", id), Locale: locale})
		if err != nil {
			t.Fatal(err)
		}
		coretest.Valid(t, "cloud.instance", in)
		return in
	}
	en, zh := read("us-central1-a/spot-1", "en"), read("us-central1-a/spot-1", "zh-CN")
	if en.StateDetail != "Google Cloud took this spot VM back." || zh.StateDetail == "" || zh.StateDetail == en.StateDetail ||
		zh.State != wire.CloudInstanceStatePreempted {
		t.Fatalf("en %q, zh %q", en.StateDetail, zh.StateDetail)
	}
	if odd := read("us-central1-a/odd-1", "zh-CN"); !strings.HasSuffix(odd.StateDetail, "SOMETHING_NEW") || odd.StateDetail == read("us-central1-a/odd-1", "en").StateDetail {
		t.Fatalf("odd %q", odd.StateDetail)
	}
	_, enErr := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: ref("proj-unknown", "us-central1-a/web-1"), Locale: "en"})
	_, zhErr := s.p.Instance(ctx, wire.CloudInstanceParams{Instance: ref("proj-unknown", "us-central1-a/web-1"), Locale: "zh-CN"})
	if coretest.Code(zhErr) != wire.CloudErrorCodeNotFound || errText(zhErr) == errText(enErr) {
		t.Fatalf("en %v, zh %v", enErr, zhErr)
	}
}
