package imports

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/household/internal/model"
	"github.com/nwverse/persona-extensions/pluginkit/spaces/kernel"
)

// The calls below are what the page and the assistant's tools do. Each that writes records runs inside an update of
// the records: b is the base being changed, and the import store is updated within it, so a refused step saves
// neither. (Should saving the records fail after the import store was saved, the draft is gone and the import's record
// does not match the records: it then cannot be undone, and the user previews again.)

// Preview plans the input against the records of h and stores it as a new draft.
func Preview(imp *Store, h *model.Home, in Input, loc *time.Location) (Draft, error) {
	var out Draft
	err := imp.Update(func(st *State, now time.Time) error {
		plan, err := Make(h, in, nil, model.DayOf(now, loc))
		if err != nil {
			return err
		}
		out = st.Put(Draft{Input: in, Plan: plan}, now)
		return nil
	})
	return out, err
}

// Replan plans a stored draft again against the records of h, with the edits added to the ones it has, and stores the
// new plan: the user then agrees to that one. Edits with Skip are kept as such.
func Replan(imp *Store, h *model.Home, id string, edits []Edit, loc *time.Location) (Draft, error) {
	var out Draft
	err := imp.Update(func(st *State, now time.Time) error {
		d, ok := st.Draft(id)
		if !ok {
			return missing(id)
		}
		all := append(append([]Edit{}, d.Edits...), edits...)
		plan, err := Make(h, d.Input, all, model.DayOf(now, loc))
		if err != nil {
			return err
		}
		d.Edits, d.Plan = all, plan
		out = st.Put(d, now)
		return nil
	})
	return out, err
}

// Discard drops a draft. Nothing in the records changes.
func Discard(imp *Store, id string) (Draft, error) {
	var out Draft
	err := imp.Update(func(st *State, _ time.Time) error {
		d, ok := st.Draft(id)
		if !ok {
			return missing(id)
		}
		st.Drop(id)
		out = d
		return nil
	})
	return out, err
}

func missing(id string) error {
	return fmt.Errorf("没有草稿「%s」：它可能已经写入、放弃，或者过了 24 小时作废了", id)
}

// Apply writes a draft into b (see Prepare and Write), drops the draft and records the import so that it can be
// undone. Nothing is changed when it fails.
func Apply(b *kernel.Base, imp *Store, id string, skip []int, edits []Edit, now time.Time, loc *time.Location) (Applied, error) {
	var out Applied
	err := imp.Update(func(st *State, _ time.Time) error {
		d, ok := st.Draft(id)
		if !ok {
			return missing(id)
		}
		h := model.Load(household(b), loc)
		plan, err := Prepare(h, d, skip, edits, model.DayOf(now, loc))
		if err != nil {
			return err
		}
		touched, err := Write(b, h, plan, now, loc)
		if err != nil {
			return err
		}
		out = Applied{Draft: d.ID, Source: d.Input.Source, At: now, Totals: plan.Totals, Touched: touched, Says: []string{}}
		for _, s := range plan.Steps {
			if s.Writes() {
				out.Says = append(out.Says, s.Says)
			}
		}
		st.Drop(id)
		st.Record(out)
		return nil
	})
	return out, err
}

// UndoLast undoes the import applied last (see Undo for when it refuses). want, when set, must be
// the draft id of that import, so that a stale request does not undo a newer one.
func UndoLast(b *kernel.Base, imp *Store, want string, now time.Time) (Applied, error) {
	var out Applied
	err := imp.Update(func(st *State, _ time.Time) error {
		last, ok := st.Last()
		if !ok {
			return errors.New("还没有导入过东西，没有可以撤销的")
		}
		if want != "" && last.Draft != want {
			return fmt.Errorf("最近一次导入是「%s」，不是「%s」；只能撤销最近一次", last.Draft, want)
		}
		if err := Undo(b, last, now); err != nil {
			return err
		}
		last.Undone = now
		out = *last
		return nil
	})
	return out, err
}

// Summary is a plan in plain Chinese, one line per step, then what was inferred, what needs an answer and the totals:
// what the assistant reads back to the user.
func Summary(src string, p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "从%s读到 %d 样：\n", SourceLabel(src), len(p.Steps))
	var inferred, asks []string
	for _, s := range p.Steps {
		fmt.Fprintf(&b, "%d. %s\n", s.Line, s.Says)
		if w := InferredWords(s); w != "" && s.Writes() {
			inferred = append(inferred, fmt.Sprintf("第 %d 行的%s", s.Line, w))
		}
		if s.Ask != nil {
			asks = append(asks, fmt.Sprintf("第 %d 行「%s」", s.Line, s.Title))
		}
	}
	for _, u := range p.Unread {
		fmt.Fprintf(&b, "没读懂：「%s」（%s）\n", u.Text, u.Why)
	}
	if len(inferred) > 0 {
		fmt.Fprintf(&b, "推测的（可以改）：%s。\n", strings.Join(inferred, "，"))
	}
	if len(asks) > 0 {
		fmt.Fprintf(&b, "要你定：%s。\n", strings.Join(asks, "、"))
	}
	b.WriteString("合计：" + TotalsText(p.Totals))
	return b.String()
}

// TotalsText is what a plan changes, in one sentence.
func TotalsText(t Totals) string {
	var parts []string
	add := func(n int, f string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf(f, n))
		}
	}
	add(t.NewItems, "新加 %d 件")
	add(t.AddedTo, "给 %d 件加数量")
	add(t.Counted, "%d 件按看到的记数量")
	add(t.Updated, "%d 件补上信息")
	add(t.Bought, "%d 条采购标成已买")
	add(t.ToList, "%d 样加到采购清单")
	add(t.NewPlaces, "新建 %d 个位置")
	add(t.OnList, "%d 样已经在清单上")
	add(t.Unchanged, "%d 样已经记着")
	add(t.Skipped, "%d 行不记")
	add(t.Questions, "%d 行要你定")
	add(t.Unread, "%d 行没读懂")
	if len(parts) == 0 {
		parts = append(parts, "没有要改的")
	}
	out := strings.Join(parts, "，") + "。"
	if t.PriceTotal != nil {
		out += fmt.Sprintf("这几行的金额合计 %s 元（金额不记到库存里）。", num(*t.PriceTotal))
	}
	return out
}

// ConfirmText is the sentence the page asks before writing a plan.
func ConfirmText(t Totals) string {
	return fmt.Sprintf("确认写入？会改动 %d 样：%s写入后可以撤销。", t.Changes, TotalsText(t))
}
