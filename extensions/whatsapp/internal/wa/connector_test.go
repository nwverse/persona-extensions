package wa

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/kb"
	"github.com/nwverse/persona-extensions/pluginkit"
)

func setupSeededConn(t *testing.T, cfg Config) (*Conn, *fakeBackend, *Store, time.Time) {
	t.Helper()
	st, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	be := &fakeBackend{
		hasSession: true,
		connected:  true,
		account:    "Me (+12025550199)",
		jid:        "12025550199@s.whatsapp.net",
		lid:        "99990001@lid",
		avatars: map[string]*AvatarData{
			"12025550101@s.whatsapp.net": {ID: "av1", Mime: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff, 0xd9}},
		},
	}
	conn := NewWithDeps(be, st, time.UTC, cfg)
	conn.now = func() time.Time { return now }
	conn.selfJID = be.jid
	conn.selfLID = be.lid
	conn.status = connector.Status{State: connector.StateOK, Account: be.account, Via: conn.viaLabel()}

	// Seed contacts and LID mapping.
	_ = st.UpsertContact(Contact{JID: "12025550101@s.whatsapp.net", LID: "11110001@lid", FullName: "Alice Chen", PushName: "Alice"})
	_ = st.UpsertContact(Contact{JID: "12025550102@s.whatsapp.net", PushName: "Bob"})

	// 1. Unread DM from Alice.
	_, _ = st.UpsertMessage(Message{
		ChatJID:    "12025550101@s.whatsapp.net",
		ID:         "DM1",
		SenderJID:  "12025550101@s.whatsapp.net",
		SenderName: "Alice Chen",
		Type:       "TEXT",
		Text:       "Hi, can you review the release checklist?",
		At:         now.Add(-10 * time.Minute),
	}, Chat{Kind: "dm", Name: "Alice Chen"}, true)
	_ = st.SetChatAvatar("12025550101@s.whatsapp.net", "av1", "https://pps.whatsapp.net/example")

	// 2. Group with an @mention.
	_, _ = st.UpsertMessage(Message{
		ChatJID:    "120363000000001@g.us",
		ID:         "GRP1",
		SenderJID:  "12025550102@s.whatsapp.net",
		SenderName: "Bob",
		Type:       "TEXT",
		Text:       "Regular chat message without mention",
		At:         now.Add(-20 * time.Minute),
	}, Chat{Kind: "group", Name: "Release Team", MemberCount: 8}, true)
	_, _ = st.UpsertMessage(Message{
		ChatJID:     "120363000000001@g.us",
		ID:          "GRP2",
		SenderJID:   "12025550101@s.whatsapp.net",
		SenderName:  "Alice Chen",
		Type:        "TEXT",
		Text:        "@me please confirm deployment window",
		At:          now.Add(-5 * time.Minute),
		MentionedMe: true,
	}, Chat{Kind: "group", Name: "Release Team", MemberCount: 8}, true)

	// 3. Group WITHOUT any @mention -> must NOT enter Inbox.
	_, _ = st.UpsertMessage(Message{
		ChatJID:    "120363000000002@g.us",
		ID:         "NOISY1",
		SenderJID:  "12025550102@s.whatsapp.net",
		SenderName: "Bob",
		Type:       "TEXT",
		Text:       "Random chatter",
		At:         now.Add(-3 * time.Minute),
	}, Chat{Kind: "group", Name: "Random Group"}, true)

	// 4. Muted DM -> must NOT enter Inbox.
	_, _ = st.UpsertMessage(Message{
		ChatJID:    "12025550103@s.whatsapp.net",
		ID:         "MUTE1",
		SenderJID:  "12025550103@s.whatsapp.net",
		SenderName: "Muted Contact",
		Type:       "TEXT",
		Text:       "Promo alert",
		At:         now.Add(-2 * time.Minute),
	}, Chat{Kind: "dm", Name: "Muted Contact"}, true)
	_ = st.SetChatMute("12025550103@s.whatsapp.net", -1)

	return conn, be, st, now
}

