package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The cold-session backup page (nav page AI 对话): computer filter, network-drive status, each
// computer's state with what to do next and its upload action, search over the local cold index
// (deep full-text search on demand), results with their location and restore. Session status is
// drawn on this page only; the main page links here.

const (
	sessionsUI      = "disklessly-sessions"
	allMachines     = "all"
	coldShown       = 50
	coldSearchHint  = "搜对话：标题、第一句话、项目或关键词"
	coldOffloadText = "把一周没动的 AI 对话搬到网盘"
	coldRetryText   = "重试：搬到网盘"
	coldRefreshText = "重新检查所有电脑"
	coldDeepText    = "搜全文"
	sessionsTitle   = "AI 对话"
	// coldQueryRunes keeps a query well under the 2000-character Search.value limit.
	coldQueryRunes = 200
)

// clipQuery trims a search query and cuts it to coldQueryRunes runes.
func clipQuery(q string) string {
	q = strings.TrimSpace(q)
	if r := []rune(q); len(r) > coldQueryRunes {
		q = strings.TrimSpace(string(r[:coldQueryRunes]))
	}
	return q
}

// sessPage is the AI 对话 page state: the machine filter and the query only (16 KB limit).
type sessPage struct {
	Machine string `json:"m,omitempty"`
	Query   string `json:"q,omitempty"`
	// Open is the part of the page whose 详情 is shown.
	Open string `json:"x,omitempty"`
}

// deepResult carries a deep search's hits into one render; they are never stored in page state.
type deepResult struct {
	hits []ColdHit
	errs []string
}

func locationTone(loc string) string {
	switch loc {
	case LocCloud:
		return wire.TonePositive
	case LocMissing:
		return wire.ToneDanger
	case LocLocal:
		return wire.ToneNeutral
	}
	return wire.ToneCaution
}

