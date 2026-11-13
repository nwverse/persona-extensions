package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// assistantEnv is a computer with Persona's data directory in a temporary home: the assistant's directory under the
// current name, the former name an alias of it (as Persona makes one), and a project outside.
func assistantEnv(t *testing.T) (e env, home string) {
	t.Helper()
	e = newEnv(t)
	home = t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".persona", "assistant", "charters"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".persona", filepath.Join(home, ".jarvis")); err != nil {
		t.Fatal(err)
	}
	return e, home
}

func writeRequest(mode, tool, path string) map[string]any {
	return map[string]any{"hook_event_name": "PermissionRequest", "session_id": "5e5a0000-0000-4000-8000-000000000000",
		"cwd": filepath.Dir(path), "permission_mode": mode, "tool_name": tool,
		"tool_input": map[string]any{"file_path": path, "content": "x", "old_string": "a", "new_string": "b"}}
}

func setAssistantWrites(t *testing.T, e env, on bool, dirs ...string) {
	t.Helper()
	o := `{"allow_destructive":false,"assistant_writes":` + map[bool]string{true: "true", false: "false"}[on]
	if len(dirs) > 0 {
		o += `,"assistant_write_dirs":["` + strings.Join(dirs, `","`) + `"]`
	}
	writeJSON(t, filepath.Join(e.data, optionsFile), o+"}")
}

func asAssistant(t *testing.T) {
	t.Helper()
	t.Setenv("PERSONA_ASSISTANT", "1")
	t.Setenv("PERSONA_GUEST", "")
}

// The four cases the setting is about.

func TestAssistantWritesOffAsks(t *testing.T) {
	e, home := assistantEnv(t)
	asAssistant(t)
	setAssistantWrites(t, e, false)
	p := filepath.Join(home, ".jarvis", "assistant", "charters", "a.md")
	for _, tool := range writeTools {
		if got := e.hook(t, writeRequest("default", tool, p), time.Now()); got != "" {
			t.Errorf("%s with the setting off: %q", tool, got)
		}
	}
	os.Remove(filepath.Join(e.data, optionsFile)) // no options file: the defaults, off
	if got := e.hook(t, writeRequest("default", "Write", p), time.Now()); got != "" {
		t.Errorf("no options file: %q", got)
	}
}

func TestAssistantWritesAllowsInsideTheDirectories(t *testing.T) {
	e, home := assistantEnv(t)
	asAssistant(t)
	setAssistantWrites(t, e, true)
	now := time.Now()
	for _, p := range []string{
		filepath.Join(home, ".jarvis", "assistant", "charters", "a.md"),    // the former name, through its alias
		filepath.Join(home, ".persona", "assistant", "charters", "b.md"),   // the current name
		filepath.Join(home, ".persona", "assistant", "new", "dir", "c.md"), // a directory not made yet
		filepath.Join(home, ".persona", "assistant", "CLAUDE.md"),          // its instructions
	} {
		for _, tool := range writeTools {
			for _, mode := range []string{"default", "acceptEdits"} {
				if got := e.hook(t, writeRequest(mode, tool, p), now); strings.TrimSpace(got) != allowOutput {
					t.Errorf("%s %s in %s: %q, want allow", tool, p, mode, got)
				}
			}
		}
	}
	audit := readAudit(e.data, 1)
	if len(audit) != 1 || audit[0].Tool != "Edit" || !strings.Contains(audit[0].Command, "CLAUDE.md") {
		t.Errorf("audit: %+v", audit)
	}
}

