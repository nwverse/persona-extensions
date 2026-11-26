package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// uploadFleet is a local computer whose CLI is ready, build-box without a CLI and nas-box without
// a backend; apply answers the upload run and gate, when set, holds it until closed.
type uploadFleet struct {
	a     *app
	apply string
	err   error
	gate  chan struct{}
}

func newUploadFleet(t *testing.T) *uploadFleet {
	t.Helper()
	f := &uploadFleet{a: newTestApp(t), apply: sampleSessionApply}
	f.a.runner = func(ctx context.Context, m *Machine, s string, d time.Duration) (string, error) {
		switch {
		case m.ID == "build-box" && strings.Contains(s, "sessions list --state all"):
			return "@@SECTION:COLD_UNAME\nLinux x86_64\n@@COLD_NOCLI\n", nil
		case m.ID == "nas-box" && strings.Contains(s, "sessions list --state all"):
			return coldRefreshOut(coldListJSON(), `{"ok":true,"backend":{"id":"","type":"","reachable":false},"counts":{},"segments":{}}`), nil
		case strings.Contains(s, "sessions --apply"):
			if f.gate != nil {
				<-f.gate
			}
			return f.apply, f.err
		}
		return coldRunner(nil)(ctx, m, s, d)
	}
	for _, h := range []string{"build-box", "nas-box"} {
		if err := f.a.db().update(func(st *State, now time.Time) error {
			_, err := st.upsertMachine(MachineInput{Host: h, Name: h}, now)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.act(t, "", "cold-refresh", map[string]string{"m": ""})
	f.a.waitJobs()
	return f
}

func (f *uploadFleet) act(t *testing.T, state, action string, values map[string]string) *wire.ActionResult {
	t.Helper()
	res, err := f.a.actionSessions(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{State: state, Action: action, Values: values}})
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatalf("%s: CheckView: %v", action, err)
	}
	return res
}

// view draws AI 对话 with machine selected ("" for all computers).
func (f *uploadFleet) view(t *testing.T, machine string) *wire.View {
	t.Helper()
	return f.act(t, ext.EncodeState(sessPage{Machine: machine}), "search", map[string]string{"q": ""}).View
}

func offloadButtons(items []wire.Component) []wire.Component {
	var out []wire.Component
	for _, c := range items {
		if c.Type == wire.ComponentButton && c.Action != nil && c.Action.ID == "cold-offload" {
			out = append(out, c)
		}
	}
	return out
}

func allOffloadButtons(v *wire.View) []wire.Component {
	var out []wire.Component
	for _, s := range v.Sections {
		out = append(out, offloadButtons(s.Items)...)
	}
	return out
}

// F1: the upload is reachable from 全部电脑, offered only on a ready computer, and a computer
// that is not ready says why instead.
func TestUploadReachableFromAllComputersAndOnlyWhenReady(t *testing.T) {
	f := newUploadFleet(t)

	all := f.view(t, "")
	if b := offloadButtons(itemsOf(all, "local")); len(b) != 1 || b[0].Title != coldOffloadText || b[0].Action.Values["m"] != "local" {
		t.Fatalf("全部电脑: the ready computer has no upload button: %+v", itemsOf(all, "local"))
	}
	for _, id := range []string{"build-box", "nas-box"} {
		if b := offloadButtons(itemsOf(all, id)); len(b) != 0 {
			t.Fatalf("%s is not ready but offers the upload: %+v", id, b)
		}
	}
	if n := len(allOffloadButtons(all)); n != 1 {
		t.Fatalf("全部电脑: %d upload buttons, want the ready computer's only", n)
	}
	if txt := texts(all); !strings.Contains(txt, "1 台电脑可以搬") || !strings.Contains(txt, "其余 2 台还不能搬") {
		t.Fatalf("全部电脑 does not say where the upload is:\n%s", txt)
	}

	for id, why := range map[string]string{"build-box": coldStateLabel(ColdNoCLI), "nas-box": coldStateLabel(ColdNoBackend)} {
		v := f.view(t, id)
		if b := allOffloadButtons(v); len(b) != 0 {
			t.Fatalf("%s selected: upload offered although it is not ready: %+v", id, b)
		}
		if txt := texts(v); !strings.Contains(txt, "现在还不能把对话搬到网盘（"+why+"）") {
			t.Fatalf("%s selected: no reason given:\n%s", id, txt)
		}
	}
	nas := f.view(t, "nas-box")
	if !strings.Contains(fmt.Sprint(machineItems(nas)), "disklessly backend add rclone") {
		t.Fatal("nas-box: the command that sets up a network drive is missing")
	}

	// A stale tap on a computer known not to be ready is refused with the same reason, and starts nothing.
	_, err := f.a.actionSessions(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{
		State: ext.EncodeState(sessPage{Machine: "build-box"}), Action: "cold-offload", Values: map[string]string{"m": "build-box"}}})
	if err == nil || !strings.Contains(err.Error(), "现在还不能把对话搬到网盘") || !strings.Contains(err.Error(), "配套小工具") {
		t.Fatalf("upload on build-box: %v", err)
	}
	if j, ok := f.a.jobSnapshot()["build-box"]; ok && j.Kind == jobOffload {
		t.Fatalf("a refused upload started a job: %+v", j)
	}

	one := f.view(t, "local")
	if b := allOffloadButtons(one); len(b) != 1 || !strings.Contains(b[0].Action.Confirm, "读回来核对") ||
		!strings.Contains(b[0].Action.Confirm, "核对无误才删掉电脑上的那份") || !strings.Contains(b[0].Action.Confirm, "一个都不删") {
		t.Fatalf("one computer: upload button or its confirm: %+v", b)
	}
}

