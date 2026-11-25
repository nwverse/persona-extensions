package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/ext"
)

//go:embed persona-extension.json
var authorManifest []byte

var manifest = pluginkit.PackagedManifest(authorManifest)

var about = func() (out struct{ ID, Version string }) {
	if err := json.Unmarshal(manifest, &out); err != nil {
		panic(err)
	}
	return out
}()

const connectorID = "binance"

type app struct {
	mu      sync.RWMutex
	cfg     config
	cfgErr  error
	cl      *client
	keyErr  error // the secrets cannot be read
	apiKey  string
	privPEM string
	market  *market
	ledger  *ledger
	paper   *ledger
	hub     *ext.Hub
	notify  notifier
	now     func() time.Time

	perms     keyPerms
	locked    string // non-empty: why writes are refused
	lastErr   error
	lastOK    time.Time
	leverage  map[string]map[string]int
	dual      map[string]bool
	positions map[string][]position
	posAt     map[string]time.Time
	quotes    map[string]quote // card price by args hash, for the market order slippage check
	writeMu   sync.Mutex       // one write at a time
	inbox     *inbox
	w         *watchState
	stop      context.CancelFunc

	// For tests: where Binance is, and whether init starts the background loop.
	hostURL func(string) string
	noLoop  bool
}

// quote is the price a card showed.
type quote struct {
	Price float64
	At    time.Time
}

func newApp() *app {
	a := &app{cfg: defaultConfig(), market: newMarket(), now: time.Now, leverage: map[string]map[string]int{},
		dual: map[string]bool{}, positions: map[string][]position{}, posAt: map[string]time.Time{}, quotes: map[string]quote{}}
	a.cfg, _ = parseConfig(nil)
	a.ledger, _ = openLedger("")
	a.paper, _ = openNamedLedger("", "paper-ledger.json")
	a.inbox = newInbox()
	a.w = newWatchState()
	a.cl = newClient(newHTTPClient(nil, envUpstream, 30*time.Second), nil)
	return a
}

func (a *app) client() *client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cl
}

func (a *app) init(ctx context.Context, in *ext.Init, hub *ext.Hub) (map[string]any, error) {
	cfg, cfgErr := parseConfig(in.InitializeParams.Config)
	s, keyErr := newSigner(in.InitializeParams.Secrets["api_key"], in.InitializeParams.Secrets["ed25519_private_key"])
	l, err := openLedger(in.Info().DataDir)
	if err != nil {
		return nil, err
	}
	var paper *ledger
	if cfg.PaperTrading {
		paper, err = openNamedLedger(in.Info().DataDir, "paper-ledger.json")
		if err != nil {
			return nil, err
		}
	}
	env := ext.Env()
	a.mu.Lock()
	a.cfg, a.cfgErr, a.keyErr, a.ledger, a.hub = cfg, cfgErr, keyErr, l, hub
	a.paper = paper
	a.apiKey, a.privPEM = in.InitializeParams.Secrets["api_key"], in.InitializeParams.Secrets["ed25519_private_key"]
	a.cl = newClient(a.httpClient(cfg.proxy, 30*time.Second), s)
	a.cl.hostURL = a.hostURL
	if env.HubURL != "" && env.HubToken != "" {
		a.notify = newHubNotifier(env.HubURL, env.HubToken)
	}
	if s == nil {
		a.locked = "还没有填写 API Key"
	} else {
		a.locked = "API Key 权限还没有检查完"
	}
	a.mu.Unlock()
	if cfgErr != nil {
		log.Printf("binance: settings: %v", cfgErr)
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	a.stop = cancel
	if !a.noLoop {
		go a.loop(loopCtx)
	}
	return map[string]any{"connectors": manifestConnectors()}, nil
}

// manifestConnectors is the manifest's contributes.connectors, as written there (the hub checks the program has it).
func manifestConnectors() json.RawMessage {
	var m struct {
		Contributes struct {
			Connectors json.RawMessage `json:"connectors"`
		} `json:"contributes"`
	}
	_ = json.Unmarshal(manifest, &m)
	return m.Contributes.Connectors
}

func (a *app) connector(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var in struct {
		Contribution string          `json:"contribution"`
		Name         string          `json:"name"`
		Args         json.RawMessage `json:"args"`
		RequestID    string          `json:"request_id"`
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
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		text, err := a.callTool(ctx, in.Name, in.Args)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": text}, nil
	case "actions.list":
		a.mu.RLock()
		defer a.mu.RUnlock()
		if a.cfg.PaperTrading {
			return map[string]any{"tools": paperTools}, nil
		}
		return map[string]any{"tools": writeTools}, nil
	case "actions.describe":
		return a.describe(in.Name, in.Args)
	case "actions.do":
		if strings.TrimSpace(in.RequestID) == "" || len(in.RequestID) > 128 {
			return nil, errors.New("actions.do needs a request_id of 1 to 128 characters")
		}
		ctx, cancel := context.WithTimeout(ctx, 50*time.Second)
		defer cancel()
		return a.do(ctx, in.RequestID, in.Name, in.Args)
	case "inbox.collect":
		return map[string]any{"things": a.inbox.collect(a.rules())}, nil
	}
	return nil, ext.UnknownMethod("connector." + method)
}

func (a *app) rules() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg.rules
}

