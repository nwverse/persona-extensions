package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
)

// The hub program. It never talks to Orca: it hears new to-dos (event todo.changed, what "created"), keeps the
// ones the owner's filter takes, and asks the node program on the chosen Mac to hand each one to the chosen bot
// (nodes.call orcabot.handoff). From that program it hears when the bot's turn ended (node.event orcabot.finished) or
// waits on the owner (orcabot.needs_you), and then tells the owner (POST /api/v1/notify): the bot's answer stays in
// its Orca chat. It writes what the bot writes back through the MCP command (node.event orcabot.writeback) onto the
// to-do as an outcome decision (POST /api/v1/todos/{id}/decisions), after checking that the to-do was handed to that
// bot's chat.
//
// The to-do → chat mapping is persisted before anything is sent, so each to-do is handed over at most once across
// restarts. The event queue drops its oldest past 512 and does not redeliver, so a reconcile reads the to-do list
// at start and every few minutes. Only new to-dos are forwarded: made after the bot and the filter were set (armed),
// within the last two days, matching when they were made, at most a few an hour. Persona's own agent tasks and
// question to-dos are left alone unless the owner includes tasks. Write-backs are decisions, which send no
// todo.changed, and a to-do in the mapping is never sent again: a bot's write-back cannot start another hand-over.

const (
	reconcileEvery = 5 * time.Minute
	retryEvery     = time.Minute
	handoffTimeout = 90 * time.Second
	// maxAttempts bounds the refusals of a hand-over; Orca or the Mac being away does not count.
	maxAttempts = 8
	// maxAge: a to-do older than this is not forwarded, and a hand-over still waiting for the Mac then fails.
	maxAge = 48 * time.Hour
	// perPass bounds the hand-overs one reconcile starts; the hourly cap is the owner's setting.
	perPass           = 5
	defaultPerHour    = 10
	waitRetry         = 2 * time.Minute
	maxNotifyAttempts = 5
	// recordsKept bounds the persisted mapping; to-dos made before the newest one dropped are never forwarded.
	recordsKept = 2000
)

// botChoice is the bot the owner picked on the page, on the Mac it was listed by. Gone: Orca no longer has it, and
// forwarding pauses until the owner picks one again.
type botChoice struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Machine  string    `json:"machine"`
	ChosenAt time.Time `json:"chosen_at"`
	Gone     bool      `json:"gone,omitempty"`
}

type record struct {
	TodoID    string `json:"todo_id"`
	Title     string `json:"title"`
	Machine   string `json:"machine"`
	BotID     string `json:"bot_id"`
	BotName   string `json:"bot_name"`
	SessionID string `json:"session_id,omitempty"`
	OpID      string `json:"op_id"`
	// State: pending (not handed over yet: Text and Todo are kept so a retry sends the same), sent, finished, failed.
	State      string        `json:"state"`
	Text       string        `json:"text,omitempty"`
	Todo       *todoSnapshot `json:"todo,omitempty"`
	Attempts   int           `json:"attempts,omitempty"`
	LastError  string        `json:"last_error,omitempty"`
	LastCode   string        `json:"last_code,omitempty"`
	NextTry    time.Time     `json:"next_try,omitzero"`
	CreatedAt  time.Time     `json:"created_at"`
	TodoMadeAt time.Time     `json:"todo_made_at,omitzero"`
	SentAt     time.Time     `json:"sent_at,omitzero"`
	FinishedAt time.Time     `json:"finished_at,omitzero"`
	TurnState  string        `json:"turn_state,omitempty"`
	Outcome    string        `json:"outcome,omitempty"`
	Notified   bool          `json:"notified,omitempty"`
	// NotifyAttempts counts failed notifications of the finish; NotifyNote says what became of it when not plain.
	NotifyAttempts int      `json:"notify_attempts,omitempty"`
	NotifyNote     string   `json:"notify_note,omitempty"`
	NeedsYou       int      `json:"needs_you,omitempty"` // the last wait the owner was told of
	FailNotified   bool     `json:"fail_notified,omitempty"`
	Refs           []string `json:"refs,omitempty"`
}

