package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/ext"
)

//go:embed persona-extension.json
var authorManifest []byte

var manifest = pluginkit.PackagedManifest(authorManifest)

const quotaPath = "/v1/magpie/quotas?kept=1"

type quotaRequest struct {
	QueryID string            `json:"query_id"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type quotaResponse struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

type magpieAccount struct {
	Provider  string         `json:"provider"`
	Kind      string         `json:"kind"`
	Plan      string         `json:"plan"`
	User      string         `json:"user"`
	Name      string         `json:"name"`
	ReadAt    string         `json:"readAt"`
	AsOf      string         `json:"asOf"`
	Error     string         `json:"error"`
	ReadError string         `json:"readError"`
	Windows   []magpieWindow `json:"windows"`
}

type magpieWindow struct {
	Name      string   `json:"name"`
	Used      *float64 `json:"used"`
	ResetsAt  string   `json:"resetsAt"`
	Text      bool     `json:"text"`
	Unlimited bool     `json:"unlimited"`
}

type account struct {
	ID        string     `json:"id"`
	Provider  string     `json:"provider"`
	Name      string     `json:"name"`
	Plan      string     `json:"plan,omitempty"`
	SampledAt *time.Time `json:"sampled_at,omitempty"`
	Notice    string     `json:"notice,omitempty"`
	Windows   []window   `json:"windows"`
}

type window struct {
	Label    string     `json:"label"`
	Used     float64    `json:"used_percent"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Model    string     `json:"model,omitempty"`
	Title    string     `json:"title,omitempty"`
}

func main() {
	ext.Main(program())
}

func program() ext.Program {
	var hub *ext.Hub
	var secret []byte
	return ext.Program{
		ID: "magpie-usage", Version: "0.1.0", Manifest: manifest,
		Init: func(_ context.Context, in *ext.Init, h *ext.Hub) (map[string]any, error) {
			hub = h
			var err error
			dir := in.Info().DataDir
			if dir == "" {
				// The standalone Persona conformance runner provides the directory through the environment.
				dir = os.Getenv("PERSONA_EXTENSION_DATA")
			}
			secret, err = accountSecret(dir)
			if err != nil {
				return nil, errors.New("cannot initialize Magpie account aliases")
			}
			return nil, nil
		},
		Namespaces: map[string]ext.Handler{"usage": func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
			if method != "query" {
				return nil, ext.UnknownMethod(method)
			}
			var p struct {
				Contribution string `json:"contribution"`
				QueryID      string `json:"query_id"`
				BaseURL      string `json:"base_url"`
				Lang         string `json:"lang"`
			}
			if json.Unmarshal(raw, &p) != nil || p.Contribution != "magpie" || p.QueryID == "" || hub == nil {
				return nil, errors.New("invalid Magpie usage query")
			}
			base, err := url.Parse(p.BaseURL)
			if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "http" && base.Scheme != "https") {
				return nil, errors.New("invalid Magpie gateway address")
			}
			body, err := fetchQuotas(base, p.QueryID, func(req quotaRequest) (quotaResponse, error) {
				var resp quotaResponse
				err := hub.Call(ctx, "usage.http", req, &resp)
				return resp, err
			})
			if err != nil {
				return nil, err
			}
			accounts, err := mapQuotas(body, secret, base.String(), p.Lang)
			if err != nil {
				return nil, err
			}
			return struct {
				Accounts []account `json:"accounts"`
			}{accounts}, nil
		}},
	}
}

func fetchQuotas(base *url.URL, queryID string, send func(quotaRequest) (quotaResponse, error)) (string, error) {
	// A path relative to the launcher's gateway root also works behind a reverse proxy with a path prefix.
	req := quotaRequest{QueryID: queryID, Method: http.MethodGet, URL: quotaPath}
	resp, err := send(req)
	if err != nil {
		return "", errors.New("cannot read Magpie subscription usage")
	}
	if resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden {
		host := strings.ToLower(base.Hostname())
		if base.Scheme != "https" && host != "localhost" && !net.ParseIP(host).IsLoopback() {
			return "", errors.New("a protected remote Magpie gateway needs HTTPS")
		}
		req.Headers = map[string]string{"Authorization": "Bearer ${launcher_key}"}
		resp, err = send(req)
	}
	if err != nil || resp.Status != http.StatusOK {
		return "", errors.New("Magpie quota endpoint did not return usage")
	}
	return resp.Body, nil
}

