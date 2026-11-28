package wa

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
)

const (
	inboxDays         = 8 * 24 * time.Hour
	inboxChatsLimit   = 150
	inboxContextLines = 10
	inboxReplyDedup   = 10 * time.Minute
)

// Collect builds InboxThings purely from the local SQLite index (zero WhatsApp requests):
//   - Unread direct messages (`kind: "dm"`) where the other party wrote last; closes automatically
//     when read elsewhere (`Read: true`) or when the user replied (`Done: DoneReplied`).
//   - Group chats with unread @mentions or replies to the user (`kind: "mention"`); closes when read.
//   - Muted and archived chats are excluded from the inbox (they remain archived to the knowledge base).
//   - Carries `Extra.RuleTier: "action"` as fallback (CapFallback) and stamps user `triage_rules` when set.
func (c *Conn) Collect(ctx context.Context) ([]connector.InboxThing, error) {
	if c.store == nil {
		return nil, nil
	}
	chats, _, err := c.store.ListChats(inboxChatsLimit, false, false, "all")
	if err != nil {
		return nil, err
	}
	now := c.now()
	var out []connector.InboxThing
	for _, ch := range chats {
		if ch.Archived || ch.IsMuted(now) || ch.LastMsgTS.IsZero() || now.Sub(ch.LastMsgTS) > inboxDays {
			continue
		}
		if ch.Kind != "dm" && ch.Kind != "group" {
			continue
		}

		ctxText := c.buildInboxContext(ch)
		tags := []connector.Tag{{Key: connector.TagChat, Value: ch.JID, Name: ch.Name}}
		if ch.Kind == "dm" {
			tags = append(tags, connector.Tag{Key: connector.TagSender, Value: ch.JID, Name: ch.Name})
		}

		thing := connector.InboxThing{
			Thread:   ch.JID,
			Title:    ch.Name,
			Sender:   ch.LastMsgFrom,
			Preview:  clipRunes(oneLine(ch.LastMsgText), 240),
			Context:  ctxText,
			At:       ch.LastMsgTS,
			OpenURL:  OpenChatURL(ch.JID),
			CanReply: true,
			Avatar:   avatarRef(ch.JID, ch.AvatarID),
			Tier:     connector.TierAction,
			Tags:     tags,
			Extra:    &connector.ThingExtra{RuleTier: string(connector.TierAction)},
		}
		if c.cfg.TriageRules != "" {
			thing.Tier = ""
			thing.Rules = c.cfg.TriageRules
		}

		switch ch.Kind {
		case "dm":
			thing.Kind = "dm"
			thing.Label = "私聊"
			thing.Last = ch.LastMsgID
			thing.ReplyTo = fmt.Sprintf("回复 %s（WhatsApp 私聊）", ch.Name)
			switch {
			case ch.LastMsgMine:
				thing.Done = connector.DoneReplied
			case ch.UnreadCount == 0:
				thing.Read = true
			}
		case "group":
			if ch.LastMentionID == "" {
				continue
			}
			thing.Kind = "mention"
			thing.Label = "@你"
			thing.Last = ch.LastMentionID
			thing.ReplyTo = fmt.Sprintf("回复群聊 %s（群内所有成员可见）", ch.Name)
			if ch.MentionCount == 0 {
				thing.Read = true
			}
		}
		out = append(out, thing)
	}
	return out, nil
}

func (c *Conn) buildInboxContext(ch Chat) string {
	msgs, err := c.store.RecentMessagesForContext(ch.JID, inboxContextLines)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	kindLabel := "WhatsApp 私聊"
	if ch.Kind == "group" {
		kindLabel = "WhatsApp 群"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s（%s）\n", ch.Name, kindLabel)
	for _, m := range msgs {
		from := m.SenderName
		if m.Mine {
			from = "我"
		}
		line := formatMessageText(m)
		fmt.Fprintf(&b, "%s %s：%s\n", c.when(m.At), from, clipRunes(oneLine(line), lineCap))
	}
	return b.String()
}

// Reply sends the user's confirmed inbox reply directly to the WhatsApp conversation.
// Retries carrying the same ClientID are deduplicated so a timeout never double-sends.
func (c *Conn) Reply(ctx context.Context, r connector.InboxReply) error {
	chatJID := CanonicalizeChatJID(r.Thread, c.store)
	if !IsSupportedChatJID(chatJID) {
		return fmt.Errorf("无效的 WhatsApp 会话 %q", r.Thread)
	}
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return errors.New("回复内容不能为空")
	}
	if runeCount := len([]rune(text)); runeCount > textMost {
		return fmt.Errorf("回复内容有 %d 字，WhatsApp 单条最多 %d 字", runeCount, textMost)
	}
	if c.backend == nil || !c.backend.IsConnected() {
		return errNotConnected
	}

	if r.ClientID != "" {
		c.replyMu.Lock()
		c.pruneReplyCacheLocked()
		if _, dup := c.sentReply[r.ClientID]; dup {
			c.replyMu.Unlock()
			return nil
		}
		c.replyMu.Unlock()
	}

	// Quote the referenced message in group chats when we have it locally.
	var quote *Message
	if r.Last != "" && IsGroupJID(chatJID) && c.store != nil {
		if m, ok, err := c.store.GetMessage(chatJID, r.Last); err == nil && ok {
			quote = &m
		}
	}

	receipt, err := c.backend.SendText(ctx, chatJID, text, quote)
	if err != nil {
		return errors.New(scrubError(err))
	}

	if r.ClientID != "" {
		c.replyMu.Lock()
		c.sentReply[r.ClientID] = c.now()
		c.replyMu.Unlock()
	}

	// Record the sent reply locally and clear unread/mention counters immediately.
	c.recordSentMessage(chatJID, receipt, text, quote)
	return nil
}

func (c *Conn) pruneReplyCacheLocked() {
	cutoff := c.now().Add(-inboxReplyDedup)
	for id, at := range c.sentReply {
		if at.Before(cutoff) {
			delete(c.sentReply, id)
		}
	}
}

func (c *Conn) recordSentMessage(chatJID string, receipt SendReceipt, text string, quote *Message) {
	if c.store == nil {
		return
	}
	c.mu.Lock()
	selfJID := c.selfJID
	c.mu.Unlock()
	msg := Message{
		ChatJID:    chatJID,
		ID:         receipt.ID,
		SenderJID:  selfJID,
		SenderName: "我",
		Mine:       true,
		Type:       "TEXT",
		Text:       text,
		At:         receipt.At,
	}
	if quote != nil {
		msg.ReplyToID = quote.ID
		msg.ReplyToSender = quote.SenderJID
		msg.ReplyToText = clipRunes(oneLine(quote.Text), 200)
	}
	_, _ = c.store.UpsertMessage(msg, Chat{JID: chatJID, Kind: ChatKind(chatJID)}, true)
	_ = c.store.SetChatRead(chatJID, true)
	c.notifyChats()
	c.notifyInbox()
}