type hubState struct {
	Bot      *botChoice         `json:"bot,omitempty"`
	ArmedAt  time.Time          `json:"armed_at,omitzero"`
	ArmedKey string             `json:"armed_key,omitempty"`
	FloorAt  time.Time          `json:"floor_at,omitzero"`
	Records  map[string]*record `json:"records"`
	// Passed are new to-dos seen when they were made that the filter did not take: they are not forwarded later,
	// even if edited to match. Kept for maxAge.
	Passed map[string]time.Time `json:"passed,omitempty"`
}

type hubApp struct {
	now            func() time.Time
	reconcileEvery time.Duration

	sendMu sync.Mutex // hand-overs one at a time

	mu      sync.Mutex
	cfg     config
	data    string
	hub     *ext.Hub
	api     hubAPI
	nodes   nodeCaller
	scopes  []string
	st      hubState
	created chan string
	lastErr string // the last failure of reading the to-dos or writing back, for the page
	started bool
}

func newHubApp(api hubAPI, nodes nodeCaller) *hubApp {
	return &hubApp{now: time.Now, reconcileEvery: reconcileEvery, api: api, nodes: nodes,
		created: make(chan string, 256), st: hubState{Records: map[string]*record{}}}
}

func (a *hubApp) program() ext.Program {
	return ext.Program{
		ID: about.ID, Version: about.Version, Manifest: manifest,
		Init:       a.init,
		UI:         map[string]ext.Page{uiBot: a.page()},
		Namespaces: map[string]ext.Handler{"event": a.event, "node": a.nodeEvent},
	}
}

func (a *hubApp) init(ctx context.Context, in *ext.Init, hub *ext.Hub) (map[string]any, error) {
	var c config
	if err := in.Config(&c); err != nil {
		return nil, err
	}
	data := strings.TrimSpace(in.Info().DataDir)
	if data == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		data = filepath.Join(dir, "persona-orca-bot-hub")
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.cfg, a.data, a.hub, a.scopes = c, data, hub, in.Hub.Scopes
	if a.api == nil {
		a.api = newHubClient(in.Hub.URL, in.Hub.Token)
	}
	if a.nodes == nil && hub != nil {
		a.nodes = rpcNodes{hub: hub}
	}
	a.load()
	a.rearm()
	a.settle()
	a.save()
	start := !a.started
	a.started = true
	a.mu.Unlock()
	if start {
		go a.run(ctx)
	}
	return nil, nil
}

func (a *hubApp) statePath() string { return filepath.Join(a.data, "state.json") }

// load reads the state; the caller holds a.mu. A damaged state is set aside and the program starts armed anew, so
// nothing made before is forwarded (a lost mapping must not send twice).
func (a *hubApp) load() {
	var st hubState
	if err := readJSONFile(a.statePath(), &st); err != nil {
		log.Printf("orca-bot: the state was damaged and is kept as state.json.bad; forwarding starts anew")
		st = hubState{}
	}
	if st.Records == nil {
		st.Records = map[string]*record{}
	}
	a.st = st
}

// save writes the state; the caller holds a.mu.
func (a *hubApp) save() {
	if len(a.st.Records) > recordsKept {
		var old []*record
		for _, r := range a.st.Records {
			if r.State == "finished" || r.State == "failed" {
				old = append(old, r)
			}
		}
		sort.Slice(old, func(i, j int) bool { return old[i].CreatedAt.Before(old[j].CreatedAt) })
		for _, r := range old {
			if len(a.st.Records) <= recordsKept {
				break
			}
			delete(a.st.Records, r.TodoID)
			// A to-do made no later than one whose record is gone could otherwise be forwarded again.
			if made := r.TodoMadeAt; made.After(a.st.FloorAt) {
				a.st.FloorAt = made
			}
			if r.CreatedAt.After(a.st.FloorAt) {
				a.st.FloorAt = r.CreatedAt
			}
		}
	}
	for id, at := range a.st.Passed {
		if a.now().Sub(at) > maxAge {
			delete(a.st.Passed, id)
		}
	}
	if a.data == "" {
		return
	}
	if err := writeJSONFile(a.statePath(), a.st); err != nil {
		log.Printf("orca-bot: cannot save the state: %v", err)
	}
}