// coldSections draws the cold backup status, actions and results for machine m (nil: all machines),
// keeping the total component count within maxItems (defaulting to wire.ItemsMost).
func (d draw) coldSections(m *Machine, query string, deep *deepResult, maxItems int) []wire.Section {
	if maxItems <= 0 {
		maxItems = wire.ItemsMost
	}
	query = clipQuery(query)
	machineArg := ""
	if m != nil {
		machineArg = m.ID
	}
	var machines []*Machine
	for _, mm := range d.st.Machines {
		if (m == nil && mm.Enabled) || (m != nil && mm.ID == m.ID) {
			machines = append(machines, mm)
		}
	}

	counts := map[string]int{}
	var total, indexed int
	var capped bool
	var lastCheck time.Time
	byState := map[string]int{}
	var running []string
	ready := 0
	type machineRow struct {
		m     *Machine
		cat   *ColdCatalog
		state string
	}
	var rows []machineRow
	for _, mm := range machines {
		cat := d.st.cold(mm.ID)
		state := cat.stateCode()
		byState[state]++
		if state == ColdReady {
			ready++
		}
		rows = append(rows, machineRow{mm, cat, state})
		if j, ok := d.jobs[mm.ID]; ok && j.running() {
			running = append(running, "「"+machineLabel(mm)+"」"+strings.TrimPrefix(j.Label, "正在"))
		}
		if cat == nil {
			continue
		}
		total += cat.Total
		indexed += len(cat.Entries)
		capped = capped || cat.Capped
		if cat.Backend.CheckedAt.After(lastCheck) {
			lastCheck = cat.Backend.CheckedAt
		}
		for _, e := range cat.Entries {
			counts[cat.location(e, mm, d.now)]++
		}
	}
	slices.SortStableFunc(rows, func(a, b machineRow) int { return cmp.Compare(coldStateRank(a.state), coldStateRank(b.state)) })

	// The summary counts machines by state ("2 正常 · 2 未安装 CLI · 1 CLI 版本过旧"), worst first.
	var states []string
	for s := range byState {
		states = append(states, s)
	}
	slices.SortFunc(states, func(a, b string) int { return cmp.Compare(coldStateRank(a), coldStateRank(b)) })
	var parts []string
	worst := wire.TonePositive
	for _, s := range states {
		parts = append(parts, fmt.Sprintf("%d %s", byState[s], coldStateLabel(s)))
		if t := coldStateTone(s); t == wire.ToneDanger || (t == wire.ToneCaution && worst == wire.TonePositive) {
			worst = t
		}
	}
	machineStat := wire.Stat{Label: "电脑", Value: fmt.Sprintf("%d 台", len(machines)), Detail: strings.Join(parts, " · "), Tone: worst}
	if len(running) > 0 {
		machineStat.Detail = fmt.Sprintf("%d 台正在处理 · %s", len(running), machineStat.Detail)
	}
	checked := "还没检查过"
	if !lastCheck.IsZero() {
		checked = d.when(lastCheck) + "检查"
	}
	indexDetail := checked
	if capped {
		indexDetail = fmt.Sprintf("只列出最新的 %d 个（共 %d 个）· %s", indexed, total, indexDetail)
	}
	status := wire.Section{
		Title:  "AI 对话：旧对话搬到网盘，要用时再拿回来",
		Footer: "「在网盘里」只看最近 24 小时内的检查结果；网盘连不上、还没检查或电脑关机时显示「暂时不确定」，不会当成丢了。",
		Items: []wire.Component{{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
			Text: "Claude Code、Codex 这些 AI 助手的聊天记录会一直占着硬盘。一周没动的对话可以搬到你自己的网盘：先上传、再从网盘读回来核对无误，才删掉电脑上的那份；还在用的对话不会动。要用时在下面搜到它，点「拿回来」。"}},
	}
	status.Items = append(status.Items, d.statComponents("", []wire.Stat{
		{Label: "记下的对话", Value: fmt.Sprintf("%d 个", indexed), Detail: indexDetail},
		{Label: LocCloud, Value: fmt.Sprintf("%d 个", counts[LocCloud]), Tone: toneIf(counts[LocCloud] > 0, wire.TonePositive)},
		{Label: "不确定 / 找不到", Value: fmt.Sprintf("%d / %d", counts[LocUnknown], counts[LocMissing]),
			Tone: cmp.Or(toneIf(counts[LocMissing] > 0, wire.ToneDanger), toneIf(counts[LocUnknown] > 0, wire.ToneCaution))},
		machineStat,
	})...)
	if len(running) > 0 {
		status.Items = append(status.Items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
			Text: clip("正在进行："+strings.Join(running, "、")+"。完成后页面会自动更新，可以先离开这页。", 300)})
	}
	// Where the upload is: under each ready computer's row below; say so, or why there is none.
	status.Items = append(status.Items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
		Text: clip(offloadHint(m, ready, len(machines), byState), 300)})
	if m == nil {
		// One computer's own row below has its 重新检查 button.
		status.Items = append(status.Items, wire.Component{
			Type: wire.ComponentButton, Title: coldRefreshText,
			Action: &wire.Action{ID: "cold-refresh", Values: map[string]string{"m": machineArg}},
		})
	}
	if strings.TrimSpace(query) != "" {
		status.Items = append(status.Items, wire.Component{
			Type: wire.ComponentButton, Title: coldDeepText,
			Action: &wire.Action{ID: "cold-deep", Values: map[string]string{"m": machineArg, "q": query}},
		})
	}

	var hits []ColdHit
	title := "已经搬到网盘的对话"
	if deep != nil {
		hits = deep.hits
		title = fmt.Sprintf("搜全文「%s」（%d 个）", clip(query, 24), len(hits))
	} else {
		hits = SearchCold(d.st, query, machineArg, 0, d.now)
		if strings.TrimSpace(query) != "" {
			title = fmt.Sprintf("搜索「%s」（%d 个）", clip(query, 24), len(hits))
		}
	}
	results := wire.Section{Title: title}
	if deep != nil {
		for _, e := range deep.errs[:min(len(deep.errs), 4)] {
			msg, _ := humanize(e)
			results.Items = append(results.Items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary, Text: clip("搜全文没有搜完："+msg, 300)})
		}
	}
	shown := hits[:min(len(hits), coldShown)]
	seenColdKeys := map[string]bool{}
	for i, h := range shown {
		row := d.coldRow(h)
		for n := i; seenColdKeys[row.Key]; n++ {
			row.Key = boundedKey(fmt.Sprintf("cold-%d-", n), h.MachineID+"-"+h.ID)
		}
		seenColdKeys[row.Key] = true
		results.Items = append(results.Items, row)
	}
	var notes []string
	if len(hits) > len(shown) {
		notes = append(notes, fmt.Sprintf("显示最新的 %d 个，共 %d 个；多输几个词可以缩小范围。", len(shown), len(hits)))
	}
	if capped {
		notes = append(notes, fmt.Sprintf("这里每台电脑只列出最新的 %d 个对话，更早的请用「%s」。", maxColdEntries, coldDeepText))
	}
	results.Footer = strings.Join(notes, " ")
	if len(shown) == 0 {
		detail := "还没有搬到网盘的对话。点「重新检查」看看电脑的情况；检查只读取信息，不会搬走或删除任何东西。"
		if strings.TrimSpace(query) != "" {
			detail = "这里没有找到；可以试试「" + coldDeepText + "」，在电脑上搜对话的完整内容。"
		}
		results.Items = append(results.Items, d.emptyComponents("chat", "没有对话", detail, nil)...)
	}

	if allowResults := max(0, maxItems-len(status.Items)); len(results.Items) > allowResults {
		results.Items = results.Items[:allowResults]
	}
	machineSec := wire.Section{Key: "cold-machines", Title: "每台电脑的情况"}
	machineBudget := max(0, maxItems-len(status.Items)-len(results.Items))
	compactCount := 0
	for i, r := range rows {
		rem := len(rows) - i - 1
		if machineBudget <= 0 || len(machineSec.Items) >= machineBudget {
			break
		}
		if len(rows) > 1 && len(machineSec.Items) == machineBudget-1 && rem > 0 {
			machineSec.Items = append(machineSec.Items, wire.Component{
				Type:  wire.ComponentText,
				Style: wire.TextStyleSecondary,
				Text:  fmt.Sprintf("还有 %d 台电脑没列出来，在顶部「电脑」里选它查看。", len(rows)-i),
			})
			break
		}
		full := d.machineStateItems(r.m, r.cat, r.state)
		if len(rows) == 1 || len(machineSec.Items)+len(full)+rem <= machineBudget {
			if len(machineSec.Items)+len(full) <= machineBudget {
				machineSec.Items = append(machineSec.Items, full...)
				continue
			}
		}
		machineSec.Items = append(machineSec.Items, d.machineStateCompactRow(r.m, r.cat, r.state))
		compactCount++
	}
	if compactCount > 0 {
		machineSec.Footer = fmt.Sprintf("后面 %d 台电脑只显示一行；点它（或在顶部「电脑」里选它）可以看到全部操作。", compactCount)
	}
	if len(machineSec.Items) == 0 {
		return []wire.Section{status, results}
	}
	return []wire.Section{status, machineSec, results}
}

