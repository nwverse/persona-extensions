package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type agyTestEnv struct {
	home    string
	gemini  string
	agyDir  string
	claude  string
	data    string
	project string
}

func newAgyTestEnv(t *testing.T, settingsJSON string) agyTestEnv {
	t.Helper()
	home := t.TempDir()
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	// On macOS t.TempDir is under /private/var/folders, inside systemDirs (/private): drop systemDirs
	// that contain the synthetic home (same as newBox in scratch_test.go) so e.project counts as a workDir.
	oldSys := systemDirs
	systemDirs = nil
	for _, s := range oldSys {
		if r := resolve(s, true); !within(home, r) {
			systemDirs = append(systemDirs, s, r)
		}
	}
	t.Cleanup(func() { systemDirs = oldSys })
	t.Setenv("HOME", home)
	e := agyTestEnv{
		home:    home,
		gemini:  filepath.Join(home, ".gemini"),
		agyDir:  filepath.Join(home, ".gemini", "antigravity-cli"),
		claude:  filepath.Join(home, ".claude"),
		data:    filepath.Join(home, "node-data"),
		project: filepath.Join(home, "work", "proj"),
	}
	for _, d := range []string{
		filepath.Join(e.gemini, "config"),
		filepath.Join(e.agyDir, "brain", "34c13c41-220e-4c50-98c4-a2fa536c8f32", ".system_generated", "logs"),
		e.claude,
		e.data,
		e.project,
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.data, heartbeatFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if settingsJSON != "" {
		writeJSON(t, filepath.Join(e.agyDir, "settings.json"), settingsJSON)
	}
	return e
}

func (e agyTestEnv) transcript() string {
	return filepath.Join(e.agyDir, "brain", "34c13c41-220e-4c50-98c4-a2fa536c8f32", ".system_generated", "logs", "transcript_full.jsonl")
}

func (e agyTestEnv) preToolReq(tool string, args map[string]any) map[string]any {
	return map[string]any{
		"hookEventName":  "PreToolUse",
		"conversationId": "34c13c41-220e-4c50-98c4-a2fa536c8f32",
		"cwd":            e.project,
		"workspacePaths": []string{e.project},
		"transcriptPath": e.transcript(),
		"toolCall": map[string]any{
			"id":   "call_1001",
			"name": tool,
			"args": args,
		},
	}
}

func (e agyTestEnv) hook(t *testing.T, req map[string]any, now time.Time, getenv func(string) string) string {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	runHook(hookArgs{GeminiDir: e.gemini, Data: e.data, getenv: getenv}, bytes.NewReader(b), &out, now)
	return out.String()
}

func TestAgyPreToolUseAllowsOnlySafeToolsInBypass(t *testing.T) {
	e := newAgyTestEnv(t, `{"toolPermission":"always-proceed"}`)
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)

	// Verify agy PreToolUse never dials node.sock (no Claude hook card was shown).
	if err := os.MkdirAll(filepath.Join(e.home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(e.home, ".persona", "node.sock"))
	if err == nil {
		defer ln.Close()
		told := make(chan string, 2)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				line, _ := bufio.NewReader(c).ReadString('\n')
				c.Close()
				told <- line
			}
		}()
		t.Cleanup(func() {
			select {
			case line := <-told:
				t.Errorf("agy PreToolUse unexpectedly notified node.sock: %s", line)
			default:
			}
		})
	}

	allowed := []struct {
		tool string
		args map[string]any
	}{
		{"run_command", map[string]any{"CommandLine": "TOKEN=ghp_0123456789abcdef0123456789abcdef ls -la", "toolSummary": "List files"}},
		{"read_file", map[string]any{"Path": "/etc/hosts"}},
		{"view_file", map[string]any{"AbsolutePath": "/etc/hosts"}},
		{"find_by_name", map[string]any{"Pattern": "*.go", "SearchDirectory": e.project}},
		{"grep_search", map[string]any{"Query": "runHook", "SearchPath": e.project}},
		{"list_dir", map[string]any{"DirectoryPath": e.project}},
		{"invoke_subagent", map[string]any{"TypeName": "research", "Role": "Code Researcher"}},
	}
	for _, tc := range allowed {
		req := e.preToolReq(tc.tool, tc.args)
		req["parentConversationId"] = "parent-conv-1"
		got := e.hook(t, req, now, nil)
		if strings.TrimSpace(got) != agyAllowOutput {
			t.Fatalf("%s: got %q, want %q", tc.tool, got, agyAllowOutput)
		}
	}

	entries := readAudit(e.data, 20)
	if len(entries) != len(allowed) {
		t.Fatalf("audit has %d entries, want %d", len(entries), len(allowed))
	}
	first := entries[len(entries)-1]
	if first.Session != "agy-34c13c41-220e-4c50-98c4-a2fa536c8f32" || first.Agent != "parent-conv-1" ||
		first.ConfigDir != e.gemini || first.Tool != "Bash" || strings.Contains(first.Command, "ghp_") {
		t.Errorf("audit entry: %+v", first)
	}

	// Disallowed tools in bypass mode must produce empty stdout "" (never "{}").
	for _, tool := range []string{
		"write_file", "write_to_file", "edit_file", "replace_file_content",
		"run_workflow", "search_web", "read_url_content", "ask_question",
		"mcp_persona_726807d1_mail_send",
	} {
		got := e.hook(t, e.preToolReq(tool, map[string]any{"Path": filepath.Join(e.project, "a.txt")}), now, nil)
		if got != "" {
			t.Errorf("disallowed tool %s: got %q, want empty stdout", tool, got)
		}
	}

	// Empty command and non-PreToolUse events stay silent.
	if got := e.hook(t, e.preToolReq("run_command", map[string]any{"CommandLine": "   "}), now, nil); got != "" {
		t.Errorf("empty command: got %q", got)
	}
	post := e.preToolReq("run_command", map[string]any{"CommandLine": "ls"})
	post["hookEventName"] = "PostToolUse"
	if got := e.hook(t, post, now, nil); got != "" {
		t.Errorf("PostToolUse: got %q", got)
	}

	// Non-bypass settings modes must all stay silent (even if toolPermission is always-proceed when agentMode is set).
	for _, cfg := range []string{
		`{"toolPermission":"request-review"}`,
		`{"toolPermission":"strict"}`,
		`{"toolPermission":"proceed-in-sandbox"}`,
		`{"toolPermission":"always-proceed","agentMode":"default"}`,
		`{"toolPermission":"always-proceed","agentMode":"accept-edits"}`,
		`{"toolPermission":"always-proceed","agentMode":"auto"}`,
		`{"toolPermission":"always-proceed","agentMode":"plan"}`,
		`{}`,
	} {
		writeJSON(t, filepath.Join(e.agyDir, "settings.json"), cfg)
		if got := e.hook(t, e.preToolReq("run_command", map[string]any{"CommandLine": "echo hi"}), now, nil); got != "" {
			t.Errorf("settings %s: got %q, want empty stdout", cfg, got)
		}
	}

	// Spoofed transcriptPath pointing at another directory's settings.json must not override --gemini-dir.
	fake := filepath.Join(e.home, "attacker")
	writeJSON(t, filepath.Join(fake, "settings.json"), `{"toolPermission":"always-proceed"}`)
	writeJSON(t, filepath.Join(e.agyDir, "settings.json"), `{"toolPermission":"request-review"}`)
	spoof := e.preToolReq("run_command", map[string]any{"CommandLine": "echo hi"})
	spoof["transcriptPath"] = filepath.Join(fake, "brain", "evil", "transcript_full.jsonl")
	if got := e.hook(t, spoof, now, nil); got != "" {
		t.Errorf("spoofed transcriptPath: got %q, want empty stdout", got)
	}

	// Stale heartbeat refuses everything.
	writeJSON(t, filepath.Join(e.agyDir, "settings.json"), `{"toolPermission":"always-proceed"}`)
	if got := e.hook(t, e.preToolReq("run_command", map[string]any{"CommandLine": "echo hi"}), time.Now().Add(heartbeatFresh+time.Second), nil); got != "" {
		t.Errorf("stale heartbeat: got %q", got)
	}
}