// F2: a failed upload offers a real retry, the text names that button, and a run on the hub's own
// computer is not called an SSH connection.
func TestFailedUploadOffersRetryAndNamesNoSSHForThisComputer(t *testing.T) {
	f := newUploadFleet(t)
	f.apply = "@@SESSIONS_RC 1\n@@SESSIONS_JSON {\"ok\":false,\"error\":{\"code\":1,\"message\":\"index locked\",\"name\":\"EX_SOFTWARE\"}}\n"
	f.act(t, ext.EncodeState(sessPage{Machine: "local"}), "cold-offload", map[string]string{"m": "local"})
	f.a.waitJobs()
	items := itemsOf(f.view(t, "local"), "local")
	b := offloadButtons(items)
	if len(b) != 1 || b[0].Title != coldRetryText || b[0].Action.Confirm != sessionOffloadConfirm(&Machine{Name: "local (x)", IsLocal: true}) {
		t.Fatalf("no retry button after a failed upload: %+v", items)
	}
	txt := fmt.Sprint(items)
	if !strings.Contains(txt, "没有成功") || !strings.Contains(txt, "「"+coldRetryText+"」") {
		t.Fatalf("the failure does not point at the retry button: %s", txt)
	}
	if !buttonsHave(items, "详情") {
		t.Fatal("the raw CLI answer is not behind 详情")
	}

	// The runner fails with an SSH-shaped error on the hub's own computer.
	f.apply, f.err = "", fmt.Errorf("SSH 连接 localhost 失败：exit status 255")
	f.act(t, ext.EncodeState(sessPage{Machine: "local"}), "cold-offload", map[string]string{"m": "local"})
	f.a.waitJobs()
	j := f.a.jobSnapshot()["local"]
	if j.Err == "" || strings.Contains(j.Err, "SSH") || strings.Contains(j.Err, "hub") || strings.Contains(j.Err, "远程登录localhost") {
		t.Fatalf("local failure is worded as SSH: %q", j.Err)
	}
	items = itemsOf(f.view(t, "local"), "local")
	if txt := fmt.Sprint(items); !strings.Contains(txt, "「"+coldRetryText+"」") || strings.Contains(txt, "点「重试」") {
		t.Fatalf("local failure: %s", txt)
	}
	if len(offloadButtons(items)) != 1 {
		t.Fatal("no retry after a run error")
	}
}

func buttonsHave(items []wire.Component, title string) bool {
	return buttonIn(items, title) != nil
}

