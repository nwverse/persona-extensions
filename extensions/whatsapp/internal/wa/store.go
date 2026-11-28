package wa

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

const (
	defaultMaxRows = 200000
	indexDBFile    = "whatsapp-index.db"
)

// Store is the pure-Go SQLite local index for WhatsApp chats, messages (with FTS5),
// contacts, and LID <-> phone JID mappings.
type Store struct {
	mu      sync.Mutex
	db      *sql.DB
	path    string
	inMem   bool
	lidToPN map[string]string
	pnToLID map[string]string
}

// OpenStore opens (or creates) the local SQLite index in dataDir.
// When dataDir is empty, an in-memory SQLite database is used (ideal for unit tests).
func OpenStore(dataDir string) (*Store, error) {
	dsn := ":memory:"
	dbPath := ""
	inMem := true
	if strings.TrimSpace(dataDir) != "" {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
		dbPath = filepath.Join(dataDir, indexDBFile)
		dsn = dbPath
		inMem = false
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open index sqlite: %w", err)
	}
	// Single connection keeps writes contention-free and keeps :memory: alive.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{
		db:      db,
		path:    dbPath,
		inMem:   inMem,
		lidToPN: map[string]string{},
		pnToLID: map[string]string{},
	}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !inMem {
		_ = os.Chmod(dbPath, 0o600)
	}
	if err := s.loadLIDCache(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initSchema() error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA auto_vacuum = INCREMENTAL",
	}
	if !s.inMem {
		pragmas = append(pragmas, "PRAGMA journal_mode = WAL", "PRAGMA synchronous = NORMAL")
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}

	const ddl = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS lid_map (
	lid        TEXT PRIMARY KEY,
	pn         TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_lid_map_pn ON lid_map(pn);

CREATE TABLE IF NOT EXISTS contacts (
	jid           TEXT PRIMARY KEY,
	lid           TEXT NOT NULL DEFAULT '',
	phone         TEXT NOT NULL DEFAULT '',
	push_name     TEXT NOT NULL DEFAULT '',
	full_name     TEXT NOT NULL DEFAULT '',
	first_name    TEXT NOT NULL DEFAULT '',
	business_name TEXT NOT NULL DEFAULT '',
	updated_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS chats (
	jid             TEXT PRIMARY KEY,
	kind            TEXT NOT NULL,
	name            TEXT NOT NULL DEFAULT '',
	phone           TEXT NOT NULL DEFAULT '',
	unread_count    INTEGER NOT NULL DEFAULT 0,
	mention_count   INTEGER NOT NULL DEFAULT 0,
	muted_until     INTEGER NOT NULL DEFAULT 0,
	archived        INTEGER NOT NULL DEFAULT 0,
	last_msg_id     TEXT NOT NULL DEFAULT '',
	last_msg_ts     INTEGER NOT NULL DEFAULT 0,
	last_msg_text   TEXT NOT NULL DEFAULT '',
	last_msg_from   TEXT NOT NULL DEFAULT '',
	last_msg_mine   INTEGER NOT NULL DEFAULT 0,
	last_mention_id TEXT NOT NULL DEFAULT '',
	last_mention_ts INTEGER NOT NULL DEFAULT 0,
	avatar_id       TEXT NOT NULL DEFAULT '',
	avatar_url      TEXT NOT NULL DEFAULT '',
	member_count    INTEGER NOT NULL DEFAULT 0,
	updated_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chats_last_ts ON chats(last_msg_ts DESC);

CREATE TABLE IF NOT EXISTS messages (
	seq             INTEGER PRIMARY KEY AUTOINCREMENT,
	chat_jid        TEXT NOT NULL,
	id              TEXT NOT NULL,
	sender_jid      TEXT NOT NULL DEFAULT '',
	sender_name     TEXT NOT NULL DEFAULT '',
	mine            INTEGER NOT NULL DEFAULT 0,
	msg_type        TEXT NOT NULL DEFAULT 'TEXT',
	text            TEXT NOT NULL DEFAULT '',
	ts              INTEGER NOT NULL,
	reply_to_id     TEXT NOT NULL DEFAULT '',
	reply_to_sender TEXT NOT NULL DEFAULT '',
	reply_to_text   TEXT NOT NULL DEFAULT '',
	mentioned_me    INTEGER NOT NULL DEFAULT 0,
	edited          INTEGER NOT NULL DEFAULT 0,
	reactions_json  TEXT NOT NULL DEFAULT '',
	media_json      TEXT NOT NULL DEFAULT '',
	UNIQUE(chat_jid, id)
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_jid, ts DESC, seq DESC);
CREATE INDEX IF NOT EXISTS idx_messages_ts ON messages(ts DESC, seq DESC);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
	text,
	sender_name,
	content='messages',
	content_rowid='seq',
	tokenize='unicode61'
);

CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, text, sender_name) VALUES (new.seq, new.text, new.sender_name);
END;

CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, text, sender_name) VALUES('delete', old.seq, old.text, old.sender_name);
END;

CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE OF text, sender_name ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, text, sender_name) VALUES('delete', old.seq, old.text, old.sender_name);
	INSERT INTO messages_fts(rowid, text, sender_name) VALUES (new.seq, new.text, new.sender_name);
END;
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("init index schema: %w", err)
	}
	return nil
}

func (s *Store) loadLIDCache() error {
	rows, err := s.db.Query(`SELECT lid, pn FROM lid_map`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var lid, pn string
		if err := rows.Scan(&lid, &pn); err != nil {
			return err
		}
		s.lidToPN[lid] = pn
		s.pnToLID[pn] = lid
	}
	return rows.Err()
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// ResetForAccount checks whether the local index belongs to accountJID; if it belonged to a
// different account, all tables are wiped cleanly so two WhatsApp accounts never mix.
func (s *Store) ResetForAccount(accountJID string) error {
	accountJID = NormalizeJID(accountJID)
	if accountJID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var prev string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'account_jid'`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if prev != "" && prev != accountJID {
		if err := s.wipeLocked(); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(`INSERT INTO meta(key, value) VALUES('account_jid', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, accountJID)
	return err
}

// Wipe clears all local index data (called on Logout).
func (s *Store) Wipe() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wipeLocked()
}

