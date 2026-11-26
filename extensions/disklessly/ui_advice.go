package main

import (
	"fmt"
	"strings"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The 「帮我腾空间」 block of 硬盘空间 shows the first steps of disklessly_advise in plain words:
// each with its gain, its risk and the existing button that does it. Its rows only open the
// page that has that button; it adds no button that deletes or moves anything.

const (
	adviceTitle = "帮我腾空间"
	// adviceShown is how many steps the block lists: a few lines, so the summary above stays in sight.
	adviceShown = 3
	// assistantRowTitle is the row that says what to tell the Persona assistant.
	assistantRowTitle = "让助手帮我腾空间"
	// assistantPrompt is the sentence to say; the quick prompt of the manifest carries it too.
	assistantPrompt = "帮我腾点空间"
)

// adviceSection is the block: at most adviceShown step rows and the row about the assistant;
// what else there is goes in the footer, which costs no component.
func (d draw) adviceSection() wire.Section {
	sec := wire.Section{Key: "advice", Title: adviceTitle}
	adv, err := buildAdvice(d.st, d.now, adviceOptions{})
	var items []wire.Component
	switch {
	case err != nil:
		// buildAdvice fails only for an unknown machine, never for all of them.
	case adviceUnchecked(adv):
		items = append(items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
			Text: "检查过以后，这里会按先后列出先删什么、能腾出多少、有什么风险。"})
	case len(adv.Steps) == 0:
		text := "按上次检查的结果，现在没有能腾出来的东西。"
		if len(adv.LookFirst) > 0 {
			text = "放心删的都删过了，只剩「建议先看」的，删不删你自己定（在「" + planText + "」里看）。"
		}
		items = append(items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary, Text: text})
	default:
		for _, s := range adv.Steps[:min(len(adv.Steps), adviceShown)] {
			items = append(items, d.adviceRow(s))
		}
		var more []string
		if n := len(adv.Steps) - adviceShown; n > 0 {
			more = append(more, fmt.Sprintf("还有 %d 步，助手那里能看到全部", n))
		}
		if n := len(adv.LookFirst); n > 0 {
			var b uint64
			for _, s := range adv.LookFirst {
				b += s.FreesBytes
			}
			more = append(more, fmt.Sprintf("另有 %d 项「建议先看」的（%s）默认不删，在「%s」里自己决定", n, FormatBytes(b), planText))
		}
		if len(more) > 0 {
			sec.Footer = clip(strings.Join(more, "；")+"。", 300)
		}
	}
	sec.Items = append(items, wire.Component{
		Type:     wire.ComponentRow,
		Key:      "advice-assistant",
		Icon:     "chat",
		Title:    assistantRowTitle,
		Subtitle: "对 Persona 助手说「" + assistantPrompt + "」（消息框的快捷提示里也有）。它会照这个顺序讲每一步腾出多少、有什么风险，问你做哪一步；每删一样、每搬一次都要你点头。",
	})
	return sec
}

// adviceUnchecked reports that no computer of adv was checked yet.
func adviceUnchecked(adv *advice) bool {
	for _, am := range adv.Machines {
		if am.CheckedAt != nil {
			return false
		}
	}
	return true
}

// adviceRow is one step: what, how much, the risk, and where its existing button is. Tapping it
// opens that page (the computer's 能删什么, or its AI 对话), never the deletion itself.
func (d draw) adviceRow(s adviceStep) wire.Component {
	m := d.st.machine(s.MachineID)
	title := fmt.Sprintf("%d. %s：删掉%s", s.Rank, s.MachineName, s.KindName)
	if s.Action == adviceMove {
		title = fmt.Sprintf("%d. %s：把%s搬到网盘", s.Rank, s.MachineName, s.KindName)
	}
	badge := &wire.Badge{Text: "+" + FormatBytes(s.FreesBytes), Tone: wire.TonePositive}
	var where string
	switch {
	case !s.Possible:
		badge.Tone = wire.ToneNeutral
		where = "现在还做不了：" + s.short + "。"
		if s.pageTab == sessionsUI {
			where += "到「" + sessionsTitle + "」页看怎么处理。"
		} else {
			where += "等它开机联网后重新检查。"
		}
	case m != nil && d.busy(m):
		where = "这台电脑正在忙别的，忙完再做。"
	case s.pageTab == sessionsUI:
		where = "在「" + sessionsTitle + "」页这台电脑那一行用「" + d.moveButton(s.MachineID) + "」。"
	case len(s.PageButtons) == 1:
		where = "在「能删什么」里用「" + s.PageButtons[0] + "」那一行。"
	default:
		where = "在「能删什么」的「每一项」里逐项删（同类里「建议先看」的那项不在这一步）。"
	}
	act := &wire.Action{ID: "select-machine", Values: map[string]string{"m": s.MachineID, "t": tabItems}}
	if s.pageTab == sessionsUI {
		act = &wire.Action{ID: "open-sessions", Push: &wire.Push{UI: sessionsUI, Title: sessionsTitle, State: ext.EncodeState(sessPage{Machine: s.MachineID})}}
	}
	return wire.Component{
		Type:     wire.ComponentRow,
		Key:      boundedKey(fmt.Sprintf("advice-%d-", s.Rank), s.MachineID),
		Icon:     "check",
		Title:    clip(title, 200),
		Subtitle: clip(s.Risk+where, 400),
		Badge:    badge,
		Action:   act,
	}
}

// moveButton is the AI 对话 page's upload button of machine id as it is drawn now: 重试 after a
// failed upload.
func (d draw) moveButton(id string) string {
	if j, ok := d.jobs[id]; ok && !j.running() && j.Kind == jobOffload && j.Err != "" {
		return coldRetryText
	}
	return coldOffloadText
}