func (a *app) status() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := map[string]any{"state": "ok", "account": "币安"}
	switch {
	case a.cfgErr != nil:
		out["state"], out["error"] = "error", "设置有误："+a.cfgErr.Error()
	case a.cfg.PaperTrading:
		out["account"] = "币安 · 纸面模拟"
	case a.keyErr != nil:
		out["state"], out["error"] = "not_signed_in", a.keyErr.Error()
	case a.cl.signer == nil:
		out["state"], out["guide"] = "not_configured", []string{"在 设置 › 扩展 › 币安 填写 API Key 和 Ed25519 私钥；只看公开行情可以不填"}
	case a.perms.Checked && a.locked != "":
		out["state"], out["error"] = "no_permission", a.locked
	case a.lastErr != nil:
		out["state"], out["error"] = "error", clip(a.lastErr.Error(), 200)
	}
	if !a.lastOK.IsZero() {
		out["last_sync"] = a.lastOK
	}
	return out
}

func (a *app) settings(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != "check" {
		return nil, ext.UnknownMethod("settings." + method)
	}
	var in struct {
		Config  json.RawMessage   `json:"config"`
		Secrets map[string]string `json:"secrets"`
		Proxy   string            `json:"proxy"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &ext.Error{Code: "invalid", Message: "invalid settings parameters"}
	}
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	ok, msg := a.checkSettings(ctx, in.Config, in.Secrets)
	if ok {
		return map[string]any{"ok": true}, nil
	}
	return map[string]any{"ok": false, "error": msg}, nil
}

// checkSettings checks proposed settings; Binance is asked only when both secrets are present and the key or the proxy
// changes (a guard setting changed alone is checked here, so a slow network cannot keep the user from tightening a
// limit). Because the hub stores secrets one at a time, saving either api_key or ed25519_private_key while the other is
// still unset checks its shape and lets it be saved; the extension stays not_signed_in with writes locked until both
// secrets are present and pass checkKey.
func (a *app) checkSettings(ctx context.Context, rawCfg json.RawMessage, secrets map[string]string) (bool, string) {
	cfg, err := parseConfig(rawCfg)
	if err != nil {
		return false, "设置有误：" + err.Error()
	}
	apiKey := strings.TrimSpace(secrets["api_key"])
	privPEM := strings.TrimSpace(secrets["ed25519_private_key"])
	if apiKey == "" && privPEM == "" {
		return true, ""
	}
	if apiKey == "" || privPEM == "" {
		if err := validateAPIKey(apiKey); err != nil {
			return false, err.Error()
		}
		if _, err := parseEd25519PrivateKey(privPEM); err != nil {
			return false, err.Error()
		}
		return true, ""
	}
	s, err := newSigner(apiKey, privPEM)
	if err != nil {
		return false, err.Error()
	}
	a.mu.RLock()
	same := secrets["api_key"] == a.apiKey && secrets["ed25519_private_key"] == a.privPEM && a.perms.Checked && a.locked == "" &&
		cfg.ProxyURL == a.cfg.ProxyURL
	a.mu.RUnlock()
	if same {
		return true, ""
	}
	c := newClient(a.httpClient(cfg.proxy, 8*time.Second), s)
	c.hostURL = a.hostURL
	if _, err := checkKey(ctx, c); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return false, "连币安超时，没有保存，请稍后重试"
		}
		return false, err.Error() + "，没有保存"
	}
	return true, ""
}

func main() {
	log.SetFlags(0)
	ext.Main(program(newApp()))
}

func program(a *app) ext.Program {
	return ext.Program{ID: about.ID, Version: about.Version, Manifest: manifest, Init: a.init,
		Namespaces: map[string]ext.Handler{"connector": a.connector, "settings": a.settings}}
}

// httpClient is the client to Binance; tests (hostURL set) use a plain one.
func (a *app) httpClient(proxy *url.URL, timeout time.Duration) *http.Client {
	if a.hostURL != nil {
		return &http.Client{Timeout: timeout}
	}
	return newHTTPClient(proxy, envUpstream, timeout)
}
