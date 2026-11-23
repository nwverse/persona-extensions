package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
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
//	orcabot.status            whether Orca runs and is compatible, and the MCP command to register for a bot
//	orcabot.bots              the bots of this Orca (bots.list), for the bot picker
//	orcabot.handoff           a new chat of the bot (bots.session.start) and the to-do sent there (agentSession.send)
//	orcabot.set_scope         whether the bot's MCP command may act without knowing its chat
//	orcabot.finished_ack      the hub heard that the bot finished: stop telling it
//	orcabot.writeback_result  what became of a write-back the MCP command asked for
//
// and, on its own: it polls agentSession.history of each chat it handed a to-do to until the turn ends, then tells the
// hub (node.event orcabot.finished, repeated until acknowledged); it tells the hub when the bot waits on an approval
// or a question (orcabot.needs_you); and it serves the MCP command (orca-bot mcp) over a unix socket in its data
// directory that only this user can open, forwarding write-backs to the hub (node.event orcabot.writeback, repeated
// until answered). The hand-overs are persisted, so a restart neither sends twice nor forgets a chat. When it starts it
// says so (orcabot.hello), so the hub can retry what waited for this Mac.

const (
	nodePollEvery = 10 * time.Second
	// nodeTurnSkew: a turn that began this long before the send still counts as the send's (clock and journal skew).
	nodeTurnSkew = 5 * time.Second
	// nodeWatchFor is how long a chat is followed before the hub is told it is still not done.
	nodeWatchFor = 24 * time.Hour
	// nodeLostAfter: a chat whose history Orca keeps refusing (while Orca itself answers) for this long is gone.
	nodeLostAfter = 30 * time.Minute
	// nodeReemit is how often an unacknowledged finish, a needs-you or an unanswered write-back is told again.
	nodeReemit = time.Minute
	// nodeTellFor: an unacknowledged finish is told again for this long, then given up.
	nodeTellFor = 24 * time.Hour
	// nodeAckWait is how long a write-back waits for the hub's answer before telling the bot it is on its way.
	nodeAckWait = 20 * time.Second
	// nodeStaleClaim: a hand-over that never sent and was last tried longer ago than the hub waits for an answer
	// (handoffTimeout) no longer holds its chat: a new hand-over may take the chat over.
	nodeStaleClaim = 2 * time.Minute
	// nodeUnsentKept: a hand-over that never sent is forgotten after this (the hub gives up after maxAge).
	nodeUnsentKept = 72 * time.Hour
	// nodeHandoffsKept bounds the persisted hand-overs: the oldest acknowledged ones go first.
	nodeHandoffsKept = 1000
	// maxSocketPath: a unix socket path holds at most 103 bytes on macOS.
	maxSocketPath = 100
	// mcpMaxLine bounds one request on the MCP socket.
	mcpMaxLine = 256 << 10
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
	// Text is kept only while pending, to tell the hub again; EmittedAt is when it was last told.
	Text      string    `json:"text,omitempty"`
	EmittedAt time.Time `json:"emitted_at,omitzero"`
}

type nodeHandoff struct {
	TodoID    string       `json:"todo_id"`
	BotID     string       `json:"bot_id"`
	SessionID string       `json:"session_id,omitempty"`
	OpID      string       `json:"op_id"`
	Todo      todoSnapshot `json:"todo"`
	CreatedAt time.Time    `json:"created_at"`
	TriedAt   time.Time    `json:"tried_at,omitzero"` // when the last attempt to send it ended
	// Unsure: a send failed without Orca refusing it (a timeout, a dropped connection): Orca may have taken it. Such a
	// claim on its chat is never let go by age, nor by abandon; only a retry (Orca replays the same operation) or the
	// prune of never-sent hand-overs ends it.
	Unsure     bool      `json:"unsure,omitempty"`
	Sent       bool      `json:"sent"`
	SentAt     time.Time `json:"sent_at,omitzero"`
	SawWorking bool      `json:"saw_working,omitempty"`
	Finished   bool      `json:"finished,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	TurnState  string    `json:"turn_state,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	Acked      bool      `json:"acked,omitempty"`
	EmittedAt  time.Time `json:"emitted_at,omitzero"`
	// FailingSince is when Orca, answering, began refusing this chat's history.
	FailingSince time.Time `json:"failing_since,omitzero"`
	// PromptPolls counts the polls in a row that saw an approval or question waiting; Episode numbers the waits.
	PromptPolls     int         `json:"prompt_polls,omitempty"`
	PromptOpen      bool        `json:"prompt_open,omitempty"`
	PromptEpisode   int         `json:"prompt_episode,omitempty"`
	PromptEmittedAt time.Time   `json:"prompt_emitted_at,omitzero"`
	Writes          []nodeWrite `json:"writes,omitempty"`
}