// F3: an upload the CLI refuses because no backend is configured (exit 78) says so and gives the
// same fix as a computer whose check found no backend.
func TestUploadWithoutBackendExplainsAndShowsFix(t *testing.T) {
	f := newUploadFleet(t)
	f.apply = "@@SESSIONS_RC 78\n@@SESSIONS_JSON {\"ok\":false,\"error\":{\"code\":78,\"message\":\"no backend configured\",\"name\":\"EX_CONFIG\"}}\n@@SESSIONS_ERR no backend configured\n"
	f.act(t, ext.EncodeState(sessPage{Machine: "local"}), "cold-offload", map[string]string{"m": "local"})
	f.a.waitJobs()
	j := f.a.jobSnapshot()["local"]
	if !strings.Contains(j.Err, "还没有设置网盘") || !strings.Contains(j.Err, "一个都没有搬走或删除") || strings.Contains(j.Err, "退出码") {
		t.Fatalf("no-backend failure: %q", j.Err)
	}
	if j.Command != noBackendCommand || !strings.Contains(j.Raw, "no backend configured") {
		t.Fatalf("no-backend fix: command %q raw %q", j.Command, j.Raw)
	}
	items := itemsOf(f.view(t, "local"), "local")
	var code string
	for _, c := range items {
		if c.Type == wire.ComponentCode {
			code = c.Text
		}
	}
	if code != noBackendCommand {
		t.Fatalf("the row does not show the command that sets up a network drive: %+v", items)
	}
}

// F4: a running upload says what is being done; a finished one says how many moved, the space
// freed and how many in-use conversations stayed.
func TestUploadProgressAndOutcomeInPlainWords(t *testing.T) {
	f := newUploadFleet(t)
	f.gate = make(chan struct{})
	res := f.act(t, ext.EncodeState(sessPage{Machine: "local"}), "cold-offload", map[string]string{"m": "local"})
	if !strings.Contains(res.Toast, "搬到网盘") || !strings.Contains(res.Toast, "核对无误才删") {
		t.Fatalf("toast: %s", res.Toast)
	}
	for _, m := range []string{"local", ""} {
		txt := texts(f.view(t, m))
		if strings.Contains(txt, "读取") || !strings.Contains(txt, "正在进行：「"+localMachineName+"」把一周没动的 AI 对话搬到网盘") {
			t.Fatalf("running upload (%q):\n%s", m, txt)
		}
	}
	close(f.gate)
	f.a.waitJobs()
	j := f.a.jobSnapshot()["local"]
	want := "搬完了：1 个一周没动的 AI 对话已经放进网盘，电脑上腾出 1.2 MiB；1 个还在用的对话留在电脑上没动。"
	if j.Err != "" || j.Note != want {
		t.Fatalf("outcome %q (err %q), want %q", j.Note, j.Err, want)
	}
	if strings.Contains(j.Note, "→") {
		t.Fatal("outcome shows the before → after free-space arithmetic")
	}
}

// F5: session state lives on AI 对话 only; the main page's fourth tab is 清理记录 and links there.
func TestMainPageDoesNotRepeatSessionState(t *testing.T) {
	a := newTestApp(t)
	a.runner = coldRunner(nil)
	ctx := context.Background()
	for _, tab := range []string{tabSessions, tabHistory} {
		res, err := a.action(ctx, &ext.UIRequest{UIParams: wire.UIParams{State: ext.EncodeState(page{Tab: tab}), Action: "tab", Values: map[string]string{"tab": tab}}})
		if err != nil {
			t.Fatal(err)
		}
		v := res.View
		if v.Tabs == nil || v.Tabs.Value != tabHistory || v.Tabs.Items[3].Title != "清理记录" || v.Tabs.Items[3].Badge != "0" {
			t.Fatalf("tab %s: %+v", tab, v.Tabs)
		}
		for _, s := range v.Sections {
			if s.Key == "cold-machines" || strings.Contains(s.Title, "会话") {
				t.Fatalf("tab %s draws session state: %q", tab, s.Title)
			}
		}
		link := rowsWithPrefix(v, "open-sessions")
		if len(link) != 1 || link[0].Action.Push == nil || link[0].Action.Push.UI != sessionsUI {
			t.Fatalf("tab %s: no link to %s: %+v", tab, sessionsTitle, link)
		}
	}
	// An app that does not push pages calls the action instead.
	res, err := a.action(ctx, &ext.UIRequest{UIParams: wire.UIParams{State: ext.EncodeState(page{Tab: tabHistory}), Action: "open-sessions"}})
	if err != nil || res.Push == nil || res.Push.UI != sessionsUI {
		t.Fatalf("open-sessions: %+v %v", res, err)
	}
}

