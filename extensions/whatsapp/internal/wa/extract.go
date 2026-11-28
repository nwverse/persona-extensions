package wa

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// ExtractedEvent represents the normalized interpretation of a whatsmeow *events.Message.
type ExtractedEvent struct {
	Kind string // "message" | "reaction" | "edit" | "revoke" | "ignore"

	Message Message

	// For reaction/edit/revoke events:
	TargetID  string
	Emoji     string
	NewText   string
	SenderJID string
	ChatJID   string

	// Any LID <-> PN mappings learned from the message source.
	LIDPairs []LIDPair
}

// ExtractMessageEvent converts a whatsmeow *events.Message into a normalized ExtractedEvent.
// selfPN and selfLID are the user's own canonical JIDs used to detect @mentions and replies to self.
func ExtractMessageEvent(evt *events.Message, selfPN, selfLID string, r JIDResolver) ExtractedEvent {
	if evt == nil || evt.Message == nil {
		return ExtractedEvent{Kind: "ignore"}
	}
	info := evt.Info
	var pairs []LIDPair
	// Collect any LID <-> PN pairs revealed by MessageSource alt fields.
	pairs = appendLIDPair(pairs, info.Sender, info.SenderAlt)
	if !info.IsGroup {
		pairs = appendLIDPair(pairs, info.Chat, info.RecipientAlt)
		if !info.IsFromMe {
			pairs = appendLIDPair(pairs, info.Chat, info.SenderAlt)
		}
	}

	rawChat := CanonicalJIDFromTypes(info.Chat)
	if IsLID(rawChat) && !info.RecipientAlt.IsEmpty() && IsPhoneJID(CanonicalJIDFromTypes(info.RecipientAlt)) {
		rawChat = CanonicalJIDFromTypes(info.RecipientAlt)
	}
	chatJID := CanonicalizeChatJID(rawChat, r)
	if !IsSupportedChatJID(chatJID) {
		return ExtractedEvent{Kind: "ignore", LIDPairs: pairs}
	}

	rawSender := CanonicalJIDFromTypes(info.Sender)
	if IsLID(rawSender) && !info.SenderAlt.IsEmpty() && IsPhoneJID(CanonicalJIDFromTypes(info.SenderAlt)) {
		rawSender = CanonicalJIDFromTypes(info.SenderAlt)
	}
	senderJID := CanonicalizeChatJID(rawSender, r)

	msg := unwrapMessage(evt.Message)
	if msg == nil {
		return ExtractedEvent{Kind: "ignore", LIDPairs: pairs}
	}

	// 1. Reaction message
	if rx := msg.GetReactionMessage(); rx != nil {
		targetID := rx.GetKey().GetID()
		if targetID == "" {
			return ExtractedEvent{Kind: "ignore", LIDPairs: pairs}
		}
		return ExtractedEvent{
			Kind:      "reaction",
			ChatJID:   chatJID,
			TargetID:  targetID,
			SenderJID: senderJID,
			Emoji:     rx.GetText(),
			LIDPairs:  pairs,
		}
	}

	// 2. Protocol message (edit / revoke)
	if pm := msg.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			if tid := pm.GetKey().GetID(); tid != "" {
				return ExtractedEvent{Kind: "revoke", ChatJID: chatJID, TargetID: tid, LIDPairs: pairs}
			}
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			if tid := pm.GetKey().GetID(); tid != "" {
				_, newText, _, _ := extractBody(pm.GetEditedMessage())
				return ExtractedEvent{Kind: "edit", ChatJID: chatJID, TargetID: tid, NewText: newText, LIDPairs: pairs}
			}
		}
		return ExtractedEvent{Kind: "ignore", LIDPairs: pairs}
	}

	// 3. Normal content message
	msgType, text, media, ctxInfo := extractBody(msg)
	if msgType == "" && text == "" && media == nil {
		return ExtractedEvent{Kind: "ignore", LIDPairs: pairs}
	}

	var replyID, replySender, replyText string
	mentionedMe := false
	if ctxInfo != nil {
		if sid := strings.TrimSpace(ctxInfo.GetStanzaID()); sid != "" {
			replyID = sid
			replySender = CanonicalizeChatJID(ctxInfo.GetParticipant(), r)
			if qm := ctxInfo.GetQuotedMessage(); qm != nil {
				_, qt, _, _ := extractBody(qm)
				replyText = clipRunes(oneLine(qt), 200)
			}
			// Replying to our own message in a group counts as a mention/reply-to-me.
			if info.IsGroup && !info.IsFromMe && matchesSelf(replySender, selfPN, selfLID, r) {
				mentionedMe = true
			}
		}
		if info.IsGroup && !info.IsFromMe && !mentionedMe {
			for _, mJID := range ctxInfo.GetMentionedJID() {
				if matchesSelf(mJID, selfPN, selfLID, r) {
					mentionedMe = true
					break
				}
			}
		}
	}

	rec := Message{
		ChatJID:       chatJID,
		ID:            info.ID,
		SenderJID:     senderJID,
		SenderName:    strings.TrimSpace(info.PushName),
		Mine:          info.IsFromMe,
		Type:          msgType,
		Text:          text,
		At:            info.Timestamp.UTC(),
		ReplyToID:     replyID,
		ReplyToSender: replySender,
		ReplyToText:   replyText,
		MentionedMe:   mentionedMe,
		Edited:        evt.IsEdit,
		Media:         media,
	}
	return ExtractedEvent{
		Kind:     "message",
		Message:  rec,
		ChatJID:  chatJID,
		LIDPairs: pairs,
	}
}

