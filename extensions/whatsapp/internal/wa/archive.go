package wa

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/kb"
)

type archiveCursor struct {
	Seq int64 `json:"seq"`
}

const archiveMsgsPerCall = 1000

// Changes streams newly-indexed WhatsApp messages from the local SQLite store into Persona's
// knowledge base using a monotonic sequence cursor. Backfilled historical messages get new
// sequence numbers and are automatically included in subsequent archive rounds.
func (c *Conn) Changes(_ context.Context, cursor string, limit int) ([]connector.ChatBatch, string, bool, error) {
	if c.store == nil {
		return nil, cursor, false, nil
	}
	var cur archiveCursor
	if cursor != "" {
		if err := json.Unmarshal([]byte(cursor), &cur); err != nil {
			return nil, cursor, false, fmt.Errorf("the archive cursor does not read: %w", err)
		}
	}
	if limit <= 0 {
		limit = 20
	}
	rawBatches, nextSeq, more, err := c.store.ArchiveSince(cur.Seq, limit, archiveMsgsPerCall)
	if err != nil {
		return nil, cursor, false, err
	}
	out := make([]connector.ChatBatch, 0, len(rawBatches))
	for _, rb := range rawBatches {
		if len(rb.Messages) == 0 {
			continue
		}
		out = append(out, connector.ChatBatch{
			Chat: kb.SourceChat{
				ID:        rb.Chat.JID,
				Name:      rb.Chat.Name,
				Kind:      rb.Chat.Kind,
				UpdatedAt: rb.Chat.LastMsgTS,
			},
			Messages: toSourceMessages(rb.Messages),
		})
	}
	cur.Seq = nextSeq
	c.mu.Lock()
	c.lastSync = c.now()
	c.mu.Unlock()
	b, _ := json.Marshal(cur)
	return out, string(b), more, nil
}

func toSourceMessages(msgs []Message) []kb.SourceMessage {
	byID := make(map[string]string, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m.Text
	}
	out := make([]kb.SourceMessage, 0, len(msgs))
	for _, m := range msgs {
		text := formatMessageTextWithLookup(m, byID)
		sm := kb.SourceMessage{
			ID:         m.ID,
			Position:   strconv.FormatInt(m.Seq, 10),
			SenderID:   m.SenderJID,
			SenderName: m.SenderName,
			Mine:       m.Mine,
			Type:       m.Type,
			Text:       text,
			At:         m.At,
		}
		if !m.Mine {
			if phone := PhoneFromJID(m.SenderJID); phone != "" {
				sm.SenderHandles = []kb.Handle{{Kind: kb.HandlePhone, Value: "+" + phone}}
			}
		}
		out = append(out, sm)
	}
	return out
}

func formatMessageText(m Message) string {
	return formatMessageTextWithLookup(m, nil)
}

func formatMessageTextWithLookup(m Message, lookup map[string]string) string {
	t := m.Text
	if m.ReplyToID != "" {
		quoted := m.ReplyToText
		if quoted == "" && lookup != nil {
			quoted = lookup[m.ReplyToID]
		}
		if quoted != "" {
			t = "回复「" + clipRunes(oneLine(quoted), 30) + "」：" + t
		} else {
			t = "回复一条消息：" + t
		}
	}
	return t
}
