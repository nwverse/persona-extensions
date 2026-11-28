package aws

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func lsInstanceJSON(name, state, public string) map[string]any {
	in := map[string]any{
		"name": name, "arn": "arn:aws:lightsail:eu-central-1:000000000000:Instance/00000000-0000-0000-0000-000000000000",
		"supportCode": "000000000000/i-0example", "createdAt": 1.7640252e9,
		"location":     map[string]any{"availabilityZone": "eu-central-1a", "regionName": "eu-central-1"},
		"resourceType": "Instance", "blueprintId": "ubuntu_24_04", "blueprintName": "Ubuntu", "bundleId": "small_3_0",
		"isStaticIp": false, "privateIpAddress": "172.26.0.10", "ipv6Addresses": []string{"2001:db8:1::5"},
		"hardware": map[string]any{"cpuCount": 2, "ramSizeInGb": 2.0,
			"disks": []map[string]any{{"sizeInGb": 100, "isSystemDisk": false}, {"sizeInGb": 60, "isSystemDisk": true}}},
		"state": map[string]any{"code": 16, "name": state},
		"tags":  []map[string]any{{"key": "env", "value": "test"}},
	}
	if public != "" {
		in["publicIpAddress"] = public
	}
	return in
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func lsError(typ, msg string) string {
	return mustJSON(map[string]string{"__type": typ, "code": "", "message": msg})
}

func newLightsail(t *testing.T, f *fakeAWS, regions ...string) core.Adapter {
	t.Helper()
	if len(regions) == 0 {
		regions = []string{"eu-central-1"}
	}
	a, err := NewLightsail(f.config(nil, regions))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var lsRef = wire.CloudInstanceRef{Account: "default/eu-central-1", ID: "Ubuntu-1"}

func TestLightsailInstancesAcrossAccounts(t *testing.T) {
	f := newFake(t, func(c call) (int, string) {
		if c.Op != "GetInstances" || c.Service != "lightsail" {
			t.Errorf("unexpected %s %s", c.Service, c.Op)
		}
		switch c.Key + "/" + c.Region {
		case keyDefault + "/eu-central-1":
			if c.JSON["pageToken"] == nil {
				return 200, mustJSON(map[string]any{"instances": []any{lsInstanceJSON("Ubuntu-1", "running", "203.0.113.40")}, "nextPageToken": "tok-2"})
			}
			if c.JSON["pageToken"] != "tok-2" {
				t.Errorf("pageToken = %v", c.JSON["pageToken"])
			}
			return 200, mustJSON(map[string]any{"instances": []any{lsInstanceJSON("Ubuntu-2", "stopped", "")}})
		case keyOps + "/eu-central-1":
			return 400, lsError("UnrecognizedClientException", "The security token included in the request is invalid: "+tokenOps)
		}
		t.Errorf("unexpected account %s/%s", c.Key, c.Region)
		return 500, ""
	})
	a := newLightsail(t, f)
	list, problems, err := a.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(problems, []string{"ops/eu-central-1"}) || len(list) != 2 || list[0].ID != "Ubuntu-1" || list[1].ID != "Ubuntu-2" {
		t.Fatalf("list %v problems %v", list, problems)
	}
	p := coretest.Provider(a, nil, allSecrets...)
	res, err := p.Instances(ctx, wire.CloudInstancesParams{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", res)
	coretest.NoLeak(t, string(raw), allSecrets...)
	if !res.Partial || !strings.Contains(res.Detail, "ops/eu-central-1") {
		t.Fatalf("list = %s", raw)
	}
}

func TestLightsailInstanceFields(t *testing.T) {
	f := newFake(t, func(c call) (int, string) {
		if c.Op != "GetInstance" || c.JSON["instanceName"] != "Ubuntu-1" {
			t.Errorf("call = %+v", c)
		}
		return 200, mustJSON(map[string]any{"instance": lsInstanceJSON("Ubuntu-1", "running", "203.0.113.40")})
	})
	a := newLightsail(t, f)
	in, err := a.Instance(ctx, lsRef)
	if err != nil {
		t.Fatal(err)
	}
	if in.State != wire.CloudInstanceStateRunning || in.Region != "eu-central-1" || in.Zone != "eu-central-1a" || in.DisplayName != "Ubuntu-1" {
		t.Fatalf("instance = %+v", in)
	}
	if !reflect.DeepEqual(in.PublicIps, []string{"203.0.113.40", "2001:db8:1::5"}) || !reflect.DeepEqual(in.PrivateIps, []string{"172.26.0.10"}) {
		t.Fatalf("addresses %v %v", in.PublicIps, in.PrivateIps)
	}
	if in.Spec == nil || *in.Spec != (wire.CloudSpec{Size: "small_3_0", Cpu: 2, MemoryMb: 2048, DiskGb: 60, OS: "Ubuntu"}) {
		t.Fatalf("spec = %+v", in.Spec)
	}
	if in.ConsoleURL != "https://lightsail.aws.amazon.com/ls/webapp/eu-central-1/instances/Ubuntu-1/connect" || core.HTTPSURL(in.ConsoleURL, 2048) == "" {
		t.Fatalf("console = %q", in.ConsoleURL)
	}
	p := coretest.Provider(a, nil, allSecrets...)
	pin, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: lsRef})
	if err != nil {
		t.Fatal(err)
	}
	coretest.NoLeak(t, string(coretest.Valid(t, "cloud.instance", pin)), allSecrets...)
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: lsRef, Action: wire.CloudActionKindConsole, RequestID: "c1"})
	if err != nil || res.URL != in.ConsoleURL {
		t.Fatalf("console = %+v %v", res, err)
	}
	coretest.Valid(t, "cloud.action", res)
}

