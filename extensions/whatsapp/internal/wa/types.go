package wa

import (
	"context"
	"time"
)

// Config holds connector configuration parsed from initialize's config JSON.
type Config struct {
	TriageRules   string
	QuietPresence bool
	MaxStorageMB  int
}

// DefaultConfig returns safe defaults (quiet presence ON, 128 MB local index cap).
func DefaultConfig() Config {
	return Config{
		QuietPresence: true,
		MaxStorageMB:  128,
	}
}

// Chat represents one WhatsApp conversation (DM or group) in the local index.
type Chat struct {
	JID           string
	Kind          string // "dm" | "group"
	Name          string
	Phone         string // E.164 digits without '+' when DM has a phone JID
	UnreadCount   int
	MentionCount  int
	MutedUntil    int64 // Unix seconds (0 = not muted, <0 = muted forever)
	Archived      bool
	LastMsgID     string
	LastMsgTS     time.Time
	LastMsgText   string
	LastMsgFrom   string
	LastMsgMine   bool
	LastMentionID string
	LastMentionTS time.Time
	AvatarID      string
	AvatarURL     string
	MemberCount   int
	UpdatedAt     time.Time
}

// IsMuted reports whether the chat is muted at now.
func (c Chat) IsMuted(now time.Time) bool {
	if c.MutedUntil < 0 {
		return true
	}
	return c.MutedUntil > now.Unix()
}

// Contact represents a synced WhatsApp contact with optional LID mapping.
type Contact struct {
	JID          string
	LID          string
	Phone        string
	PushName     string
	FullName     string
	FirstName    string
	BusinessName string
	UpdatedAt    time.Time
}

// DisplayName returns the best human-readable name for the contact.
func (c Contact) DisplayName() string {
	saved := c.FullName
	if saved == "" {
		saved = c.FirstName
	}
	return BestDisplayName(saved, c.BusinessName, c.PushName, c.JID)
}

// MediaMeta holds lightweight metadata for an attachment on a message.
type MediaMeta struct {
	Kind     string `json:"kind,omitempty"` // image | sticker | video | voice | file
	Mime     string `json:"mime,omitempty"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Duration int    `json:"duration,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

// Message represents one indexed WhatsApp message.
type Message struct {
	Seq           int64
	ChatJID       string
	ID            string
	SenderJID     string
	SenderName    string
	Mine          bool
	Type          string // TEXT, IMAGE, VIDEO, AUDIO, VOICE, FILE, STICKER, LOCATION, CONTACT, SYSTEM
	Text          string
	At            time.Time
	ReplyToID     string
	ReplyToSender string
	ReplyToText   string
	MentionedMe   bool
	Edited        bool
	Reactions     map[string]string // senderJID -> emoji
	Media         *MediaMeta
}

// SearchHit pairs a matched message with its chat metadata.
type SearchHit struct {
	Chat    Chat
	Message Message
}

// LIDPair associates a WhatsApp LID JID with its canonical phone JID.
type LIDPair struct {
	LID string
	PN  string
}

// SendReceipt is returned after sending a WhatsApp text/reply message.
type SendReceipt struct {
	ID string
	At time.Time
}

// BackfillAnchor describes the oldest known message in a chat used to request older history
// from the user's primary phone.
type BackfillAnchor struct {
	ChatJID   string
	MessageID string
	SenderJID string
	Mine      bool
	At        time.Time
}

// AvatarData holds fetched profile picture metadata and bytes.
type AvatarData struct {
	ID   string
	URL  string
	Mime string
	Data []byte
}

// LoginEvent communicates progress during QR or 8-digit phone pairing code login.
type LoginEvent struct {
	QRCode  string // raw WhatsApp QR payload when QR mode produces/rotates a code
	Paired  bool   // true once pairing succeeds
	Account string // display account string once paired
	JID     string // canonical phone JID once paired
	LID     string // canonical LID JID if known
	Err     error  // non-nil when pairing fails or expires
}

// LoginSession represents an active in-progress QR or phone-pairing authentication session.
type LoginSession interface {
	// Events returns a channel emitting QR refreshes, pairing completion, or terminal errors.
	Events() <-chan LoginEvent
	// RequestPairCode requests an 8-digit pairing code for digits (E.164 without '+') once connected.
	RequestPairCode(ctx context.Context, phoneDigits string) (string, error)
	// Cancel aborts the login attempt and closes its temporary connection.
	Cancel()
}

// Backend abstracts whatsmeow networking and login operations so the connector state machine,
// tools, inbox, archive, threads, and approval flows can be tested without real network calls.
type Backend interface {
	// HasSession reports whether an encrypted paired device exists in Hub secrets.
	HasSession(ctx context.Context) bool
	// AccountInfo returns the saved account display label and canonical JID/LID.
	AccountInfo() (account, jid, lid string)
	// Connect connects the paired WhatsApp session and stays alive until ctx ends or Disconnect.
	// Callbacks deliver real-time updates to Conn.
	Connect(ctx context.Context, quietPresence bool, cb BackendCallbacks) error
	// Disconnect closes the active websocket connection without unlinking the device.
	Disconnect()
	// IsConnected reports whether the websocket is currently connected and logged in.
	IsConnected() bool
	// StartLogin opens a fresh unpaired WhatsApp session for QR scan or 8-digit phone pairing.
	StartLogin(ctx context.Context) (LoginSession, error)
	// SendText sends a text message (or quoted reply when replyTo is non-nil).
	SendText(ctx context.Context, chatJID, text string, replyTo *Message) (SendReceipt, error)
	// SendReaction sets (or clears when emoji == "") a reaction on target message.
	SendReaction(ctx context.Context, chatJID, messageID, senderJID string, fromMe bool, emoji string) error
	// MarkRead sends read receipts for messages in chatJID grouped by senderJID.
	MarkRead(ctx context.Context, chatJID string, bySender map[string][]string, readAt time.Time) error
	// RequestHistorySync asks the user's primary phone for `count` older messages before anchor.
	RequestHistorySync(ctx context.Context, anchor BackfillAnchor, count int) error
	// FetchAvatar downloads a chat/user profile picture (returns nil, nil when none exists).
	FetchAvatar(ctx context.Context, jid string) (*AvatarData, error)
	// Logout unlinks the device from WhatsApp (when reachable) and deletes encrypted Hub secrets.
	Logout(ctx context.Context) error
	// Flush persists any dirty device/Signal state to Hub encrypted secrets.
	Flush(ctx context.Context, force bool) error
	// Close flushes and shuts down the backend cleanly.
	Close(ctx context.Context) error
}

// BackendCallbacks receives live WhatsApp events from the backend into Conn + LocalStore.
type BackendCallbacks struct {
	OnConnected      func(account, jid, lid string)
	OnDisconnected   func(err error)
	OnLoggedOut      func(reason string)
	OnStreamReplaced func()
	OnMessage        func(msg Message, chatHint Chat, isLive bool)
	OnReaction       func(chatJID, targetID, senderJID, emoji string)
	OnEdit           func(chatJID, targetID, newText string)
	OnRevoke         func(chatJID, targetID string)
	OnChatRead       func(chatJID string, read bool)
	OnChatMute       func(chatJID string, mutedUntil int64)
	OnChatArchive    func(chatJID string, archived bool)
	OnContact        func(c Contact)
	OnGroup          func(jid, name string, members int)
	OnLIDMapping     func(pair LIDPair)
	OnHistoryBatch   func(chatJID string, onDemand bool, count int)
}