// jargonRE is wording the pages no longer use: English levels and terms, and internal Chinese.
var jargonRE = regexp.MustCompile(`(?i)\bsafe\b|\breview\b|dry-run|水位|纳管|台账|根分区|挂载点|扫描模式|\bhub\b|冷会话|节点|备份后端|\bCLI\b|\bbackend\b|invalid page state`)

// visibleText is what a view shows people: titles, texts, stats, tables, fields, confirms;
// commands to copy and links are left out (they stay exact).
func visibleText(v *wire.View) string {
	var b strings.Builder
	add := func(s ...string) { b.WriteString(strings.Join(s, "\n") + "\n") }
	add(v.Title)
	if v.Tabs != nil {
		for _, t := range v.Tabs.Items {
			add(t.Title)
		}
	}
	if v.Filter != nil {
		add(v.Filter.Title)
		for _, c := range v.Filter.Choices {
			add(c.Title)
		}
	}
	if v.Search != nil {
		add(v.Search.Placeholder)
	}
	for _, s := range v.Sections {
		add(s.Title, s.Footer)
		for _, c := range s.Items {
			if c.Type == wire.ComponentCode {
				continue
			}
			add(c.Title, c.Subtitle, c.Detail, c.Text, c.Label, c.Placeholder, c.Caption)
			if c.Badge != nil {
				add(c.Badge.Text)
			}
			for _, bd := range c.Badges {
				add(bd.Text)
			}
			for _, f := range c.Facts {
				add(f.Label, f.Value)
			}
			for _, st := range c.Stats {
				add(st.Label, st.Value, st.Detail)
			}
			for _, col := range c.Columns {
				add(col.Title)
			}
			for _, r := range c.Rows {
				add(r...)
			}
			for _, se := range c.Series {
				add(se.Name)
				for _, pt := range se.Points {
					add(pt.Label, pt.Display)
				}
			}
			if c.Action != nil {
				add(c.Action.Confirm)
			}
			if c.Button != nil {
				add(c.Button.Title)
			}
			for _, ra := range c.Actions {
				add(ra.Title, ra.Action.Confirm)
			}
		}
	}
	return b.String()
}

// The pages a first-time user sees speak plain Chinese: no levels in English, no internal terms.
func TestPagesUsePlainWords(t *testing.T) {
	ctx := context.Background()
	f := newUploadFleet(t)
	a := f.a
	var views []*wire.View
	push := func(v *wire.View, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		views = append(views, v)
	}
	push(a.render(ctx, &ext.UIRequest{}))
	push(a.card(ctx, &ext.UIRequest{}))
	main := func(state, action string, values map[string]string) {
		t.Helper()
		res, err := a.action(ctx, &ext.UIRequest{UIParams: wire.UIParams{State: state, Action: action, Values: values}})
		if err != nil {
			t.Fatal(err)
		}
		views = append(views, res.View)
	}
	main("", "scan-all", nil)
	a.waitJobs()
	for _, tab := range []string{tabFleet, tabItems, tabPlan, tabHistory} {
		main(ext.EncodeState(page{Machine: "local"}), "tab", map[string]string{"tab": tab})
	}
	main("", "new-machine", nil)
	main("", "edit-machine", map[string]string{"m": "build-box"})
	push(a.card(ctx, &ext.UIRequest{}))
	for _, m := range []string{"", "local", "build-box", "nas-box"} {
		views = append(views, f.view(t, m))
	}
	f.apply = "@@SESSIONS_RC 78\n@@SESSIONS_JSON {\"ok\":false,\"error\":{\"code\":78,\"message\":\"no backend configured\",\"name\":\"EX_CONFIG\"}}\n"
	f.act(t, ext.EncodeState(sessPage{Machine: "local"}), "cold-offload", map[string]string{"m": "local"})
	a.waitJobs()
	views = append(views, f.view(t, "local"))
	for i, v := range views {
		if m := jargonRE.FindAllString(visibleText(v), -1); len(m) > 0 {
			t.Errorf("view %d (%s) uses %q:\n%s", i, v.Title, m, visibleText(v))
		}
	}
	// An unreadable page state is reported in Chinese.
	_, err := a.render(ctx, &ext.UIRequest{UIParams: wire.UIParams{State: "%%%"}})
	if err == nil || !strings.Contains(err.Error(), "重新打开") {
		t.Fatalf("bad state: %v", err)
	}
}