func TestLightsailStateMapping(t *testing.T) {
	const (
		start, stop, reboot, force = wire.CloudActionKindStart, wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindForceStop
		sync, list, create, cons   = wire.CloudActionKindSyncIp, wire.CloudActionKindSnapshotList, wire.CloudActionKindSnapshotCreate, wire.CloudActionKindConsole
	)
	type K = []wire.CloudActionKind
	cases := []struct {
		vendor string
		state  wire.CloudInstanceState
		acts   K
	}{
		{"pending", wire.CloudInstanceStateStarting, K{sync, list, cons}},
		{"starting", wire.CloudInstanceStateStarting, K{sync, list, cons}},
		{"running", wire.CloudInstanceStateRunning, K{stop, reboot, force, sync, list, create, cons}},
		{"stopping", wire.CloudInstanceStateStopping, K{force, sync, list, cons}},
		{"shutting-down", wire.CloudInstanceStateStopping, K{force, sync, list, cons}},
		{"stopped", wire.CloudInstanceStateStopped, K{start, sync, list, create, cons}},
		{"terminated", wire.CloudInstanceStateTerminated, K{sync}},
		{"terminating", wire.CloudInstanceStateTerminated, K{sync}},
		{"mystery", wire.CloudInstanceStateUnknown, K{sync, cons}},
	}
	for _, c := range cases {
		t.Run(c.vendor, func(t *testing.T) {
			f := newFake(t, func(call) (int, string) {
				return 200, mustJSON(map[string]any{"instance": lsInstanceJSON("Ubuntu-1", c.vendor, "")})
			})
			in, err := newLightsail(t, f).Instance(ctx, lsRef)
			if err != nil {
				t.Fatal(err)
			}
			if in.State != c.state || !reflect.DeepEqual(in.Actions, c.acts) {
				t.Fatalf("state %s actions %v, want %s %v", in.State, in.Actions, c.state, c.acts)
			}
			coretest.Valid(t, "cloud.instance", in)
		})
	}
}

func TestLightsailPowerActions(t *testing.T) {
	ops := mustJSON(map[string]any{"operations": []any{map[string]any{"id": "op-1", "status": "Started", "operationType": "x", "isTerminal": false}}})
	cases := []struct {
		kind  wire.CloudActionKind
		op    string
		force any
	}{
		{wire.CloudActionKindStart, "StartInstance", nil},
		{wire.CloudActionKindStop, "StopInstance", nil},
		{wire.CloudActionKindForceStop, "StopInstance", true},
		{wire.CloudActionKindReboot, "RebootInstance", nil},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			f := newFake(t, func(call) (int, string) { return 200, ops })
			p := coretest.Provider(newLightsail(t, f), nil, allSecrets...)
			params := wire.CloudActionParams{Instance: lsRef, Action: c.kind, RequestID: "req-" + wire.CloudRequestID(c.kind)}
			res, err := p.Action(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			coretest.Valid(t, "cloud.action", res)
			if res.Status != wire.CloudActionStatusAccepted {
				t.Fatalf("status = %s", res.Status)
			}
			got := f.last(c.op)
			if got.JSON["instanceName"] != "Ubuntu-1" || got.JSON["force"] != c.force || got.Region != "eu-central-1" {
				t.Fatalf("call = %+v", got)
			}
			if _, err := p.Action(ctx, params); err != nil {
				t.Fatal(err)
			}
			if n := f.count(c.op); n != 1 {
				t.Fatalf("%s called %d times", c.op, n)
			}
		})
	}
	// An operation that failed at once.
	f := newFake(t, func(call) (int, string) {
		return 200, mustJSON(map[string]any{"operations": []any{map[string]any{"status": "Failed", "errorCode": "InternalError", "errorDetails": "could not start"}}})
	})
	if _, err := newLightsail(t, f).Action(ctx, core.Action{Ref: lsRef, Kind: wire.CloudActionKindStart, RequestID: "r"}); coretest.Code(err) != wire.CloudErrorCodeFailed {
		t.Fatalf("failed operation: %v", err)
	}
	// Unsupported kinds never reach Lightsail.
	g := newFake(t, func(c call) (int, string) {
		t.Errorf("Lightsail asked %s", c.Op)
		return 500, ""
	})
	for _, k := range []wire.CloudActionKind{wire.CloudActionKindHardReset, wire.CloudActionKindCustom} {
		if _, err := newLightsail(t, g).Action(ctx, core.Action{Ref: lsRef, Kind: k, RequestID: "r"}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("%s: %v", k, err)
		}
	}
}

