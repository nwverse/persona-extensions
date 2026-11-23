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
	// expiryWindow: at most one "waited too long" notification in it, with the count since the last one.
	expiryWindow = 6 * time.Hour
	// abandonPerPass bounds the Macs told of given-up hand-overs in one pass; abandonKept is how long one is kept.
	abandonPerPass = 10
	abandonKept    = 72 * time.Hour
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
	NotifyAttempts int    `json:"notify_attempts,omitempty"`
	NotifyNote     string `json:"notify_note,omitempty"`
	NeedsYou       int    `json:"needs_you,omitempty"` // the last wait the owner was told of
	FailNotified   bool   `json:"fail_notified,omitempty"`
	// NotFoundAt: when Orca first said the bot is gone for this hand-over; it must say so again waitRetry later.
	NotFoundAt time.Time `json:"not_found_at,omitzero"`
	Refs       []string  `json:"refs,omitempty"`
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
	// Abandoned are the hand-overs given up here that the Mac has not been told of yet: it lets their chat go when
	// nothing was sent. Dropped after abandonKept (the Mac forgets never-sent hand-overs by then anyway).
	Abandoned map[string]abandonEntry `json:"abandoned,omitempty"`
	// Expired is the digest of hand-overs that waited past maxAge since the last notification of them.
	Expired expiryDigest `json:"expired,omitzero"`
}

type abandonEntry struct {
	Machine string    `json:"machine"`
	At      time.Time `json:"at"`
}

type expiryDigest struct {
	Count    int       `json:"count,omitempty"`
	Titles   []string  `json:"titles,omitempty"` // the first few
	Bot      string    `json:"bot,omitempty"`
	NoticeAt time.Time `json:"notice_at,omitzero"` // the last notification of them
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
	// writing are the write-backs on their way to the hub's API, by to-do: they count toward the limit.
	writing map[string]map[string]bool
}

func newHubApp(api hubAPI, nodes nodeCaller) *hubApp {
	return &hubApp{now: time.Now, reconcileEvery: reconcileEvery, api: api, nodes: nodes,
		created: make(chan string, 256), writing: map[string]map[string]bool{}, st: hubState{Records: map[string]*record{}}}
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
	for id, e := range a.st.Abandoned {
		if a.now().Sub(e.At) > abandonKept {
			delete(a.st.Abandoned, id)
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
			a.failRecord(r, why)
		}
	}
}

// failRecord gives a hand-over up here, and notes that the Mac is to be told (abandon). The caller holds a.mu.
func (a *hubApp) failRecord(r *record, why string) {
	r.State, r.LastError, r.Text, r.Todo = "failed", why, "", nil
	if r.SessionID == "" && r.Machine != "" { // never sent, as far as the hub knows
		if a.st.Abandoned == nil {
			a.st.Abandoned = map[string]abandonEntry{}
		}
		a.st.Abandoned[r.TodoID] = abandonEntry{Machine: r.Machine, At: a.now()}
	}
}

