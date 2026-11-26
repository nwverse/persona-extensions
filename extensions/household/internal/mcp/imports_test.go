package mcp

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona-extensions/extensions/household/internal/model"
)

// receipt is what an assistant would send after reading a made-up supermarket receipt.
const receipt = `{"source":"receipt","note":"周六超市","lines":[
	{"title":"纯牛奶","qty":2,"unit":"盒","price":12},
	{"title":"鸡蛋","qty":12,"unit":"个","expires":"2026-12-30","price":15.8},
	{"title":"洗衣液","qty":1,"unit":"瓶","price":29.9},
	{"title":"香蕉","qty":1,"unit":"把","price":8.5}
]}`

func lineOf(t *testing.T, preview any, n int) map[string]any {
	t.Helper()
	for _, l := range get(t, preview, "lines").([]any) {
		if get(t, l, "line") == float64(n) {
			return asMap(t, l)
		}
	}
	t.Fatalf("no line %d in %v", n, preview)
	return nil
}

func TestImportFromAReceiptEndToEnd(t *testing.T) {
	s := start(t, seeded(t, true))
	defer s.finish()
	statusBefore := s.done("household_status", `{}`)

	pre := s.done("household_import_preview", oneLine(receipt))
	id := asStr(t, get(t, pre, "draft_id"))
	if get(t, lineOf(t, pre, 1), "action") != "add_to_item" || get(t, lineOf(t, pre, 3), "action") != "new_item" {
		t.Fatalf("preview lines %v", get(t, pre, "lines"))
	}
	// The manual entry 洗衣液 on the shopping list is marked bought with the new item.
	if get(t, lineOf(t, pre, 3), "buy", "title") != "洗衣液" {
		t.Fatalf("detergent line %v", lineOf(t, pre, 3))
	}
	// The banana's category is inferred, and said so.
	banana := lineOf(t, pre, 4)
	if inf := get(t, banana, "inferred").([]any); len(inf) == 0 || !strings.Contains(asStr(t, get(t, banana, "says")), "推测") {
		t.Fatalf("banana %v", banana)
	}
	if !strings.Contains(asStr(t, get(t, pre, "summary")), "合计") || get(t, pre, "totals", "price_total") != 66.2 {
		t.Fatalf("summary %v totals %v", get(t, pre, "summary"), get(t, pre, "totals"))
	}
	// The preview changed no record.
	if after := s.done("household_status", `{}`); fmt.Sprint(after) != fmt.Sprint(statusBefore) {
		t.Fatal("preview changed the records")
	}
	// The draft is listed.
	if l := s.done("household_import_list", `{}`); get(t, l, "drafts", 0, "draft_id") != id {
		t.Fatalf("list %v", l)
	}

	done := s.done("household_import_apply", fmt.Sprintf(`{"draft_id":%q}`, id))
	if len(get(t, done, "done").([]any)) != 4 {
		t.Fatalf("applied %v", done)
	}
	milk := rec(t, s, model.TItem, "纯牛奶")
	if get(t, milk, "qty") != float64(2) {
		t.Fatalf("milk after import %v (the expired batch is gone, only the new 2 count)", milk)
	}
	eggs := rec(t, s, model.TItem, "鸡蛋")
	if get(t, eggs, "qty") != float64(12) || get(t, eggs, "expires") != "2026-12-30" {
		t.Fatalf("eggs after import %v", eggs)
	}
	if b := rec(t, s, model.TBuy, "洗衣液"); get(t, b, "status") != "已买" {
		t.Fatalf("detergent purchase %v", b)
	}
	list := s.done("household_import_list", `{}`)
	if get(t, list, "last_import", "can_undo") != true || len(get(t, list, "drafts").([]any)) != 0 {
		t.Fatalf("list after apply %v", list)
	}
	// Applying twice is refused: the draft is gone.
	if msg := s.failed("household_import_apply", fmt.Sprintf(`{"draft_id":%q}`, id)); !strings.Contains(msg, "没有草稿") {
		t.Fatalf("second apply: %s", msg)
	}

	undone := s.done("household_import_undo", `{}`)
	if get(t, undone, "removed") != float64(2) {
		t.Fatalf("undo %v", undone)
	}
	if after := s.done("household_status", `{}`); fmt.Sprint(get(t, after, "summary")) != fmt.Sprint(get(t, statusBefore, "summary")) {
		t.Fatalf("status after undo %v, before %v", get(t, after, "summary"), get(t, statusBefore, "summary"))
	}
	if msg := s.failed("household_import_undo", `{}`); !strings.Contains(msg, "撤销过") {
		t.Fatalf("second undo: %s", msg)
	}
}