func TestLightsailErrors(t *testing.T) {
	cases := []struct {
		status    int
		typ, code string
		msg       string
		want      wire.CloudErrorCode
	}{
		{400, "NotFoundException", "", "The Instance does not exist", wire.CloudErrorCodeNotFound},
		{400, "InvalidInputException", "DoesNotExist", "Requested resource not found", wire.CloudErrorCodeNotFound},
		{400, "AccessDeniedException", "", "not authorized", wire.CloudErrorCodeNotSignedIn},
		{401, "UnauthenticatedException", "", "no auth", wire.CloudErrorCodeNotSignedIn},
		{400, "UnrecognizedClientException", "", "The security token included in the request is invalid", wire.CloudErrorCodeNotSignedIn},
		{403, "InvalidSignatureException", "", "The request signature we calculated does not match", wire.CloudErrorCodeNotSignedIn},
		{400, "ExpiredTokenException", "", "The security token included in the request is expired", wire.CloudErrorCodeNotSignedIn},
		{400, "OperationFailureException", "", "Sorry, you can't do that while the instance is in the stopping state", wire.CloudErrorCodeInvalidState},
		{400, "InvalidInputException", "", "The instance is not running", wire.CloudErrorCodeInvalidState},
		{400, "ThrottlingException", "", "Rate exceeded", wire.CloudErrorCodeRateLimited},
		{500, "ServiceException", "", "There is not enough capacity to fulfill your request", wire.CloudErrorCodeCapacity},
		{500, "ServiceException", "", "An internal error occurred", wire.CloudErrorCodeFailed},
		{400, "OperationFailureException", "", "Something else", wire.CloudErrorCodeFailed},
		{400, "AccountSetupInProgressException", "", "setting up", wire.CloudErrorCodeFailed},
	}
	for _, c := range cases {
		t.Run(c.typ+"/"+c.msg, func(t *testing.T) {
			f := newFake(t, func(call) (int, string) {
				body := map[string]string{"__type": c.typ, "message": c.msg + " (" + keyDefault + " " + secretDefault + ")"}
				if c.code != "" {
					body["code"] = c.code
				}
				return c.status, mustJSON(body)
			})
			a := newLightsail(t, f)
			_, err := a.Instance(ctx, lsRef)
			if coretest.Code(err) != c.want {
				t.Fatalf("code = %s (%v)", coretest.Code(err), err)
			}
			coretest.NoLeak(t, err.Error(), allSecrets...)
			_, perr := coretest.Provider(a, nil).Action(ctx, wire.CloudActionParams{Instance: lsRef, Action: wire.CloudActionKindStop, RequestID: "r"})
			if coretest.Code(perr) != c.want {
				t.Fatalf("provider code = %s", coretest.Code(perr))
			}
			coretest.NoLeak(t, perr.Error(), allSecrets...)
		})
	}
	// A namespaced type, and an answer without one.
	f := newFake(t, func(call) (int, string) { return 400, `{"message":"gone"}` })
	g := newFake(t, func(call) (int, string) {
		return 400, `{"__type":"com.amazonaws.lightsail#NotFoundException","message":"gone"}`
	})
	if _, err := newLightsail(t, g).Instance(ctx, lsRef); coretest.Code(err) != wire.CloudErrorCodeNotFound {
		t.Fatalf("namespaced type: %v", err)
	}
	if _, err := newLightsail(t, f).Instance(ctx, lsRef); coretest.Code(err) != wire.CloudErrorCodeFailed {
		t.Fatalf("no type: %v", err)
	}
	// Accounts and names that are not set up are not found without asking.
	h := newFake(t, func(c call) (int, string) {
		t.Errorf("Lightsail asked %s", c.Op)
		return 500, ""
	})
	for _, ref := range []wire.CloudInstanceRef{{Account: "default/us-east-1", ID: "Ubuntu-1"}, {Account: "x/eu-central-1", ID: "Ubuntu-1"}, {Account: "default/eu-central-1", ID: "a/b"}} {
		if _, err := newLightsail(t, h).Instance(ctx, ref); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("%v: %v", ref, err)
		}
	}
}

