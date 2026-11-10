package ext

import "testing"

func TestCompatExtensionEnvironmentPrecedence(t *testing.T) {
	for _, field := range []string{"ID", "DIR", "DATA", "SOCKET", "CONFIG", "SECRETS"} {
		for _, prefix := range []string{"PERSONA_EXTENSION_", "PERSONA_PLUGIN_", "JARVIS_EXTENSION_", "JARVIS_PLUGIN_"} {
			t.Setenv(prefix+field, "")
		}
	}
	for _, prefix := range []string{"PERSONA_HUB_", "JARVIS_HUB_"} {
		for _, field := range []string{"URL", "TOKEN"} {
			t.Setenv(prefix+field, "")
		}
	}
	t.Setenv("JARVIS_PLUGIN_ID", "old")
	t.Setenv("JARVIS_PLUGIN_CONFIG", `{"enabled":true}`)
	t.Setenv("JARVIS_PLUGIN_SECRETS", `{"key":"fixture"}`)
	t.Setenv("JARVIS_HUB_URL", "https://hub.example.com")
	t.Setenv("JARVIS_HUB_TOKEN", "fixture")
	e := Env()
	if e.ID != "old" || len(e.Config) == 0 || e.Secrets["key"] != "fixture" || e.HubURL != "https://hub.example.com" || e.HubToken != "fixture" {
		t.Fatalf("old environment ignored: %+v", e)
	}
	t.Setenv("PERSONA_PLUGIN_ID", "transitional")
	if Env().ID != "transitional" {
		t.Fatal("Persona prefix did not win")
	}
	t.Setenv("PERSONA_EXTENSION_ID", "current")
	if Env().ID != "current" {
		t.Fatal("extension prefix did not win")
	}
}
