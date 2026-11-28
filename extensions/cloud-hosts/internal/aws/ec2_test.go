package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

// inst is one synthetic EC2 instance.
type inst struct {
	id, state, name     string
	public, private, v6 string
	spot                bool
	reasonCode, reason  string
	transition          string
	instanceStore       bool
}

func (i inst) xml() string {
	var b strings.Builder
	fmt.Fprintf(&b, `<item><instanceId>%s</instanceId><imageId>ami-0example</imageId>`, i.id)
	fmt.Fprintf(&b, `<instanceState><code>16</code><name>%s</name></instanceState>`, i.state)
	fmt.Fprintf(&b, `<privateDnsName>ip-10-0-0-5.example.test</privateDnsName><dnsName/><reason>%s</reason>`, i.transition)
	b.WriteString(`<instanceType>t3.small</instanceType><placement><availabilityZone>us-east-1b</availabilityZone><tenancy>default</tenancy></placement>`)
	if i.private != "" {
		fmt.Fprintf(&b, `<privateIpAddress>%s</privateIpAddress>`, i.private)
	}
	if i.public != "" {
		fmt.Fprintf(&b, `<ipAddress>%s</ipAddress>`, i.public)
	}
	if i.instanceStore {
		b.WriteString(`<rootDeviceType>instance-store</rootDeviceType><rootDeviceName>/dev/sda1</rootDeviceName><blockDeviceMapping/>`)
	} else {
		b.WriteString(`<rootDeviceType>ebs</rootDeviceType><rootDeviceName>/dev/xvda</rootDeviceName>`)
		fmt.Fprintf(&b, `<blockDeviceMapping><item><deviceName>/dev/xvdf</deviceName><ebs><volumeId>vol-0data%s</volumeId><status>attached</status></ebs></item>`, i.id[2:])
		fmt.Fprintf(&b, `<item><deviceName>/dev/xvda</deviceName><ebs><volumeId>vol-0root%s</volumeId><status>attached</status><deleteOnTermination>true</deleteOnTermination></ebs></item></blockDeviceMapping>`, i.id[2:])
	}
	if i.spot {
		b.WriteString(`<instanceLifecycle>spot</instanceLifecycle><spotInstanceRequestId>sir-0example</spotInstanceRequestId>`)
	}
	b.WriteString(`<architecture>arm64</architecture>`)
	if i.reasonCode != "" {
		fmt.Fprintf(&b, `<stateReason><code>%s</code><message>%s</message></stateReason>`, i.reasonCode, i.reason)
	}
	if i.name != "" {
		fmt.Fprintf(&b, `<tagSet><item><key>env</key><value>test</value></item><item><key>Name</key><value>%s</value></item></tagSet>`, i.name)
	}
	b.WriteString(`<networkInterfaceSet>`)
	if i.private != "" {
		// A secondary interface listed first, to check the primary's addresses lead.
		b.WriteString(`<item><networkInterfaceId>eni-0second</networkInterfaceId><attachment><deviceIndex>1</deviceIndex></attachment><privateIpAddress>10.0.1.9</privateIpAddress><privateIpAddressesSet><item><privateIpAddress>10.0.1.9</privateIpAddress><primary>true</primary></item></privateIpAddressesSet></item>`)
		fmt.Fprintf(&b, `<item><networkInterfaceId>eni-0first</networkInterfaceId><attachment><deviceIndex>0</deviceIndex></attachment><privateIpAddress>%s</privateIpAddress>`, i.private)
		if i.public != "" {
			fmt.Fprintf(&b, `<association><publicIp>%s</publicIp></association>`, i.public)
		}
		fmt.Fprintf(&b, `<privateIpAddressesSet><item><privateIpAddress>%s</privateIpAddress><primary>true</primary></item><item><privateIpAddress>10.0.0.77</privateIpAddress><primary>false</primary><association><publicIp>198.51.100.77</publicIp></association></item></privateIpAddressesSet>`, i.private)
		if i.v6 != "" {
			fmt.Fprintf(&b, `<ipv6AddressesSet><item><ipv6Address>%s</ipv6Address></item></ipv6AddressesSet>`, i.v6)
		}
		b.WriteString(`</item>`)
	}
	b.WriteString(`</networkInterfaceSet>`)
	b.WriteString(`<cpuOptions><coreCount>1</coreCount><threadsPerCore>2</threadsPerCore></cpuOptions><platformDetails>Linux/UNIX</platformDetails>`)
	b.WriteString(`</item>`)
	return b.String()
}

