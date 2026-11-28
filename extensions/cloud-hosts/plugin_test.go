package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Synthetic credentials: nothing here opens anything.
const (
	hetznerToken = "test-hetzner-token-0001"
	webhookToken = "test-webhook-token-0001"
)

func TestProtocol(t *testing.T) {
	res := exttest.Check(t, manifest, exttest.Serve(t, program()))
	// With nothing set up, no provider is offered.
	if got := string(res.Contributes["cloud_hosts"]); got != "[]" {
		t.Fatalf("contributes.cloud_hosts with no settings: %s", got)
	}
}

// The manifest is the contract with the hub: every provider is declared once, a custom action that changes
// something asks for a confirmation, and every address the adapters reach on their own is in permissions.network.
func TestManifest(t *testing.T) {
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	ids, custom := declared()
	want := []string{"aws-ec2", "aws-lightsail", "gcp", "oci", "pterodactyl", "solusvm", "solusvm2", "virtualizor",
		"proxmox", "hetzner", "digitalocean", "vultr", "custom"}
	if !slices.Equal(ids, want) {
		t.Fatalf("providers %v, want %v", ids, want)
	}
	built := build(settings{}, nil, t.TempDir())
	for _, id := range ids {
		if b, ok := built[id]; !ok || b.configured {
			t.Errorf("%s: built %v configured %v with no settings", id, ok, b.configured)
		}
	}
	if len(built) != len(ids) {
		t.Errorf("build makes %d providers, the manifest declares %d", len(built), len(ids))
	}
	if custom["oci"]["keepalive"] != wire.CloudSafetyRead || custom["custom"]["probe"] != wire.CloudSafetyRead ||
		custom["custom"]["renew"] != wire.CloudSafetyConfirm {
		t.Errorf("custom actions: %v", custom)
	}
	var raw struct {
		Contributes struct {
			CloudHosts []wire.CloudHostItem `json:"cloud_hosts"`
		} `json:"contributes"`
		Secrets []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"secrets"`
		Permissions struct {
			Network []string `json:"network"`
		} `json:"permissions"`
		Configuration struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(manifest, &raw); err != nil {
		t.Fatal(err)
	}
	for _, c := range raw.Contributes.CloudHosts {
		for _, a := range c.CustomActions {
			if !a.Safety.Valid() || (a.Safety != wire.CloudSafetyRead && strings.TrimSpace(a.Confirm) == "") {
				t.Errorf("%s/%s: safety %q needs a confirmation sentence", c.ID, a.ID, a.Safety)
			}
		}
	}
	// Every secret the program reads is declared, none is required (each provider is optional), and no credential is
	// a configuration field.
	names := []string{secretAWS, secretGCP, secretOCIConfig, secretOCIKey, secretPterodactyl, secretSolusVM, secretSolusVM2,
		secretVirtualizor, secretProxmox, secretHetzner, secretDigitalOcean, secretVultr, secretCustomToken}
	var declaredSecrets []string
	for _, s := range raw.Secrets {
		declaredSecrets = append(declaredSecrets, s.Name)
		if s.Required {
			t.Errorf("secret %s is required, but every provider is optional", s.Name)
		}
	}
	slices.Sort(names)
	slices.Sort(declaredSecrets)
	if !slices.Equal(names, declaredSecrets) {
		t.Errorf("secrets %v, the program reads %v", declaredSecrets, names)
	}
	for field := range raw.Configuration.Properties {
		for _, word := range []string{"key", "token", "secret", "password", "credential", "hash"} {
			if strings.Contains(field, word) {
				t.Errorf("configuration field %q looks like a credential: credentials are secrets", field)
			}
		}
	}
	for _, host := range []string{"*.amazonaws.com", "compute.googleapis.com", "oauth2.googleapis.com", "*.oraclecloud.com",
		"api.hetzner.cloud", "api.digitalocean.com", "api.vultr.com", "config:pterodactyl_url", "config:solusvm_url",
		"config:solusvm2_url", "config:virtualizor_url", "config:proxmox_url", "config:custom_webhook_url"} {
		if !slices.Contains(raw.Permissions.Network, host) {
			t.Errorf("permissions.network lacks %s", host)
		}
	}
	for _, entry := range raw.Permissions.Network {
		if strings.HasSuffix(entry, ":http") {
			t.Errorf("permissions.network %s: credentials must not travel over plain http", entry)
		}
	}
}

// hetznerMock is a small Hetzner Cloud API: one server that changes its public address when it is started.
type hetznerMock struct {
	mu       sync.Mutex
	status   string
	ip       string
	powerOns int
	images   int
	echo     bool // answer 401 with the token in the error body
}

func (m *hetznerMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+hetznerToken || m.echo {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"code":"unauthorized","message":"the token %s is not valid"}}`, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		return
	}
	server := func() string {
		return fmt.Sprintf(`{"id":4201,"name":"web-1","status":%q,"locked":false,
			"public_net":{"ipv4":{"ip":%q},"ipv6":{"ip":"2001:db8:10::/64"}},"private_net":[{"ip":"10.0.0.2"}],
			"server_type":{"name":"cx22","cores":2,"memory":4,"disk":40,"architecture":"x86"},
			"location":{"name":"fsn1"},"datacenter":{"name":"fsn1-dc14","location":{"name":"fsn1"}},"image":{"description":"Debian 13"}}`, m.status, m.ip)
	}
	switch path := strings.TrimPrefix(r.URL.Path, "/v1"); {
	case r.Method == http.MethodGet && path == "/servers":
		fmt.Fprintf(w, `{"servers":[%s],"meta":{"pagination":{"next_page":null}}}`, server())
	case r.Method == http.MethodGet && path == "/servers/4201":
		fmt.Fprintf(w, `{"server":%s}`, server())
	case r.Method == http.MethodPost && path == "/servers/4201/actions/poweron":
		m.powerOns++
		m.status, m.ip = "running", "203.0.113.77" // it comes back with another address
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"action":{"id":1,"status":"running","error":null}}`)
	case r.Method == http.MethodPost && path == "/servers/4201/actions/create_image":
		m.images++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"action":{"id":2,"status":"running","error":null},"image":{"id":9001,"type":"snapshot","status":"creating","description":"before upgrade","image_size":null,"created":"2026-11-20T10:00:00+00:00","deleted":null,"created_from":{"id":4201}}}`)
	case r.Method == http.MethodGet && path == "/images":
		fmt.Fprint(w, `{"images":[{"id":9001,"type":"snapshot","status":"available","description":"before upgrade","image_size":2.5,"created":"2026-11-20T10:00:00+00:00","deleted":null,"created_from":{"id":4201}}],"meta":{"pagination":{"next_page":null}}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":"not_found","message":"not found"}}`)
	}
}

