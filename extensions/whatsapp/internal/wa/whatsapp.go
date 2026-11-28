// Package wa implements the native Persona WhatsApp connector on top of go.mau.fi/whatsmeow
// and pure-Go SQLite (modernc.org/sqlite, CGO_ENABLED=0).
//
// Device credentials and Signal keys are kept purely in memory and flushed encrypted to the hub's
// connector_secrets store (vault.go); only the rebuildable local message/chat/contact search index
// resides in the extension's DataDir.
package wa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/wa/spec"
	"github.com/nwverse/persona-extensions/pluginkit"
)

var (
	errNotConnected = errors.New("未连接到 WhatsApp，请先在「设置 › 连接器 › WhatsApp」登录或等待重连")

	retryFirst   = 5 * time.Second
	retryLast    = 5 * time.Minute
	newsSettle   = 2 * time.Second
	capCheckStep = 200
)

// ParseConfig parses the extension configuration JSON from initialize.
func ParseConfig(raw json.RawMessage) Config {
	cfg := DefaultConfig()
	cfg.TriageRules = pluginkit.TriageRules(raw)
	if len(raw) == 0 {
		return cfg
	}
	var c struct {
		QuietPresence *bool `json:"quiet_presence"`
		MaxStorageMB  *int  `json:"max_storage_mb"`
	}
	if err := json.Unmarshal(raw, &c); err == nil {
		if c.QuietPresence != nil {
			cfg.QuietPresence = *c.QuietPresence
		}
		if c.MaxStorageMB != nil && *c.MaxStorageMB >= 16 && *c.MaxStorageMB <= 1024 {
			cfg.MaxStorageMB = *c.MaxStorageMB
		}
	}
	return cfg
}

// Conn is the WhatsApp Persona connector implementing Accounts, Login, Events, Inbox, Archive,
// Threads, ReadTools, Catalog, Actions, and Runner.
type Conn struct {
	backend Backend
	store   *Store
	cfg     Config
	zone    *time.Location
	now     func() time.Time

	mu        sync.Mutex
	status    connector.Status
	lastSync  time.Time
	lastEvent time.Time
	selfJID   string
	selfLID   string
	ingested  int

	// Login state machine (login.go)
	loginMu sync.Mutex
	pending *pendingLogin

	// Signal channels for Run() and Watch()
	wake      chan struct{}
	newsChats chan struct{}
	newsInbox chan struct{}

	// Pending on-demand backfill waiters by chat JID
	backfillMu sync.Mutex
	backfills  map[string][]chan int

	// Idempotent inbox reply cache by ClientID
	replyMu   sync.Mutex
	sentReply map[string]time.Time

	// In-memory avatar byte cache (bounded)
	avatarMu sync.Mutex
	avatars  map[string]connector.Media
}

// New constructs a production WhatsApp connector backed by whatsmeow, encrypted Hub secrets,
// and a local SQLite index in dataDir.
func New(ctx context.Context, secrets connector.Secrets, dataDir string, zone *time.Location, cfg Config) (*Conn, error) {
	store, err := OpenStore(dataDir)
	if err != nil {
		return nil, err
	}
	backend, err := NewWhatsmeowBackend(ctx, secrets, store)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return NewWithDeps(backend, store, zone, cfg), nil
}

// NewWithDeps constructs a Conn with an explicit Backend and Store (used by tests and New).
func NewWithDeps(backend Backend, store *Store, zone *time.Location, cfg Config) *Conn {
	if cfg.MaxStorageMB <= 0 {
		cfg.MaxStorageMB = DefaultConfig().MaxStorageMB
	}
	c := &Conn{
		backend:   backend,
		store:     store,
		cfg:       cfg,
		zone:      zone,
		now:       time.Now,
		status:    connector.Status{State: connector.StateNotSignedIn, Error: "还没登录 WhatsApp"},
		wake:      make(chan struct{}, 1),
		newsChats: make(chan struct{}, 1),
		newsInbox: make(chan struct{}, 1),
		backfills: map[string][]chan int{},
		sentReply: map[string]time.Time{},
		avatars:   map[string]connector.Media{},
	}
	if backend != nil {
		if acc, jid, lid := backend.AccountInfo(); jid != "" {
			c.selfJID = jid
			c.selfLID = lid
			c.status = connector.Status{
				State:   connector.StateError,
				Account: acc,
				Via:     c.viaLabel(),
				Error:   "正在连接 WhatsApp（使用已保存的加密会话）",
			}
		}
	}
	return c
}

func (*Conn) ID() string                   { return spec.ID }
func (*Conn) Name() string                 { return spec.Name }
func (*Conn) Manifest() connector.Manifest { return spec.Manifest() }

// Status answers from memory (never dials WhatsApp).
func (c *Conn) Status(context.Context) connector.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.status
	st.LastSync = c.lastSync
	st.LastEvent = c.lastEvent
	return st
}

func (c *Conn) setStatus(st connector.Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = st
}

func (c *Conn) viaLabel() string {
	mode := "多设备协议 · 静默在线"
	if !c.cfg.QuietPresence {
		mode = "多设备协议"
	}
	return mode
}

