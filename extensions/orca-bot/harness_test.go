package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A fake hub (its to-do, decision and notify routes), the node program wired to it as nodes.call and node.event
// would, and the fake Orca underneath: everything synthetic.

const testMac = "mac-test-1"

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fakeHub struct {
	mu        sync.Mutex
	todos     []todoRow
	decisions []struct {
		TodoID string
		D      decisionPayload
	}
	notices   []notice
	failList  error
	notifyErr error // Notify fails with it while set
	held      bool  // push grading holds every notification
}

func (h *fakeHub) add(t todoRow) {
	h.mu.Lock()
	h.todos = append([]todoRow{t}, h.todos...)
	h.mu.Unlock()
}

func (h *fakeHub) Todos(ctx context.Context) ([]todoRow, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failList != nil {
		return nil, h.failList
	}
	return append([]todoRow(nil), h.todos...), nil
}

func (h *fakeHub) Todo(ctx context.Context, id string) (*todoRow, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range h.todos {
		if t.ID == id {
			c := t
			return &c, nil
		}
	}
	return nil, nil
}

func (h *fakeHub) AddDecision(ctx context.Context, todoID string, d decisionPayload) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, x := range h.decisions {
		if x.TodoID == todoID && x.D.Ref == d.Ref {
			return false, nil
		}
	}
	h.decisions = append(h.decisions, struct {
		TodoID string
		D      decisionPayload
	}{todoID, d})
	return true, nil
}

func (h *fakeHub) Notify(ctx context.Context, n notice) (delivery, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.notifyErr != nil {
		return delivery{}, h.notifyErr
	}
	h.notices = append(h.notices, n)
	if h.held {
		return delivery{Held: true, Reason: "ln_p1_held"}, nil
	}
	return delivery{Sent: true}, nil
}

func (h *fakeHub) noticeTitles() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, n := range h.notices {
		out = append(out, n.Title)
	}
	return out
}

func (h *fakeHub) counts() (decisions, notices int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.decisions), len(h.notices)
}

// nodeLink is nodes.call to one node program (on testMac), by way of JSON as on the wire.
type nodeLink struct {
	node *nodeApp
	down atomic.Bool
}

func (l *nodeLink) Call(ctx context.Context, machine, method string, params, out any) error {
	if l.down.Load() || machine != testMac {
		return fmt.Errorf("machine_offline")
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	res, err := l.node.rpc(ctx, strings.TrimPrefix(method, "orcabot."), raw)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

type world struct {
	t     *testing.T
	ctx   context.Context
	clock *clock
	orca  *fakeOrca
	hub   *fakeHub
	hubD  string
	nodeD string
	link  *nodeLink
	app   *hubApp
	node  *nodeApp
	cfg   config
}

func shortTemp(t *testing.T, prefix string) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func newWorld(t *testing.T, cfg config) *world {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := &world{t: t, ctx: ctx, clock: &clock{t: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)}, orca: newFakeOrca(t),
		hub: &fakeHub{}, hubD: shortTemp(t, "obh"), nodeD: shortTemp(t, "obn"), cfg: cfg}
	w.orca.now = w.clock.now
	w.startNode()
	w.startHub()
	return w
}

// startNode starts (or restarts) the node program on its data directory.
func (w *world) startNode() {
	w.node = newNodeApp(w.orca.client())
	w.node.now, w.node.ackWait = w.clock.now, 2*time.Second
	if w.link == nil {
		w.link = &nodeLink{}
	}
	w.link.node = w.node
	if err := w.node.open(w.ctx, w.nodeD, w.emit); err != nil {
		w.t.Fatal(err)
	}
}

// startHub starts (or restarts) the hub program on its data directory, with the world's settings.
func (w *world) startHub() {
	w.app = newHubApp(w.hub, w.link)
	w.app.now = w.clock.now
	w.app.mu.Lock()
	w.app.cfg, w.app.data, w.app.scopes = w.cfg, w.hubD, []string{"todos.read", "todos.write", "notify"}
	w.app.load()
	w.app.rearm()
	w.app.save()
	w.app.mu.Unlock()
}

// setConfig is the owner saving the settings: the hub restarts the program with them.
func (w *world) setConfig(cfg config) {
	w.cfg = cfg
	w.startHub()
}

// emit is node.event, as the hub hands it to the hub program.
func (w *world) emit(event string, params any) error {
	raw, _ := json.Marshal(map[string]any{"machine_id": testMac, "event": event, "params": params})
	app := w.app
	if app == nil { // the hub program is not running: the event is lost, as on a real hub
		return nil
	}
	go func() { _, _ = app.nodeEvent(w.ctx, "event", raw) }() // events are not awaited
	return nil
}

func (w *world) pick(botID string) {
	w.t.Helper()
	if toast := w.app.act(w.ctx, "pick:"+botID); !strings.Contains(toast, "以后") {
		w.t.Fatalf("pick %s: %s", botID, toast)
	}
}

func (w *world) newTodo(id, title, notes, project string) todoRow {
	w.clock.add(time.Second)
	t := todoRow{ID: id, Title: title, Notes: notes, Project: project, CreatedAt: w.clock.now().Format(time.RFC3339Nano)}
	w.hub.add(t)
	return t
}

// created is the todo.changed event for a new to-do, handled as the program's loop would.
func (w *world) created(id string) {
	raw, _ := json.Marshal(map[string]any{"id": id, "title": "x", "what": "created"})
	_, _ = w.app.event(w.ctx, "todo.changed", raw)
	select {
	case got := <-w.app.created:
		w.app.consider(w.ctx, got, nil)
	default:
	}
}

func (w *world) record(id string) *record {
	w.app.mu.Lock()
	defer w.app.mu.Unlock()
	if r := w.app.st.Records[id]; r != nil {
		c := *r
		return &c
	}
	return nil
}

// eventually waits for cond, for the events the node program sends without waiting.
func (w *world) eventually(what string, cond func() bool) {
	w.t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.t.Fatalf("timed out waiting: %s", what)
}

func (w *world) nodeHandoff(id string) nodeHandoff {
	w.node.mu.Lock()
	defer w.node.mu.Unlock()
	if h := w.node.st.Handoffs[id]; h != nil {
		return *h
	}
	return nodeHandoff{}
}

// mcp is the bot's MCP command as registered from the page (its token), run in chat session ("" none known).
func (w *world) mcp(bot, session string) *mcpServer {
	token := w.node.status(context.Background(), bot).Token
	return &mcpServer{socket: w.node.socket, bot: bot, token: token, session: session}
}

func (w *world) noticeCount() int {
	_, n := w.hub.counts()
	return n
}
