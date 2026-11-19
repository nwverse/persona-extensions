package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
)

// The program on a computer (the manifest's node section): while it runs, the hook's entry is in each configured
// Claude config directory and its heartbeat is fresh; when it stops (switched off for this computer, extension
// disabled, upgraded, uninstalled, the hub gone past its lease) it takes the entries out again. If it is killed
// before it can, the heartbeat goes stale and the hook allows nothing (hook.go).

// config is the extension's settings, as the hub sends them.
type config struct {
	Machines         []string `json:"machines"`
	ConfigDirs       []string `json:"config_dirs"`
	AllowDestructive bool     `json:"allow_destructive"`
	AssistantWrites  bool     `json:"assistant_writes"`
	AssistantDirs    []string `json:"assistant_write_dirs"`
}

func (c config) dirs() []string {
	if len(c.ConfigDirs) == 0 {
		return []string{"~/.claude"}
	}
	return c.ConfigDirs
}

type node struct {
	bin     string // this program, as the entries run it
	data    string // its data directory: heartbeat and audit log
	matcher string // the entries' matcher (matcherFor)

	mu         sync.Mutex
	dirs       []dirState
	configured []string
	profiles   string // Persona's assistant channel profiles, beside node-plugins

	// beatWG is the heartbeat ticker goroutine. cleanup joins it before it removes the heartbeat, so a beat
	// already due cannot land during cleanup and recreate the file (a fresh heartbeat with the entry still in
	// would keep answering allow after this program exited).
	beatWG sync.WaitGroup
}

// dirState is one config directory: where its settings are, and whether the entry is in.
type dirState struct {
	Dir       string `json:"dir"`
	Settings  string `json:"settings,omitempty"`
	Installed bool   `json:"installed"`
	Error     string `json:"error,omitempty"`
	auto      bool   // discovered, not an explicit config_dir: revalidate its final component before cleanup
}

// status is what bypass.status answers: the directories and the newest auto-allows.
type status struct {
	Version string       `json:"version"`
	Dirs    []dirState   `json:"dirs"`
	Recent  []auditEntry `json:"recent"`
}

const recentShown = 30

func (n *node) program(id, version string) ext.Program {
	return ext.Program{
		ID: id, Version: version, Manifest: manifest,
		Init: n.init,
		Namespaces: map[string]ext.Handler{"bypass": func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
			if method != "status" {
				return nil, ext.UnknownMethod("bypass." + method)
			}
			n.mu.Lock()
			defer n.mu.Unlock()
			return status{Version: version, Dirs: n.check(), Recent: readAudit(n.data, recentShown)}, nil
		}},
	}
}

func (n *node) init(ctx context.Context, in *ext.Init, _ *ext.Hub) (map[string]any, error) {
	var c config
	if err := in.Config(&c); err != nil {
		return nil, err
	}
	n.data = in.Info().DataDir
	if n.data == "" {
		n.data = ext.Env().Data
	}
	if n.data == "" {
		return nil, fmt.Errorf("no data directory")
	}
	n.configured = c.dirs()
	// <persona home>/node-plugins/<storage>/data identifies the same Persona
	// home whose profiles/<launcher> directories the assistant selects. Do not
	// infer it from HOME: PERSONA_HOME may point somewhere else.
	if plugins := filepath.Dir(filepath.Dir(n.data)); filepath.Base(n.data) == "data" && filepath.Base(plugins) == "node-plugins" {
		n.profiles = filepath.Join(filepath.Dir(plugins), "profiles")
	}
	if err := os.MkdirAll(n.data, 0o700); err != nil {
		return nil, err
	}
	if n.bin == "" {
		bin, err := os.Executable()
		if err != nil {
			return nil, err
		}
		if real, err := filepath.EvalSymlinks(bin); err == nil {
			bin = real
		}
		n.bin = bin
	}
	n.matcher = matcherFor(c.AssistantWrites)
	opts, err := json.Marshal(options{AllowDestructive: c.AllowDestructive, AssistantWrites: c.AssistantWrites, AssistantWriteDirs: c.AssistantDirs})
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(filepath.Join(n.data, optionsFile), opts); err != nil {
		return nil, err // the hook must not read an old setting
	}
	n.beat()
	n.mu.Lock()
	n.install(c.dirs())
	n.mu.Unlock()
	n.beatWG.Add(1)
	go func() {
		defer n.beatWG.Done()
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n.refresh()
			}
		}
	}()
	return nil, nil
}

// beat touches the heartbeat.
func (n *node) beat() {
	p := filepath.Join(n.data, heartbeatFile)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err != nil {
		_ = os.WriteFile(p, nil, 0o600)
	}
}

// refresh is the heartbeat tick: renew freshness, then install newly created
// profiles and replace stale entries with this executable's current command.
func (n *node) refresh() {
	n.beat()
	n.mu.Lock()
	defer n.mu.Unlock()
	n.install(nil)
}