func TestInboxRulesTriageAndDirectReply(t *testing.T) {
	ctx := context.Background()

	// Without triage_rules: default TierAction + fallback RuleTier.
	conn, be, st, _ := setupSeededConn(t, DefaultConfig())
	things, err := conn.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginkit.CheckThings(things, "", true); err != nil {
		t.Fatalf("CheckThings(no rules): %v", err)
	}
	if len(things) != 2 {
		t.Fatalf("expected 2 inbox things (1 DM + 1 mentioned group), got %d: %+v", len(things), things)
	}
	byThread := map[string]connector.InboxThing{}
	for _, th := range things {
		byThread[th.Thread] = th
	}
	dm := byThread["12025550101@s.whatsapp.net"]
	if dm.Kind != "dm" || dm.Tier != connector.TierAction || !dm.CanReply || dm.Read || dm.Done != "" || dm.OpenURL != "https://wa.me/12025550101" {
		t.Fatalf("unexpected DM inbox thing: %+v", dm)
	}
	grp := byThread["120363000000001@g.us"]
	if grp.Kind != "mention" || grp.Last != "GRP2" || !grp.CanReply || grp.Read {
		t.Fatalf("unexpected Group inbox thing: %+v", grp)
	}

	// With triage_rules: Tier cleared so hub model runs, Extra.RuleTier kept as fallback.
	rules := "工作群 @我 需要处理\n陌生私聊算噪音"
	conn2, _, _, _ := setupSeededConn(t, Config{QuietPresence: true, MaxStorageMB: 128, TriageRules: rules})
	things2, err := conn2.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginkit.CheckThings(things2, rules, true); err != nil {
		t.Fatalf("CheckThings(with rules): %v", err)
	}
	for _, th := range things2 {
		if th.Tier != "" || th.Rules != rules || th.Extra == nil || th.Extra.RuleTier != string(connector.TierAction) {
			t.Fatalf("triage_rules stamping mismatch: %+v", th)
		}
	}

	// Inbox Reply sends once (deduplicated by ClientID), quotes in group, and marks DM replied.
	err = conn.Reply(ctx, connector.InboxReply{
		Thread:   "120363000000001@g.us",
		Last:     "GRP2",
		Text:     "Deployment window confirmed for 14:00 UTC.",
		ClientID: "req-inbox-1",
	})
	if err != nil {
		t.Fatalf("Inbox Reply: %v", err)
	}
	// Duplicate retry with same ClientID must be a no-op.
	if err := conn.Reply(ctx, connector.InboxReply{
		Thread:   "120363000000001@g.us",
		Last:     "GRP2",
		Text:     "Deployment window confirmed for 14:00 UTC.",
		ClientID: "req-inbox-1",
	}); err != nil {
		t.Fatalf("Inbox Reply retry: %v", err)
	}
	if len(be.sentTexts) != 1 || be.sentTexts[0].replyTo != "GRP2" {
		t.Fatalf("expected 1 quoted group reply, got %+v", be.sentTexts)
	}

	// Marking Alice's DM read elsewhere sets Read=true on next Collect.
	_ = st.SetChatRead("12025550101@s.whatsapp.net", true)
	thingsAfter, _ := conn.Collect(ctx)
	for _, th := range thingsAfter {
		if th.Thread == "12025550101@s.whatsapp.net" && !th.Read {
			t.Fatalf("expected Alice DM Read=true after SetChatRead, got %+v", th)
		}
	}
}

func TestArchiveThreadsAndMedia(t *testing.T) {
	ctx := context.Background()
	conn, _, _, _ := setupSeededConn(t, DefaultConfig())

	// 1. Archive streams batches with phone handles and advances cursor monotonically.
	batches, cur1, more, err := conn.Changes(ctx, "", 2)
	if err != nil || len(batches) != 2 || !more {
		t.Fatalf("Changes(page 1): len=%d more=%v err=%v", len(batches), more, err)
	}
	foundPhoneHandle := false
	for _, b := range batches {
		for _, m := range b.Messages {
			for _, h := range m.SenderHandles {
				if h.Kind == kb.HandlePhone && strings.HasPrefix(h.Value, "+1202") {
					foundPhoneHandle = true
				}
			}
		}
	}
	if !foundPhoneHandle {
		t.Fatalf("expected phone handle in archived messages: %+v", batches)
	}
	batches2, cur2, more2, err := conn.Changes(ctx, cur1, 10)
	if err != nil || len(batches2) == 0 || more2 {
		t.Fatalf("Changes(page 2): len=%d more=%v err=%v", len(batches2), more2, err)
	}
	// Third call with latest cursor returns zero new batches.
	batches3, _, _, err := conn.Changes(ctx, cur2, 10)
	if err != nil || len(batches3) != 0 {
		t.Fatalf("Changes(page 3): expected 0 batches, got %d (%v)", len(batches3), err)
	}

	// 2. Threads pagination and avatar Media fetch.
	page, err := conn.Thread(ctx, "120363000000001@g.us", "", 1)
	if err != nil {
		t.Fatalf("Thread(newest): %v", err)
	}
	if page.Chat.Reply != "whatsapp_send" || page.Chat.Members != 8 || len(page.Messages) != 1 || page.Messages[0].ID != "GRP2" || page.Before != "GRP2" {
		t.Fatalf("unexpected thread page 1: %+v", page)
	}
	older, err := conn.Thread(ctx, "120363000000001@g.us", page.Before, 5)
	if err != nil || len(older.Messages) != 1 || older.Messages[0].ID != "GRP1" || older.Before != "" {
		t.Fatalf("unexpected older thread page: %+v err=%v", older, err)
	}

	dmPage, err := conn.Thread(ctx, "12025550101@s.whatsapp.net", "", 10)
	if err != nil || dmPage.Chat.Avatar == "" {
		t.Fatalf("expected DM avatar ref, got %+v err=%v", dmPage.Chat, err)
	}
	media, err := conn.Media(ctx, dmPage.Chat.Avatar)
	if err != nil || media.Mime != "image/jpeg" || len(media.Data) != 4 {
		t.Fatalf("Media(avatar): got %+v err=%v", media, err)
	}
}