// ready says whether to-dos are forwarded now, and if not, what is missing; the caller holds a.mu.
func (a *hubApp) ready() (bool, string) {
	switch {
	case a.cfg.machine() == "":
		return false, "还没选运行 Orca 的 Mac"
	case a.st.Bot == nil || a.st.Bot.Machine != a.cfg.machine():
		return false, "还没选机器人"
	case a.st.Bot.Gone:
		return false, "选的机器人在 Orca 里找不到了，已暂停转发：请重新选一个"
	case !a.cfg.filterSet():
		return false, "还没设置转发哪些待办（项目或关键词）"
	}
	return true, ""
}

// rearm starts forwarding anew when the Mac, the bot or the filter changed: only to-dos made from then on are
// forwarded. The caller holds a.mu.
func (a *hubApp) rearm() {
	if ok, _ := a.ready(); !ok {
		if a.st.ArmedKey != "" {
			a.st.ArmedKey, a.st.ArmedAt = "", time.Time{}
		}
		return
	}
	key := a.cfg.machine() + "\x00" + a.st.Bot.ID + "\x00" + a.cfg.filterKey()
	if key != a.st.ArmedKey || a.st.ArmedAt.IsZero() {
		a.st.ArmedKey, a.st.ArmedAt = key, a.now()
	}
}

// settle fails the hand-overs still waiting that would now go where the owner no longer forwards: forwarding stopped,
// or another Mac or bot chosen. The caller holds a.mu.
func (a *hubApp) settle() {
	ok, missing := a.ready()
	for _, r := range a.st.Records {
		if r.State != "pending" || r.SessionID != "" {
			continue
		}
		why := ""
		switch {
		case !ok:
			why = "转发已停止（" + missing + "），这条没有交出去"
		case r.Machine != a.cfg.machine() || r.BotID != a.st.Bot.ID:
			why = "换了 Mac 或机器人，这条没有交给原来的机器人"
		}
		if why != "" {
			r.State, r.LastError, r.LastCode, r.Text, r.Todo = "failed", why, "", "", nil
		}
	}
}

func (a *hubApp) run(ctx context.Context) {
	t := time.NewTicker(a.reconcileEvery)
	defer t.Stop()
	retry := time.NewTicker(retryEvery)
	defer retry.Stop()
	a.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-a.created:
			a.consider(ctx, id, nil)
		case <-retry.C:
			a.retryDue(ctx, false)
		case <-t.C:
			a.reconcile(ctx)
		}
	}
}

// event takes the hub's events: a new to-do is looked at; every other change is not (and a to-do already handed
// over is never handed over again).
func (a *hubApp) event(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method != "todo.changed" {
		return nil, nil
	}
	var ev struct {
		ID   string `json:"id"`
		What string `json:"what"`
	}
	if err := json.Unmarshal(params, &ev); err != nil || ev.What != "created" || !safeID.MatchString(ev.ID) {
		return nil, nil
	}
	select {
	case a.created <- ev.ID:
	default: // the reconcile catches it
	}
	return nil, nil
}

// reconcile reads the to-do list and considers each to-do, oldest first, starting at most perPass hand-overs; it
// also retries the hand-overs that are due.
func (a *hubApp) reconcile(ctx context.Context) {
	a.mu.Lock()
	ok, _ := a.ready()
	api := a.api
	a.mu.Unlock()
	if !ok || api == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	rows, err := api.Todos(cctx)
	cancel()
	a.mu.Lock()
	if err != nil {
		a.lastErr = "读取待办列表失败：" + clip(err.Error(), 200)
		a.mu.Unlock()
		return
	}
	if strings.HasPrefix(a.lastErr, "读取待办列表失败") {
		a.lastErr = ""
	}
	a.mu.Unlock()
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].created().Before(rows[j].created()) })
	started := 0
	for i := range rows {
		if ctx.Err() != nil || started >= perPass {
			break
		}
		if a.consider(ctx, rows[i].ID, &rows[i]) {
			started++
		}
	}
	a.retryDue(ctx, false)
}