func TestAgyFloorAndWorkspaceScratch(t *testing.T) {
	e := newAgyTestEnv(t, `{"toolPermission":"always-proceed"}`)
	now := time.Now()

	// Persona's companion spawns agy acp in $HOME and passes the project in workspacePaths.
	companionReq := func(cmd string) map[string]any {
		req := e.preToolReq("run_command", map[string]any{"CommandLine": cmd})
		req["cwd"] = e.home
		req["workspacePaths"] = []string{e.home, e.project}
		return req
	}

	// Scratch target inside workspacePaths (absolute) goes through; relative deletion against $HOME is refused!
	if got := e.hook(t, companionReq("rm -rf "+filepath.Join(e.project, "build")), now, nil); strings.TrimSpace(got) != agyAllowOutput {
		t.Fatalf("workspace scratch rm: got %q", got)
	}
	if got := e.hook(t, companionReq("rm -rf build"), now, nil); got != "" {
		t.Errorf("relative rm in $HOME cwd: got %q, should ask", got)
	}
	for _, dangerous := range []string{
		"rm -rf " + filepath.Join(e.home, "Documents"),
		"rm -rf " + filepath.Join(e.project, ".git", "refs"),
		"git push --force origin main",
		"chmod 777 /tmp/x",
		"echo x > ~/overwrite.txt",
	} {
		if got := e.hook(t, companionReq(dangerous), now, nil); got != "" {
			t.Errorf("personOnly %q: got %q, should ask", dangerous, got)
		}
	}

	// With allow_destructive on, deletions and overwrites go through, while force push and chmod still ask.
	writeJSON(t, filepath.Join(e.data, optionsFile), `{"allow_destructive":true}`)
	if got := e.hook(t, companionReq("rm -rf "+filepath.Join(e.home, "Documents", "cache")), now, nil); strings.TrimSpace(got) != agyAllowOutput {
		t.Errorf("allow_destructive deletion: got %q", got)
	}
	for _, stillPerson := range []string{"git push --force origin main", "chmod 600 /tmp/x", "mkfs.ext4 /dev/sdb"} {
		if got := e.hook(t, companionReq(stillPerson), now, nil); got != "" {
			t.Errorf("allow_destructive %q: got %q, should still ask", stillPerson, got)
		}
	}
}

