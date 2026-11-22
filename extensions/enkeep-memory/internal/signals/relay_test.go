package signals

import (
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/enkeep-memory/internal/hubapi"
)

// userMsgs counts only the user's words (design v2 §4.2): a session Persona started, a relay and a probe are not
// the user's; pasted material is taken out; a relayed quote counts once, and only when the ledger has it by the user.
func TestUserMsgsByRelay(t *testing.T) {
	day := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	at := day.Add(10 * time.Hour)
	own := &session{Session: hubapi.Session{ID: "own"}, Ref: "agents/m/own", start: day, end: day.AddDate(0, 0, 1), Items: []hubapi.Item{
		{Seq: 1, At: at, Kind: kAgent, Text: "已改好，要不要再跑一遍？"},
		{Seq: 2, At: at, Kind: kUser, Text: "不是这个文件"},
		{Seq: 3, At: at, Kind: kUser, Text: "部署验证：请只回复 OK，不要调用任何工具。"},
		{Seq: 4, At: at, Kind: kUser, Text: "<pasted_content id=\"p\">占位日志</pasted_content id=\"p\">"},
		{Seq: 5, At: at, Kind: kUser, Text: "（占位主管经主对话转）## 用户原话\n- 「以后先跑门禁再合并」"},
	}}
	task := &session{Session: hubapi.Session{ID: "task", ByPersona: true}, Ref: "agents/m/task", start: day, end: day.AddDate(0, 0, 1), Items: []hubapi.Item{
		{Seq: 1, At: at, Kind: kUser, Text: "## 用户原话\n- 「汇报只写结论」\n把占位服务升级"},
		{Seq: 2, At: at, Kind: kUser, Text: "主管：继续，按计划做"},
	}}
	g := &gathered{Sessions: []*session{own, task}}
	ms := g.userMsgs()
	if len(ms) != 1 || ms[0].Text != "不是这个文件" {
		t.Fatalf("without a ledger only the user's own words: %+v", ms)
	}
	g.Ledger = []string{"汇报只写结论", "不是这个文件"}
	ms = g.userMsgs()
	if len(ms) != 2 || ms[1].Text != "汇报只写结论" || !ms[1].Relayed {
		t.Fatalf("a ledger-verified relayed quote counts once: %+v", ms)
	}
}

// A short reply to the AI goes to the correction judge whatever its words; a bare agreement does not.
func TestShortReply(t *testing.T) {
	for text, want := range map[string]bool{"？？？": true, "又来了": true, "好的": false, "ok": false} {
		if got := shortReply(msg{Text: text, Prior: "改好了"}); got != want {
			t.Errorf("shortReply(%q) = %v", text, got)
		}
	}
	if shortReply(msg{Text: "又来了"}) {
		t.Error("no AI line before it: not a reply")
	}
}