// start runs the program against the fake hub with the given settings and secrets.
func start(t *testing.T, cfg settings, secrets map[string]string) *exttest.Host {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return exttest.StartWith(t, manifest, exttest.Serve(t, program()), func(p *wire.InitializeParams) {
		p.Config, p.Secrets = raw, secrets
	})
}

func errCode(err error) string {
	var e *ext.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// noLeak fails when a credential appears in what the program answered.
func noLeak(t *testing.T, what any, secrets ...string) {
	t.Helper()
	text := fmt.Sprint(what)
	if raw, err := json.Marshal(what); err == nil {
		text += string(raw)
	}
	if err, ok := what.(error); ok {
		text += err.Error()
	}
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Fatalf("a credential leaked into an answer: %q in %q", s, text)
		}
	}
}

// One provider end to end over the protocol: the list, the spot-style "start, then read the new address back",
// request_id as an idempotency key, snapshots and the console link, each answer held to the schema by the fake hub.
func TestEndToEnd(t *testing.T) {
	mock := &hetznerMock{status: "off", ip: "203.0.113.10"}
	srv := httptest.NewServer(mock)
	defer srv.Close()
	overrides.bases = map[string]string{"hetzner": srv.URL + "/v1"}
	defer func() { overrides.bases = nil }()

	h := start(t, settings{}, map[string]string{secretHetzner: hetznerToken})
	if got := string(h.Init.Contributes["cloud_hosts"]); got != `[{"id":"hetzner"}]` {
		t.Fatalf("contributes.cloud_hosts: %s", got)
	}
	base := func() (string, string) { return "hetzner", "en" }
	contribution, locale := base()

	var list wire.CloudInstanceList
	if err := h.Call("cloud.instances", wire.CloudInstancesParams{Contribution: contribution, Locale: locale, Refresh: true}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Instances) != 1 {
		t.Fatalf("instances: %+v", list)
	}
	in := list.Instances[0]
	if in.Account != "default" || in.ID != "4201" || in.State != wire.CloudInstanceStateStopped ||
		!slices.Contains(in.Actions, wire.CloudActionKindStart) || slices.Contains(in.Actions, wire.CloudActionKindStop) ||
		!slices.Equal(in.PublicIps[:1], []string{"203.0.113.10"}) || !strings.HasPrefix(in.ConsoleURL, "https://") {
		t.Fatalf("instance: %+v", in)
	}
	noLeak(t, list, hetznerToken)
	ref := wire.CloudInstanceRef{Account: in.Account, ID: in.ID}

	// start: accepted at once; the same request_id again is answered from the record, not sent twice.
	act := wire.CloudActionParams{Contribution: contribution, Locale: locale, Instance: ref, Action: wire.CloudActionKindStart, RequestID: "req-start-1"}
	for i := 0; i < 2; i++ {
		var res wire.CloudActionResult
		if err := h.Call("cloud.action", act, &res); err != nil {
			t.Fatal(err)
		}
		if res.Status != wire.CloudActionStatusAccepted && res.Status != wire.CloudActionStatusDone {
			t.Fatalf("start: %+v", res)
		}
	}
	if mock.powerOns != 1 {
		t.Fatalf("the vendor was asked to power on %d times for one request_id", mock.powerOns)
	}

	// The hub's follow-up: cloud.instance says running, with the address the instance came back with.
	var now wire.CloudInstance
	if err := h.Call("cloud.instance", wire.CloudInstanceParams{Contribution: contribution, Locale: locale, Instance: ref}, &now); err != nil {
		t.Fatal(err)
	}
	if now.State != wire.CloudInstanceStateRunning || now.PublicIps[0] != "203.0.113.77" || !slices.Contains(now.Actions, wire.CloudActionKindStop) {
		t.Fatalf("after start: %+v", now)
	}
	// sync_ip is the vendor's answer of now, also when the list was read before the start.
	var synced wire.CloudActionResult
	act.Action, act.RequestID = wire.CloudActionKindSyncIp, "req-sync-1"
	if err := h.Call("cloud.action", act, &synced); err != nil {
		t.Fatal(err)
	}
	if synced.Status != wire.CloudActionStatusDone || synced.Instance == nil || synced.Instance.PublicIps[0] != "203.0.113.77" {
		t.Fatalf("sync_ip: %+v", synced)
	}
	// The list without refresh does not answer the address from before the start either.
	if err := h.Call("cloud.instances", wire.CloudInstancesParams{Contribution: contribution, Locale: locale}, &list); err != nil {
		t.Fatal(err)
	}
	if list.Instances[0].PublicIps[0] != "203.0.113.77" {
		t.Fatalf("the list still says the old address: %+v", list.Instances[0])
	}

	// console: an https link without credentials.
	var console wire.CloudActionResult
	act.Action, act.RequestID = wire.CloudActionKindConsole, "req-console-1"
	if err := h.Call("cloud.action", act, &console); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(console.URL, "https://") || strings.Contains(console.URL, "@") {
		t.Fatalf("console: %+v", console)
	}

	// snapshot_create, twice with one request_id, then the list.
	act.Action, act.RequestID, act.Name = wire.CloudActionKindSnapshotCreate, "req-snap-1", "before upgrade"
	for i := 0; i < 2; i++ {
		var res wire.CloudActionResult
		if err := h.Call("cloud.action", act, &res); err != nil {
			t.Fatal(err)
		}
		if res.Snapshot == nil || res.Snapshot.ID != "9001" {
			t.Fatalf("snapshot_create: %+v", res)
		}
	}
	if mock.images != 1 {
		t.Fatalf("%d snapshots were taken for one request_id", mock.images)
	}
	var snaps wire.CloudSnapshotList
	if err := h.Call("cloud.snapshots", wire.CloudSnapshotsParams{Contribution: contribution, Locale: locale, Instance: ref}, &snaps); err != nil {
		t.Fatal(err)
	}
	if len(snaps.Snapshots) != 1 || snaps.Snapshots[0].State != wire.CloudSnapshotStateReady {
		t.Fatalf("snapshots: %+v", snaps)
	}

	// The closed error vocabulary: snapshot_list is never an action, a gone instance is not_found, an action the
	// provider does not declare is unsupported.
	act.Name = ""
	act.Action, act.RequestID = wire.CloudActionKindCustom, "req-custom-1"
	act.Custom = "renew"
	if err := h.Call("cloud.action", act, nil); errCode(err) != string(wire.CloudErrorCodeUnsupported) {
		t.Fatalf("an undeclared custom action: %v", err)
	}
	gone := wire.CloudInstanceRef{Account: "default", ID: "4999"}
	if err := h.Call("cloud.instance", wire.CloudInstanceParams{Contribution: contribution, Locale: locale, Instance: gone}, nil); errCode(err) != string(wire.CloudErrorCodeNotFound) {
		t.Fatalf("a gone instance: %v", err)
	}

	// A vendor that echoes the token back in its error: the answer names the failure, never the token.
	mock.mu.Lock()
	mock.echo = true
	mock.mu.Unlock()
	err := h.Call("cloud.instances", wire.CloudInstancesParams{Contribution: contribution, Locale: locale, Refresh: true}, &list)
	if err == nil {
		noLeak(t, list, hetznerToken)
		if !list.Partial {
			t.Fatalf("a refused token must not read as a complete list: %+v", list)
		}
	} else {
		if errCode(err) != string(wire.CloudErrorCodeNotSignedIn) {
			t.Fatalf("a refused token: %v", err)
		}
		noLeak(t, err, hetznerToken)
	}
	err = h.Call("cloud.instance", wire.CloudInstanceParams{Contribution: contribution, Locale: locale, Instance: ref}, nil)
	if errCode(err) != string(wire.CloudErrorCodeNotSignedIn) {
		t.Fatalf("a refused token: %v", err)
	}
	noLeak(t, err, hetznerToken)
}

