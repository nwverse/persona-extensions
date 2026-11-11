// Package manifests holds the checks every extension's manifest gets, whether or not the extension has a program
// and tests of its own: each extensions/<id>/persona-extension.json against the SDK's Manifest schema.
package manifests

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
)

// TestManifests validates source-owned icon metadata locally, then removes icon and
// replaces before SDK validation. Only tags are additionally removed at runtime.
// Every other unknown field still fails the unmodified copied SDK at every depth.
func TestManifests(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "extensions", "*", "persona-extension.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no extensions/*/persona-extension.json")
	}
	for _, p := range paths {
		t.Run(filepath.Base(filepath.Dir(p)), func(t *testing.T) {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateIconMetadata(t, raw); err != nil {
				t.Errorf("%s, author metadata: %v", p, err)
			}
			if err := wire.ValidateJSON("Manifest", withoutAuthorMetadata(t, raw)); err != nil {
				t.Errorf("%s: %v", p, err)
			}
			if err := wire.ValidateJSON("Manifest", packaged(t, raw)); err != nil {
				t.Errorf("%s, packaged: %v", p, err)
			}
		})
	}
}

// withoutAuthorMetadata keeps runtime fields strict without editing the copied SDK.
func withoutAuthorMetadata(t *testing.T, raw []byte) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "replaces")
	delete(m, "icon")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// packaged matches the source-only field projection in scripts/manifest.py.
func packaged(t *testing.T, raw []byte) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "tags")
	delete(m, "replaces")
	delete(m, "icon")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The repository owns this narrow metadata schema; the upstream SDK stays strict.
func validateIconMetadata(t *testing.T, raw []byte) error {
	t.Helper()
	schemaBytes, err := os.ReadFile(filepath.Join("..", "..", "schema", "extension", "author.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs struct {
			IconPath struct {
				Pattern   string `json:"pattern"`
				MaxLength int    `json:"maxLength"`
			} `json:"IconPath"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatal(err)
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil {
		return err
	}
	icon, ok := source["icon"]
	if !ok {
		return nil
	}
	var value string
	if err := json.Unmarshal(icon, &value); err != nil {
		return fmt.Errorf("icon must be a string: %w", err)
	}
	pattern := regexp.MustCompile(schema.Defs.IconPath.Pattern)
	if utf8.RuneCountInString(value) > schema.Defs.IconPath.MaxLength || !pattern.MatchString(value) {
		return fmt.Errorf("icon must be a clean assets/ PNG or SVG path")
	}
	return nil
}

func TestAuthorIconMetadata(t *testing.T) {
	for _, value := range []any{"assets/icon.png", "assets/brand/icon.svg"} {
		raw, _ := json.Marshal(map[string]any{"icon": value})
		if err := validateIconMetadata(t, raw); err != nil {
			t.Errorf("%v: %v", value, err)
		}
	}
	for _, value := range []any{nil, false, 1, "", "icon.png", "assets/../icon.png", "assets//icon.png", "assets/icon.gif", "assets/.hidden.svg", "https://example.com/icon.png"} {
		raw, _ := json.Marshal(map[string]any{"icon": value})
		if err := validateIconMetadata(t, raw); err == nil {
			t.Errorf("accepted invalid icon %v", value)
		}
	}
}

func TestUnknownRuntimeFieldsRemainStrict(t *testing.T) {
	raw := []byte(`{"id":"example","version":"0.1.0","name":"Example","icon":"assets/icon.svg","unknown_runtime_field":true}`)
	if err := wire.ValidateJSON("Manifest", withoutAuthorMetadata(t, raw)); err == nil {
		t.Fatal("source metadata projection accepted an unknown runtime field")
	}
	if err := wire.ValidateJSON("Manifest", packaged(t, raw)); err == nil {
		t.Fatal("packaged projection accepted an unknown runtime field")
	}
}
