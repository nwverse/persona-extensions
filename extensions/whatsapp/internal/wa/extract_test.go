package wa

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestExtractMessageEvents(t *testing.T) {
	selfPN := "12025550199@s.whatsapp.net"
	selfLID := "88887777@lid"
	res := mapResolver{"44445555@lid": "12025550101@s.whatsapp.net"}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

	// 1. Group message mentioning self by LID inside EphemeralMessage wrapper.
	evtMention := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:      types.NewJID("120363000000001", types.GroupServer),
				Sender:    types.NewJID("44445555", types.HiddenUserServer),
				SenderAlt: types.NewJID("12025550101", types.DefaultUserServer),
				IsGroup:   true,
			},
			ID:        "M_MENTION",
			PushName:  "Alice",
			Timestamp: now,
		},
		Message: &waE2E.Message{
			EphemeralMessage: &waE2E.FutureProofMessage{
				Message: &waE2E.Message{
					ExtendedTextMessage: &waE2E.ExtendedTextMessage{
						Text: proto.String("@me please review"),
						ContextInfo: &waE2E.ContextInfo{
							MentionedJID: []string{selfLID},
						},
					},
				},
			},
		},
	}
	got := ExtractMessageEvent(evtMention, selfPN, selfLID, res)
	if got.Kind != "message" || !got.Message.MentionedMe || got.Message.SenderJID != "12025550101@s.whatsapp.net" {
		t.Fatalf("mention extraction failed: %+v", got)
	}
	if len(got.LIDPairs) != 1 || got.LIDPairs[0].LID != "44445555@lid" || got.LIDPairs[0].PN != "12025550101@s.whatsapp.net" {
		t.Fatalf("expected LID pair extracted from SenderAlt: %+v", got.LIDPairs)
	}

	// 2. Group message replying to self's message counts as MentionedMe.
	evtReply := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:    types.NewJID("120363000000001", types.GroupServer),
				Sender:  types.NewJID("12025550102", types.DefaultUserServer),
				IsGroup: true,
			},
			ID:        "M_REPLY",
			PushName:  "Bob",
			Timestamp: now,
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String("Sounds good!"),
				ContextInfo: &waE2E.ContextInfo{
					StanzaID:      proto.String("MY_ORIG_1"),
					Participant:   proto.String(selfPN),
					QuotedMessage: &waE2E.Message{Conversation: proto.String("Shall we meet at 3pm?")},
				},
			},
		},
	}
	got = ExtractMessageEvent(evtReply, selfPN, selfLID, res)
	if got.Kind != "message" || !got.Message.MentionedMe || got.Message.ReplyToID != "MY_ORIG_1" || got.Message.ReplyToText != "Shall we meet at 3pm?" {
		t.Fatalf("reply-to-self extraction failed: %+v", got)
	}

	// 3. Reaction message.
	evtReact := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:   types.NewJID("12025550101", types.DefaultUserServer),
				Sender: types.NewJID("12025550101", types.DefaultUserServer),
			},
			ID:        "M_RX",
			Timestamp: now,
		},
		Message: &waE2E.Message{
			ReactionMessage: &waE2E.ReactionMessage{
				Key:  &waCommon.MessageKey{ID: proto.String("M_ORIG")},
				Text: proto.String("🔥"),
			},
		},
	}
	got = ExtractMessageEvent(evtReact, selfPN, selfLID, res)
	if got.Kind != "reaction" || got.TargetID != "M_ORIG" || got.Emoji != "🔥" {
		t.Fatalf("reaction extraction failed: %+v", got)
	}

	// 4. Edit and Revoke protocol messages.
	evtEdit := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("12025550101", types.DefaultUserServer)},
			ID:            "M_ED_WRAP",
		},
		Message: &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{
				Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
				Key:           &waCommon.MessageKey{ID: proto.String("M_ORIG")},
				EditedMessage: &waE2E.Message{Conversation: proto.String("Fixed typo")},
			},
		},
	}
	got = ExtractMessageEvent(evtEdit, selfPN, selfLID, res)
	if got.Kind != "edit" || got.TargetID != "M_ORIG" || got.NewText != "Fixed typo" {
		t.Fatalf("edit extraction failed: %+v", got)
	}

	evtRevoke := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.NewJID("12025550101", types.DefaultUserServer)},
		},
		Message: &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_REVOKE.Enum(),
				Key:  &waCommon.MessageKey{ID: proto.String("M_DEL")},
			},
		},
	}
	got = ExtractMessageEvent(evtRevoke, selfPN, selfLID, res)
	if got.Kind != "revoke" || got.TargetID != "M_DEL" {
		t.Fatalf("revoke extraction failed: %+v", got)
	}

	// 5. Status broadcast ignored.
	evtStatus := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: types.StatusBroadcastJID},
			ID:            "ST1",
		},
		Message: &waE2E.Message{Conversation: proto.String("My status")},
	}
	if got := ExtractMessageEvent(evtStatus, selfPN, selfLID, res); got.Kind != "ignore" {
		t.Fatalf("expected status broadcast ignored, got %+v", got)
	}
}