// offloadHint says where the upload button is (under each ready computer's row), or why no
// computer shown can upload now.
func offloadHint(m *Machine, ready, total int, byState map[string]int) string {
	if m != nil {
		state := ""
		for s := range byState {
			state = s
		}
		if state == ColdReady {
			return "要腾地方：点下面这台电脑的「" + coldOffloadText + "」。"
		}
		return "「" + machineLabel(m) + "」现在还不能把对话搬到网盘（" + coldStateLabel(state) + "）：按下面的提示处理好，再点「重新检查」。"
	}
	switch {
	case total == 0:
		return "还没有电脑。先到「硬盘空间」页添加电脑。"
	case ready == total:
		return "要腾地方：在下面某台电脑那一行点「" + coldOffloadText + "」，每次搬一台。"
	case ready > 0:
		return fmt.Sprintf("%d 台电脑可以搬：在它那一行点「%s」。其余 %d 台还不能搬，原因和处理办法写在各自那一行。", ready, coldOffloadText, total-ready)
	}
	return "现在没有哪台电脑能把对话搬到网盘：先按下面每台电脑的提示装好配套小工具、设置好网盘，再点「重新检查」。"
}

// refreshAfter asks the app to draw the page again while a background job runs (hubs that do
// not take ui.invalidate still redraw); 0 otherwise.
func (d draw) refreshAfter() int {
	for _, j := range d.jobs {
		if j.running() {
			return coldPollSeconds
		}
	}
	return 0
}

// coldPollSeconds is the redraw interval while a job runs (the view schema's minimum).
const coldPollSeconds = 30

// coldAdvice is what a machine row says and offers: the problem, and which fixes apply.
type coldAdvice struct {
	problem
	brew    bool         // offer 用 Homebrew 装 / 升级
	upgrade bool         // Homebrew already manages the CLI: upgrade it
	copy    installOffer // src set: offer copy-install
	docs    bool         // link to the install documentation
	retry   string       // the refresh button's verb
}

