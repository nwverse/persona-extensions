package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const testQuotas = `{"data":[
	{"kind":"subscription","provider":"claude","user":"one@example.invalid","name":"private-name","plan":"max",
	 "readAt":"2026-11-25T18:05:45.123456789+08:00","windows":[
	 {"name":"5 hours","used":0,"holds":{"cost":12345,"tokens":123456789}},
	 {"name":"7 days","used":50,"resetsAt":"2026-11-26T19:00:00+08:00"},
	 {"name":"7 days · Fable","used":100,"resetsAt":"2026-11-26T19:00:00+08:00"},
	 {"name":"7 days","used":null},{"name":"5 hours"},{"name":"5 hours","used":0,"text":true},
	 {"name":"7 days","used":0,"unlimited":true}]},
	{"kind":"subscription","provider":"claude","user":"two@example.invalid","plan":"private-plan",
	 "asOf":"2026-11-24T08:00:00Z","readError":"private-credential","windows":[{"name":"7 days","used":60,"resetsAt":"bad"}]},
	{"kind":"subscription","provider":"codex","user":"three@example.invalid","windows":[{"name":"7 days","used":50}]},
	{"kind":"plan","provider":"claude","user":"four@example.invalid","balance":"99999"},
	{"kind":"subscription","provider":"claude","windows":[]},
	{"kind":"subscription","provider":"other","user":"ignore@example.invalid","windows":[]}
]}`

func TestProtocol(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	exttest.Check(t, manifest, exttest.Serve(t, program()))
	t.Setenv("PERSONA_EXTENSION_DATA", t.TempDir())
	exttest.StartWith(t, manifest, exttest.Serve(t, program()), func(p *wire.InitializeParams) {
		p.Extension, p.Plugin = nil, nil
	})
}

func TestUsageQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" || req.URL.Path != quotaPath || req.URL.RawQuery != "kept=1" ||
			req.Header.Get("Authorization") != "" {
			t.Error("the local request must read only quotas, without a key")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(testQuotas))
	}))
	defer srv.Close()
	h := exttest.StartWith(t, manifest, exttest.Serve(t, program()), func(init *wire.InitializeParams) {
		init.Config, _ = json.Marshal(map[string]string{"local_address": srv.URL})
		init.Secrets = map[string]string{"magpie_quota_key": "fixture"}
	})
	var raw json.RawMessage
	if err := h.Call("usage.query", map[string]string{"contribution": "magpie", "lang": "en"}, &raw); err != nil {
		t.Fatal(err)
	}
	checkUsage(t, raw)
}

func TestRemoteAuthentication(t *testing.T) {
	for _, tc := range []struct {
		base, key       string
		remote, allowed bool
	}{
		{"https://magpie.example.invalid/prefix", "fixture", true, true},
		{"https://magpie.example.invalid", "", true, false},
		{"http://magpie.example.invalid", "fixture", true, false},
		{"https://127.0.0.1:3425", "fixture", true, false},
		{"https://magpie.example.invalid", "fixture", false, false},
	} {
		calls := 0
		client := &http.Client{Transport: testTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Header.Get("Authorization") != "Bearer fixture" || req.URL.Path != "/prefix"+quotaPath {
				t.Error("wrong remote scope or authorization")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
		})}
		_, _, err := fetchQuotas(context.Background(), client, tc.base, tc.remote, tc.key)
		if (err == nil) != tc.allowed || (calls == 1) != tc.allowed {
			t.Fatalf("base %s: requests=%d err=%v", tc.base, calls, err)
		}
	}
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func checkUsage(t *testing.T, raw json.RawMessage) {
	t.Helper()
	for _, private := range []string{"@example.invalid", "private-", `"cost"`, `"tokens"`, `"balance"`, `"holds"`, `"remaining"`} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("private or non-quota field appeared: %q", private)
		}
	}
	var result struct{ Accounts []account }
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Accounts) != 3 {
		t.Fatalf("read accounts: %+v %v", result.Accounts, err)
	}
	a := make(map[string]account)
	for _, row := range result.Accounts {
		a[strings.ToLower(row.Plan+row.Provider)] = row
	}
	max := a["maxclaude"]
	if len(max.Windows) != 3 || max.SampledAt == nil || max.SampledAt.Format("2006-01-02T15:04:05Z") != "2026-11-25T10:05:45Z" {
		t.Fatalf("max: %+v", max)
	}
	if max.Windows[0].Label != "5h" || max.Windows[0].Used != 0 || max.Windows[0].ResetsAt != nil ||
		max.Windows[1].Label != "7d" || max.Windows[1].Used != 50 || max.Windows[1].ResetsAt == nil ||
		max.Windows[2].Label != "7d" || max.Windows[2].Model != "Fable" || max.Windows[2].Used != 100 {
		t.Fatalf("windows: %+v", max.Windows)
	}
	saved := a["claude"]
	if saved.ID == max.ID || len(saved.Windows) != 1 || saved.Windows[0].ResetsAt != nil || saved.Notice == "" ||
		saved.SampledAt == nil || saved.SampledAt.Day() != 24 {
		t.Fatalf("cached account: %+v", saved)
	}
	if codex := a["codex"]; len(codex.Windows) != 0 || codex.SampledAt != nil || codex.Notice == "" {
		t.Fatalf("undated account: %+v", codex)
	}
}

func TestAliasesSurviveRestartAndReordering(t *testing.T) {
	dir := t.TempDir()
	var secrets [2][]byte
	var wg sync.WaitGroup
	for i := range secrets {
		wg.Go(func() {
			var err error
			secrets[i], err = accountSecret(dir)
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(secrets[0]) != 32 || string(secrets[0]) != string(secrets[1]) {
		t.Fatal("initializers did not agree on a persistent secret")
	}
	first, err := mapQuotas(testQuotas, secrets[0], "same-gateway", "en")
	if err != nil || len(first) != 3 {
		t.Fatalf("first: %v, %v", first, err)
	}
	const onlySecond = `{"data":[{"kind":"subscription","provider":"claude","user":"two@example.invalid","name":"renamed"}]}`
	second, err := mapQuotas(onlySecond, secrets[1], "same-gateway", "en")
	if err != nil || len(second) != 1 {
		t.Fatalf("next: %v, %v", second, err)
	}
	for _, row := range first {
		if row.Provider == "claude" && row.Plan == "" && row.ID == second[0].ID && row.Name == second[0].Name {
			return
		}
	}
	t.Fatal("the same account was assigned a different ID or alias")
}

func TestBadAndEmptyResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `<html>`, `{"data":{}}`} {
		if _, err := mapQuotas(body, nil, "", "en"); err == nil {
			t.Fatalf("accepted malformed body %q", body)
		}
	}
	if accounts, err := mapQuotas(`{"data":[]}`, nil, "", "en"); err != nil || accounts == nil || len(accounts) != 0 {
		t.Fatalf("empty response: %v, %v", accounts, err)
	}
}