func describeXML(next string, items ...inst) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>00000000-0000-0000-0000-000000000001</requestId><reservationSet>`)
	for _, i := range items {
		b.WriteString(`<item><reservationId>r-0example</reservationId><groupSet/><instancesSet>` + i.xml() + `</instancesSet></item>`)
	}
	b.WriteString(`</reservationSet>`)
	if next != "" {
		b.WriteString(`<nextToken>` + next + `</nextToken>`)
	}
	b.WriteString(`</DescribeInstancesResponse>`)
	return b.String()
}

func ec2Error(code, msg string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<Response><Errors><Error><Code>` + code + `</Code><Message>` + msg + `</Message></Error></Errors><RequestID>00000000-0000-0000-0000-000000000002</RequestID></Response>`
}

func stateChangeXML(op, id, prev, cur string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><%sResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>r1</requestId><instancesSet><item><instanceId>%s</instanceId><currentState><code>0</code><name>%s</name></currentState><previousState><code>80</code><name>%s</name></previousState></item></instancesSet></%sResponse>`, op, id, cur, prev, op)
}

func newEC2(t *testing.T, f *fakeAWS, regions ...string) core.Adapter {
	t.Helper()
	a, err := NewEC2(f.config(regions, nil))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var ctx = context.Background()

var running = inst{id: "i-0123456789abcdef0", state: "running", name: "web", public: "203.0.113.10", private: "10.0.0.5", v6: "2001:db8::10"}

func TestEC2InstancesAcrossAccounts(t *testing.T) {
	f := newFake(t, func(c call) (int, string) {
		if c.Op != "DescribeInstances" {
			t.Errorf("unexpected %s", c.Op)
		}
		switch c.Key + "/" + c.Region {
		case keyDefault + "/us-east-1":
			if c.Form.Get("MaxResults") != "200" {
				t.Errorf("MaxResults = %q", c.Form.Get("MaxResults"))
			}
			if c.Form.Get("NextToken") == "" {
				return 200, describeXML("page-2", running)
			}
			if c.Form.Get("NextToken") != "page-2" {
				t.Errorf("NextToken = %q", c.Form.Get("NextToken"))
			}
			return 200, describeXML("", inst{id: "i-0aaaaaaaaaaaaaaa1", state: "stopped", private: "10.0.0.6"})
		case keyDefault + "/eu-west-1":
			return 200, describeXML("")
		case keyOps + "/us-east-1":
			return 401, ec2Error("AuthFailure", "AWS was not able to validate the provided access credentials "+keyOps+" "+secretOps)
		case keyOps + "/eu-west-1":
			return 200, describeXML("", inst{id: "i-0bbbbbbbbbbbbbbb2", state: "running", public: "198.51.100.20", private: "172.16.0.4"})
		}
		t.Errorf("unexpected account %s/%s", c.Key, c.Region)
		return 500, ""
	})
	a := newEC2(t, f, "us-east-1", "eu-west-1", "us-east-1")
	list, problems, err := a.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(problems, []string{"ops/us-east-1"}) {
		t.Fatalf("problems = %v", problems)
	}
	var got []string
	for _, in := range list {
		got = append(got, string(in.Account)+" "+string(in.ID))
	}
	want := []string{"default/us-east-1 i-0123456789abcdef0", "default/us-east-1 i-0aaaaaaaaaaaaaaa1", "ops/eu-west-1 i-0bbbbbbbbbbbbbbb2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("instances = %v", got)
	}

	p := coretest.Provider(a, nil, allSecrets...)
	res, err := p.Instances(ctx, wire.CloudInstancesParams{Locale: "en", Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := coretest.Valid(t, "cloud.instances", res)
	coretest.NoLeak(t, string(raw), allSecrets...)
	if !res.Partial || !strings.Contains(res.Detail, "ops/us-east-1") || len(res.Instances) != 3 {
		t.Fatalf("list = %s", raw)
	}
}

func TestEC2AllAccountsFailing(t *testing.T) {
	f := newFake(t, func(c call) (int, string) {
		return 403, ec2Error("UnauthorizedOperation", "You are not authorized to perform this operation. "+keyDefault)
	})
	a := newEC2(t, f)
	_, _, err := a.Instances(ctx)
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("err = %v", err)
	}
	coretest.NoLeak(t, err.Error(), allSecrets...)
	_, perr := coretest.Provider(a, nil).Instances(ctx, wire.CloudInstancesParams{Locale: "en"})
	if coretest.Code(perr) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("provider err = %v", perr)
	}
	coretest.NoLeak(t, perr.Error(), allSecrets...)
}

func TestEC2InstanceFields(t *testing.T) {
	f := newFake(t, func(c call) (int, string) {
		if c.Form.Get("InstanceId.1") != running.id {
			t.Errorf("InstanceId.1 = %q", c.Form.Get("InstanceId.1"))
		}
		return 200, describeXML("", running)
	})
	a := newEC2(t, f)
	in, err := a.Instance(ctx, wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)})
	if err != nil {
		t.Fatal(err)
	}
	if in.DisplayName != "web" || in.Region != "us-east-1" || in.Zone != "us-east-1b" || !in.ObservedAt.Equal(testNow) {
		t.Fatalf("instance = %+v", in)
	}
	if !reflect.DeepEqual(in.PublicIps, []string{"203.0.113.10", "198.51.100.77", "2001:db8::10"}) {
		t.Fatalf("public = %v", in.PublicIps)
	}
	if !reflect.DeepEqual(in.PrivateIps, []string{"10.0.0.5", "10.0.0.77", "10.0.1.9"}) {
		t.Fatalf("private = %v", in.PrivateIps)
	}
	if in.Spec == nil || *in.Spec != (wire.CloudSpec{Size: "t3.small", Cpu: 2, Arch: "arm64", OS: "Linux/UNIX"}) {
		t.Fatalf("spec = %+v", in.Spec)
	}
	if in.Lifecycle != nil {
		t.Fatalf("lifecycle = %+v", in.Lifecycle)
	}
	if in.ConsoleURL != "https://us-east-1.console.aws.amazon.com/ec2/home?region=us-east-1#InstanceDetails:instanceId=i-0123456789abcdef0" {
		t.Fatalf("console = %q", in.ConsoleURL)
	}
	if core.HTTPSURL(in.ConsoleURL, 2048) == "" {
		t.Fatal("the console link is not https without credentials")
	}

	p := coretest.Provider(a, nil, allSecrets...)
	pin, err := p.Instance(ctx, wire.CloudInstanceParams{Locale: "en", Instance: wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)}})
	if err != nil {
		t.Fatal(err)
	}
	coretest.NoLeak(t, string(coretest.Valid(t, "cloud.instance", pin)), allSecrets...)
	res, err := p.Action(ctx, wire.CloudActionParams{Locale: "en", Instance: wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)},
		Action: wire.CloudActionKindConsole, RequestID: "req-console"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.URL != in.ConsoleURL || res.Status != wire.CloudActionStatusDone {
		t.Fatalf("console = %+v", res)
	}
}

func TestEC2StateMapping(t *testing.T) {
	const (
		start, stop, reboot, force = wire.CloudActionKindStart, wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindForceStop
		sync, list, create, cons   = wire.CloudActionKindSyncIp, wire.CloudActionKindSnapshotList, wire.CloudActionKindSnapshotCreate, wire.CloudActionKindConsole
	)
	type K = []wire.CloudActionKind
	cases := []struct {
		name  string
		in    inst
		state wire.CloudInstanceState
		acts  K
		spot  bool
	}{
		{"pending", inst{state: "pending"}, wire.CloudInstanceStateStarting, K{sync, list, cons}, false},
		{"running", inst{state: "running"}, wire.CloudInstanceStateRunning, K{stop, reboot, force, sync, list, create, cons}, false},
		{"running instance store", inst{state: "running", instanceStore: true}, wire.CloudInstanceStateRunning, K{reboot, sync, cons}, false},
		{"stopping", inst{state: "stopping"}, wire.CloudInstanceStateStopping, K{force, sync, list, cons}, false},
		{"shutting-down", inst{state: "shutting-down"}, wire.CloudInstanceStateStopping, K{sync, cons}, false},
		{"stopped", inst{state: "stopped", reasonCode: "Client.UserInitiatedShutdown", reason: "Client.UserInitiatedShutdown: User initiated shutdown"}, wire.CloudInstanceStateStopped, K{start, sync, list, create, cons}, false},
		{"terminated", inst{state: "terminated"}, wire.CloudInstanceStateTerminated, K{sync, cons}, false},
		{"spot running", inst{state: "running", spot: true}, wire.CloudInstanceStateRunning, K{stop, reboot, force, sync, list, create, cons}, true},
		{"spot stopped by the user", inst{state: "stopped", spot: true, reasonCode: "Client.UserInitiatedShutdown", reason: "Client.UserInitiatedShutdown: User initiated shutdown"}, wire.CloudInstanceStateStopped, K{start, sync, list, create, cons}, true},
		{"spot stopped by an interruption", inst{state: "stopped", spot: true, reasonCode: "Server.SpotInstanceShutdown", reason: "Server.SpotInstanceShutdown: Spot instance shutdown"}, wire.CloudInstanceStatePreempted, K{start, sync, list, create, cons}, true},
		{"spot interruption by wording", inst{state: "stopped", spot: true, transition: "Service initiated (Spot interruption)"}, wire.CloudInstanceStatePreempted, K{start, sync, list, create, cons}, true},
		{"spot terminated by an interruption", inst{state: "terminated", spot: true, reasonCode: "Server.SpotInstanceTermination", reason: "Server.SpotInstanceTermination: Spot instance termination"}, wire.CloudInstanceStateTerminated, K{sync, cons}, true},
		{"unreadable", inst{state: "mystery"}, wire.CloudInstanceStateUnknown, K{sync, cons}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.in.id = "i-0cccccccccccccccc"
			f := newFake(t, func(call) (int, string) { return 200, describeXML("", c.in) })
			a := newEC2(t, f)
			in, err := a.Instance(ctx, wire.CloudInstanceRef{Account: "default/us-east-1", ID: "i-0cccccccccccccccc"})
			if err != nil {
				t.Fatal(err)
			}
			if in.State != c.state || !reflect.DeepEqual(in.Actions, c.acts) {
				t.Fatalf("state %s actions %v, want %s %v", in.State, in.Actions, c.state, c.acts)
			}
			if (in.Lifecycle != nil && in.Lifecycle.Spot) != c.spot {
				t.Fatalf("lifecycle = %+v", in.Lifecycle)
			}
			if c.state == wire.CloudInstanceStatePreempted && in.StateDetail == "" && c.in.reason != "" {
				t.Fatal("no state detail for a preemption")
			}
			if c.in.state == "terminated" && c.spot && in.StateDetail != "AWS took this spot instance back and terminated it. It cannot be started again." {
				t.Fatalf("spot termination detail = %q", in.StateDetail)
			}
			coretest.Valid(t, "cloud.instance", in)
		})
	}
}

func TestEC2PowerActions(t *testing.T) {
	ref := wire.CloudInstanceRef{Account: "ops/eu-west-1", ID: wire.CloudInstanceID(running.id)}
	cases := []struct {
		kind       wire.CloudActionKind
		op, force  string
		answer     string
		wantStatus wire.CloudActionStatus
	}{
		{wire.CloudActionKindStart, "StartInstances", "", stateChangeXML("StartInstances", running.id, "stopped", "pending"), wire.CloudActionStatusAccepted},
		{wire.CloudActionKindStop, "StopInstances", "", stateChangeXML("StopInstances", running.id, "running", "stopping"), wire.CloudActionStatusAccepted},
		{wire.CloudActionKindStop, "StopInstances", "", stateChangeXML("StopInstances", running.id, "stopped", "stopped"), wire.CloudActionStatusDone},
		{wire.CloudActionKindForceStop, "StopInstances", "true", stateChangeXML("StopInstances", running.id, "stopping", "stopping"), wire.CloudActionStatusAccepted},
		{wire.CloudActionKindReboot, "RebootInstances", "", `<RebootInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>r</requestId><return>true</return></RebootInstancesResponse>`, wire.CloudActionStatusAccepted},
	}
	for _, c := range cases {
		t.Run(string(c.kind)+"-"+string(c.wantStatus), func(t *testing.T) {
			f := newFake(t, func(call) (int, string) { return 200, c.answer })
			a := newEC2(t, f, "eu-west-1")
			p := coretest.Provider(a, nil, allSecrets...)
			params := wire.CloudActionParams{Locale: "en", Instance: ref, Action: c.kind, RequestID: "req-" + wire.CloudRequestID(c.kind)}
			res, err := p.Action(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			coretest.NoLeak(t, string(coretest.Valid(t, "cloud.action", res)), allSecrets...)
			if res.Status != c.wantStatus {
				t.Fatalf("status = %s", res.Status)
			}
			got := f.last(c.op)
			if got.Region != "eu-west-1" || got.Key != keyOps || got.Form.Get("InstanceId.1") != running.id || got.Form.Get("Force") != c.force {
				t.Fatalf("call = %+v", got)
			}
			// The same request id again: answered from the record, EC2 is not asked twice.
			if _, err := p.Action(ctx, params); err != nil {
				t.Fatal(err)
			}
			if n := f.count(c.op); n != 1 {
				t.Fatalf("%s called %d times", c.op, n)
			}
		})
	}
}

func TestEC2UnsupportedActions(t *testing.T) {
	f := newFake(t, func(c call) (int, string) {
		t.Errorf("EC2 asked %s", c.Op)
		return 500, ""
	})
	a := newEC2(t, f)
	ref := wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)}
	for _, k := range []wire.CloudActionKind{wire.CloudActionKindHardReset, wire.CloudActionKindCustom} {
		_, err := a.Action(ctx, core.Action{Ref: ref, Kind: k, Custom: "renew", RequestID: "r"})
		if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("%s: %v", k, err)
		}
	}
	p := coretest.Provider(a, nil)
	_, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindHardReset, RequestID: "r1"})
	if coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("provider hard_reset: %v", err)
	}
}

func TestEC2Errors(t *testing.T) {
	cases := []struct {
		status int
		code   string
		want   wire.CloudErrorCode
	}{
		{400, "InvalidInstanceID.NotFound", wire.CloudErrorCodeNotFound},
		{400, "InvalidInstanceID.Malformed", wire.CloudErrorCodeNotFound},
		{401, "AuthFailure", wire.CloudErrorCodeNotSignedIn},
		{403, "UnauthorizedOperation", wire.CloudErrorCodeNotSignedIn},
		{401, "InvalidClientTokenId", wire.CloudErrorCodeNotSignedIn},
		{403, "SignatureDoesNotMatch", wire.CloudErrorCodeNotSignedIn},
		{400, "ExpiredToken", wire.CloudErrorCodeNotSignedIn},
		{400, "RequestExpired", wire.CloudErrorCodeNotSignedIn},
		{401, "OptInRequired", wire.CloudErrorCodeNotSignedIn},
		{400, "IncorrectInstanceState", wire.CloudErrorCodeInvalidState},
		{400, "IncorrectState", wire.CloudErrorCodeInvalidState},
		{400, "IncorrectSpotRequestState", wire.CloudErrorCodeInvalidState},
		{503, "RequestLimitExceeded", wire.CloudErrorCodeRateLimited},
		{400, "ThrottlingException", wire.CloudErrorCodeRateLimited},
		{500, "InsufficientInstanceCapacity", wire.CloudErrorCodeCapacity},
		{400, "SpotMaxPriceTooLow", wire.CloudErrorCodeCapacity},
		{400, "MaxSpotInstanceCountExceeded", wire.CloudErrorCodeCapacity},
		{400, "InstanceLimitExceeded", wire.CloudErrorCodeCapacity},
		{400, "InsufficientFreeAddressesInSubnet", wire.CloudErrorCodeCapacity},
		{400, "VcpuLimitExceeded", wire.CloudErrorCodeCapacity},
		{400, "Unsupported", wire.CloudErrorCodeCapacity},
		{400, "OperationNotPermitted", wire.CloudErrorCodeRefused},
		{400, "UnsupportedOperation", wire.CloudErrorCodeRefused},
		{400, "InvalidParameterValue", wire.CloudErrorCodeFailed},
		{503, "Unavailable", wire.CloudErrorCodeFailed},
	}
	ref := wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			f := newFake(t, func(call) (int, string) {
				return c.status, ec2Error(c.code, "Request for "+keyDefault+" with "+secretDefault+" failed.")
			})
			a := newEC2(t, f)
			_, err := a.Action(ctx, core.Action{Ref: ref, Kind: wire.CloudActionKindStart, RequestID: "r"})
			if coretest.Code(err) != c.want {
				t.Fatalf("code = %s (%v)", coretest.Code(err), err)
			}
			coretest.NoLeak(t, err.Error(), allSecrets...)
			if !strings.Contains(err.Error(), c.code) {
				t.Fatalf("message %q does not name the vendor's code", err)
			}
			p := coretest.Provider(a, nil)
			_, perr := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: "r2"})
			if coretest.Code(perr) != c.want {
				t.Fatalf("provider code = %s", coretest.Code(perr))
			}
			coretest.NoLeak(t, perr.Error(), allSecrets...)
		})
	}
	// An answer that is not EC2's error document falls back on the status.
	f := newFake(t, func(call) (int, string) { return 403, "<html>denied " + secretDefault + "</html>" })
	_, err := newEC2(t, f).Instance(ctx, ref)
	if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
		t.Fatalf("403 without a document: %v", err)
	}
	coretest.NoLeak(t, err.Error(), allSecrets...)
}

func TestEC2NotFound(t *testing.T) {
	f := newFake(t, func(call) (int, string) { return 200, describeXML("") })
	a := newEC2(t, f)
	for _, ref := range []wire.CloudInstanceRef{
		{Account: "default/us-east-1", ID: "i-0ddddddddddddddd1"}, // EC2 lists nothing
		{Account: "default/eu-west-1", ID: "i-0ddddddddddddddd1"}, // a region not set up
		{Account: "nobody/us-east-1", ID: "i-0ddddddddddddddd1"},  // a profile not set up
		{Account: "default", ID: "i-0ddddddddddddddd1"},
		{Account: "default/us-east-1", ID: "not-an-instance"},
	} {
		if _, err := a.Instance(ctx, ref); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("%v: %v", ref, err)
		}
	}
	if n := f.count("DescribeInstances"); n != 1 {
		t.Fatalf("DescribeInstances called %d times", n)
	}
}

// After a start EC2 gives the instance a new public address: Instance and sync_ip read it now, never a cache.
func TestEC2AddressAfterStart(t *testing.T) {
	var mu sync.Mutex
	cur := running
	f := newFake(t, func(c call) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		switch c.Op {
		case "DescribeInstances":
			return 200, describeXML("", cur)
		case "StartInstances":
			cur.public = "198.51.100.99"
			return 200, stateChangeXML("StartInstances", cur.id, "stopped", "pending")
		}
		return 500, ""
	})
	a := newEC2(t, f)
	p := coretest.Provider(a, nil, allSecrets...)
	ref := wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)}
	before, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil || before.PublicIps[0] != "203.0.113.10" {
		t.Fatalf("before = %v %v", before.PublicIps, err)
	}
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: "start-1"}); err != nil {
		t.Fatal(err)
	}
	after, err := p.Instance(ctx, wire.CloudInstanceParams{Instance: ref})
	if err != nil || after.PublicIps[0] != "198.51.100.99" {
		t.Fatalf("after = %v %v", after.PublicIps, err)
	}
	res, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSyncIp, RequestID: "sync-1"})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.action", res)
	if res.Instance == nil || res.Instance.PublicIps[0] != "198.51.100.99" {
		t.Fatalf("sync_ip = %+v", res)
	}
	if n := f.count("DescribeInstances"); n != 3 {
		t.Fatalf("DescribeInstances called %d times, want 3", n)
	}
}

func snapshotsXML(next string, items ...string) string {
	s := `<?xml version="1.0" encoding="UTF-8"?><DescribeSnapshotsResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>r</requestId><snapshotSet>` + strings.Join(items, "") + `</snapshotSet>`
	if next != "" {
		s += `<nextToken>` + next + `</nextToken>`
	}
	return s + `</DescribeSnapshotsResponse>`
}

func snapXML(id, status, start, name string, requestID ...string) string {
	s := `<item><snapshotId>` + id + `</snapshotId><volumeId>vol-0root0123456789abcdef0</volumeId><status>` + status + `</status><startTime>` + start + `</startTime><progress>100%</progress><volumeSize>8</volumeSize><description>Created by test</description>`
	tags := ""
	if name != "" {
		tags += `<item><key>Name</key><value>` + name + `</value></item>`
	}
	for _, r := range requestID {
		tags += `<item><key>persona-request-id</key><value>` + r + `</value></item>`
	}
	if tags != "" {
		s += `<tagSet>` + tags + `</tagSet>`
	}
	return s + `</item>`
}

func TestEC2Snapshots(t *testing.T) {
	f := newFake(t, func(c call) (int, string) {
		switch c.Op {
		case "DescribeInstances":
			return 200, describeXML("", running)
		case "DescribeSnapshots":
			if c.Form.Get("Owner.1") != "self" || c.Form.Get("Filter.1.Name") != "volume-id" || c.Form.Get("Filter.1.Value.1") != "vol-0root0123456789abcdef0" {
				t.Errorf("DescribeSnapshots form = %v", c.Form)
			}
			if c.Form.Get("NextToken") == "" {
				return 200, snapshotsXML("snap-page-2", snapXML("snap-0aaa", "completed", "2026-11-01T10:00:00.000Z", "before-upgrade"))
			}
			return 200, snapshotsXML("", snapXML("snap-0bbb", "pending", "2026-11-20T10:00:00.000Z", ""), snapXML("snap-0ccc", "error", "2026-10-01T10:00:00.000Z", ""))
		}
		return 500, ""
	})
	a := newEC2(t, f)
	p := coretest.Provider(a, nil, allSecrets...)
	res, err := p.Snapshots(ctx, wire.CloudSnapshotsParams{Instance: wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)}})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.snapshots", res)
	var got []string
	for _, s := range res.Snapshots {
		got = append(got, fmt.Sprintf("%s %s %s %g", s.ID, s.Name, s.State, s.SizeGb))
	}
	want := []string{"snap-0bbb Created by test creating 8", "snap-0aaa before-upgrade ready 8", "snap-0ccc Created by test failed 8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshots = %v", got)
	}

	// An instance-store root has no snapshots.
	g := newFake(t, func(c call) (int, string) {
		return 200, describeXML("", inst{id: running.id, state: "running", instanceStore: true})
	})
	if _, err := newEC2(t, g).Snapshots(ctx, wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
		t.Fatalf("instance store: %v", err)
	}
}

func TestEC2SnapshotCreate(t *testing.T) {
	var mu sync.Mutex
	created := ""
	f := newFake(t, func(c call) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		switch c.Op {
		case "DescribeInstances":
			return 200, describeXML("", running)
		case "DescribeSnapshots":
			if c.Form.Get("Filter.2.Name") != "tag:persona-request-id" || c.Form.Get("Filter.2.Value.1") == "" {
				t.Errorf("idempotency lookup = %v", c.Form)
			}
			// EC2 matches filter values with * and ? as wildcards: a lookup for "snap-req-*" finds snap-req-1's snapshot.
			if v := c.Form.Get("Filter.2.Value.1"); created != "" && (v == "snap-req-1" || strings.ContainsAny(v, "*?")) {
				return 200, snapshotsXML("", snapXML("snap-0new0000000000001", "pending", "2026-11-27T09:30:00.000Z", "nightly", "snap-req-1"))
			}
			return 200, snapshotsXML("")
		case "CreateSnapshot":
			if created == "" {
				created = "snap-0new0000000000001"
			} else {
				created = "snap-0new0000000000002"
			}
			return 200, `<?xml version="1.0" encoding="UTF-8"?><CreateSnapshotResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/"><requestId>r</requestId><snapshotId>` + created + `</snapshotId><volumeId>vol-0root0123456789abcdef0</volumeId><status>pending</status><startTime>2026-11-27T09:30:00.000Z</startTime><progress></progress><volumeSize>8</volumeSize><description>` + c.Form.Get("Description") + `</description><tagSet><item><key>Name</key><value>` + c.Form.Get("TagSpecification.1.Tag.1.Value") + `</value></item></tagSet></CreateSnapshotResponse>`
		}
		return 500, ""
	})
	a := newEC2(t, f)
	p := coretest.Provider(a, nil, allSecrets...)
	ref := wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)}
	params := wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSnapshotCreate, Name: "nightly", RequestID: "snap-req-1"}
	res, err := p.Action(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	coretest.NoLeak(t, string(coretest.Valid(t, "cloud.action", res)), allSecrets...)
	if res.Status != wire.CloudActionStatusAccepted || res.Snapshot == nil || res.Snapshot.ID != "snap-0new0000000000001" ||
		res.Snapshot.Name != "nightly" || res.Snapshot.State != wire.CloudSnapshotStateCreating {
		t.Fatalf("result = %+v %+v", res, res.Snapshot)
	}
	c := f.last("CreateSnapshot")
	if c.Form.Get("VolumeId") != "vol-0root0123456789abcdef0" || c.Form.Get("TagSpecification.1.ResourceType") != "snapshot" ||
		c.Form.Get("TagSpecification.1.Tag.1.Key") != "Name" || c.Form.Get("TagSpecification.1.Tag.1.Value") != "nightly" ||
		c.Form.Get("TagSpecification.1.Tag.2.Key") != "persona-request-id" || c.Form.Get("TagSpecification.1.Tag.2.Value") != "snap-req-1" ||
		!strings.Contains(c.Form.Get("Description"), running.id) {
		t.Fatalf("CreateSnapshot form = %v", c.Form)
	}
	// The same request id through the provider: answered from its record.
	if _, err := p.Action(ctx, params); err != nil {
		t.Fatal(err)
	}
	// The same request id after a restart (a new provider): the tagged snapshot is found, none is taken again.
	again, err := coretest.Provider(newEC2(t, f), nil).Action(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if again.Snapshot == nil || again.Snapshot.ID != "snap-0new0000000000001" {
		t.Fatalf("retry = %+v", again)
	}
	if n := f.count("CreateSnapshot"); n != 1 {
		t.Fatalf("CreateSnapshot called %d times", n)
	}

	// A request id with wildcard characters matches another request's snapshot in EC2's filter: that one is not
	// answered, a new snapshot is taken.
	wild, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSnapshotCreate, Name: "wild", RequestID: "snap-req-*"})
	if err != nil {
		t.Fatal(err)
	}
	if wild.Snapshot == nil || wild.Snapshot.ID != "snap-0new0000000000002" || f.count("CreateSnapshot") != 2 {
		t.Fatalf("wildcard request id answered %+v (CreateSnapshot %d)", wild.Snapshot, f.count("CreateSnapshot"))
	}

	// Without a name the snapshot is named after the instance and the time.
	if _, err := p.Action(ctx, wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindSnapshotCreate, RequestID: "snap-req-2"}); err != nil {
		t.Fatal(err)
	}
	if got := f.last("CreateSnapshot").Form.Get("TagSpecification.1.Tag.1.Value"); got != "web-20261127-093000" {
		t.Fatalf("default name = %q", got)
	}
}