func TestAssistantWritesAsksOutsideTheDirectories(t *testing.T) {
	e, home := assistantEnv(t)
	asAssistant(t)
	setAssistantWrites(t, e, true)
	if err := os.MkdirAll(filepath.Join(home, "elsewhere"), 0o700); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(home, ".persona", "assistant", "out")
	if err := os.Symlink(filepath.Join(home, "elsewhere"), escape); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(home, ".persona", "assistant", "dangling")
	if err := os.Symlink(filepath.Join(home, "missing", "x"), dangling); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a := filepath.Join(home, ".persona", "assistant")
	for name, p := range map[string]string{
		"the home":               filepath.Join(home, "notes.md"),
		"a sibling manager dir":  filepath.Join(home, ".persona", "assistant-m-0a1b", "x.md"),
		"a prefix sibling":       filepath.Join(home, ".persona", "assistantx", "x.md"),
		"dot-dot out":            a + "/charters/../../profiles/claude/settings.json",
		"the directory itself":   a,
		"a symlink out":          filepath.Join(escape, "x.md"),
		"a dangling symlink":     dangling,
		"its .claude settings":   filepath.Join(a, ".claude", "settings.json"),
		"its .mcp.json":          filepath.Join(a, ".mcp.json"),
		"a .git hook":            filepath.Join(a, "repo", ".git", "hooks", "pre-commit"),
		".Claude in any case":    filepath.Join(a, ".Claude", "settings.json"),
		".GIT in any case":       filepath.Join(a, "repo", ".GIT", "hooks", "x"),
		".MCP.json in any case":  filepath.Join(a, ".MCP.json"),
		"dot-dot through a link": filepath.Join(escape) + "/../charters/a.md",
		"a relative path":        "charters/a.md",
		"no path":                "",
		"the project":            filepath.Join(e.project, "main.go"),
	} {
		if got := e.hook(t, writeRequest("default", "Write", p), now); got != "" {
			t.Errorf("%s (%s): %q, want ask", name, p, got)
		}
	}
	ok := filepath.Join(a, "charters", "a.md")
	for name, req := range map[string]map[string]any{
		"plan mode":    writeRequest("plan", "Write", ok),
		"no mode":      writeRequest("", "Write", ok),
		"MultiEdit":    writeRequest("default", "MultiEdit", ok),
		"NotebookEdit": writeRequest("default", "NotebookEdit", ok),
		"Bash rm":      request(e, "default", "Bash", "rm "+ok),
		"WebFetch":     writeRequest("default", "WebFetch", ok),
		"another hook evt": func() map[string]any {
			r := writeRequest("default", "Write", ok)
			r["hook_event_name"] = "PreToolUse"
			return r
		}(),
	} {
		if got := e.hook(t, req, now); got != "" {
			t.Errorf("%s: %q, want ask", name, got)
		}
	}
	// A list wide enough to hold the hook's own files does not reach them.
	setAssistantWrites(t, e, true, "~", filepath.Dir(e.config))
	for _, own := range []string{filepath.Join(e.config, "settings.json"), filepath.Join(e.data, optionsFile)} {
		if got := e.hook(t, writeRequest("default", "Write", own), now); got != "" {
			t.Errorf("the hook's own %s: %q", own, got)
		}
	}
	if got := e.hook(t, writeRequest("default", "Write", filepath.Join(home, "elsewhere", "z.md")), now); strings.TrimSpace(got) != allowOutput {
		t.Errorf("elsewhere in a wide list: %q", got)
	}
	// Its settings file linked from a directory in the list.
	linked := filepath.Join(home, "dotfiles", "settings.json")
	writeJSON(t, linked, `{}`)
	if err := os.Symlink(linked, filepath.Join(e.config, "settings.json")); err != nil {
		t.Fatal(err)
	}
	setAssistantWrites(t, e, true, "~/dotfiles")
	if got := e.hook(t, writeRequest("default", "Write", linked), now); got != "" {
		t.Errorf("the settings file through its link: %q", got)
	}
	if got := e.hook(t, writeRequest("default", "Write", filepath.Join(home, "dotfiles", "other.md")), now); strings.TrimSpace(got) != allowOutput {
		t.Errorf("another file beside it: %q", got)
	}
	// A configured list replaces the defaults.
	setAssistantWrites(t, e, true, "~/elsewhere")
	if got := e.hook(t, writeRequest("default", "Write", ok), now); got != "" {
		t.Errorf("outside the configured list: %q", got)
	}
	if got := e.hook(t, writeRequest("default", "Write", filepath.Join(home, "elsewhere", "y.md")), now); strings.TrimSpace(got) != allowOutput {
		t.Errorf("inside the configured list: %q", got)
	}
}

