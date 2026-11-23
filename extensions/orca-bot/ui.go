package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The page under the extension's details (Settings › Extensions › Orca 机器人 › Orca 机器人, an extension_page). The
// settings form has no list filled in at run time, so the bot is picked here: the page lists the bots that Orca on
// the chosen Mac reports (bots.list, through the node program), and tapping one keeps it in the extension's own
// state. It also says whether forwarding runs and why not, shows the exact MCP command to register with the bot, and
// the latest hand-overs. Only components every app draws (UI version 3).

const (
	uiBot       = "bot"
	pageTimeout = 8 * time.Second
	recentShown = 20
)

func (a *hubApp) page() ext.Page {
	return ext.Page{
		Render: func(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
			if r.Contribution != "" && r.Contribution != uiBot {
				return nil, ext.NoContribution(r.Contribution)
			}
			return a.view(ctx, r.TZ), nil
		},
		Action: func(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
			if r.Contribution != "" && r.Contribution != uiBot {
				return nil, ext.NoContribution(r.Contribution)
			}
			toast := a.act(ctx, strings.TrimSpace(r.Action))
			return &wire.ActionResult{View: a.view(ctx, r.TZ), Toast: toast}, nil
		},
	}
}

type botList struct {
	Bots    []orcaBot `json:"bots"`
	Enabled bool      `json:"enabled"`
}

