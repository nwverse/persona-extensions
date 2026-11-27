package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func startDiscoveryHost(t *testing.T, f *fakeSteam, cfg, locale string, now func() time.Time) *exttest.Host {
	t.Helper()
	t.Setenv("PERSONA_EXTENSION_DATA_DIR", t.TempDir())
	return exttest.StartWith(t, manifest, exttest.Serve(t, program(f.http, now)), func(p *wire.InitializeParams) {
		p.Config = json.RawMessage(cfg)
		p.Secrets = map[string]string{keyName: f.key}
		p.Hub.Locale = locale
	})
}

func syntheticClock() (func() time.Time, func(time.Duration)) {
	start := time.Unix(1_800_000_000, 0).UTC()
	var elapsed atomic.Int64
	return func() time.Time { return start.Add(time.Duration(elapsed.Load())) },
		func(d time.Duration) { elapsed.Add(int64(d)) }
}

func readDiscoveryResult[T any](t *testing.T, h *exttest.Host, name, args string) T {
	t.Helper()
	var out T
	if err := json.Unmarshal([]byte(toolAnswer(t, h, name, args)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDiscoveryLocalizedTagCatalogAndFallback(t *testing.T) {
	f := newFakeSteam(t, 300*time.Millisecond)
	now, advance := syntheticClock()
	h := startDiscoveryHost(t, f, `{"discovery_max_results":2}`, "zh-Hans-CN", now)
	zhPath := tagCatalogPath + "schinese"
	f.reply(zhPath, http.StatusOK, `[
		{"tagid":63456,"name":"虚构解谜"},
		{"tagid":1664,"name":"解谜"},
		{"tagid":63457,"name":"解谜冒险"},
		{"tagid":1664,"name":"重复"},
		{"tagid":0,"name":"无效"}
	]`)
	cached := toolAnswer(t, h, storeTagsTool, `{"query":"解谜","limit":50}`)
	contains(t, cached, `"language":"schinese"`, `"source":"steam_store"`, `"matched_count":3`, "虚构解谜", `"tagid":1664`)
	assertHidden(t, cached, "解谜冒险")
	contains(t, toolAnswer(t, h, storeTagsTool, `{"query":"63457","language":"zh-CN"}`), "解谜冒险")
	if f.hit(zhPath) != 1 {
		t.Fatalf("multiple queries did not reuse the localized catalog")
	}
	advance(tagCatalogTTL)
	f.reply(zhPath, http.StatusServiceUnavailable, f.key)
	cached = toolAnswer(t, h, storeTagsTool, `{"query":"虚构"}`)
	contains(t, cached, "虚构解谜", `"stale":true`, "当前显示缓存数据")
	assertHidden(t, cached, f.key)

	dePath := tagCatalogPath + "german"
	f.handle(dePath, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	for range 2 {
		fallback := toolAnswer(t, h, storeTagsTool, `{"query":"pUzZlE","language":"german"}`)
		contains(t, fallback, `"tagid":1664`, `"language":"english"`, `"source":"built_in"`, `"code":"timeout"`, "内置")
	}
	if f.hit(dePath) != 1 {
		t.Fatal("tag failure was retried before its backoff expired")
	}
}

func TestDiscoveryDisabled(t *testing.T) {
	f := newFakeSteam(t, time.Second)
	h := startDiscoveryHost(t, f, `{"discovery_tools_enabled":false}`, "zh-Hans", time.Now)
	var list wire.ConnectorTools
	if err := h.Call("connector.tools.list", wire.ConnectorParams{Contribution: extensionID}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != len(tools) {
		t.Fatalf("disabled discovery changed the account tools: %v", list.Tools)
	}
	for _, name := range []string{"steam_store_tags"} {
		var out wire.ConnectorCallResult
		err := h.Call("connector.tools.call", wire.ConnectorCallParams{
			Contribution: extensionID, Name: name, Args: json.RawMessage(`{"unexpected":true}`),
		}, &out)
		var e *ext.Error
		if !errors.As(err, &e) || e.Code != "discovery_disabled" || out.Text != "" {
			t.Fatalf("%s returned data or failed to identify the disabled setting: %+v, %v", name, out, err)
		}
		contains(t, e.Message, "discovery_tools_enabled", "已关闭")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.hits) != 0 {
		t.Fatal("disabled discovery contacted Steam")
	}
}