func mapQuotas(body string, secret []byte, scope, lang string) ([]account, error) {
	var data struct {
		Data []magpieAccount `json:"data"`
	}
	if json.Unmarshal([]byte(body), &data) != nil || data.Data == nil {
		return nil, errors.New("invalid Magpie quota response")
	}
	out := []account{}
	seen := make(map[string]bool)
	for _, src := range data.Data {
		if src.Kind != "subscription" || (src.Provider != "claude" && src.Provider != "codex") {
			continue
		}
		identity := strings.TrimSpace(src.User)
		if identity == "" {
			identity = strings.TrimSpace(src.Name)
		}
		if identity == "" {
			continue
		}
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(scope + "\x00" + src.Provider + "\x00" + identity))
		id := hex.EncodeToString(mac.Sum(nil)[:16])
		if seen[id] {
			continue
		}
		seen[id] = true
		a := account{ID: id, Provider: src.Provider, Name: "Magpie " + id[:12], Windows: []window{}}
		switch plan := strings.ToLower(src.Plan); plan {
		case "free", "pro", "max", "promax", "plus", "team", "enterprise", "business":
			a.Plan = plan
		}
		a.SampledAt = parseTime(src.ReadAt)
		if a.SampledAt == nil {
			a.SampledAt = parseTime(src.AsOf)
		}
		// Persona defaults an undated sample to the poll time. Only send numerical windows with a known sample time.
		if a.SampledAt != nil {
			for _, w := range src.Windows {
				if w.Used == nil || w.Text || w.Unlimited {
					continue
				}
				v := window{Used: *w.Used, ResetsAt: parseTime(w.ResetsAt)}
				switch w.Name {
				case "5 hours":
					v.Label = "5h"
				case "7 days":
					v.Label = "7d"
				case "7 days · Fable":
					v.Label, v.Model, v.Title = "7d", "Fable", "Fable"
				default:
					continue
				}
				a.Windows = append(a.Windows, v)
			}
		}
		switch {
		case len(a.Windows) == 0:
			a.Notice = localized(lang, "Subscription usage unavailable", "暂无订阅额度读数", "サブスクリプションの使用量を取得できません")
		case src.Error != "" || src.ReadError != "":
			a.Notice = localized(lang, "Magpie is showing a saved reading", "Magpie 显示的是上次读数", "Magpie の保存済みの値を表示しています")
		}
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b account) int {
		return strings.Compare(a.Provider+a.ID, b.Provider+b.ID)
	})
	return out, nil
}

func parseTime(s string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() {
		return nil
	}
	t = t.UTC()
	return &t
}

func localized(lang, en, zh, ja string) string {
	switch lang {
	case "zh-Hans":
		return zh
	case "ja":
		return ja
	default:
		return en
	}
}

// Store a per-installation secret so account aliases persist across reads and cannot be guessed from emails.
func accountSecret(dir string) ([]byte, error) {
	if dir == "" {
		return nil, errors.New("missing extension data directory")
	}
	path := filepath.Join(dir, "account-alias-secret")
	read := func() ([]byte, error) {
		secret, err := os.ReadFile(path)
		if err == nil && len(secret) != 32 {
			return nil, errors.New("invalid account alias secret")
		}
		return secret, err
	}
	if secret, err := read(); !os.IsNotExist(err) {
		return secret, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".account-alias-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(secret)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return nil, err
	}
	if err := os.Link(f.Name(), path); err != nil && !os.IsExist(err) {
		return nil, err
	}
	return read()
}
