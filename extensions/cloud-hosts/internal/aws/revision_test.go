package aws

import (
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Only a throttled request is transient: AWS did not run it. A 5xx answer may follow a request that took effect.
func TestTransientOnlyForThrottling(t *testing.T) {
	cases := []struct {
		status    int
		code      string
		transient bool
	}{
		{503, "RequestLimitExceeded", true},
		{400, "ThrottlingException", true},
		{503, "Unavailable", false},
		{500, "InternalError", false},
		{500, "InsufficientInstanceCapacity", false},
	}
	ref := wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)}
	for _, c := range cases {
		t.Run(c.code, func(t *testing.T) {
			f := newFake(t, func(call) (int, string) { return c.status, ec2Error(c.code, "no") })
			a := newEC2(t, f)
			_, err := a.Action(ctx, core.Action{Ref: ref, Kind: wire.CloudActionKindStart, RequestID: "r"})
			if e := core.AsError(err); e == nil || e.Transient != c.transient {
				t.Fatalf("err = %#v, want transient %v", e, c.transient)
			}
			// Through the provider: a transient failure lets the same request id ask again, any other is answered
			// from the record without a second StartInstances.
			p := coretest.Provider(a, nil, allSecrets...)
			params := wire.CloudActionParams{Instance: ref, Action: wire.CloudActionKindStart, RequestID: "same"}
			_, _ = p.Action(ctx, params)
			_, _ = p.Action(ctx, params)
			want := 1
			if c.transient {
				want = 2
			}
			if n := f.count("StartInstances"); n != want+1 { // +1 for the direct call above
				t.Fatalf("StartInstances called %d times, want %d", n, want+1)
			}
		})
	}
	f := newFake(t, func(call) (int, string) { return 400, lsError("ThrottlingException", "Rate exceeded") })
	_, err := newLightsail(t, f).Instance(ctx, lsRef)
	if e := core.AsError(err); e == nil || !e.Transient {
		t.Fatalf("Lightsail throttling: %#v", e)
	}
	g := newFake(t, func(call) (int, string) { return 500, lsError("ServiceException", "An internal error occurred") })
	_, err = newLightsail(t, g).Instance(ctx, lsRef)
	if e := core.AsError(err); e == nil || e.Transient {
		t.Fatalf("Lightsail 500: %#v", e)
	}
}

// The adapter does not clip the vendor's words: the Provider redacts first and clips after.
func TestVendorMessageNotClipped(t *testing.T) {
	long := strings.Repeat("word ", 80) + "end-of-message"
	f := newFake(t, func(call) (int, string) { return 400, ec2Error("InvalidParameterValue", long) })
	_, err := newEC2(t, f).Instance(ctx, wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(running.id)})
	if err == nil || !strings.Contains(err.Error(), "end-of-message") {
		t.Fatalf("EC2 message clipped: %v", err)
	}
	g := newFake(t, func(call) (int, string) { return 400, lsError("InvalidInputException", long) })
	_, err = newLightsail(t, g).Instance(ctx, lsRef)
	if err == nil || !strings.Contains(err.Error(), "end-of-message") {
		t.Fatalf("Lightsail message clipped: %v", err)
	}
}

// An id with a slash, a percent sign or a space can be neither an EC2 instance id nor a Lightsail name: it is not
// found without a request, and never becomes part of a console link.
func TestOddIDsAreNotFoundWithoutAsking(t *testing.T) {
	f := newFake(t, func(c call) (int, string) {
		t.Errorf("AWS asked %s", c.Op)
		return 500, ""
	})
	ec, ls := newEC2(t, f), newLightsail(t, f)
	for _, id := range []wire.CloudInstanceID{"i-0abc/../x", "i-0abc%2F", "i-0abc def", "a/b", "a%2Fb", "a b", "-lead", ".hidden"} {
		if _, err := ec.Instance(ctx, wire.CloudInstanceRef{Account: "default/us-east-1", ID: id}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("EC2 %q: %v", id, err)
		}
		lref := wire.CloudInstanceRef{Account: "default/eu-central-1", ID: id}
		if _, err := ls.Instance(ctx, lref); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("Lightsail %q: %v", id, err)
		}
		if _, err := ls.Action(ctx, core.Action{Ref: lref, Kind: wire.CloudActionKindStart, RequestID: "r"}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("Lightsail start %q: %v", id, err)
		}
		if _, err := ls.Action(ctx, core.Action{Ref: lref, Kind: wire.CloudActionKindSnapshotCreate, RequestID: "r"}); coretest.Code(err) != wire.CloudErrorCodeNotFound {
			t.Fatalf("Lightsail snapshot %q: %v", id, err)
		}
	}
	// A listed instance whose name is not a Lightsail name gets no console link.
	g := newFake(t, func(call) (int, string) {
		return 200, mustJSON(map[string]any{"instances": []any{lsInstanceJSON("odd name/x", "running", "")}})
	})
	list, _, err := newLightsail(t, g).Instances(ctx)
	if err != nil || len(list) != 2 { // one per profile
		t.Fatalf("odd listed name: %+v %v", list, err)
	}
	for _, in := range list {
		if in.ConsoleURL != "" {
			t.Fatalf("odd listed name has a console link: %q", in.ConsoleURL)
		}
	}
	// Names with periods and underscores are Lightsail's own and are asked about.
	h := newFake(t, func(c call) (int, string) {
		return 200, mustJSON(map[string]any{"instance": lsInstanceJSON("my_site.example-1", "running", "")})
	})
	in, err := newLightsail(t, h).Instance(ctx, wire.CloudInstanceRef{Account: "default/eu-central-1", ID: "my_site.example-1"})
	if err != nil || in.ConsoleURL != "https://lightsail.aws.amazon.com/ls/webapp/eu-central-1/instances/my_site.example-1/connect" {
		t.Fatalf("dotted name: %+v %v", in, err)
	}
}

