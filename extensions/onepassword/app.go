package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
)

const (
	connectorID  = "onepassword"
	uiConnection = "connection"
	tokenSecret  = "service_account_token"

	// rateWindow is how long the status says rate_limited after 1Password refused a request for it; afterwards the
	// tools are offered again, so the next call finds out whether the limit is over.
	rateWindow = 10 * time.Minute
)

var errNotConfigured = errors.New("1Password is not set up: no service account token and no computer with op")

type app struct {
	connect  connectFunc
	validate func(ctx context.Context, reference string) error // validateSDK
	now      func() time.Time

	connMu sync.Mutex // one sign-in at a time; held across the network, so never with mu
	mu     sync.Mutex
	token  string
	// machine is the computer whose op the extension uses instead of the token, when one is picked (#376); nodes
	// reaches its program there.
	machine string
	nodes   nodeCaller
	locale  string // the hub's language, for the words of a settings check
	api     opAPI
	lastOK  time.Time
	// lastErr is the last error 1Password answered, already redacted; errAt when.
	lastErr error
	errKind errKind
	errAt   time.Time
	// uses are the last uses of references Persona told of (secrets.used), newest first, for the page.
	uses []secretUse
}

func newApp(connect connectFunc) *app {
	return &app{connect: connect, validate: validateSDK, now: time.Now}
}

// config is the extension's configuration: the computer whose op to use, if any (at most one).
type config struct {
	Machines []string `json:"machines"`
}

func (a *app) init(_ context.Context, in *ext.Init, hub *ext.Hub) (map[string]any, error) {
	var c config
	if err := in.Config(&c); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.token = strings.TrimSpace(in.Secrets[tokenSecret])
	a.locale = in.Hub.Locale
	a.machine = ""
	if len(c.Machines) > 0 {
		a.machine = strings.TrimSpace(c.Machines[0])
	}
	if a.nodes == nil && hub != nil {
		a.nodes = hubNodeCaller(hub)
	}
	return nil, nil
}

// client is op on the picked computer when there is one; otherwise it signs in with the token on first use and keeps
// the client for the rest of the run (a new token or computer restarts the program).
func (a *app) client(ctx context.Context) (opAPI, error) {
	a.connMu.Lock()
	defer a.connMu.Unlock()
	a.mu.Lock()
	api, token, machine, nodes := a.api, a.token, a.machine, a.nodes
	a.mu.Unlock()
	if machine != "" && nodes != nil {
		return nodeAPI{call: nodes, machine: machine}, nil
	}
	if api != nil {
		return api, nil
	}
	if token == "" {
		return nil, errNotConfigured
	}
	api, err := a.connect(ctx, token)
	if err != nil {
		return nil, a.record(err)
	}
	a.mu.Lock()
	a.api = api
	a.mu.Unlock()
	return api, nil
}

// record notes how a call to 1Password went, and returns err redacted (nil for nil).
func (a *app) record(err error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err == nil {
		a.lastOK, a.lastErr = a.now(), nil
		return nil
	}
	clean := a.clean(err)
	a.lastErr, a.errKind, a.errAt = clean, kindOf(err), a.now()
	return clean
}

// clean is err as it may leave the program: the token removed, one line, clipped. A rate limit keeps its code.
// Called with mu held.
func (a *app) clean(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded // its words carry nothing secret, and the page tells it apart
	}
	msg := oneLine(redact(err.Error(), a.token), 300)
	if c := opCode(err); c != "" && c != codeRateLimited { // op on a computer: its code tells the page what to say
		return &ext.Error{Code: c, Message: msg}
	}
	if kindOf(err) == errRate {
		return &ext.Error{Code: "rate_limited", Message: "1Password rate limit reached: " + msg}
	}
	return errors.New(msg)
}

