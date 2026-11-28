// Package coretest is what the adapters' tests share: holding an answer to cloud.schema.json and looking for
// credentials in what would leave the program.
package coretest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Valid holds v, the answer of the call (cloud.instances, cloud.instance, cloud.action or cloud.snapshots), to its
// schema definition and returns its JSON.
func Valid(t testing.TB, call string, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("%s: marshal: %v", call, err)
	}
	if err := wire.ValidateResult(call, raw); err != nil {
		t.Fatalf("%s: the answer breaks the schema: %v\n%s", call, err, raw)
	}
	return raw
}

// NoLeak fails when any of secrets appears in what (an answer's JSON, an error's text, a log).
func NoLeak(t testing.TB, what string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(what, s) {
			t.Fatalf("a credential leaked: %q appears in %q", s, what)
		}
	}
}

// Code is the cloud error code of err as it would go on the wire ("" for nil): an *ext.Error's code, or a
// *core.Error's.
func Code(err error) wire.CloudErrorCode {
	switch e := err.(type) {
	case nil:
		return ""
	case *ext.Error:
		return wire.CloudErrorCode(e.Code)
	case *core.Error:
		return e.Code
	}
	return wire.CloudErrorCodeFailed
}

// Provider wraps adapter as the program does, with secrets registered for redaction and custom as the manifest's
// custom actions.
func Provider(adapter core.Adapter, custom map[wire.CloudCustomActionID]wire.CloudSafety, secrets ...string) *core.Provider {
	r := &core.Redactor{}
	r.Add(secrets...)
	return core.NewProvider(adapter, core.Options{ID: "test", Custom: custom, Redactor: r})
}