func unwrapMessage(m *waE2E.Message) *waE2E.Message {
	for range 4 {
		if m == nil {
			return nil
		}
		switch {
		case m.GetDeviceSentMessage() != nil && m.GetDeviceSentMessage().GetMessage() != nil:
			m = m.GetDeviceSentMessage().GetMessage()
		case m.GetEphemeralMessage() != nil && m.GetEphemeralMessage().GetMessage() != nil:
			m = m.GetEphemeralMessage().GetMessage()
		case m.GetViewOnceMessage() != nil && m.GetViewOnceMessage().GetMessage() != nil:
			m = m.GetViewOnceMessage().GetMessage()
		case m.GetViewOnceMessageV2() != nil && m.GetViewOnceMessageV2().GetMessage() != nil:
			m = m.GetViewOnceMessageV2().GetMessage()
		case m.GetViewOnceMessageV2Extension() != nil && m.GetViewOnceMessageV2Extension().GetMessage() != nil:
			m = m.GetViewOnceMessageV2Extension().GetMessage()
		case m.GetDocumentWithCaptionMessage() != nil && m.GetDocumentWithCaptionMessage().GetMessage() != nil:
			m = m.GetDocumentWithCaptionMessage().GetMessage()
		case m.GetEditedMessage() != nil && m.GetEditedMessage().GetMessage() != nil:
			m = m.GetEditedMessage().GetMessage()
		default:
			return m
		}
	}
	return m
}