func (a *hubApp) fetchBots(ctx context.Context, machine string) (*botList, error) {
	a.mu.Lock()
	nodes := a.nodes
	a.mu.Unlock()
	if nodes == nil {
		return nil, fmt.Errorf("the node program cannot be reached")
	}
	cctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	var out botList
	if err := nodes.Call(cctx, machine, "orcabot.bots", map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// act does what a tap asked; the toast to show.
func (a *hubApp) act(ctx context.Context, action string) string {
	kind, arg, _ := strings.Cut(action, ":")
	switch kind {
	case "refresh", "":
		return ""
	case "clear":
		a.mu.Lock()
		a.st.Bot = nil
		a.rearm()
		a.settle() // what still waited is not handed over any more
		a.save()
		a.mu.Unlock()
		return "不再转发：没有选机器人"
	case "scope":
		a.mu.Lock()
		machine, bot := a.cfg.machine(), a.st.Bot
		nodes := a.nodes
		a.mu.Unlock()
		if bot == nil || machine == "" || nodes == nil || (arg != "wide" && arg != "chat") {
			return "先选一个机器人"
		}
		cctx, cancel := context.WithTimeout(ctx, pageTimeout)
		defer cancel()
		if err := nodes.Call(cctx, machine, "orcabot.set_scope", map[string]any{"bot_id": bot.ID, "wide": arg == "wide"}, nil); err != nil {
			return "没能改：" + failureWords(err)
		}
		if arg == "wide" {
			return "已放宽：这个机器人的任何对话都能读写交给它的待办"
		}
		return "已收紧：只有接到待办的那个对话能读写它"
	case "pick":
		a.mu.Lock()
		machine := a.cfg.machine()
		a.mu.Unlock()
		if machine == "" || !safeID.MatchString(arg) {
			return "先在设置里选一台 Mac"
		}
		list, err := a.fetchBots(ctx, machine)
		if err != nil {
			return "读不到那台 Mac 上的机器人：" + failureWords(err)
		}
		for _, b := range list.Bots {
			if b.ID != arg {
				continue
			}
			a.mu.Lock()
			a.st.Bot = &botChoice{ID: b.ID, Name: clip(cleanText(b.Name), 80), Machine: machine, ChosenAt: a.now()}
			a.rearm()
			a.settle() // what still waited for the old bot is not handed to it any more
			a.save()
			a.mu.Unlock()
			return fmt.Sprintf("以后符合条件的新待办会交给「%s」", clip(b.Name, 40))
		}
		return "那台 Mac 上的 Orca 里没有这个机器人了"
	}
	return "未知的操作"
}

func (a *hubApp) view(ctx context.Context, tz string) *wire.View {
	v := &wire.View{Title: "Orca 机器人"}
	a.mu.Lock()
	cfg := a.cfg
	bot := a.st.Bot
	ok, missing := a.ready()
	lastErr := a.lastErr
	armedAt := a.st.ArmedAt
	var recent []record
	for _, r := range a.st.Records {
		recent = append(recent, *r)
	}
	nodes := a.nodes
	a.mu.Unlock()
	loc, err := time.LoadLocation(tz)
	if tz == "" || err != nil {
		loc = time.Local
	}

	machine := cfg.machine()
	if machine == "" {
		v.Sections = []wire.Section{
			{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: "还没选 Mac", Icon: "settings",
				Detail: "先在这个扩展的设置里选一台开着 Orca 的 Mac，再回到这一页选机器人。"}}},
			setupSection(),
		}
		return v
	}

	// What the node program says of Orca, and the bots.
	var st nodeStatus
	var stErr error
	var list *botList
	var listErr error
	if nodes != nil {
		cctx, cancel := context.WithTimeout(ctx, pageTimeout)
		params := map[string]any{}
		if bot != nil && bot.Machine == machine {
			params["bot_id"] = bot.ID
		}
		stErr = nodes.Call(cctx, machine, "orcabot.status", params, &st)
		cancel()
		if stErr == nil && st.OrcaRunning {
			list, listErr = a.fetchBots(ctx, machine)
		}
	} else {
		stErr = fmt.Errorf("the node program cannot be reached")
	}

	status := wire.Section{Title: "状态"}
	if ok {
		status.Items = append(status.Items, wire.Component{Type: wire.ComponentRow, Title: "正在转发", Icon: "check", Tone: "positive",
			Subtitle: "只转发 " + armedAt.In(loc).Format("2006-01-02 15:04") + " 以后新建的、符合条件的待办"})
	} else {
		status.Items = append(status.Items, wire.Component{Type: wire.ComponentRow, Title: "没有在转发", Subtitle: missing, Icon: "warning", Tone: "warning"})
	}
	orcaRow := wire.Component{Type: wire.ComponentRow, Title: "Orca"}
	switch {
	case stErr != nil:
		orcaRow.Subtitle, orcaRow.Icon, orcaRow.Tone = "连不上那台 Mac 上的扩展程序：它可能离线，或程序还没启动", "warning", "danger"
	case !st.OrcaRunning:
		orcaRow.Subtitle, orcaRow.Icon, orcaRow.Tone = orcaWords(st.ErrorCode), "warning", "danger"
	default:
		orcaRow.Subtitle, orcaRow.Icon, orcaRow.Tone = strings.TrimSpace("运行中 "+st.OrcaVersion), "check", "positive"
	}
	status.Items = append(status.Items, orcaRow)
	botRow := wire.Component{Type: wire.ComponentRow, Title: "机器人", Subtitle: "没选"}
	if bot != nil && bot.Machine == machine {
		botRow.Subtitle = bot.Name
	}
	status.Items = append(status.Items, botRow,
		wire.Component{Type: wire.ComponentRow, Title: "转发哪些待办", Subtitle: filterWords(cfg)})
	if lastErr != "" {
		status.Items = append(status.Items, wire.Component{Type: wire.ComponentText, Text: lastErr, Style: wire.TextStyleSecondary})
	}
	if st.PollError != "" {
		status.Items = append(status.Items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
			Text: "跟进机器人的对话时出错，会继续重试：" + clip(st.PollError, 200)})
	}
	status.Items = append(status.Items, wire.Component{Type: wire.ComponentButton, Title: "刷新", Action: &wire.Action{ID: "refresh"}})

	pick := wire.Section{Title: "选一个机器人",
		Footer: "列表来自那台 Mac 上的 Orca。换了机器人或筛选条件后，只转发之后新建的待办。"}
	switch {
	case stErr != nil || !st.OrcaRunning:
		pick.Items = []wire.Component{{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
			Text: "Orca 在那台 Mac 上运行起来以后，这里会列出它的机器人。"}}
	case listErr != nil:
		pick.Items = []wire.Component{{Type: wire.ComponentText, Style: wire.TextStyleSecondary, Text: "读不到机器人列表：" + failureWords(listErr)}}
	case !list.Enabled:
		pick.Items = []wire.Component{{Type: wire.ComponentText, Text: "Orca 里的机器人功能关着：在 Orca 里打开后再来选。"}}
	case len(list.Bots) == 0:
		pick.Items = []wire.Component{{Type: wire.ComponentEmpty, Title: "Orca 里还没有机器人", Icon: "list",
			Detail: "先在 Orca 里建一个机器人，再回到这一页。"}}
	default:
		bots := append([]orcaBot(nil), list.Bots...)
		sort.SliceStable(bots, func(i, j int) bool { return bots[i].Name < bots[j].Name })
		found := false
		for _, b := range bots {
			name := clip(cleanText(b.Name), 80)
			row := wire.Component{Type: wire.ComponentRow, Title: name, Subtitle: botWords(b)}
			if bot != nil && bot.Machine == machine && bot.ID == b.ID && !bot.Gone {
				row.Icon, row.Tone, row.Subtitle = "check", "positive", strings.TrimSpace("正在用 · "+row.Subtitle)
				found = true
			} else {
				row.Action = &wire.Action{ID: "pick:" + b.ID, Confirm: fmt.Sprintf("以后符合条件的新待办交给「%s」？", clip(name, 40))}
			}
			pick.Items = append(pick.Items, row)
		}
		if bot != nil && bot.Machine == machine && (!found || bot.Gone) {
			sub := "转发暂停了，请重新选一个"
			if found {
				sub = "Orca 曾经找不到它，转发暂停了：点它重新选上即可恢复"
			}
			pick.Items = append([]wire.Component{{Type: wire.ComponentRow, Title: "选过的「" + bot.Name + "」不在列表里了",
				Subtitle: sub, Icon: "warning", Tone: "danger"}}, pick.Items...)
		}
	}
	if bot != nil {
		pick.Items = append(pick.Items, wire.Component{Type: wire.ComponentButton, Title: "不再转发给机器人",
			Action: &wire.Action{ID: "clear", Confirm: "不再把新待办交给机器人？"}})
	}

	v.Sections = []wire.Section{status, pick}
	if bot != nil && bot.Machine == machine {
		v.Sections = append(v.Sections, mcpSection(st, bot))
	}
	v.Sections = append(v.Sections, recentSection(recent, loc), setupSection())
	return v
}