// install puts the entry into each explicitly configured directory and each
// existing assistant channel profile. A nil dirs reconciles the initial
// configuration again. Discovery never creates a profile and does not follow a
// final-component symlink out of Persona's profiles directory.
func (n *node) install(dirs []string) {
	if dirs == nil {
		dirs = n.configured
	}
	configured := append([]string(nil), dirs...)
	discovered := n.assistantProfiles()
	explicit := map[string]bool{}
	for _, d := range configured {
		if abs, err := expand(d); err == nil {
			explicit[abs] = true
		}
	}
	automatic := map[string]bool{}
	for _, d := range discovered {
		if abs, err := expand(d); err == nil {
			automatic[abs] = true
		}
	}
	dirs = append(configured, discovered...)
	n.dirs = n.dirs[:0]
	seen := map[string]bool{}
	for _, d := range dirs {
		s := dirState{Dir: d}
		abs, err := expand(d)
		if err == nil {
			if seen[abs] {
				continue
			}
			seen[abs] = true
			s.auto = automatic[abs] && !explicit[abs]
		}
		switch {
		case err != nil:
			s.Error = err.Error()
		case !isDir(abs):
			s.Error = "no such directory"
		default:
			s.Settings = settingsPath(abs)
			if _, err := installHookWith(s.Settings, cmp.Or(n.matcher, hookMatcher), hookCommand(n.bin, abs, n.data)); err != nil {
				s.Error = err.Error()
			} else {
				s.Installed = true
			}
		}
		if s.Error != "" {
			fmt.Fprintf(os.Stderr, "%s: %s: %s\n", currentExtensionID, d, s.Error)
		}
		n.dirs = append(n.dirs, s)
	}
}

// assistantProfiles lists the current channel directories. ReadDir sorts them,
// so status and upgrade order stay stable. A profiles directory that does not
// exist yet is normal for a node whose assistant has not started.
func (n *node) assistantProfiles() []string {
	if n.profiles == "" {
		return nil
	}
	entries, err := os.ReadDir(n.profiles)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(n.profiles, entry.Name()))
		}
	}
	return dirs
}

// realAssistantProfile revalidates an automatically discovered path at cleanup:
// it is still the same immediate child kind, not a symlink substituted after the
// last refresh. Lstat never follows that final component.
func (n *node) realAssistantProfile(dir string) bool {
	if n.profiles == "" || filepath.Dir(dir) != n.profiles {
		return false
	}
	st, err := os.Lstat(dir)
	return err == nil && st.IsDir() && st.Mode()&os.ModeSymlink == 0
}

// check reads again whether each entry is still in (someone may have edited the file).
func (n *node) check() []dirState {
	out := make([]dirState, len(n.dirs))
	for i, s := range n.dirs {
		if s.Settings != "" && s.Error == "" {
			s.Installed = hasHook(s.Settings)
		}
		out[i] = s
	}
	return out
}

// cleanup takes the entries out and stops the heartbeat; the program is about to exit. ext.Main has returned by
// then, so the run context is cancelled: join the ticker goroutine before taking the node lock (a due refresh may be
// waiting for that lock), then remove the heartbeat and entries. No beat or refresh can afterwards resurrect them.
func (n *node) cleanup() {
	n.beatWG.Wait()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.data != "" {
		_ = os.Remove(filepath.Join(n.data, heartbeatFile))
	}
	// A new profile may have been seeded by Persona after the last heartbeat.
	// Merge current profiles with old states: a previously missing explicit
	// directory has no Settings yet and must not suppress the cleanup.
	seen := map[string]int{}
	for i, s := range n.dirs {
		if abs, err := expand(s.Dir); err == nil {
			seen[abs] = i
		}
	}
	for _, d := range n.assistantProfiles() {
		abs, err := expand(d)
		if err != nil {
			continue
		}
		if i, ok := seen[abs]; ok {
			if n.dirs[i].Settings == "" {
				n.dirs[i].Settings = settingsPath(abs)
			}
			continue
		}
		n.dirs = append(n.dirs, dirState{Dir: d, Settings: settingsPath(abs), auto: true})
	}
	for _, s := range n.dirs {
		// Only automatic discovery has the no-symlink-escape rule. An explicit
		// config_dir keeps the extension's existing settings-symlink semantics.
		if s.auto && !n.realAssistantProfile(s.Dir) {
			continue
		}
		if s.Settings == "" {
			continue
		}
		if _, err := uninstallHook(s.Settings); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s: %v\n", currentExtensionID, s.Dir, err)
		}
	}
}

// expand turns ~ into the home directory; the result must be absolute.
func expand(d string) (string, error) {
	d = strings.TrimSpace(d)
	if d == "~" || strings.HasPrefix(d, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(home, strings.TrimPrefix(d, "~"))
	}
	if !filepath.IsAbs(d) {
		return "", fmt.Errorf("not an absolute path")
	}
	return filepath.Clean(d), nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// writeFileAtomic replaces path with b (mode 0600) so that a reader sees the old file or the new one.
func writeFileAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
