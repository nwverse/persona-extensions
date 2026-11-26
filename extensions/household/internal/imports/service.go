package imports

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/household/internal/model"
	"github.com/nwverse/persona-extensions/pluginkit/spaces/kernel"
)

// The calls below are what the page and the assistant's tools do. Each that writes records runs inside an update of
// the records: b is the base being changed. The import store is written in two phases, so that it never claims more
// than the records hold: Apply and UndoLast record the change as pending (the draft stays), and the Finish they return
// confirms it once the records are saved, or takes it back when they are not. A pending change that was never
// finished (the program stopped in between) is settled from the records by Settle, the next time the store is read
// with them.

// ErrChanged refuses a yes given to another revision of a draft than the one stored.
var ErrChanged = errors.New("预览已经变了，请先看一遍新的再确认")

// Finish confirms (ok) or takes back a change recorded as pending, after the records were saved or not.
type Finish func(ok bool) error

// Preview plans the input against the records of h and stores it as a new draft. byUser marks a draft made on the
// page.
func Preview(imp *Store, h *model.Home, in Input, loc *time.Location, byUser bool) (Draft, error) {
	var out Draft
	err := imp.Update(func(st *State, now time.Time) error {
		plan, err := Make(h, in, nil, model.DayOf(now, loc))
		if err != nil {
			return err
		}
		out, err = st.Put(Draft{Input: in, Plan: plan}, now, byUser)
		return err
	})
	return out, err
}

// Replan plans a stored draft again against the records of h, with the edits added to the ones it has, and stores the
// new plan as its next revision: the user then agrees to that one. rev, when above zero, must be the revision the
// request was made for. Only the user (byUser) re-plans a draft made on the page.
func Replan(imp *Store, h *model.Home, id string, rev int, edits []Edit, loc *time.Location, byUser bool) (Draft, error) {
	var out Draft
	err := imp.Update(func(st *State, now time.Time) error {
		d, ok := st.Draft(id)
		if !ok {
			return missing(id)
		}
		if d.ByUser && !byUser {
			return errors.New("这份是用户在家庭管家页面上粘贴的清单，请让用户在页面上回答、修改或确认")
		}
		if rev > 0 && d.Rev != rev {
			return ErrChanged
		}
		all := append(append([]Edit{}, d.Edits...), edits...)
		plan, err := Make(h, d.Input, all, model.DayOf(now, loc))
		if err != nil {
			return err
		}
		d.Edits, d.Plan = all, plan
		out, err = st.Put(d, now, byUser)
		return err
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

// Apply writes revision rev of a draft into b (see Prepare and Write) and records the import as pending; the draft
// stays until Finish(true). Nothing is changed when it fails.
func Apply(b *kernel.Base, imp *Store, id string, rev int, skip []int, edits []Edit, now time.Time, loc *time.Location) (Applied, Finish, error) {
	var out Applied
	err := imp.Update(func(st *State, _ time.Time) error {
		st.Settle(b)
		d, ok := st.Draft(id)
		if !ok {
			return missing(id)
		}
		if rev != d.Rev {
			return ErrChanged
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
		out = Applied{Draft: d.ID, Source: d.Input.Source, At: now, Totals: plan.Totals, Touched: touched, Says: []string{},
			Pending: true}
		for _, s := range plan.Steps {
			if s.Writes() {
				out.Says = append(out.Says, s.Says)
			}
		}
		st.Record(out)
		return nil
	})
	if err != nil {
		return Applied{}, nil, err
	}
	finish := func(ok bool) error {
		return imp.Update(func(st *State, _ time.Time) error {
			i := pendingIndex(st, out.Draft, out.At)
			if i < 0 {
				return nil
			}
			if ok {
				st.Applied[i].Pending = false
				st.Drop(out.Draft)
			} else {
				st.Applied = append(st.Applied[:i], st.Applied[i+1:]...)
			}
			return nil
		})
	}
	out.Pending = false
	return out, finish, nil
}

func pendingIndex(st *State, draft string, at time.Time) int {
	for i := len(st.Applied) - 1; i >= 0; i-- {
		if a := st.Applied[i]; a.Pending && a.Draft == draft && a.At.Equal(at) {
			return i
		}
	}
	return -1
}

// UndoLast undoes the import applied last (see Undo for when it refuses) and records the undo as pending until
// Finish. want, when set, must be the draft id of that import, so that a stale request does not undo a newer one.
func UndoLast(b *kernel.Base, imp *Store, want string, now time.Time) (Applied, Finish, error) {
	var out Applied
	err := imp.Update(func(st *State, _ time.Time) error {
		st.Settle(b)
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
		last.Undone, last.UndoPending = now, true
		out = *last
		return nil
	})
	if err != nil {
		return Applied{}, nil, err
	}
	finish := func(ok bool) error {
		return imp.Update(func(st *State, _ time.Time) error {
			for i := range st.Applied {
				a := &st.Applied[i]
				if a.Draft == out.Draft && a.At.Equal(out.At) && a.UndoPending {
					a.UndoPending = false
					if !ok {
						a.Undone = time.Time{}
					}
				}
			}
			return nil
		})
	}
	out.UndoPending = false
	return out, finish, nil
}

// Settle decides every pending change from the records of b: an import is written when a record it created exists or
// a record it changed is as the import left it (it then loses its draft), else it was never written (its entry goes,
// its draft stays); an undo is written when every record the import created is gone and every record it changed is
// back as it was, else the import is not undone.
func (s *State) Settle(b *kernel.Base) {
	keep := s.Applied[:0]
	for _, a := range s.Applied {
		if a.Pending {
			if !written(b, a) {
				continue
			}
			a.Pending = false
			s.Drop(a.Draft)
		}
		if a.UndoPending {
			a.UndoPending = false
			if !undone(b, a) {
				a.Undone = time.Time{}
			}
		}
		keep = append(keep, a)
	}
	s.Applied = keep
}

func written(b *kernel.Base, a Applied) bool {
	for _, t := range a.Touched {
		e, ok := entity(b, t.ID)
		if !ok {
			continue
		}
		if t.Before == nil {
			return true
		}
		if cur, err := json.Marshal(e); err == nil && sameJSON(cur, t.After) {
			return true
		}
	}
	return false
}

func undone(b *kernel.Base, a Applied) bool {
	for _, t := range a.Touched {
		e, ok := entity(b, t.ID)
		if t.Before == nil {
			if ok {
				return false
			}
			continue
		}
		if !ok || !sameContent(e, t.Before) {
			return false
		}
	}
	return true
}

// sameContent reports whether the record has the title, stage and values of the stored one, whenever it was saved.
func sameContent(e *kernel.Entity, raw []byte) bool {
	var was kernel.Entity
	if json.Unmarshal(raw, &was) != nil {
		return false
	}
	x, _ := json.Marshal([]any{e.Title, e.Status, e.Values})
	y, _ := json.Marshal([]any{was.Title, was.Status, was.Values})
	return string(x) == string(y)
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
	add(t.Lowered, "其中 %d 件会比现在记的少")
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
