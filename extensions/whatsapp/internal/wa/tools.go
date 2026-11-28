package wa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/prompts"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/proto"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/wa/spec"
)

const (
	lineCap         = 600
	textMost        = 4096
	chatsDef        = 30
	chatsMost       = 100
	messagesDef     = 30
	messagesMost    = 100
	searchDef       = 20
	searchMost      = 50
	contactsDef     = 50
	contactsMost    = 200
	backfillDef     = 50
	backfillMost    = 100
	markReadIDsMost = 50
)

var (
	backfillWait = 15 * time.Second
	zeroTime     time.Time
)

// flexString decodes either a JSON string or a JSON integer/number as a string.
// Persona hub's /thread/reply converts all-digit reply_to IDs to numbers on the wire.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err := dec.Decode(&n); err == nil {
		// Ensure it really was a JSON number, not boolean/object/array.
		if _, err := n.Int64(); err == nil {
			*f = flexString(n.String())
			return nil
		}
	}
	return fmt.Errorf("expected string, got %s", string(b))
}

type toolArgs struct {
	Chat            string     `json:"chat"`
	Limit           int        `json:"limit"`
	UnreadOnly      bool       `json:"unread_only"`
	IncludeArchived bool       `json:"include_archived"`
	Kind            string     `json:"kind"`
	Before          string     `json:"before"`
	Since           string     `json:"since"`
	Until           string     `json:"until"`
	Query           string     `json:"query"`
	ID              string     `json:"id"`
	IDs             []string   `json:"ids"`
	Count           int        `json:"count"`
	Text            string     `json:"text"`
	ReplyTo         flexString `json:"reply_to"`
	Emoji           *string    `json:"emoji"`
}

var allowedArgNames = func() map[string]bool {
	t := reflect.TypeFor[toolArgs]()
	out := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		out[t.Field(i).Tag.Get("json")] = true
	}
	return out
}()