// setJob puts a finished job of kind with error text errText on machine id.
func setJob(a *app, id, kind, errText string) {
	a.jobs.mu.Lock()
	defer a.jobs.mu.Unlock()
	if a.jobs.jobs == nil {
		a.jobs.jobs = map[string]*coldJob{}
		a.jobs.sem = make(chan struct{}, coldParallel)
	}
	a.jobs.jobs[id] = &coldJob{Kind: kind, Label: "x", Started: t0, Finished: t0, Err: errText}
}

// quoted returns the 「…」 names in s.
func quoted(s string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`「([^「」]+)」`).FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// A failed job's text names only a button its row really draws, and never the upload unless the
// upload failed: a restore, install, Homebrew or file job that failed does not point at
// 「重试：搬到网盘」, which deletes local copies.
func TestFailedJobRetryNamesItsOwnButton(t *testing.T) {
	f := newUploadFleet(t)
	const generic = "网盘连不上，修好后点「重试」。"
	cases := []struct {
		machine, kind string
		wantRetry     string // the button the text must name; "" none
		wantHow       string // a sentence the text must carry instead
	}{
		{"local", jobOffload, coldRetryText, ""},
		{"local", jobRestore, "", "点「拿回来」"},
		{"local", jobInstall, "重新检查 " + localMachineName, ""},
		{"local", jobBrew, "重新检查 " + localMachineName, ""},
		{"local", jobFileOffload, "", "磁盘占用图"},
		{"local", jobFileRestore, "", "磁盘占用图"},
		{"build-box", jobOffload, "重新检查 build-box", ""},
		{"build-box", jobRestore, "", "点「拿回来」"},
		{"build-box", jobInstall, "重新检查 build-box", ""},
		{"build-box", jobBrew, "重新检查 build-box", ""},
		{"nas-box", jobFileOffload, "", "磁盘占用图"},
	}
	for _, c := range cases {
		t.Run(c.machine+"/"+c.kind, func(t *testing.T) {
			setJob(f.a, c.machine, c.kind, generic)
			items := itemsOf(f.view(t, c.machine), c.machine)
			var outcome string
			buttons := map[string]bool{}
			for _, it := range items {
				if it.Type == wire.ComponentText && strings.Contains(it.Text, "没有成功") {
					outcome = it.Text
				}
				if it.Type == wire.ComponentButton {
					buttons[it.Title] = true
				}
			}
			if outcome == "" {
				t.Fatalf("no outcome line: %+v", items)
			}
			if c.kind != jobOffload && (strings.Contains(outcome, coldRetryText) || buttons[coldRetryText]) {
				t.Fatalf("a failed %s points at the upload: %q", c.kind, outcome)
			}
			if strings.Contains(outcome, "「重试」") {
				t.Fatalf("names a 重试 button the row does not have: %q", outcome)
			}
			if c.wantRetry != "" && (!strings.Contains(outcome, "「"+c.wantRetry+"」") || !buttons[c.wantRetry]) {
				t.Fatalf("want retry %q (drawn %v): %q", c.wantRetry, buttons, outcome)
			}
			if c.wantHow != "" && !strings.Contains(outcome, c.wantHow) {
				t.Fatalf("want %q: %q", c.wantHow, outcome)
			}
			for _, q := range quoted(outcome) {
				if strings.HasPrefix(q, "重试") || strings.HasPrefix(q, "重新检查") || strings.HasPrefix(q, "用 Homebrew") || strings.HasPrefix(q, "从 ") {
					if !buttons[q] {
						t.Fatalf("names button %q that the row does not draw (%v): %q", q, buttons, outcome)
					}
				}
			}
		})
	}
}

