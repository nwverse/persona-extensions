package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"

	"github.com/nwverse/persona-extensions/extensions/household/internal/imports"
	"github.com/nwverse/persona-extensions/extensions/household/internal/model"
)

func fp(v float64) *float64 { return &v }

// preview stores a draft as the assistant's tool would.
func preview(t *testing.T, a *App, in imports.Input) imports.Draft {
	t.Helper()
	d, err := imports.Preview(a.importsOf(), home(t, a.st), in, testLoc())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func buttonTitledIn(s wire.Section, title string) (wire.Component, bool) {
	for _, c := range s.Items {
		if c.Type == wire.ComponentButton && c.Title == title {
			return c, true
		}
	}
	return wire.Component{}, false
}

func receiptInput() imports.Input {
	return imports.Input{Source: imports.SrcReceipt, Note: "周六超市", Lines: []imports.Line{
		{Title: "番茄", Qty: fp(2), Unit: "个"},
		{Title: "酸奶", Qty: fp(4), Unit: "杯"},
		{Title: "洗衣液", Qty: fp(1), Unit: "瓶"},
		{Title: "香蕉", Qty: fp(1), Unit: "把"},
	}}
}

func TestDraftSectionShowsLinesAndConfirm(t *testing.T) {
	st := newStore(t, true)
	a, _ := newApp(st)
	d := preview(t, a, receiptInput())
	v := render(t, a, state{})
	checkView(t, "overview with a draft", v, false)
	if v.Sections[1].Title != "待确认的导入" {
		t.Fatalf("second section %q, want the draft right under the numbers", v.Sections[1].Title)
	}
	sec := v.Sections[1]
	if !strings.Contains(sec.Footer, "小票") || !strings.Contains(sec.Footer, "周六超市") {
		t.Fatalf("footer %q", sec.Footer)
	}
	var banana wire.Component
	for _, c := range sec.Items {
		if strings.HasSuffix(c.Title, "香蕉") {
			banana = c
		}
	}
	if banana.Badge == nil || banana.Badge.Text != "有推测" || !strings.Contains(banana.Subtitle, "（推测）") {
		t.Fatalf("banana row %+v", banana)
	}
	ok, found := buttonTitledIn(sec, "确认写入")
	if !found || ok.Action.ID != "impapply:"+d.ID || !strings.Contains(ok.Action.Confirm, "会改动 4 样") {
		t.Fatalf("确认写入 %+v", ok)
	}
	if drop, found := buttonTitledIn(sec, "放弃"); !found || drop.Action.Confirm == "" {
		t.Fatalf("放弃 %+v", drop)
	}
	// The card says a draft waits.
	cv, err := a.Card().Render(t.Context(), request(state{}, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	checkView(t, "card with a draft", cv, true)
	if !strings.Contains(fmt.Sprint(cv.Sections), "有 1 份导入等你确认") {
		t.Fatalf("card %+v", cv.Sections)
	}
}

func TestConfirmOnThePageWritesAndOffersUndo(t *testing.T) {
	st := newStore(t, true)
	a, changed := newApp(st)
	d := preview(t, a, receiptInput())
	before := snapshot(t, st)
	res := act(t, a, state{}, nil, "impapply:"+d.ID)
	toastHas(t, res, "已写入")
	if *changed == 0 {
		t.Fatal("the changed hook was not called")
	}
	if got := home(t, st); len(got.Items) != 12 {
		t.Fatalf("%d items after the import", len(got.Items))
	}
	v := res.View
	checkView(t, "after apply", v, false)
	if _, ok := sectionTitled(v, "待确认的导入"); ok {
		t.Fatal("the applied draft is still shown")
	}
	last, ok := sectionTitled(v, "刚才的导入")
	if !ok {
		t.Fatal("no 刚才的导入")
	}
	undo, ok := buttonTitledIn(last, "撤销这次导入")
	if !ok || undo.Action.Confirm == "" {
		t.Fatalf("undo button %+v", last.Items)
	}
	res = act(t, a, state{}, nil, undo.Action.ID)
	toastHas(t, res, "已撤销")
	if after := home(t, st); len(after.Items) != 10 {
		t.Fatalf("%d items after undo", len(after.Items))
	}
	_ = before
	last, _ = sectionTitled(res.View, "刚才的导入")
	if _, ok := buttonTitledIn(last, "撤销这次导入"); ok || !strings.Contains(fmt.Sprint(last.Items), "已经撤销") {
		t.Fatalf("after undo %+v", last.Items)
	}
}

func TestUndoOnThePageSaysWhyNot(t *testing.T) {
	st := newStore(t, true)
	a, _ := newApp(st)
	d := preview(t, a, receiptInput())
	act(t, a, state{}, nil, "impapply:"+d.ID)
	act(t, a, state{}, nil, "use:"+entityID(t, st, model.TItem, "番茄"))
	v := render(t, a, state{})
	last, _ := sectionTitled(v, "刚才的导入")
	if _, ok := buttonTitledIn(last, "撤销这次导入"); ok || !strings.Contains(fmt.Sprint(last.Items), "「番茄」在导入之后又改过") {
		t.Fatalf("last import %+v", last.Items)
	}
}

func TestQuestionsAreAnsweredOnThePage(t *testing.T) {
	st := newStore(t, true)
	a, _ := newApp(st)
	d := preview(t, a, imports.Input{Source: imports.SrcText, Lines: []imports.Line{{Title: "牛奶", Qty: fp(1), Unit: "盒"}}})
	v := render(t, a, state{})
	sec := v.Sections[1]
	if _, ok := buttonTitledIn(sec, "确认写入"); ok {
		t.Fatal("确认写入 offered while a question is open")
	}
	same, ok := buttonTitledIn(sec, "就是「纯牛奶」")
	if !ok || !strings.Contains(fmt.Sprint(sec.Items), "要你定") {
		t.Fatalf("question %+v", sec.Items)
	}
	snap := snapshot(t, st)
	res := act(t, a, state{}, nil, same.Action.ID)
	toastHas(t, res, "已记下")
	if snapshot(t, st) != snap {
		t.Fatal("answering changed the records")
	}
	if _, ok := buttonTitledIn(res.View.Sections[1], "确认写入"); !ok {
		t.Fatalf("no 确认写入 after the answer: %+v", res.View.Sections[1].Items)
	}
	_ = d
}

func TestStaleDraftAsksToRecompute(t *testing.T) {
	st := newStore(t, true)
	a, _ := newApp(st)
	d := preview(t, a, imports.Input{Source: imports.SrcReceipt, Lines: []imports.Line{{Title: "洗衣液", Qty: fp(1), Unit: "瓶"}}})
	act(t, a, state{}, nil, "skip:"+entityID(t, st, model.TBuy, "洗衣液"))
	sec := render(t, a, state{}).Sections[1]
	if _, ok := buttonTitledIn(sec, "确认写入"); ok {
		t.Fatal("确认写入 on a stale draft")
	}
	if _, ok := buttonTitledIn(sec, "重新算一遍"); !ok {
		t.Fatalf("no 重新算一遍: %+v", sec.Items)
	}
	res := act(t, a, state{}, nil, "impreplan:"+d.ID)
	if _, ok := buttonTitledIn(res.View.Sections[1], "确认写入"); !ok {
		t.Fatalf("no 确认写入 after recomputing: %+v", res.View.Sections[1].Items)
	}
	// Discarding changes no record.
	snap := snapshot(t, st)
	res = act(t, a, state{}, nil, "impdrop:"+d.ID)
	toastHas(t, res, "已放弃")
	if snapshot(t, st) != snap {
		t.Fatal("discard changed the records")
	}
}

func TestPasteMakesADraft(t *testing.T) {
	st := newStore(t, true)
	a, _ := newApp(st)
	v := render(t, a, state{})
	bulk, ok := sectionTitled(v, "一次记很多")
	if !ok {
		t.Fatal("no 一次记很多")
	}
	if !strings.Contains(fmt.Sprint(bulk.Items), "把这张小票记到家庭管家") {
		t.Fatalf("assistant hint %+v", bulk.Items)
	}
	paste, ok := buttonTitledIn(bulk, "粘贴清单")
	if !ok || paste.Action.Push == nil {
		t.Fatalf("粘贴清单 %+v", paste)
	}
	form := decodeState(t, paste.Action.Push.State)
	fv := render(t, a, form)
	checkView(t, "paste form", fv, false)
	if fv.Title != "粘贴清单" {
		t.Fatalf("form %+v", fv)
	}
	// Empty and unreadable text is refused with a toast.
	toastHas(t, act(t, a, form, map[string]string{"paste_text": " "}, "imppaste"), "先把要记的东西粘贴进来")
	toastHas(t, act(t, a, form, map[string]string{"paste_text": "!!!\n123"}, "imppaste"), "一行都没读懂")
	snap := snapshot(t, st)
	res := act(t, a, form, map[string]string{"paste_text": "番茄 2 个\n???\n鸡蛋 十个 2026-12-05", "paste_target": "=stock", "paste_mode": "=add"}, "imppaste")
	toastHas(t, res, "读到 2 样")
	if !res.Pop || snapshot(t, st) != snap {
		t.Fatalf("paste result %+v, or the records changed", res)
	}
	ov := render(t, a, state{})
	sec := ov.Sections[1]
	if sec.Title != "待确认的导入" || !strings.Contains(sec.Footer, "粘贴的清单") || !strings.Contains(fmt.Sprint(sec.Items), "没读懂") {
		t.Fatalf("draft section %+v", sec)
	}
}

func TestEmptyHouseholdOffersPasteAndShowsDrafts(t *testing.T) {
	st := newStore(t, false)
	a, _ := newApp(st)
	v := render(t, a, state{})
	if _, ok := sectionTitled(v, "一次记很多"); !ok {
		t.Fatal("no 一次记很多 on the empty overview")
	}
	preview(t, a, imports.Input{Source: imports.SrcText, Lines: []imports.Line{{Title: "牛奶", Qty: fp(2), Unit: "盒"}}})
	v = render(t, a, state{})
	checkView(t, "empty overview with a draft", v, false)
	if v.Sections[0].Items[0].Type != wire.ComponentEmpty || v.Sections[1].Title != "待确认的导入" {
		t.Fatalf("sections %q %q", v.Sections[0].Items[0].Type, v.Sections[1].Title)
	}
}

func TestOverviewWithManyDraftsStaysInBudget(t *testing.T) {
	st := newStore(t, true)
	a, _ := newApp(st)
	var lines []imports.Line
	for i := range imports.MaxLines {
		lines = append(lines, imports.Line{Title: fmt.Sprintf("东西%d", i), Qty: fp(1)})
	}
	lines[0] = imports.Line{Title: "牛奶", Qty: fp(1), Unit: "盒"} // a question with its buttons
	for range imports.DraftsMost {
		preview(t, a, imports.Input{Source: imports.SrcText, Lines: lines,
			Unread: []imports.Unread{{Line: 61, Text: "?", Why: "看不出"}, {Line: 62, Text: "?", Why: "看不出"},
				{Line: 63, Text: "?", Why: "看不出"}, {Line: 64, Text: "?", Why: "看不出"}, {Line: 65, Text: "?", Why: "看不出"},
				{Line: 66, Text: "?", Why: "看不出"}}})
	}
	v := render(t, a, state{})
	checkView(t, "overview with many drafts", v, false)
	n := 0
	for _, s := range v.Sections {
		if strings.Contains(s.Title, "待确认的导入") {
			n++
		}
	}
	if n != draftsShown || !strings.Contains(fmt.Sprint(v.Sections), "还有 2 份更早的导入") {
		t.Fatalf("%d drafts shown", n)
	}
}