func (a *app) status() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]any{"state": "ok", "account": "1Password service account", "via": "service account token"}
	if a.machine != "" {
		out["account"], out["via"] = "1Password account of op on a computer", "op on a computer"
	}
	switch {
	case a.token == "" && a.machine == "":
		out["state"] = "not_configured"
		out["guide"] = []string{
			"Create a service account on 1Password.com (Developer in the sidebar), with read access to the vaults Persona may look into.",
			"Paste its token into this extension's settings (Settings › Extensions › 1Password).",
			"Or pick a computer in those settings where the 1Password CLI (op) is signed in through the 1Password app.",
		}
	case a.lastErr != nil && a.errKind == errAuth && a.machine != "":
		out["state"] = "not_signed_in"
		out["error"] = a.lastErr.Error()
		out["guide"] = []string{"Unlock the 1Password app on that computer, check that Integrate with 1Password CLI is on in its Developer settings, and approve op when it asks."}
	case a.lastErr != nil && a.errKind == errAuth:
		out["state"] = "not_signed_in"
		out["error"] = a.lastErr.Error()
		out["guide"] = []string{"1Password refused the service account token. Create a new token and paste it into this extension's settings."}
	case a.lastErr != nil && a.errKind == errRate && a.now().Sub(a.errAt) < rateWindow:
		out["state"] = "rate_limited"
		out["error"] = a.lastErr.Error()
	}
	if !a.lastOK.IsZero() {
		out["last_sync"] = a.lastOK
	}
	return out
}

func (a *app) connector(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var in struct {
		Contribution string          `json:"contribution"`
		Name         string          `json:"name"`
		Args         json.RawMessage `json:"args"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: "invalid connector parameters"}
		}
	}
	if in.Contribution != "" && in.Contribution != connectorID {
		return nil, ext.NoContribution(in.Contribution)
	}
	switch method {
	case "status":
		return a.status(), nil
	case "tools.list":
		return map[string]any{"tools": readTools}, nil
	case "tools.call":
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		text, err := a.callTool(ctx, in.Name, in.Args)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		text = redact(text, a.token)
		a.mu.Unlock()
		return map[string]any{"text": text}, nil
	case "actions.list":
		return map[string]any{"tools": []tool{}}, nil
	}
	return nil, ext.UnknownMethod("connector." + method)
}

// settings handles settings.check: the hub asks before it saves a new token, and saves it only if 1Password takes
// it. The check signs in with a client of its own and drops it; the running program keeps its own token until the
// hub restarts it with the new one.
func (a *app) settings(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != "check" {
		return nil, ext.UnknownMethod("settings." + method)
	}
	var in struct {
		Secrets map[string]string `json:"secrets"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: "invalid settings parameters"}
		}
	}
	token := strings.TrimSpace(in.Secrets[tokenSecret])
	if token == "" {
		return map[string]any{"ok": true}, nil
	}
	a.mu.Lock()
	t := textsFor(a.locale)
	a.mu.Unlock()
	if !strings.HasPrefix(token, "ops_") {
		return map[string]any{"ok": false, "error": t.checkNotToken}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	fail := func(err error) (any, error) {
		if errors.Is(err, context.DeadlineExceeded) {
			return map[string]any{"ok": false, "error": t.timedOut}, nil
		}
		return map[string]any{"ok": false, "error": fmt.Sprintf(t.checkRefused, oneLine(redact(err.Error(), token), 220))}, nil
	}
	api, err := a.connect(ctx, token)
	if err != nil {
		return fail(err)
	}
	if _, err := api.listVaults(ctx); err != nil {
		return fail(err)
	}
	return map[string]any{"ok": true}, nil
}

// listVaults reads the vaults the service account can see.
func (a *app) listVaults(ctx context.Context) ([]vaultInfo, error) {
	api, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := api.listVaults(ctx)
	if err != nil {
		return nil, a.record(err)
	}
	a.record(nil)
	out := make([]vaultInfo, 0, len(raw))
	for _, v := range raw {
		out = append(out, vaultFromSDK(v))
	}
	return out, nil
}

func (a *app) listItems(ctx context.Context, vaultID string) ([]itemInfo, error) {
	api, err := a.client(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := api.listItems(ctx, vaultID)
	if err != nil {
		return nil, a.record(err)
	}
	a.record(nil)
	out := make([]itemInfo, 0, len(raw))
	for _, it := range raw {
		out = append(out, itemFromOverview(it))
	}
	return out, nil
}

func (a *app) getItem(ctx context.Context, vaultID, itemID string) (itemDetail, error) {
	api, err := a.client(ctx)
	if err != nil {
		return itemDetail{}, err
	}
	raw, err := api.getItem(ctx, vaultID, itemID)
	if err != nil {
		return itemDetail{}, a.record(err)
	}
	a.record(nil)
	return itemFromSDK(raw), nil
}