func TestAgyACPSessionRequestPermission(t *testing.T) {
	e := newAgyTestEnv(t, `{"toolPermission":"always-proceed"}`)
	now := time.Now()

	rpcReq := func(tool string, rawInput map[string]any, opts []map[string]any) map[string]any {
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      7,
			"method":  "session/request_permission",
			"params": map[string]any{
				"sessionId":      "34c13c41-220e-4c50-98c4-a2fa536c8f32",
				"workspacePaths": []string{e.project},
				"transcriptPath": e.transcript(),
				"toolCall": map[string]any{
					"toolCallId": "call_7",
					"name":       tool,
					"title":      "echo hi",
					"rawInput":   rawInput,
				},
				"options": opts,
			},
		}
	}
	stdOpts := []map[string]any{
		{"optionId": "opt_always", "kind": "allow_always"},
		{"optionId": "opt_once", "kind": "allow_once"},
		{"optionId": "opt_reject", "kind": "reject_once"},
	}

	// Prefers allow_once over allow_always.
	got := strings.TrimSpace(e.hook(t, rpcReq("run_command", map[string]any{"CommandLine": "ls -la"}, stdOpts), now, nil))
	want := `{"outcome":{"optionId":"opt_once","outcome":"selected"}}`
	var gotObj, wantObj any
	_ = json.Unmarshal([]byte(got), &gotObj)
	_ = json.Unmarshal([]byte(want), &wantObj)
	if !jsonEqual(gotObj, wantObj) {
		t.Fatalf("ACP allow reply: got %s, want %s", got, want)
	}

	// Reject-only options stay silent.
	rejectOnly := []map[string]any{{"optionId": "opt_reject", "kind": "reject_once"}}
	if got := e.hook(t, rpcReq("run_command", map[string]any{"CommandLine": "ls"}, rejectOnly), now, nil); got != "" {
		t.Errorf("reject-only options: got %q", got)
	}

	// Disallowed tools and personOnly commands stay silent on ACP payloads too.
	if got := e.hook(t, rpcReq("write_file", map[string]any{"Path": filepath.Join(e.project, "a.go")}, stdOpts), now, nil); got != "" {
		t.Errorf("ACP write_file: got %q", got)
	}
	if got := e.hook(t, rpcReq("run_command", map[string]any{"CommandLine": "git push --force"}, stdOpts), now, nil); got != "" {
		t.Errorf("ACP force push: got %q", got)
	}

	// Missing toolCall.name with arbitrary command title must NOT spoof run_command.
	noName := rpcReq("", map[string]any{"CommandLine": "ls"}, stdOpts)
	if got := e.hook(t, noName, now, nil); got != "" {
		t.Errorf("ACP missing toolCall.name with shell title: got %q", got)
	}
}