// The adapter's own words follow the caller's language; the vendor's are passed on as they are.
func TestLocalizedWords(t *testing.T) {
	spot := inst{id: "i-0eeeeeeeeeeeeeee1", state: "stopped", spot: true, reasonCode: "Server.SpotInstanceShutdown", reason: "Server.SpotInstanceShutdown: Spot instance shutdown"}
	user := inst{id: "i-0eeeeeeeeeeeeeee2", state: "stopped", reasonCode: "Client.UserInitiatedShutdown", reason: "Client.UserInitiatedShutdown: User initiated shutdown"}
	f := newFake(t, func(c call) (int, string) {
		if c.Form.Get("InstanceId.1") == user.id {
			return 200, describeXML("", user)
		}
		return 200, describeXML("", spot)
	})
	p := coretest.Provider(newEC2(t, f), nil, allSecrets...)
	ref := wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(spot.id)}
	en, err := p.Instance(ctx, wire.CloudInstanceParams{Locale: "en", Instance: ref})
	if err != nil || en.State != wire.CloudInstanceStatePreempted || en.StateDetail != "AWS took this spot instance back." {
		t.Fatalf("en = %+v %v", en, err)
	}
	zh, err := p.Instance(ctx, wire.CloudInstanceParams{Locale: "zh-CN", Instance: ref})
	if err != nil || zh.StateDetail == "" || zh.StateDetail == en.StateDetail || !strings.HasPrefix(zh.StateDetail, "AWS") {
		t.Fatalf("zh = %+v %v", zh, err)
	}
	coretest.Valid(t, "cloud.instance", zh)
	// The vendor's own state reason is not translated.
	u, err := p.Instance(ctx, wire.CloudInstanceParams{Locale: "zh-CN", Instance: wire.CloudInstanceRef{Account: "default/us-east-1", ID: wire.CloudInstanceID(user.id)}})
	if err != nil || u.StateDetail != user.reason {
		t.Fatalf("vendor detail = %q %v", u.StateDetail, err)
	}
	// A not-found answer of the adapter itself.
	_, enErr := p.Instance(ctx, wire.CloudInstanceParams{Locale: "en", Instance: wire.CloudInstanceRef{Account: "nobody/us-east-1", ID: wire.CloudInstanceID(spot.id)}})
	_, zhErr := p.Instance(ctx, wire.CloudInstanceParams{Locale: "zh-CN", Instance: wire.CloudInstanceRef{Account: "nobody/us-east-1", ID: wire.CloudInstanceID(spot.id)}})
	if coretest.Code(enErr) != wire.CloudErrorCodeNotFound || coretest.Code(zhErr) != wire.CloudErrorCodeNotFound || enErr.Error() == zhErr.Error() {
		t.Fatalf("not found: %v / %v", enErr, zhErr)
	}
}

// A list stops at the Provider's limit for one account.
func TestListStopsAtLimit(t *testing.T) {
	var mu sync.Mutex
	page := 0
	f := newFake(t, func(c call) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		var items []inst
		for i := 0; i < 200; i++ {
			items = append(items, inst{id: "i-" + strings.Repeat("0", 13) + hex4(page*200+i), state: "running"})
		}
		page++
		return 200, describeXML("next-"+hex4(page), items...)
	})
	a, err := NewEC2(Config{Credentials: "[default]\naws_access_key_id = " + keyDefault + "\naws_secret_access_key = " + secretDefault + "\n",
		HTTP: f.srv.Client(), Now: f.config(nil, nil).Now, Endpoint: f.config(nil, nil).Endpoint})
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := a.Instances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) < core.MaxInstances || f.count("DescribeInstances") != 3 {
		t.Fatalf("%d instances in %d pages", len(list), f.count("DescribeInstances"))
	}
	res, err := coretest.Provider(a, nil).Instances(ctx, wire.CloudInstancesParams{Refresh: true})
	if err != nil || !res.Partial || len(res.Instances) != core.MaxInstances {
		t.Fatalf("partial %v, %d instances, %v", res.Partial, len(res.Instances), err)
	}
}

func hex4(n int) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[n>>12&15], digits[n>>8&15], digits[n>>4&15], digits[n&15]})
}