func extractBody(m *waE2E.Message) (msgType, text string, media *MediaMeta, ctxInfo *waE2E.ContextInfo) {
	m = unwrapMessage(m)
	if m == nil {
		return "", "", nil, nil
	}
	switch {
	case m.GetConversation() != "":
		return "TEXT", m.GetConversation(), nil, nil
	case m.GetExtendedTextMessage() != nil:
		et := m.GetExtendedTextMessage()
		return "TEXT", et.GetText(), nil, et.GetContextInfo()
	case m.GetImageMessage() != nil:
		im := m.GetImageMessage()
		caption := strings.TrimSpace(im.GetCaption())
		t := "[图片]"
		if caption != "" {
			t = "[图片] " + caption
		}
		return "IMAGE", t, &MediaMeta{
			Kind:   "image",
			Mime:   im.GetMimetype(),
			Size:   int64(im.GetFileLength()),
			Width:  int(im.GetWidth()),
			Height: int(im.GetHeight()),
		}, im.GetContextInfo()
	case m.GetVideoMessage() != nil:
		vm := m.GetVideoMessage()
		caption := strings.TrimSpace(vm.GetCaption())
		t := "[视频]"
		if caption != "" {
			t = "[视频] " + caption
		}
		return "VIDEO", t, &MediaMeta{
			Kind:     "video",
			Mime:     vm.GetMimetype(),
			Size:     int64(vm.GetFileLength()),
			Duration: int(vm.GetSeconds()),
			Width:    int(vm.GetWidth()),
			Height:   int(vm.GetHeight()),
		}, vm.GetContextInfo()
	case m.GetAudioMessage() != nil:
		am := m.GetAudioMessage()
		kind := "voice"
		label := "[语音]"
		if !am.GetPTT() {
			label = "[音频]"
		}
		if sec := am.GetSeconds(); sec > 0 {
			label = fmt.Sprintf("%s %ds", label, sec)
		}
		return "VOICE", label, &MediaMeta{
			Kind:     kind,
			Mime:     am.GetMimetype(),
			Size:     int64(am.GetFileLength()),
			Duration: int(am.GetSeconds()),
		}, am.GetContextInfo()
	case m.GetDocumentMessage() != nil:
		dm := m.GetDocumentMessage()
		name := strings.TrimSpace(dm.GetFileName())
		if name == "" {
			name = strings.TrimSpace(dm.GetTitle())
		}
		t := "[文件]"
		if name != "" {
			t = "[文件] " + name
		}
		if capText := strings.TrimSpace(dm.GetCaption()); capText != "" {
			t += "：" + capText
		}
		return "FILE", t, &MediaMeta{
			Kind: "file",
			Mime: dm.GetMimetype(),
			Name: name,
			Size: int64(dm.GetFileLength()),
		}, dm.GetContextInfo()
	case m.GetStickerMessage() != nil:
		sm := m.GetStickerMessage()
		return "STICKER", "[贴纸]", &MediaMeta{
			Kind:   "sticker",
			Mime:   sm.GetMimetype(),
			Size:   int64(sm.GetFileLength()),
			Width:  int(sm.GetWidth()),
			Height: int(sm.GetHeight()),
		}, sm.GetContextInfo()
	case m.GetLocationMessage() != nil:
		lm := m.GetLocationMessage()
		name := firstNonEmpty(lm.GetName(), lm.GetAddress())
		if name != "" {
			return "LOCATION", fmt.Sprintf("[位置] %s (%.5f, %.5f)", name, lm.GetDegreesLatitude(), lm.GetDegreesLongitude()), nil, lm.GetContextInfo()
		}
		return "LOCATION", fmt.Sprintf("[位置] (%.5f, %.5f)", lm.GetDegreesLatitude(), lm.GetDegreesLongitude()), nil, lm.GetContextInfo()
	case m.GetContactMessage() != nil:
		cm := m.GetContactMessage()
		name := strings.TrimSpace(cm.GetDisplayName())
		if name == "" {
			name = "联系人名片"
		}
		return "CONTACT", "[名片] " + name, nil, cm.GetContextInfo()
	case m.GetContactsArrayMessage() != nil:
		ca := m.GetContactsArrayMessage()
		return "CONTACT", fmt.Sprintf("[名片] %s（%d 个联系人）", firstNonEmpty(ca.GetDisplayName(), "联系人"), len(ca.GetContacts())), nil, ca.GetContextInfo()
	}
	return "", "", nil, nil
}

func matchesSelf(candidate, selfPN, selfLID string, r JIDResolver) bool {
	norm := NormalizeJID(candidate)
	if norm == "" {
		return false
	}
	selfPN = NormalizeJID(selfPN)
	selfLID = NormalizeJID(selfLID)
	if selfPN != "" && norm == selfPN {
		return true
	}
	if selfLID != "" && norm == selfLID {
		return true
	}
	if IsLID(norm) && r != nil && selfPN != "" {
		if pn, ok := r.ResolveLIDToPN(norm); ok && NormalizeJID(pn) == selfPN {
			return true
		}
	}
	return false
}

func appendLIDPair(pairs []LIDPair, a, b types.JID) []LIDPair {
	sa := CanonicalJIDFromTypes(a)
	sb := CanonicalJIDFromTypes(b)
	if sa == "" || sb == "" {
		return pairs
	}
	if IsLID(sa) && IsPhoneJID(sb) {
		return append(pairs, LIDPair{LID: sa, PN: sb})
	}
	if IsLID(sb) && IsPhoneJID(sa) {
		return append(pairs, LIDPair{LID: sb, PN: sa})
	}
	return pairs
}

func clipRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