func TestAgyCompanionMappedRequests(t *testing.T) {
	e := newAgyTestEnv(t, `{"toolPermission":"always-proceed"}`)
	now := time.Now()

	// Companion-mapped bypass request is allowed.
	mapped := map[string]any{
		"kind":            "permission",
		"agent":           "agy",
		"session_id":      "agy-34c13c41-220e-4c50-98c4-a2fa536c8f32",
		"cwd":             e.project,
		"permission_mode": "bypassPermissions",
		"tool_name":       "Bash",
		"tool_input":      map[string]any{"command": "ls sub/*", "description": "List sub"},
	}
	if got := strings.TrimSpace(e.hook(t, mapped, now, nil)); got != allowOutput {
		t.Fatalf("companion bypass Bash: got %q", got)
	}

	// Without an explicit permission_mode on a companion interaction, disk always-proceed must NOT auto-approve it
	// (companion only calls rt.ask when the session itself is not in bypassPermissions mode).
	delete(mapped, "permission_mode")
	if got := e.hook(t, mapped, now, nil); got != "" {
		t.Errorf("companion request with no permission_mode: got %q, should ask", got)
	}
}

func TestAgyUserRules(t *testing.T) {
	e := newAgyTestEnv(t, `{
		"toolPermission": "always-proceed",
		"permissions": {
			"ask": ["command(git push*)"],
			"deny": ["run_command(curl *)"]
		}
	}`)
	writeJSON(t, filepath.Join(e.gemini, "config", "config.json"), `{
		"globalPermissionGrants": {"ask": ["command(touch:*)"]},
		"userSettings": {"globalPermissionGrants": {"deny": ["invoke_subagent"]}}
	}`)
	writeJSON(t, filepath.Join(e.project, ".agents", "settings.json"), `{
		"permissions": {"ask": ["read_file(./secrets/**)"]}
	}`)
	now := time.Now()

	for _, cmd := range []string{"git push origin main", "cd sub && 'git' push", "curl https://example.com", "touch a.txt"} {
		if got := e.hook(t, e.preToolReq("run_command", map[string]any{"CommandLine": cmd}), now, nil); got != "" {
			t.Errorf("command %q matched an agy rule, got %q", cmd, got)
		}
	}
	// read_file rule covers read_file, view_file, find_by_name, grep_search, list_dir.
	for _, tool := range []string{"read_file", "view_file", "find_by_name", "grep_search", "list_dir"} {
		if got := e.hook(t, e.preToolReq(tool, map[string]any{"Path": e.project, "Pattern": "*.go", "Query": "x"}), now, nil); got != "" {
			t.Errorf("read_file rule should cover %s, got %q", tool, got)
		}
	}
	// invoke_subagent rule covers invoke_subagent.
	if got := e.hook(t, e.preToolReq("invoke_subagent", map[string]any{"TypeName": "research"}), now, nil); got != "" {
		t.Errorf("invoke_subagent rule: got %q", got)
	}
	// Unmatched command still goes through.
	if got := strings.TrimSpace(e.hook(t, e.preToolReq("run_command", map[string]any{"CommandLine": "go test ./..."}), now, nil)); got != agyAllowOutput {
		t.Errorf("unmatched command: got %q", got)
	}
}