// coldProblem words machine m's cold-backup state for people: the same text on the page and in
// disklessly_sessions_status.
func coldProblem(st *State, m *Machine, cat *ColdCatalog, state string) coldAdvice {
	name := "「" + machineLabel(m) + "」"
	a := coldAdvice{retry: "重新检查"}
	a.Title = coldStateLabel(state)
	switch state {
	case ColdReady:
		a.retry = "重新检查"
	case ColdNoCLI, ColdCLIOld:
		a.docs = true
		if state == ColdNoCLI {
			a.Reason = name + "上还没装 Disklessly 的配套小工具（disklessly），所以这台电脑的旧对话还不能搬到网盘。"
		} else {
			a.Reason = name + "上的配套小工具 disklessly " + cmp.Or(shortVersion(cmp.Or(cat.CLI.Version, m.BinaryVersion)), "（版本不清楚）") +
				" 太旧，还不会搬对话，需要升级。"
			a.Raw = cat.Detail
		}
		a.upgrade = state == ColdCLIOld && brewManaged(cat.CLI)
		cmd := brewInstallCmd
		if a.upgrade {
			cmd = brewUpgradeCmd
		}
		manual := brewSetupComment + "\n" + brewSetupCommand + "\n"
		if strings.HasPrefix(cmp.Or(catPlatform(cat), m.Platform), "linux") {
			manual += brewLinuxShellEnv + "\n"
		}
		manual += brewInstallCmd
		offer := installSourceFor(st, m)
		switch {
		case cat != nil && cat.Brew != "":
			a.brew = true
			verb := "安装"
			if a.upgrade {
				verb = "升级"
			}
			a.Next = "这台电脑有 Homebrew（一个装软件的工具）：点「用 Homebrew " + verb + "配套小工具」就行，装好后会自动重新检查。它在后台运行的命令如下。"
			a.Command = cmd
		case cat == nil || !cat.BrewKnown:
			a.copy = offer
			a.Next = "还不知道这台电脑有没有 Homebrew（一个装软件的工具）：先点「重新检查 " + machineLabel(m) + "」，有的话会出现「用 Homebrew 装配套小工具」。"
			if offer.src != nil {
				a.Next += "也可以直接点「从 " + machineLabel(offer.src) + " 复制过来」。"
			} else {
				a.Next += "现在也没法从别的电脑复制（" + strings.TrimSuffix(offer.reason, "。") + "）。"
			}
		case offer.src != nil:
			a.copy = offer
			a.Next = fmt.Sprintf("这台电脑没有 Homebrew：点「从 %s 复制过来」，会从同为 %s 的 %s 复制 disklessly %s 过来；也可以在这台电脑上运行下面的命令，先装 Homebrew 再装配套小工具。",
				machineLabel(offer.src), offer.platform, machineLabel(offer.src), cmp.Or(shortVersion(offer.cli.Version), "（版本不清楚）"))
			a.Command = manual
		default:
			a.Next = "这台电脑没有 Homebrew，也没法从别的电脑复制（" + strings.TrimSuffix(offer.reason, "。") + "）。请在这台电脑上运行下面的命令，先装 Homebrew 再装配套小工具，然后点「重新检查」。"
			a.Command = manual
		}
	case ColdNoBackend:
		a.problem = noBackendProblem(m)
		a.Raw = cat.Detail
		a.retry = "重新检查"
	case ColdBackendDown:
		b := cat.Backend
		id := cmp.Or(b.ID, b.Type, "（未命名）")
		a.Reason = name + "用的网盘（" + id + "）现在连不上，所以对话在不在网盘里暂时显示「暂时不确定」，不会当成丢了。"
		a.Next = "确认网盘（或家里的 NAS）在线、网络通了，再点「重新检查」；想看具体原因，在这台电脑上运行下面的命令。"
		a.Command = "disklessly doctor"
		if strings.Contains(b.Error, "repo.json is not in") {
			// The backend answers, but holds no Disklessly repository.
			a.Reason = name + "用的网盘（" + id + "）能连上，但里面还没有 Disklessly 的存放目录（找不到 repo.json），所以暂时显示「暂时不确定」。"
			a.Next = "多半是选错了网盘目录：在这台电脑上用下面的命令看看设置了哪些网盘，换成正确的那个；如果这就是要新用的目录，就在里面新建。然后点「重新检查」。"
			a.Command = "disklessly backend list\ndisklessly backend use <后端 ID>   # 换到已经在用的那个网盘\ndisklessly init                    # 或者：在当前网盘里新建存放目录"
		}
		a.Raw = strings.TrimSpace(fmt.Sprintf("%s（%s，%d ms）", b.Error, cmp.Or(b.Type, "网盘"), b.LatencyMs))
	case ColdOffline:
		errText := m.LastError
		if cat != nil {
			errText = cmp.Or(cat.Error, errText)
		}
		a.problem = sshProblem(m, errText)
		a.retry = "重试"
		if !a.Retry {
			a.retry = ""
		}
	case ColdUnreadable:
		a.retry = "重试"
		hung := cat != nil && strings.HasPrefix(cat.Error, "disklessly 没有正常结束")
		if hung {
			a.Reason = name + "上的配套小工具 3 分钟内没有答完，网盘里的情况暂时不确定。"
			a.Next = "过一会儿点「重试」；一直这样的话，在这台电脑上运行下面的命令看看卡在哪里。"
		} else {
			a.Reason = name + "上的配套小工具出错了（回答看不懂），网盘里的情况暂时不确定。"
			a.Next = "点「重试」；还是这样的话，在这台电脑上运行下面的命令，把输出（「详情」里也有）发给维护者。"
		}
		a.Command = "disklessly --json sessions status --check-segments"
		if cat != nil {
			_, raw := humanize(cat.Error)
			lines := []string{cmp.Or(raw, cat.Error)}
			if cat.Detail != "" {
				lines = append(lines, "配套小工具输出的第一行："+cat.Detail)
			}
			a.Raw = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	default:
		a.retry = "检查"
		a.Reason = "还没检查过" + name + "。"
		a.Next = "点「检查 " + machineLabel(m) + "」：只读取信息，不会搬走或删除任何东西。"
	}
	return a
}

// noBackendCommand sets up a network drive for the CLI; the commands stay exact.
const noBackendCommand = "disklessly backend list\ndisklessly backend add rclone <远端:路径>   # 也可以是 smb、nfs、webdav、openlist\ndisklessly cloud connect gdrive --use      # 或者直接连一个网盘"

// noBackendProblem is what a computer whose CLI has no network drive set up says, on its row and
// (m nil: the row's own computer) after an upload the CLI refused for that reason.
func noBackendProblem(m *Machine) problem {
	reason := "「" + machineLabel(m) + "」还没有设置网盘，旧对话没地方放，所以还不能搬。"
	next := "先在这台电脑上设置一个网盘（Google Drive 这类网盘、家里的 NAS 都行）：运行下面的命令，然后点「重新检查」。"
	if m == nil {
		reason = "这台电脑还没有设置网盘，对话没地方放，这次一个都没有搬走或删除。"
		next = "先在这台电脑上设置一个网盘（Google Drive 这类网盘、家里的 NAS 都行）：运行下面的命令，设置好后点「重试」。"
	}
	return problem{
		Title:   coldStateLabel(ColdNoBackend),
		Reason:  reason,
		Next:    next,
		Command: noBackendCommand,
	}
}

// detailsKey names what the 详情 toggle of a part of the page opens.
func detailsKey(kind, id string) string { return kind + "-" + id }

// detailsItems is the 详情 toggle for raw (nothing without it) and, when open, the raw text.
func (d draw) detailsItems(key, raw string) []wire.Component {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if d.open != key {
		return []wire.Component{{Type: wire.ComponentButton, Title: "详情", Action: &wire.Action{ID: "details", Values: map[string]string{"k": key}}}}
	}
	return []wire.Component{
		{Type: wire.ComponentCode, Key: boundedKey("raw-", key), Text: clipRunes(raw, 4000)},
		{Type: wire.ComponentButton, Title: "收起详情", Action: &wire.Action{ID: "details", Values: map[string]string{"k": key}}},
	}
}

// commandItem draws a command to copy.
func commandItem(key, command string) []wire.Component {
	if command == "" {
		return nil
	}
	return []wire.Component{{Type: wire.ComponentCode, Key: boundedKey("cmd-", key), Language: "sh", Text: command}}
}

// machineStateRow builds the header row and advice for one machine.
func (d draw) machineStateRow(m *Machine, cat *ColdCatalog, state string) (wire.Component, coldAdvice) {
	name := machineLabel(m)
	row := wire.Component{
		Type:  wire.ComponentRow,
		Key:   boundedKey("mach-", m.ID),
		Icon:  "gauge",
		Title: clip(cmp.Or(strings.TrimSpace(name), strings.TrimSpace(m.ID), "没起名的电脑"), 200),
		Badge: &wire.Badge{Text: coldStateLabel(state), Tone: coldStateTone(state)},
	}
	var sub []string
	if p := cmp.Or(catPlatform(cat), m.Platform); p != "" {
		sub = append(sub, p)
	}
	if cat != nil && cat.CLI.Version != "" {
		sub = append(sub, "配套小工具 "+shortVersion(cat.CLI.Version))
	}
	if cat != nil && !cat.CheckedAt.IsZero() {
		sub = append(sub, d.when(cat.CheckedAt)+"检查")
	} else if cat != nil && !cat.RefreshedAt.IsZero() {
		sub = append(sub, d.when(cat.RefreshedAt)+"检查")
	}
	row.Subtitle = clip(strings.Join(sub, " · "), 400)

	adv := coldProblem(d.st, m, cat, state)
	if state == ColdReady {
		n, cloud, missing := 0, 0, 0
		for _, e := range cat.Entries {
			n++
			switch cat.location(e, m, d.now) {
			case LocCloud:
				cloud++
			case LocMissing:
				missing++
			}
		}
		b := cat.Backend
		row.Detail = fmt.Sprintf("一切正常，可以搬。记下 %d 个对话：%d 个在网盘里，%d 个网盘里找不到 · 网盘 %s 连得上（%d 毫秒）",
			n, cloud, missing, cmp.Or(b.ID, b.Type, "（没起名）"), b.LatencyMs)
		if len(cat.Missing) > 0 {
			row.Detail += fmt.Sprintf("；网盘里少了 %d 个数据包，里面的对话拿不回来", len(cat.Missing))
		}
		if len(cat.Unchecked) > 0 || cat.TimedOut {
			row.Detail += fmt.Sprintf("；还有 %d 个数据包没来得及核对", len(cat.Unchecked))
		}
	} else {
		row.Detail = adv.text()
	}
	row.Detail = clip(row.Detail, 400)
	return row, adv
}

// machineStateCompactRow draws one machine as a single tappable summary row that switches the
// machine filter to m so its full commands and actions remain reachable within wire.ItemsMost.
func (d draw) machineStateCompactRow(m *Machine, cat *ColdCatalog, state string) wire.Component {
	row, _ := d.machineStateRow(m, cat, state)
	if j, ok := d.jobs[m.ID]; ok && j.running() {
		row.Badge = &wire.Badge{Text: "进行中", Tone: wire.ToneNeutral}
		row.Detail = clip(fmt.Sprintf("%s…（%s开始）", j.Label, d.when(j.Started)), 400)
	}
	// Encode m.ID in Action.ID when it fits within 128 runes so UIVersion 1-3 clients
	// (which do not send Action.Values) can also select the folded machine; keep Values for UIv4+ and >120-rune IDs.
	actionID := "machine"
	if utf8.RuneCountInString("machine:"+m.ID) <= 128 {
		actionID = "machine:" + m.ID
	}
	row.Action = &wire.Action{ID: actionID, Values: map[string]string{"machine": m.ID}}
	return row
}

// machineStateItems draws one machine's row (name, state, cause and next step) and the actions
// and command that fix it; raw output stays behind 详情.
func (d draw) machineStateItems(m *Machine, cat *ColdCatalog, state string) []wire.Component {
	name := machineLabel(m)
	row, adv := d.machineStateRow(m, cat, state)

	raw := adv.Raw
	items := []wire.Component{row}
	command := adv.Command
	var last *coldJob
	if j, ok := d.jobs[m.ID]; ok {
		if j.running() {
			row.Badge = &wire.Badge{Text: "进行中", Tone: wire.ToneNeutral}
			items[0] = row
			items = append(items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
				Text: fmt.Sprintf("%s…（%s开始，完成后页面会自动更新）", j.Label, d.when(j.Started))})
			return items // no actions while it runs: a second start would be refused anyway
		}
		if j.Kind != jobRefresh {
			last = &j
			items = append(items, d.jobOutcome(j, retryFor(j, m, state, adv))...)
			if j.Raw != "" {
				raw = strings.TrimSpace(raw + "\n上次操作：" + j.Raw)
			}
			if j.Err != "" && j.Command != "" {
				command = j.Command
			}
		}
	}
	items = append(items, commandItem(detailsKey("cold", m.ID), command)...)
	// The upload: offered on a ready computer only; a failed upload offers a real retry.
	switch {
	case last != nil && last.Kind == jobOffload && last.Err != "" && state == ColdReady:
		items = append(items, offloadButton(m, coldRetryText))
	case state == ColdReady:
		items = append(items, offloadButton(m, coldOffloadText))
	}
	if adv.brew {
		verb, cmd := "安装", brewInstallCmd
		if adv.upgrade {
			verb, cmd = "升级", brewUpgradeCmd
		}
		confirm := fmt.Sprintf("在「%s」上用 Homebrew %s配套小工具 disklessly？会在这台电脑上运行：%s。装好后先运行 disklessly version 确认能用，再重新检查这台电脑。", name, verb, cmd)
		if strings.HasPrefix(cmp.Or(catPlatform(cat), m.Platform), "darwin") {
			confirm += "在 Mac 上还会去掉 Homebrew 给这个没签名的程序加的隔离标记，不然远程登录时它运行不了。"
		}
		if cat != nil && cat.CLI.Path != "" && !brewManaged(cat.CLI) {
			confirm += "原来的 " + cat.CLI.Path + " 不会删；如果系统先找到的还是它，装完会提醒你。"
		}
		items = append(items, wire.Component{Type: wire.ComponentButton, Title: "用 Homebrew " + verb + "配套小工具",
			Action: &wire.Action{ID: "cold-brew", Values: map[string]string{"m": m.ID}, Confirm: confirm}})
	}
	if adv.copy.src != nil {
		items = append(items, wire.Component{Type: wire.ComponentButton, Title: "从 " + machineLabel(adv.copy.src) + " 复制过来",
			Action: &wire.Action{ID: "cold-install", Values: map[string]string{"m": m.ID, "id": adv.copy.src.ID}, Confirm: installConfirm(adv.copy, m)}})
	}
	if adv.retry != "" {
		items = append(items, wire.Component{Type: wire.ComponentButton, Title: adv.retry + " " + name,
			Action: &wire.Action{ID: "cold-refresh", Values: map[string]string{"m": m.ID}}})
	}
	if adv.docs {
		items = append(items, wire.Component{Type: wire.ComponentLink, Title: "怎么装配套小工具", URL: coldInstallDocsURL})
	}
	return append(items, d.detailsItems(detailsKey("cold", m.ID), raw)...)
}