func TestConstructors(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ls   bool
	}{
		{"no credentials", Config{}, false},
		{"no complete profile", Config{Credentials: "[default]\naws_access_key_id = AKIAEXAMPLEEXAMPLE00\n"}, false},
		{"only a secret", Config{Credentials: "[default]\naws_secret_access_key = " + secretDefault + "\n"}, false},
		{"lightsail without regions", Config{Credentials: testCredentials}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var err error
			if c.ls {
				_, err = NewLightsail(c.cfg)
			} else {
				_, err = NewEC2(c.cfg)
			}
			if coretest.Code(err) != wire.CloudErrorCodeNotSignedIn {
				t.Fatalf("err = %v", err)
			}
			coretest.NoLeak(t, err.Error(), allSecrets...)
		})
	}
	if _, err := NewEC2(Config{Credentials: testCredentials, Regions: []string{"us-east-1.evil.test/"}}); err == nil {
		t.Fatal("a region that is not one was taken")
	}
	// Regions of other partitions (amazonaws.com.cn and the isolated regions) cannot be reached: refused at setup.
	for _, r := range []string{"cn-north-1", "cn-northwest-1", "us-iso-east-1", "us-isob-east-1"} {
		if _, err := NewEC2(Config{Credentials: testCredentials, Regions: []string{"us-east-1", r}}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("region %s: %v", r, err)
		}
		if _, err := NewLightsail(Config{Credentials: testCredentials, LightsailRegions: []string{r}}); coretest.Code(err) != wire.CloudErrorCodeUnsupported {
			t.Fatalf("lightsail region %s: %v", r, err)
		}
	}
	if _, err := NewEC2(Config{Credentials: testCredentials, Regions: []string{"us-gov-west-1"}}); err != nil {
		t.Fatalf("GovCloud: %v", err)
	}
	a, err := NewEC2(Config{Credentials: testCredentials})
	if err != nil {
		t.Fatal(err)
	}
	ec := a.(*ec2Adapter)
	if !reflect.DeepEqual(ec.regions, []string{"us-east-1"}) || ec.endpoint("ec2", "us-east-1") != "https://ec2.us-east-1.amazonaws.com" {
		t.Fatalf("defaults: %v %s", ec.regions, ec.endpoint("ec2", "us-east-1"))
	}
	var names []string
	for _, acct := range ec.accounts() {
		names = append(names, acct.name())
	}
	if !reflect.DeepEqual(names, []string{"default/us-east-1", "ops/us-east-1"}) {
		t.Fatalf("accounts = %v", names)
	}
	sh, ok := a.(core.SecretHolder)
	if !ok {
		t.Fatal("not a SecretHolder")
	}
	if got := sh.SecretValues(); !reflect.DeepEqual(got, allSecrets) {
		t.Fatalf("secrets = %v", got)
	}
	if _, ok := a.(core.Consoler); ok {
		t.Fatal("EC2 needs no Consoler: console_url is the link")
	}
}

func TestEC2GovCloudHasNoConsoleLink(t *testing.T) {
	f := newFake(t, func(call) (int, string) { return 200, describeXML("", running) })
	in, err := newEC2(t, f, "us-gov-west-1").Instance(ctx, wire.CloudInstanceRef{Account: "default/us-gov-west-1", ID: wire.CloudInstanceID(running.id)})
	if err != nil {
		t.Fatal(err)
	}
	coretest.Valid(t, "cloud.instance", in)
	if in.ConsoleURL != "" || slices.Contains(in.Actions, wire.CloudActionKindConsole) {
		t.Fatalf("GovCloud console = %q, actions %v", in.ConsoleURL, in.Actions)
	}
}