// tellAbandoned tells the Mac of the hand-overs given up here, oldest first, at most abandonPerPass in a pass, and
// none more to a Mac that did not answer; those not told are told next time.
func (a *hubApp) tellAbandoned(ctx context.Context) {
	type entry struct {
		id string
		abandonEntry
	}
	a.mu.Lock()
	nodes := a.nodes
	todo := make([]entry, 0, len(a.st.Abandoned))
	for id, e := range a.st.Abandoned {
		todo = append(todo, entry{id, e})
	}
	a.mu.Unlock()
	if nodes == nil {
		return
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].At.Before(todo[j].At) })
	down := map[string]bool{}
	for i, e := range todo {
		if i >= abandonPerPass || ctx.Err() != nil {
			break
		}
		if down[e.Machine] {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := nodes.Call(cctx, e.Machine, "orcabot.abandon", map[string]any{"todo_id": e.id}, nil)
		cancel()
		if err != nil {
			down[e.Machine] = true
			continue
		}
		a.mu.Lock()
		delete(a.st.Abandoned, e.id)
		a.save()
		a.mu.Unlock()
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

// sentLastHour counts the hand-overs that reached the bot in the last hour, and says when the oldest of them leaves
// that hour; the caller holds a.mu.
func (a *hubApp) sentLastHour() (int, time.Time) {
	k, free := 0, time.Time{}
	now := a.now()
	for _, r := range a.st.Records {
		if !r.SentAt.IsZero() && now.Sub(r.SentAt) < time.Hour {
			k++
			if at := r.SentAt.Add(time.Hour); free.IsZero() || at.Before(free) {
				free = at
			}
		}
	}
	return k, free
}

// capped: the hourly cap is reached; the caller holds a.mu.
func (a *hubApp) capped() (bool, time.Time) {
	k, free := a.sentLastHour()
	return k >= a.cfg.perHour(), free
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
	case func() bool { c, _ := a.capped(); return c }():
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

// retryDue retries the pending hand-overs whose time has come, oldest first, at most perPass of them and never past
// the hourly cap; those waiting for the Mac or Orca at once when back is set (the Mac said it started). Hand-overs
// that waited past maxAge fail together, with one notification for the pass; the Mac is told of what was given up.
func (a *hubApp) retryDue(ctx context.Context, back bool) {
	a.mu.Lock()
	if ok, _ := a.ready(); !ok {
		a.settle()
		a.save()
		a.mu.Unlock()
		a.tellAbandoned(ctx)
		return
	}
	a.expire()
	var due []*record
	now := a.now()
	for _, r := range a.st.Records {
		if r.State == "pending" && (!now.Before(r.NextTry) || back && waiting(r.LastCode)) {
			due = append(due, r)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].CreatedAt.Before(due[j].CreatedAt) })
	ids := make([]string, 0, len(due))
	for _, r := range due {
		ids = append(ids, r.TodoID)
	}
	tell := a.expiredNotice()
	api, canNotify := a.api, a.canNotify()
	a.save()
	a.mu.Unlock()
	if tell != nil && canNotify && api != nil {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, _ = api.Notify(cctx, *tell)
		cancel()
	}
	for i, id := range ids {
		if ctx.Err() != nil || i >= perPass {
			break
		}
		a.mu.Lock()
		full, _ := a.capped()
		a.mu.Unlock()
		if full {
			break
		}
		a.attempt(ctx, id)
	}
	a.tellAbandoned(ctx)
}

// expire fails the hand-overs still waiting after maxAge, into the digest; the caller holds a.mu.
func (a *hubApp) expire() {
	var out []*record
	for _, r := range a.st.Records {
		if r.State == "pending" && r.SessionID == "" && a.now().Sub(r.CreatedAt) > maxAge {
			a.failRecord(r, "等了 48 小时也没能交给机器人")
			r.FailNotified = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	d := &a.st.Expired
	for _, r := range out {
		d.Count++
		d.Bot = r.BotName
		if len(d.Titles) < 3 {
			d.Titles = append(d.Titles, clip(r.Title, 30))
		}
	}
}

// expiredNotice is the notification of the hand-overs that waited too long since the last one: at most one in
// expiryWindow, carrying the count, under an id of its own window so it does not replace an earlier one unread. The
// caller holds a.mu.
func (a *hubApp) expiredNotice() *notice {
	d := &a.st.Expired
	now := a.now()
	if d.Count == 0 || (!d.NoticeAt.IsZero() && now.Sub(d.NoticeAt) < expiryWindow) {
		return nil
	}
	var titles []string
	for _, t := range d.Titles {
		titles = append(titles, "「"+t+"」")
	}
	more := ""
	if d.Count > len(d.Titles) {
		more = "等"
	}
	n := &notice{Title: "待办没交给 Orca 机器人", ID: fmt.Sprintf("orca-bot-expired-%d", now.Unix()/int64(expiryWindow/time.Second)),
		PushLevel: "P1", Body: fmt.Sprintf("%d 条待办等了 48 小时也没能交给机器人「%s」：%s%s。", d.Count,
			clip(orName(d.Bot), 40), strings.Join(titles, "、"), more)}
	*d = expiryDigest{NoticeAt: now}
	return n
}

func retryAfter(attempts int) time.Duration {
	d := time.Minute << min(attempts-1, 7) // 1m, 2m, 4m … about 2h
	return min(d, 2*time.Hour)
}

// refusal: the node program refused the hand-over or Orca refused it; it uses up an attempt. The node program
// always names its failures, so a failure without a code is the host's (the Mac away, the call dropped).
func refusal(code string) bool {
	switch code {
	case "invalid", "orca_error", "orca_incompatible":
		return true
	}
	return false
}

// waiting: a failure that is the Mac, Orca or the bot being away for now (offline, not_running, a timeout, a busy
// chat, a paused bot, bots turned off, a failure without a code), not a refusal; it does not use up attempts.
func waiting(code string) bool { return code != "bot_not_found" && !refusal(code) }

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
	if full, free := a.capped(); full { // the hourly cap counts what reached the bot: wait for room
		r.NextTry = free
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
	if code == "bot_not_found" {
		// One answer does not pause everything: the bot must be missing from a fresh bots.list too.
		a.mu.Unlock()
		gone, sure := a.botMissing(ctx, machine, params.BotID)
		a.mu.Lock()
		r = a.st.Records[id]
		if r == nil || r.State != "pending" {
			a.mu.Unlock()
			return
		}
		switch {
		case !sure:
			code = "offline" // could not ask again: wait
		case !gone:
			code, r.NotFoundAt = "orca_error", time.Time{} // listed after all: an ordinary refusal
		case r.NotFoundAt.IsZero() || a.now().Sub(r.NotFoundAt) < waitRetry:
			// Gone by two answers in a row, but only seconds apart: ask again later before pausing everything.
			if r.NotFoundAt.IsZero() {
				r.NotFoundAt = a.now()
			}
			code = "bot_unconfirmed"
		}
	}
	r.LastError, r.LastCode = failureWords(err), code
	now := a.now()
	var tell *notice
	switch {
	case code == "bot_not_found":
		a.failRecord(r, r.LastError)
		if a.st.Bot != nil && a.st.Bot.ID == r.BotID {
			a.st.Bot.Gone = true // pause until the owner picks again: every other hand-over would fail the same way
			a.rearm()
			a.settle()
		}
	case waiting(code):
		r.NextTry = now.Add(waitRetry) // the Mac or Orca away: no attempt used; maxAge ends it (expire)
	default:
		r.Attempts++
		if r.Attempts >= maxAttempts {
			a.failRecord(r, r.LastError)
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

// botMissing asks the Mac for bots.list again: gone when the bot is not in it; sure when the list was read.
func (a *hubApp) botMissing(ctx context.Context, machine, botID string) (gone, sure bool) {
	a.mu.Lock()
	nodes := a.nodes
	a.mu.Unlock()
	if nodes == nil {
		return false, false
	}
	cctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	var list botList
	if err := nodes.Call(cctx, machine, "orcabot.bots", map[string]any{}, &list); err != nil {
		return false, false
	}
	for _, b := range list.Bots {
		if b.ID == botID {
			return false, true
		}
	}
	return true, true
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

var knownCodes = []string{"bot_not_found", "bot_unconfirmed", "bot_paused", "bots_disabled", "orca_busy",
	"orca_not_running", "orca_incompatible", "orca_timeout", "orca_error", "offline", "timeout", "not_running", "invalid"}

// errCode is the structured code of a failure of nodes.call: the error's own code (the host's offline or timeout, or
// the node program's), or the code the node program put first in its message ("<code>: …") should the host pass on
// only the message. Never words found inside Orca's text.
func errCode(err error) string {
	var e *ext.Error
	if errors.As(err, &e) {
		if knownCode(e.Code) {
			return e.Code
		}
		if c, _, ok := strings.Cut(e.Message, ": "); ok && knownCode(c) {
			return c
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
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
	case "bot_unconfirmed":
		return "Orca 说找不到这个机器人，过一会儿再确认一次"
	case "bot_paused":
		return "机器人在 Orca 里暂停了，恢复后会再交"
	case "not_running":
		return "那台 Mac 上本扩展的程序没在运行"
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
	if a.writing[w.TodoID][w.Ref] { // the same write is on its way: its answer reaches every request waiting for it
		a.mu.Unlock()
		return
	}
	dup := slices.Contains(r.Refs, w.Ref)
	if !dup && len(r.Refs)+len(a.writing[w.TodoID]) >= writesPerTodo {
		a.mu.Unlock()
		result(writeAck{Error: fmt.Sprintf("这条待办已经写回了 %d 次，到上限了", writesPerTodo)})
		return
	}
	if a.writing[w.TodoID] == nil {
		a.writing[w.TodoID] = map[string]bool{}
	}
	a.writing[w.TodoID][w.Ref] = true // reserved: a write-back at the same time counts it
	bot := orName(r.BotName)
	api := a.api
	a.mu.Unlock()
	// release drops the reservation (the caller holds a.mu); it is dropped before the node program is answered, so a
	// write-back after this one never sees it counted twice (in Refs and reserved).
	release := func() {
		delete(a.writing[w.TodoID], w.Ref)
		if len(a.writing[w.TodoID]) == 0 {
			delete(a.writing, w.TodoID)
		}
	}
	if api == nil {
		a.mu.Lock()
		release()
		a.mu.Unlock()
		result(writeAck{Error: "hub 的接口还没准备好"})
		return
	}
	d := decisionPayload{Question: fmt.Sprintf("Orca 机器人「%s」的准备", clip(bot, 40)), Choice: text,
		Reason: "由 Orca 机器人通过 orca-bot 扩展写回；详情在它的 Orca 对话里", Ref: w.Ref, Source: "outcome"}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	inserted, err := api.AddDecision(cctx, w.TodoID, d)
	cancel()
	a.mu.Lock()
	release()
	if err != nil {
		a.lastErr = "写回待办失败：" + clip(err.Error(), 200)
		a.mu.Unlock()
		result(writeAck{Error: "写回待办失败：" + clip(err.Error(), 200)})
		return
	}
	if r := a.st.Records[w.TodoID]; r != nil && !slices.Contains(r.Refs, w.Ref) {
		r.Refs = append(r.Refs, w.Ref)
		a.save()
	}
	a.invalidate()
	a.mu.Unlock()
	result(writeAck{OK: true, Inserted: inserted})
}
