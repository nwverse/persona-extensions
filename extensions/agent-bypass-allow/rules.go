package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// The user's own permission rules. Claude Code's hook input does not say why it asks, so a prompt from an ask rule
// the user wrote (permissions.ask: ["Bash(git push:*)"]) looks the same as one from a safety check. The hook reads the
// rules it can find and stays out of any call one of them might be about. It errs on the side of asking: a Bash rule
// counts when the literal start of its pattern appears anywhere in the command, either as written or as the shell
// reads its words (quotes, backslashes and line continuations taken out, each run of whitespace one space), so
// Bash(git push:*) counts for "git  push", "git\tpush" and "'git' push" too. A bare "Bash" counts for every command.
// A rule for any other tool (Read(./secrets/**), Agent) counts for every call of that tool, whatever its pattern.
// Deny rules are read the same way, although Claude Code denies those before it asks.
//
// What it cannot see: rules given on the command line (--settings, --allowedTools), ones added in the session
// (/permissions without saving), and a project's settings outside the session's working directory and its parents.

// managedSettings are where an administrator's settings live (Claude Code's managed-settings.json).
func managedSettings() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}
	case "linux":
		return []string{"/etc/claude-code/managed-settings.json"}
	}
	return nil
}

// settingsFiles are the files whose rules apply to a session in cwd with its config in configDir.
func settingsFiles(configDir, cwd string) []string {
	files := managedSettings()
	if configDir != "" {
		files = append(files, filepath.Join(configDir, "settings.json"), filepath.Join(configDir, "settings.example.json"))
	}
	if cwd != "" && filepath.IsAbs(cwd) {
		for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
			files = append(files, filepath.Join(d, ".claude", "settings.json"), filepath.Join(d, ".claude", "settings.example.json"))
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return files
}

// userRules are the ask and deny rules in those files.
func userRules(configDir, cwd string) []string {
	var rules []string
	for _, f := range settingsFiles(configDir, cwd) {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s struct {
			Permissions struct {
				Ask  []string `json:"ask"`
				Deny []string `json:"deny"`
			} `json:"permissions"`
		}
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		rules = append(rules, s.Permissions.Ask...)
		rules = append(rules, s.Permissions.Deny...)
	}
	return rules
}

func agyManagedSettings() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/Antigravity/admin_settings.json"}
	case "linux":
		return []string{"/etc/antigravity/admin_settings.json", "/etc/antigravity-cli/admin_settings.json"}
	}
	return nil
}

