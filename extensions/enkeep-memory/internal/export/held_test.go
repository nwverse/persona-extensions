package export

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/entry"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
)

// heldRewrite answers as a hub that holds the model past the call's wait (model_paused) while held.
type heldRewrite struct {
	echo
	held bool
}

func (m *heldRewrite) Complete(ctx context.Context, r model.Request) (model.Response, error) {
	if m.held {
		return model.Response{}, &model.PausedError{Message: "busy"}
	}
	return m.echo.Complete(ctx, r)
}

// The rewrite held (0.2.0-beta.34): the compile returns ErrPaused and writes nothing, rather than write the exits with
// the lines it could not rewrite left out; once the hold lets go the next compile writes them.
func TestHeldRewriteWritesNothing(t *testing.T) {
	dir := t.TempDir()
	st := fixture()
	m := &heldRewrite{}
	compile(t, st, dir, m)
	before := allExits(t, dir)
	add := ent("i-0000000010", entry.Interaction, entry.Class, "新加的占位交互要求")
	st.Entries[add.ID] = add
	m.held = true
	later := now.Add(24 * time.Hour)
	opt := Options{Dir: dir, Model: m, Lang: "zh-Hans", Loc: time.UTC, Now: func() time.Time { return later }}
	if _, err := Compile(context.Background(), st, opt); !errors.Is(err, model.ErrPaused) {
		t.Fatalf("held: err %v, want ErrPaused", err)
	}
	if after := allExits(t, dir); !maps.Equal(before, after) || add.Export != nil {
		t.Fatalf("written while held: export %+v", add.Export)
	}
	m.held = false
	if _, err := Compile(context.Background(), st, opt); err != nil {
		t.Fatal(err)
	}
	if add.Export == nil || add.Export.Text == "" {
		t.Fatalf("not rewritten after the hold: %+v", add.Export)
	}
}