func parseToolArgs(args json.RawMessage) (toolArgs, error) {
	var a toolArgs
	if len(bytes.TrimSpace(args)) == 0 {
		return a, nil
	}
	// Reject keys whose case doesn't match the schema exactly (matches Telegram's gate guarantee).
	var rawKeys map[string]json.RawMessage
	if err := json.NewDecoder(bytes.NewReader(args)).Decode(&rawKeys); err != nil {
		return a, fmt.Errorf("arguments: %w", err)
	}
	for k := range rawKeys {
		if !allowedArgNames[k] {
			return a, fmt.Errorf("arguments: unknown field %q", k)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, fmt.Errorf("arguments: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return a, errors.New("arguments: trailing data")
	}
	return a, nil
}

// Tools returns the read-only MCP tools (including on-demand local history backfill).
func (c *Conn) Tools() []proto.Tool {
	out := make([]proto.Tool, 0, len(spec.ReadTools))
	for _, name := range spec.ReadTools {
		t := prompts.Tool(name)
		out = append(out, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// Actions returns the write MCP tools; every one requires phone approval (ConfirmAlways).
func (c *Conn) Actions() []proto.Tool {
	out := make([]proto.Tool, 0, len(spec.WriteActions))
	for _, name := range spec.WriteActions {
		t := prompts.Tool(name)
		out = append(out, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// JSON response shapes for MCP tools.
type (
	chatOut struct {
		ID       string      `json:"id"`
		Name     string      `json:"name"`
		Kind     string      `json:"kind"` // dm | group
		Phone    string      `json:"phone,omitempty"`
		Unread   int         `json:"unread,omitempty"`
		Mentions int         `json:"mentions,omitempty"`
		Muted    bool        `json:"muted,omitempty"`
		Archived bool        `json:"archived,omitempty"`
		Members  int         `json:"members,omitempty"`
		Last     *messageOut `json:"last,omitempty"`
	}
	messageOut struct {
		ID          string            `json:"id"`
		At          string            `json:"at"`
		From        string            `json:"from,omitempty"`
		FromID      string            `json:"from_id,omitempty"`
		Mine        bool              `json:"mine,omitempty"`
		Type        string            `json:"type,omitempty"`
		Text        string            `json:"text"`
		ReplyTo     string            `json:"reply_to,omitempty"`
		ReplyToText string            `json:"reply_to_text,omitempty"`
		Mentioned   bool              `json:"mentioned,omitempty"`
		Edited      bool              `json:"edited,omitempty"`
		Media       *MediaMeta        `json:"media,omitempty"`
		Reactions   map[string]string `json:"reactions,omitempty"`
	}
	contactOut struct {
		ID           string `json:"id"`
		LID          string `json:"lid,omitempty"`
		Phone        string `json:"phone,omitempty"`
		Name         string `json:"name"`
		PushName     string `json:"push_name,omitempty"`
		BusinessName string `json:"business_name,omitempty"`
	}
	searchHitOut struct {
		Chat    chatOut    `json:"chat"`
		Message messageOut `json:"message"`
	}
)

func chatRef(ch Chat) chatOut {
	return chatOut{
		ID:    ch.JID,
		Name:  ch.Name,
		Kind:  ch.Kind,
		Phone: FormatPhoneDisplay(ch.Phone),
	}
}

func (c *Conn) chatSummary(ch Chat) chatOut {
	out := chatRef(ch)
	out.Unread = ch.UnreadCount
	out.Mentions = ch.MentionCount
	out.Muted = ch.IsMuted(c.now())
	out.Archived = ch.Archived
	out.Members = ch.MemberCount
	if !ch.LastMsgTS.IsZero() {
		from := ch.LastMsgFrom
		if ch.LastMsgMine {
			from = ""
		}
		out.Last = &messageOut{
			ID:   ch.LastMsgID,
			At:   c.stamp(ch.LastMsgTS),
			From: from,
			Mine: ch.LastMsgMine,
			Text: clipRunes(ch.LastMsgText, textMost),
		}
	}
	return out
}

func (c *Conn) messageSummary(m Message) messageOut {
	out := messageOut{
		ID:          m.ID,
		At:          c.stamp(m.At),
		From:        m.SenderName,
		FromID:      m.SenderJID,
		Mine:        m.Mine,
		Type:        strings.ToLower(m.Type),
		Text:        clipRunes(m.Text, textMost),
		ReplyTo:     m.ReplyToID,
		ReplyToText: m.ReplyToText,
		Mentioned:   m.MentionedMe,
		Edited:      m.Edited,
		Media:       m.Media,
		Reactions:   m.Reactions,
	}
	if m.Mine {
		out.From = ""
		out.FromID = ""
	}
	return out
}

func (c *Conn) parseWhen(s, what string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	zone := c.zone
	if zone == nil {
		zone = time.UTC
	}
	if t, err := time.ParseInLocation(time.DateOnly, s, zone); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%s: %q is neither an RFC 3339 time nor a YYYY-MM-DD date", what, s)
}

func bound(n, def, most int) int {
	if n <= 0 {
		return def
	}
	return min(n, most)
}

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// Call executes one of the read-only MCP tools.
func (c *Conn) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if c.store == nil {
		return "", errNotConnected
	}
	a, err := parseToolArgs(args)
	if err != nil {
		return "", err
	}
	since, err := c.parseWhen(a.Since, "since")
	if err != nil {
		return "", err
	}
	until, err := c.parseWhen(a.Until, "until")
	if err != nil {
		return "", err
	}

	switch name {
	case "whatsapp_list_chats":
		kind := strings.ToLower(strings.TrimSpace(a.Kind))
		if kind == "" {
			kind = "all"
		}
		if kind != "all" && kind != "dm" && kind != "group" {
			return "", fmt.Errorf("kind must be all, dm, or group (got %q)", a.Kind)
		}
		chats, more, err := c.store.ListChats(bound(a.Limit, chatsDef, chatsMost), a.UnreadOnly, a.IncludeArchived, kind)
		if err != nil {
			return "", err
		}
		out := struct {
			Chats []chatOut `json:"chats"`
			More  bool      `json:"more,omitempty"`
		}{Chats: make([]chatOut, 0, len(chats)), More: more}
		for _, ch := range chats {
			out.Chats = append(out.Chats, c.chatSummary(ch))
		}
		return jsonOf(out)

	case "whatsapp_read_messages":
		ch, err := c.store.ResolveChatInput(a.Chat)
		if err != nil {
			return "", err
		}
		msgs, nextBefore, err := c.store.ReadMessages(ch.JID, a.Before, since, until, bound(a.Limit, messagesDef, messagesMost))
		if err != nil {
			return "", err
		}
		out := struct {
			Chat       chatOut      `json:"chat"`
			Messages   []messageOut `json:"messages"`
			NextBefore string       `json:"next_before,omitempty"`
		}{
			Chat:       chatRef(ch),
			Messages:   make([]messageOut, 0, len(msgs)),
			NextBefore: nextBefore,
		}
		for _, m := range msgs {
			out.Messages = append(out.Messages, c.messageSummary(m))
		}
		return jsonOf(out)

	case "whatsapp_search_messages":
		if strings.TrimSpace(a.Query) == "" {
			return "", errors.New("query is required")
		}
		chatJID := ""
		if strings.TrimSpace(a.Chat) != "" {
			ch, err := c.store.ResolveChatInput(a.Chat)
			if err != nil {
				return "", err
			}
			chatJID = ch.JID
		}
		hits, err := c.store.SearchMessages(a.Query, chatJID, since, until, bound(a.Limit, searchDef, searchMost))
		if err != nil {
			return "", err
		}
		out := struct {
			Query   string         `json:"query"`
			Results []searchHitOut `json:"results"`
		}{Query: a.Query, Results: make([]searchHitOut, 0, len(hits))}
		for _, h := range hits {
			out.Results = append(out.Results, searchHitOut{
				Chat:    chatRef(h.Chat),
				Message: c.messageSummary(h.Message),
			})
		}
		return jsonOf(out)

	case "whatsapp_get_message":
		if strings.TrimSpace(a.ID) == "" {
			return "", errors.New("id is required")
		}
		ch, err := c.store.ResolveChatInput(a.Chat)
		if err != nil {
			return "", err
		}
		m, ok, err := c.store.GetMessage(ch.JID, a.ID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("%s 里没有找到消息 %s", ch.Name, a.ID)
		}
		return jsonOf(struct {
			Chat    chatOut    `json:"chat"`
			Message messageOut `json:"message"`
		}{Chat: chatRef(ch), Message: c.messageSummary(m)})

	case "whatsapp_list_contacts":
		contacts, more, err := c.store.ListContacts(a.Query, bound(a.Limit, contactsDef, contactsMost))
		if err != nil {
			return "", err
		}
		out := struct {
			Contacts []contactOut `json:"contacts"`
			More     bool         `json:"more,omitempty"`
		}{Contacts: make([]contactOut, 0, len(contacts)), More: more}
		for _, ct := range contacts {
			out.Contacts = append(out.Contacts, contactOut{
				ID:           ct.JID,
				LID:          ct.LID,
				Phone:        FormatPhoneDisplay(ct.Phone),
				Name:         ct.DisplayName(),
				PushName:     ct.PushName,
				BusinessName: ct.BusinessName,
			})
		}
		return jsonOf(out)

	case "whatsapp_history_backfill":
		return c.toolHistoryBackfill(ctx, a)

	default:
		return "", fmt.Errorf("unknown tool %s", name)
	}
}

func (c *Conn) toolHistoryBackfill(ctx context.Context, a toolArgs) (string, error) {
	if c.backend == nil || !c.backend.IsConnected() {
		return "", errNotConnected
	}
	ch, err := c.store.ResolveChatInput(a.Chat)
	if err != nil {
		return "", err
	}
	var anchorMsg Message
	var found bool
	if strings.TrimSpace(a.Before) != "" {
		anchorMsg, found, err = c.store.GetMessage(ch.JID, a.Before)
	} else {
		anchorMsg, found, err = c.store.OldestMessage(ch.JID)
	}
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("会话 %s 本地尚无锚点消息，无法向主设备请求更早历史", ch.Name)
	}
	count := bound(a.Count, backfillDef, backfillMost)
	waiter, cleanup := c.registerBackfillWaiter(ch.JID)
	defer cleanup()

	anchor := BackfillAnchor{
		ChatJID:   ch.JID,
		MessageID: anchorMsg.ID,
		SenderJID: anchorMsg.SenderJID,
		Mine:      anchorMsg.Mine,
		At:        anchorMsg.At,
	}
	if err := c.backend.RequestHistorySync(ctx, anchor, count); err != nil {
		return "", errors.New(scrubError(err))
	}

	fetched := 0
	status := "requested_pending"
	timer := time.NewTimer(backfillWait)
	defer timer.Stop()
	select {
	case n := <-waiter:
		fetched = n
		status = "synced"
	case <-timer.C:
	case <-ctx.Done():
		return "", ctx.Err()
	}

	total, _ := c.store.MessageCount(ch.JID)
	return jsonOf(struct {
		Chat           chatOut `json:"chat"`
		BeforeID       string  `json:"before_id"`
		RequestedCount int     `json:"requested_count"`
		Fetched        int     `json:"fetched"`
		TotalInChat    int     `json:"total_in_chat"`
		Status         string  `json:"status"`
	}{
		Chat:           chatRef(ch),
		BeforeID:       anchorMsg.ID,
		RequestedCount: count,
		Fetched:        fetched,
		TotalInChat:    total,
		Status:         status,
	})
}

// ---- Write Actions (every write goes through the Hub's confirmation gate) ----

type plannedWrite struct {
	name    string
	chat    Chat
	args    toolArgs
	replyTo *Message
	target  *Message
	emoji   string
}

// strictWriteTarget requires an exact canonical JID (no surrounding whitespace, no ambiguous display names)
// that exists in our local index or contact list so the confirmation card unambiguously identifies the target.
func (c *Conn) strictWriteTarget(rawChat string) (Chat, error) {
	if strings.TrimSpace(rawChat) == "" {
		return Chat{}, errors.New("chat is required")
	}
	if rawChat != strings.TrimSpace(rawChat) {
		return Chat{}, fmt.Errorf("会话 %q 前后不能有空白", rawChat)
	}
	if !strings.Contains(rawChat, "@") {
		return Chat{}, fmt.Errorf("写操作只认明确的会话 JID（如 <手机号>@s.whatsapp.net 或 <群id>@g.us），不认名字：%q", rawChat)
	}
	norm := NormalizeJID(rawChat)
	if !IsSupportedChatJID(norm) {
		return Chat{}, fmt.Errorf("不支持的 WhatsApp 会话 JID：%q", rawChat)
	}
	canon := CanonicalizeChatJID(norm, c.store)
	if c.store != nil {
		if ch, ok, err := c.store.GetChat(canon); err == nil && ok {
			return ch, nil
		}
		// Allow sending to a known synced contact even if no local message has been indexed yet.
		if IsPhoneJID(canon) {
			contacts, _, err := c.store.ListContacts(canon, 5)
			if err == nil {
				for _, ct := range contacts {
					if ct.JID == canon {
						return Chat{JID: canon, Kind: "dm", Name: ct.DisplayName(), Phone: ct.Phone}, nil
					}
				}
			}
		}
	}
	return Chat{}, fmt.Errorf("不认识会话 %q：请先用 whatsapp_list_chats 或 whatsapp_list_contacts 确认目标 JID", rawChat)
}

func (c *Conn) planWrite(name string, rawArgs json.RawMessage) (plannedWrite, error) {
	if !slices.Contains(spec.WriteActions, name) {
		return plannedWrite{}, fmt.Errorf("unknown action %s", name)
	}
	a, err := parseToolArgs(rawArgs)
	if err != nil {
		return plannedWrite{}, err
	}
	ch, err := c.strictWriteTarget(a.Chat)
	if err != nil {
		return plannedWrite{}, err
	}
	pw := plannedWrite{name: name, chat: ch, args: a}

	switch name {
	case "whatsapp_send":
		if strings.TrimSpace(a.Text) == "" {
			return pw, errors.New("text is required")
		}
		u16 := 0
		for _, r := range a.Text {
			u16 += utf16.RuneLen(r)
		}
		if u16 > textMost {
			return pw, fmt.Errorf("text has %d characters; WhatsApp allows at most %d per message", u16, textMost)
		}
		if rid := strings.TrimSpace(string(a.ReplyTo)); rid != "" {
			if c.store != nil {
				if m, ok, err := c.store.GetMessage(ch.JID, rid); err == nil && ok {
					pw.replyTo = &m
				}
			}
			if pw.replyTo == nil {
				pw.replyTo = &Message{ChatJID: ch.JID, ID: rid}
			}
		}

	case "whatsapp_react":
		id := strings.TrimSpace(a.ID)
		if id == "" {
			return pw, errors.New("id is required")
		}
		if a.Emoji == nil {
			return pw, errors.New("emoji is required (pass empty string to remove reaction)")
		}
		emoji := strings.TrimSpace(*a.Emoji)
		if emoji != "" && utf8.RuneCountInString(emoji) > 8 {
			return pw, errors.New("emoji must be a single emoji (e.g. 👍) or empty string")
		}
		pw.emoji = emoji
		if c.store != nil {
			if m, ok, err := c.store.GetMessage(ch.JID, id); err == nil && ok {
				pw.target = &m
			}
		}
		if pw.target == nil {
			pw.target = &Message{ChatJID: ch.JID, ID: id}
		}

	case "whatsapp_mark_read":
		if len(a.IDs) > markReadIDsMost {
			return pw, fmt.Errorf("ids: at most %d message ids at once", markReadIDsMost)
		}
		for _, id := range a.IDs {
			if strings.TrimSpace(id) == "" {
				return pw, errors.New("ids must not contain empty strings")
			}
		}
	}
	return pw, nil
}

func calledChat(ch Chat) string {
	kind := "私聊"
	if ch.Kind == "group" {
		kind = "群"
	}
	return fmt.Sprintf("%s（%s · %s）", ch.Name, kind, ch.JID)
}

func audienceOf(ch Chat) string {
	if ch.Kind == "group" {
		return ch.Name + "的所有成员"
	}
	return ch.Name
}

// Describe builds the offline phone confirmation card for every WhatsApp write action.
func (c *Conn) Describe(_ context.Context, name string, args json.RawMessage) (connector.Action, string, error) {
	pw, err := c.planWrite(name, args)
	if err != nil {
		return connector.Action{}, "", err
	}
	act := connector.Action{Name: name, Visible: audienceOf(pw.chat)}
	switch name {
	case "whatsapp_send":
		if pw.replyTo != nil {
			if pw.replyTo.Text != "" {
				return act, fmt.Sprintf("用你的 WhatsApp 回复 %s 里的消息 %s（「%s」）：\n\n%s",
					calledChat(pw.chat), pw.replyTo.ID, clipRunes(oneLine(pw.replyTo.Text), 40), pw.args.Text), nil
			}
			return act, fmt.Sprintf("用你的 WhatsApp 回复 %s 里的消息 %s：\n\n%s",
				calledChat(pw.chat), pw.replyTo.ID, pw.args.Text), nil
		}
		return act, fmt.Sprintf("用你的 WhatsApp 发给 %s：\n\n%s", calledChat(pw.chat), pw.args.Text), nil

	case "whatsapp_react":
		if pw.emoji == "" {
			return act, fmt.Sprintf("取消你在 %s 消息 %s 上的表态", calledChat(pw.chat), pw.target.ID), nil
		}
		return act, fmt.Sprintf("在 %s 的消息 %s 上点 %s", calledChat(pw.chat), pw.target.ID, pw.emoji), nil

	case "whatsapp_mark_read":
		act.Visible = audienceOf(pw.chat) + "（对方可能看到已读回执）"
		scope := "全部未读消息"
		if len(pw.args.IDs) > 0 {
			scope = fmt.Sprintf("%d 条指定消息", len(pw.args.IDs))
		}
		return act, fmt.Sprintf("把 %s 标为已读（%s）", calledChat(pw.chat), scope), nil
	}
	return connector.Action{}, "", fmt.Errorf("unknown action %s", name)
}

// Do executes a write action after the hub's confirmation gate approved it.
func (c *Conn) Do(ctx context.Context, name string, args json.RawMessage) (string, error) {
	pw, err := c.planWrite(name, args)
	if err != nil {
		return "", err
	}
	if c.backend == nil || !c.backend.IsConnected() {
		return "", errNotConnected
	}

	switch name {
	case "whatsapp_send":
		receipt, err := c.backend.SendText(ctx, pw.chat.JID, pw.args.Text, pw.replyTo)
		if err != nil {
			return "", errors.New(scrubError(err))
		}
		c.recordSentMessage(pw.chat.JID, receipt, pw.args.Text, pw.replyTo)
		replyID := ""
		if pw.replyTo != nil {
			replyID = pw.replyTo.ID
		}
		return jsonOf(struct {
			Sent messageOut `json:"sent"`
			Chat string     `json:"chat"`
		}{
			Sent: messageOut{
				ID:      receipt.ID,
				At:      c.stamp(receipt.At),
				Mine:    true,
				Type:    "text",
				Text:    pw.args.Text,
				ReplyTo: replyID,
			},
			Chat: pw.chat.JID,
		})

	case "whatsapp_react":
		err := c.backend.SendReaction(ctx, pw.chat.JID, pw.target.ID, pw.target.SenderJID, pw.target.Mine, pw.emoji)
		if err != nil {
			return "", errors.New(scrubError(err))
		}
		if c.store != nil {
			c.mu.Lock()
			selfJID := c.selfJID
			c.mu.Unlock()
			_ = c.store.ApplyReaction(pw.chat.JID, pw.target.ID, selfJID, pw.emoji)
		}
		return jsonOf(struct {
			Reacted string `json:"reacted"`
			Chat    string `json:"chat"`
			ID      string `json:"id"`
		}{
			Reacted: pw.emoji,
			Chat:    pw.chat.JID,
			ID:      pw.target.ID,
		})

	case "whatsapp_mark_read":
		bySender := map[string][]string{}
		if len(pw.args.IDs) > 0 {
			for _, id := range pw.args.IDs {
				id = strings.TrimSpace(id)
				sender := ""
				if c.store != nil {
					if m, ok, err := c.store.GetMessage(pw.chat.JID, id); err == nil && ok {
						sender = m.SenderJID
					}
				}
				if sender == "" && pw.chat.Kind == "dm" {
					sender = pw.chat.JID
				}
				bySender[sender] = append(bySender[sender], id)
			}
		} else if c.store != nil {
			limit := max(pw.chat.UnreadCount, 10)
			if limit > markReadIDsMost {
				limit = markReadIDsMost
			}
			grouped, err := c.store.RecentUnreadIncoming(pw.chat.JID, limit)
			if err != nil {
				return "", err
			}
			bySender = grouped
		}

		marked := 0
		for _, ids := range bySender {
			marked += len(ids)
		}
		if marked > 0 {
			if err := c.backend.MarkRead(ctx, pw.chat.JID, bySender, c.now()); err != nil {
				return "", errors.New(scrubError(err))
			}
		}
		if c.store != nil {
			_ = c.store.SetChatRead(pw.chat.JID, true)
			c.notifyInbox()
		}
		return jsonOf(struct {
			Read   string `json:"read"`
			Marked int    `json:"marked"`
		}{
			Read:   pw.chat.JID,
			Marked: marked,
		})
	}
	return "", fmt.Errorf("unknown action %s", name)
}
