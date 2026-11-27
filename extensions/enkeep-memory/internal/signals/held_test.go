package signals

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/hubapi"
	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/model"
)

// heldModel is the fake model, with the steps in held answering as a call the hub held past its wait (model_paused).
type heldModel struct {
	*fakeModel
	held map[string]bool
}

func (m *heldModel) Complete(ctx context.Context, r model.Request) (model.Response, error) {
	if m.held[r.Step] {
		return model.Response{}, &model.PausedError{Message: "model.complete: 并发已满，10 秒后重试"}
	}
	return m.fakeModel.Complete(ctx, r)
}

// The correction judge held (0.2.0-beta.34): the day's signals stop with ErrPaused, for the day to stay open, and
// nothing is kept; once the hold lets go the same day judges the messages as if nothing happened.
func TestHeldCorrectionJudgeKeepsTheDayOpen(t *testing.T) {
	h := &fakeHub{full: map[string][]hubapi.Item{
		"s1": items(0, [2]string{kAgent, "要我确认一下 project-x 的目标吗？"}, [2]string{kUser, "不是说过了吗，别再问了"}),
		"s2": items(30, [2]string{kUser, "说过了，不用再问"}),
	}, sessions: []hubapi.Session{sess("s1", true), sess("s2", false)}}
	st := newState()
	m := &heldModel{fakeModel: &fakeModel{correct: func(text string) map[string]any {
		if strings.Contains(text, "说过") {
			return map[string]any{"issue_key": "asks-again", "issue_text": "用户已经回答过的事不要再问一遍"}
		}
		return nil
	}}, held: map[string]bool{"signals_correct": true}}
	o := opts(t, h, m, st)
	if _, err := Run(context.Background(), st, day, o); !errors.Is(err, model.ErrPaused) {
		t.Fatalf("held judge: err %v, want ErrPaused", err)
	}
	if len(st.Corrections) != 0 {
		t.Fatalf("kept while held: %+v", st.Corrections)
	}
	m.held = nil
	res, err := Run(context.Background(), st, day, o)
	if err != nil || res.Corrections != 2 || len(st.Corrections) != 2 {
		t.Fatalf("after the hold: %+v %v, %d kept", res, err, len(st.Corrections))
	}
}

// A dig's mining held: no dig record is kept (a record would mark the hit done for good), the day's signals stop with
// ErrPaused; the next pass digs it.
func TestHeldMineKeepsNoDig(t *testing.T) {
	h := &fakeHub{full: map[string][]hubapi.Item{
		"s1": items(0, [2]string{kToolUse, "deploy"}, [2]string{kToolResult, "ERR connection refused"},
			[2]string{kToolUse, "deploy --retry"}, [2]string{kToolResult, "ok"}),
	}, sessions: []hubapi.Session{sess("s1", false)}}
	m := &mineModel{fakeModel: &fakeModel{dig: true}, respond: func() (model.Response, error) {
		return model.Response{}, &model.PausedError{Message: "busy"}
	}}
	st := newState()
	o := opts(t, h, m, st)
	if _, err := Run(context.Background(), st, day, o); !errors.Is(err, model.ErrPaused) {
		t.Fatalf("held mine: err %v, want ErrPaused", err)
	}
	if len(st.Digs) != 0 {
		t.Fatalf("a dig kept while held: %+v", st.Digs)
	}
	m.respond = func() (model.Response, error) { return model.Response{Text: `{"entries":[]}`}, nil }
	if _, err := Run(context.Background(), st, day, o); err != nil {
		t.Fatal(err)
	}
	if len(st.Digs) != 1 || st.Digs[0].Decision != DecDig || m.mineCalls != 2 {
		t.Fatalf("after the hold: digs %+v, mine calls %d", st.Digs, m.mineCalls)
	}
}

// The dig decision held: the same, no record.
func TestHeldDigDecisionKeepsNoDig(t *testing.T) {
	h := &fakeHub{full: map[string][]hubapi.Item{
		"s1": items(0, [2]string{kToolUse, "deploy"}, [2]string{kToolResult, "ERR connection refused"},
			[2]string{kToolUse, "deploy --retry"}, [2]string{kToolResult, "ok"}),
	}, sessions: []hubapi.Session{sess("s1", false)}}
	m := &heldModel{fakeModel: &fakeModel{dig: true}, held: map[string]bool{"signals_dig": true}}
	st := newState()
	if _, err := Run(context.Background(), st, day, opts(t, h, m, st)); !errors.Is(err, model.ErrPaused) || len(st.Digs) != 0 {
		t.Fatalf("held decision: err %v digs %+v", err, st.Digs)
	}
}
