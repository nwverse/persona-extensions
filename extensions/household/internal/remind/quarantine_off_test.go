package remind

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/household/internal/model"
)

// The quarantine of an ambiguous create ends with its alert (docs/extensions/household.md), also when the alert
// ends while to-dos are off; the next occurrence then gets a to-do once to-dos are back on.
func TestRunTodoQuarantineEndsWhileTodosOff(t *testing.T) {
	cases := []struct {
		name string
		off  func(r *Reminder, api *fakeAPI)
	}{
		{"mode notify", func(r *Reminder, _ *fakeAPI) { r.Config.Mode = "notify" }},
		{"mode off", func(r *Reminder, _ *fakeAPI) { r.Config.Mode = "off" }},
		{"todos scope gone", func(_ *Reminder, api *fakeAPI) { api.scopes[ScopeTodos] = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			api := newFake(ScopeTodos, ScopeNotify)
			api.ambiguousCreate = 1
			path := filepath.Join(t.TempDir(), "remind.json")
			r := &Reminder{Hub: api, Path: path, Config: Config{Mode: "todos", At: "00:00"}}
			home := manyOutHome(1)

			// 1. The create for item:i-000 has an unknown outcome: the key is quarantined.
			if err := r.Run(ctx, home, at(9, 0), cst); err == nil {
				t.Fatal("Run returned nil, want the ambiguous create error")
			}
			// 2. To-dos are switched off.
			tc.off(r, api)
			// 3. The alert ends (restocked) and later comes back, all while to-dos are off.
			if err := r.Run(ctx, &model.Home{}, at(9, 10), cst); err != nil {
				t.Fatal(err)
			}
			if err := r.Run(ctx, home, at(9, 20), cst); err != nil {
				t.Fatal(err)
			}
			// 4. To-dos are switched back on: the new occurrence gets a to-do.
			r.Config.Mode = "todos"
			api.scopes[ScopeTodos] = true
			if err := r.Run(ctx, home, at(9, 30), cst); err != nil {
				t.Fatal(err)
			}
			if api.creates != 2 {
				q := readStateFile(t, path)["quarantined"]
				t.Fatalf("create calls = %d, want 2: the alert ended while to-dos were off, so its next occurrence "+
					"gets a to-do again (quarantined = %v)", api.creates, q)
			}
		})
	}
}

// While the alert lasts, the quarantine is kept also when the todos.write scope goes away and comes back; the
// to-do that may exist is not created again.
func TestRunTodoQuarantineKeptWhileScopeGone(t *testing.T) {
	ctx := context.Background()
	api := newFake(ScopeTodos)
	api.ambiguousCreate = 1
	path := filepath.Join(t.TempDir(), "remind.json")
	r := &Reminder{Hub: api, Path: path, Config: Config{Mode: "todos"}}
	home := manyOutHome(1)
	if err := r.Run(ctx, home, at(9, 0), cst); err == nil {
		t.Fatal("Run returned nil, want the ambiguous create error")
	}

	api.scopes[ScopeTodos] = false
	if err := r.Run(ctx, home, at(9, 10), cst); err != nil {
		t.Fatal(err)
	}
	api.scopes[ScopeTodos] = true
	if err := r.Run(ctx, home, at(9, 20), cst); err != nil {
		t.Fatal(err)
	}
	if api.creates != 1 {
		t.Fatalf("create calls = %d, want 1: the alert lasted, so its quarantine is kept", api.creates)
	}
	if q, _ := readStateFile(t, path)["quarantined"].(map[string]any); len(q) != 1 {
		t.Fatalf("quarantined = %v, want the one ambiguous alert", q)
	}
}