// offloadButton starts machine m's cold-session upload, behind the confirm that says what moves,
// what is checked and what is deleted.
func offloadButton(m *Machine, title string) wire.Component {
	return wire.Component{Type: wire.ComponentButton, Key: boundedKey("offload-", m.ID), Title: title,
		Action: &wire.Action{ID: "cold-offload", Values: map[string]string{"m": m.ID}, Confirm: sessionOffloadConfirm(m)}}
}

// retryHint is how a failed job is tried again from the page: the title of a button that the
// row really draws, or else a sentence saying where to do it.
type retryHint struct{ button, how string }

// retryFor names, for the failed job j on machine m, the button under its row that runs the same
// work again; only buttons machineStateItems draws for m's state are named. A restore is retried
// from its conversation's row, and a file offload from Persona's disk usage map.
func retryFor(j coldJob, m *Machine, state string, adv coldAdvice) retryHint {
	fallback := retryHint{}
	if adv.retry != "" {
		fallback.button = adv.retry + " " + machineLabel(m)
	}
	switch j.Kind {
	case jobOffload:
		if state == ColdReady {
			return retryHint{button: coldRetryText}
		}
	case jobRestore:
		return retryHint{how: "想再试一次：在下面找到这个对话，点「拿回来」。"}
	case jobInstall:
		if adv.copy.src != nil {
			return retryHint{button: "从 " + machineLabel(adv.copy.src) + " 复制过来"}
		}
	case jobBrew:
		if adv.brew {
			verb := "安装"
			if adv.upgrade {
				verb = "升级"
			}
			return retryHint{button: "用 Homebrew " + verb + "配套小工具"}
		}
	case jobFileOffload, jobFileRestore:
		return retryHint{how: "想再试一次：回到 Persona 的磁盘占用图，重新选中它们。"}
	}
	return fallback
}

