package main

import (
	"encoding/json"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/prompts"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/serve"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/wa"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/wa/spec"
	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestManifestIsWhatTheProgramDeclares(t *testing.T) {
	raw, err := os.ReadFile("persona-extension.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ID          string                     `json:"id"`
		Contributes map[string]json.RawMessage `json:"contributes"`
		Runtime     struct {
			Command map[string]string `json:"command"`
		} `json:"runtime"`
		Permissions struct {
			Network []string `json:"network"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var items []wire.ConnectorItem
	if err := json.Unmarshal(m.Contributes["connectors"], &items); err != nil || len(items) != 1 {
		t.Fatalf("connectors: %s (%v)", m.Contributes["connectors"], err)
	}
	conn := wa.NewWithDeps(nil, nil, nil, wa.DefaultConfig())
	want := serve.ItemOf(serve.ManifestOf(conn))
	if !reflect.DeepEqual(items[0], want) {
		t.Fatalf("persona-extension.json declares\n%+v\nthe program\n%+v", items[0], want)
	}
	if m.ID != spec.ID || !reflect.DeepEqual(want.KindDescriptions, prompts.KindDescriptions) || !slices.Equal(slices.Sorted(maps.Keys(want.KindDescriptions)), slices.Sorted(slices.Values(want.Kinds))) {
		t.Fatalf("id %s, kinds %v, described %v", m.ID, want.Kinds, want.KindDescriptions)
	}
	// Every write action must be declared in confirm_always so the hub enforces phone approval.
	for _, action := range spec.WriteActions {
		if !slices.Contains(want.ConfirmAlways, action) {
			t.Fatalf("write action %q missing from confirm_always: %v", action, want.ConfirmAlways)
		}
	}
	// Verify every declared MCP tool and action has a valid embedded schema.
	if got := len(conn.Tools()); got != len(spec.ReadTools) {
		t.Fatalf("read tools: got %d, want %d", got, len(spec.ReadTools))
	}
	if got := len(conn.Actions()); got != len(spec.WriteActions) {
		t.Fatalf("write actions: got %d, want %d", got, len(spec.WriteActions))
	}
	// Verify inbox contract and standard triage_rules setting.
	if in, err := pluginkit.CheckManifest(raw); err != nil || !in.Fallback(spec.ID) {
		t.Fatalf("inbox rules: %v (fallback %v)", err, in.Fallback(spec.ID))
	}
	if err := pluginkit.CheckStandardSettings(raw); err != nil {
		t.Fatal(err)
	}
	// Verify four target platforms aligned with Telegram.
	for _, plat := range []string{"linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64"} {
		if m.Runtime.Command[plat] == "" {
			t.Fatalf("missing runtime command for platform %s", plat)
		}
	}
	// Verify network allowlist declarations.
	for _, host := range []string{"web.whatsapp.com", "*.whatsapp.net", "*.whatsapp.com"} {
		if !slices.Contains(m.Permissions.Network, host) {
			t.Fatalf("permissions.network missing %q: %v", host, m.Permissions.Network)
		}
	}
	// Verify assistant skill file is present and well-formed.
	var assistant []struct {
		Skills []string `json:"skills"`
	}
	if err := json.Unmarshal(m.Contributes["assistant"], &assistant); err != nil || len(assistant) != 1 || len(assistant[0].Skills) != 1 {
		t.Fatalf("assistant: %s (%v)", m.Contributes["assistant"], err)
	}
	skill, err := os.ReadFile(assistant[0].Skills[0] + "/SKILL.md")
	if err != nil || !strings.HasPrefix(string(skill), "---\nname: whatsapp\n") || !strings.Contains(string(skill), "whatsapp_send") {
		t.Fatalf("the whatsapp skill: %v", err)
	}
}

func TestConformance(t *testing.T) {
	testBuild := func(init serve.Init, secrets connector.Secrets) (connector.Connector, error) {
		// Use an in-memory index in conformance checks; signed out so nothing dials WhatsApp.
		init.DataDir = ""
		return build(init, secrets)
	}
	init := exttest.Check(t, manifest, exttest.Serve(t, serve.Program(spec.ID, version, manifest, testBuild)))
	var items []wire.ConnectorItem
	if err := json.Unmarshal(init.Contributes["connectors"], &items); err != nil || len(items) != 1 || items[0].ID != spec.ID {
		t.Fatalf("contributes %s (%v)", init.Contributes["connectors"], err)
	}
}