func TestImportQuestionsAndAnswers(t *testing.T) {
	s := start(t, seeded(t, true))
	defer s.finish()
	pre := s.done("household_import_preview", `{"source":"text","lines":[{"title":"牛奶","qty":1,"unit":"盒"},{"title":"番茄","qty":1,"unit":"斤"}]}`)
	id := asStr(t, get(t, pre, "draft_id"))
	milk := lineOf(t, pre, 1)
	if get(t, milk, "action") != "ask" || get(t, milk, "question", "kind") != "same_item" {
		t.Fatalf("milk %v", milk)
	}
	if get(t, lineOf(t, pre, 2), "question", "kind") != "unit" {
		t.Fatalf("tomato %v", lineOf(t, pre, 2))
	}
	if msg := s.failed("household_import_apply", fmt.Sprintf(`{"draft_id":%q}`, id)); !strings.Contains(msg, "要用户定") {
		t.Fatalf("apply with questions: %s", msg)
	}
	milkID := idOf(t, s, model.TItem, "纯牛奶")
	// Re-preview with the answers, then apply.
	again := s.done("household_import_preview", fmt.Sprintf(`{"draft_id":%q,"edits":[{"line":1,"match":%q},{"line":2,"qty":3,"unit":"个"}]}`, id, milkID))
	if get(t, lineOf(t, again, 1), "action") != "add_to_item" || get(t, lineOf(t, again, 2), "after") != float64(7) {
		t.Fatalf("re-preview %v", get(t, again, "lines"))
	}
	s.done("household_import_apply", fmt.Sprintf(`{"draft_id":%q}`, id))
	if got := get(t, rec(t, s, model.TItem, "番茄"), "qty"); got != float64(7) {
		t.Fatalf("tomato %v", got)
	}
}

func TestImportUndoRefusedAfterALaterChange(t *testing.T) {
	s := start(t, seeded(t, true))
	defer s.finish()
	pre := s.done("household_import_preview", `{"source":"order_screenshot","lines":[{"title":"番茄","qty":2,"unit":"个"}]}`)
	s.done("household_import_apply", fmt.Sprintf(`{"draft_id":%q}`, asStr(t, get(t, pre, "draft_id"))))
	s.done("household_use", fmt.Sprintf(`{"id":%q,"amount":1}`, idOf(t, s, model.TItem, "番茄")))
	if msg := s.failed("household_import_undo", `{}`); !strings.Contains(msg, "「番茄」在导入之后又改过") {
		t.Fatalf("undo: %s", msg)
	}
	if l := s.done("household_import_list", `{}`); get(t, l, "last_import", "can_undo") != false || get(t, l, "last_import", "why_not") == "" {
		t.Fatalf("list %v", l)
	}
}

func TestImportDiscardAndBadInput(t *testing.T) {
	s := start(t, seeded(t, false))
	defer s.finish()
	pre := s.done("household_import_preview", `{"source":"fridge_photo","lines":[{"title":"酸奶","qty":3,"unit":"杯"}]}`)
	id := asStr(t, get(t, pre, "draft_id"))
	if get(t, pre, "mode") != "count" {
		t.Fatalf("fridge photo mode %v", get(t, pre, "mode"))
	}
	s.done("household_import_discard", fmt.Sprintf(`{"draft_id":%q}`, id))
	if msg := s.failed("household_import_apply", fmt.Sprintf(`{"draft_id":%q}`, id)); !strings.Contains(msg, "没有草稿") {
		t.Fatalf("apply after discard: %s", msg)
	}
	for name, args := range map[string]string{
		"no source":     `{"lines":[{"title":"x"}]}`,
		"bad source":    `{"source":"fax","lines":[{"title":"x"}]}`,
		"no lines":      `{"source":"text","lines":[]}`,
		"bad date":      `{"source":"text","lines":[{"title":"牛奶","expires":"明天"}]}`,
		"unknown field": `{"source":"text","lines":[{"title":"牛奶","brand":"x"}]}`,
		"edits alone":   `{"source":"text","lines":[{"title":"牛奶"}],"edits":[{"line":1}]}`,
		"draft and new": `{"draft_id":"imp-1","source":"text"}`,
	} {
		if msg := s.failed("household_import_preview", args); msg == "" {
			t.Errorf("%s: no message", name)
		}
	}
}

// oneLine puts a JSON text on one line: the protocol is one message per line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
