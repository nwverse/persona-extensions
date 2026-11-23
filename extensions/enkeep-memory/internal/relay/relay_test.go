package relay

import "testing"

// Synthetic texts only: the shapes of a task prompt, a relay, a probe and a paste.
func TestClassify(t *testing.T) {
	for text, want := range map[string]string{
		"部署验证：请只回复 OK，不要调用任何工具。":                                         Test,
		"Deployment check: reply only with OK, do not call any tools.":   Test,
		"<persona-context kind=\"context\">…</persona-context>\n把占位服务升级": Persona,
		"（占位主管经主对话转）把占位服务升级到最新":                                          Persona,
		"做完后\n\nPersona task handoffs: use task_spawn …":                 Persona,
		"<cross-session-message from=\"x\">请看一下</cross-session-message>": Persona,
		"这个方案先别上线，等周五评审完再说":                                              None,
		"只回复要点就行，别写长文":                                                   None, // a fixed answer alone is not a probe
		"测试一下这个接口为什么超时":                                                  None,
	} {
		if got := Classify(text); got != want {
			t.Errorf("Classify(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestSplitPaste(t *testing.T) {
	own, n := SplitPaste("看看这个\n<pasted_content id=\"a1\">\n用简体中文，直接做，不进计划模式。\n</pasted_content id=\"a1\">")
	if own != "看看这个" || n == 0 {
		t.Fatalf("own %q, pasted %d", own, n)
	}
	own, n = SplitPaste("<pasted_content id=\"b2\">占位材料</pasted_content id=\"b2\">")
	if own != "" || n == 0 {
		t.Fatalf("own %q, pasted %d", own, n)
	}
	if own, n := SplitPaste("没有粘贴"); own != "没有粘贴" || n != 0 {
		t.Fatal(own, n)
	}
}

// Synthetic relay: a "用户原话" block with a lead-in, bracketed words, and an inline "用户说「…」".
func TestQuotes(t *testing.T) {
	text := "<persona-context>…</persona-context>\n用简体中文，直接做。\n\n## 用户原话\n11-02 09:15，用户在占位卡片上定了两点：\n" +
		"- 占位开关：「默认开」\n- 历史范围：「直接全补占位月份」\n\n## 顺序\n第 1 步……\n另外用户说「先跑门禁再合并」，照办。"
	got := Quotes(text)
	want := []string{"默认开", "直接全补占位月份", "先跑门禁再合并"}
	if len(got) != len(want) {
		t.Fatalf("Quotes = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Quotes = %q, want %q", got, want)
		}
	}
	// A block line without brackets is taken whole; a lead-in ending in a colon is not.
	got = Quotes("### 原话\n- 以后汇报先写结论\n用户补充：\n")
	if len(got) != 1 || got[0] != "以后汇报先写结论" {
		t.Fatalf("Quotes = %q", got)
	}
	if got := Quotes("把占位服务升级到最新版本，跑完门禁报给我"); len(got) != 0 {
		t.Fatalf("no block, no quotes: %q", got)
	}
}

func TestThree(t *testing.T) {
	if Three("我", "先跑门禁", "我") != WhoUser || Three("我", RelayedMark+"先跑门禁", "我") != WhoRelayed ||
		Three("Persona", "先跑门禁", "我") != WhoAgent {
		t.Fatal("three values")
	}
}
