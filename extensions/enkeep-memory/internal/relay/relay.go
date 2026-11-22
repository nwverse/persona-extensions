// Package relay tells a user-role text that is not the user's own words (0.2.0-beta.37, docs/sources.md "Who said
// it"): a task prompt Persona or the assistant sent an agent, a supervisor's or the assistant's relay, the context the
// hub puts in front of a prompt, a test prompt, and material pasted in. In agent sessions and the assistant's
// conversation the model role "user" is whoever typed into the session, and before the hub marked automated inputs
// (proto.Item.Source, Injected, From) those all looked like the user; entries such as "the user wants only OK as the
// answer" came from them. The rules look at the structure of the text only, never at any one user's names or
// projects.
package relay

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// What a user-role text is when it is not the user's words.
const (
	None    = ""        // the user's own
	Persona = "persona" // a task prompt, a relay, hub context
	Test    = "test"    // a probe: "reply only OK, call no tool"
)

// markers make a whole text Persona's: the hub's context block, its task-handoff boilerplate and result-file note, a
// cross-session message, a relay marked as such.
var markers = []string{
	"<persona-context", "<jarvis-context", "Persona task handoffs:", "<cross-session-message", "[Cross-session",
	"（Persona：任务的结果", "<task-notification",
	"经主对话转", "主对话转达", "主管转达", "（转自主管", "relayed by the supervisor", "relayed from the assistant",
}

// probe: a short text that asks for a fixed answer or no tool calls, the shape of a deployment or smoke test.
var (
	probeAnswer = regexp.MustCompile(`(?i)(只|仅|只要)(需要)?(回复|回答|回)|reply (only )?with|respond (only )?with|answer (only )?with|just (reply|say|answer)|reply only`)
	probeTools  = regexp.MustCompile(`(?i)不要?(调用|使用|用)(任何)?工具|别(调用|用)工具|(do not|don't) (call|use) (any )?tools?|without (calling|using) (any )?tools?`)
	probeTest   = regexp.MustCompile(`(?i)(部署|上线|冒烟|连通|联通)?(验证|测试|自检)|smoke|ping|health ?check|deploy(ment)? (check|test|verification)`)
)

const probeMost = 120 // runes: a probe is a line or two

// Classify says what a user-role text is: None when it is the user's own words.
func Classify(text string) string {
	for _, m := range markers {
		if strings.Contains(text, m) {
			return Persona
		}
	}
	own, _ := SplitPaste(text)
	t := strings.TrimSpace(own)
	if t != "" && utf8.RuneCountInString(t) <= probeMost {
		a, tl, ts := probeAnswer.MatchString(t), probeTools.MatchString(t), probeTest.MatchString(t)
		if (a && tl) || (a && ts) || (tl && ts) {
			return Test
		}
	}
	return None
}

var pasted = regexp.MustCompile(`(?s)<pasted_content\b[^>]*>.*?</pasted_content\b[^>]*>`)

// SplitPaste takes out the blocks the client marks as pasted (<pasted_content …>…</pasted_content …>): what is left is
// what the user typed; pasted says how many characters were pasted. A pasted block is material, not the user's words,
// even when the user pasted it.
func SplitPaste(text string) (own string, pastedRunes int) {
	own = pasted.ReplaceAllStringFunc(text, func(m string) string {
		pastedRunes += utf8.RuneCountInString(m)
		return " "
	})
	return strings.TrimSpace(own), pastedRunes
}

// PasteMark is the short mark left where pasted material was taken out.
func PasteMark(n int) string { return fmt.Sprintf("[粘贴材料 %d 字]", n) }