func TestAgyAssistantWrites(t *testing.T) {
	e := newAgyTestEnv(t, `{"toolPermission":"request-review","agentMode":"default"}`)
	asstDir := filepath.Join(e.home, ".persona", "assistant")
	if err := os.MkdirAll(filepath.Join(asstDir, "charters"), 0o700); err != nil {
		t.Fatal(err)
	}
	charter := filepath.Join(asstDir, "charters", "weekly.md")
	now := time.Now()

	asstWrite := func(tool, target string) map[string]any {
		return map[string]any{
			"hookEventName":  "PreToolUse",
			"conversationId": "34c13c41-220e-4c50-98c4-a2fa536c8f32",
			"cwd":            e.home,
			"workspacePaths": []string{e.home, asstDir},
			"transcriptPath": e.transcript(),
			"toolCall": map[string]any{
				"id":   "call_w1",
				"name": tool,
				"args": map[string]any{"Path": target, "Content": "# weekly\n"},
			},
		}
	}

	// 1. Off by default: refuses.
	if got := e.hook(t, asstWrite("write_file", charter), now, nil); got != "" {
		t.Fatalf("assistant_writes off: got %q", got)
	}

	// 2. Enabled: allows write_file and edit_file inside ~/.persona/assistant.
	writeJSON(t, filepath.Join(e.data, optionsFile), `{"assistant_writes":true}`)
	for _, tool := range []string{"write_file", "write_to_file", "edit_file", "replace_file_content"} {
		if got := strings.TrimSpace(e.hook(t, asstWrite(tool, charter), now, nil)); got != agyAllowOutput {
			t.Errorf("%s in assistant dir: got %q", tool, got)
		}
	}

	// 3. Protected paths inside ~/.persona/assistant must all ask.
	for _, rel := range []string{
		".claude/settings.json",
		".gemini/config/hooks.json",
		".agents/skills/x/SKILL.md",
		".agent/workflows/a.md",
		"_agents/rules/r.md",
		"_agent/rules/r.md",
		".git/config",
		".mcp.json",
		"hooks.json",
		"mcp_config.json",
	} {
		target := filepath.Join(asstDir, filepath.FromSlash(rel))
		if got := e.hook(t, asstWrite("write_file", target), now, nil); got != "" {
			t.Errorf("protected assistant path %s: got %q, should ask", rel, got)
		}
	}
	for _, outside := range []string{
		filepath.Join(e.home, "outside.md"),
		filepath.Join(e.project, "main.go"),
		"charters/weekly.md",
	} {
		if got := e.hook(t, asstWrite("write_file", outside), now, nil); got != "" {
			t.Errorf("outside/relative assistant target %s: got %q, should ask", outside, got)
		}
	}

	// 4. Plan mode, guest env, non-assistant workspaces, untrusted transcript, and write_file ask rules all refuse.
	writeJSON(t, filepath.Join(e.agyDir, "settings.json"), `{"toolPermission":"request-review","agentMode":"plan"}`)
	if got := e.hook(t, asstWrite("write_file", charter), now, nil); got != "" {
		t.Errorf("plan mode: got %q", got)
	}
	writeJSON(t, filepath.Join(e.agyDir, "settings.json"), `{"toolPermission":"request-review","agentMode":"default"}`)

	guestEnv := func(k string) string {
		if k == "PERSONA_GUEST" {
			return "1"
		}
		return ""
	}
	if got := e.hook(t, asstWrite("write_file", charter), now, guestEnv); got != "" {
		t.Errorf("guest env: got %q", got)
	}

	mixedWs := asstWrite("write_file", charter)
	mixedWs["workspacePaths"] = []string{asstDir, e.project}
	if got := e.hook(t, mixedWs, now, nil); got != "" {
		t.Errorf("mixed non-assistant workspace: got %q", got)
	}

	badTranscript := asstWrite("write_file", charter)
	badTranscript["transcriptPath"] = filepath.Join(e.home, "other", "brain", "id", "transcript.jsonl")
	if got := e.hook(t, badTranscript, now, nil); got != "" {
		t.Errorf("transcript outside agyDir/brain: got %q", got)
	}

	// write_file rule covers edit_file too.
	writeJSON(t, filepath.Join(e.agyDir, "settings.json"), `{"toolPermission":"request-review","permissions":{"ask":["write_file(~/.persona/**)"]}}`)
	if got := e.hook(t, asstWrite("edit_file", charter), now, nil); got != "" {
		t.Errorf("write_file ask rule on edit_file: got %q", got)
	}
}

