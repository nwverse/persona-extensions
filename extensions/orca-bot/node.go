package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
)

// The program on the Mac the owner picked (the manifest's node section). It is the only part that talks to Orca, and
// only over Orca's local runtime socket. What it does:
//
//	orcabot.status            whether Orca runs and is compatible, and the MCP command to register
//	orcabot.bots              the bots of this Orca (bots.list), for the bot picker
//	orcabot.handoff           a new chat of the bot (bots.session.start) and the to-do sent there (agentSession.send)
//	orcabot.finished_ack      the hub heard that the bot finished: stop telling it
//	orcabot.writeback_result  what became of a write-back the MCP command asked for
//
// and, on its own: it polls agentSession.history of each chat it handed a to-do to until the turn ends, then tells the
// hub (node.event orcabot.finished, repeated until acknowledged); and it serves the MCP command (orca-bot mcp) over a
// unix socket in its data directory that only this user can open, forwarding write-backs to the hub
// (node.event orcabot.writeback). The hand-overs are persisted, so a restart neither sends twice nor forgets a chat.

const (
	nodePollEvery = 10 * time.Second
	// nodeQuietStart: a turn seen idle this soon after the send, without having been seen working, may not have
	// started yet.
	nodeQuietStart = time.Minute
	// nodeWatchFor is how long a chat is followed before the hub is told it is still not done.
	nodeWatchFor = 24 * time.Hour
	// nodeReemit is how often an unacknowledged finish is told again.
	nodeReemit = time.Minute
	// nodeAckWait is how long a write-back waits for the hub's answer before telling the bot it is on its way.
	nodeAckWait = 20 * time.Second
	// nodeHandoffsKept bounds the persisted hand-overs: the oldest acknowledged ones go first.
	nodeHandoffsKept = 1000
	// maxSocketPath: a unix socket path holds at most 103 bytes on macOS.
	maxSocketPath = 100
)

// orcaAPI is what the node program needs of Orca (orcaClient; a fake in tests).
type orcaAPI interface {
	Status(ctx context.Context) (string, error)
	Bots(ctx context.Context) ([]orcaBot, bool, error)
	StartSession(ctx context.Context, botID string) (string, error)
	Head(ctx context.Context, sessionID string) (*orcaHead, error)
	Send(ctx context.Context, sessionID string, fence int64, opID, text string) error
}

// todoSnapshot is the to-do as it was handed over: what the MCP command's todo_get answers.
type todoSnapshot struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Notes   string `json:"notes,omitempty"`
	Project string `json:"project,omitempty"`
	Due     string `json:"due,omitempty"`
}

type nodeWrite struct {
	Ref    string    `json:"ref"`
	At     time.Time `json:"at"`
	Status string    `json:"status"` // pending | written | duplicate | refused
	Error  string    `json:"error,omitempty"`
}

type nodeHandoff struct {
	TodoID     string       `json:"todo_id"`
	BotID      string       `json:"bot_id"`
	SessionID  string       `json:"session_id,omitempty"`
	OpID       string       `json:"op_id"`
	Todo       todoSnapshot `json:"todo"`
	CreatedAt  time.Time    `json:"created_at"`
	Sent       bool         `json:"sent"`
	SentAt     time.Time    `json:"sent_at,omitzero"`
	SawWorking bool         `json:"saw_working,omitempty"`
	Finished   bool         `json:"finished,omitempty"`
	FinishedAt time.Time    `json:"finished_at,omitzero"`
	TurnState  string       `json:"turn_state,omitempty"`
	Acked      bool         `json:"acked,omitempty"`
	EmittedAt  time.Time    `json:"emitted_at,omitzero"`
	Writes     []nodeWrite  `json:"writes,omitempty"`
}

type nodeState struct {
	Handoffs map[string]*nodeHandoff `json:"handoffs"`
}

type writeAck struct {
	OK       bool   `json:"ok"`
	Inserted bool   `json:"inserted"`
	Error    string `json:"error,omitempty"`
}

type nodeApp struct {
	orca      orcaAPI
	now       func() time.Time
	pollEvery time.Duration
	ackWait   time.Duration

	sendMu sync.Mutex // one hand-over at a time: Orca reuses an empty chat, so two at once could share one

	mu       sync.Mutex
	data     string
	emit     func(event string, params any) error
	st       nodeState
	acks     map[string]chan writeAck
	link     string // the stable path of this program, for the MCP command
	socket   string
	sockErr  string
	lastPoll string // the last error following the chats, "" when fine
	listener net.Listener
}