func (s *Store) wipeLocked() error {
	for _, stmt := range []string{
		`DELETE FROM messages`,
		`DELETE FROM chats`,
		`DELETE FROM contacts`,
		`DELETE FROM lid_map`,
		`DELETE FROM meta`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	s.lidToPN = map[string]string{}
	s.pnToLID = map[string]string{}
	return nil
}

// ResolveLIDToPN resolves a normalized LID JID to its canonical phone JID if known.
func (s *Store) ResolveLIDToPN(lid string) (string, bool) {
	lid = NormalizeJID(lid)
	if !IsLID(lid) {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pn, ok := s.lidToPN[lid]
	return pn, ok
}

// ResolvePNToLID resolves a canonical phone JID to its mapped LID JID if known.
func (s *Store) ResolvePNToLID(pn string) (string, bool) {
	pn = NormalizeJID(pn)
	if !IsPhoneJID(pn) {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lid, ok := s.pnToLID[pn]
	return lid, ok
}

// PutLIDMapping records a LID <-> Phone JID mapping and merges any pre-existing LID chat/messages/contact
// rows into the canonical phone JID without duplicating messages.
func (s *Store) PutLIDMapping(lidRaw, pnRaw string) error {
	lid := NormalizeJID(lidRaw)
	pn := NormalizeJID(pnRaw)
	if !IsLID(lid) || !IsPhoneJID(pn) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`INSERT INTO lid_map(lid, pn, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(lid) DO UPDATE SET pn = excluded.pn, updated_at = excluded.updated_at`, lid, pn, now); err != nil {
		return err
	}

	// Merge messages that arrived under the LID chat JID into the canonical PN chat JID.
	if _, err := tx.Exec(`DELETE FROM messages WHERE chat_jid = ? AND id IN (SELECT id FROM messages WHERE chat_jid = ?)`, lid, pn); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE messages SET chat_jid = ? WHERE chat_jid = ?`, pn, lid); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE messages SET sender_jid = ? WHERE sender_jid = ?`, pn, lid); err != nil {
		return err
	}

	// Merge chat row if one existed under LID.
	var lidChat Chat
	var hasLIDChat bool
	if c, ok, err := getChatTx(tx, lid); err != nil {
		return err
	} else if ok {
		lidChat, hasLIDChat = c, true
	}
	if hasLIDChat {
		pnChat, hasPNChat, err := getChatTx(tx, pn)
		if err != nil {
			return err
		}
		if !hasPNChat {
			lidChat.JID = pn
			lidChat.Phone = PhoneFromJID(pn)
			if lidChat.Name == "" || lidChat.Name == lid {
				lidChat.Name = BestDisplayName("", "", "", pn)
			}
			if err := putChatTx(tx, lidChat); err != nil {
				return err
			}
		} else {
			if pnChat.Name == "" || pnChat.Name == pn || pnChat.Name == "+"+pnChat.Phone {
				if lidChat.Name != "" && lidChat.Name != lid {
					pnChat.Name = lidChat.Name
				}
			}
			if pnChat.Phone == "" {
				pnChat.Phone = PhoneFromJID(pn)
			}
			pnChat.UnreadCount = max(pnChat.UnreadCount, lidChat.UnreadCount)
			pnChat.MentionCount = max(pnChat.MentionCount, lidChat.MentionCount)
			if lidChat.LastMsgTS.After(pnChat.LastMsgTS) {
				pnChat.LastMsgID = lidChat.LastMsgID
				pnChat.LastMsgTS = lidChat.LastMsgTS
				pnChat.LastMsgText = lidChat.LastMsgText
				pnChat.LastMsgFrom = lidChat.LastMsgFrom
				pnChat.LastMsgMine = lidChat.LastMsgMine
			}
			if lidChat.LastMentionTS.After(pnChat.LastMentionTS) {
				pnChat.LastMentionID = lidChat.LastMentionID
				pnChat.LastMentionTS = lidChat.LastMentionTS
			}
			if pnChat.AvatarID == "" && lidChat.AvatarID != "" {
				pnChat.AvatarID = lidChat.AvatarID
				pnChat.AvatarURL = lidChat.AvatarURL
			}
			if err := putChatTx(tx, pnChat); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM chats WHERE jid = ?`, lid); err != nil {
			return err
		}
	}

	// Merge contact row if one existed under LID.
	var push, full, first, biz string
	err = tx.QueryRow(`SELECT push_name, full_name, first_name, business_name FROM contacts WHERE jid = ?`, lid).Scan(&push, &full, &first, &biz)
	if err == nil {
		if _, err := tx.Exec(`INSERT INTO contacts(jid, lid, phone, push_name, full_name, first_name, business_name, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(jid) DO UPDATE SET
				lid = CASE WHEN excluded.lid != '' THEN excluded.lid ELSE contacts.lid END,
				phone = CASE WHEN excluded.phone != '' THEN excluded.phone ELSE contacts.phone END,
				push_name = CASE WHEN contacts.push_name = '' THEN excluded.push_name ELSE contacts.push_name END,
				full_name = CASE WHEN contacts.full_name = '' THEN excluded.full_name ELSE contacts.full_name END,
				first_name = CASE WHEN contacts.first_name = '' THEN excluded.first_name ELSE contacts.first_name END,
				business_name = CASE WHEN contacts.business_name = '' THEN excluded.business_name ELSE contacts.business_name END,
				updated_at = excluded.updated_at`,
			pn, lid, PhoneFromJID(pn), push, full, first, biz, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM contacts WHERE jid = ?`, lid); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	} else {
		_, _ = tx.Exec(`UPDATE contacts SET lid = ? WHERE jid = ? AND lid = ''`, lid, pn)
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	s.lidToPN[lid] = pn
	s.pnToLID[pn] = lid
	return nil
}

// UpsertContact inserts or merges contact display details and updates any DM chat display name.
func (s *Store) UpsertContact(c Contact) error {
	jid := NormalizeJID(c.JID)
	lid := NormalizeJID(c.LID)
	if jid == "" && lid != "" {
		jid = lid
	}
	if jid == "" || (!IsPhoneJID(jid) && !IsLID(jid)) {
		return nil
	}
	if IsLID(jid) && IsPhoneJID(c.Phone+"@"+ServerUser) && c.Phone != "" {
		lid = jid
		jid = c.Phone + "@" + ServerUser
	}
	if lid != "" && IsLID(lid) && IsPhoneJID(jid) {
		_ = s.PutLIDMapping(lid, jid)
	}
	if IsLID(jid) {
		if pn, ok := s.ResolveLIDToPN(jid); ok {
			lid = jid
			jid = pn
		}
	}
	phone := c.Phone
	if phone == "" {
		phone = PhoneFromJID(jid)
	}
	if lid == "" && IsPhoneJID(jid) {
		if mapped, ok := s.ResolvePNToLID(jid); ok {
			lid = mapped
		}
	}
	now := time.Now().Unix()

	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO contacts(jid, lid, phone, push_name, full_name, first_name, business_name, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			lid = CASE WHEN excluded.lid != '' THEN excluded.lid ELSE contacts.lid END,
			phone = CASE WHEN excluded.phone != '' THEN excluded.phone ELSE contacts.phone END,
			push_name = CASE WHEN excluded.push_name != '' THEN excluded.push_name ELSE contacts.push_name END,
			full_name = CASE WHEN excluded.full_name != '' THEN excluded.full_name ELSE contacts.full_name END,
			first_name = CASE WHEN excluded.first_name != '' THEN excluded.first_name ELSE contacts.first_name END,
			business_name = CASE WHEN excluded.business_name != '' THEN excluded.business_name ELSE contacts.business_name END,
			updated_at = excluded.updated_at`,
		jid, lid, phone, strings.TrimSpace(c.PushName), strings.TrimSpace(c.FullName), strings.TrimSpace(c.FirstName), strings.TrimSpace(c.BusinessName), now)
	if err != nil {
		return err
	}
	// Refresh DM chat display name if the contact now has a better name.
	best := BestDisplayName(firstNonEmpty(c.FullName, c.FirstName), c.BusinessName, c.PushName, jid)
	if best != "" && best != jid && best != "+"+phone {
		_, _ = s.db.Exec(`UPDATE chats SET name = ?, updated_at = ?
			WHERE jid = ? AND kind = 'dm' AND (name = '' OR name = jid OR name = '+' || phone OR ? != '')`,
			best, now, jid, firstNonEmpty(c.FullName, c.FirstName, c.BusinessName))
	}
	return nil
}

// UpsertChat creates or merges chat metadata (name, unread/mute/archive/member count/avatar).
func (s *Store) UpsertChat(c Chat) error {
	jid := CanonicalizeChatJID(c.JID, s)
	if !IsSupportedChatJID(jid) {
		return nil
	}
	kind := c.Kind
	if kind == "" {
		kind = ChatKind(jid)
	}
	phone := c.Phone
	if phone == "" && kind == "dm" {
		phone = PhoneFromJID(jid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, has, err := getChatDB(s.db, jid)
	if err != nil {
		return err
	}
	now := time.Now()
	if !has {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			name = s.contactDisplayNameLocked(jid)
		}
		c.JID = jid
		c.Kind = kind
		c.Name = name
		c.Phone = phone
		if c.UpdatedAt.IsZero() {
			c.UpdatedAt = now
		}
		return putChatDB(s.db, c)
	}
	if name := strings.TrimSpace(c.Name); name != "" && name != jid {
		existing.Name = name
	}
	if existing.Phone == "" && phone != "" {
		existing.Phone = phone
	}
	if c.MemberCount > 0 {
		existing.MemberCount = c.MemberCount
	}
	if c.UnreadCount > existing.UnreadCount {
		existing.UnreadCount = c.UnreadCount
	}
	if c.MutedUntil != 0 {
		existing.MutedUntil = c.MutedUntil
	}
	if c.Archived {
		existing.Archived = true
	}
	if c.AvatarID != "" {
		existing.AvatarID = c.AvatarID
		existing.AvatarURL = c.AvatarURL
	}
	if c.LastMsgTS.After(existing.LastMsgTS) {
		existing.LastMsgID = c.LastMsgID
		existing.LastMsgTS = c.LastMsgTS
		existing.LastMsgText = c.LastMsgText
		existing.LastMsgFrom = c.LastMsgFrom
		existing.LastMsgMine = c.LastMsgMine
	}
	existing.UpdatedAt = now
	return putChatDB(s.db, existing)
}

// UpsertMessage inserts or updates one message (deduplicated by (chat_jid, id)) and updates the
// parent chat's latest preview, unread count, and mention tracking. Returns whether the row was newly inserted.
func (s *Store) UpsertMessage(m Message, chatHint Chat, isLive bool) (inserted bool, err error) {
	chatJID := CanonicalizeChatJID(m.ChatJID, s)
	if !IsSupportedChatJID(chatJID) || strings.TrimSpace(m.ID) == "" {
		return false, nil
	}
	senderJID := CanonicalizeChatJID(m.SenderJID, s)
	if senderJID == "" && !m.Mine && ChatKind(chatJID) == "dm" {
		senderJID = chatJID
	}
	if m.At.IsZero() {
		m.At = time.Now()
	}
	if m.Type == "" {
		m.Type = "TEXT"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	senderName := strings.TrimSpace(m.SenderName)
	if m.Mine {
		senderName = "我"
	} else if senderName == "" {
		senderName = s.contactDisplayNameLocked(senderJID)
	}

	// Keep contact push name fresh when provided.
	if !m.Mine && senderJID != "" && strings.TrimSpace(m.SenderName) != "" && m.SenderName != senderJID {
		now := time.Now().Unix()
		_, _ = s.db.Exec(`INSERT INTO contacts(jid, phone, push_name, updated_at) VALUES(?, ?, ?, ?)
			ON CONFLICT(jid) DO UPDATE SET push_name = CASE WHEN excluded.push_name != '' THEN excluded.push_name ELSE contacts.push_name END, updated_at = excluded.updated_at`,
			senderJID, PhoneFromJID(senderJID), strings.TrimSpace(m.SenderName), now)
	}

	var reactJSON, mediaJSON string
	if len(m.Reactions) > 0 {
		if b, err := json.Marshal(m.Reactions); err == nil {
			reactJSON = string(b)
		}
	}
	if m.Media != nil {
		if b, err := json.Marshal(m.Media); err == nil {
			mediaJSON = string(b)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	err = tx.QueryRow(`SELECT 1 FROM messages WHERE chat_jid = ? AND id = ?`, chatJID, m.ID).Scan(&exists)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	inserted = errors.Is(err, sql.ErrNoRows)

	if inserted {
		_, err = tx.Exec(`INSERT INTO messages(chat_jid, id, sender_jid, sender_name, mine, msg_type, text, ts, reply_to_id, reply_to_sender, reply_to_text, mentioned_me, edited, reactions_json, media_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			chatJID, m.ID, senderJID, senderName, boolInt(m.Mine), m.Type, m.Text, m.At.Unix(),
			m.ReplyToID, m.ReplyToSender, m.ReplyToText, boolInt(m.MentionedMe), boolInt(m.Edited), reactJSON, mediaJSON)
	} else {
		_, err = tx.Exec(`UPDATE messages SET
				sender_jid = CASE WHEN ? != '' THEN ? ELSE sender_jid END,
				sender_name = CASE WHEN ? != '' THEN ? ELSE sender_name END,
				text = CASE WHEN ? != '' THEN ? ELSE text END,
				reply_to_id = CASE WHEN ? != '' THEN ? ELSE reply_to_id END,
				reply_to_sender = CASE WHEN ? != '' THEN ? ELSE reply_to_sender END,
				reply_to_text = CASE WHEN ? != '' THEN ? ELSE reply_to_text END,
				mentioned_me = CASE WHEN ? = 1 THEN 1 ELSE mentioned_me END,
				edited = CASE WHEN ? = 1 THEN 1 ELSE edited END,
				media_json = CASE WHEN ? != '' THEN ? ELSE media_json END
			WHERE chat_jid = ? AND id = ?`,
			senderJID, senderJID, senderName, senderName, m.Text, m.Text,
			m.ReplyToID, m.ReplyToID, m.ReplyToSender, m.ReplyToSender, m.ReplyToText, m.ReplyToText,
			boolInt(m.MentionedMe), boolInt(m.Edited), mediaJSON, mediaJSON,
			chatJID, m.ID)
	}
	if err != nil {
		return false, err
	}

	// Upsert chat summary row.
	chat, hasChat, err := getChatTx(tx, chatJID)
	if err != nil {
		return false, err
	}
	if !hasChat {
		kind := chatHint.Kind
		if kind == "" {
			kind = ChatKind(chatJID)
		}
		name := strings.TrimSpace(chatHint.Name)
		if name == "" {
			if kind == "dm" {
				name = s.contactDisplayNameTx(tx, chatJID)
			} else {
				name = chatJID
			}
		}
		chat = Chat{
			JID:         chatJID,
			Kind:        kind,
			Name:        name,
			Phone:       PhoneFromJID(chatJID),
			UnreadCount: chatHint.UnreadCount,
			MutedUntil:  chatHint.MutedUntil,
			Archived:    chatHint.Archived,
			MemberCount: chatHint.MemberCount,
		}
	} else {
		if name := strings.TrimSpace(chatHint.Name); name != "" && name != chatJID {
			if chat.Name == "" || chat.Name == chatJID || chat.Name == "+"+chat.Phone || chat.Kind == "group" {
				chat.Name = name
			}
		}
		if chatHint.MemberCount > 0 {
			chat.MemberCount = chatHint.MemberCount
		}
	}

	if m.At.Unix() >= chat.LastMsgTS.Unix() {
		chat.LastMsgID = m.ID
		chat.LastMsgTS = m.At
		chat.LastMsgText = m.Text
		chat.LastMsgFrom = senderName
		chat.LastMsgMine = m.Mine
	}
	if isLive && inserted {
		if m.Mine {
			chat.UnreadCount = 0
			chat.MentionCount = 0
		} else {
			chat.UnreadCount++
			if m.MentionedMe && chat.Kind == "group" {
				chat.MentionCount++
			}
		}
	}
	if m.MentionedMe && !m.Mine && chat.Kind == "group" && m.At.Unix() >= chat.LastMentionTS.Unix() {
		chat.LastMentionID = m.ID
		chat.LastMentionTS = m.At
	}
	chat.UpdatedAt = time.Now()
	if err := putChatTx(tx, chat); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return inserted, nil
}

// ApplyEdit updates an existing message's text and marks it edited.
func (s *Store) ApplyEdit(chatJID, msgID, newText string) error {
	chatJID = CanonicalizeChatJID(chatJID, s)
	if chatJID == "" || msgID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE messages SET text = ?, edited = 1 WHERE chat_jid = ? AND id = ?`, newText, chatJID, msgID)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`UPDATE chats SET last_msg_text = ? WHERE jid = ? AND last_msg_id = ?`, newText, chatJID, msgID)
	return nil
}

// ApplyRevoke marks a revoked/deleted message with a tombstone text while preserving thread ordering.
func (s *Store) ApplyRevoke(chatJID, msgID string) error {
	return s.ApplyEdit(chatJID, msgID, "[已撤回消息]")
}

// ApplyReaction updates the reaction map on a target message. Empty emoji removes sender's reaction.
func (s *Store) ApplyReaction(chatJID, targetID, senderJID, emoji string) error {
	chatJID = CanonicalizeChatJID(chatJID, s)
	senderJID = CanonicalizeChatJID(senderJID, s)
	if chatJID == "" || targetID == "" {
		return nil
	}
	if senderJID == "" {
		senderJID = "self"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	err := s.db.QueryRow(`SELECT reactions_json FROM messages WHERE chat_jid = ? AND id = ?`, chatJID, targetID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	reacts := map[string]string{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &reacts)
	}
	if strings.TrimSpace(emoji) == "" {
		delete(reacts, senderJID)
	} else {
		reacts[senderJID] = emoji
	}
	var next string
	if len(reacts) > 0 {
		if b, err := json.Marshal(reacts); err == nil {
			next = string(b)
		}
	}
	_, err = s.db.Exec(`UPDATE messages SET reactions_json = ? WHERE chat_jid = ? AND id = ?`, next, chatJID, targetID)
	return err
}

// SetChatRead updates unread/mention counters when read receipts or MarkChatAsRead events arrive.
func (s *Store) SetChatRead(chatJID string, read bool) error {
	chatJID = CanonicalizeChatJID(chatJID, s)
	if chatJID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if read {
		_, err := s.db.Exec(`UPDATE chats SET unread_count = 0, mention_count = 0, updated_at = ? WHERE jid = ?`, time.Now().Unix(), chatJID)
		return err
	}
	_, err := s.db.Exec(`UPDATE chats SET unread_count = CASE WHEN unread_count = 0 THEN 1 ELSE unread_count END, updated_at = ? WHERE jid = ?`, time.Now().Unix(), chatJID)
	return err
}

// SetChatMute updates a chat's muted_until timestamp (0 = unmuted, <0 = forever).
func (s *Store) SetChatMute(chatJID string, mutedUntil int64) error {
	chatJID = CanonicalizeChatJID(chatJID, s)
	if chatJID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chats SET muted_until = ?, updated_at = ? WHERE jid = ?`, mutedUntil, time.Now().Unix(), chatJID)
	return err
}

// SetChatArchive updates a chat's archived state.
func (s *Store) SetChatArchive(chatJID string, archived bool) error {
	chatJID = CanonicalizeChatJID(chatJID, s)
	if chatJID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chats SET archived = ?, updated_at = ? WHERE jid = ?`, boolInt(archived), time.Now().Unix(), chatJID)
	return err
}

// SetChatAvatar stores cached profile picture id/url for a chat.
func (s *Store) SetChatAvatar(chatJID, avatarID, avatarURL string) error {
	chatJID = CanonicalizeChatJID(chatJID, s)
	if chatJID == "" || avatarID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE chats SET avatar_id = ?, avatar_url = ?, updated_at = ? WHERE jid = ?`, avatarID, avatarURL, time.Now().Unix(), chatJID)
	return err
}

// ListChats lists chats ordered by latest activity descending.
func (s *Store) ListChats(limit int, unreadOnly, includeArchived bool, kind string) ([]Chat, bool, error) {
	if limit <= 0 {
		limit = 30
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var where []string
	var args []any
	if unreadOnly {
		where = append(where, "unread_count > 0")
	}
	if !includeArchived {
		where = append(where, "archived = 0")
	}
	if kind == "dm" || kind == "group" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	q := `SELECT jid, kind, name, phone, unread_count, mention_count, muted_until, archived,
		last_msg_id, last_msg_ts, last_msg_text, last_msg_from, last_msg_mine,
		last_mention_id, last_mention_ts, avatar_id, avatar_url, member_count, updated_at
		FROM chats`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY last_msg_ts DESC, updated_at DESC LIMIT ?"
	args = append(args, limit+1)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// GetChat returns a single chat by canonical JID (resolving LID if needed).
func (s *Store) GetChat(rawJID string) (Chat, bool, error) {
	jid := CanonicalizeChatJID(rawJID, s)
	if jid == "" {
		return Chat{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return getChatDB(s.db, jid)
}

// ResolveChatInput resolves a user-supplied chat reference for read tools:
// canonical JID, LID (mapped to PN), phone number (+86...), or exact chat name.
func (s *Store) ResolveChatInput(input string) (Chat, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Chat{}, errors.New("chat is required")
	}
	// 1. Direct JID or LID
	if norm := CanonicalizeChatJID(input, s); norm != "" && IsSupportedChatJID(norm) {
		if c, ok, err := s.GetChat(norm); err != nil {
			return Chat{}, err
		} else if ok {
			return c, nil
		}
	}
	// 2. Phone number -> s.whatsapp.net JID
	if pnJID, err := PhoneToJID(input); err == nil {
		if c, ok, err := s.GetChat(pnJID); err != nil {
			return Chat{}, err
		} else if ok {
			return c, nil
		}
	}
	// 3. Exact chat name match (must be unique)
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT jid, kind, name, phone, unread_count, mention_count, muted_until, archived,
		last_msg_id, last_msg_ts, last_msg_text, last_msg_from, last_msg_mine,
		last_mention_id, last_mention_ts, avatar_id, avatar_url, member_count, updated_at
		FROM chats WHERE lower(name) = lower(?) ORDER BY last_msg_ts DESC`, input)
	if err != nil {
		s.mu.Unlock()
		return Chat{}, err
	}
	var matches []Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			rows.Close()
			s.mu.Unlock()
			return Chat{}, err
		}
		matches = append(matches, c)
	}
	rows.Close()
	s.mu.Unlock()
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Chat{}, fmt.Errorf("找不到会话 %q（请先用 whatsapp_list_chats 查看会话 id）", input)
	default:
		return Chat{}, fmt.Errorf("有 %d 个会话叫 %q：请改用 whatsapp_list_chats 返回的精确 id（JID）", len(matches), input)
	}
}

// ReadMessages returns one page of messages in chatJID, ordered oldest-first.
// `before` may be a message ID in that chat (or empty for newest page).
// Returns (messages oldest-first, nextBefore cursor, error).
func (s *Store) ReadMessages(chatJID, before string, since, until time.Time, limit int) ([]Message, string, error) {
	chatJID = CanonicalizeChatJID(chatJID, s)
	if limit <= 0 {
		limit = 30
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var where []string
	var args []any
	where = append(where, "chat_jid = ?")
	args = append(args, chatJID)

	if strings.TrimSpace(before) != "" {
		var beforeTS, beforeSeq int64
		err := s.db.QueryRow(`SELECT ts, seq FROM messages WHERE chat_jid = ? AND id = ?`, chatJID, strings.TrimSpace(before)).Scan(&beforeTS, &beforeSeq)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", fmt.Errorf("before: message %q not found in chat %s", before, chatJID)
		}
		if err != nil {
			return nil, "", err
		}
		where = append(where, "(ts < ? OR (ts = ? AND seq < ?))")
		args = append(args, beforeTS, beforeTS, beforeSeq)
	}
	if !since.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, since.Unix())
	}
	if !until.IsZero() {
		where = append(where, "ts < ?")
		args = append(args, until.Unix())
	}

	q := `SELECT seq, chat_jid, id, sender_jid, sender_name, mine, msg_type, text, ts,
		reply_to_id, reply_to_sender, reply_to_text, mentioned_me, edited, reactions_json, media_json
		FROM messages WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ts DESC, seq DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var desc []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, "", err
		}
		desc = append(desc, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	hasOlder := len(desc) > limit
	if hasOlder {
		desc = desc[:limit]
	}
	// Reverse to oldest-first.
	for i, j := 0, len(desc)-1; i < j; i, j = i+1, j-1 {
		desc[i], desc[j] = desc[j], desc[i]
	}
	nextBefore := ""
	if hasOlder && len(desc) > 0 {
		nextBefore = desc[0].ID
	}
	return desc, nextBefore, nil
}

// GetMessage returns one message by chat JID and message ID.
func (s *Store) GetMessage(chatJID, msgID string) (Message, bool, error) {
	chatJID = CanonicalizeChatJID(chatJID, s)
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(`SELECT seq, chat_jid, id, sender_jid, sender_name, mine, msg_type, text, ts,
		reply_to_id, reply_to_sender, reply_to_text, mentioned_me, edited, reactions_json, media_json
		FROM messages WHERE chat_jid = ? AND id = ?`, chatJID, strings.TrimSpace(msgID))
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	return m, true, nil
}

// OldestMessage returns the oldest stored message in chatJID (used as the anchor for on-demand backfill).
func (s *Store) OldestMessage(chatJID string) (Message, bool, error) {
	chatJID = CanonicalizeChatJID(chatJID, s)
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(`SELECT seq, chat_jid, id, sender_jid, sender_name, mine, msg_type, text, ts,
		reply_to_id, reply_to_sender, reply_to_text, mentioned_me, edited, reactions_json, media_json
		FROM messages WHERE chat_jid = ? ORDER BY ts ASC, seq ASC LIMIT 1`, chatJID)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	return m, true, nil
}

// MessageCount returns the total number of stored messages in chatJID (or across all chats when chatJID == "").
func (s *Store) MessageCount(chatJID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	var err error
	if chatJID == "" {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&n)
	} else {
		err = s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE chat_jid = ?`, CanonicalizeChatJID(chatJID, s)).Scan(&n)
	}
	return n, err
}

// SearchMessages performs full-text search using SQLite FTS5, with automatic CJK/substring LIKE
// fallback so both English phrases and Chinese substrings match reliably. Results are newest first.
func (s *Store) SearchMessages(query, chatJID string, since, until time.Time, limit int) ([]SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	if chatJID != "" {
		chatJID = CanonicalizeChatJID(chatJID, s)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var msgs []Message
	seen := map[int64]bool{}

	// 1. Try FTS5 when query can be tokenized safely (especially fast for Latin/space-separated words).
	if ftsQuery := buildFTSQuery(query); ftsQuery != "" && !containsCJK(query) {
		ftsMsgs, err := s.searchFTSLocked(ftsQuery, chatJID, since, until, limit)
		if err == nil {
			for _, m := range ftsMsgs {
				seen[m.Seq] = true
				msgs = append(msgs, m)
			}
		}
	}

	// 2. For CJK queries or if FTS5 yielded fewer than limit results, supplement with escaped LIKE substring search.
	if len(msgs) < limit {
		likeMsgs, err := s.searchLikeLocked(query, chatJID, since, until, limit)
		if err != nil {
			return nil, err
		}
		for _, m := range likeMsgs {
			if seen[m.Seq] {
				continue
			}
			seen[m.Seq] = true
			msgs = append(msgs, m)
			if len(msgs) >= limit {
				break
			}
		}
	}

	chatsCache := map[string]Chat{}
	hits := make([]SearchHit, 0, len(msgs))
	for _, m := range msgs {
		c, ok := chatsCache[m.ChatJID]
		if !ok {
			if found, exists, err := getChatDB(s.db, m.ChatJID); err == nil && exists {
				c = found
			} else {
				c = Chat{JID: m.ChatJID, Kind: ChatKind(m.ChatJID), Name: m.ChatJID}
			}
			chatsCache[m.ChatJID] = c
		}
		hits = append(hits, SearchHit{Chat: c, Message: m})
	}
	return hits, nil
}

func (s *Store) searchFTSLocked(ftsQuery, chatJID string, since, until time.Time, limit int) ([]Message, error) {
	var where []string
	var args []any
	where = append(where, "messages_fts MATCH ?")
	args = append(args, ftsQuery)
	if chatJID != "" {
		where = append(where, "m.chat_jid = ?")
		args = append(args, chatJID)
	}
	if !since.IsZero() {
		where = append(where, "m.ts >= ?")
		args = append(args, since.Unix())
	}
	if !until.IsZero() {
		where = append(where, "m.ts < ?")
		args = append(args, until.Unix())
	}
	q := `SELECT m.seq, m.chat_jid, m.id, m.sender_jid, m.sender_name, m.mine, m.msg_type, m.text, m.ts,
		m.reply_to_id, m.reply_to_sender, m.reply_to_text, m.mentioned_me, m.edited, m.reactions_json, m.media_json
		FROM messages_fts JOIN messages m ON m.seq = messages_fts.rowid
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY m.ts DESC, m.seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) searchLikeLocked(query, chatJID string, since, until time.Time, limit int) ([]Message, error) {
	var where []string
	var args []any
	pattern := "%" + escapeLike(strings.ToLower(query)) + "%"
	where = append(where, `(lower(text) LIKE ? ESCAPE '\' OR lower(sender_name) LIKE ? ESCAPE '\')`)
	args = append(args, pattern, pattern)
	if chatJID != "" {
		where = append(where, "chat_jid = ?")
		args = append(args, chatJID)
	}
	if !since.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, since.Unix())
	}
	if !until.IsZero() {
		where = append(where, "ts < ?")
		args = append(args, until.Unix())
	}
	q := `SELECT seq, chat_jid, id, sender_jid, sender_name, mine, msg_type, text, ts,
		reply_to_id, reply_to_sender, reply_to_text, mentioned_me, edited, reactions_json, media_json
		FROM messages WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ts DESC, seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListContacts lists synced contacts optionally filtered by query.
func (s *Store) ListContacts(query string, limit int) ([]Contact, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	query = strings.TrimSpace(query)
	s.mu.Lock()
	defer s.mu.Unlock()
	var q string
	var args []any
	if query == "" {
		q = `SELECT jid, lid, phone, push_name, full_name, first_name, business_name, updated_at
			FROM contacts ORDER BY CASE WHEN full_name != '' THEN 0 WHEN business_name != '' THEN 1 WHEN push_name != '' THEN 2 ELSE 3 END, updated_at DESC LIMIT ?`
		args = append(args, limit+1)
	} else {
		p := "%" + escapeLike(strings.ToLower(query)) + "%"
		q = `SELECT jid, lid, phone, push_name, full_name, first_name, business_name, updated_at
			FROM contacts WHERE lower(jid) LIKE ? ESCAPE '\' OR lower(lid) LIKE ? ESCAPE '\' OR lower(phone) LIKE ? ESCAPE '\'
			OR lower(push_name) LIKE ? ESCAPE '\' OR lower(full_name) LIKE ? ESCAPE '\' OR lower(first_name) LIKE ? ESCAPE '\' OR lower(business_name) LIKE ? ESCAPE '\'
			ORDER BY updated_at DESC LIMIT ?`
		args = append(args, p, p, p, p, p, p, p, limit+1)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		var upd int64
		if err := rows.Scan(&c.JID, &c.LID, &c.Phone, &c.PushName, &c.FullName, &c.FirstName, &c.BusinessName, &upd); err != nil {
			return nil, false, err
		}
		if upd > 0 {
			c.UpdatedAt = time.Unix(upd, 0).UTC()
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// RecentUnreadIncoming returns recent incoming (mine = 0) messages in chatJID for MarkRead,
// grouped by sender JID (required by whatsmeow.Client.MarkRead in group chats).
func (s *Store) RecentUnreadIncoming(chatJID string, limit int) (map[string][]string, error) {
	chatJID = CanonicalizeChatJID(chatJID, s)
	if limit <= 0 {
		limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT id, sender_jid FROM messages WHERE chat_jid = ? AND mine = 0 ORDER BY ts DESC, seq DESC LIMIT ?`, chatJID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grouped := map[string][]string{}
	for rows.Next() {
		var id, sender string
		if err := rows.Scan(&id, &sender); err != nil {
			return nil, err
		}
		grouped[sender] = append(grouped[sender], id)
	}
	return grouped, rows.Err()
}

// ArchiveBatch holds one chat and its newly-indexed messages for Knowledge Base archiving.
type ArchiveBatch struct {
	Chat     Chat
	Messages []Message
}

// ArchiveSince returns up to maxBatches chat batches containing messages with seq > afterSeq.
// Returns (batches, nextSeq, more, err).
func (s *Store) ArchiveSince(afterSeq int64, maxBatches, maxMsgsPerCall int) ([]ArchiveBatch, int64, bool, error) {
	if maxBatches <= 0 {
		maxBatches = 20
	}
	if maxMsgsPerCall <= 0 {
		maxMsgsPerCall = 1000
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT seq, chat_jid, id, sender_jid, sender_name, mine, msg_type, text, ts,
		reply_to_id, reply_to_sender, reply_to_text, mentioned_me, edited, reactions_json, media_json
		FROM messages WHERE seq > ? ORDER BY seq ASC LIMIT ?`, afterSeq, maxMsgsPerCall+1)
	if err != nil {
		return nil, afterSeq, false, err
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, afterSeq, false, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, afterSeq, false, err
	}
	more := len(msgs) > maxMsgsPerCall
	if more {
		msgs = msgs[:maxMsgsPerCall]
	}

	// Group messages by chat while enforcing maxBatches cleanly so cursor never skips an un-emitted chat.
	chatOrder := []string{}
	byChat := map[string][]Message{}
	nextSeq := afterSeq
	for _, m := range msgs {
		if _, exists := byChat[m.ChatJID]; !exists {
			if len(chatOrder) >= maxBatches {
				more = true
				break
			}
			chatOrder = append(chatOrder, m.ChatJID)
		}
		byChat[m.ChatJID] = append(byChat[m.ChatJID], m)
		if m.Seq > nextSeq {
			nextSeq = m.Seq
		}
	}

	batches := make([]ArchiveBatch, 0, len(chatOrder))
	for _, jid := range chatOrder {
		c, ok, err := getChatDB(s.db, jid)
		if err != nil || !ok {
			c = Chat{JID: jid, Kind: ChatKind(jid), Name: jid}
		}
		batches = append(batches, ArchiveBatch{Chat: c, Messages: byChat[jid]})
	}
	return batches, nextSeq, more, nil
}

// EnforceStorageCap prunes the oldest messages when the SQLite index exceeds maxBytes or maxRows,
// keeping at least the newest message per chat so chat summaries stay intact.
// Returns the number of pruned messages.
func (s *Store) EnforceStorageCap(ctx context.Context, maxBytes int64, maxRows int) (int64, error) {
	if maxRows <= 0 {
		maxRows = defaultMaxRows
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var totalRows int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&totalRows); err != nil {
		return 0, err
	}
	var pageCount, pageSize int64
	_ = s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount)
	_ = s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize)
	dbBytes := pageCount * pageSize

	needBytes := maxBytes > 0 && dbBytes > maxBytes
	needRows := totalRows > int64(maxRows)
	if !needBytes && !needRows {
		return 0, nil
	}

	toDelete := int64(0)
	if needRows {
		toDelete = totalRows - int64(maxRows) + max(int64(maxRows)/10, 1)
	}
	if needBytes {
		pctDelete := max(totalRows/5, 1)
		if pctDelete > toDelete {
			toDelete = pctDelete
		}
	}

	// Keep the latest message of each chat so chat rows remain consistent.
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE seq IN (
		SELECT m.seq FROM messages m
		WHERE m.seq NOT IN (SELECT MAX(seq) FROM messages GROUP BY chat_jid)
		ORDER BY m.ts ASC, m.seq ASC
		LIMIT ?
	)`, toDelete)
	if err != nil {
		return 0, err
	}
	deleted, _ := res.RowsAffected()
	if deleted > 0 {
		_, _ = s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`)
		if !s.inMem {
			_, _ = s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		}
	}
	return deleted, nil
}

// RecentMessagesForContext returns the latest n messages in chatJID (oldest first) for inbox context.
func (s *Store) RecentMessagesForContext(chatJID string, n int) ([]Message, error) {
	msgs, _, err := s.ReadMessages(chatJID, "", time.Time{}, time.Time{}, n)
	return msgs, err
}

func (s *Store) contactDisplayNameLocked(jid string) string {
	return s.contactDisplayNameTx(s.db, jid)
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func (s *Store) contactDisplayNameTx(q queryRower, jid string) string {
	jid = NormalizeJID(jid)
	if jid == "" {
		return ""
	}
	var push, full, first, biz string
	err := q.QueryRow(`SELECT push_name, full_name, first_name, business_name FROM contacts WHERE jid = ?`, jid).Scan(&push, &full, &first, &biz)
	if err == nil {
		return BestDisplayName(firstNonEmpty(full, first), biz, push, jid)
	}
	return BestDisplayName("", "", "", jid)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanChat(r rowScanner) (Chat, error) {
	var c Chat
	var archived, lastMine int
	var lastTS, mentionTS, upd int64
	err := r.Scan(
		&c.JID, &c.Kind, &c.Name, &c.Phone, &c.UnreadCount, &c.MentionCount, &c.MutedUntil, &archived,
		&c.LastMsgID, &lastTS, &c.LastMsgText, &c.LastMsgFrom, &lastMine,
		&c.LastMentionID, &mentionTS, &c.AvatarID, &c.AvatarURL, &c.MemberCount, &upd,
	)
	if err != nil {
		return Chat{}, err
	}
	c.Archived = archived != 0
	c.LastMsgMine = lastMine != 0
	if lastTS > 0 {
		c.LastMsgTS = time.Unix(lastTS, 0).UTC()
	}
	if mentionTS > 0 {
		c.LastMentionTS = time.Unix(mentionTS, 0).UTC()
	}
	if upd > 0 {
		c.UpdatedAt = time.Unix(upd, 0).UTC()
	}
	return c, nil
}

func getChatDB(db *sql.DB, jid string) (Chat, bool, error) {
	return getChatQ(db, jid)
}

func getChatTx(tx *sql.Tx, jid string) (Chat, bool, error) {
	return getChatQ(tx, jid)
}

func getChatQ(q queryRower, jid string) (Chat, bool, error) {
	row := q.QueryRow(`SELECT jid, kind, name, phone, unread_count, mention_count, muted_until, archived,
		last_msg_id, last_msg_ts, last_msg_text, last_msg_from, last_msg_mine,
		last_mention_id, last_mention_ts, avatar_id, avatar_url, member_count, updated_at
		FROM chats WHERE jid = ?`, jid)
	c, err := scanChat(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Chat{}, false, nil
	}
	if err != nil {
		return Chat{}, false, err
	}
	return c, true, nil
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func putChatDB(db *sql.DB, c Chat) error { return putChatE(db, c) }
func putChatTx(tx *sql.Tx, c Chat) error { return putChatE(tx, c) }

func putChatE(e execer, c Chat) error {
	var lastTS, mentionTS int64
	if !c.LastMsgTS.IsZero() {
		lastTS = c.LastMsgTS.Unix()
	}
	if !c.LastMentionTS.IsZero() {
		mentionTS = c.LastMentionTS.Unix()
	}
	upd := c.UpdatedAt.Unix()
	if c.UpdatedAt.IsZero() {
		upd = time.Now().Unix()
	}
	_, err := e.Exec(`INSERT INTO chats(jid, kind, name, phone, unread_count, mention_count, muted_until, archived,
		last_msg_id, last_msg_ts, last_msg_text, last_msg_from, last_msg_mine,
		last_mention_id, last_mention_ts, avatar_id, avatar_url, member_count, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			kind = excluded.kind,
			name = excluded.name,
			phone = excluded.phone,
			unread_count = excluded.unread_count,
			mention_count = excluded.mention_count,
			muted_until = excluded.muted_until,
			archived = excluded.archived,
			last_msg_id = excluded.last_msg_id,
			last_msg_ts = excluded.last_msg_ts,
			last_msg_text = excluded.last_msg_text,
			last_msg_from = excluded.last_msg_from,
			last_msg_mine = excluded.last_msg_mine,
			last_mention_id = excluded.last_mention_id,
			last_mention_ts = excluded.last_mention_ts,
			avatar_id = excluded.avatar_id,
			avatar_url = excluded.avatar_url,
			member_count = excluded.member_count,
			updated_at = excluded.updated_at`,
		c.JID, c.Kind, c.Name, c.Phone, c.UnreadCount, c.MentionCount, c.MutedUntil, boolInt(c.Archived),
		c.LastMsgID, lastTS, c.LastMsgText, c.LastMsgFrom, boolInt(c.LastMsgMine),
		c.LastMentionID, mentionTS, c.AvatarID, c.AvatarURL, c.MemberCount, upd)
	return err
}

func scanMessage(r rowScanner) (Message, error) {
	var m Message
	var mine, mentioned, edited int
	var ts int64
	var reactJSON, mediaJSON string
	err := r.Scan(
		&m.Seq, &m.ChatJID, &m.ID, &m.SenderJID, &m.SenderName, &mine, &m.Type, &m.Text, &ts,
		&m.ReplyToID, &m.ReplyToSender, &m.ReplyToText, &mentioned, &edited, &reactJSON, &mediaJSON,
	)
	if err != nil {
		return Message{}, err
	}
	m.Mine = mine != 0
	m.MentionedMe = mentioned != 0
	m.Edited = edited != 0
	if ts > 0 {
		m.At = time.Unix(ts, 0).UTC()
	}
	if reactJSON != "" {
		_ = json.Unmarshal([]byte(reactJSON), &m.Reactions)
	}
	if mediaJSON != "" {
		var mm MediaMeta
		if json.Unmarshal([]byte(mediaJSON), &mm) == nil {
			m.Media = &mm
		}
	}
	return m, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func buildFTSQuery(raw string) string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		clean := strings.ReplaceAll(f, `"`, "")
		if clean == "" {
			continue
		}
		parts = append(parts, `"`+clean+`"`)
	}
	return strings.Join(parts, " ")
}

func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
