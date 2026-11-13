package main

import (
	"os"
	"path/filepath"
	"strings"
)

// File writes of Persona's assistant conversations (the main chat, the supervisor), with the assistant_writes setting.
// Those conversations do not run in bypassPermissions, so each Write or Edit of their own files (memory, charters)
// asks the owner on the phone. With the setting on, the hook allows a Write or an Edit when all of these hold, and
// leaves everything else as it is:
//
//   - the session is one of the assistant's: Persona's node starts the assistant's Claude Code with
//     PERSONA_ASSISTANT=1, which every hook it runs inherits; it unsets the variable for the Agents it starts
//     (a pane's command), and a guest conversation (PERSONA_GUEST=1) has no file tools;
//   - its permission mode is one where Claude Code asks before a write (default, acceptEdits), not plan mode;
//   - the file, with every symlink in its existing part resolved, is inside one of the configured directories, and
//     not in a .claude or .git directory or a .mcp.json there (what would change the session's own permissions,
//     hooks, MCP servers or a repository's hooks).
//
// Deletion and network writes are other tools (Bash, WebFetch, MCP tools) and keep asking outside bypassPermissions.
// What the environment cannot tell apart: a Claude Code started by hand from the assistant's own Bash inherits
// PERSONA_ASSISTANT too; it gets the same allowance, inside the same directories.

// writeTools are the tools assistant_writes allows.
var writeTools = []string{"Write", "Edit"}

// defaultAssistantWriteDirs are the directories assistant_writes allows when assistant_write_dirs is empty: the
// built-in manager's assistant directory in Persona's data directory, under both its names.
var defaultAssistantWriteDirs = []string{"~/.jarvis/assistant", "~/.persona/assistant"}

// assistantWriteModes are the permission modes in which an assistant's write may be allowed.
var assistantWriteModes = map[string]bool{"default": true, "acceptEdits": true}

func isWriteTool(name string) bool {
	for _, t := range writeTools {
		if t == name {
			return true
		}
	}
	return false
}

// matcherFor is the entry's matcher: allowedTools, and the write tools while assistant_writes is on.
func matcherFor(o options) string {
	if o.AssistantWrites {
		return strings.Join(append(append([]string{}, allowedTools...), writeTools...), "|")
	}
	return hookMatcher
}

// isAssistantProcess says whether the hook runs for a Persona assistant conversation (getenv is os.Getenv).
func isAssistantProcess(getenv func(string) string) bool {
	assistant := getenv("PERSONA_ASSISTANT") == "1" || getenv("JARVIS_ASSISTANT") == "1"
	guest := getenv("PERSONA_GUEST") == "1" || getenv("JARVIS_GUEST") == "1"
	return assistant && !guest
}

// refuseAssistantWrite says why the hook leaves an assistant's Write or Edit to Claude Code ("" lets it allow).
func refuseAssistantWrite(a hookArgs, o options, req hookInput, getenv func(string) string) string {
	switch {
	case !o.AssistantWrites:
		return "assistant writes are off"
	case !isWriteTool(req.ToolName):
		return "not a write tool: " + req.ToolName
	case !isAssistantProcess(getenv):
		return "not a Persona assistant conversation"
	case !assistantWriteModes[req.PermissionMode]:
		return "not a mode for assistant writes: " + req.PermissionMode
	}
	if why := outsideWriteDirs(req.ToolInput.FilePath, o.writeDirs()); why != "" {
		return why
	}
	// A wide list must not reach the hook's own files: the settings with its entry and the user's rules, and the
	// options this setting is in.
	for _, own := range []string{a.ConfigDir, a.Data} {
		if own != "" && outsideWriteDirs(req.ToolInput.FilePath, []string{own}) != "outside the assistant write directories" {
			return "the hook's own files: " + own
		}
	}
	// The settings file may be a symlink elsewhere (a dotfile manager's): where it points is the hook's too.
	if a.ConfigDir != "" {
		if target, err := realPath(settingsPath(a.ConfigDir)); err == nil {
			if p, err := realPath(filepath.Clean(req.ToolInput.FilePath)); err == nil && p == target {
				return "the hook's own settings file"
			}
		}
	}
	return ""
}

func (o options) writeDirs() []string {
	if len(o.AssistantWriteDirs) == 0 {
		return defaultAssistantWriteDirs
	}
	return o.AssistantWriteDirs
}

// outsideWriteDirs says why path is not one assistant_writes allows in dirs ("" when it is).
func outsideWriteDirs(path string, dirs []string) string {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "no absolute file path"
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." { // cleaned before the symlinks are resolved, it could name another file than the OS writes
			return "a path with .."
		}
	}
	resolved, err := realPath(filepath.Clean(path))
	if err != nil {
		return "cannot resolve the file path"
	}
	for _, d := range dirs {
		abs, err := expand(d)
		if err != nil {
			continue
		}
		root, err := filepath.EvalSymlinks(abs)
		if err != nil {
			continue // a directory that is not there allows nothing
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			// Any case: macOS file systems ignore it by default, so .Claude is .claude there.
			if strings.EqualFold(part, ".claude") || strings.EqualFold(part, ".git") || strings.EqualFold(part, ".mcp.json") {
				return "a protected file in " + d + ": " + part
			}
		}
		return ""
	}
	return "outside the assistant write directories"
}

// realPath is path with the symlinks in its existing part resolved; the part that does not exist yet is kept as it
// is (a new file, a new directory under the assistant's).
func realPath(path string) (string, error) {
	var rest []string
	for p := path; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(p); err == nil {
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				return "", err // a dangling symlink: where it would write is not known
			}
			return filepath.Join(append([]string{resolved}, rest...)...), nil
		}
		if filepath.Dir(p) == p {
			return "", os.ErrNotExist
		}
		rest = append([]string{filepath.Base(p)}, rest...)
	}
}