func newNodeApp(orca orcaAPI) *nodeApp {
	if orca == nil {
		orca = newOrcaClient(nil)
	}
	return &nodeApp{orca: orca, now: time.Now, pollEvery: nodePollEvery, ackWait: nodeAckWait,
		acks: map[string]chan writeAck{}, st: nodeState{Handoffs: map[string]*nodeHandoff{}}}
}

func (n *nodeApp) program() ext.Program {
	return ext.Program{
		ID: about.ID, Version: about.Version, Manifest: manifest,
		Init: func(ctx context.Context, in *ext.Init, hub *ext.Hub) (map[string]any, error) {
			data := strings.TrimSpace(in.Info().DataDir)
			if data == "" {
				dir, err := os.UserCacheDir()
				if err != nil {
					return nil, err
				}
				data = filepath.Join(dir, "persona-orca-bot")
			}
			if err := n.start(ctx, data, func(event string, params any) error { return hub.Notify(event, params) }); err != nil {
				return nil, err
			}
			return nil, nil
		},
		Namespaces: map[string]ext.Handler{"orcabot": n.rpc},
	}
}

// start opens the program (open) and starts following the chats, until ctx ends.
func (n *nodeApp) start(ctx context.Context, data string, emit func(string, any) error) error {
	if err := n.open(ctx, data, emit); err != nil {
		return err
	}
	go n.follow(ctx)
	return nil
}

// open loads the state, keeps the stable link to this program and opens the MCP socket.
func (n *nodeApp) open(ctx context.Context, data string, emit func(string, any) error) error {
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	n.mu.Lock()
	n.data, n.emit = data, emit
	var st nodeState
	if err := readJSONFile(n.statePath(), &st); err != nil {
		log.Printf("orca-bot: the hand-over state was damaged and is kept as state.json.bad; starting empty")
		st = nodeState{}
	}
	if st.Handoffs == nil {
		st.Handoffs = map[string]*nodeHandoff{}
	}
	n.st = st
	n.mu.Unlock()

	if link, err := keepLink(data); err != nil {
		log.Printf("orca-bot: cannot keep the link to this program: %v", err)
	} else {
		n.mu.Lock()
		n.link = link
		n.mu.Unlock()
	}
	if err := n.listen(ctx); err != nil {
		log.Printf("orca-bot: the MCP socket is not open: %v", err)
		n.mu.Lock()
		n.sockErr = err.Error()
		n.mu.Unlock()
	}
	return nil
}

func (n *nodeApp) statePath() string { return filepath.Join(n.data, "state.json") }

// save writes the state; the caller holds n.mu.
func (n *nodeApp) save() {
	if len(n.st.Handoffs) > nodeHandoffsKept {
		var done []*nodeHandoff
		for _, h := range n.st.Handoffs {
			if h.Acked {
				done = append(done, h)
			}
		}
		sort.Slice(done, func(i, j int) bool { return done[i].CreatedAt.Before(done[j].CreatedAt) })
		for _, h := range done {
			if len(n.st.Handoffs) <= nodeHandoffsKept {
				break
			}
			delete(n.st.Handoffs, h.TodoID)
		}
	}
	if err := writeJSONFile(n.statePath(), n.st); err != nil {
		log.Printf("orca-bot: cannot save the hand-over state: %v", err)
	}
}

// keepLink points <data>/bin/orca-bot at this program, so the command registered with Orca stays valid across
// upgrades (the hub installs each version in a directory of its own).
func keepLink(data string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	dir := filepath.Join(data, "bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	link := filepath.Join(dir, "orca-bot")
	if cur, err := os.Readlink(link); err == nil && cur == exe {
		return link, nil
	}
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(exe, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return link, nil
}

// socketPath is the MCP socket: in the data directory, or, when that path is too long for a unix socket, in a
// directory of this user's own under /tmp.
func socketPath(data string) (string, error) {
	p := filepath.Join(data, "mcp.sock")
	if len(p) <= maxSocketPath {
		return p, nil
	}
	dir := filepath.Join("/tmp", fmt.Sprintf("orca-bot-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !st.IsDir() || st.Mode().Perm() != 0o700 || !ownedByMe(st) {
		return "", fmt.Errorf("%s is not a private directory of this user", dir)
	}
	return filepath.Join(dir, "mcp.sock"), nil
}

func (n *nodeApp) listen(ctx context.Context) error {
	n.mu.Lock()
	data := n.data
	n.mu.Unlock()
	path, err := socketPath(data)
	if err != nil {
		return err
	}
	_ = os.Remove(path) // a socket left by the previous run
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return err
	}
	n.mu.Lock()
	n.socket, n.listener, n.sockErr = path, l, ""
	n.mu.Unlock()
	go func() {
		<-ctx.Done()
		l.Close()
	}()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go n.serveConn(ctx, c)
		}
	}()
	return nil
}