type nodeState struct {
	Handoffs map[string]*nodeHandoff `json:"handoffs"`
	// Tokens are the per-bot secrets of the MCP command (--token): another local process cannot pass for the bot's
	// tool by naming the bot alone.
	Tokens map[string]string `json:"tokens,omitempty"`
	// Wide lists the bots whose MCP command may act without knowing its chat (the owner's choice on the page).
	Wide map[string]bool `json:"wide,omitempty"`
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

	sendMu sync.Mutex // one hand-over at a time

	mu       sync.Mutex
	data     string
	emit     func(event string, params any) error
	st       nodeState
	acks     map[string][]chan writeAck // the MCP requests waiting for the hub's answer, by ref
	link     string                     // the stable path of this program, for the MCP command
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
		acks: map[string][]chan writeAck{}, st: nodeState{Handoffs: map[string]*nodeHandoff{}}}
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
	if emit != nil {
		_ = emit("orcabot.hello", map[string]any{}) // the hub may retry at once what waited for this Mac
	}
	return nil
}

func (n *nodeApp) statePath() string { return filepath.Join(n.data, "state.json") }

// save writes the state; the caller holds n.mu.
func (n *nodeApp) save() {
	for id, h := range n.st.Handoffs { // never sent and long given up by the hub: forgotten, notes and all
		if !h.Sent && n.now().Sub(lastTry(h)) > nodeUnsentKept {
			delete(n.st.Handoffs, id)
		}
	}
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
		var p struct {
			BotID string `json:"bot_id"`
		}
		_ = json.Unmarshal(params, &p)
		return n.status(ctx, p.BotID), nil
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
	case "set_scope":
		var p struct {
			BotID string `json:"bot_id"`
			Wide  bool   `json:"wide"`
		}
		if err := json.Unmarshal(params, &p); err != nil || !safeID.MatchString(p.BotID) {
			return nil, &ext.Error{Code: "invalid", Message: "set_scope: bot_id"}
		}
		n.mu.Lock()
		if n.st.Wide == nil {
			n.st.Wide = map[string]bool{}
		}
		if p.Wide {
			n.st.Wide[p.BotID] = true
		} else {
			delete(n.st.Wide, p.BotID)
		}
		n.save()
		n.mu.Unlock()
		return map[string]any{"wide": p.Wide}, nil
	case "abandon":
		var p struct {
			TodoID string `json:"todo_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: err.Error()}
		}
		n.sendMu.Lock() // not in the middle of its own hand-over
		n.mu.Lock()
		released := n.abandon(p.TodoID)
		n.mu.Unlock()
		n.sendMu.Unlock()
		return map[string]any{"released": released}, nil
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
			h.Todo.Notes = "" // done with: the notes need not stay on this Mac
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
	var ee *ext.Error
	if errors.As(err, &ee) {
		return ee
	}
	code := "orca_error"
	switch {
	case errors.Is(err, errOrcaNotRunning):
		code = "orca_not_running"
	case errors.Is(err, errOrcaIncompatible):
		code = "orca_incompatible"
	case errors.Is(err, context.DeadlineExceeded):
		code = "orca_timeout"
	case orcaCode(err) == "bot_not_found": // Orca's own code (BOT_ERROR_CODES)
		code = "bot_not_found"
	case orcaCode(err) == "bots_disabled": // Orca's BOTS_DISABLED_CODE
		code = "bots_disabled"
	}
	return &ext.Error{Code: code, Message: code + ": " + clip(err.Error(), 300)}
}

func codeErr(code, words string) error { return &ext.Error{Code: code, Message: code + ": " + words} }

// orcaDown: Orca is away or slow, not refusing (worth trying again).
func orcaDown(err error) bool {
	return errors.Is(err, errOrcaNotRunning) || errors.Is(err, context.DeadlineExceeded)
}

type nodeStatus struct {
	OrcaRunning bool   `json:"orca_running"`
	OrcaVersion string `json:"orca_version,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
	Error       string `json:"error,omitempty"`
	Link        string `json:"link,omitempty"`
	Socket      string `json:"socket,omitempty"`
	SocketError string `json:"socket_error,omitempty"`
	// Token and Wide are the MCP command's for the bot asked about.
	Token     string `json:"token,omitempty"`
	Wide      bool   `json:"wide,omitempty"`
	Following int    `json:"following"`
	PollError string `json:"poll_error,omitempty"`
}

// tokenFor is the bot's MCP token, made on first use; the caller holds n.mu.
func (n *nodeApp) tokenFor(bot string) string {
	if n.st.Tokens == nil {
		n.st.Tokens = map[string]string{}
	}
	if t := n.st.Tokens[bot]; t != "" {
		return t
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	t := hex.EncodeToString(b[:])
	n.st.Tokens[bot] = t
	n.save()
	return t
}

func (n *nodeApp) status(ctx context.Context, bot string) nodeStatus {
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
	if safeID.MatchString(bot) {
		s.Token, s.Wide = n.tokenFor(bot), n.st.Wide[bot]
	}
	for _, h := range n.st.Handoffs {
		if h.Sent && !h.Finished {
			s.Following++
		}
	}
	return s
}

func lastTry(h *nodeHandoff) time.Time {
	if h.TriedAt.After(h.CreatedAt) {
		return h.TriedAt
	}
	return h.CreatedAt
}

// claimedBy is the to-do another hand-over holds the chat for, "" when none does. A holder that never sent and was
// last tried longer ago than nodeStaleClaim lets the chat go (the chat is still empty, nothing of it is there): it
// starts from a new chat should it be tried again. The caller holds n.mu.
func (n *nodeApp) claimedBy(sessionID, todoID string) string {
	for _, h := range n.st.Handoffs {
		if h.SessionID != sessionID || h.TodoID == todoID {
			continue
		}
		if !h.Sent && !h.Unsure && n.now().Sub(lastTry(h)) > nodeStaleClaim {
			h.SessionID = ""
			continue
		}
		return h.TodoID
	}
	return ""
}

// abandon forgets a hand-over the hub gave up, when it never sent: its chat is free again. A sent one stays (it is
// followed to its end). The caller holds n.mu.
func (n *nodeApp) abandon(todoID string) bool {
	h := n.st.Handoffs[todoID]
	if h == nil || h.Sent || h.Unsure { // an unsure one may be in its chat: the chat stays claimed
		return false
	}
	delete(n.st.Handoffs, todoID)
	n.save()
	return true
}

func (n *nodeApp) handoff(ctx context.Context, p handoffParams) (*handoffResult, error) {
	if !safeID.MatchString(p.TodoID) || !safeID.MatchString(p.BotID) || !orcaOpIDPattern.MatchString(p.OpID) {
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
	defer func() { // the claim's age counts from the end of the attempt, however long it ran
		n.mu.Lock()
		h.TriedAt = n.now()
		n.save()
		n.mu.Unlock()
	}()
	sessionID, opID := h.SessionID, h.OpID
	if sessionID != "" && n.claimedBy(sessionID, p.TodoID) != "" {
		// Never expected (a chat is claimed once), but never send two to-dos into one chat.
		h.SessionID, sessionID = "", ""
		n.save()
	}
	n.mu.Unlock()

	if sessionID == "" {
		// A paused bot takes no work: try again later rather than open a chat it will not answer.
		bots, _, err := n.orca.Bots(ctx)
		if err != nil {
			return nil, nodeErr(err)
		}
		var bot *orcaBot
		for i := range bots {
			if bots[i].ID == p.BotID {
				bot = &bots[i]
			}
		}
		switch {
		case bot == nil:
			return nil, codeErr("bot_not_found", "the bot is not in bots.list")
		case bot.Paused:
			return nil, codeErr("bot_paused", "the bot is paused in Orca")
		}
		sid, err := n.orca.StartSession(ctx, p.BotID)
		if err != nil {
			return nil, nodeErr(err)
		}
		if !safeID.MatchString(sid) {
			return nil, nodeErr(fmt.Errorf("%w: bots.session.start answered an odd session id", errOrcaIncompatible))
		}
		n.mu.Lock()
		// Orca answers an empty chat already open instead of a new one, and a chat stays empty after a send until
		// its turn opens: the chat of another hand-over is refused, and this one is tried again later.
		if other := n.claimedBy(sid, p.TodoID); other != "" {
			n.mu.Unlock()
			return nil, codeErr("orca_busy", "Orca offered a chat another to-do was just sent to")
		}
		h.SessionID, sessionID = sid, sid
		n.save()
		n.mu.Unlock()
	}
	// The fence names the history's checkpoint; a chat with no history yet has none (Orca's history refuses it), and
	// hosts since 1.4.246 ignore it, so 1 stands in unless Orca is away.
	fence := int64(1)
	if head, err := n.orca.Head(ctx, sessionID); err == nil && head.Fence > 0 {
		fence = head.Fence
	} else if err != nil && (orcaDown(err) || errors.Is(err, errOrcaIncompatible)) {
		return nil, nodeErr(err)
	}
	// The same operation id every attempt: should an earlier attempt have reached Orca, this one replays it.
	if err := n.orca.Send(ctx, sessionID, fence, opID, p.Text); err != nil {
		n.mu.Lock()
		h.Unsure = h.Unsure || orcaUnsure(err)
		n.save()
		n.mu.Unlock()
		return nil, nodeErr(err)
	}
	n.mu.Lock()
	h.Sent, h.SentAt, h.Unsure = true, n.now(), false
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
	// State: completed | interrupted | unverifiable | unavailable | timeout; Outcome: Orca's verdict of a completed
	// turn (success, failure, cancellation, superseded), "" when unknown.
	State   string `json:"state"`
	Outcome string `json:"outcome,omitempty"`
}

type needsYouEvent struct {
	TodoID    string `json:"todo_id"`
	SessionID string `json:"session_id"`
	BotID     string `json:"bot_id"`
	Episode   int    `json:"episode"`
}

func (h *nodeHandoff) finishedEvent() finishedEvent {
	return finishedEvent{h.TodoID, h.SessionID, h.BotID, h.TurnState, h.Outcome}
}

// pollOnce looks once at every chat still followed, and tells the hub again what it has not acknowledged.
func (n *nodeApp) pollOnce(ctx context.Context) {
	type tell struct {
		event  string
		params any
	}
	var tells []tell
	n.mu.Lock()
	var follow []*nodeHandoff
	now := n.now()
	for _, h := range n.st.Handoffs {
		switch {
		case h.Sent && !h.Finished:
			follow = append(follow, h)
		case h.Finished && !h.Acked && now.Sub(h.FinishedAt) > nodeTellFor:
			h.Acked, h.Todo.Notes = true, "" // told for a day without an answer: given up, the notes not kept
			n.save()
		case h.Finished && !h.Acked && now.Sub(h.EmittedAt) >= nodeReemit:
			h.EmittedAt = now
			tells = append(tells, tell{"orcabot.finished", h.finishedEvent()})
		}
		for i := range h.Writes {
			w := &h.Writes[i]
			if w.Status == "pending" && now.Sub(w.At) > nodeTellFor { // a day without an answer (the hub moved on)
				w.Status, w.Error, w.Text = "refused", "Persona 一天内没有收下", ""
				n.save()
				continue
			}
			if w.Status == "pending" && w.Text != "" && now.Sub(w.EmittedAt) >= nodeReemit {
				w.EmittedAt = now
				tells = append(tells, tell{"orcabot.writeback", writebackParams(h, w)})
			}
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
		sid := h.SessionID
		n.mu.Unlock()
		cctx, cancel := context.WithTimeout(ctx, orcaHistoryTimeout)
		head, err := n.orca.Head(cctx, sid)
		cancel()

		n.mu.Lock()
		now := n.now()
		state, outcome := "", ""
		changed := false
		switch {
		case err != nil && orcaDown(err):
			pollErr = clip(err.Error(), 200)
		case err != nil:
			// Orca answers but refuses this chat's history (a chat deleted in Orca has no code of its own): after a
			// while it is gone.
			pollErr = clip(err.Error(), 200)
			if h.FailingSince.IsZero() {
				h.FailingSince, changed = now, true
			} else if now.Sub(h.FailingSince) >= nodeLostAfter {
				state = "unavailable"
			}
		default:
			if !h.FailingSince.IsZero() {
				h.FailingSince, changed = time.Time{}, true
			}
			if head.Working != nil && *head.Working && !h.SawWorking {
				h.SawWorking, changed = true, true
			}
			if ev, ok := n.prompts(h, head.Prompts, now); ok {
				tells = append(tells, tell{"orcabot.needs_you", ev})
				changed = true
			}
			state, outcome = turnEnd(head, h)
		}
		if state == "" && now.Sub(h.SentAt) > nodeWatchFor {
			state = "timeout"
		}
		if state != "" {
			h.Finished, h.FinishedAt, h.TurnState, h.Outcome, h.EmittedAt = true, now, state, outcome, now
			tells = append(tells, tell{"orcabot.finished", h.finishedEvent()})
			changed = true
		}
		if changed {
			n.save()
		}
		n.mu.Unlock()
	}
	n.mu.Lock()
	n.lastPoll = pollErr
	n.mu.Unlock()
	for _, t := range tells {
		if emit != nil {
			_ = emit(t.event, t.params)
		}
	}
}

// prompts keeps count of the approvals and questions the bot waits on: two polls in a row with one open a wait (told
// as needs-you, again every nodeReemit while it lasts, the hub telling the owner once); none closes it. The caller
// holds n.mu.
func (n *nodeApp) prompts(h *nodeHandoff, waiting int, now time.Time) (needsYouEvent, bool) {
	if waiting <= 0 {
		h.PromptPolls, h.PromptOpen = 0, false
		return needsYouEvent{}, false
	}
	h.PromptPolls++
	switch {
	case !h.PromptOpen && h.PromptPolls >= 2:
		h.PromptOpen, h.PromptEpisode, h.PromptEmittedAt = true, h.PromptEpisode+1, now
	case h.PromptOpen && now.Sub(h.PromptEmittedAt) >= nodeReemit:
		h.PromptEmittedAt = now
	default:
		return needsYouEvent{}, false
	}
	return needsYouEvent{h.TodoID, h.SessionID, h.BotID, h.PromptEpisode}, true
}

// turnEnd says how the turn opened by the hand-over's send ended ("" while it has not), and Orca's verdict of it.
// Only a turn Orca shows, that is final and that belongs to the send (its user item is the send's operation, or it
// began no earlier than the send less a small skew) ends it: no turn yet, one Orca hides after a restart, or an older
// turn keeps waiting (until the chat is gone or the day is over).
func turnEnd(head *orcaHead, h *nodeHandoff) (string, string) {
	if head.Working != nil && *head.Working {
		return "", ""
	}
	t := head.Turn
	if t == nil {
		return "", ""
	}
	// Orca keys the user item of a send as "orca:" + its clientOperationId (agentJournalSubmissionKey).
	ours := (t.UserItemID != "" && t.UserItemID == "orca:"+h.OpID) || t.since(h.SentAt.Add(-nodeTurnSkew).UnixMilli())
	if !ours {
		return "", ""
	}
	switch t.State {
	case "running":
		if head.Working != nil { // not working, yet the turn says running: it died
			return "interrupted", ""
		}
		return "", "" // an older host that does not say whether it works: still running
	case "completed", "interrupted", "unverifiable":
		return t.State, t.Outcome
	}
	return "", ""
}

// --- the MCP command's socket ---

// mcpRequest is one request of the MCP command: the bot it was registered for and its token, the chat it runs in
// when Orca says (ORCA_AGENT_SESSION_ID), and the tool's arguments.
type mcpRequest struct {
	Method  string `json:"method"` // list | get | write
	Bot     string `json:"bot"`
	Token   string `json:"token"`
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
		line, err := readLine(r, mcpMaxLine) // never more than mcpMaxLine of one request in memory
		if err != nil && len(line) == 0 {
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

const (
	notHanded = "这条待办不是交给这个机器人的这个对话的，读写不了。只有 Persona 交给这个机器人的待办可以读写。"
	noChat    = "Orca 没有告诉这个工具它在哪个对话里运行，所以不能读写待办。需要的话，主人可以在 Persona 里本扩展的页面上放宽这个限制。"
	badToken  = "MCP 命令的 --bot 或 --token 不对：请按 Persona 里本扩展页面上的命令重新登记。"
)

// lookup finds the to-do handed to bot (and, when the chat is known, to that chat); the caller holds n.mu.
func (n *nodeApp) lookup(bot, session, todoID string) *nodeHandoff {
	h := n.st.Handoffs[todoID]
	if h == nil || !h.Sent || h.BotID != bot || (session != "" && h.SessionID != session) {
		return nil
	}
	return h
}

// authorized: the request carries the bot's token; the caller holds n.mu.
func (n *nodeApp) authorized(req mcpRequest) bool {
	want := n.st.Tokens[req.Bot]
	return want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(req.Token)) == 1
}

func (n *nodeApp) mcp(ctx context.Context, req mcpRequest) mcpReply {
	if !safeID.MatchString(req.Bot) || (req.Session != "" && !safeID.MatchString(req.Session)) {
		return mcpReply{Error: badToken}
	}
	n.mu.Lock()
	ok, wide := n.authorized(req), n.st.Wide[req.Bot]
	n.mu.Unlock()
	if !ok {
		return mcpReply{Error: badToken}
	}
	// A tool process several chats share carries no chat (Orca leaves the caller out on purpose): without one, it
	// reaches nothing, unless the owner allowed it on the page.
	if req.Session == "" && !wide {
		return mcpReply{Error: noChat}
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
		for i, w := range h.Writes {
			w.Text = ""
			writes[i] = w
		}
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

func writebackParams(h *nodeHandoff, w *nodeWrite) map[string]any {
	return map[string]any{"todo_id": h.TodoID, "session_id": h.SessionID, "bot_id": h.BotID, "ref": w.Ref, "text": w.Text}
}

func (n *nodeApp) write(ctx context.Context, req mcpRequest) mcpReply {
	text := strings.TrimSpace(cleanText(req.Text))
	if text == "" {
		return mcpReply{Error: "text 是空的"}
	}
	if utf8.RuneCountInString(text) > writeMaxRunes {
		return mcpReply{Error: fmt.Sprintf("text 太长了（%d 字）：请把要点压缩到 %d 字以内再写，细节留在 Orca 对话里", utf8.RuneCountInString(text), writeMaxRunes)}
	}
	n.mu.Lock()
	h := n.lookup(req.Bot, req.Session, req.TodoID)
	if h == nil {
		n.mu.Unlock()
		return mcpReply{Error: notHanded}
	}
	ref := "orca-bot:" + shortHash(24, h.TodoID, h.SessionID, text)
	var w *nodeWrite
	for i := range h.Writes {
		if h.Writes[i].Ref != ref || h.Writes[i].Status == "refused" {
			continue
		}
		if h.Writes[i].Status != "pending" {
			n.mu.Unlock()
			return mcpReply{OK: true, Result: map[string]any{"status": "duplicate", "message": "同样的内容已经写过了。"}}
		}
		w = &h.Writes[i] // still waiting for the hub: tell it again, the same entry
	}
	if w == nil {
		if countWrites(h.Writes) >= writesPerTodo || len(h.Writes) >= 3*writesPerTodo {
			n.mu.Unlock()
			return mcpReply{Error: fmt.Sprintf("这条待办已经写回了 %d 次，到上限了。", writesPerTodo)}
		}
		h.Writes = append(h.Writes, nodeWrite{Ref: ref, At: n.now(), Status: "pending", Text: text})
		w = &h.Writes[len(h.Writes)-1]
	}
	w.EmittedAt = n.now()
	ch := make(chan writeAck, 1)
	n.acks[ref] = append(n.acks[ref], ch) // the same write asked twice at once: both wait for the one answer
	n.save()
	ev := writebackParams(h, w)
	emit, wait := n.emit, n.ackWait
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		left := n.acks[ref][:0]
		for _, c := range n.acks[ref] {
			if c != ch {
				left = append(left, c)
			}
		}
		if len(left) == 0 {
			delete(n.acks, ref)
		} else {
			n.acks[ref] = left
		}
		n.mu.Unlock()
	}()
	if emit != nil {
		_ = emit("orcabot.writeback", ev) // lost while the hub program is away: told again until answered
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
		return mcpReply{OK: true, Result: map[string]any{"status": "pending", "message": "已交给 Persona，会在 Persona 收到后写到这条待办上。"}}
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
				h.Writes[i].Text = ""
				n.save()
			}
		}
	}
	for _, ch := range n.acks[ref] {
		select {
		case ch <- a:
		default:
		}
	}
}