func (c *Conn) kick() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Conn) notifyChats() {
	select {
	case c.newsChats <- struct{}{}:
	default:
	}
}

func (c *Conn) notifyInbox() {
	select {
	case c.newsInbox <- struct{}{}:
	default:
	}
}

// Run maintains the long-lived WhatsApp connection while a paired session exists, with exponential
// backoff reconnects and graceful shutdown cleanup.
func (c *Conn) Run(ctx context.Context) {
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.Close(sctx)
		cancel()
	}()

	wait := retryFirst
	for ctx.Err() == nil {
		if c.backend == nil || !c.backend.HasSession(ctx) {
			c.setStatus(connector.Status{
				State: connector.StateNotSignedIn,
				Error: "还没登录 WhatsApp（支持二维码扫码或手机号 8 位配对码）",
			})
			select {
			case <-ctx.Done():
				return
			case <-c.wake:
			}
			wait = retryFirst
			continue
		}

		acc, jid, lid := c.backend.AccountInfo()
		if jid != "" && c.store != nil {
			_ = c.store.ResetForAccount(jid)
		}
		c.mu.Lock()
		c.selfJID, c.selfLID = jid, lid
		if c.status.State != connector.StateOK {
			c.status = connector.Status{
				State:   connector.StateError,
				Account: acc,
				Via:     c.viaLabel(),
				Error:   "正在连接 WhatsApp…",
			}
		}
		c.mu.Unlock()

		start := c.now()
		connCtx, stopConn := context.WithCancel(ctx)
		// Cancel active connection immediately if wake fires (e.g. Logout or re-login).
		wakeDone := make(chan struct{})
		go func() {
			select {
			case <-c.wake:
				stopConn()
			case <-wakeDone:
			case <-connCtx.Done():
			}
		}()

		err := c.backend.Connect(connCtx, c.cfg.QuietPresence, c.callbacks())
		close(wakeDone)
		stopConn()

		if ctx.Err() != nil {
			return
		}
		if !c.backend.HasSession(ctx) {
			// Logged out remotely or locally.
			wait = retryFirst
			continue
		}
		if c.now().Sub(start) > time.Minute {
			wait = retryFirst
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			accNow, _, _ := c.backend.AccountInfo()
			c.setStatus(connector.Status{
				State:   connector.StateError,
				Account: accNow,
				Via:     c.viaLabel(),
				Error:   fmt.Sprintf("WhatsApp 连接中断：%s，%s 后自动重连", scrubError(err), wait.Round(time.Second)),
			})
			log.Printf("whatsapp connect ended (%s); retrying in %s", scrubError(err), wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			wait = retryFirst
		case <-time.After(wait):
			wait = min(wait*2, retryLast)
		}
	}
}

func (c *Conn) callbacks() BackendCallbacks {
	return BackendCallbacks{
		OnConnected: func(account, jid, lid string) {
			if jid != "" && c.store != nil {
				_ = c.store.ResetForAccount(jid)
				if lid != "" {
					_ = c.store.PutLIDMapping(lid, jid)
				}
			}
			c.mu.Lock()
			c.selfJID = jid
			c.selfLID = lid
			c.lastEvent = c.now()
			c.status = connector.Status{
				State:   connector.StateOK,
				Account: account,
				Via:     c.viaLabel(),
			}
			c.mu.Unlock()
			c.notifyChats()
			c.notifyInbox()
		},
		OnDisconnected: func(err error) {
			c.mu.Lock()
			acc := c.status.Account
			c.status = connector.Status{
				State:   connector.StateError,
				Account: acc,
				Via:     c.viaLabel(),
				Error:   "与 WhatsApp 断开连接，正在自动重连…",
			}
			c.mu.Unlock()
		},
		OnLoggedOut: func(reason string) {
			if c.store != nil {
				_ = c.store.Wipe()
			}
			c.mu.Lock()
			c.selfJID, c.selfLID = "", ""
			c.lastSync = time.Time{}
			c.status = connector.Status{
				State: connector.StateNotSignedIn,
				Error: "WhatsApp 会话已失效（" + reason + "），请重新登录",
			}
			c.mu.Unlock()
		},
		OnStreamReplaced: func() {
			c.mu.Lock()
			acc := c.status.Account
			c.status = connector.Status{
				State:   connector.StateError,
				Account: acc,
				Via:     c.viaLabel(),
				Error:   "检测到同会话在其他实例连接（StreamReplaced），请避免重复运行",
			}
			c.mu.Unlock()
		},
		OnLIDMapping: func(pair LIDPair) {
			if c.store != nil {
				_ = c.store.PutLIDMapping(pair.LID, pair.PN)
			}
		},
		OnContact: func(ct Contact) {
			if c.store != nil {
				_ = c.store.UpsertContact(ct)
			}
		},
		OnGroup: func(jid, name string, members int) {
			if c.store != nil {
				_ = c.store.UpsertChat(Chat{JID: jid, Kind: "group", Name: name, MemberCount: members})
			}
		},
		OnMessage: func(msg Message, chatHint Chat, isLive bool) {
			if c.store == nil {
				return
			}
			inserted, err := c.store.UpsertMessage(msg, chatHint, isLive)
			if err != nil {
				log.Printf("whatsapp store message error: %v", err)
				return
			}
			c.mu.Lock()
			c.lastEvent = c.now()
			c.ingested++
			needCap := c.ingested%capCheckStep == 0
			c.mu.Unlock()
			if needCap {
				c.enforceCap(context.Background())
			}
			if inserted {
				c.notifyChats()
			}
			if isLive {
				c.notifyInbox()
			}
		},
		OnReaction: func(chatJID, targetID, senderJID, emoji string) {
			if c.store != nil {
				_ = c.store.ApplyReaction(chatJID, targetID, senderJID, emoji)
			}
		},
		OnEdit: func(chatJID, targetID, newText string) {
			if c.store != nil {
				_ = c.store.ApplyEdit(chatJID, targetID, newText)
				c.notifyChats()
				c.notifyInbox()
			}
		},
		OnRevoke: func(chatJID, targetID string) {
			if c.store != nil {
				_ = c.store.ApplyRevoke(chatJID, targetID)
				c.notifyChats()
				c.notifyInbox()
			}
		},
		OnChatRead: func(chatJID string, read bool) {
			if c.store != nil {
				_ = c.store.SetChatRead(chatJID, read)
				c.notifyInbox()
			}
		},
		OnChatMute: func(chatJID string, mutedUntil int64) {
			if c.store != nil {
				_ = c.store.SetChatMute(chatJID, mutedUntil)
				c.notifyInbox()
			}
		},
		OnChatArchive: func(chatJID string, archived bool) {
			if c.store != nil {
				_ = c.store.SetChatArchive(chatJID, archived)
				c.notifyInbox()
			}
		},
		OnHistoryBatch: func(chatJID string, onDemand bool, count int) {
			if count > 0 {
				c.enforceCap(context.Background())
				c.notifyChats()
				c.notifyInbox()
			}
			if onDemand {
				c.signalBackfill(chatJID, count)
			}
		},
	}
}