// jobOutcome is the line under a machine saying how its last job ended. A failure's 「重试」 names
// the button retry gives, so the text never points at a button that is not there (or at one
// that does something else).
func (d draw) jobOutcome(j coldJob, retry ...retryHint) []wire.Component {
	text := j.Note
	if j.Err != "" {
		text = "没有成功：" + j.Err
		if len(retry) > 0 {
			r := retry[0]
			if r.button == "" {
				text = strings.ReplaceAll(text, "修好后点「重试」", "修好后再试")
				text = strings.ReplaceAll(text, "然后点「重试」", "然后再试")
				text = strings.ReplaceAll(text, "点「重试」", "再试一次")
				if r.how != "" {
					text = strings.TrimRight(text, "。；") + "。" + r.how
				}
			} else {
				text = strings.ReplaceAll(text, "「重试」", "「"+r.button+"」")
				if !strings.Contains(text, r.button) {
					text = strings.TrimRight(text, "。；") + "。处理好后点「" + r.button + "」。"
				}
			}
		}
	}
	if text == "" {
		return nil
	}
	return []wire.Component{{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
		Text: clip(fmt.Sprintf("%s · %s", d.when(j.Finished), text), 400)}}
}

// sandboxSection is the page's first section while the extension runs in the hub's sandbox.
func sandboxSection() []wire.Section {
	if !sandboxed() {
		return nil
	}
	p := sandboxProblem()
	return []wire.Section{{Key: "sandbox", Title: "Disklessly 被关在隔离环境（沙箱）里，连不上其他电脑", Items: append(
		[]wire.Component{{Type: wire.ComponentText, Text: p.text()}}, commandItem("sandbox", p.Command)...)}}
}