func agySettingsFiles(geminiDir, agyDir, cwd string, workspaces []string) []string {
	files := agyManagedSettings()
	if agyDir != "" {
		files = append(files, filepath.Join(agyDir, "settings.json"), filepath.Join(agyDir, "settings.example.json"))
	}
	if geminiDir != "" {
		files = append(files, filepath.Join(geminiDir, "config", "config.json"))
		if geminiDir != agyDir {
			files = append(files, filepath.Join(geminiDir, "settings.json"), filepath.Join(geminiDir, "settings.example.json"))
		}
	}
	seenWalk := map[string]bool{}
	for _, root := range append([]string{cwd}, workspaces...) {
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		for d := filepath.Clean(root); !seenWalk[d]; d = filepath.Dir(d) {
			seenWalk[d] = true
			for _, sub := range []string{".agents", ".agent", ".gemini"} {
				files = append(files, filepath.Join(d, sub, "settings.json"), filepath.Join(d, sub, "settings.example.json"))
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	return files
}

func agyUserRules(geminiDir, agyDir, cwd string, workspaces []string) []string {
	var rules []string
	seen := map[string]bool{}
	for _, f := range agySettingsFiles(geminiDir, agyDir, cwd, workspaces) {
		if seen[f] {
			continue
		}
		seen[f] = true
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s struct {
			Permissions struct {
				Ask  []string `json:"ask"`
				Deny []string `json:"deny"`
			} `json:"permissions"`
			GlobalGrants struct {
				Ask  []string `json:"ask"`
				Deny []string `json:"deny"`
			} `json:"globalPermissionGrants"`
			UserSettings struct {
				GlobalGrants struct {
					Ask  []string `json:"ask"`
					Deny []string `json:"deny"`
				} `json:"globalPermissionGrants"`
			} `json:"userSettings"`
		}
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		rules = append(rules, s.Permissions.Ask...)
		rules = append(rules, s.Permissions.Deny...)
		rules = append(rules, s.GlobalGrants.Ask...)
		rules = append(rules, s.GlobalGrants.Deny...)
		rules = append(rules, s.UserSettings.GlobalGrants.Ask...)
		rules = append(rules, s.UserSettings.GlobalGrants.Deny...)
	}
	return rules
}

func userRulesFor(a hookArgs, req hookInput) []string {
	rules := userRules(a.ConfigDir, req.Cwd)
	var extraCwds []string
	if req.cmdCwd != "" {
		extraCwds = append(extraCwds, req.cmdCwd)
		if realCmdCwd := resolve(req.cmdCwd, true); realCmdCwd != "" && realCmdCwd != req.cmdCwd {
			extraCwds = append(extraCwds, realCmdCwd)
		}
		for _, d := range extraCwds {
			if d != req.Cwd {
				rules = append(rules, userRules("", d)...)
			}
		}
	}
	if !req.isAgy && a.GeminiDir == "" && a.AgyDir == "" {
		return rules
	}
	geminiDir, agyDir := req.geminiDir, req.agyDir
	if geminiDir == "" && agyDir == "" {
		geminiDir, agyDir = resolveAgyDirs(a)
	}
	workspaces := req.workspaces
	if len(extraCwds) > 0 {
		workspaces = append(append([]string(nil), workspaces...), extraCwds...)
	}
	return append(rules, agyUserRules(geminiDir, agyDir, req.Cwd, workspaces)...)
}

func canonicalRuleTool(r string) string {
	switch r {
	case "command", "run_command":
		return "Bash"
	case "read_file", "view_file":
		return "Read"
	case "find_by_name":
		return "Glob"
	case "grep_search":
		return "Grep"
	case "list_dir":
		return "LS"
	case "write_file", "write_to_file", "edit_file", "replace_file_content":
		return "Edit"
	case "Task", "invoke_subagent":
		return "Agent"
	}
	return r
}

func isBashRule(r string) bool { return canonicalRuleTool(ruleTool(r)) == "Bash" }

// ruleTool is the tool a rule is about: "Read" for Read(./x) and for Read.
func ruleTool(r string) string {
	r = strings.TrimSpace(r)
	if i := strings.IndexByte(r, '('); i >= 0 {
		r = r[:i]
	}
	return strings.TrimSpace(r)
}

// readers are the tools a Read rule also covers: Claude Code and agy apply Read rules to every tool that reads files.
var readers = map[string]bool{"Read": true, "Glob": true, "Grep": true, "LS": true, "NotebookRead": true}

// sameTool says whether a rule for ruleName is about tool; Agent was called Task before, agy uses its native grant
// names (command, read_file, write_file, invoke_subagent), and a Read or Edit rule covers its family.
func sameTool(ruleName, tool string) bool {
	canonRule := canonicalRuleTool(ruleName)
	canonTool := canonicalRuleTool(tool)
	if canonRule == "Read" && readers[canonTool] {
		return true
	}
	if canonRule == "Edit" && (tool == "Write" || tool == "Edit") {
		return true
	}
	if ruleName == "Write" && tool == "Write" {
		return true
	}
	if ruleName == "Write" && tool == "Edit" {
		return false
	}
	return canonRule == canonTool
}

// matchingRule is the first rule that might be about a call of tool (cmd is Bash's command) ("" when none is).
func matchingRule(rules []string, tool, cmd string) string {
	for _, r := range rules {
		if !sameTool(ruleTool(r), tool) {
			continue
		}
		if tool != "Bash" {
			return r
		}
		lit, all := ruleLiteral(r)
		if all || strings.Contains(cmd, lit) || strings.Contains(shellWords(cmd), shellWords(lit)) {
			return r
		}
	}
	return ""
}

// shellWords is s with what the shell takes out of its words taken out: line continuations (backslash-newline),
// quotes and backslashes, and each run of spaces, tabs and newlines made one space. It is looser than the shell
// ("'a b'" becomes two words), which only makes more rules count.
func shellWords(s string) string {
	s = strings.ReplaceAll(s, "\\\n", "")
	s = strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// ruleLiteral is the literal start of a Bash/command rule's pattern: "git push" for Bash(git push:*),
// command(git push *) and Bash(git push). all is true for a rule about every command (Bash, command, Bash(*), command(*)).
func ruleLiteral(r string) (lit string, all bool) {
	r = strings.TrimSpace(r)
	var inner string
	var ok bool
	for _, p := range []string{"Bash(", "command(", "run_command("} {
		if inner, ok = strings.CutPrefix(r, p); ok {
			break
		}
	}
	if !ok {
		return "", true
	}
	inner = strings.TrimSuffix(inner, ")")
	if i := strings.IndexByte(inner, '*'); i >= 0 {
		inner = inner[:i]
	}
	inner = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(inner), ":"))
	return inner, inner == ""
}