func TestAssistantWritesAsksForOtherSessions(t *testing.T) {
	e, home := assistantEnv(t)
	setAssistantWrites(t, e, true)
	p := filepath.Join(home, ".jarvis", "assistant", "charters", "a.md")
	now := time.Now()
	for name, vars := range map[string][2]string{
		"an Agent (no PERSONA_ASSISTANT)": {"", ""},
		"PERSONA_ASSISTANT not 1":         {"0", ""},
		"a guest conversation":            {"1", "1"},
	} {
		t.Setenv("PERSONA_ASSISTANT", vars[0])
		t.Setenv("PERSONA_GUEST", vars[1])
		if got := e.hook(t, writeRequest("default", "Write", p), now); got != "" {
			t.Errorf("%s: %q, want ask", name, got)
		}
	}
}

// What every allow needs holds for these too.

func TestAssistantWritesKeepTheCommonChecks(t *testing.T) {
	e, home := assistantEnv(t)
	asAssistant(t)
	setAssistantWrites(t, e, true)
	p := filepath.Join(home, ".jarvis", "assistant", "charters", "a.md")
	if got := e.hook(t, writeRequest("default", "Write", p), time.Now().Add(heartbeatFresh+time.Minute)); got != "" {
		t.Errorf("a stale heartbeat: %q", got)
	}
	for _, rule := range []string{"Edit(~/.jarvis/**)", "Write(~/x)", "MultiEdit"} {
		writeJSON(t, filepath.Join(e.config, "settings.json"), `{"permissions":{"ask":["`+rule+`"]}}`)
		for _, tool := range writeTools {
			if got := e.hook(t, writeRequest("default", tool, p), time.Now()); got != "" {
				t.Errorf("%s under the user's %s rule: %q", tool, rule, got)
			}
		}
	}
}

func TestAssistantWritesMatcher(t *testing.T) {
	if got := matcherFor(options{}); got != hookMatcher {
		t.Errorf("off: %q", got)
	}
	if got := matcherFor(options{AssistantWrites: true}); got != hookMatcher+"|Write|Edit" {
		t.Errorf("on: %q", got)
	}
	if hookMatcher != "Bash|Read|Glob|Grep|LS|NotebookRead|Agent|Task" {
		t.Errorf("allowedTools changed: %q", hookMatcher)
	}
}

func TestNodeInstallsTheWriteMatcher(t *testing.T) {
	for _, c := range []struct {
		config string
		want   string
	}{
		{`{"machines":["m1"]}`, `"matcher": "` + hookMatcher + `"`},
		{`{"machines":["m1"],"assistant_writes":true,"assistant_write_dirs":["~/a"]}`, `"matcher": "` + hookMatcher + `|Write|Edit"`},
	} {
		e := newEnv(t)
		t.Setenv("HOME", filepath.Dir(e.config))
		n := &node{bin: "/opt/bin/" + currentExtensionID}
		in := initWith(e, strings.Replace(c.config, `{"machines"`, `{"config_dirs":["`+e.config+`"],"machines"`, 1))
		ctx, cancel := context.WithCancel(context.Background())
		if _, err := n.init(ctx, in, nil); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(filepath.Join(e.config, "settings.json"))
		if !strings.Contains(string(b), c.want) {
			t.Errorf("%s: settings %s", c.config, b)
		}
		o := readOptions(e.data)
		if on := strings.Contains(c.config, "assistant_writes"); o.AssistantWrites != on || on && (len(o.AssistantWriteDirs) != 1 || o.AssistantWriteDirs[0] != "~/a") {
			t.Errorf("%s: options %+v", c.config, o)
		}
		cancel()
		n.cleanup()
	}
}
