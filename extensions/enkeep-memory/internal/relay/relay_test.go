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