// --- the hub's calls ---

type handoffParams struct {
	TodoID string       `json:"todo_id"`
	BotID  string       `json:"bot_id"`
	OpID   string       `json:"op_id"`
	Text   string       `json:"text"`
	Todo   todoSnapshot `json:"todo"`
}

type handoffResult struct {
	SessionID string `json:"session_id"`
	Already   bool   `json:"already,omitempty"`
}

func (n *nodeApp) rpc(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "status":
		return n.status(ctx), nil
	case "bots":
		bots, enabled, err := n.orca.Bots(ctx)
		if err != nil {
			return nil, nodeErr(err)
		}
		if bots == nil {
			bots = []orcaBot{}
		}
		return map[string]any{"bots": bots, "enabled": enabled}, nil
	case "handoff":
		var p handoffParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: "handoff: " + err.Error()}
		}
		return n.handoff(ctx, p)
	case "finished_ack":
		var p struct {
			TodoID string `json:"todo_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: err.Error()}
		}
		n.mu.Lock()
		if h := n.st.Handoffs[p.TodoID]; h != nil && h.Finished && !h.Acked {
			h.Acked = true
			n.save()
		}
		n.mu.Unlock()
		return map[string]any{}, nil
	case "writeback_result":
		var p struct {
			TodoID string `json:"todo_id"`
			Ref    string `json:"ref"`
			writeAck
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: err.Error()}
		}
		n.writeResult(p.TodoID, p.Ref, p.writeAck)
		return map[string]any{}, nil
	}
	return nil, ext.UnknownMethod("orcabot." + method)
}

// nodeErr turns an Orca failure into an error the hub can tell apart by its code (and its message, should the hub
// pass only that on).
func nodeErr(err error) error {
	code := "orca_error"
	switch {
	case errors.Is(err, errOrcaNotRunning):
		code = "orca_not_running"
	case errors.Is(err, errOrcaIncompatible):
		code = "orca_incompatible"
	case errors.Is(err, context.DeadlineExceeded):
		code = "orca_timeout"
	case orcaCode(err) == "bot_not_found" || strings.Contains(err.Error(), "bot_not_found"):
		code = "bot_not_found"
	case strings.Contains(err.Error(), "bots_disabled") || strings.Contains(strings.ToLower(err.Error()), "bots are off"):
		code = "bots_disabled"
	}
	return &ext.Error{Code: code, Message: code + ": " + clip(err.Error(), 300)}
}

type nodeStatus struct {
	OrcaRunning bool   `json:"orca_running"`
	OrcaVersion string `json:"orca_version,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	Error       string `json:"error,omitempty"`
	Link        string `json:"link,omitempty"`
	Socket      string `json:"socket,omitempty"`
	SocketError string `json:"socket_error,omitempty"`
	Following   int    `json:"following"`
	PollError   string `json:"poll_error,omitempty"`
}