// After a start Lightsail may give the instance a new public address: Instance and sync_ip read it now.
func TestLightsailAddressAfterStart(t *testing.T) {
	var mu sync.Mutex
	state, ip := "stopped", ""
	f := newFake(t, func(c call) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		switch c.Op {
		case "GetInstance":
			return 200, mustJSON(map[string]any{"instance": lsInstanceJSON("Ubuntu-1", state, ip)})
		case "StartInstance":
			state, ip = "running", "198.51.100.140"
			return 200, `{"operations":[{"status":"Started"}]}`
		}
		return 500, ""
	})
	p := coretest.Provider(newLightsail(t, f), nil, allSecrets...)
	before, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: lsRef})
	if err != nil || len(before.PublicIps) != 1 || before.PublicIps[0] != "2001:db8:1::5" {
		t.Fatalf("before = %v %v", before.PublicIps, err)
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: lsRef, Action: wire.CloudActionKindStart, RequestID: "s1"}); err != nil {
		t.Fatal(err)
	}
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: lsRef, Action: wire.CloudActionKindSyncIp, RequestID: "sync"})
	if err != nil || res.Instance == nil || res.Instance.PublicIps[0] != "198.51.100.140" || res.Instance.State != wire.CloudInstanceStateRunning {
		t.Fatalf("sync_ip = %+v %v", res.Instance, err)
	}
	coretest.Valid(t, "cloud.action", res)
}

func lsSnap(name, from, state string, created float64) map[string]any {
	return map[string]any{"name": name, "arn": "arn:aws:lightsail:eu-central-1:000000000000:InstanceSnapshot/0", "createdAt": created,
		"state": state, "fromInstanceName": from, "sizeInGb": 60, "resourceType": "InstanceSnapshot"}
}

func TestLightsailSnapshots(t *testing.T) {
	f := newFake(t, func(c call) (int, string) {
		switch c.Op {
		case "GetInstance":
			return 200, mustJSON(map[string]any{"instance": lsInstanceJSON("Ubuntu-1", "running", "203.0.113.40")})
		case "GetInstanceSnapshots":
			if c.JSON["pageToken"] == nil {
				return 200, mustJSON(map[string]any{"instanceSnapshots": []any{lsSnap("snap-a", "Ubuntu-1", "available", 1.7600e9), lsSnap("other", "Ubuntu-9", "available", 1.7610e9)}, "nextPageToken": "p2"})
			}
			return 200, mustJSON(map[string]any{"instanceSnapshots": []any{lsSnap("snap-b", "Ubuntu-1", "pending", 1.7620e9), lsSnap("snap-c", "Ubuntu-1", "error", 1.7590e9)}})
		}
		return 500, ""
	})
	p := coretest.Provider(newLightsail(t, f), nil, allSecrets...)
	res, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: lsRef})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.snapshots", res)
	var got []string
	for _, s := range res.Snapshots {
		got = append(got, fmt.Sprintf("%s %s %d", s.ID, s.State, s.CreatedAt.Unix()))
	}
	want := []string{"snap-b creating 1762000000", "snap-a ready 1760000000", "snap-c failed 1759000000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshots = %v", got)
	}
}

func TestLightsailSnapshotsManyPages(t *testing.T) {
	// The region has many snapshots of other instances; this one's are on page 15 and beyond.
	var mu sync.Mutex
	pages := 0
	f := newFake(t, func(c call) (int, string) {
		switch c.Op {
		case "GetInstance":
			return 200, mustJSON(map[string]any{"instance": lsInstanceJSON("Ubuntu-1", "running", "203.0.113.40")})
		case "GetInstanceSnapshots":
			mu.Lock()
			defer mu.Unlock()
			pages++
			if pages < 15 {
				return 200, mustJSON(map[string]any{"instanceSnapshots": []any{lsSnap(fmt.Sprintf("other-%d", pages), "Ubuntu-9", "available", 1.7600e9)}, "nextPageToken": fmt.Sprintf("p%d", pages+1)})
			}
			// From here on, every page holds 150 of this instance's snapshots and never ends: 200 are enough.
			var snaps []any
			for i := 0; i < 150; i++ {
				snaps = append(snaps, lsSnap(fmt.Sprintf("snap-%d-%d", pages, i), "Ubuntu-1", "available", 1.7600e9+float64(i)))
			}
			return 200, mustJSON(map[string]any{"instanceSnapshots": snaps, "nextPageToken": fmt.Sprintf("p%d", pages+1)})
		}
		return 500, ""
	})
	p := coretest.Provider(newLightsail(t, f), nil, allSecrets...)
	res, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: lsRef})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.snapshots", res)
	if len(res.Snapshots) != core.MaxSnapshots || f.count("GetInstanceSnapshots") != 16 {
		t.Fatalf("%d snapshots from %d pages", len(res.Snapshots), f.count("GetInstanceSnapshots"))
	}
}