// 删掉放心删的缓存 says that old logs and temporary files are deleted too and do not come back.
func TestSafeCleanConfirmNamesLogs(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	if _, err := a.action(ctx, &ext.UIRequest{UIParams: wire.UIParams{Action: "scan-all"}}); err != nil {
		t.Fatal(err)
	}
	a.waitJobs()
	var confirms []string
	for _, tab := range []string{tabFleet, tabPlan} {
		res, err := a.action(ctx, &ext.UIRequest{UIParams: wire.UIParams{State: ext.EncodeState(page{Machine: "local"}), Action: "tab", Values: map[string]string{"tab": tab}}})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range res.View.Sections {
			for _, c := range s.Items {
				if c.Action != nil && c.Action.ID == "clean-safe" {
					confirms = append(confirms, c.Action.Confirm)
				}
			}
		}
	}
	if len(confirms) != 2 {
		t.Fatalf("clean-safe buttons: %d", len(confirms))
	}
	for _, c := range confirms {
		for _, w := range []string{"7 天以前的系统日志", "压缩归档的旧日志", "临时文件", "找不回来", "AI 对话都不"} {
			if !strings.Contains(c, w) {
				t.Fatalf("confirm lacks %q: %s", w, c)
			}
		}
	}
	if got := safeLogsNote(&Scan{Items: []ScanItem{{Category: CatGoCache, Safety: SafetySafe, Bytes: 1}}}); got != "" {
		t.Fatalf("no logs, yet: %q", got)
	}
}

// The MCP contract: machine_name 运行 Persona 的电脑 is accepted back as machine, the stored host
// name still finds that computer's sessions, and the tool description lists the location values
// the results carry.
func TestMCPAcceptsLocalLabelAndHostName(t *testing.T) {
	st, m := coldState(t)
	m.Name = "local (devbox-7)"
	if got := st.machine(localMachineName); got == nil || got.ID != m.ID {
		t.Fatalf("machine(%q) = %+v", localMachineName, got)
	}
	if hits := SearchCold(st, "checkout", localMachineName, 0, t0); len(hits) != 1 || hits[0].MachineName != localMachineName {
		t.Fatalf("search by label: %+v", hits)
	}
	if hits := SearchCold(st, "devbox-7 checkout", "", 0, t0); len(hits) != 1 {
		t.Fatalf("search by host name: %d hits", len(hits))
	}

	s := newStore(t.TempDir())
	s.homeDir = ""
	s.now = func() time.Time { return t0 }
	srv := &mcpServer{store: s, runner: coldRunner(nil)}
	ctx := context.Background()
	if _, err := srv.call(ctx, "disklessly_sessions_status", []byte(`{"refresh":true}`)); err != nil {
		t.Fatal(err)
	}
	out, err := srv.call(ctx, "disklessly_sessions_search", []byte(`{"query":"checkout","machine":"`+localMachineName+`"}`))
	if err != nil {
		t.Fatalf("machine_name sent back: %v", err)
	}
	if txt := fmt.Sprint(out); !strings.Contains(txt, "claude --resume "+uuidClaude) || !strings.Contains(txt, localMachineName) {
		t.Fatalf("search: %s", txt)
	}

	var desc string
	for _, tl := range tools {
		if tl.name == "disklessly_sessions_search" {
			desc = tl.description
		}
	}
	for _, w := range []string{LocCloud, LocUnknown, LocMissing, LocLocal, "不代表丢失"} {
		if !strings.Contains(desc, w) {
			t.Fatalf("sessions_search description lacks %q: %s", w, desc)
		}
	}
	for _, old := range []string{"在云端", "云端缺失", "位置「未知」"} {
		if strings.Contains(desc, old) {
			t.Fatalf("sessions_search description still lists %q", old)
		}
	}
}

// One spelling for the hub's own computer across the code's texts.
func TestOneNameForThisComputer(t *testing.T) {
	re := regexp.MustCompile(`运行 Persona 的(那台|这台)电脑|运行 Persona 的这台|本机节点`)
	for _, f := range []string{"ui.go", "ui_sessions.go", "ui_charts.go", "coldjobs.go", "coldstate.go", "problems.go", "mainjobs.go", "model.go", "offload_i18n.go", "scanner.go", "cleaner.go", "coldindex.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if m := re.FindAllString(string(b), -1); len(m) > 0 {
			t.Errorf("%s: %q", f, m)
		}
	}
	for _, loc := range []string{locEN, locJA} {
		for k, v := range offloadText[loc] {
			if strings.Contains(strings.ToLower(v), "cloud") || strings.Contains(v, "クラウド") {
				t.Errorf("%s %s mixes cloud with network drive: %q", loc, k, v)
			}
		}
	}
}