func (c *Conn) enforceCap(ctx context.Context) {
	if c.store == nil || c.cfg.MaxStorageMB <= 0 {
		return
	}
	maxBytes := int64(c.cfg.MaxStorageMB) << 20
	if _, err := c.store.EnforceStorageCap(ctx, maxBytes, defaultMaxRows); err != nil {
		log.Printf("whatsapp storage cap check: %v", err)
	}
}

func (c *Conn) registerBackfillWaiter(chatJID string) (chan int, func()) {
	ch := make(chan int, 4)
	c.backfillMu.Lock()
	c.backfills[chatJID] = append(c.backfills[chatJID], ch)
	c.backfillMu.Unlock()
	cleanup := func() {
		c.backfillMu.Lock()
		list := c.backfills[chatJID]
		for i, w := range list {
			if w == ch {
				list = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(list) == 0 {
			delete(c.backfills, chatJID)
		} else {
			c.backfills[chatJID] = list
		}
		c.backfillMu.Unlock()
	}
	return ch, cleanup
}

func (c *Conn) signalBackfill(chatJID string, count int) {
	c.backfillMu.Lock()
	waiters := append([]chan int(nil), c.backfills[chatJID]...)
	c.backfillMu.Unlock()
	for _, ch := range waiters {
		select {
		case ch <- count:
		default:
		}
	}
}

// Watch batches live WhatsApp changes and notifies the hub for archive (ScopeChats) and inbox (ScopeInbox).
func (c *Conn) Watch(ctx context.Context, changed func(connector.Scope)) error {
	for {
		var scopeChats, scopeInbox bool
		select {
		case <-ctx.Done():
			return nil
		case <-c.newsChats:
			scopeChats = true
		case <-c.newsInbox:
			scopeInbox = true
		}

		// Settle bursty events into one hub notification.
		timer := time.NewTimer(newsSettle)
	settle:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-c.newsChats:
				scopeChats = true
			case <-c.newsInbox:
				scopeInbox = true
			case <-timer.C:
				break settle
			}
		}
		if scopeChats {
			changed(connector.ScopeChats)
		}
		if scopeInbox {
			changed(connector.ScopeInbox)
		}
	}
}

// Close cancels pending logins, flushes encrypted credentials, and closes the local index.
func (c *Conn) Close(ctx context.Context) error {
	c.cancelPendingLogin()
	var errs []error
	if c.backend != nil {
		if err := c.backend.Close(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if c.store != nil {
		if err := c.store.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Conn) when(t time.Time) string {
	if c.zone == nil {
		return t.UTC().Format("2006-01-02 15:04 UTC")
	}
	return t.In(c.zone).Format("2006-01-02 15:04")
}

func (c *Conn) stamp(t time.Time) string {
	if c.zone == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return t.In(c.zone).Format(time.RFC3339)
}

func scrubError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 240 {
		msg = msg[:240] + "…"
	}
	return msg
}