func orcaWords(code string) string {
	switch code {
	case "orca_not_running":
		return "没在运行：打开那台 Mac 上的 Orca"
	case "orca_incompatible":
		return "版本不兼容：请更新 Orca 或本扩展"
	case "orca_timeout":
		return "没有及时回应"
	}
	return "读不到它的状态"
}

func botWords(b orcaBot) string {
	var parts []string
	if b.Agent != "" {
		parts = append(parts, clip(cleanText(b.Agent), 30))
	}
	if b.Paused {
		parts = append(parts, "已暂停")
	}
	return strings.Join(parts, " · ")
}

func filterWords(c config) string {
	var parts []string
	if p := strings.TrimSpace(c.Project); p != "" {
		parts = append(parts, "项目 "+clip(p, 60))
	}
	if kws := c.keywords(); len(kws) > 0 {
		parts = append(parts, "关键词 "+clip(strings.Join(kws, "、"), 120))
	}
	if len(parts) == 0 {
		return "没设置：在设置里填项目或关键词"
	}
	return strings.Join(parts, "，并且")
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// shellWord quotes s for a POSIX shell when it needs it.
func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// mcpCommand is the command line Orca runs for the bot's tool.
func mcpCommand(link, socket, botID, token string) string {
	return shellWord(link) + " mcp --socket " + shellWord(socket) + " --bot " + shellWord(botID) + " --token " + shellWord(token)
}

// inDoubleQuotes escapes s for a POSIX shell's double quotes: \, ", $ and ` lose their meaning.
func inDoubleQuotes(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`").Replace(s)
}

// toolsAddCommand is what the owner runs once on the Mac to give the bot the tool. The bot is named by its id, which
// orca bot tools add --bot takes as well as a name (Orca's findBot matches the id first).
func toolsAddCommand(link, socket, token string, bot *botChoice) string {
	return `orca bot tools add "` + inDoubleQuotes(mcpCommand(link, socket, bot.ID, token)) + `" --name persona-todos --bot ` +
		shellWord(bot.ID)
}

func mcpSection(st nodeStatus, bot *botChoice) wire.Section {
	sec := wire.Section{Title: "让机器人能写回待办",
		Footer: "这个工具只能读写交给这个机器人的待办，别的待办它碰不到；命令里的 token 不要给别人。写回的内容作为一条决定记录出现在待办上，不改待办的标题和备注，但会出现在待办的对话话题里，其他扩展和之后处理这条待办的 Agent 也看得到。"}
	switch {
	case st.Link == "" || st.Socket == "" || st.Token == "":
		why := "那台 Mac 上的扩展程序还没准备好 MCP 命令"
		if st.SocketError != "" {
			why += "：" + clip(st.SocketError, 160)
		}
		sec.Items = []wire.Component{{Type: wire.ComponentText, Style: wire.TextStyleSecondary, Text: why}}
	default:
		sec.Items = []wire.Component{
			{Type: wire.ComponentText, Text: "在那台 Mac 的终端里运行一次下面这条命令，把本扩展的 MCP 工具（persona-todos）加给这个机器人。命令里的路径在扩展升级后不变，不用重新登记；换了机器人要按新的命令再加一次。"},
			{Type: wire.ComponentText, Style: wire.TextStyleCode, Text: toolsAddCommand(st.Link, st.Socket, st.Token, bot)},
			{Type: wire.ComponentText, Style: wire.TextStyleSecondary, Text: "加好以后，可以用 orca bot tools --bot " + shellWord(bot.ID) + " 看它有没有这个工具。"},
		}
		scope := wire.Component{Type: wire.ComponentButton, Title: "放宽：不知道是哪个对话时也允许读写",
			Action: &wire.Action{ID: "scope:wide", Confirm: "Orca 没告诉工具它在哪个对话里时（几个对话共用一个工具进程），这个机器人的任何对话都能读写交给它的所有待办。确定放宽？"}}
		words := "默认只有接到待办的那个对话能读写它；Orca 没告诉工具它在哪个对话里时，工具什么都读写不了。"
		if st.Wide {
			scope = wire.Component{Type: wire.ComponentButton, Title: "收紧：只允许接到待办的对话", Action: &wire.Action{ID: "scope:chat"}}
			words = "已放宽：Orca 没告诉工具它在哪个对话里时，这个机器人的任何对话都能读写交给它的所有待办。"
		}
		sec.Items = append(sec.Items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary, Text: words}, scope)
	}
	return sec
}

func stateWords(r record) string {
	switch r.State {
	case "pending":
		if r.LastError != "" {
			return "还没交出去：" + r.LastError
		}
		return "正在交给机器人"
	case "sent":
		return "机器人在处理"
	case "finished":
		switch {
		case r.TurnState == "completed" && r.Outcome == "failure":
			return "这一轮出错结束了"
		case r.TurnState == "completed" && (r.Outcome == "cancellation" || r.Outcome == "superseded"):
			return "这一轮被中断了"
		}
		switch r.TurnState {
		case "unverifiable":
			return "结束了，但 Orca 无法确认是否正常做完"
		case "interrupted":
			return "这一轮被中断了"
		case "unavailable":
			return "对话找不到了"
		case "timeout":
			return "24 小时没做完，不再跟进"
		}
		return "做完了，回答在 Orca 里"
	case "failed":
		if r.LastError != "" {
			return "没交出去：" + r.LastError
		}
		return "没交出去"
	}
	return r.State
}

func recentSection(recs []record, loc *time.Location) wire.Section {
	sec := wire.Section{Title: "最近转发"}
	sort.Slice(recs, func(i, j int) bool { return recs[i].CreatedAt.After(recs[j].CreatedAt) })
	for i, r := range recs {
		if i == recentShown {
			break
		}
		sub := stateWords(r)
		if n := len(r.Refs); n > 0 {
			sub += fmt.Sprintf(" · 写回 %d 次", n)
		}
		if r.NotifyNote != "" {
			sub += " · " + r.NotifyNote
		}
		sec.Items = append(sec.Items, wire.Component{Type: wire.ComponentRow, Title: clip(r.Title, 80),
			Subtitle: r.CreatedAt.In(loc).Format("01-02 15:04") + " · " + sub})
	}
	if len(sec.Items) == 0 {
		sec.Items = []wire.Component{{Type: wire.ComponentText, Style: wire.TextStyleSecondary, Text: "还没有转发过待办。"}}
	}
	return sec
}

func setupSection() wire.Section {
	steps := []string{
		"1. 在设置里选运行 Orca 的 Mac，并填要转发的项目或关键词（至少一个）。",
		"2. 在这一页从 Orca 的机器人列表里选一个机器人。",
		"3. 按「让机器人能写回待办」里的命令，在那台 Mac 上把 MCP 工具加给机器人。",
		"之后每条新建的、符合条件的待办会在这个机器人那里开一个新对话；它做完这一轮时，Persona 通知你，回答留在 Orca 里。",
	}
	sec := wire.Section{Title: "怎么用"}
	for _, s := range steps {
		sec.Items = append(sec.Items, wire.Component{Type: wire.ComponentText, Text: s})
	}
	return sec
}
