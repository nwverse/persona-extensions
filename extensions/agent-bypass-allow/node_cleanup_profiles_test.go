package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func profileCleanupNode(t *testing.T, personaHome string, configDirs []string) (*node, string, context.CancelFunc) {
	t.Helper()
	root := filepath.Dir(filepath.Dir(personaHome))
	home := filepath.Join(root, "home", "me")
	t.Setenv("HOME", home)
	data := filepath.Join(personaHome, "node-plugins", "agent-bypass-allow", "data")
	for _, dir := range []string{filepath.Join(home, ".claude"), data} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	config, _ := json.Marshal(map[string]any{"machines": []string{"m1"}, "config_dirs": configDirs})
	n := &node{bin: "/opt/extension/bin/agent-bypass-allow"}
	in := &ext.Init{}
	in.Extension = &wire.ExtensionInfo{ID: currentExtensionID, DataDir: data}
	in.InitializeParams.Config = config
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := n.init(ctx, in, nil); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		n.cleanup()
	})
	return n, data, cancel
}

func TestNodeCleanupBackfillsPreviouslyMissingExplicitProfile(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "me")
	t.Setenv("HOME", home)
	personaHome := filepath.Join(root, "state", "persona")
	profile := filepath.Join(personaHome, "profiles", "channel")
	n, data, cancel := profileCleanupNode(t, personaHome, []string{"~/.claude", profile})
	if len(n.dirs) != 2 || n.dirs[1].Settings != "" {
		t.Fatalf("missing explicit directory did not leave the expected placeholder: %+v", n.dirs)
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profile, "settings.json")
	if _, err := installHookWith(path, n.matcher, hookCommand("/opt/extension/seed", profile, data)); err != nil {
		t.Fatal(err)
	}
	cancel()
	n.cleanup()
	if hasHook(path) {
		t.Fatal("cleanup skipped a profile that was missing at the last refresh")
	}
}

func TestNodeCleanupDoesNotFollowReplacedProfileLink(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "me")
	t.Setenv("HOME", home)
	personaHome := filepath.Join(root, "state", "persona")
	profile := filepath.Join(personaHome, "profiles", "channel")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	n, data, cancel := profileCleanupNode(t, personaHome, []string{"~/.claude"})
	path := filepath.Join(profile, "settings.json")
	if !hasHook(path) {
		t.Fatal("profile was not installed before the replacement")
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(profile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, profile); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(outside, "settings.json")
	if _, err := installHookWith(outsidePath, n.matcher, hookCommand("/opt/extension/outside", outside, data)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	n.cleanup()
	after, err := os.ReadFile(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("cleanup followed a replaced profile symlink outside the profiles root")
	}
	if !hasHook(outsidePath) {
		t.Fatal("test control lost the outside hook")
	}
}
