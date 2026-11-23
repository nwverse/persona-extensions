package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The client of the Orca desktop app's runtime, over its local unix socket only (never the paired WebSocket or Orca
// Cloud). One JSON request per line {id, authToken, method, params}, answered by one JSON line
// {id, ok, result | error{code, message}}; slow calls send {"_keepalive": true} lines first. The socket and its token
// are in orca-runtime.json in Orca's data directory, and the token changes each time Orca starts, so the file is read
// again whenever a call cannot connect or is refused as unauthorized. Streaming methods are refused on this socket,
// so the end of a bot's turn is polled with agentSession.history.
//
// The wire shapes follow Persona core's client (server/internal/orcart) and Orca's own sources
// (src/main/runtime/rpc/methods/bots.ts, structured-sessions/structured-agent-session.ts,
// src/shared/rpc-contract/bots-params.ts). The protocol is not documented by Orca: status.get is checked first and
// anything unexpected fails closed.

const (
	// orcaProtocol is the runtime protocol this client speaks; a runtime that serves only newer clients, or speaks
	// an older protocol than orcaMinServer, is incompatible.
	orcaProtocol  = 3
	orcaMinServer = 3

	orcaDialTimeout    = time.Second
	orcaCallTimeout    = 5 * time.Second
	orcaHistoryTimeout = 10 * time.Second
	orcaWriteTimeout   = 30 * time.Second
	orcaMaxLine        = 16 << 20
	orcaCompatRecheck  = 5 * time.Minute
)

var (
	// errOrcaNotRunning: no metadata file, no unix transport in it, or its socket is gone or refuses.
	errOrcaNotRunning = errors.New("Orca is not running on this Mac")
	// errOrcaIncompatible: the running Orca speaks a runtime protocol this program does not.
	errOrcaIncompatible = errors.New("Orca's runtime protocol is not compatible")
)

// orcaError is the runtime's own error answer, by its stable code (unauthorized, method_not_found, bot_not_found…).
type orcaError struct {
	Method, Code, Message string
}

func (e *orcaError) Error() string {
	if e.Message != "" && e.Message != e.Code {
		return "orca " + e.Method + ": " + e.Code + ": " + e.Message
	}
	return "orca " + e.Method + ": " + e.Code
}