func (n *nodeApp) status(ctx context.Context) nodeStatus {
	var s nodeStatus
	version, err := n.orca.Status(ctx)
	if err != nil {
		var ee *ext.Error
		errors.As(nodeErr(err), &ee)
		s.ErrorCode, s.Error = ee.Code, clip(err.Error(), 300)
	} else {
		s.OrcaRunning, s.OrcaVersion = true, version
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	s.Link, s.Socket, s.SocketError, s.PollError = n.link, n.socket, n.sockErr, n.lastPoll
	for _, h := range n.st.Handoffs {
		if h.Sent && !h.Finished {
			s.Following++
		}
	}
	return s
}

func (n *nodeApp) handoff(ctx context.Context, p handoffParams) (*handoffResult, error) {
	if !safeID.MatchString(p.TodoID) || !safeID.MatchString(p.BotID) || p.OpID == "" || len(p.OpID) > 100 {
		return nil, &ext.Error{Code: "invalid", Message: "handoff: todo_id, bot_id or op_id is not valid"}
	}
	if strings.TrimSpace(p.Text) == "" || len(p.Text) > 64<<10 {
		return nil, &ext.Error{Code: "invalid", Message: "handoff: the text is empty or too long"}
	}
	n.sendMu.Lock()
	defer n.sendMu.Unlock()

	n.mu.Lock()
	h := n.st.Handoffs[p.TodoID]
	switch {
	case h != nil && h.Sent:
		res := &handoffResult{SessionID: h.SessionID, Already: true}
		n.mu.Unlock()
		return res, nil
	case h != nil && h.BotID != p.BotID && h.SessionID != "":
		n.mu.Unlock()
		return nil, &ext.Error{Code: "invalid", Message: "handoff: this to-do was being handed to another bot"}
	case h == nil:
		p.Todo.ID = p.TodoID
		h = &nodeHandoff{TodoID: p.TodoID, BotID: p.BotID, OpID: p.OpID, Todo: p.Todo, CreatedAt: n.now()}
		n.st.Handoffs[p.TodoID] = h
		n.save()
	case h.SessionID == "": // an earlier attempt never reached Orca: nothing was sent, take this one as it is
		p.Todo.ID = p.TodoID
		h.BotID, h.OpID, h.Todo = p.BotID, p.OpID, p.Todo
		n.save()
	}
	sessionID, opID := h.SessionID, h.OpID
	n.mu.Unlock()

	if sessionID == "" {
		sid, err := n.orca.StartSession(ctx, p.BotID)
		if err != nil {
			return nil, nodeErr(err)
		}
		if !safeID.MatchString(sid) {
			return nil, nodeErr(fmt.Errorf("%w: bots.session.start answered an odd session id", errOrcaIncompatible))
		}
		n.mu.Lock()
		h.SessionID, sessionID = sid, sid
		n.save()
		n.mu.Unlock()
	}
	head, err := n.orca.Head(ctx, sessionID)
	if err != nil {
		return nil, nodeErr(err)
	}
	// The same operation id every attempt: should an earlier attempt have reached Orca, this one replays it.
	if err := n.orca.Send(ctx, sessionID, head.Fence, opID, p.Text); err != nil {
		return nil, nodeErr(err)
	}
	n.mu.Lock()
	h.Sent, h.SentAt = true, n.now()
	n.save()
	n.mu.Unlock()
	return &handoffResult{SessionID: sessionID}, nil
}

// --- following the chats ---

func (n *nodeApp) follow(ctx context.Context) {
	t := time.NewTicker(n.pollEvery)
	defer t.Stop()
	for {
		n.pollOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type finishedEvent struct {
	TodoID    string `json:"todo_id"`
	SessionID string `json:"session_id"`
	BotID     string `json:"bot_id"`
	State     string `json:"state"` // completed | interrupted | unverifiable | unavailable | timeout
}

// pollOnce looks once at every chat still followed, and tells the hub again of finishes it has not acknowledged.
func (n *nodeApp) pollOnce(ctx context.Context) {
	n.mu.Lock()
	var follow []*nodeHandoff
	var tell []finishedEvent
	now := n.now()
	for _, h := range n.st.Handoffs {
		switch {
		case h.Sent && !h.Finished:
			follow = append(follow, h)
		case h.Finished && !h.Acked && now.Sub(h.EmittedAt) >= nodeReemit:
			h.EmittedAt = now
			tell = append(tell, finishedEvent{h.TodoID, h.SessionID, h.BotID, h.TurnState})
		}
	}
	emit := n.emit
	n.mu.Unlock()

	var pollErr string
	for _, h := range follow {
		if ctx.Err() != nil {
			return
		}
		n.mu.Lock()
		sid, sentAt, saw := h.SessionID, h.SentAt, h.SawWorking
		n.mu.Unlock()
		state, working, err := n.turnEnded(ctx, sid, sentAt, saw)
		n.mu.Lock()
		if working {
			h.SawWorking = true
		}
		if err != nil {
			pollErr = clip(err.Error(), 200)
		}
		if state == "" && n.now().Sub(sentAt) > nodeWatchFor {
			state = "timeout"
		}
		if state != "" {
			h.Finished, h.FinishedAt, h.TurnState, h.EmittedAt = true, n.now(), state, n.now()
			tell = append(tell, finishedEvent{h.TodoID, h.SessionID, h.BotID, state})
			n.save()
		} else if working && !saw {
			n.save()
		}
		n.mu.Unlock()
	}
	n.mu.Lock()
	n.lastPoll = pollErr
	n.mu.Unlock()
	for _, ev := range tell {
		if emit != nil {
			_ = emit("orcabot.finished", ev)
		}
	}
}

// turnEnded says how the chat's turn ended ("" while it has not), and whether it is working now.
func (n *nodeApp) turnEnded(ctx context.Context, sessionID string, sentAt time.Time, sawWorking bool) (string, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, orcaHistoryTimeout)
	defer cancel()
	head, err := n.orca.Head(cctx, sessionID)
	if err != nil {
		if code := orcaCode(err); strings.Contains(code, "not_found") || strings.Contains(code, "session_gone") {
			return "unavailable", false, nil
		}
		return "", false, err
	}
	if head.Working != nil {
		if *head.Working {
			return "", true, nil
		}
		if !sawWorking && n.now().Sub(sentAt) < nodeQuietStart {
			return "", false, nil // not seen starting yet
		}
		return turnStateOr(head.TurnState), false, nil
	}
	// An older host that does not say whether it works: the latest turn, if it began after the send.
	if head.TurnState == "running" {
		return "", true, nil
	}
	if head.TurnState != "" && head.TurnAt >= sentAt.Add(-5*time.Second).UnixMilli() {
		return turnStateOr(head.TurnState), false, nil
	}
	return "", false, nil
}

func turnStateOr(s string) string {
	switch s {
	case "completed", "interrupted", "unverifiable":
		return s
	}
	return "completed"
}

// --- the MCP command's socket ---

// mcpRequest is one request of the MCP command: the bot it was registered for, the chat it runs in when Orca says
// (ORCA_AGENT_SESSION_ID), and the tool's arguments.
type mcpRequest struct {
	Method  string `json:"method"` // list | get | write
	Bot     string `json:"bot"`
	Session string `json:"session,omitempty"`
	TodoID  string `json:"todo_id,omitempty"`
	Text    string `json:"text,omitempty"`
}

type mcpReply struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (n *nodeApp) serveConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	r := bufio.NewReaderSize(c, 64<<10)
	for {
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Minute))
		line, err := r.ReadBytes('\n')
		if len(line) > 256<<10 {
			return
		}
		if len(strings.TrimSpace(string(line))) > 0 {
			var req mcpRequest
			var rep mcpReply
			if jerr := json.Unmarshal(line, &req); jerr != nil {
				rep = mcpReply{Error: "请求不是合法的 JSON"}
			} else {
				rep = n.mcp(ctx, req)
			}
			b, _ := json.Marshal(rep)
			_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, werr := c.Write(append(b, '\n')); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

const notHanded = "这条待办不是交给这个机器人的，读写不了。只有 Persona 交给这个机器人的待办可以读写。"

// lookup finds the to-do handed to bot (and, when the chat is known, to that chat); the caller holds n.mu.
func (n *nodeApp) lookup(bot, session, todoID string) *nodeHandoff {
	h := n.st.Handoffs[todoID]
	if h == nil || !h.Sent || h.BotID != bot || (session != "" && h.SessionID != session) {
		return nil
	}
	return h
}

func (n *nodeApp) mcp(ctx context.Context, req mcpRequest) mcpReply {
	if !safeID.MatchString(req.Bot) || (req.Session != "" && !safeID.MatchString(req.Session)) {
		return mcpReply{Error: "MCP 命令缺少有效的 --bot（登记命令时要带上机器人的 id）"}
	}
	switch req.Method {
	case "list":
		n.mu.Lock()
		type row struct {
			TodoID   string    `json:"todo_id"`
			Title    string    `json:"title"`
			SentAt   time.Time `json:"sent_at"`
			Finished bool      `json:"finished"`
		}
		rows := []row{}
		for _, h := range n.st.Handoffs {
			if n.lookup(req.Bot, req.Session, h.TodoID) != nil {
				rows = append(rows, row{h.TodoID, h.Todo.Title, h.SentAt, h.Finished})
			}
		}
		n.mu.Unlock()
		sort.Slice(rows, func(i, j int) bool { return rows[i].SentAt.After(rows[j].SentAt) })
		return mcpReply{OK: true, Result: map[string]any{"todos": rows}}
	case "get":
		n.mu.Lock()
		defer n.mu.Unlock()
		h := n.lookup(req.Bot, req.Session, req.TodoID)
		if h == nil {
			return mcpReply{Error: notHanded}
		}
		writes := make([]nodeWrite, len(h.Writes))
		copy(writes, h.Writes)
		return mcpReply{OK: true, Result: map[string]any{"todo": h.Todo, "handed_at": h.SentAt, "write_backs": writes,
			"write_backs_left": max(0, writesPerTodo-countWrites(h.Writes))}}
	case "write":
		return n.write(ctx, req)
	}
	return mcpReply{Error: "未知的请求"}
}

func countWrites(ws []nodeWrite) int {
	k := 0
	for _, w := range ws {
		if w.Status != "refused" {
			k++
		}
	}
	return k
}

func (n *nodeApp) write(ctx context.Context, req mcpRequest) mcpReply {
	text := strings.TrimSpace(cleanText(req.Text))
	if text == "" {
		return mcpReply{Error: "text 是空的"}
	}
	if utf8.RuneCountInString(text) > writeMaxRunes {
		return mcpReply{Error: fmt.Sprintf("text 太长了（%d 字）：请把要点压缩到 %d 字以内再写", utf8.RuneCountInString(text), writeMaxRunes)}
	}
	n.mu.Lock()
	h := n.lookup(req.Bot, req.Session, req.TodoID)
	if h == nil {
		n.mu.Unlock()
		return mcpReply{Error: notHanded}
	}
	ref := "orca-bot:" + shortHash(24, h.TodoID, h.SessionID, text)
	for _, w := range h.Writes {
		if w.Ref == ref && w.Status != "refused" {
			n.mu.Unlock()
			return mcpReply{OK: true, Result: map[string]any{"status": "duplicate", "message": "同样的内容已经写过了。"}}
		}
	}
	if countWrites(h.Writes) >= writesPerTodo || len(h.Writes) >= 3*writesPerTodo {
		n.mu.Unlock()
		return mcpReply{Error: fmt.Sprintf("这条待办已经写回了 %d 次，到上限了。", writesPerTodo)}
	}
	ch := make(chan writeAck, 1)
	n.acks[ref] = ch
	h.Writes = append(h.Writes, nodeWrite{Ref: ref, At: n.now(), Status: "pending"})
	n.save()
	ev := map[string]any{"todo_id": h.TodoID, "session_id": h.SessionID, "bot_id": h.BotID, "ref": ref, "text": text}
	emit, wait := n.emit, n.ackWait
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.acks, ref)
		n.mu.Unlock()
	}()
	if emit == nil || emit("orcabot.writeback", ev) != nil {
		n.writeResult(req.TodoID, ref, writeAck{Error: "没能交给 Persona"})
		return mcpReply{Error: "没能把内容交给 Persona，请稍后再试。"}
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case a := <-ch:
		if !a.OK {
			return mcpReply{Error: "Persona 没有写入：" + clip(a.Error, 300)}
		}
		status := "written"
		if !a.Inserted {
			status = "duplicate"
		}
		return mcpReply{OK: true, Result: map[string]any{"status": status, "message": "已经写到这条待办上（作为一条决定记录）。"}}
	case <-t.C:
		return mcpReply{OK: true, Result: map[string]any{"status": "pending", "message": "已交给 Persona，稍后会写到这条待办上。"}}
	case <-ctx.Done():
		return mcpReply{Error: "程序正在退出"}
	}
}

// writeResult records what the hub did with a write-back, and wakes the MCP request waiting for it.
func (n *nodeApp) writeResult(todoID, ref string, a writeAck) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if h := n.st.Handoffs[todoID]; h != nil {
		for i := range h.Writes {
			if h.Writes[i].Ref == ref && h.Writes[i].Status == "pending" {
				switch {
				case !a.OK:
					h.Writes[i].Status, h.Writes[i].Error = "refused", clip(a.Error, 200)
				case a.Inserted:
					h.Writes[i].Status = "written"
				default:
					h.Writes[i].Status = "duplicate"
				}
				n.save()
			}
		}
	}
	if ch := n.acks[ref]; ch != nil {
		select {
		case ch <- a:
		default:
		}
	}
}