func TestReadToolsCatalogAndBackfill(t *testing.T) {
	ctx := context.Background()
	origWait := backfillWait
	backfillWait = 500 * time.Millisecond
	t.Cleanup(func() { backfillWait = origWait })

	conn, be, st, now := setupSeededConn(t, DefaultConfig())

	// 1. whatsapp_list_chats
	raw, err := conn.Call(ctx, "whatsapp_list_chats", json.RawMessage(`{"unread_only":true}`))
	if err != nil || !strings.Contains(raw, "12025550101@s.whatsapp.net") {
		t.Fatalf("list_chats: raw=%s err=%v", raw, err)
	}
	// Reject unknown fields strictly.
	if _, err := conn.Call(ctx, "whatsapp_list_chats", json.RawMessage(`{"bogus":true}`)); err == nil {
		t.Fatal("expected error on unknown field")
	}

	// 2. whatsapp_read_messages (accepts phone number or exact name)
	raw, err = conn.Call(ctx, "whatsapp_read_messages", json.RawMessage(`{"chat":"Alice Chen"}`))
	if err != nil || !strings.Contains(raw, "release checklist") {
		t.Fatalf("read_messages: raw=%s err=%v", raw, err)
	}

	// 3. whatsapp_search_messages
	raw, err = conn.Call(ctx, "whatsapp_search_messages", json.RawMessage(`{"query":"deployment"}`))
	if err != nil || !strings.Contains(raw, "GRP2") {
		t.Fatalf("search_messages: raw=%s err=%v", raw, err)
	}

	// 4. whatsapp_get_message
	raw, err = conn.Call(ctx, "whatsapp_get_message", json.RawMessage(`{"chat":"12025550101@s.whatsapp.net","id":"DM1"}`))
	if err != nil || !strings.Contains(raw, "DM1") {
		t.Fatalf("get_message: raw=%s err=%v", raw, err)
	}

	// 5. whatsapp_list_contacts
	raw, err = conn.Call(ctx, "whatsapp_list_contacts", json.RawMessage(`{"query":"Alice"}`))
	if err != nil || !strings.Contains(raw, "11110001@lid") {
		t.Fatalf("list_contacts: raw=%s err=%v", raw, err)
	}

	// 6. whatsapp_history_backfill (simulate primary phone responding asynchronously with ON_DEMAND batch)
	be.onBackfill = func(anchor BackfillAnchor, _ int) {
		go func() {
			_, _ = st.UpsertMessage(Message{
				ChatJID:    anchor.ChatJID,
				ID:         "DM0_OLD",
				SenderJID:  anchor.ChatJID,
				SenderName: "Alice Chen",
				Type:       "TEXT",
				Text:       "Older synced message from primary phone",
				At:         now.Add(-48 * time.Hour),
			}, Chat{Kind: "dm", Name: "Alice Chen"}, false)
			conn.signalBackfill(anchor.ChatJID, 1)
		}()
	}
	raw, err = conn.Call(ctx, "whatsapp_history_backfill", json.RawMessage(`{"chat":"12025550101@s.whatsapp.net","count":25}`))
	if err != nil || !strings.Contains(raw, `"status":"synced"`) || !strings.Contains(raw, `"fetched":1`) {
		t.Fatalf("history_backfill: raw=%s err=%v", raw, err)
	}

	// 7. Catalog lists only read-only tools and answers with unique <chat>:<id> item IDs.
	sources, err := conn.Catalog(ctx)
	if err != nil || len(sources) != 1 || len(sources[0].Tools) != 6 {
		t.Fatalf("Catalog: %+v err=%v", sources, err)
	}
	catOut, err := conn.CatalogCall(ctx, connector.CatalogQuery{
		Source: "whatsapp",
		Tool:   "read_messages",
		Args:   json.RawMessage(`{"chat":"12025550101@s.whatsapp.net"}`),
	})
	if err != nil || !strings.Contains(catOut, `"id":"12025550101@s.whatsapp.net:DM1"`) {
		t.Fatalf("CatalogCall(read_messages): %s err=%v", catOut, err)
	}
	if _, err := conn.CatalogCall(ctx, connector.CatalogQuery{Source: "whatsapp", Tool: "send", Args: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("CatalogCall must refuse write action 'send'")
	}
}

func TestWriteActionsApprovalGateAndExecution(t *testing.T) {
	ctx := context.Background()
	conn, be, _, _ := setupSeededConn(t, DefaultConfig())

	// 1. ConfirmAlways guarantees even a blanket allow rule still requires phone confirmation.
	allowAll := []connector.Rule{{Connector: "*", Action: "*", Decision: connector.Allow}}
	for _, action := range []string{"whatsapp_send", "whatsapp_react", "whatsapp_mark_read"} {
		if got := connector.Decide(allowAll, conn.Manifest(), conn.ID(), action, json.RawMessage(`{}`)); got != connector.Confirm {
			t.Fatalf("action %s: Decide with allow-all rule = %q, want %q", action, got, connector.Confirm)
		}
	}

	// 2. Write target validation refuses display names and surrounding spaces.
	if _, _, err := conn.Describe(ctx, "whatsapp_send", json.RawMessage(`{"chat":"Alice Chen","text":"hi"}`)); err == nil {
		t.Fatal("whatsapp_send must refuse ambiguous display name")
	}
	if _, _, err := conn.Describe(ctx, "whatsapp_send", json.RawMessage(`{"chat":" 12025550101@s.whatsapp.net ","text":"hi"}`)); err == nil {
		t.Fatal("whatsapp_send must refuse surrounding spaces")
	}

	// 3. Describe & Do for whatsapp_send (including quoted reply and numeric reply_to compatibility).
	sendArgs := json.RawMessage(`{"chat":"12025550101@s.whatsapp.net","text":"Looks great, shipping now.","reply_to":"DM1"}`)
	act, summary, err := conn.Describe(ctx, "whatsapp_send", sendArgs)
	if err != nil || act.Visible != "Alice Chen" || !strings.Contains(summary, "Looks great, shipping now.") || !strings.Contains(summary, "DM1") {
		t.Fatalf("Describe(whatsapp_send): act=%+v summary=%q err=%v", act, summary, err)
	}
	// Numeric reply_to from hub /thread/reply must also decode cleanly.
	if _, _, err := conn.Describe(ctx, "whatsapp_send", json.RawMessage(`{"chat":"12025550101@s.whatsapp.net","text":"ok","reply_to":12345}`)); err != nil {
		t.Fatalf("Describe(numeric reply_to): %v", err)
	}
	out, err := conn.Do(ctx, "whatsapp_send", sendArgs)
	if err != nil || !strings.Contains(out, `"chat":"12025550101@s.whatsapp.net"`) || len(be.sentTexts) != 1 || be.sentTexts[0].replyTo != "DM1" {
		t.Fatalf("Do(whatsapp_send): out=%s sent=%+v err=%v", out, be.sentTexts, err)
	}

	// 4. Describe & Do for whatsapp_react (uses group message sender JID from local store).
	reactArgs := json.RawMessage(`{"chat":"120363000000001@g.us","id":"GRP2","emoji":"👍"}`)
	act, summary, err = conn.Describe(ctx, "whatsapp_react", reactArgs)
	if err != nil || !strings.Contains(act.Visible, "Release Team") || !strings.Contains(summary, "👍") {
		t.Fatalf("Describe(whatsapp_react): act=%+v summary=%q err=%v", act, summary, err)
	}
	if _, err := conn.Do(ctx, "whatsapp_react", reactArgs); err != nil {
		t.Fatalf("Do(whatsapp_react): %v", err)
	}
	if len(be.sentReacts) != 1 || be.sentReacts[0].sender != "12025550101@s.whatsapp.net" || be.sentReacts[0].emoji != "👍" {
		t.Fatalf("unexpected react call: %+v", be.sentReacts)
	}

	// 5. Describe & Do for whatsapp_mark_read groups group messages by sender JID.
	readArgs := json.RawMessage(`{"chat":"120363000000001@g.us"}`)
	act, summary, err = conn.Describe(ctx, "whatsapp_mark_read", readArgs)
	if err != nil || !strings.Contains(act.Visible, "已读回执") || !strings.Contains(summary, "Release Team") {
		t.Fatalf("Describe(whatsapp_mark_read): act=%+v summary=%q err=%v", act, summary, err)
	}
	out, err = conn.Do(ctx, "whatsapp_mark_read", readArgs)
	if err != nil || !strings.Contains(out, `"marked":2`) {
		t.Fatalf("Do(whatsapp_mark_read): out=%s err=%v", out, err)
	}
	if len(be.sentReads) != 1 || len(be.sentReads[0].bySender) != 2 {
		t.Fatalf("expected MarkRead grouped across 2 distinct senders in group, got %+v", be.sentReads)
	}
}