// handedLastHour counts the hand-overs begun in the last hour; the caller holds a.mu.
func (a *hubApp) handedLastHour() int {
	k := 0
	for _, r := range a.st.Records {
		if a.now().Sub(r.CreatedAt) < time.Hour {
			k++
		}
	}
	return k
}

// skippedKind: a to-do that is not the owner's own open to-do: one of Persona's agent tasks (unless included), a
// question the assistant asked, a concern, a done one. The caller holds a.mu.
func (a *hubApp) skippedKind(row *todoRow) bool {
	return strings.HasPrefix(row.ID, "todo_ask_") || row.Kind == "concern" || strings.TrimSpace(row.DoneAt) != "" ||
		(strings.TrimSpace(row.Origin) != "" && !a.cfg.IncludeTasks)
}

// consider looks at one to-do (row nil: read it first, it came from an event) and hands it over when it is new,
// matched the filter when it was made and was never handed over; true when it began a hand-over.
func (a *hubApp) consider(ctx context.Context, id string, row *todoRow) bool {
	if !safeID.MatchString(id) || strings.HasPrefix(id, "todo_ask_") {
		return false
	}
	a.mu.Lock()
	ok, _ := a.ready()
	_, known := a.st.Records[id]
	_, passed := a.st.Passed[id]
	api := a.api
	a.mu.Unlock()
	if !ok || known || passed || api == nil {
		return false
	}
	fromEvent := row == nil
	if fromEvent {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		r, err := api.Todo(cctx, id)
		cancel()
		if err != nil || r == nil {
			return false // gone, not shown to this extension, or the hub busy: the reconcile looks again
		}
		row = r
	}
	if row.ID != id {
		return false
	}

	a.mu.Lock()
	if ok, _ := a.ready(); !ok {
		a.mu.Unlock()
		return false
	}
	if _, known := a.st.Records[id]; known {
		a.mu.Unlock()
		return false
	}
	now := a.now()
	made := row.created()
	skip := false
	switch {
	case made.IsZero() && !fromEvent: // when it was made is unknown: only an event says it is new
		skip = true
	case !made.IsZero() && (made.Before(a.st.ArmedAt) || now.Sub(made) > maxAge):
		skip = true
	case !made.IsZero() && !a.st.FloorAt.IsZero() && !made.After(a.st.FloorAt):
		skip = true
	case a.skippedKind(row) || !a.cfg.matches(*row):
		skip = true
		if fromEvent { // seen as it was made, and not taken: an edit later does not make it new
			if a.st.Passed == nil {
				a.st.Passed = map[string]time.Time{}
			}
			a.st.Passed[id] = now
			a.save()
		}
	case a.handedLastHour() >= a.cfg.perHour():
		skip = true // over the hourly cap: a later reconcile takes it, while it is still new
	}
	if skip {
		a.mu.Unlock()
		return false
	}
	snap := row.snapshot()
	bot := *a.st.Bot
	rec := &record{TodoID: id, Title: clip(snap.Title, 200), Machine: a.cfg.machine(), BotID: bot.ID, BotName: bot.Name,
		OpID: orcaOperationID(now, "orca-bot:"+id+":"+bot.ID), State: "pending", Text: buildRequest(snap), Todo: &snap,
		CreatedAt: now, TodoMadeAt: made}
	a.st.Records[id] = rec
	a.save() // before anything is sent: a restart finds it and never starts a second hand-over
	a.mu.Unlock()
	a.attempt(ctx, id)
	return true
}

// retryDue retries the pending hand-overs whose time has come; those waiting for the Mac or Orca at once when back is
// set (the Mac said it started).
func (a *hubApp) retryDue(ctx context.Context, back bool) {
	a.mu.Lock()
	if ok, _ := a.ready(); !ok {
		a.settle()
		a.save()
		a.mu.Unlock()
		return
	}
	var due []string
	now := a.now()
	for id, r := range a.st.Records {
		if r.State == "pending" && (!now.Before(r.NextTry) || back && waiting(r.LastCode)) {
			due = append(due, id)
		}
	}
	a.mu.Unlock()
	sort.Strings(due)
	for _, id := range due {
		if ctx.Err() != nil {
			return
		}
		a.attempt(ctx, id)
	}
}