func orcaCode(err error) string {
	var e *orcaError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// orcaMetaPath is the desktop app's orca-runtime.json ($ORCA_USER_DATA_PATH's when set, Orca's own override).
func orcaMetaPath() string {
	if d := os.Getenv("ORCA_USER_DATA_PATH"); d != "" {
		return filepath.Join(d, "orca-runtime.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "orca", "orca-runtime.json")
}

type orcaMeta struct {
	socket, token, runtimeID string
}

type orcaCompat struct {
	runtimeID string
	at        time.Time
	version   string
	err       error
}

// orcaClient is one Orca runtime, found through its metadata file.
type orcaClient struct {
	metaPath func() string
	now      func() time.Time

	seq    atomic.Int64
	mu     sync.Mutex
	meta   *orcaMeta
	compat *orcaCompat
}

func newOrcaClient(metaPath func() string) *orcaClient {
	if metaPath == nil {
		metaPath = orcaMetaPath
	}
	return &orcaClient{metaPath: metaPath, now: time.Now}
}

func (c *orcaClient) readMeta() (*orcaMeta, error) {
	path := c.metaPath()
	if path == "" {
		return nil, errOrcaNotRunning
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w (%s not readable)", errOrcaNotRunning, filepath.Base(path))
	}
	var f struct {
		RuntimeID  string `json:"runtimeId"`
		AuthToken  string `json:"authToken"`
		Transports []struct {
			Kind     string `json:"kind"`
			Endpoint string `json:"endpoint"`
		} `json:"transports"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%w (%s is not valid JSON)", errOrcaNotRunning, filepath.Base(path))
	}
	m := &orcaMeta{token: f.AuthToken, runtimeID: f.RuntimeID}
	for _, t := range f.Transports {
		if t.Kind == "unix" && t.Endpoint != "" { // never the websocket transport: it needs pairing
			m.socket = t.Endpoint
			break
		}
	}
	if m.socket == "" || m.token == "" {
		return nil, fmt.Errorf("%w (no unix transport in %s)", errOrcaNotRunning, filepath.Base(path))
	}
	return m, nil
}

func (c *orcaClient) currentMeta() (*orcaMeta, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.meta != nil {
		return c.meta, nil
	}
	m, err := c.readMeta()
	if err != nil {
		return nil, err
	}
	c.meta = m
	return m, nil
}

// forget drops m: the next call reads the metadata file again (Orca restarted with a new socket or token).
func (c *orcaClient) forget(m *orcaMeta) {
	c.mu.Lock()
	if c.meta == m {
		c.meta = nil
	}
	c.mu.Unlock()
}

func orcaRetryable(err error) bool {
	return orcaCode(err) == "unauthorized" || errors.Is(err, errOrcaNotRunning)
}

// call runs one method: the runtime's compatibility is checked first (once per runtime); a call that cannot connect
// or is refused as unauthorized reads the metadata file again and is tried once more.
func (c *orcaClient) call(ctx context.Context, timeout time.Duration, method string, params, out any) error {
	for attempt := 0; ; attempt++ {
		m, err := c.currentMeta()
		if err != nil {
			return err
		}
		err = c.checkCompat(ctx, m)
		if err == nil {
			err = c.callOnce(ctx, m, timeout, method, params, out)
		}
		if err != nil && orcaRetryable(err) && attempt == 0 {
			c.forget(m)
			continue
		}
		return err
	}
}

// orcaStatus is the part of status.get this program reads.
type orcaStatus struct {
	AppVersion                        string `json:"appVersion"`
	RuntimeProtocolVersion            int    `json:"runtimeProtocolVersion"`
	MinCompatibleRuntimeClientVersion int    `json:"minCompatibleRuntimeClientVersion"`
	ProtocolVersion                   int    `json:"protocolVersion"`
}

func (s orcaStatus) compatible() error {
	v := s.RuntimeProtocolVersion
	if v == 0 {
		v = s.ProtocolVersion
	}
	switch {
	case v < orcaMinServer:
		return fmt.Errorf("%w: Orca %s speaks runtime protocol %d, this extension needs %d or later", errOrcaIncompatible,
			s.AppVersion, v, orcaMinServer)
	case s.MinCompatibleRuntimeClientVersion > orcaProtocol:
		return fmt.Errorf("%w: Orca %s needs runtime clients of protocol %d or later, this extension speaks %d",
			errOrcaIncompatible, s.AppVersion, s.MinCompatibleRuntimeClientVersion, orcaProtocol)
	}
	return nil
}

func (c *orcaClient) checkCompat(ctx context.Context, m *orcaMeta) error {
	c.mu.Lock()
	cp := c.compat
	c.mu.Unlock()
	if cp != nil && cp.runtimeID == m.runtimeID && c.now().Sub(cp.at) < orcaCompatRecheck {
		return cp.err
	}
	var st orcaStatus
	if err := c.callOnce(ctx, m, orcaCallTimeout, "status.get", nil, &st); err != nil {
		return err
	}
	cp = &orcaCompat{runtimeID: m.runtimeID, at: c.now(), version: st.AppVersion, err: st.compatible()}
	c.mu.Lock()
	c.compat = cp
	c.mu.Unlock()
	return cp.err
}

// Status checks that Orca runs and speaks a compatible protocol, and says its version.
func (c *orcaClient) Status(ctx context.Context) (string, error) {
	if err := c.call(ctx, orcaCallTimeout, "status.get", nil, nil); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.compat == nil {
		return "", nil
	}
	return c.compat.version, c.compat.err
}

func (c *orcaClient) callOnce(ctx context.Context, m *orcaMeta, timeout time.Duration, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dialOrca(ctx, m.socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	id := "ob" + strconv.FormatInt(c.seq.Add(1), 10)
	if params == nil {
		params = struct{}{}
	}
	req, err := json.Marshal(map[string]any{"id": id, "authToken": m.token, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return fmt.Errorf("orca %s: write: %w", method, err)
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	for {
		line, err := readOrcaLine(r)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("orca %s: %w", method, context.DeadlineExceeded)
			}
			return fmt.Errorf("orca %s: read: %w", method, err)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env struct {
			ID        json.RawMessage `json:"id"`
			OK        bool            `json:"ok"`
			Keepalive bool            `json:"_keepalive"`
			Result    json.RawMessage `json:"result"`
			Error     *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Meta struct {
				RuntimeID string `json:"runtimeId"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return fmt.Errorf("%w: orca %s answered something that is not JSON", errOrcaIncompatible, method)
		}
		if env.Keepalive {
			continue
		}
		var gotID string
		_ = json.Unmarshal(env.ID, &gotID)
		// A request Orca could not read is answered under the id "unknown": it is this one's.
		if gotID != id && !(gotID == "unknown" && !env.OK) {
			continue
		}
		if r := env.Meta.RuntimeID; r != "" && m.runtimeID != "" && r != m.runtimeID {
			c.forget(m) // another runtime answered than the file names: read the file again next time
		}
		if !env.OK {
			e := &orcaError{Method: method, Code: "error"}
			if env.Error != nil {
				e.Code, e.Message = env.Error.Code, clip(env.Error.Message, 300)
			}
			if e.Code == "method_not_found" {
				return fmt.Errorf("%w: %v", errOrcaIncompatible, e)
			}
			return e
		}
		if out == nil || len(env.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("%w: orca %s answered an unexpected shape", errOrcaIncompatible, method)
		}
		return nil
	}
}

func dialOrca(ctx context.Context, socket string) (net.Conn, error) {
	if _, err := os.Stat(socket); err != nil {
		return nil, fmt.Errorf("%w (its socket is gone)", errOrcaNotRunning)
	}
	dctx, cancel := context.WithTimeout(ctx, orcaDialTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dctx, "unix", socket)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("%w (its socket refuses)", errOrcaNotRunning)
		}
		return nil, fmt.Errorf("orca: connect: %w", err)
	}
	return conn, nil
}

func readOrcaLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > orcaMaxLine {
			return nil, fmt.Errorf("line over %d bytes", orcaMaxLine)
		}
		switch {
		case err == nil:
			return bytes.TrimRight(buf, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case len(buf) > 0 && errors.Is(err, io.EOF):
			return buf, nil
		default:
			return nil, err
		}
	}
}

// orcaBot is the part of a bot of bots.list this program shows.
type orcaBot struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Agent  string `json:"agent,omitempty"`
	Paused bool   `json:"paused,omitempty"`
}

// Bots is bots.list: the bots of this Orca, and whether bots are turned on in it.
func (c *orcaClient) Bots(ctx context.Context) ([]orcaBot, bool, error) {
	var res struct {
		Bots        []orcaBot `json:"bots"`
		BotsEnabled *bool     `json:"botsEnabled"`
	}
	if err := c.call(ctx, orcaCallTimeout, "bots.list", nil, &res); err != nil {
		return nil, false, err
	}
	enabled := res.BotsEnabled == nil || *res.BotsEnabled
	bots := res.Bots[:0]
	for _, b := range res.Bots {
		if b.ID != "" {
			bots = append(bots, b)
		}
	}
	return bots, enabled, nil
}

// StartSession is bots.session.start: a new chat of the bot beside its Chat (Orca reuses an empty one already open).
func (c *orcaClient) StartSession(ctx context.Context, botID string) (string, error) {
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, orcaWriteTimeout, "bots.session.start", map[string]any{"botId": botID}, &res); err != nil {
		return "", err
	}
	if res.SessionID == "" {
		return "", fmt.Errorf("%w: bots.session.start answered without a session", errOrcaIncompatible)
	}
	return res.SessionID, nil
}

// orcaHead is what the newest history page says of a session.
type orcaHead struct {
	Fence int64
	// Working is nil from a host that does not say; true while a turn runs or a send is still unanswered.
	Working *bool
	Prompts int
	// TurnState is the latest turn's lifecycle (running, completed, interrupted, unverifiable), "" when unknown;
	// TurnAt when that turn record was made (ms since the epoch).
	TurnState string
	TurnAt    int64
}