// A provider the user did not set up answers not_signed_in with what is missing; a contribution the manifest does
// not have is no_contribution.
func TestNotSetUp(t *testing.T) {
	h := start(t, settings{}, nil)
	ids, _ := declared()
	for _, id := range ids {
		err := h.Call("cloud.instances", wire.CloudInstancesParams{Contribution: id, Locale: "en"}, nil)
		if errCode(err) != string(wire.CloudErrorCodeNotSignedIn) {
			t.Errorf("%s: %v", id, err)
		}
		err = h.Call("cloud.action", wire.CloudActionParams{Contribution: id, Locale: "en",
			Instance: wire.CloudInstanceRef{Account: "a", ID: "b"}, Action: wire.CloudActionKindStart, RequestID: wire.CloudRequestID("r-" + id)}, nil)
		if errCode(err) != string(wire.CloudErrorCodeNotSignedIn) {
			t.Errorf("%s action: %v", id, err)
		}
	}
	if err := h.Call("cloud.instances", wire.CloudInstancesParams{Contribution: "nope", Locale: "en"}, nil); errCode(err) != wire.ErrorCodeNoContribution {
		t.Errorf("an unknown contribution: %v", err)
	}
}

// What the user wrote decides which providers this run has; a half set-up panel and a plain http address are
// offered so their problem is shown, and neither reaches the network.
func TestConfiguredProviders(t *testing.T) {
	h := start(t, settings{
		AWSRegions:          []string{"us-east-1"},
		AWSLightsailRegions: []string{"eu-central-1"},
		PterodactylURL:      "https://panel.example.com",
		ProxmoxURL:          "http://pve.example.com:8006",
		CustomHosts:         []string{"name=free-vps; console=https://panel.example.com/vps/1; expires=2027-01-31"},
	}, map[string]string{
		secretAWS:     "[default]\naws_access_key_id = AKIAEXAMPLEEXAMPLE00\naws_secret_access_key = test-secret-access-key-000000000000000000\n",
		secretProxmox: "root@pam!persona=00000000-0000-0000-0000-000000000001",
	})
	var got []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(h.Init.Contributes["cloud_hosts"], &got); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, g := range got {
		ids = append(ids, g.ID)
	}
	if want := []string{"aws-ec2", "aws-lightsail", "pterodactyl", "proxmox", "custom"}; !slices.Equal(ids, want) {
		t.Fatalf("configured providers %v, want %v", ids, want)
	}
	// The panel without its key says so.
	err := h.Call("cloud.instances", wire.CloudInstancesParams{Contribution: "pterodactyl", Locale: "en"}, nil)
	if errCode(err) != string(wire.CloudErrorCodeNotSignedIn) {
		t.Fatalf("a panel without its key: %v", err)
	}
	// A key is never sent to a plain http address.
	err = h.Call("cloud.instances", wire.CloudInstancesParams{Contribution: "proxmox", Locale: "en"}, nil)
	if errCode(err) != string(wire.CloudErrorCodeFailed) || !strings.Contains(err.Error(), "https://") {
		t.Fatalf("a plain http panel: %v", err)
	}
	noLeak(t, err, "00000000-0000-0000-0000-000000000001")
}

