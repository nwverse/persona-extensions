package wa

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
)

const avatarCacheCap = 512

var errNoMedia = errors.New("找不到该媒体资源")

func avatarRef(jid, avatarID string) string {
	jid = NormalizeJID(jid)
	avatarID = strings.TrimSpace(avatarID)
	if jid == "" || avatarID == "" {
		return ""
	}
	return "a." + base64.RawURLEncoding.EncodeToString([]byte(jid)) + "." + avatarID
}

func parseAvatarRef(ref string) (jid, avatarID string, ok bool) {
	parts := strings.Split(ref, ".")
	if len(parts) != 3 || parts[0] != "a" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}
	j := NormalizeJID(string(raw))
	if !IsSupportedChatJID(j) {
		return "", "", false
	}
	return j, parts[2], true
}

// Thread returns one page of conversation messages (oldest first) for the Persona app thread view.
func (c *Conn) Thread(_ context.Context, thread, before string, limit int) (connector.ThreadPage, error) {
	if c.store == nil {
		return connector.ThreadPage{}, errNotConnected
	}
	ch, err := c.store.ResolveChatInput(thread)
	if err != nil {
		return connector.ThreadPage{}, err
	}
	limit = min(max(limit, 1), connector.ThreadMost)
	msgs, nextBefore, err := c.store.ReadMessages(ch.JID, before, zeroTime, zeroTime, limit)
	if err != nil {
		return connector.ThreadPage{}, err
	}

	byID := make(map[string]Message, len(msgs))
	for _, m := range msgs {
		byID[m.ID] = m
	}

	page := connector.ThreadPage{
		Chat: connector.ThreadChat{
			ID:      ch.JID,
			Name:    ch.Name,
			Kind:    ch.Kind,
			URL:     OpenChatURL(ch.JID),
			Avatar:  avatarRef(ch.JID, ch.AvatarID),
			Members: ch.MemberCount,
			Reply:   "whatsapp_send",
		},
		Messages: make([]connector.ThreadMessage, 0, len(msgs)),
		Before:   nextBefore,
	}

	for _, m := range msgs {
		tm := connector.ThreadMessage{
			ID:     m.ID,
			At:     m.At,
			Mine:   m.Mine,
			Text:   m.Text,
			Edited: m.Edited,
		}
		if m.Mine {
			tm.Sender = connector.ThreadPerson{Name: "我"}
		} else {
			tm.Sender = connector.ThreadPerson{
				ID:   m.SenderJID,
				Name: m.SenderName,
			}
		}
		if m.ReplyToID != "" {
			q := connector.ThreadQuote{
				ID:   m.ReplyToID,
				From: m.ReplyToSender,
				Text: m.ReplyToText,
			}
			if orig, ok := byID[m.ReplyToID]; ok {
				if q.From == "" || q.From == orig.SenderJID {
					if orig.Mine {
						q.From = "我"
					} else {
						q.From = orig.SenderName
					}
				}
				if q.Text == "" {
					q.Text = clipRunes(oneLine(orig.Text), 200)
				}
			} else if orig, ok, err := c.store.GetMessage(ch.JID, m.ReplyToID); err == nil && ok {
				if orig.Mine {
					q.From = "我"
				} else if orig.SenderName != "" {
					q.From = orig.SenderName
				}
				if q.Text == "" {
					q.Text = clipRunes(oneLine(orig.Text), 200)
				}
			}
			tm.ReplyTo = &q
		}
		if m.Media != nil {
			tm.Media = []connector.ThreadMedia{{
				Kind:     m.Media.Kind,
				Mime:     m.Media.Mime,
				Name:     m.Media.Name,
				Size:     m.Media.Size,
				Duration: m.Media.Duration,
				Width:    m.Media.Width,
				Height:   m.Media.Height,
			}}
		}
		page.Messages = append(page.Messages, tm)
	}
	return page, nil
}

// Media serves avatar images referenced by InboxThing.Avatar and ThreadChat.Avatar.
func (c *Conn) Media(ctx context.Context, ref string) (connector.Media, error) {
	jid, _, ok := parseAvatarRef(ref)
	if !ok {
		return connector.Media{}, errNoMedia
	}
	c.avatarMu.Lock()
	if cached, hit := c.avatars[ref]; hit {
		c.avatarMu.Unlock()
		return cached, nil
	}
	c.avatarMu.Unlock()

	if c.backend == nil || !c.backend.IsConnected() {
		return connector.Media{}, errNotConnected
	}
	av, err := c.backend.FetchAvatar(ctx, jid)
	if err != nil {
		return connector.Media{}, errors.New(scrubError(err))
	}
	if av == nil || len(av.Data) == 0 {
		return connector.Media{}, errNoMedia
	}
	if c.store != nil && av.ID != "" {
		_ = c.store.SetChatAvatar(jid, av.ID, av.URL)
	}
	media := connector.Media{Mime: av.Mime, Data: av.Data}
	c.avatarMu.Lock()
	if len(c.avatars) >= avatarCacheCap {
		c.avatars = map[string]connector.Media{}
	}
	c.avatars[ref] = media
	c.avatarMu.Unlock()
	return media, nil
}
