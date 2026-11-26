package main

import (
	"cmp"
	"context"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	pageUI = "disklessly"
	cardUI = "disklessly-card"

	// inlineChartMachines caps full per-machine chart+table blocks in fleetTab so a 64-machine fleet stays well under
	// the View schema's 300-component limit while keeping every machine's row, watermark bar and actions.
	inlineChartMachines = 12
	// maxShownScanItems caps tappable ScanItem rows in itemsTab to match the 100-row table and stay well within wire.ItemsMost.
	maxShownScanItems = 100
)

type page struct {
	Machine string `json:"m,omitempty"`
	Tab     string `json:"t,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Query   string `json:"q,omitempty"`
	// SessQuery is the cold-session search of the 会话与台账 tab.
	SessQuery string `json:"sq,omitempty"`
	// Open is the part of the page whose 详情 is shown.
	Open string `json:"x,omitempty"`
}

const (
	tabFleet    = "fleet"
	tabItems    = "items"
	tabPlan     = "plan"
	tabSessions = "sessions"

	modeHome       = ""
	modeNewMachine = "new-machine"
	modeEditMach   = "edit-machine"
)

type draw struct {
	st        *State
	now       time.Time
	loc       *time.Location
	uiVersion int
	deep      *deepResult // a deep search's hits for this render only
	// jobs are the background jobs by machine id, as of this render.
	jobs map[string]coldJob
	// open is the part of the page whose 详情 is shown.
	open string
	// leadItems is the component count of leading sections (e.g. sandbox banner) reserved against wire.ItemsMost.
	leadItems int
}

func (d draw) v2() bool { return d.uiVersion == 0 || d.uiVersion >= 2 }
func (d draw) v3() bool { return d.uiVersion == 0 || d.uiVersion >= 3 }
func (d draw) v4() bool { return d.uiVersion == 0 || d.uiVersion >= 4 }
func (d draw) v6() bool { return d.uiVersion == 0 || d.uiVersion >= 6 }
func (d draw) v7() bool { return d.uiVersion == 0 || d.uiVersion >= 7 }

func (d draw) fitVersion(v *wire.View) *wire.View {
	if v == nil || d.uiVersion == 0 {
		return v
	}
	if !d.v2() {
		v.RefreshAfter = 0
	}
	if !d.v3() {
		v.Search, v.Filter = nil, nil
	}
	if !d.v4() {
		v.Tabs, v.Layout = nil, ""
	}
	for si := range v.Sections {
		s := &v.Sections[si]
		if !d.v7() {
			s.Key = ""
		}
		for ii := range s.Items {
			it := &s.Items[ii]
			if !d.v6() {
				it.Actions = nil
				for k := range it.Series {
					it.Series[k].Color = ""
				}
			}
			if !d.v4() && it.Type == wire.ComponentCode {
				s.Items[ii] = wire.Component{
					Type:  wire.ComponentText,
					Key:   it.Key,
					Style: wire.TextStyleCode,
					Text:  clip(cmp.Or(strings.TrimSpace(it.Text), "—"), wire.StringMost),
				}
				it = &s.Items[ii]
			}
			if !d.v3() {
				it.Key = ""
				it.Icon = ""
				it.Avatar = nil
				it.Thumbnail = nil
			}
		}
	}
	return v
}

func (d draw) statComponents(key string, stats []wire.Stat) []wire.Component {
	if d.v2() {
		return []wire.Component{{Type: wire.ComponentStatGrid, Key: key, Stats: stats}}
	}
	out := make([]wire.Component, 0, len(stats))
	for _, st := range stats {
		out = append(out, wire.Component{
			Type:   wire.ComponentMetric,
			Label:  st.Label,
			Value:  st.Value,
			Detail: st.Detail,
			Tone:   st.Tone,
		})
	}
	return out
}

func (d draw) emptyComponents(icon, title, detail string, btn *wire.EmptyButton) []wire.Component {
	if d.v3() {
		return []wire.Component{{
			Type:   wire.ComponentEmpty,
			Icon:   icon,
			Title:  title,
			Detail: detail,
			Button: btn,
		}}
	}
	out := []wire.Component{{
		Type:  wire.ComponentText,
		Style: wire.TextStyleSecondary,
		Text:  strings.TrimSpace(title + "：" + detail),
	}}
	if btn != nil {
		act := btn.Action
		out = append(out, wire.Component{
			Type:   wire.ComponentButton,
			Title:  btn.Title,
			Action: &act,
		})
	}
	return out
}

func (a *app) render(_ context.Context, r *ext.UIRequest) (*wire.View, error) {
	var p page
	if err := r.DecodeState(&p); err != nil {
		return nil, &ext.Error{Code: "invalid", Message: "invalid page state"}
	}
	return a.pageView(r, p, nil)
}

func (a *app) pageView(r *ext.UIRequest, p page, deep *deepResult) (*wire.View, error) {
	var v *wire.View
	err := a.db().read(func(st *State) {
		d := a.drawer(st, r)
		d.deep, d.open = deep, p.Open
		v = d.view(p)
	})
	return v, err
}

func (a *app) drawer(st *State, r *ext.UIRequest) draw {
	var tz string
	var uiVer int
	if r != nil {
		tz = r.TZ
		uiVer = r.UIVersion
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return draw{st: st, now: a.now(), loc: loc, uiVersion: uiVer, jobs: a.jobSnapshot()}
}

func (d draw) view(p page) *wire.View {
	switch p.Mode {
	case modeNewMachine:
		return d.machineForm(p, nil)
	case modeEditMach:
		m := d.st.machine(p.Machine)
		if m == nil {
			return d.home(page{})
		}
		return d.machineForm(p, m)
	}
	return d.home(p)
}

func (d draw) home(p page) *wire.View {
	statuses := d.st.statuses()
	if len(statuses) == 0 {
		d.st.ensureDefaults(d.now)
		statuses = d.st.statuses()
	}

	selected := d.st.machine(p.Machine)
	if selected == nil || !selected.Enabled {
		selected = statuses[0].Machine
	}
	p.Machine = selected.ID
	if p.Tab != tabFleet && p.Tab != tabItems && p.Tab != tabPlan && p.Tab != tabSessions {
		p.Tab = tabFleet
	}

	v := &wire.View{
		Title:    "Disklessly 机器磁盘",
		Fallback: d.fallbackSummary(statuses),
	}

	if d.v3() {
		const maxFilterChoices = 100
		const maxMachineChoices = maxFilterChoices - 1 // reserve 1 slot for "+ 添加 SSH 机器"
		choices := make([]wire.Choice, 0, min(len(statuses), maxMachineChoices)+1)
		selectedIncluded := false
		for _, ms := range statuses {
			if len(choices) >= maxMachineChoices {
				break
			}
			label := clip(machineLabel(ms.Machine), 20)
			if ms.TotalBytes > 0 {
				label = fmt.Sprintf("%s · %.0f%%", label, ms.UsedPercent)
			}
			choices = append(choices, wire.Choice{Value: ms.Machine.ID, Title: clip(label, 28)})
			if ms.Machine.ID == selected.ID {
				selectedIncluded = true
			}
		}
		if !selectedIncluded && len(choices) > 0 {
			label := clip(machineLabel(selected), 20)
			ms := d.st.machineStatus(selected)
			if ms.TotalBytes > 0 {
				label = fmt.Sprintf("%s · %.0f%%", label, ms.UsedPercent)
			}
			choices[len(choices)-1] = wire.Choice{Value: selected.ID, Title: clip(label, 28)}
		}
		choices = append(choices, wire.Choice{Value: "+", Title: "＋ 添加 SSH 机器"})
		v.Filter = &wire.Filter{
			Name:    "machine",
			Title:   "机器",
			Choices: choices,
			Value:   selected.ID,
			Action:  wire.Action{ID: "machine"},
		}
	}

	scan := d.st.Scans[selected.ID]
	itemCount := 0
	if scan != nil {
		itemCount = len(scan.Items)
	}
	planCount := 0
	if scan != nil {
		if pl, err := BuildCleanPlan(selected, scan, nil, nil, false); err == nil {
			planCount = len(pl.Items)
		}
	}

	if d.v4() {
		v.Tabs = &wire.Tabs{
			Name:  "tab",
			Value: p.Tab,
			Items: []wire.Tab{
				{Value: tabFleet, Title: "多机总览", Badge: strconv.Itoa(len(statuses))},
				{Value: tabItems, Title: "可回收明细", Badge: strconv.Itoa(itemCount)},
				{Value: tabPlan, Title: "清理计划", Badge: strconv.Itoa(planCount)},
				{Value: tabSessions, Title: "会话与台账", Badge: strconv.Itoa(len(d.st.History))},
			},
			Action: wire.Action{ID: "tab"},
		}
	}
	v.State = ext.EncodeState(p)

	var lead []wire.Section
	if d.v4() {
		lead = sandboxSection()
	} else if sandboxed() {
		sp := sandboxProblem()
		lead = []wire.Section{{
			Title: sp.Title,
			Items: []wire.Component{{Type: wire.ComponentText, Text: sp.message()}},
		}}
	}
	d.leadItems = sectionItemCount(lead)

	switch p.Tab {
	case tabItems:
		if d.v3() {
			v.Search = &wire.Search{Placeholder: "搜索分类、路径或说明", Value: p.Query, Action: wire.Action{ID: "search"}}
		}
		v.Sections = d.itemsTab(p, selected, scan)
	case tabPlan:
		v.Sections = d.planTab(p, selected, scan)
	case tabSessions:
		if d.v3() {
			v.Search = &wire.Search{Placeholder: coldSearchHint, Value: clipQuery(p.SessQuery), Action: wire.Action{ID: "cold-search"}}
		}
		v.Sections = d.sessionsAndHistoryTab(p, selected, scan)
	default:
		v.Sections = d.fleetTab(p, statuses, selected)
	}
	if d.v2() {
		v.RefreshAfter = d.refreshAfter()
	}
	if len(lead) > 0 {
		v.Sections = append(lead, v.Sections...)
	}
	return d.fitVersion(v)
}

func fleetAlertSummary(statuses []MachineStatus) (string, string) {
	var warnCount, critCount, unscannedCount, offlineCount int
	for _, ms := range statuses {
		switch ms.Level {
		case HealthCritical:
			critCount++
		case HealthWarning:
			warnCount++
		case HealthUnscanned:
			unscannedCount++
		case HealthOffline:
			offlineCount++
		}
	}
	switch {
	case critCount > 0:
		return fmt.Sprintf("%d 台红色告警", critCount), wire.ToneDanger
	case warnCount > 0:
		return fmt.Sprintf("%d 台橙色预警", warnCount), wire.ToneCaution
	case offlineCount > 0:
		return fmt.Sprintf("%d 台离线", offlineCount), wire.ToneCaution
	case unscannedCount > 0:
		return fmt.Sprintf("%d 台未扫描", unscannedCount), wire.ToneNeutral
	default:
		return "全部正常", wire.TonePositive
	}
}

// ---- Tab 1: Fleet Disk Overview ----

func (d draw) fleetTab(_ page, statuses []MachineStatus, selected *Machine) []wire.Section {
	var totalReclaim, totalSafe, totalFreed uint64
	for _, ms := range statuses {
		totalReclaim += ms.ReclaimableBytes
		totalSafe += ms.SafeBytes
	}
	for _, h := range d.st.History {
		totalFreed += h.FreedBytes
	}
	alertText, alertTone := fleetAlertSummary(statuses)

	topStats := []wire.Stat{
		{Label: "纳管机器", Value: fmt.Sprintf("%d 台", len(statuses)), Detail: "本机 + SSH 节点"},
		{Label: "水位状态", Value: alertText, Detail: "80% 橙 · 90% 红", Tone: alertTone},
		{Label: "总可回收", Value: FormatBytes(totalReclaim), Detail: "安全项 " + FormatBytes(totalSafe), Tone: toneIf(totalReclaim > 0, wire.TonePositive)},
		{Label: "累计释放", Value: FormatBytes(totalFreed), Detail: fmt.Sprintf("共 %d 次记录", len(d.st.History))},
	}
	topItems := d.statComponents("fleet-summary", topStats)
	topItems = append(topItems, wire.Component{
		Type:   wire.ComponentButton,
		Title:  "扫描全部机器",
		Action: &wire.Action{ID: "scan-all"},
	})

	secs := []wire.Section{{
		Title: "多机器磁盘水位与回收总览",
		Items: topItems,
	}}

	chartLimit := inlineChartMachines
	if len(statuses) > 32 {
		chartLimit = 8
	}

	// Reserve budget for lead (sandbox), top summary section, and trailing manage section (2 items).
	fleetBudget := max(0, wire.ItemsMost-d.leadItems-len(topItems)-2)
	used := 0

	for idx, ms := range statuses {
		rem := len(statuses) - idx - 1
		if used >= fleetBudget {
			break
		}
		if used == fleetBudget-1 && rem > 0 {
			secs = append(secs, wire.Section{
				Items: []wire.Component{{
					Type:  wire.ComponentText,
					Style: wire.TextStyleSecondary,
					Text:  fmt.Sprintf("还有 %d 台机器未展开，请通过顶部「机器」筛选查看。", len(statuses)-idx),
				}},
			})
			break
		}
		m := ms.Machine
		scan := d.st.Scans[m.ID]
		badge := healthBadge(ms)
		compactTail := len(statuses) > 28 && idx >= chartLimit

		var subParts []string
		if m.IsLocal {
			subParts = append(subParts, "本机")
		} else {
			subParts = append(subParts, "SSH "+m.Host)
		}
		if m.Platform != "" {
			subParts = append(subParts, m.Platform)
		}
		if ms.LastScannedAt != nil {
			subParts = append(subParts, d.when(*ms.LastScannedAt))
			if ms.Partial {
				subParts = append(subParts, "缓存未统计完（探测超时，水位照常）")
			}
		} else {
			subParts = append(subParts, "尚未扫描")
		}

		makeRow := func() wire.Component {
			return wire.Component{
				Type:     wire.ComponentRow,
				Key:      boundedKey("mach-", m.ID),
				Icon:     machineIcon(ms.Level),
				Title:    clip(cmp.Or(strings.TrimSpace(m.Name), strings.TrimSpace(m.ID), "未命名机器"), 200),
				Subtitle: clip(strings.Join(subParts, " · "), 400),
				Badge:    badge,
				Action:   &wire.Action{ID: "select-machine", Values: map[string]string{"m": m.ID, "t": tabItems}},
			}
		}

		makeCompact := func() wire.Section {
			sec := wire.Section{
				Title: machineLabel(m),
				Items: []wire.Component{makeRow()},
			}
			if ms.TotalBytes > 0 {
				sec.Items = append(sec.Items, machineProgressComponent(ms))
			}
			footer := machinePlainTextSummary(ms, scan)
			if m.LastError != "" && ms.TotalBytes > 0 {
				footer = "上次扫描失败：" + sshProblem(m, m.LastError).Title + " · " + footer
			}
			if ms.CleanCount > 0 {
				footer += fmt.Sprintf(" · 累计清理 %d 次共释放 %s", ms.CleanCount, FormatBytes(ms.TotalFreedBytes))
			}
			sec.Footer = clip(footer, 1800)
			if d.busy(m) {
				sec.Items = append(sec.Items, d.machineJobItems(m)...)
				return sec
			}
			if ms.SafeBytes > 0 {
				sec.Items = append(sec.Items, wire.Component{
					Type:  wire.ComponentButton,
					Key:   boundedKey("btn-clean-", m.ID),
					Title: fmt.Sprintf("一键安全清理 (%s)", FormatBytes(ms.SafeBytes)),
					Action: &wire.Action{
						ID:      "clean-safe",
						Values:  map[string]string{"m": m.ID},
						Confirm: fmt.Sprintf("对「%s」执行全部 safe 等级可重建缓存清理（预计释放 %s）？运行中容器、活跃仓库与 Agent 会话绝不触碰；冷会话上云需在「会话与台账」单独执行。", m.Name, FormatBytes(ms.SafeBytes)),
					},
				})
			} else {
				sec.Items = append(sec.Items, wire.Component{
					Type:   wire.ComponentButton,
					Key:    boundedKey("btn-scan-", m.ID),
					Title:  "扫描 " + machineLabel(m),
					Action: &wire.Action{ID: "scan", Values: map[string]string{"m": m.ID}},
				})
			}
			return sec
		}

		var sec wire.Section
		if compactTail {
			sec = makeCompact()
		} else {
			sec = wire.Section{
				Title: machineLabel(m),
				Items: []wire.Component{makeRow()},
			}
			if ms.TotalBytes > 0 {
				mountPoint := "/"
				var bd categoryBreakdown
				if scan != nil {
					mountPoint = cmp.Or(scan.Mount.MountPoint, "/")
					bd = summarizeCategories(scan.Items, &scan.Sessions)
				}
				sec.Items = append(sec.Items, machineProgressComponent(ms))

				if d.v2() && idx < chartLimit {
					afterSafe := min(ms.TotalBytes, ms.AvailableBytes+ms.SafeBytes)
					sec.Items = append(sec.Items, d.statComponents(boundedKey("fleet-stats-", m.ID), []wire.Stat{
						{Label: "当前可用", Value: FormatBytes(ms.AvailableBytes), Detail: "总计 " + FormatBytes(ms.TotalBytes)},
						{Label: "可回收合计", Value: FormatBytes(ms.ReclaimableBytes), Detail: "安全项 " + FormatBytes(ms.SafeBytes), Tone: toneIf(ms.ReclaimableBytes > 0, wire.TonePositive)},
						{Label: "清理后可用", Value: FormatBytes(afterSafe), Detail: "执行 Safe 项后", Tone: toneIf(ms.SafeBytes > 0, wire.TonePositive)},
						{Label: "冷会话 (>7d)", Value: fmt.Sprintf("%d 个", ms.Sessions.ColdCount), Detail: FormatBytes(ms.Sessions.ColdBytes) + " · 热 " + strconv.Itoa(ms.Sessions.HotCount) + " 个", Tone: toneIf(ms.Sessions.ColdBytes > 0, wire.ToneCaution)},
					})...)

					sec.Items = append(sec.Items, rootUsageChart(ms, mountPoint, "fleet"))
					if catChart, ok := reclaimCategoryChart(m, bd, "fleet"); ok {
						sec.Items = append(sec.Items, catChart)
					} else {
						sec.Items = append(sec.Items, wire.Component{
							Type:  wire.ComponentText,
							Style: wire.TextStyleSecondary,
							Text:  "当前未发现待清理缓存或冷会话（6 类均为 0 B）。",
						})
					}
					if d.v3() {
						sec.Items = append(sec.Items, machineBreakdownTable(ms, mountPoint, bd, "fleet"))
					} else {
						sec.Items = append(sec.Items, wire.Component{
							Type:  wire.ComponentText,
							Style: wire.TextStyleSecondary,
							Text:  machinePlainTextSummary(ms, scan),
						})
					}
				} else {
					sec.Items = append(sec.Items, wire.Component{
						Type:  wire.ComponentText,
						Style: wire.TextStyleSecondary,
						Text:  machinePlainTextSummary(ms, scan),
					})
				}
			} else if m.LastError != "" {
				// The problem is drawn with the job lines below.
			} else {
				sec.Items = append(sec.Items, wire.Component{
					Type:  wire.ComponentText,
					Style: wire.TextStyleSecondary,
					Text:  "点击下方「扫描」探测根分区磁盘水位、可回收缓存与 Agent 会话分布。",
				})
			}

			if ms.CleanCount > 0 {
				sec.Footer = fmt.Sprintf("上次清理释放 %s · 累计清理 %d 次共释放 %s",
					FormatBytes(ms.LastFreedBytes), ms.CleanCount, FormatBytes(ms.TotalFreedBytes))
			}

			sec.Items = append(sec.Items, d.machineJobItems(m)...)
			if !d.busy(m) {
				sec.Items = append(sec.Items,
					wire.Component{
						Type:   wire.ComponentButton,
						Key:    boundedKey("btn-scan-", m.ID),
						Title:  "扫描 " + machineLabel(m),
						Action: &wire.Action{ID: "scan", Values: map[string]string{"m": m.ID}},
					},
					wire.Component{
						Type:   wire.ComponentButton,
						Key:    boundedKey("btn-plan-", m.ID),
						Title:  "生成清理计划",
						Action: &wire.Action{ID: "open-plan", Values: map[string]string{"m": m.ID}},
					},
				)
				if ms.SafeBytes > 0 {
					sec.Items = append(sec.Items, wire.Component{
						Type:  wire.ComponentButton,
						Key:   boundedKey("btn-clean-", m.ID),
						Title: fmt.Sprintf("一键安全清理 (%s)", FormatBytes(ms.SafeBytes)),
						Action: &wire.Action{
							ID:      "clean-safe",
							Values:  map[string]string{"m": m.ID},
							Confirm: fmt.Sprintf("对「%s」执行全部 safe 等级可重建缓存清理（预计释放 %s）？运行中容器、活跃仓库与 Agent 会话绝不触碰；冷会话上云需在「会话与台账」单独执行。", m.Name, FormatBytes(ms.SafeBytes)),
						},
					})
				}
			}
			// Fall back to the compact card when expanding this machine would starve remaining machines past wire.ItemsMost.
			if used+len(sec.Items)+rem*3 > fleetBudget {
				sec = makeCompact()
			}
		}

		// Ultimate guard for abnormally large fleets (> ~95 machines): collapse to a single row so every machine fits.
		if used+len(sec.Items)+rem > fleetBudget && len(sec.Items) > 1 {
			sec.Items = sec.Items[:1]
		}
		used += len(sec.Items)
		secs = append(secs, sec)
	}

	manage := wire.Section{
		Title:  "机器节点管理",
		Footer: "自动同步 ~/.persona/config.json 与 ~/.ssh/config 中的机器节点，也支持手动新增或编辑 SSH 节点。",
		Items: []wire.Component{
			{
				Type:     wire.ComponentRow,
				Icon:     "settings",
				Title:    "编辑当前机器节点 (" + machineLabel(selected) + ")",
				Subtitle: cmp.Or(selected.Host, "localhost") + " · 来源 " + selected.Source,
				Action:   &wire.Action{ID: "edit-machine", Values: map[string]string{"m": selected.ID}},
			},
			{
				Type:   wire.ComponentButton,
				Title:  "手动添加 SSH 机器",
				Action: &wire.Action{ID: "new-machine"},
			},
		},
	}
	secs = append(secs, manage)
	return secs
}

// ---- Tab 2: Reclaimable Categories & Items Table ----

func (d draw) itemsTab(p page, m *Machine, scan *Scan) []wire.Section {
	ms := d.st.machineStatus(m)
	head := d.machineHeaderOrText(ms, scan)
	top := wire.Section{Items: append([]wire.Component{head}, d.machineJobItems(m)...)}
	if scan != nil && scan.Mount.TotalBytes > 0 {
		top.Items = append(top.Items, machineProgressComponent(ms))
	}
	if !d.busy(m) {
		top.Items = append(top.Items, wire.Component{
			Type:   wire.ComponentButton,
			Title:  "立即重新扫描 " + machineLabel(m),
			Action: &wire.Action{ID: "scan", Values: map[string]string{"m": m.ID}},
		})
	}
	if scan == nil {
		return []wire.Section{
			top,
			{
				Items: d.emptyComponents(
					"gauge",
					machineLabel(m)+" 尚未扫描",
					"点击立即扫描探测根分区容量及 Go、Node/Python、Docker、Rust/Xcode、系统日志与冷会话。",
					&wire.EmptyButton{
						Title:  "扫描 " + machineLabel(m),
						Action: wire.Action{ID: "scan", Values: map[string]string{"m": m.ID}},
					},
				),
			},
		}
	}

	q := strings.ToLower(strings.TrimSpace(p.Query))
	var filtered []ScanItem
	for _, it := range scan.Items {
		if q != "" {
			hay := strings.ToLower(it.Title + " " + it.Path + " " + it.Category + " " + CategoryLabels[it.Category] + " " + it.Reason)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		filtered = append(filtered, it)
	}

	var sessForChart *SessionSummary
	if q == "" {
		sessForChart = &scan.Sessions
	}
	bd := summarizeCategories(filtered, sessForChart)

	if d.v2() && scan.Mount.TotalBytes > 0 {
		chartSec := wire.Section{
			Title: "磁盘水位与分类占用图",
			Items: []wire.Component{rootUsageChart(ms, scan.Mount.MountPoint, "items")},
		}
		if catChart, ok := reclaimCategoryChart(m, bd, "items"); ok {
			chartSec.Items = append(chartSec.Items, catChart)
		} else {
			chartSec.Items = append(chartSec.Items, wire.Component{
				Type:  wire.ComponentText,
				Style: wire.TextStyleSecondary,
				Text:  "当前未发现待清理缓存或冷会话（6 类均为 0 B）。",
			})
		}
		if len(filtered) == 0 {
			catSummarySec := wire.Section{Title: "可回收分类汇总"}
			if d.v3() {
				catSummarySec.Items = append(catSummarySec.Items, machineBreakdownTable(ms, scan.Mount.MountPoint, bd, "items"))
			} else {
				catSummarySec.Items = append(catSummarySec.Items, wire.Component{
					Type:  wire.ComponentText,
					Style: wire.TextStyleSecondary,
					Text:  machinePlainTextSummary(ms, scan),
				})
			}
			return []wire.Section{
				top,
				chartSec,
				catSummarySec,
				{
					Items: d.emptyComponents("check", "没有发现待清理缓存项", "当前机器非常干净，或搜索条件没有匹配项。", nil),
				},
			}
		}
		return append([]wire.Section{top, chartSec}, d.itemsBreakdownSections(m, ms, scan, filtered, bd)...)
	}

	if len(filtered) == 0 {
		return []wire.Section{
			top,
			{
				Items: d.emptyComponents("check", "没有发现待清理缓存项", "当前机器非常干净，或搜索条件没有匹配项。", nil),
			},
		}
	}
	return append([]wire.Section{top}, d.itemsBreakdownSections(m, ms, scan, filtered, bd)...)
}

func (d draw) itemsBreakdownSections(m *Machine, ms MachineStatus, scan *Scan, filtered []ScanItem, bd categoryBreakdown) []wire.Section {
	catBytes := map[string]uint64{}
	catCounts := map[string]int{}
	for _, it := range filtered {
		catBytes[it.Category] += it.Bytes
		catCounts[it.Category]++
	}

	catSec := wire.Section{
		Title: "可回收分类汇总（按分类一键清理）",
	}
	if d.v3() {
		catSec.Items = append(catSec.Items, machineBreakdownTable(ms, scan.Mount.MountPoint, bd, "items"))
	} else {
		catSec.Items = append(catSec.Items, wire.Component{
			Type:  wire.ComponentText,
			Style: wire.TextStyleSecondary,
			Text:  machinePlainTextSummary(ms, scan),
		})
	}

	// Per-category quick clean rows
	for _, cid := range CategoryIDs {
		if catBytes[cid] == 0 {
			continue
		}
		catSec.Items = append(catSec.Items, wire.Component{
			Type:     wire.ComponentRow,
			Key:      "cat-clean-" + cid,
			Icon:     "folder",
			Title:    "清理：" + CategoryCompactLabels[cid],
			Subtitle: fmt.Sprintf("%d 项 · 预计释放 %s", catCounts[cid], FormatBytes(catBytes[cid])),
			Badge:    &wire.Badge{Text: FormatBytes(catBytes[cid]), Tone: wire.TonePositive},
			Action: &wire.Action{
				ID:      "clean-category",
				Values:  map[string]string{"m": m.ID, "cat": cid},
				Confirm: categoryConfirm(m, cid, catCounts[cid], catBytes[cid]),
			},
		})
	}

	// Detailed items table (4 phone-friendly columns; full path stays in tappable rows below)
	detailSec := wire.Section{
		Title:  "可回收项目明细（按体积排序）",
		Footer: "安全保护红线：绝不触碰运行中的服务容器、活跃 git 仓库未提交改动、7 天内活跃的 Agent 会话或凭据配置。",
	}
	shownItems := filtered[:min(len(filtered), maxShownScanItems)]
	if d.v3() {
		itemRows := make([][]string, 0, len(shownItems))
		for _, it := range shownItems {
			safetyTag := "safe"
			if it.Safety == SafetyReview {
				safetyTag = "review"
			} else if it.Category == CatColdAgentSession {
				safetyTag = "上云"
			}
			itemRows = append(itemRows, []string{
				clipCellWidth(it.Title, 12.5, 82.5, 13),
				CategoryAxisLabels[it.Category],
				safetyTag,
				FormatBytes(it.Bytes),
			})
		}
		detailSec.Items = append(detailSec.Items, wire.Component{
			Type: wire.ComponentTable,
			Key:  boundedKey("items-detail-tbl-", m.ID),
			Columns: []wire.TableColumn{
				{Title: "项目"},
				{Title: "分类"},
				{Title: "等级"},
				{Title: "体积", Align: "end"},
			},
			Rows: itemRows,
		})
	}

	seenItemKeys := map[string]bool{}
	for idx, it := range shownItems {
		bTone := wire.TonePositive
		bText := "safe · " + FormatBytes(it.Bytes)
		if it.Safety == SafetyReview {
			bTone = wire.ToneCaution
			bText = "review · " + FormatBytes(it.Bytes)
		} else if it.Category == CatColdAgentSession {
			bText = "上云 · " + FormatBytes(it.Bytes)
		}
		actVerb := "清理"
		if it.Action == "archive" {
			actVerb = "归档"
		}
		key := boundedKey("item-", it.ID)
		for n := idx; seenItemKeys[key]; n++ {
			key = boundedKey(fmt.Sprintf("item-%d-", n), it.ID)
		}
		seenItemKeys[key] = true
		detailSec.Items = append(detailSec.Items, wire.Component{
			Type:     wire.ComponentRow,
			Key:      key,
			Title:    clip(cmp.Or(strings.TrimSpace(it.Title), strings.TrimSpace(it.ID), "未命名项"), 200),
			Subtitle: clip(fmt.Sprintf("%s · %s", clip(it.Path, 48), it.Reason), 400),
			Badge:    &wire.Badge{Text: bText, Tone: bTone},
			Action: &wire.Action{
				ID:      "clean-item",
				Values:  map[string]string{"m": m.ID, "item": it.ID},
				Confirm: itemConfirm(m, it, actVerb),
			},
		})
	}
	if len(filtered) > len(shownItems) {
		detailSec.Footer = fmt.Sprintf("当前显示体积最大的前 %d 项（共 %d 项），可用搜索框缩小范围。%s", len(shownItems), len(filtered), detailSec.Footer)
	}

	return []wire.Section{catSec, detailSec}
}

// ---- Tab 3: Dry-Run Plan & Safe Clean ----

func (d draw) planTab(_ page, m *Machine, scan *Scan) []wire.Section {
	ms := d.st.machineStatus(m)
	head := d.machineHeaderOrText(ms, scan)
	if scan == nil {
		return []wire.Section{
			{Items: []wire.Component{head}},
			{
				Items: d.emptyComponents(
					"list",
					"尚未生成清理计划",
					"请先扫描该机器以生成 Dry-Run 安全清理计划。",
					&wire.EmptyButton{
						Title:  "扫描并生成计划",
						Action: wire.Action{ID: "scan", Values: map[string]string{"m": m.ID}},
					},
				),
			},
		}
	}

	plan, _ := BuildCleanPlan(m, scan, nil, nil, true)
	afterSafeAvail := scan.Mount.AvailableBytes + scan.SafeBytes
	if scan.Mount.TotalBytes > 0 {
		afterSafeAvail = min(scan.Mount.TotalBytes, afterSafeAvail)
	}
	var secs []wire.Section

	summarySec := wire.Section{
		Title:  "安全清理计划预览 (Dry-Run)",
		Footer: "安全等级说明：safe 为可无损自动重建的编译器/包缓存（一键 safe 清理不含冷会话上云）；review 为较大体积产物（如无头浏览器镜像），重新运行时按需下载。",
		Items:  append([]wire.Component{head}, d.machineJobItems(m)...),
	}
	summarySec.Items = append(summarySec.Items, d.statComponents(boundedKey("plan-stats-", m.ID), []wire.Stat{
		{Label: "当前可用", Value: FormatBytes(scan.Mount.AvailableBytes), Detail: scan.Mount.MountPoint},
		{Label: "Safe 预计释放", Value: FormatBytes(scan.SafeBytes), Tone: wire.TonePositive, Detail: "无损可重建项"},
		{Label: "清理后可用", Value: FormatBytes(afterSafeAvail), Tone: wire.TonePositive, Detail: fmt.Sprintf("%s → %s", FormatBytes(scan.Mount.AvailableBytes), FormatBytes(afterSafeAvail))},
		{Label: "Review 待确认", Value: FormatBytes(scan.ReviewBytes), Tone: toneIf(scan.ReviewBytes > 0, wire.ToneCaution)},
	})...)

	if d.v2() {
		if impact, ok := cleanImpactChart(ms, scan); ok {
			summarySec.Items = append(summarySec.Items, impact)
		} else if scan.Mount.TotalBytes > 0 {
			summarySec.Items = append(summarySec.Items, rootUsageChart(ms, scan.Mount.MountPoint, "plan"))
		}
	}

	if scan.SafeBytes > 0 {
		summarySec.Items = append(summarySec.Items, wire.Component{
			Type:  wire.ComponentButton,
			Title: fmt.Sprintf("一键执行全部 Safe 清理计划 (+%s)", FormatBytes(scan.SafeBytes)),
			Action: &wire.Action{
				ID:      "clean-safe",
				Values:  map[string]string{"m": m.ID},
				Confirm: fmt.Sprintf("确认对「%s」执行 Dry-Run 计划中的全部 safe 项（预计可用空间 %s → %s，释放 +%s）？", m.Name, FormatBytes(scan.Mount.AvailableBytes), FormatBytes(afterSafeAvail), FormatBytes(scan.SafeBytes)),
			},
		})
	}

	secs = append(secs, summarySec)

	// Interactive category checklist form for customized clean execution
	formSec := wire.Section{
		Title:  "按分类勾选执行清理",
		Footer: "打开对应分类开关后点击「按勾选分类执行清理」，自动在清理前后通过 df 实测释放字节并计入历史台账。",
	}
	catTotals := map[string]uint64{}
	catSafe := map[string]uint64{}
	for _, it := range scan.Items {
		if !it.InUse {
			catTotals[it.Category] += it.Bytes
			if it.Category != CatColdAgentSession && it.Safety == SafetySafe {
				catSafe[it.Category] += it.Bytes
			}
		}
	}
	hasAnyToggle := false
	for _, cid := range CategoryIDs {
		tot := catTotals[cid]
		if tot == 0 {
			continue
		}
		hasAnyToggle = true
		defaultVal := "true"
		if cid == CatColdAgentSession || catSafe[cid] == 0 {
			defaultVal = "false"
		}
		formSec.Items = append(formSec.Items, wire.Component{
			Type:  wire.ComponentField,
			Name:  "cat_" + cid,
			Label: fmt.Sprintf("%s (%s)", CategoryLabels[cid], FormatBytes(tot)),
			Kind:  wire.FieldKindToggle,
			Value: defaultVal,
		})
	}
	formSec.Items = append(formSec.Items, wire.Component{
		Type:  wire.ComponentField,
		Name:  "include_review",
		Label: "同时包含 review 等级项（如 Playwright 浏览器缓存）",
		Kind:  wire.FieldKindToggle,
		Value: "false",
	})
	if hasAnyToggle {
		formSec.Items = append(formSec.Items, wire.Component{
			Type:  wire.ComponentButton,
			Title: "按勾选分类执行清理",
			Action: &wire.Action{
				ID:      "clean-selected",
				Submit:  true,
				Values:  map[string]string{"m": m.ID},
				Confirm: fmt.Sprintf("确认对「%s」按所勾选的分类执行清理并记录 df 实测台账？", m.Name),
			},
		})
		secs = append(secs, formSec)
	}

	if plan != nil && len(plan.Items) > 0 {
		shownPlan := plan.Items[:min(len(plan.Items), 100)]
		if d.v3() {
			rows := make([][]string, 0, len(shownPlan))
			for _, it := range shownPlan {
				rows = append(rows, []string{
					clipCellWidth(it.Title, 12.5, 82.5, 13),
					CategoryAxisLabels[it.Category],
					it.Safety,
					FormatBytes(it.Bytes),
				})
			}
			secs = append(secs, wire.Section{
				Title: "Dry-Run 计划明细清单",
				Items: []wire.Component{
					{
						Type: wire.ComponentTable,
						Key:  boundedKey("plan-tbl-", m.ID),
						Columns: []wire.TableColumn{
							{Title: "计划项"},
							{Title: "分类"},
							{Title: "等级"},
							{Title: "预计释放", Align: "end"},
						},
						Rows: rows,
					},
				},
			})
		} else {
			planItems := make([]wire.Component, 0, len(shownPlan))
			for _, it := range shownPlan {
				planItems = append(planItems, wire.Component{
					Type:     wire.ComponentRow,
					Title:    clip(cmp.Or(strings.TrimSpace(it.Title), strings.TrimSpace(it.ID), "未命名项"), 200),
					Subtitle: CategoryCompactLabels[it.Category] + " · " + it.Safety,
					Value:    FormatBytes(it.Bytes),
				})
			}
			secs = append(secs, wire.Section{
				Title: "Dry-Run 计划明细清单",
				Items: planItems,
			})
		}
	}

	return secs
}

// ---- Tab 4: Coding Agent Session Tiering & Cleanup History Ledger ----

func (d draw) sessionsAndHistoryTab(p page, m *Machine, _ *Scan) []wire.Section {
	ms := d.st.machineStatus(m)
	var secs []wire.Section
	if d.v4() {
		secs = d.coldSections(m, p.SessQuery, d.deep, wire.ItemsMost-d.leadItems-60)
	}

	sess := ms.Sessions
	cloudStat := wire.Stat{Label: "云端备份", Value: "未连接", Detail: "需要 disklessly CLI", Tone: wire.ToneCaution}
	if c := sess.Cloud; c != nil {
		backend := cmp.Or(c.Backend.ID, c.Backend.Type, "backend")
		if c.Backend.Reachable {
			cloudStat = wire.Stat{Label: "云端备份", Value: fmt.Sprintf("%d 个", c.Counts["archived"]+c.Counts["remote"]),
				Detail: fmt.Sprintf("%s · %s · 可达 %d ms", FormatBytes(c.ArchivedBytes), backend, c.Backend.LatencyMs), Tone: wire.TonePositive}
		} else {
			cloudStat = wire.Stat{Label: "云端备份", Value: "不可达", Detail: clip(backend+" · "+cmp.Or(c.Backend.Error, "后端连不上"), 80), Tone: wire.ToneDanger}
		}
	}
	sessSec := wire.Section{
		Title:  "Coding Agent 会话冷热分层 (" + machineLabel(m) + ")",
		Footer: "冷会话由 disklessly 上传到备份后端并写入索引后才移除本地副本；7 天内活跃、正在打开或仍被引用的会话保持原样。后端不可达或未安装 CLI 时不会归档或删除任何会话。",
		Items: d.statComponents(boundedKey("sess-stats-", m.ID), []wire.Stat{
			{Label: "热会话 (≤7天)", Value: fmt.Sprintf("%d 个", sess.HotCount), Detail: FormatBytes(sess.HotBytes) + " · 活跃保护中", Tone: wire.TonePositive},
			{Label: "冷会话 (>7天)", Value: fmt.Sprintf("%d 个", sess.ColdCount), Detail: FormatBytes(sess.ColdBytes) + " · 可上传归档", Tone: toneIf(sess.ColdBytes > 0, wire.ToneCaution)},
			cloudStat,
			{Label: "旧版本地归档包", Value: fmt.Sprintf("%d 个", sess.ArchivedCount), Detail: FormatBytes(sess.ArchivedBytes) + " · " + cmp.Or(sess.ArchiveDir, "~/.disklessly/session-archives")},
		}),
	}
	if sess.Note != "" {
		sessSec.Items = append(sessSec.Items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary, Text: sess.Note})
	}
	if c := sess.Cloud; c != nil && len(c.Segments.Missing) > 0 {
		sessSec.Items = append(sessSec.Items, wire.Component{Type: wire.ComponentText, Style: wire.TextStyleSecondary,
			Text: fmt.Sprintf("备份后端缺少 %d 个归档分段，相关会话无法恢复。", len(c.Segments.Missing))})
	}

	if len(sess.Tools) > 0 && d.v3() {
		var rows [][]string
		for _, tl := range sess.Tools[:min(len(sess.Tools), 100)] {
			rows = append(rows, []string{
				clipCellWidth(tl.Label, 12.5, 82.5, 12),
				compactCountBytes(tl.HotCount, tl.HotBytes),
				compactCountBytes(tl.ColdCount, tl.ColdBytes),
				compactCountBytes(tl.ArchivedCount, tl.ArchivedBytes),
			})
		}
		sessSec.Items = append(sessSec.Items, wire.Component{
			Type: wire.ComponentTable,
			Key:  boundedKey("sess-tools-tbl-", m.ID),
			Columns: []wire.TableColumn{
				{Title: "工具"},
				{Title: "热 ≤7d", Align: "end"},
				{Title: "冷 >7d", Align: "end"},
				{Title: "已归档", Align: "end"},
			},
			Rows: rows,
		})
	} else if len(sess.Tools) > 0 {
		for _, tl := range sess.Tools[:min(len(sess.Tools), 20)] {
			sessSec.Items = append(sessSec.Items, wire.Component{
				Type:  wire.ComponentRow,
				Title: clip(cmp.Or(strings.TrimSpace(tl.Label), strings.TrimSpace(tl.Tool), "Agent"), 200),
				Subtitle: fmt.Sprintf("热 %d (%s) · 冷 %d (%s) · 已归档 %d (%s)",
					tl.HotCount, FormatBytes(tl.HotBytes),
					tl.ColdCount, FormatBytes(tl.ColdBytes),
					tl.ArchivedCount, FormatBytes(tl.ArchivedBytes)),
			})
		}
	} else {
		sessSec.Items = append(sessSec.Items, wire.Component{
			Type:  wire.ComponentText,
			Style: wire.TextStyleSecondary,
			Text:  "扫描机器后将在此显示 Claude Code 与 Codex 的冷热会话体积分布。",
		})
	}

	secs = append(secs, sessSec)

	// Cleanup History Ledger
	histSec := wire.Section{
		Title:  "历史清理台账",
		Footer: "记录每次清理前后 df 实测可用空间对比（如 5.4 GiB → 18.0 GiB (+12.6 GiB)）。",
	}
	if len(d.st.History) == 0 {
		histSec.Items = d.emptyComponents(
			"clock",
			"尚无历史清理记录",
			"在多机总览或清理计划页执行安全清理后，每次实测释放的空间对比都会记录在这里。",
			nil,
		)
	} else {
		if d.v3() {
			var hRows [][]string
			for _, h := range d.st.History[:min(len(d.st.History), 50)] {
				hRows = append(hRows, []string{
					h.CreatedAt.In(d.loc).Format("01-02 15:04"),
					clipCellWidth(histMachineLabel(d.st, h), 12.5, 48.0, 10),
					compactAvailChange(h),
					compactFreedCell(h.FreedBytes),
				})
			}
			histSec.Items = append(histSec.Items, wire.Component{
				Type: wire.ComponentTable,
				Key:  "hist-tbl",
				Columns: []wire.TableColumn{
					{Title: "时间"},
					{Title: "机器"},
					{Title: "可用变化"},
					{Title: "释放", Align: "end"},
				},
				Rows: hRows,
			})
		}
		seenHistKeys := map[string]bool{}
		for idx, h := range d.st.History[:min(len(d.st.History), 20)] {
			key := boundedKey("hist-row-", h.ID)
			for n := idx; seenHistKeys[key]; n++ {
				key = boundedKey(fmt.Sprintf("hist-row-%d-", n), h.ID)
			}
			seenHistKeys[key] = true
			histSec.Items = append(histSec.Items, wire.Component{
				Type:     wire.ComponentRow,
				Key:      key,
				Icon:     "check",
				Title:    clip(fmt.Sprintf("%s · %s", h.MachineName, h.Summary), 200),
				Subtitle: clip(fmt.Sprintf("%s · 清理 %d 项（%s）", d.when(h.CreatedAt), len(h.Items), strings.Join(h.Categories, "、")), 400),
				Badge:    &wire.Badge{Text: "+" + FormatBytes(h.FreedBytes), Tone: wire.TonePositive},
			})
		}
	}
	secs = append(secs, histSec)
	return secs
}

// ---- Machine Form (Add / Edit SSH Machine) ----

func (d draw) machineForm(p page, m *Machine) *wire.View {
	title := "添加 SSH 远程机器"
	hostVal, nameVal, userVal, portVal, idFileVal := "", "", "", "", ""
	if m != nil {
		title = "编辑机器节点 · " + m.Name
		hostVal = m.Host
		nameVal = m.Name
		userVal = m.User
		if m.Port > 0 {
			portVal = strconv.Itoa(m.Port)
		}
		idFileVal = m.IdentityFile
	}

	var items []wire.Component
	if m == nil || !m.IsLocal {
		items = append(items, wire.Component{
			Type:        wire.ComponentField,
			Name:        "host",
			Label:       "SSH Host 别名或地址 *",
			Kind:        wire.FieldKindText,
			Value:       hostVal,
			Placeholder: "例如 armbox、devvm、mini-2 或 dev.example.com",
		})
	}
	items = append(items,
		wire.Component{
			Type:        wire.ComponentField,
			Name:        "name",
			Label:       "显示名称",
			Kind:        wire.FieldKindText,
			Value:       nameVal,
			Placeholder: "例如 armbox (ARM VM) 或 devvm (主力开发机)",
		},
	)
	if m == nil || !m.IsLocal {
		items = append(items,
			wire.Component{
				Type:        wire.ComponentField,
				Name:        "user",
				Label:       "SSH 用户名（可选，默认读 ~/.ssh/config）",
				Kind:        wire.FieldKindText,
				Value:       userVal,
				Placeholder: "例如 ubuntu、armbox",
			},
			wire.Component{
				Type:        wire.ComponentField,
				Name:        "port",
				Label:       "SSH 端口（可选，默认 22）",
				Kind:        wire.FieldKindNumber,
				Value:       portVal,
				Placeholder: "22",
			},
			wire.Component{
				Type:        wire.ComponentField,
				Name:        "identity_file",
				Label:       "私钥路径（可选，默认读 ~/.ssh/config）",
				Kind:        wire.FieldKindText,
				Value:       idFileVal,
				Placeholder: "~/.ssh/id_ed25519",
			},
		)
	}
	items = append(items,
		wire.Component{
			Type:   wire.ComponentButton,
			Title:  "保存机器节点",
			Action: &wire.Action{ID: "save-machine", Submit: true},
		},
		wire.Component{
			Type:   wire.ComponentButton,
			Title:  "取消",
			Action: &wire.Action{ID: "cancel"},
		},
	)
	if m != nil && !m.IsLocal {
		items = append(items, wire.Component{
			Type:  wire.ComponentButton,
			Title: "移除此机器节点",
			Action: &wire.Action{
				ID:      "delete-machine",
				Values:  map[string]string{"m": m.ID},
				Confirm: fmt.Sprintf("确认从 Disklessly 机器管理中移除「%s」？", m.Name),
			},
		})
	}
	return d.fitVersion(&wire.View{
		Title:    title,
		Fallback: title,
		State:    ext.EncodeState(p),
		Sections: []wire.Section{{Items: items}},
	})
}

// ---- Overview Card ----

func (a *app) card(_ context.Context, r *ext.UIRequest) (*wire.View, error) {
	var out *wire.View
	err := a.db().read(func(st *State) {
		d := a.drawer(st, r)
		statuses := st.statuses()
		fallback := d.fallbackSummary(statuses)
		var items []wire.Component
		if len(statuses) > 0 {
			var totalReclaim uint64
			var featured *MachineStatus
			for i := range statuses {
				ms := &statuses[i]
				totalReclaim += ms.ReclaimableBytes
				if ms.TotalBytes > 0 && (featured == nil || watermarkChartRank(ms.Level) < watermarkChartRank(featured.Level) ||
					(watermarkChartRank(ms.Level) == watermarkChartRank(featured.Level) && ms.UsedPercent > featured.UsedPercent)) {
					featured = ms
				}
			}

			alertText, alertTone := fleetAlertSummary(statuses)
			if d.v2() {
				items = append(items, wire.Component{
					Type: wire.ComponentStatGrid,
					Key:  "card-stats",
					Stats: []wire.Stat{
						{Label: "机器", Value: fmt.Sprintf("%d 台", len(statuses))},
						{Label: "水位", Value: alertText, Tone: alertTone},
						{Label: "可回收", Value: FormatBytes(totalReclaim), Tone: toneIf(totalReclaim > 0, wire.TonePositive)},
					},
				})
				if featured != nil {
					mp := "/"
					if sc := st.Scans[featured.Machine.ID]; sc != nil {
						mp = cmp.Or(sc.Mount.MountPoint, "/")
					}
					ch := rootUsageChart(*featured, mp, "card")
					ch.Title = fmt.Sprintf("%s · %.0f%%", machineLabel(featured.Machine), featured.UsedPercent)
					items = append(items, ch)
				}
			} else {
				items = append(items, wire.Component{
					Type:   wire.ComponentMetric,
					Label:  "多机水位",
					Value:  alertText,
					Detail: fmt.Sprintf("%d 台机器 · 可回收 %s", len(statuses), FormatBytes(totalReclaim)),
					Tone:   alertTone,
				})
			}

			for _, ms := range statuses[:min(len(statuses), 4)] {
				sub := "尚未扫描"
				if ms.TotalBytes > 0 {
					sub = fmt.Sprintf("已用 %.0f%%（可用 %s）· 可释放 %s",
						ms.UsedPercent, FormatBytes(ms.AvailableBytes), FormatBytes(ms.ReclaimableBytes))
					if ms.Partial {
						sub = fmt.Sprintf("已用 %.0f%%（可用 %s）· 缓存未统计完",
							ms.UsedPercent, FormatBytes(ms.AvailableBytes))
					}
				} else if ms.Machine.LastError != "" {
					sub = "离线：" + sshProblem(ms.Machine, ms.Machine.LastError).Title
				}
				items = append(items, wire.Component{
					Type:     wire.ComponentRow,
					Key:      boundedKey("card-mach-", ms.Machine.ID),
					Icon:     machineIcon(ms.Level),
					Title:    machineLabel(ms.Machine),
					Subtitle: sub,
					Badge:    healthBadge(ms),
				})
			}
			if len(st.History) > 0 {
				last := st.History[0]
				items = append(items, wire.Component{
					Type:  wire.ComponentText,
					Style: wire.TextStyleCaption,
					Text:  fmt.Sprintf("总可释放 %s · 最近清理 %s：%s", FormatBytes(totalReclaim), last.MachineName, last.Summary),
				})
			} else if totalReclaim > 0 {
				items = append(items, wire.Component{
					Type:  wire.ComponentText,
					Style: wire.TextStyleCaption,
					Text:  fmt.Sprintf("共 %d 台机器 · 总可回收 %s", len(statuses), FormatBytes(totalReclaim)),
				})
			}
		}
		if len(items) == 0 {
			msg := "Disklessly 机器磁盘管理：探测本机与 SSH 远程机器磁盘水位、清理缓存并归档冷会话。"
			items = []wire.Component{{
				Type:  wire.ComponentText,
				Style: wire.TextStyleSecondary,
				Text:  msg,
			}}
			if fallback == "" {
				fallback = msg
			}
		}
		out = d.fitVersion(&wire.View{Fallback: fallback, Sections: []wire.Section{{Items: items}}})
	})
	return out, err
}

// ---- UI Actions ----

func (a *app) action(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
	var p page
	if err := r.DecodeState(&p); err != nil {
		return nil, &ext.Error{Code: "invalid", Message: "invalid page state"}
	}
	res := &wire.ActionResult{}
	var deep *deepResult

	switch r.Action {
	case "machine":
		if r.Values["machine"] == "+" {
			p.Mode = modeNewMachine
		} else {
			p.Machine = r.Values["machine"]
			p.Mode = modeHome
		}
	case "tab":
		p.Tab = r.Values["tab"]
		p.Mode = modeHome
	case "search":
		p.Query = strings.TrimSpace(r.Values["q"])
	case "details":
		p.Open = toggle(p.Open, r.Values["k"])
	case "scan", "scan-all", "clean-safe", "clean-category", "clean-item", "clean-selected":
		toast, err := a.mainJob(r, &p)
		if err != nil {
			return nil, &ext.Error{Code: "invalid", Message: userMessage(err)}
		}
		res.Toast = toast
	case "cold-search":
		p.SessQuery = clipQuery(r.Values["q"])
		p.Tab = tabSessions
	case "cold-deep":
		p.SessQuery = clipQuery(cmp.Or(strings.TrimSpace(r.Values["q"]), p.SessQuery))
		p.Tab = tabSessions
		var err error
		if deep, err = a.coldDeep(ctx, cmp.Or(r.Values["m"], p.Machine), p.SessQuery); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: userMessage(err)}
		}
	case "cold-refresh", "cold-offload", "cold-restore", "archive-sessions", "cold-install", "cold-brew":
		p.Machine = cmp.Or(r.Values["m"], p.Machine)
		p.Tab = tabSessions
		toast, err := a.coldAction(ctx, r.Action, p.Machine, r.Values["id"])
		if err != nil {
			return nil, &ext.Error{Code: "invalid", Message: userMessage(err)}
		}
		res.Toast = toast
	case "select-machine":
		p.Machine = cmp.Or(r.Values["m"], p.Machine)
		p.Tab = cmp.Or(r.Values["t"], tabItems)
		p.Mode = modeHome
	case "open-plan":
		p.Machine = cmp.Or(r.Values["m"], p.Machine)
		p.Tab = tabPlan
		p.Mode = modeHome
	case "new-machine":
		p.Mode = modeNewMachine
	case "edit-machine":
		if m := r.Values["m"]; m != "" {
			p.Machine = m
		}
		p.Mode = modeEditMach
	case "cancel":
		p.Mode = modeHome
	default:
		err := a.db().update(func(st *State, now time.Time) error {
			return a.handleStateAction(st, now, r, &p, res)
		})
		if err != nil {
			return nil, &ext.Error{Code: "invalid", Message: userMessage(err)}
		}
	}

	var err error
	res.View, err = a.pageView(r, p, deep)
	return res, err
}

func (a *app) handleStateAction(
	st *State,
	now time.Time,
	r *ext.UIRequest,
	p *page,
	res *wire.ActionResult,
) error {
	targetID := cmp.Or(r.Values["m"], p.Machine, "local")
	switch r.Action {
	case "save-machine":
		port, _ := strconv.Atoi(strings.TrimSpace(r.Values["port"]))
		in := MachineInput{
			Host:         r.Values["host"],
			Name:         r.Values["name"],
			User:         r.Values["user"],
			Port:         port,
			IdentityFile: r.Values["identity_file"],
			Source:       "manual",
		}
		if p.Mode == modeEditMach {
			in.ID = p.Machine
			if existing := st.machine(p.Machine); existing != nil && existing.IsLocal {
				in.Host = "localhost"
			}
		}
		m, err := st.upsertMachine(in, now)
		if err != nil {
			return err
		}
		p.Machine = m.ID
		p.Mode = modeHome
		res.Toast = "已保存机器节点「" + m.Name + "」"

	case "delete-machine":
		if err := st.deleteMachine(targetID); err != nil {
			return err
		}
		p.Machine = "local"
		p.Mode = modeHome
		res.Toast = "已移除机器节点"

	default:
		return fmt.Errorf("unknown action %q", r.Action)
	}
	return nil
}

// ---- UI Helpers ----

func (d draw) machineHeaderOrText(ms MachineStatus, scan *Scan) wire.Component {
	if d.v4() {
		return d.machineHeaderComponent(ms, scan)
	}
	return wire.Component{
		Type: wire.ComponentText,
		Text: machinePlainTextSummary(ms, scan),
	}
}

func (d draw) machineHeaderComponent(ms MachineStatus, scan *Scan) wire.Component {
	m := ms.Machine
	b := healthBadge(ms)
	facts := []wire.Fact{
		{Label: "主机", Value: cmp.Or(m.Host, "localhost")},
		{Label: "来源", Value: m.Source},
	}
	if m.Platform != "" {
		facts = append(facts, wire.Fact{Label: "平台", Value: m.Platform})
	}
	if scan != nil && scan.Mount.TotalBytes > 0 {
		facts = append(facts,
			wire.Fact{Label: "根分区容量", Value: FormatBytes(scan.Mount.TotalBytes)},
			wire.Fact{Label: "可用空间", Value: FormatBytes(scan.Mount.AvailableBytes)},
			wire.Fact{Label: "可回收缓存", Value: FormatBytes(scan.ReclaimableBytes)},
		)
	}
	if ms.CleanCount > 0 {
		facts = append(facts, wire.Fact{Label: "累计已释放", Value: FormatBytes(ms.TotalFreedBytes)})
	}
	sub := "本机或 SSH 远程节点"
	if scan != nil {
		sub = fmt.Sprintf("挂载点 %s (%s) · 扫描模式 %s · 更新于 %s",
			scan.Mount.MountPoint, scan.Mount.FSType, scan.ScanMode, d.when(scan.ScannedAt))
	}
	var badges []wire.Badge
	if b != nil {
		badges = append(badges, *b)
	}
	return wire.Component{
		Type:     wire.ComponentHeader,
		Title:    machineLabel(m),
		Subtitle: sub,
		Badges:   badges,
		Facts:    facts,
	}
}

func healthBadge(ms MachineStatus) *wire.Badge {
	switch ms.Level {
	case HealthUnscanned:
		return &wire.Badge{Text: "未扫描", Tone: wire.ToneNeutral}
	case HealthOffline:
		return &wire.Badge{Text: "离线", Tone: wire.ToneCaution}
	case HealthCritical:
		return &wire.Badge{Text: fmt.Sprintf("红色告警 %.0f%%", ms.UsedPercent), Tone: wire.ToneDanger}
	case HealthWarning:
		return &wire.Badge{Text: fmt.Sprintf("橙色预警 %.0f%%", ms.UsedPercent), Tone: wire.ToneCaution}
	default:
		if ms.TotalBytes > 0 {
			return &wire.Badge{Text: fmt.Sprintf("正常 %.0f%%", ms.UsedPercent), Tone: wire.TonePositive}
		}
		return &wire.Badge{Text: "待扫描", Tone: wire.ToneNeutral}
	}
}

func progressTone(level string) string {
	switch level {
	case HealthCritical:
		return wire.ToneDanger
	case HealthWarning, HealthOffline:
		return wire.ToneCaution
	case HealthUnscanned:
		return wire.ToneNeutral
	default:
		return wire.TonePositive
	}
}

func machineIcon(level string) string {
	switch level {
	case HealthCritical, HealthWarning, HealthOffline:
		return "warning"
	default:
		return "gauge"
	}
}

func toneIf(cond bool, t string) string {
	if cond {
		return t
	}
	return ""
}

func clip(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return string(r[:1])
	}
	return string(r[:maxRunes-1]) + "…"
}

// sectionItemCount counts components across secs using wire.CheckView's accounting.
func sectionItemCount(secs []wire.Section) int {
	n := 0
	for _, s := range secs {
		for _, e := range s.Items {
			n += max(1, len(e.Tiles)+len(e.Nodes)+len(e.Pins)+len(e.Markers))
		}
	}
	return n
}

// boundedKey returns prefix+raw unchanged when <= 128 runes, or truncates raw with an 8-hex FNV-1a suffix.
func boundedKey(prefix, raw string) string {
	full := prefix + raw
	if utf8.RuneCountInString(full) <= 128 {
		return full
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(raw))
	suffix := fmt.Sprintf("-%08x", h.Sum32())
	keep := 128 - utf8.RuneCountInString(prefix) - len(suffix)
	if keep <= 0 {
		pr := []rune(prefix)
		if len(pr) > 128-len(suffix) {
			pr = pr[:max(0, 128-len(suffix))]
		}
		return string(pr) + suffix
	}
	rr := []rune(raw)
	if len(rr) > keep {
		rr = rr[:keep]
	}
	return prefix + string(rr) + suffix
}

func (d draw) when(t time.Time) string {
	if t.IsZero() {
		return "未知"
	}
	ago := d.now.Sub(t)
	switch {
	case ago < time.Minute:
		return "刚刚"
	case ago < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(ago.Minutes()))
	case ago < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(ago.Hours()))
	case ago < 7*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(ago.Hours()/24))
	}
	return t.In(d.loc).Format(time.DateOnly)
}

// sessionOffloadConfirm describes what a cold-session run does: it covers every agent at once,
// uploads off the machine and deletes the local copies only after a verified read-back.
func sessionOffloadConfirm(m *Machine) string {
	return fmt.Sprintf("把「%s」上所有 Agent（Claude Code、Codex 等）7 天外、未被运行中进程、herdr 或 Persona 引用的冷会话上传到备份后端？上传后读回校验通过才删除本地副本；后端不可达或无法确认引用时不会删除任何会话。", m.Name)
}

func categoryConfirm(m *Machine, cid string, n int, bytes uint64) string {
	if cid == CatColdAgentSession {
		return sessionOffloadConfirm(m)
	}
	return fmt.Sprintf("在「%s」上清理分类「%s」（共 %d 项，预计释放 %s）？", m.Name, CategoryLabels[cid], n, FormatBytes(bytes))
}

func itemConfirm(m *Machine, it ScanItem, verb string) string {
	if it.Category == CatColdAgentSession {
		return sessionOffloadConfirm(m)
	}
	return fmt.Sprintf("确认在「%s」上%s「%s」（路径 %s，预计释放 %s）？", m.Name, verb, it.Title, it.Path, FormatBytes(it.Bytes))
}