// Head reads the newest page of a session's history (the smallest one Orca gives).
func (c *orcaClient) Head(ctx context.Context, sessionID string) (*orcaHead, error) {
	var res struct {
		Page *struct {
			Fence               int64    `json:"fence"`
			Working             *bool    `json:"working"`
			ActionablePromptIDs []string `json:"actionablePromptIds"`
			LatestTurn          *struct {
				ObservedAt int64 `json:"observedAt"`
				Turn       struct {
					State string `json:"state"`
				} `json:"turn"`
			} `json:"latestTurn"`
		} `json:"page"`
	}
	err := c.call(ctx, orcaHistoryTimeout, "agentSession.history",
		map[string]any{"sessionId": sessionID, "direction": "tail", "limit": 1}, &res)
	if err != nil {
		return nil, err
	}
	if res.Page == nil {
		return nil, fmt.Errorf("%w: agentSession.history answered without a page", errOrcaIncompatible)
	}
	h := &orcaHead{Fence: res.Page.Fence, Working: res.Page.Working, Prompts: len(res.Page.ActionablePromptIDs)}
	if lt := res.Page.LatestTurn; lt != nil {
		h.TurnState, h.TurnAt = lt.Turn.State, lt.ObservedAt
	}
	return h, nil
}

// orcaOperationID is a write's clientOperationId, Orca's "<13-digit ms>-<32 hex>": the hex comes from key, so the same
// key and time replay instead of sending twice.
func orcaOperationID(at time.Time, key string) string {
	var hexPart string
	if key != "" {
		sum := sha256.Sum256([]byte(key))
		hexPart = hex.EncodeToString(sum[:16])
	} else {
		var b [16]byte
		_, _ = rand.Read(b[:])
		hexPart = hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%013d-%s", at.UnixMilli(), hexPart)
}

// Send is agentSession.send: text as the user's next message of the session, queued behind a running turn. The
// envelope names the operation (opID: replaying it with the same text answers as the first time) and the
// fingerprint of the payload; fence is the history's.
func (c *orcaClient) Send(ctx context.Context, sessionID string, fence int64, opID, text string) error {
	text = strings.TrimRight(text, " \t\r\n")
	if text == "" {
		return errors.New("nothing to send")
	}
	fields := map[string]any{
		"body":     map[string]any{"kind": "message", "role": "user", "blocks": []any{map[string]any{"type": "text", "text": text}}},
		"delivery": "queue-if-active",
	}
	fp, err := orcaFingerprint("agentSession.send", sessionID, fields)
	if err != nil {
		return err
	}
	if fence <= 0 {
		fence = 1 // newer hosts ignore it; a positive number passes the older ones' schema
	}
	params := map[string]any{"envelope": map[string]any{"sessionId": sessionID, "clientOperationId": opID,
		"expectedRuntimeFence": fence, "payloadFingerprint": fp}}
	for k, v := range fields {
		params[k] = v
	}
	var res struct {
		OK      *bool `json:"ok"`
		Refusal *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"refusal"`
	}
	if err := c.call(ctx, orcaWriteTimeout, "agentSession.send", params, &res); err != nil {
		return err
	}
	if res.OK == nil {
		return fmt.Errorf("%w: agentSession.send answered without ok", errOrcaIncompatible)
	}
	if !*res.OK {
		e := &orcaError{Method: "agentSession.send", Code: "agent_session_refused"}
		if res.Refusal != nil {
			e.Code, e.Message = res.Refusal.Code, clip(res.Refusal.Message, 300)
		}
		return e
	}
	return nil
}

// orcaFingerprint is a write's payloadFingerprint: the SHA-256 of the canonical JSON of {method, sessionId, fields}
// (keys sorted, no spaces, strings escaped as JavaScript's JSON.stringify does), in lowercase hex.
func orcaFingerprint(method, sessionID string, fields any) (string, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var f any
	if err := d.Decode(&f); err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := canonJSON(&b, map[string]any{"method": method, "sessionId": sessionID, "fields": f}); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func canonJSON(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		b.WriteString(x.String())
	case string:
		jsString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canonJSON(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys) // the keys are ASCII: byte order is JavaScript's UTF-16 order
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			jsString(b, k)
			b.WriteByte(':')
			if err := canonJSON(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("orca fingerprint: unexpected %T", v)
	}
	return nil
}

// jsString writes s as JSON.stringify does: Go's escaping without the HTML escapes and with U+2028 / U+2029 raw.
func jsString(b *bytes.Buffer, s string) {
	var e bytes.Buffer
	enc := json.NewEncoder(&e)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := bytes.TrimSuffix(e.Bytes(), []byte("\n"))
	out = bytes.ReplaceAll(out, []byte(` `), []byte(" "))
	out = bytes.ReplaceAll(out, []byte(` `), []byte(" "))
	b.Write(out)
}
