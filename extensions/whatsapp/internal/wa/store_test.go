package wa

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestStoreDedupSearchLIDMergeAndStorageCap(t *testing.T) {
	st, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer st.Close()

	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	// 1. Insert live message and replay duplicate -> no duplicate row, unread incremented only once.
	m1 := Message{
		ChatJID:    "12025550101@s.whatsapp.net",
		ID:         "MSG001",
		SenderJID:  "12025550101@s.whatsapp.net",
		SenderName: "Alice",
		Type:       "TEXT",
		Text:       "Let's discuss the quarterly roadmap tomorrow",
		At:         base,
	}
	ins1, err := st.UpsertMessage(m1, Chat{Name: "Alice"}, true)
	if err != nil || !ins1 {
		t.Fatalf("first upsert: (%v, %v)", ins1, err)
	}
	ins2, err := st.UpsertMessage(m1, Chat{Name: "Alice"}, true)
	if err != nil || ins2 {
		t.Fatalf("duplicate upsert should return inserted=false: (%v, %v)", ins2, err)
	}
	ch, ok, err := st.GetChat("12025550101@s.whatsapp.net")
	if err != nil || !ok || ch.UnreadCount != 1 {
		t.Fatalf("chat unread count = %d (ok=%v err=%v), want 1", ch.UnreadCount, ok, err)
	}

	// 2. Insert an older history backfill message -> must NOT overwrite chat.LastMsgID.
	mOld := Message{
		ChatJID:    "12025550101@s.whatsapp.net",
		ID:         "MSG000",
		SenderJID:  "12025550101@s.whatsapp.net",
		SenderName: "Alice",
		Type:       "TEXT",
		Text:       "昨天发给你的发布计划请查收",
		At:         base.Add(-24 * time.Hour),
	}
	if _, err := st.UpsertMessage(mOld, Chat{Name: "Alice"}, false); err != nil {
		t.Fatalf("older upsert: %v", err)
	}
	ch, _, _ = st.GetChat("12025550101@s.whatsapp.net")
	if ch.LastMsgID != "MSG001" {
		t.Fatalf("older backfill overwrote LastMsgID: got %q, want MSG001", ch.LastMsgID)
	}

	// 3. FTS5 search for English AND CJK substring search for Chinese.
	hitsEN, err := st.SearchMessages("quarterly roadmap", "", time.Time{}, time.Time{}, 10)
	if err != nil || len(hitsEN) != 1 || hitsEN[0].Message.ID != "MSG001" {
		t.Fatalf("English FTS5 search failed: hits=%+v err=%v", hitsEN, err)
	}
	hitsZH, err := st.SearchMessages("发布计划", "", time.Time{}, time.Time{}, 10)
	if err != nil || len(hitsZH) != 1 || hitsZH[0].Message.ID != "MSG000" {
		t.Fatalf("Chinese search failed: hits=%+v err=%v", hitsZH, err)
	}

	// 4. Edit updates text AND FTS5 index.
	if err := st.ApplyEdit("12025550101@s.whatsapp.net", "MSG001", "Updated milestone schedule"); err != nil {
		t.Fatalf("ApplyEdit: %v", err)
	}
	hitsOld, _ := st.SearchMessages("quarterly", "", time.Time{}, time.Time{}, 10)
	if len(hitsOld) != 0 {
		t.Fatalf("FTS5 still matched pre-edit text: %+v", hitsOld)
	}
	hitsNew, _ := st.SearchMessages("milestone", "", time.Time{}, time.Time{}, 10)
	if len(hitsNew) != 1 || !hitsNew[0].Message.Edited {
		t.Fatalf("FTS5 did not match edited text: %+v", hitsNew)
	}

	// 5. Reactions add and remove cleanly.
	if err := st.ApplyReaction("12025550101@s.whatsapp.net", "MSG001", "12025550199@s.whatsapp.net", "👍"); err != nil {
		t.Fatalf("ApplyReaction add: %v", err)
	}
	gotMsg, ok, _ := st.GetMessage("12025550101@s.whatsapp.net", "MSG001")
	if !ok || gotMsg.Reactions["12025550199@s.whatsapp.net"] != "👍" {
		t.Fatalf("reaction not stored: %+v", gotMsg.Reactions)
	}
	_ = st.ApplyReaction("12025550101@s.whatsapp.net", "MSG001", "12025550199@s.whatsapp.net", "")
	gotMsg, _, _ = st.GetMessage("12025550101@s.whatsapp.net", "MSG001")
	if len(gotMsg.Reactions) != 0 {
		t.Fatalf("reaction not removed: %+v", gotMsg.Reactions)
	}

	// 6. LID -> Phone JID merge consolidates chat, messages, and contact without duplication.
	lidJID := "44556677@lid"
	pnJID := "12025550122@s.whatsapp.net"
	_, _ = st.UpsertMessage(Message{ChatJID: lidJID, ID: "L1", SenderJID: lidJID, SenderName: "Bob", Text: "From LID", At: base}, Chat{Name: "Bob"}, true)
	_, _ = st.UpsertMessage(Message{ChatJID: pnJID, ID: "P1", SenderJID: pnJID, SenderName: "Bob", Text: "From PN", At: base.Add(time.Minute)}, Chat{Name: "Bob"}, true)
	_, _ = st.UpsertMessage(Message{ChatJID: lidJID, ID: "P1", SenderJID: lidJID, SenderName: "Bob", Text: "Duplicate P1", At: base.Add(time.Minute)}, Chat{Name: "Bob"}, false)

	if err := st.PutLIDMapping(lidJID, pnJID); err != nil {
		t.Fatalf("PutLIDMapping: %v", err)
	}
	if _, exists, _ := st.GetChat(lidJID); !exists {
		t.Fatal("GetChat(lid) should transparently resolve to merged PN chat")
	}
	mergedMsgs, _, err := st.ReadMessages(pnJID, "", time.Time{}, time.Time{}, 10)
	if err != nil || len(mergedMsgs) != 2 {
		t.Fatalf("expected 2 deduplicated merged messages, got %d (%v)", len(mergedMsgs), err)
	}
	if mergedMsgs[0].ID != "L1" || mergedMsgs[0].SenderJID != pnJID || mergedMsgs[1].ID != "P1" {
		t.Fatalf("unexpected merged messages: %+v", mergedMsgs)
	}

	// 7. Storage cap enforcement prunes oldest messages while keeping newest message per chat.
	for i := range 25 {
		_, err := st.UpsertMessage(Message{
			ChatJID:    "120363000000001@g.us",
			ID:         fmt.Sprintf("G%03d", i),
			SenderJID:  "12025550101@s.whatsapp.net",
			SenderName: "Alice",
			Text:       fmt.Sprintf("group message number %d", i),
			At:         base.Add(time.Duration(i) * time.Minute),
		}, Chat{Kind: "group", Name: "Alpha Group"}, false)
		if err != nil {
			t.Fatalf("insert group msg %d: %v", i, err)
		}
	}
	pruned, err := st.EnforceStorageCap(context.Background(), 0, 10)
	if err != nil || pruned == 0 {
		t.Fatalf("EnforceStorageCap: pruned=%d err=%v, want >0", pruned, err)
	}
	remaining, _ := st.MessageCount("")
	if remaining > 10 {
		t.Fatalf("remaining messages %d exceeded cap 10", remaining)
	}
	// Newest group message must survive pruning.
	if _, ok, _ := st.GetMessage("120363000000001@g.us", "G024"); !ok {
		t.Fatal("newest message G024 should be preserved during storage cap pruning")
	}

	// 8. Account switch wipes previous account's local index cleanly.
	if err := st.ResetForAccount("12025550199@s.whatsapp.net"); err != nil {
		t.Fatalf("ResetForAccount(A): %v", err)
	}
	if err := st.ResetForAccount("12025550188@s.whatsapp.net"); err != nil {
		t.Fatalf("ResetForAccount(B): %v", err)
	}
	if cnt, _ := st.MessageCount(""); cnt != 0 {
		t.Fatalf("expected index wiped on account switch, got %d messages", cnt)
	}
}
