package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"

	"github.com/nwverse/persona-extensions/extensions/household/internal/imports"
)

// The import part of the page: the drafts waiting for the user's yes (made by the assistant from a receipt, a photo or
// a text, or by 粘贴清单 on this page), the result of the last import with 撤销这次导入, and the two ways to start one.

// Limits of the import part, so that the overview stays within a page's budget whatever is stored: at most
// draftsShown drafts with at most stepsShown lines each.
const (
	draftsShown = 3
	stepsShown  = 15
	unreadShown = 5
	pasteForm   = "paste" // the form state of 粘贴清单
	pasteMost   = 4000    // runes the paste field takes
)

// UseImports makes imp the store of the import drafts; the assistant's tools share it. It returns the app.
func (a *App) UseImports(imp *imports.Store) *App {
	a.imp = imp
	return a
}

// importsOf is the app's draft store.
func (a *App) importsOf() *imports.Store {
	if a.imp == nil {
		a.imp = imports.Open("")
	}
	return a.imp
}

// loadImports reads the drafts and the imports applied, for the request.
func (c *ctx) loadImports() {
	c.imps = &imports.State{}
	_ = c.app.importsOf().View(func(st *imports.State) error {
		cp := *st
		c.imps = &cp
		return nil
	})
}

// assistantHint is the row that says how to have the assistant fill the household in. A page cannot write to the
// assistant: the user sends the picture or the text in the Persona conversation.
func assistantHint() wire.Component {
	return wire.Component{
		Type: wire.ComponentRow, Title: "让助手帮我记",
		Subtitle: "在 Persona 里把小票、冰箱或柜子的照片、网购订单截图发给助手，或者直接打字，说一句「把这张小票记到家庭管家」。" +
			"助手读完会在这里放一份「待确认的导入」，你点「确认写入」才会写进去。",
	}
}

// pasteButton opens the 粘贴清单 form.
func pasteButton() wire.Component {
	return button("粘贴清单", ext.Open("paste", PageUI, state{F: pasteForm}, "粘贴清单"), wire.ButtonRoleNormal)
}

// bulkSection is 一次记很多: the hint about the assistant and 粘贴清单.
func bulkSection() wire.Section {
	return wire.Section{Title: "一次记很多", Items: []wire.Component{assistantHint(), pasteButton()}}
}

// importSections are the drafts waiting, newest first, then the last import when it was today or yesterday.
func (c *ctx) importSections() []wire.Section {
	var out []wire.Section
	drafts := c.imps.Drafts
	shown := 0
	for i := len(drafts) - 1; i >= 0 && shown < draftsShown; i-- {
		out = append(out, c.draftSection(drafts[i], shown == 0))
		shown++
	}
	if left := len(drafts) - shown; left > 0 {
		out[len(out)-1].Footer = join(out[len(out)-1].Footer, fmt.Sprintf("还有 %d 份更早的导入没有显示，先处理上面的", left))
	}
	if s, ok := c.lastImportSection(); ok {
		out = append(out, s)
	}
	return out
}

// draftSection is one draft: where it came from and when, a row per line saying what will happen (inferred values and
// questions marked), the lines that could not be read, the totals, and 确认写入 and 放弃.
func (c *ctx) draftSection(d imports.Draft, first bool) wire.Section {
	title := "待确认的导入"
	if !first {
		title = "另一份待确认的导入"
	}
	var items []wire.Component
	for i, s := range d.Plan.Steps {
		if i >= stepsShown {
			items = append(items, quiet(fmt.Sprintf("还有 %d 行没有列出，写入时一起写", len(d.Plan.Steps)-stepsShown)))
			break
		}
		items = append(items, stepRow(s))
		if s.Ask != nil {
			items = append(items, answerButtons(d.ID, s)...)
		}
	}
	for i, u := range d.Plan.Unread {
		if i >= unreadShown {
			items = append(items, quiet(fmt.Sprintf("还有 %d 行没读懂", len(d.Plan.Unread)-unreadShown)))
			break
		}
		items = append(items, wire.Component{Type: wire.ComponentRow, Title: cut(u.Text, maxTitle), Subtitle: u.Why,
			Badge: &wire.Badge{Text: "没读懂"}})
	}
	items = append(items, quiet("合计："+imports.TotalsText(d.Plan.Totals)))
	stale := c.draftStale(d)
	switch {
	case stale:
		items = append(items, quiet("家里的记录在这之后变了，先重新算一遍，看过再确认。"),
			button("重新算一遍", &wire.Action{ID: "impreplan:" + d.ID}, wire.ButtonRoleNormal))
	case d.Plan.Totals.Questions > 0:
		items = append(items, quiet(fmt.Sprintf("还有 %d 行要你先定，定好了才能写入。", d.Plan.Totals.Questions)))
	case d.Plan.Totals.Changes > 0:
		items = append(items, button("确认写入", &wire.Action{ID: "impapply:" + d.ID,
			Confirm: imports.ConfirmText(d.Plan.Totals)}, wire.ButtonRoleNormal))
	}
	items = append(items, button("放弃", &wire.Action{ID: "impdrop:" + d.ID,
		Confirm: "放弃这份导入？记录不会有任何改动。"}, wire.ButtonRoleNormal))
	when := c.when(d.Created)
	footer := join(imports.SourceLabel(d.Input.Source)+" · "+when, cut(d.Input.Note, 60), "24 小时内没确认会自动作废")
	return wire.Section{Title: title, Footer: footer, Items: items}
}