func retryAfter(attempts int) time.Duration {
	d := time.Minute << min(attempts-1, 7) // 1m, 2m, 4m … about 2h
	return min(d, 2*time.Hour)
}

// waiting: a failure that is the Mac, Orca or the bot being away for now, not a refusal; it does not use up attempts.
func waiting(code string) bool {
	switch code {
	case "offline", "orca_not_running", "orca_timeout", "bot_paused", "orca_busy", "timeout":
		return true
	}
	return false
}

// attempt asks the node program to hand one pending to-do over. The same operation id and text every time: should
// an earlier attempt have reached Orca, Orca answers it again instead of sending twice.
func (a *hubApp) attempt(ctx context.Context, id string) {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	a.settle()
	r := a.st.Records[id]
	if r == nil || r.State != "pending" || r.Todo == nil {
		a.save()
		a.mu.Unlock()
		return
	}
	nodes := a.nodes
	params := handoffParams{TodoID: r.TodoID, BotID: r.BotID, OpID: r.OpID, Text: r.Text, Todo: *r.Todo}
	machine := r.Machine
	a.mu.Unlock()
	if nodes == nil {
		return
	}

	cctx, cancel := context.WithTimeout(ctx, handoffTimeout)
	var res handoffResult
	err := nodes.Call(cctx, machine, "orcabot.handoff", params, &res)
	cancel()

	a.mu.Lock()
	r = a.st.Records[id]
	if r == nil || r.State != "pending" {
		a.mu.Unlock()
		return
	}
	if err == nil && safeID.MatchString(res.SessionID) {
		r.State, r.SessionID, r.SentAt, r.LastError, r.LastCode, r.Text, r.Todo = "sent", res.SessionID, a.now(), "", "", "", nil
		a.save()
		a.invalidate()
		a.mu.Unlock()
		return
	}
	if err == nil {
		err = errors.New("the node program answered without a chat")
	}
	code := errCode(err)
	r.LastError, r.LastCode = failureWords(err), code
	now := a.now()
	var tell *notice
	switch {
	case code == "bot_not_found":
		r.State, r.Text, r.Todo = "failed", "", nil
		if a.st.Bot != nil && a.st.Bot.ID == r.BotID {
			a.st.Bot.Gone = true // pause until the owner picks again: every other hand-over would fail the same way
			a.rearm()
			a.settle()
		}
	case waiting(code):
		if now.Sub(r.CreatedAt) > maxAge {
			r.State, r.Text, r.Todo = "failed", "", nil
			tell = failNotice(r)
		} else {
			r.NextTry = now.Add(waitRetry)
		}
	default:
		r.Attempts++
		if r.Attempts >= maxAttempts {
			r.State, r.Text, r.Todo = "failed", "", nil
			tell = failNotice(r)
		} else {
			r.NextTry = now.Add(retryAfter(r.Attempts))
		}
	}
	if tell != nil {
		r.FailNotified = true
	}
	a.save()
	a.invalidate()
	api, canNotify := a.api, a.canNotify()
	a.mu.Unlock()
	if tell != nil && canNotify && api != nil {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, _ = api.Notify(cctx, *tell)
		cancel()
	}
}

func failNotice(r *record) *notice {
	return &notice{Title: "待办没交给 Orca 机器人", ID: "orca-bot-" + shortHash(16, r.TodoID), PushLevel: "P1",
		Body: fmt.Sprintf("待办「%s」没能交给机器人「%s」：%s。", clip(r.Title, 80), clip(orName(r.BotName), 40), r.LastError)}
}

func orName(name string) string {
	if name == "" {
		return "Orca 机器人"
	}
	return name
}

var knownCodes = []string{"bot_not_found", "bot_paused", "bots_disabled", "orca_busy", "orca_not_running",
	"orca_incompatible", "orca_timeout", "orca_error", "offline"}

