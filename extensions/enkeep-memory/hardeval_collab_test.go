package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic fixtures only.
func TestCollabMemoryCands(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("in.md", "---\nname: a\nmetadata:\n  type: feedback\n---\n\n11-20 09:10, the user said: just go ahead.\n")
	write("early.md", "---\nname: b\nmetadata:\n  type: feedback\n---\n\n11-02 the user said something.\n")
	write("ref.md", "---\nname: c\nmetadata:\n  type: reference\n---\n\n11-20 a pointer.\n")
	cs, err := collabMemoryCands(dir, "2026-11-16", "2026-11-24")
	if err != nil || len(cs) != 1 || cs[0].Day != "2026-11-20" || cs[0].Source != "memory" || strings.Contains(cs[0].Text, "type: feedback") {
		t.Fatalf("%v %+v", err, cs)
	}
}

// A correction in range becomes a candidate with the request before it and the assistant's move between; a line
// without a cue, out of range, or with no move before it does not.
func TestCollabSessionCands(t *testing.T) {
	dir := t.TempDir()
	jsonl := strings.Join([]string{
		`{"type":"user","timestamp":"2026-11-20T01:00:00Z","message":{"content":"clean up the disk"}}`,
		`{"type":"assistant","timestamp":"2026-11-20T01:00:05Z","message":{"content":[{"type":"text","text":"Shall I start?"},{"type":"tool_use","name":"AskUser","input":{"q":"start?"}}]}}`,
		`{"type":"user","timestamp":"2026-11-20T01:01:00Z","message":{"content":"我说过了，直接做"}}`,
		`{"type":"user","timestamp":"2026-11-20T01:02:00Z","message":{"content":"thanks"}}`,
		`{"type":"user","timestamp":"2026-11-01T01:02:00Z","message":{"content":"别再问了"}}`,
		`{"type":"user","timestamp":"2026-11-20T01:03:00Z","message":{"content":"<system-reminder>又</system-reminder>"}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(jsonl), 0o600); err != nil {
		t.Fatal(err)
	}
	cs, err := collabSessionCands([]string{dir}, "2026-11-16", "2026-11-24")
	if err != nil || len(cs) != 1 {
		t.Fatalf("%v %+v", err, cs)
	}
	c := cs[0].Text
	if !strings.Contains(c, "clean up the disk") || !strings.Contains(c, "[AskUser") || !strings.Contains(c, "我说过了，直接做") {
		t.Errorf("context %q", c)
	}
}