func catPlatform(c *ColdCatalog) string {
	if c == nil {
		return ""
	}
	return c.Platform
}

func (d draw) coldRow(h ColdHit) wire.Component {
	sub := []string{h.MachineName, cmp.Or(toolLabel(h.Tool), "Agent")}
	if p := cmp.Or(h.Project, lastPathElem(h.Cwd)); p != "" {
		sub = append(sub, p)
	}
	if t := entryTime(h.ColdEntry); !t.IsZero() {
		sub = append(sub, t.In(d.loc).Format(time.DateOnly))
	}
	if !h.Local {
		sub = append(sub, "来自另一台电脑")
	}
	row := wire.Component{
		Type:     wire.ComponentRow,
		Key:      boundedKey("cold-", h.MachineID+"-"+h.ID),
		Icon:     "chat",
		Title:    clip(cmp.Or(strings.TrimSpace(h.Title), strings.TrimSpace(h.FirstPrompt), strings.TrimSpace(h.SessionID), strings.TrimSpace(h.ID), "没有标题的对话"), 120),
		Subtitle: clip(strings.Join(sub, " · "), 400),
		Detail:   clip(cmp.Or(h.Preview, h.FirstPrompt), 240),
		Badge:    &wire.Badge{Text: h.Location, Tone: locationTone(h.Location)},
	}
	// Another device's row is not restored here: its original path belongs to that device.
	if h.Location != LocLocal && h.Local {
		confirm := fmt.Sprintf("把这个对话从网盘拿回来，放回原来的位置 %s？", cmp.Or(h.Path, "（原来的位置）"))
		if hint := resumeHint(h.ColdEntry); hint != "" {
			confirm += "拿回来后可以运行 " + hint + " 接着聊。"
		}
		row.Actions = []wire.RowAction{{Title: "拿回来", Swipe: true, Action: wire.Action{
			ID:      "cold-restore",
			Values:  map[string]string{"m": h.MachineID, "id": h.ID},
			Confirm: confirm,
		}}}
	}
	return row
}

// toggle opens key's 详情, or closes it when it is the one open.
func toggle(open, key string) string {
	if open == key {
		return ""
	}
	return clipRunes(key, 100)
}

func lastPathElem(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// coldUIBudget bounds a deep search run inside a UI request (the hub allows about 12 seconds).
const coldUIBudget = 8 * time.Second

// coldDeep runs a deep search on the machines in parallel, within coldUIBudget and without
// holding the store lock while they answer.
func (a *app) coldDeep(ctx context.Context, machine, query string) (*deepResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("请先输入要搜的内容")
	}
	if machine == allMachines {
		machine = ""
	}
	runner := a.run()
	var ms []*Machine
	if err := a.db().read(func(st *State) {
		for _, m := range deepTargets(st, machine) {
			mc := *m
			ms = append(ms, &mc)
		}
	}); err != nil {
		return nil, err
	}
	var runs map[string]deepRun
	if words := deepSearchWords(query); len(words) > 0 {
		runs = deepSearchMachines(ctx, runner, ms, words, coldShown, coldUIBudget)
	}
	var res deepResult
	err := a.db().read(func(st *State) {
		res.hits, res.errs = mergeDeepSearch(st, query, machine, coldShown, a.now(), runs)
	})
	return &res, err
}