// draftStale reports whether the records changed since the draft was planned, so that it would now do something else.
func (c *ctx) draftStale(d imports.Draft) bool {
	p, err := imports.Make(c.home, d.Input, d.Edits, c.today)
	if err != nil {
		return true
	}
	now, was := p.Sigs(), d.Plan.Sigs()
	if len(now) != len(was) {
		return true
	}
	for line, sig := range now {
		if was[line] != sig {
			return true
		}
	}
	return false
}

// stepRow is one line of a draft: its number and name, what will happen, and a badge for an inferred value, a
// question, or a line that changes nothing.
func stepRow(s imports.Step) wire.Component {
	sub := s.Says
	if len(s.Notes) > 0 {
		sub += "。" + strings.Join(s.Notes, "；")
	}
	row := wire.Component{Type: wire.ComponentRow, Title: cut(fmt.Sprintf("%d. %s", s.Line, s.Title), maxTitle),
		Subtitle: cut(sub, 400)}
	switch {
	case s.Action == imports.ActAsk:
		row.Badge = &wire.Badge{Text: "要你定", Tone: wire.ToneCaution}
	case !s.Writes():
		row.Badge = &wire.Badge{Text: "不改"}
	case len(s.Inferred) > 0:
		row.Badge = &wire.Badge{Text: "有推测", Tone: wire.ToneCaution}
	}
	return row
}

// answerButtons are the answers to a line's question, one button each.
func answerButtons(id string, s imports.Step) []wire.Component {
	var out []wire.Component
	for k, ch := range s.Ask.Choices {
		if k >= 4 {
			break
		}
		out = append(out, button(cut(ch.Label, 40),
			&wire.Action{ID: fmt.Sprintf("impans:%s:%d:%d", id, s.Line, k)}, wire.ButtonRoleNormal))
	}
	return out
}

// lastImportSection is 刚才的导入: what the last import wrote, with 撤销这次导入 while it can be undone; shown for a
// day after the import.
func (c *ctx) lastImportSection() (wire.Section, bool) {
	last, ok := c.imps.Last()
	if !ok || c.now.Sub(last.At) > 24*time.Hour {
		return wire.Section{}, false
	}
	items := []wire.Component{quiet(fmt.Sprintf("%s 写入了%s：%s", c.when(last.At), imports.SourceLabel(last.Source),
		imports.TotalsText(last.Totals)))}
	switch {
	case !last.Undone.IsZero():
		items = append(items, quiet("已经撤销（"+c.when(last.Undone)+"）。"))
	default:
		if err := imports.CheckUndo(c.b, last); err != nil {
			items = append(items, quiet(cut("不能撤销了："+err.Error(), 400)))
		} else {
			items = append(items, button("撤销这次导入", &wire.Action{ID: "impundo:" + last.Draft,
				Confirm: "撤销这次导入？这次新加的记录会删掉，改过的数量和采购会恢复成导入前的样子。"}, wire.ButtonRoleNormal))
		}
	}
	return wire.Section{Title: "刚才的导入", Items: items}, true
}

// when is a time as the page says it: 今天 09:30, 昨天 18:05, or 11-24 18:05.
func (c *ctx) when(t time.Time) string {
	lt := t.In(c.loc)
	day := lt.Format("01-02")
	switch lt.Format(time.DateOnly) {
	case string(c.today):
		day = "今天"
	case string(c.today.Add(-1)):
		day = "昨天"
	}
	return day + " " + lt.Format("15:04")
}

// pastePage is the 粘贴清单 form: one thing per line, where it goes, and whether it adds to what is there.
func (c *ctx) pastePage() *wire.View {
	return &wire.View{Title: "粘贴清单", Sections: []wire.Section{{
		Items: []wire.Component{
			quiet("每行写一样东西：名称、数量和单位，可以带过期日，例如「牛奶 2 盒」「鸡蛋 12 个 2026-12-05」。" +
				"先给你看会怎么记，你确认了才写进去；看不懂的行会标出来，不会乱猜。"),
			{Type: wire.ComponentField, Name: "paste_text", Label: "清单", Kind: wire.FieldKindMultiline,
				Placeholder: "牛奶 2 盒\n鸡蛋 12 个 2026-12-05\n洗洁精 1 瓶"},
			{Type: wire.ComponentField, Name: "paste_target", Label: "记到哪", Kind: wire.FieldKindChoice,
				Value: choicePrefix + imports.ToStock, Choices: []wire.Choice{
					{Value: choicePrefix + imports.ToStock, Title: "库存（家里有的）"},
					{Value: choicePrefix + imports.ToShopping, Title: "采购清单（要买的）"},
				}},
			{Type: wire.ComponentField, Name: "paste_mode", Label: "数量怎么算", Kind: wire.FieldKindChoice,
				Value: choicePrefix + imports.ModeAdd, Choices: []wire.Choice{
					{Value: choicePrefix + imports.ModeAdd, Title: "刚买回来，加到原有的数量上"},
					{Value: choicePrefix + imports.ModeCount, Title: "数过了，家里现在就这么多"},
				}},
			button("看看会怎么记", &wire.Action{ID: "imppaste", Submit: true}, wire.ButtonRoleNormal),
			button("取消", &wire.Action{ID: "back"}, wire.ButtonRoleNormal),
		},
	}}}
}