func TestLightsailSnapshotCreate(t *testing.T) {
	var mu sync.Mutex
	made := map[string]bool{}
	f := newFake(t, func(c call) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		switch c.Op {
		case "CreateInstanceSnapshot":
			name, _ := c.JSON["instanceSnapshotName"].(string)
			if made[name] {
				return 400, lsError("InvalidInputException", "The instance snapshot name "+name+" is already in use")
			}
			made[name] = true
			return 200, `{"operations":[{"status":"Started","operationType":"CreateInstanceSnapshot"}]}`
		case "GetInstanceSnapshot":
			name, _ := c.JSON["instanceSnapshotName"].(string)
			if !made[name] {
				return 400, lsError("NotFoundException", "no such snapshot")
			}
			return 200, mustJSON(map[string]any{"instanceSnapshot": lsSnap(name, "Ubuntu-1", "pending", 1.7642e9)})
		}
		return 500, ""
	})
	p := coretest.Provider(newLightsail(t, f), nil, allSecrets...)
	params := wire.CloudActionParams{Instance: lsRef, Action: wire.CloudActionKindSnapshotCreate, Name: "before upgrade!", RequestID: "req-snap-1"}
	res, err := p.Action(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	c := f.last("CreateInstanceSnapshot")
	name, _ := c.JSON["instanceSnapshotName"].(string)
	if c.JSON["instanceName"] != "Ubuntu-1" || !strings.HasPrefix(name, "before-upgrade-") || len(name) != len("before-upgrade-")+8 {
		t.Fatalf("CreateInstanceSnapshot = %+v", c.JSON)
	}
	if res.Status != wire.CloudActionStatusAccepted || res.Snapshot == nil || string(res.Snapshot.ID) != name || res.Snapshot.State != wire.CloudSnapshotStateCreating {
		t.Fatalf("result = %+v", res)
	}
	// Through the provider again: from its record.
	if _, err := p.Action(ctx, params); err != nil {
		t.Fatal(err)
	}
	if n := f.count("CreateInstanceSnapshot"); n != 1 {
		t.Fatalf("CreateInstanceSnapshot called %d times", n)
	}
	// After a restart the same request makes the same name, which Lightsail already has: that snapshot is answered.
	again, err := coretest.Provider(newLightsail(t, f), nil).Action(ctx, params)
	if err != nil || again.Snapshot == nil || string(again.Snapshot.ID) != name {
		t.Fatalf("retry = %+v %v", again, err)
	}
	// Another request without a name: named after the instance.
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: lsRef, Action: wire.CloudActionKindSnapshotCreate, RequestID: "req-snap-2"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.last("CreateInstanceSnapshot").JSON["instanceSnapshotName"].(string); !strings.HasPrefix(got, "Ubuntu-1-") || got == name {
		t.Fatalf("default name = %q", got)
	}
}

func TestSnapshotName(t *testing.T) {
	for _, c := range []struct{ base, inst, prefix string }{
		{"", "Ubuntu-1", "Ubuntu-1-"},
		{"--x y--", "i", "x-y-"},
		{"\u00e9\u00e9", "", "snapshot-"}, // only non-ASCII letters: nothing is left
		{strings.Repeat("a", 300), "", strings.Repeat("a", 200) + "-"},
	} {
		got := snapshotName(c.base, c.inst, "rid")
		if !strings.HasPrefix(got, c.prefix) || len(got) != len(c.prefix)+8 {
			t.Fatalf("snapshotName(%q, %q) = %q", c.base, c.inst, got)
		}
	}
	if snapshotName("a", "", "r1") == snapshotName("a", "", "r2") || snapshotName("a", "", "r1") != snapshotName("a", "", "r1") {
		t.Fatal("the name is not per request")
	}
}