// ---- AI 对话 nav page ----

func (a *app) renderSessions(_ context.Context, r *ext.UIRequest) (*wire.View, error) {
	var p sessPage
	if err := r.DecodeState(&p); err != nil {
		return nil, &ext.Error{Code: "invalid", Message: pageStateLost}
	}
	return a.sessionsView(r, p, nil)
}

func (a *app) sessionsView(r *ext.UIRequest, p sessPage, deep *deepResult) (*wire.View, error) {
	var v *wire.View
	err := a.db().read(func(st *State) {
		d := a.drawer(st, r)
		d.open = p.Open
		v = d.sessionsPage(p, deep)
	})
	return v, err
}

func (d draw) sessionsFallbackSummary(m *Machine, query string) string {
	scope := "全部电脑"
	machineArg := ""
	if m != nil {
		scope = machineLabel(m)
		machineArg = m.ID
	}
	hits := SearchCold(d.st, clipQuery(query), machineArg, 5, d.now)
	var lines []string
	lines = append(lines, fmt.Sprintf("Disklessly AI 对话（%s，找到 %d 个对话）", scope, len(hits)))
	for _, mm := range d.st.Machines {
		if (m == nil && mm.Enabled) || (m != nil && mm.ID == m.ID) {
			cat := d.st.cold(mm.ID)
			lines = append(lines, fmt.Sprintf("- %s：%s", machineLabel(mm), coldStateLabel(cat.stateCode())))
		}
	}
	return clip(strings.Join(lines, "\n"), 1950)
}

func (d draw) sessionsPage(p sessPage, deep *deepResult) *wire.View {
	var m *Machine
	if p.Machine != "" && p.Machine != allMachines {
		if m = d.st.machine(p.Machine); m == nil || !m.Enabled {
			m, p.Machine = nil, ""
		}
	}
	const maxFilterChoices = 100
	choices := []wire.Choice{{Value: allMachines, Title: "全部电脑"}}
	selectedIncluded := m == nil
	for _, mm := range d.st.Machines {
		if !mm.Enabled {
			continue
		}
		if len(choices) >= maxFilterChoices {
			break
		}
		choices = append(choices, wire.Choice{Value: mm.ID, Title: clip(machineLabel(mm), 28)})
		if m != nil && mm.ID == m.ID {
			selectedIncluded = true
		}
	}
	if !selectedIncluded && m != nil && len(choices) > 1 {
		choices[len(choices)-1] = wire.Choice{Value: m.ID, Title: clip(machineLabel(m), 28)}
	}
	value := allMachines
	if m != nil {
		value = m.ID
	}
	var filter *wire.Filter
	if len(choices) >= 2 {
		filter = &wire.Filter{Name: "machine", Title: "电脑", Choices: choices, Value: value, Action: wire.Action{ID: "machine"}}
	}
	var lead []wire.Section
	if d.v4() {
		lead = sandboxSection()
	} else if sandboxed() {
		sp := sandboxProblem()
		lead = []wire.Section{{Title: sp.Title, Items: []wire.Component{{Type: wire.ComponentText, Text: sp.message()}}}}
	}
	return d.fitVersion(&wire.View{
		Title:        sessionsTitle,
		Fallback:     d.sessionsFallbackSummary(m, p.Query),
		State:        ext.EncodeState(p),
		RefreshAfter: d.refreshAfter(),
		Search:       &wire.Search{Placeholder: coldSearchHint, Value: clipQuery(p.Query), Action: wire.Action{ID: "search"}},
		Filter:       filter,
		Sections:     append(lead, d.coldSections(m, p.Query, deep, wire.ItemsMost-sectionItemCount(lead))...),
	})
}

func (a *app) actionSessions(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
	var p sessPage
	if err := r.DecodeState(&p); err != nil {
		return nil, &ext.Error{Code: "invalid", Message: pageStateLost}
	}
	res := &wire.ActionResult{}
	var deep *deepResult
	switch {
	case r.Action == "machine":
		p.Machine = r.Values["machine"]
	case strings.HasPrefix(r.Action, "machine:"):
		p.Machine = cmp.Or(r.Values["machine"], strings.TrimPrefix(r.Action, "machine:"))
	case r.Action == "search":
		p.Query = clipQuery(r.Values["q"])
	case r.Action == "details":
		p.Open = toggle(p.Open, r.Values["k"])
	case r.Action == "cold-deep":
		p.Query = clipQuery(cmp.Or(strings.TrimSpace(r.Values["q"]), p.Query))
		var err error
		if deep, err = a.coldDeep(ctx, cmp.Or(r.Values["m"], p.Machine), p.Query); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: userMessage(err)}
		}
	default:
		toast, err := a.coldAction(ctx, r.Action, cmp.Or(r.Values["m"], p.Machine), r.Values["id"])
		if err != nil {
			return nil, &ext.Error{Code: "invalid", Message: userMessage(err)}
		}
		res.Toast = toast
	}
	var err error
	res.View, err = a.sessionsView(r, p, deep)
	return res, err
}
