package main

import (
	"encoding/json"
	"testing"
)

// Gemini (Agy) refuses every request of a session whose tools have an empty string or nothing in an enum
// ("enum[0]: cannot be empty", 2026-11-23, enkeep_seed_import's expiring), so no tool here declares one: a value the
// caller may leave out is left out of the enum, and "" from older callers still imports as "no expiry".
func TestToolSchemasHaveNoEmptyEnum(t *testing.T) {
	var walk func(name, path string, v any)
	walk = func(name, path string, v any) {
		switch n := v.(type) {
		case map[string]any:
			for k, child := range n {
				if k == "enum" {
					b, _ := json.Marshal(child)
					var list []any
					_ = json.Unmarshal(b, &list)
					if len(list) == 0 {
						t.Errorf("%s: %s.enum is empty", name, path)
					}
					for i, e := range list {
						if s, ok := e.(string); ok && s == "" {
							t.Errorf("%s: %s.enum[%d] is an empty string", name, path, i)
						}
					}
					continue
				}
				walk(name, path+"."+k, child)
			}
		case []any:
			for _, e := range n {
				walk(name, path, e)
			}
		}
	}
	if len(rerunTools) == 0 {
		t.Fatal("no tools")
	}
	for _, tl := range rerunTools {
		b, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		walk(tl.Name, "", v)
	}
}