// errCode is the code of a failure of nodes.call: the error's own code, or one named in its words (the hub may pass
// on only those); "timeout" when the Mac did not answer in time.
func errCode(err error) string {
	var e *ext.Error
	if errors.As(err, &e) && knownCode(e.Code) {
		return e.Code
	}
	msg := err.Error()
	for _, c := range knownCodes {
		if strings.Contains(msg, c) {
			return c
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout") {
		return "timeout"
	}
	return ""
}

func knownCode(c string) bool { return slices.Contains(knownCodes, c) }

// failureWords says in the owner's words why a call to the node program failed; never the message text.
func failureWords(err error) string {
	switch errCode(err) {
	case "bot_not_found":
		return "Orca 里找不到这个机器人了：请在本页重新选一个"
	case "bot_paused":
		return "机器人在 Orca 里暂停了，恢复后会再交"
	case "bots_disabled":
		return "Orca 里的机器人功能关着"
	case "orca_busy":
		return "Orca 给了一个已经用过的对话，稍后再交"
	case "orca_not_running":
		return "那台 Mac 上的 Orca 没在运行"
	case "orca_incompatible":
		return "那台 Mac 上 Orca 的版本和本扩展不兼容"
	case "orca_timeout":
		return "Orca 没有及时回应"
	case "offline":
		return "那台 Mac 离线了"
	case "timeout":
		return "那台 Mac 没有及时回应"
	}
	return "交给机器人失败：" + clip(err.Error(), 160)
}

// canNotify: the token may send notifications (no scope list: an older hub, try); the caller holds a.mu.
func (a *hubApp) canNotify() bool {
	return len(a.scopes) == 0 || slices.Contains(a.scopes, "notify")
}

func (a *hubApp) invalidate() {
	if a.hub != nil {
		_ = a.hub.Invalidate(uiBot)
	}
}

// nodeEvent takes what the node program tells: it started, a bot's turn ended or waits on the owner, a write-back.
func (a *hubApp) nodeEvent(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var ev struct {
		MachineID string          `json:"machine_id"`
		Event     string          `json:"event"`
		Params    json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(params, &ev); err != nil {
		return nil, err
	}
	a.mu.Lock()
	machine := a.cfg.machine()
	a.mu.Unlock()
	if ev.MachineID == "" || ev.MachineID != machine {
		return map[string]any{}, nil // only the Mac in the settings speaks for the bot
	}
	switch ev.Event {
	case "orcabot.hello": // the Mac is back: what waited for it goes now (event handlers run on their own)
		a.retryDue(ctx, true)
	case "orcabot.finished":
		var f finishedEvent
		if err := json.Unmarshal(ev.Params, &f); err == nil {
			a.finished(ctx, machine, f)
		}
	case "orcabot.needs_you":
		var n needsYouEvent
		if err := json.Unmarshal(ev.Params, &n); err == nil {
			a.needsYou(ctx, machine, n)
		}
	case "orcabot.writeback":
		var w writebackEvent
		if err := json.Unmarshal(ev.Params, &w); err == nil {
			a.writeback(ctx, machine, w)
		}
	}
	return map[string]any{}, nil
}

// handedTo is the record of a to-do handed to that bot's chat on the Mac in the settings; the caller holds a.mu.
func (a *hubApp) handedTo(machine, todoID, botID, sessionID string) *record {
	r := a.st.Records[todoID]
	if r == nil || r.BotID != botID || sessionID == "" || r.Machine != machine || r.SessionID != sessionID {
		return nil
	}
	return r
}

// finished tells the owner once that the bot is done with a to-do, then lets the node program stop telling.
func (a *hubApp) finished(ctx context.Context, machine string, f finishedEvent) {
	a.mu.Lock()
	r := a.st.Records[f.TodoID]
	if r != nil && r.BotID == f.BotID && r.Machine == machine && f.SessionID != "" && r.SessionID == "" &&
		(r.State == "pending" || r.State == "failed") {
		// Handed over, but the answer never came back here.
		r.State, r.SessionID, r.SentAt, r.Text, r.Todo, r.LastError = "sent", f.SessionID, a.now(), "", nil, ""
	}
	r = a.handedTo(machine, f.TodoID, f.BotID, f.SessionID)
	if r == nil || r.Notified {
		a.mu.Unlock()
		a.ack(ctx, machine, f.TodoID) // nothing (more) to tell of it: stop telling
		return
	}
	n := finishNotice(r, f.State, f.Outcome)
	api, canNotify := a.api, a.canNotify()
	a.mu.Unlock()

	note := ""
	if canNotify && api != nil {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		d, err := api.Notify(cctx, n)
		cancel()
		switch {
		case err != nil:
			a.mu.Lock()
			a.lastErr = "发送通知失败：" + clip(err.Error(), 200)
			r.NotifyAttempts++
			give := r.NotifyAttempts >= maxNotifyAttempts
			a.save()
			a.mu.Unlock()
			if !give {
				return // not acknowledged: the node program tells again in a minute
			}
			note = "通知没能发出去"
		case d.Held:
			note = "通知被 Persona 的推送分级留下了（" + clip(d.Reason, 40) + "）"
		case !d.Sent && d.Reason != "":
			note = "通知没有送到设备（" + clip(d.Reason, 40) + "）"
		}
	} else {
		note = "没有发通知的权限"
	}
	a.mu.Lock()
	r.State, r.FinishedAt, r.TurnState, r.Outcome, r.Notified, r.NotifyNote = "finished", a.now(), f.State, f.Outcome, true, note
	a.save()
	a.invalidate()
	a.mu.Unlock()
	a.ack(ctx, machine, f.TodoID)
}

func (a *hubApp) ack(ctx context.Context, machine, todoID string) {
	a.mu.Lock()
	nodes := a.nodes
	a.mu.Unlock()
	if nodes == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_ = nodes.Call(cctx, machine, "orcabot.finished_ack", map[string]any{"todo_id": todoID}, nil)
}

// needsYou tells the owner once per wait that the bot waits on an approval or a question; following goes on.
func (a *hubApp) needsYou(ctx context.Context, machine string, ev needsYouEvent) {
	a.mu.Lock()
	r := a.handedTo(machine, ev.TodoID, ev.BotID, ev.SessionID)
	if r == nil || r.State != "sent" || ev.Episode <= r.NeedsYou {
		a.mu.Unlock()
		return
	}
	n := notice{Title: "Orca 机器人在等你", ID: "orca-bot-ask-" + shortHash(16, r.TodoID), PushLevel: "P1",
		Body: fmt.Sprintf("机器人「%s」处理待办「%s」时在等你批准或回答，请到它的 Orca 对话里看看。", clip(orName(r.BotName), 40), clip(r.Title, 80))}
	api, canNotify := a.api, a.canNotify()
	a.mu.Unlock()
	if canNotify && api != nil {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := api.Notify(cctx, n)
		cancel()
		if err != nil {
			return // told again while the wait lasts
		}
	}
	a.mu.Lock()
	if ev.Episode > r.NeedsYou {
		r.NeedsYou = ev.Episode
		a.save()
	}
	a.mu.Unlock()
}

// finishNotice is the notification for a finished turn: which bot, which to-do, and where the answer is.
func finishNotice(r *record, state, outcome string) notice {
	bot, title := clip(orName(r.BotName), 40), clip(r.Title, 80)
	n := notice{Title: "Orca 机器人做完了准备", ID: "orca-bot-" + shortHash(16, r.TodoID), PushLevel: "P1",
		Body: fmt.Sprintf("机器人「%s」已经准备好待办「%s」，回答在它的 Orca 对话里。", bot, title)}
	switch {
	case state == "completed" && outcome == "failure":
		n.Title = "Orca 机器人出错了"
		n.Body = fmt.Sprintf("机器人「%s」处理待办「%s」的这一轮出错结束了，详情在它的 Orca 对话里。", bot, title)
	case state == "interrupted" || state == "completed" && (outcome == "cancellation" || outcome == "superseded"):
		n.Title = "Orca 机器人中断了"
		n.Body = fmt.Sprintf("机器人「%s」处理待办「%s」的这一轮被中断了，详情在它的 Orca 对话里。", bot, title)
	case state == "unverifiable":
		n.Title = "Orca 机器人的这一轮结束了"
		n.Body = fmt.Sprintf("机器人「%s」处理待办「%s」的这一轮结束了，但 Orca 无法确认它是否正常做完，请到它的 Orca 对话里看看。", bot, title)
	case state == "unavailable":
		n.Title = "找不到 Orca 机器人的对话"
		n.Body = fmt.Sprintf("交给机器人「%s」的待办「%s」：它在 Orca 里的对话找不到了，不再跟进。", bot, title)
	case state == "timeout":
		n.Title = "Orca 机器人还没做完"
		n.Body = fmt.Sprintf("机器人「%s」处理待办「%s」超过 24 小时还没做完，不再跟进；进度在它的 Orca 对话里。", bot, title)
	}
	return n
}

type writebackEvent struct {
	TodoID    string `json:"todo_id"`
	SessionID string `json:"session_id"`
	BotID     string `json:"bot_id"`
	Ref       string `json:"ref"`
	Text      string `json:"text"`
}

// writeback writes a bot's preparation onto the to-do, after checking again that the to-do was handed to that
// bot's chat, then tells the node program what became of it.
func (a *hubApp) writeback(ctx context.Context, machine string, w writebackEvent) {
	result := func(ack writeAck) {
		a.mu.Lock()
		nodes := a.nodes
		a.mu.Unlock()
		if nodes == nil {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_ = nodes.Call(cctx, machine, "orcabot.writeback_result",
			map[string]any{"todo_id": w.TodoID, "ref": w.Ref, "ok": ack.OK, "inserted": ack.Inserted, "error": ack.Error}, nil)
	}
	if !strings.HasPrefix(w.Ref, "orca-bot:") || len(w.Ref) > 120 || strings.Contains(w.Ref, "#") {
		result(writeAck{Error: "写回的编号不对"})
		return
	}
	text := strings.TrimSpace(cleanText(w.Text))
	if text == "" || utf8.RuneCountInString(text) > writeMaxRunes {
		result(writeAck{Error: fmt.Sprintf("内容为空或超过 %d 字", writeMaxRunes)})
		return
	}
	a.mu.Lock()
	r := a.handedTo(machine, w.TodoID, w.BotID, w.SessionID)
	if r == nil || (r.State != "sent" && r.State != "finished") {
		a.mu.Unlock()
		result(writeAck{Error: "这条待办不是交给这个机器人的这个对话的"})
		return
	}
	dup := slices.Contains(r.Refs, w.Ref)
	if !dup && len(r.Refs) >= writesPerTodo {
		a.mu.Unlock()
		result(writeAck{Error: fmt.Sprintf("这条待办已经写回了 %d 次，到上限了", writesPerTodo)})
		return
	}
	bot := orName(r.BotName)
	api := a.api
	a.mu.Unlock()
	if api == nil {
		result(writeAck{Error: "hub 的接口还没准备好"})
		return
	}
	d := decisionPayload{Question: fmt.Sprintf("Orca 机器人「%s」的准备", clip(bot, 40)), Choice: text,
		Reason: "由 Orca 机器人通过 orca-bot 扩展写回；详情在它的 Orca 对话里", Ref: w.Ref, Source: "outcome"}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	inserted, err := api.AddDecision(cctx, w.TodoID, d)
	cancel()
	if err != nil {
		a.mu.Lock()
		a.lastErr = "写回待办失败：" + clip(err.Error(), 200)
		a.mu.Unlock()
		result(writeAck{Error: "写回待办失败：" + clip(err.Error(), 200)})
		return
	}
	a.mu.Lock()
	if r := a.st.Records[w.TodoID]; r != nil && !slices.Contains(r.Refs, w.Ref) {
		r.Refs = append(r.Refs, w.Ref)
		a.save()
	}
	a.invalidate()
	a.mu.Unlock()
	result(writeAck{OK: true, Inserted: inserted})
}