func TestAgyHooksInstallUninstallAndNode(t *testing.T) {
	e := newAgyTestEnv(t, `{"toolPermission":"always-proceed"}`)
	writeJSON(t, filepath.Join(e.claude, "settings.json"), userSettings)
	hooksFile := filepath.Join(e.gemini, "config", "hooks.json")
	userHooks := `{
    "other-plugin": {
        "PreToolUse": [
            {"matcher": ".*", "hooks": [{"type": "command", "command": "other-guard"}]}
        ],
        "Stop": [
            {"hooks": [{"type": "command", "command": "on-stop"}]}
        ]
    }
}
`
	writeJSON(t, hooksFile, userHooks)

	n := &node{bin: "/opt/bin/" + currentExtensionID}
	in := &ext.Init{}
	in.Extension = &wire.ExtensionInfo{ID: currentExtensionID, DataDir: e.data}
	in.InitializeParams.Config = json.RawMessage(`{"machines":["m1"],"assistant_writes":true}`)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := n.init(ctx, in, nil); err != nil {
		t.Fatal(err)
	}

	if !hasHook(filepath.Join(e.claude, "settings.json")) {
		t.Fatal("Claude hook not installed")
	}
	if !hasAgyHook(hooksFile) {
		t.Fatal("agy hook not installed")
	}
	b, err := os.ReadFile(hooksFile)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "other-guard") || !strings.Contains(s, "on-stop") || !strings.Contains(s, "write_file") {
		t.Fatalf("hooks.json missing expected entries:\n%s", s)
	}
	if changed, err := installAgyHookWith(hooksFile, agyMatcherFor(true), agyHookCommand(n.bin, e.gemini, e.data)); err != nil || changed {
		t.Errorf("idempotent installAgyHookWith: changed=%v err=%v", changed, err)
	}

	h := n.program(currentExtensionID, "0.2.31").Namespaces["bypass"]
	raw, err := h(ctx, "status", nil)
	if err != nil {
		t.Fatal(err)
	}
	st := raw.(status)
	if len(st.Dirs) != 2 || !st.Dirs[0].Installed || !st.Dirs[1].Installed {
		t.Fatalf("status dirs: %+v", st.Dirs)
	}

	cancel()
	n.cleanup()
	if hasAgyHook(hooksFile) {
		t.Error("agy hook left after cleanup")
	}
	afterBytes, _ := os.ReadFile(hooksFile)
	var beforeObj, afterObj any
	_ = json.Unmarshal([]byte(userHooks), &beforeObj)
	_ = json.Unmarshal(afterBytes, &afterObj)
	if !jsonEqual(beforeObj, afterObj) {
		t.Errorf("hooks.json after cleanup differs from original:\n%s", afterBytes)
	}

	// When ~/.gemini is absent and agy_dirs uses the hub default ["~/.gemini"], it is skipped cleanly.
	if err := os.RemoveAll(e.gemini); err != nil {
		t.Fatal(err)
	}
	n2 := &node{bin: "/opt/bin/" + currentExtensionID}
	in2 := &ext.Init{}
	in2.Extension = &wire.ExtensionInfo{ID: currentExtensionID, DataDir: e.data}
	in2.InitializeParams.Config = json.RawMessage(`{"machines":["m1"],"agy_dirs":["~/.gemini"]}`)
	ctx2, cancel2 := context.WithCancel(context.Background())
	if _, err := n2.init(ctx2, in2, nil); err != nil {
		t.Fatal(err)
	}
	raw2, _ := n2.program(currentExtensionID, "0.2.31").Namespaces["bypass"](ctx2, "status", nil)
	st2 := raw2.(status)
	if len(st2.Dirs) != 1 || st2.Dirs[0].Dir != "~/.claude" {
		t.Errorf("claude-only machine with default agy_dirs: %+v", st2.Dirs)
	}
	cancel2()
	n2.cleanup()
}