// The custom provider end to end: a host found reachable by its probe, its webhook called once per request_id with
// the token, the read custom action, and renew moving the end of the term.
func TestCustomHostsEndToEnd(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	hooks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+webhookToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls[r.URL.Path]++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hooks.Close()
	alive, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer alive.Close()
	go func() {
		for {
			c, err := alive.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := alive.Addr().(*net.TCPAddr).Port
	allowHTTP = true
	defer func() { allowHTTP = false }()
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")

	h := start(t, settings{
		CustomWebhookURL: hooks.URL,
		CustomHosts: []string{fmt.Sprintf("name=free-vps; title=Free VPS; address=127.0.0.1; probe=tcp:%d; stop=/free-vps/stop; "+
			"reboot=/free-vps/reboot; renew=/free-vps/renew; console=https://panel.example.com/vps/1; expires=2027-01-31; term_days=30", port)},
	}, map[string]string{secretCustomToken: webhookToken})

	var list wire.CloudInstanceList
	if err := h.Call("cloud.instances", wire.CloudInstancesParams{Contribution: "custom", Locale: "en", Refresh: true}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Instances) != 1 {
		t.Fatalf("%+v", list)
	}
	in := list.Instances[0]
	if in.Account != "custom" || in.ID != "free-vps" || in.State != wire.CloudInstanceStateRunning ||
		!slices.Contains(in.Actions, wire.CloudActionKindStop) || slices.Contains(in.Actions, wire.CloudActionKindStart) ||
		!slices.Contains(in.CustomActions, "renew") || !slices.Contains(in.CustomActions, "probe") ||
		in.Lifecycle == nil || in.Lifecycle.ExpiresAt.Format("2006-01-02") != "2027-01-31" {
		t.Fatalf("instance: %+v lifecycle %+v", in, in.Lifecycle)
	}
	noLeak(t, list, webhookToken, hooks.URL)
	ref := wire.CloudInstanceRef{Account: in.Account, ID: in.ID}

	act := wire.CloudActionParams{Contribution: "custom", Locale: "en", Instance: ref, Action: wire.CloudActionKindReboot, RequestID: "req-reboot-1"}
	for i := 0; i < 2; i++ {
		if err := h.Call("cloud.action", act, nil); err != nil {
			t.Fatal(err)
		}
	}
	// An action without a webhook is not offered and not done.
	act.Action, act.RequestID = wire.CloudActionKindForceStop, "req-force-1"
	if err := h.Call("cloud.action", act, nil); errCode(err) != string(wire.CloudErrorCodeUnsupported) {
		t.Fatalf("an action without a webhook: %v", err)
	}
	// The read custom action answers what it found.
	var probed wire.CloudActionResult
	act.Action, act.Custom, act.RequestID = wire.CloudActionKindCustom, "probe", "req-probe-1"
	if err := h.Call("cloud.action", act, &probed); err != nil {
		t.Fatal(err)
	}
	if probed.Status != wire.CloudActionStatusDone || probed.Message == "" {
		t.Fatalf("probe: %+v", probed)
	}
	// renew: the webhook once, the end of the term one term later.
	var renewed wire.CloudActionResult
	act.Custom, act.RequestID = "renew", "req-renew-1"
	for i := 0; i < 2; i++ {
		if err := h.Call("cloud.action", act, &renewed); err != nil {
			t.Fatal(err)
		}
	}
	if renewed.Instance == nil || renewed.Instance.Lifecycle == nil || renewed.Instance.Lifecycle.ExpiresAt.Format("2006-01-02") != "2027-03-02" {
		t.Fatalf("renew: %+v", renewed.Instance)
	}
	noLeak(t, renewed, webhookToken, hooks.URL)
	mu.Lock()
	defer mu.Unlock()
	if calls["/free-vps/reboot"] != 1 || calls["/free-vps/renew"] != 1 || calls["/free-vps/stop"] != 0 {
		t.Fatalf("webhook calls: %v", calls)
	}
}