// actPasteOpen opens the 粘贴清单 form (for an app that calls the action instead of pushing).
func actPasteOpen(_ *ctx, _ string) (*wire.ActionResult, error) {
	return &wire.ActionResult{Push: pasteButton().Action.Push}, nil
}

// actPaste reads the pasted list into a draft and goes back to the overview, where it waits for 确认写入.
func actPaste(c *ctx, _ string) (*wire.ActionResult, error) {
	text := c.r.Values["paste_text"]
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("先把要记的东西粘贴进来，一行一样")
	}
	text = cut(text, pasteMost)
	lines, unread, notes := imports.ParsePaste(text, c.today)
	if len(lines) == 0 {
		return nil, errors.New("一行都没读懂：每行写名称和数量，例如「牛奶 2 盒」")
	}
	target := strings.TrimPrefix(c.r.Values["paste_target"], choicePrefix)
	if target != imports.ToShopping {
		target = imports.ToStock
	}
	mode := strings.TrimPrefix(c.r.Values["paste_mode"], choicePrefix)
	if mode != imports.ModeCount {
		mode = imports.ModeAdd
	}
	in := imports.Input{Source: imports.SrcPaste, Target: target, Mode: mode, Lines: lines, Unread: unread,
		Note: strings.Join(notes, "；")}
	if _, err := imports.Preview(c.app.importsOf(), c.home, in, c.loc); err != nil {
		return nil, err
	}
	c.st = state{}
	return &wire.ActionResult{Toast: fmt.Sprintf("读到 %d 样，请在「待确认的导入」里看一遍再确认", len(lines)), Pop: true}, nil
}

// actImportApply writes a draft the user confirmed.
func actImportApply(c *ctx, id string) (*wire.ActionResult, error) {
	done, err := imports.Apply(c.b, c.app.importsOf(), id, nil, nil, c.now, c.loc)
	if err != nil {
		return nil, err
	}
	return &wire.ActionResult{Toast: "已写入：" + imports.TotalsText(done.Totals)}, nil
}

// actImportDrop drops a draft; nothing in the records changes.
func actImportDrop(c *ctx, id string) (*wire.ActionResult, error) {
	if _, err := imports.Discard(c.app.importsOf(), id); err != nil {
		return nil, err
	}
	return &wire.ActionResult{Toast: "已放弃这份导入，记录没有改动"}, nil
}

// actImportReplan plans a draft again against the records as they are now.
func actImportReplan(c *ctx, id string) (*wire.ActionResult, error) {
	if _, err := imports.Replan(c.app.importsOf(), c.home, id, nil, c.loc); err != nil {
		return nil, err
	}
	return &wire.ActionResult{Toast: "已按现在的记录重新算好，请再看一遍"}, nil
}

// actImportAnswer answers the question of a draft's line with one of its choices: <draft>:<line>:<choice>.
func actImportAnswer(c *ctx, arg string) (*wire.ActionResult, error) {
	parts := strings.Split(arg, ":")
	if len(parts) != 3 {
		return nil, errors.New("这个回答看不懂，请刷新页面再试")
	}
	line, err1 := strconv.Atoi(parts[1])
	k, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil {
		return nil, errors.New("这个回答看不懂，请刷新页面再试")
	}
	d, ok := c.imps.Draft(parts[0])
	if !ok {
		return nil, errors.New("这份导入已经不在了：可能已经写入、放弃，或者过了 24 小时")
	}
	var choice *imports.Choice
	for _, s := range d.Plan.Steps {
		if s.Line == line && s.Ask != nil && k >= 0 && k < len(s.Ask.Choices) {
			choice = &s.Ask.Choices[k]
		}
	}
	if choice == nil {
		return nil, errors.New("这一行已经不用回答了，请刷新页面")
	}
	edit := imports.Edit{Line: line, Match: choice.Match, Qty: choice.Qty, Skip: choice.Skip}
	if _, err := imports.Replan(c.app.importsOf(), c.home, d.ID, []imports.Edit{edit}, c.loc); err != nil {
		return nil, err
	}
	return &wire.ActionResult{Toast: "已记下：" + choice.Label}, nil
}

// actImportUndo undoes the last import.
func actImportUndo(c *ctx, id string) (*wire.ActionResult, error) {
	if _, err := imports.UndoLast(c.b, c.app.importsOf(), id, c.now); err != nil {
		return nil, err
	}
	return &wire.ActionResult{Toast: "已撤销这次导入"}, nil
}
