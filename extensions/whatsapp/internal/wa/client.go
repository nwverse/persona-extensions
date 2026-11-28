package wa

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

var setDevicePropsOnce sync.Once

func initDeviceProps() {
	setDevicePropsOnce.Do(func() {
		store.SetOSInfo("Persona", [3]uint32{0, 1, 0})
		store.DeviceProps.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
	})
}

// waClient is the real go.mau.fi/whatsmeow implementation of Backend.
type waClient struct {
	vault *DeviceVault
	store *Store

	mu          sync.Mutex
	cli         *whatsmeow.Client
	handlerID   uint32
	connected   bool
	quiet       bool
	cb          BackendCallbacks
	proxy       ProxyConfig
	flushCancel context.CancelFunc
}

// NewWhatsmeowBackend initializes the encrypted Hub DeviceVault and returns a real whatsmeow Backend.
func NewWhatsmeowBackend(ctx context.Context, secrets connector.Secrets, idx *Store) (Backend, error) {
	initDeviceProps()
	vault, err := OpenDeviceVault(ctx, secrets)
	if err != nil {
		return nil, err
	}
	fctx, fcancel := context.WithCancel(context.Background())
	go vault.RunFlusher(fctx, 2*time.Second)
	return &waClient{
		vault:       vault,
		store:       idx,
		flushCancel: fcancel,
	}, nil
}

func (w *waClient) HasSession(ctx context.Context) bool {
	return w.vault.HasPairedDevice(ctx)
}

func (w *waClient) AccountInfo() (account, jid, lid string) {
	account, jid, lid, _ = w.vault.Meta()
	if account == "" && jid != "" {
		account = BestDisplayName("", "", "", jid)
	}
	return account, jid, lid
}

func (w *waClient) IsConnected() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cli != nil && w.connected && w.cli.IsConnected() && w.cli.IsLoggedIn()
}

func (w *waClient) Connect(ctx context.Context, quietPresence bool, cb BackendCallbacks) error {
	px, err := ResolveProxyFromEnv()
	if err != nil {
		return err
	}
	dev, err := w.vault.Device(ctx)
	if err != nil {
		return err
	}
	if dev == nil || dev.ID == nil || dev.ID.IsEmpty() {
		return errors.New("not signed in to WhatsApp")
	}

	cli := whatsmeow.NewClient(dev, waLog.Noop)
	cli.EnableAutoReconnect = true
	cli.AutoTrustIdentity = true
	if err := px.ApplyToClient(cli); err != nil {
		return err
	}

	w.mu.Lock()
	w.cli = cli
	w.quiet = quietPresence
	w.cb = cb
	w.proxy = px
	w.connected = false
	w.mu.Unlock()

	doneCh := make(chan error, 1)
	finish := func(err error) {
		select {
		case doneCh <- err:
		default:
		}
	}

	hid := cli.AddEventHandler(func(rawEvt any) {
		w.handleEvent(ctx, cli, rawEvt, finish)
	})
	w.mu.Lock()
	w.handlerID = hid
	w.mu.Unlock()

	if err := cli.Connect(); err != nil {
		cli.RemoveEventHandler(hid)
		w.mu.Lock()
		if w.cli == cli {
			w.cli = nil
			w.connected = false
		}
		w.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		w.disconnectClient(cli, hid)
		return ctx.Err()
	case err := <-doneCh:
		w.disconnectClient(cli, hid)
		return err
	}
}

func (w *waClient) disconnectClient(cli *whatsmeow.Client, hid uint32) {
	if cli == nil {
		return
	}
	cli.RemoveEventHandler(hid)
	cli.Disconnect()
	w.mu.Lock()
	if w.cli == cli {
		w.cli = nil
		w.connected = false
	}
	w.mu.Unlock()
}

func (w *waClient) Disconnect() {
	w.mu.Lock()
	cli, hid := w.cli, w.handlerID
	w.cli = nil
	w.connected = false
	w.mu.Unlock()
	if cli != nil {
		cli.RemoveEventHandler(hid)
		cli.Disconnect()
	}
}

func (w *waClient) handleEvent(ctx context.Context, cli *whatsmeow.Client, rawEvt any, finish func(error)) {
	w.mu.Lock()
	cb := w.cb
	quiet := w.quiet
	w.mu.Unlock()

	switch evt := rawEvt.(type) {
	case *events.Connected:
		w.mu.Lock()
		w.connected = true
		w.mu.Unlock()
		jid := CanonicalJIDFromTypes(cli.Store.GetJID())
		lid := CanonicalJIDFromTypes(cli.Store.GetLID())
		push := strings.TrimSpace(cli.Store.PushName)
		account := formatAccountLabel(push, jid)
		w.vault.SetMeta(account, jid, lid, push)
		go func() {
			fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = w.vault.Flush(fctx, true)
			cancel()
		}()
		// Apply quiet presence (unavailable) by default so contacts do not see 24/7 online status.
		go w.applyPresence(ctx, cli, quiet)
		// Sync joined groups and contact list asynchronously so group titles/headcounts stay accurate.
		go w.syncMetadataAfterConnect(ctx, cli, cb)
		if cb.OnConnected != nil {
			cb.OnConnected(account, jid, lid)
		}

	case *events.Disconnected:
		w.mu.Lock()
		w.connected = false
		w.mu.Unlock()
		if cb.OnDisconnected != nil {
			cb.OnDisconnected(errors.New("disconnected from WhatsApp"))
		}

	case *events.KeepAliveTimeout:
		if evt.ErrorCount >= 3 {
			finish(errors.New("WhatsApp keepalive timed out"))
		}

	case *events.StreamReplaced:
		w.mu.Lock()
		w.connected = false
		w.mu.Unlock()
		if cb.OnStreamReplaced != nil {
			cb.OnStreamReplaced()
		}
		finish(errors.New("WhatsApp session replaced by another connection"))

	case *events.LoggedOut:
		w.mu.Lock()
		w.connected = false
		w.mu.Unlock()
		reason := "device unlinked from primary phone"
		if evt.OnConnect {
			reason = fmt.Sprintf("logged out on connect (%s)", evt.Reason.String())
		}
		_ = w.vault.Clear(context.Background())
		if cb.OnLoggedOut != nil {
			cb.OnLoggedOut(reason)
		}
		finish(errors.New(reason))

	case *events.Message:
		w.processMessageEvent(cli, evt, true, cb)

	case *events.HistorySync:
		w.processHistorySync(ctx, cli, evt, cb)

	case *events.Receipt:
		if evt.Type == types.ReceiptTypeRead || evt.Type == types.ReceiptTypeReadSelf {
			if evt.IsFromMe || evt.Type == types.ReceiptTypeReadSelf {
				chatJID := w.canonicalize(cli, evt.Chat)
				if chatJID != "" && cb.OnChatRead != nil {
					cb.OnChatRead(chatJID, true)
				}
			}
		}

	case *events.MarkChatAsRead:
		if evt.Action != nil {
			chatJID := w.canonicalize(cli, evt.JID)
			if chatJID != "" && cb.OnChatRead != nil {
				cb.OnChatRead(chatJID, evt.Action.GetRead())
			}
		}

	case *events.Mute:
		if evt.Action != nil {
			chatJID := w.canonicalize(cli, evt.JID)
			if chatJID != "" && cb.OnChatMute != nil {
				var until int64
				if evt.Action.GetMuted() {
					until = evt.Action.GetMuteEndTimestamp()
					if until == 0 {
						until = -1
					} else if until > 1e12 {
						until /= 1000
					}
				}
				cb.OnChatMute(chatJID, until)
			}
		}

	case *events.Archive:
		if evt.Action != nil {
			chatJID := w.canonicalize(cli, evt.JID)
			if chatJID != "" && cb.OnChatArchive != nil {
				cb.OnChatArchive(chatJID, evt.Action.GetArchived())
			}
		}

	case *events.Contact:
		if evt.Action != nil && cb.OnContact != nil {
			jid := w.canonicalize(cli, evt.JID)
			cb.OnContact(Contact{
				JID:       jid,
				Phone:     PhoneFromJID(jid),
				FullName:  evt.Action.GetFullName(),
				FirstName: evt.Action.GetFirstName(),
			})
		}

	case *events.PushName:
		if cb.OnContact != nil {
			for _, pair := range appendLIDPair(nil, evt.JID, evt.JIDAlt) {
				if cb.OnLIDMapping != nil {
					cb.OnLIDMapping(pair)
				}
			}
			jid := w.canonicalize(cli, evt.JID)
			if IsLID(jid) && !evt.JIDAlt.IsEmpty() {
				jid = w.canonicalize(cli, evt.JIDAlt)
			}
			cb.OnContact(Contact{
				JID:      jid,
				Phone:    PhoneFromJID(jid),
				PushName: evt.NewPushName,
			})
		}

	case *events.GroupInfo:
		if cb.OnGroup != nil && evt.Name != nil && evt.Name.Name != "" {
			cb.OnGroup(CanonicalJIDFromTypes(evt.JID), evt.Name.Name, 0)
		}

	case *events.JoinedGroup:
		if cb.OnGroup != nil {
			cb.OnGroup(CanonicalJIDFromTypes(evt.JID), evt.Name, len(evt.Participants))
		}
	}
}

func (w *waClient) applyPresence(ctx context.Context, cli *whatsmeow.Client, quiet bool) {
	if cli == nil || !cli.IsConnected() {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	state := types.PresenceUnavailable
	if !quiet {
		state = types.PresenceAvailable
	}
	// SendPresence returns ErrNoPushName when PushName is empty; ignore cleanly.
	_ = cli.SendPresence(pctx, state)
}

func (w *waClient) syncMetadataAfterConnect(ctx context.Context, cli *whatsmeow.Client, cb BackendCallbacks) {
	if cli == nil || !cli.IsConnected() {
		return
	}
	// Sync saved contacts from whatsmeow's device store.
	if cli.Store != nil && cli.Store.Contacts != nil && cb.OnContact != nil {
		if all, err := cli.Store.Contacts.GetAllContacts(ctx); err == nil {
			for jid, info := range all {
				canon := w.canonicalize(cli, jid)
				if !IsPhoneJID(canon) && !IsLID(canon) {
					continue
				}
				cb.OnContact(Contact{
					JID:          canon,
					Phone:        PhoneFromJID(canon),
					PushName:     info.PushName,
					FullName:     info.FullName,
					FirstName:    info.FirstName,
					BusinessName: info.BusinessName,
				})
			}
		}
	}
	// Sync joined groups for authoritative group subjects and member counts.
	if cb.OnGroup != nil {
		gctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		groups, err := cli.GetJoinedGroups(gctx)
		cancel()
		if err == nil {
			for _, g := range groups {
				if g == nil || g.JID.IsEmpty() {
					continue
				}
				cb.OnGroup(CanonicalJIDFromTypes(g.JID), g.Name, len(g.Participants))
			}
		}
	}
}

func (w *waClient) processMessageEvent(cli *whatsmeow.Client, evt *events.Message, isLive bool, cb BackendCallbacks) {
	selfPN := CanonicalJIDFromTypes(cli.Store.GetJID())
	selfLID := CanonicalJIDFromTypes(cli.Store.GetLID())
	ext := ExtractMessageEvent(evt, selfPN, selfLID, w.store)
	for _, pair := range ext.LIDPairs {
		if cb.OnLIDMapping != nil {
			cb.OnLIDMapping(pair)
		}
	}
	switch ext.Kind {
	case "message":
		if cb.OnMessage != nil {
			cb.OnMessage(ext.Message, Chat{JID: ext.ChatJID, Kind: ChatKind(ext.ChatJID)}, isLive)
		}
	case "reaction":
		if cb.OnReaction != nil {
			cb.OnReaction(ext.ChatJID, ext.TargetID, ext.SenderJID, ext.Emoji)
		}
	case "edit":
		if cb.OnEdit != nil {
			cb.OnEdit(ext.ChatJID, ext.TargetID, ext.NewText)
		}
	case "revoke":
		if cb.OnRevoke != nil {
			cb.OnRevoke(ext.ChatJID, ext.TargetID)
		}
	}
}

func (w *waClient) processHistorySync(_ context.Context, cli *whatsmeow.Client, evt *events.HistorySync, cb BackendCallbacks) {
	if evt == nil || evt.Data == nil {
		return
	}
	data := evt.Data
	// 1. Ingest PhoneNumber <-> LID mappings first so all subsequent conversations normalize cleanly.
	if cb.OnLIDMapping != nil {
		for _, m := range data.GetPhoneNumberToLidMappings() {
			if m == nil {
				continue
			}
			lid := NormalizeJID(m.GetLidJID())
			pn := NormalizeJID(m.GetPnJID())
			if IsLID(lid) && IsPhoneJID(pn) {
				cb.OnLIDMapping(LIDPair{LID: lid, PN: pn})
			}
		}
	}
	// 2. Ingest push names.
	if cb.OnContact != nil {
		for _, pn := range data.GetPushnames() {
			if pn == nil || pn.GetID() == "" || pn.GetPushname() == "" {
				continue
			}
			jid := CanonicalizeChatJID(pn.GetID(), w.store)
			if IsPhoneJID(jid) || IsLID(jid) {
				cb.OnContact(Contact{JID: jid, Phone: PhoneFromJID(jid), PushName: pn.GetPushname()})
			}
		}
	}

	onDemand := data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND
	selfPN := CanonicalJIDFromTypes(cli.Store.GetJID())
	selfLID := CanonicalJIDFromTypes(cli.Store.GetLID())

	for _, conv := range data.GetConversations() {
		if conv == nil {
			continue
		}
		// Learn LID <-> PN from conversation fields if present.
		if cb.OnLIDMapping != nil {
			lid := NormalizeJID(conv.GetLidJID())
			pn := NormalizeJID(firstNonEmpty(conv.GetPnJID(), conv.GetOldJID(), conv.GetNewJID()))
			if IsLID(lid) && IsPhoneJID(pn) {
				cb.OnLIDMapping(LIDPair{LID: lid, PN: pn})
			}
		}
		rawChat := firstNonEmpty(conv.GetPnJID(), conv.GetID())
		chatJID := CanonicalizeChatJID(rawChat, w.store)
		if !IsSupportedChatJID(chatJID) {
			continue
		}
		parsedChatJID, err := ParseCanonicalJID(chatJID)
		if err != nil {
			continue
		}

		chatHint := Chat{
			JID:         chatJID,
			Kind:        ChatKind(chatJID),
			Name:        firstNonEmpty(conv.GetDisplayName(), conv.GetName()),
			Phone:       PhoneFromJID(chatJID),
			UnreadCount: int(conv.GetUnreadCount()),
			Archived:    conv.GetArchived(),
			MutedUntil:  int64(conv.GetMuteEndTime()),
		}
		if conv.GetConversationTimestamp() > 0 {
			chatHint.LastMsgTS = time.Unix(int64(conv.GetConversationTimestamp()), 0).UTC()
		}
		if w.store != nil {
			_ = w.store.UpsertChat(chatHint)
		}

		ingested := 0
		for _, hm := range conv.GetMessages() {
			if hm == nil || hm.GetMessage() == nil {
				continue
			}
			parsed, err := cli.ParseWebMessage(parsedChatJID, hm.GetMessage())
			if err != nil || parsed == nil {
				continue
			}
			ext := ExtractMessageEvent(parsed, selfPN, selfLID, w.store)
			for _, pair := range ext.LIDPairs {
				if cb.OnLIDMapping != nil {
					cb.OnLIDMapping(pair)
				}
			}
			switch ext.Kind {
			case "message":
				if cb.OnMessage != nil {
					cb.OnMessage(ext.Message, chatHint, false)
					ingested++
				}
			case "reaction":
				if cb.OnReaction != nil {
					cb.OnReaction(ext.ChatJID, ext.TargetID, ext.SenderJID, ext.Emoji)
				}
			case "edit":
				if cb.OnEdit != nil {
					cb.OnEdit(ext.ChatJID, ext.TargetID, ext.NewText)
				}
			case "revoke":
				if cb.OnRevoke != nil {
					cb.OnRevoke(ext.ChatJID, ext.TargetID)
				}
			}
		}
		if cb.OnHistoryBatch != nil {
			cb.OnHistoryBatch(chatJID, onDemand, ingested)
		}
	}
}

func (w *waClient) canonicalize(cli *whatsmeow.Client, j types.JID) string {
	s := CanonicalJIDFromTypes(j)
	if IsLID(s) && cli != nil && cli.Store != nil && cli.Store.LIDs != nil {
		if pn, err := cli.Store.LIDs.GetPNForLID(context.Background(), j.ToNonAD()); err == nil && !pn.IsEmpty() {
			pnStr := CanonicalJIDFromTypes(pn)
			if IsPhoneJID(pnStr) {
				if w.store != nil {
					_ = w.store.PutLIDMapping(s, pnStr)
				}
				return pnStr
			}
		}
	}
	return CanonicalizeChatJID(s, w.store)
}

func (w *waClient) activeClient() (*whatsmeow.Client, error) {
	w.mu.Lock()
	cli := w.cli
	ok := cli != nil && w.connected && cli.IsConnected() && cli.IsLoggedIn()
	w.mu.Unlock()
	if !ok {
		return nil, errNotConnected
	}
	return cli, nil
}

func (w *waClient) SendText(ctx context.Context, chatJID, text string, replyTo *Message) (SendReceipt, error) {
	cli, err := w.activeClient()
	if err != nil {
		return SendReceipt{}, err
	}
	to, err := ParseCanonicalJID(chatJID)
	if err != nil {
		return SendReceipt{}, err
	}
	var msg *waE2E.Message
	if replyTo != nil && replyTo.ID != "" {
		participant := replyTo.SenderJID
		if replyTo.Mine {
			participant = CanonicalJIDFromTypes(cli.Store.GetJID())
		}
		if participant == "" && !IsGroupJID(chatJID) {
			participant = chatJID
		}
		quotedText := replyTo.Text
		if quotedText == "" {
			quotedText = "[消息]"
		}
		msg = &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String(text),
				ContextInfo: &waE2E.ContextInfo{
					StanzaID:      proto.String(replyTo.ID),
					Participant:   proto.String(participant),
					QuotedMessage: &waE2E.Message{Conversation: proto.String(quotedText)},
				},
			},
		}
	} else {
		msg = &waE2E.Message{
			Conversation: proto.String(text),
		}
	}
	resp, err := cli.SendMessage(ctx, to, msg)
	if err != nil {
		return SendReceipt{}, err
	}
	ts := resp.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return SendReceipt{ID: resp.ID, At: ts.UTC()}, nil
}

func (w *waClient) SendReaction(ctx context.Context, chatJID, messageID, senderJID string, fromMe bool, emoji string) error {
	cli, err := w.activeClient()
	if err != nil {
		return err
	}
	chat, err := ParseCanonicalJID(chatJID)
	if err != nil {
		return err
	}
	var sender types.JID
	if fromMe {
		sender = cli.Store.GetJID().ToNonAD()
	} else if senderJID != "" {
		if parsed, perr := ParseCanonicalJID(senderJID); perr == nil {
			sender = parsed
		}
	}
	if sender.IsEmpty() && !IsGroupJID(chatJID) {
		sender = chat
	}
	msg := cli.BuildReaction(chat, sender, types.MessageID(messageID), emoji)
	_, err = cli.SendMessage(ctx, chat, msg)
	return err
}

func (w *waClient) MarkRead(ctx context.Context, chatJID string, bySender map[string][]string, readAt time.Time) error {
	cli, err := w.activeClient()
	if err != nil {
		return err
	}
	chat, err := ParseCanonicalJID(chatJID)
	if err != nil {
		return err
	}
	if readAt.IsZero() {
		readAt = time.Now()
	}
	for senderRaw, ids := range bySender {
		if len(ids) == 0 {
			continue
		}
		mids := make([]types.MessageID, 0, len(ids))
		for _, id := range ids {
			mids = append(mids, types.MessageID(id))
		}
		var sender types.JID
		if IsGroupJID(chatJID) && senderRaw != "" {
			if parsed, perr := ParseCanonicalJID(senderRaw); perr == nil {
				sender = parsed
			}
		}
		if err := cli.MarkRead(ctx, mids, readAt, chat, sender); err != nil {
			return err
		}
	}
	return nil
}

func (w *waClient) RequestHistorySync(ctx context.Context, anchor BackfillAnchor, count int) error {
	cli, err := w.activeClient()
	if err != nil {
		return err
	}
	chat, err := ParseCanonicalJID(anchor.ChatJID)
	if err != nil {
		return err
	}
	var sender types.JID
	if anchor.Mine {
		sender = cli.Store.GetJID().ToNonAD()
	} else if anchor.SenderJID != "" {
		if parsed, perr := ParseCanonicalJID(anchor.SenderJID); perr == nil {
			sender = parsed
		}
	}
	if sender.IsEmpty() {
		sender = chat
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			Sender:   sender,
			IsFromMe: anchor.Mine,
			IsGroup:  IsGroupJID(anchor.ChatJID),
		},
		ID:        types.MessageID(anchor.MessageID),
		Timestamp: anchor.At,
	}
	req := cli.BuildHistorySyncRequest(info, count)
	_, err = cli.SendPeerMessage(ctx, req)
	return err
}

func (w *waClient) FetchAvatar(ctx context.Context, jidStr string) (*AvatarData, error) {
	cli, err := w.activeClient()
	if err != nil {
		return nil, err
	}
	jid, err := ParseCanonicalJID(jidStr)
	if err != nil {
		return nil, err
	}
	pic, err := cli.GetProfilePictureInfo(ctx, jid, &whatsmeow.GetProfilePictureParams{Preview: true})
	if err != nil {
		if errors.Is(err, whatsmeow.ErrProfilePictureNotSet) || errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) {
			return nil, nil
		}
		return nil, err
	}
	if pic == nil || pic.URL == "" {
		return nil, nil
	}
	w.mu.Lock()
	px := w.proxy
	w.mu.Unlock()
	hc := px.HTTPClient(20 * time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pic.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("avatar fetch HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, connector.MediaMost+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > connector.MediaMost {
		return nil, fmt.Errorf("avatar size %d out of bounds", len(data))
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return &AvatarData{
		ID:   pic.ID,
		URL:  pic.URL,
		Mime: mime,
		Data: data,
	}, nil
}

func (w *waClient) Logout(ctx context.Context) error {
	w.mu.Lock()
	cli := w.cli
	w.mu.Unlock()
	if cli != nil && cli.IsConnected() && cli.IsLoggedIn() {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := cli.Logout(lctx)
		cancel()
		if err != nil && !errors.Is(err, whatsmeow.ErrNotConnected) && !errors.Is(err, whatsmeow.ErrNotLoggedIn) {
			// Best-effort disconnect even if unlink timed out, then clear encrypted Hub secret.
			cli.Disconnect()
		}
	} else if cli != nil {
		cli.Disconnect()
	}
	w.Disconnect()
	return w.vault.Clear(ctx)
}

func (w *waClient) Flush(ctx context.Context, force bool) error {
	return w.vault.Flush(ctx, force)
}

func (w *waClient) Close(ctx context.Context) error {
	w.Disconnect()
	if w.flushCancel != nil {
		w.flushCancel()
	}
	return w.vault.Close(ctx)
}

// StartLogin initiates a fresh unpaired whatsmeow connection for QR scan or 8-digit phone pairing.
func (w *waClient) StartLogin(ctx context.Context) (LoginSession, error) {
	px, err := ResolveProxyFromEnv()
	if err != nil {
		return nil, err
	}
	// Disconnect any live connection and ensure we have a clean unpaired device.
	w.Disconnect()
	if w.vault.HasPairedDevice(ctx) {
		if err := w.vault.Clear(ctx); err != nil {
			return nil, err
		}
	}
	dev, err := w.vault.Device(ctx)
	if err != nil {
		return nil, err
	}
	if dev.ID != nil {
		if err := w.vault.Clear(ctx); err != nil {
			return nil, err
		}
		dev, err = w.vault.Device(ctx)
		if err != nil {
			return nil, err
		}
	}

	cli := whatsmeow.NewClient(dev, waLog.Noop)
	if err := px.ApplyToClient(cli); err != nil {
		return nil, err
	}
	loginCtx, cancel := context.WithCancel(context.Background())
	qrCh, err := cli.GetQRChannel(loginCtx)
	if err != nil {
		cancel()
		return nil, err
	}

	sess := &waLoginSession{
		vault:   w.vault,
		cli:     cli,
		cancel:  cancel,
		events:  make(chan LoginEvent, 16),
		readyCh: make(chan struct{}),
		connCh:  make(chan struct{}, 1),
	}

	sess.hid = cli.AddEventHandler(func(rawEvt any) {
		switch rawEvt.(type) {
		case *events.Connected:
			select {
			case sess.connCh <- struct{}{}:
			default:
			}
		}
	})

	if err := cli.Connect(); err != nil {
		cli.RemoveEventHandler(sess.hid)
		cancel()
		return nil, err
	}

	go sess.pump(loginCtx, qrCh)
	return sess, nil
}

type waLoginSession struct {
	vault   *DeviceVault
	cli     *whatsmeow.Client
	hid     uint32
	cancel  context.CancelFunc
	events  chan LoginEvent
	readyCh chan struct{}
	connCh  chan struct{}

	readyOnce sync.Once
	closeOnce sync.Once
}

func (s *waLoginSession) markReady() {
	s.readyOnce.Do(func() { close(s.readyCh) })
}

func (s *waLoginSession) pump(ctx context.Context, qrCh <-chan whatsmeow.QRChannelItem) {
	defer close(s.events)
	for item := range qrCh {
		switch item.Event {
		case whatsmeow.QRChannelEventCode:
			s.markReady()
			select {
			case s.events <- LoginEvent{QRCode: item.Code}:
			default:
			}
		case "success":
			s.markReady()
			// Wait briefly for post-pair reconnect so whatsmeow finishes saving device state into sqlstore.
			select {
			case <-s.connCh:
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
			}
			jid := CanonicalJIDFromTypes(s.cli.Store.GetJID())
			lid := CanonicalJIDFromTypes(s.cli.Store.GetLID())
			push := strings.TrimSpace(s.cli.Store.PushName)
			account := formatAccountLabel(push, jid)
			s.vault.SetMeta(account, jid, lid, push)
			fctx, fcancel := context.WithTimeout(context.Background(), 10*time.Second)
			flushErr := s.vault.Flush(fctx, true)
			fcancel()
			s.stopClient()
			if flushErr != nil {
				s.events <- LoginEvent{Err: flushErr}
				return
			}
			s.events <- LoginEvent{Paired: true, Account: account, JID: jid, LID: lid}
			return
		case "timeout":
			s.markReady()
			s.stopClient()
			s.events <- LoginEvent{Err: errors.New("配对已超时，请重新获取二维码或配对码")}
			return
		case whatsmeow.QRChannelEventError:
			s.markReady()
			s.stopClient()
			err := item.Error
			if err == nil {
				err = errors.New("WhatsApp 配对失败")
			}
			s.events <- LoginEvent{Err: err}
			return
		default:
			s.markReady()
			s.stopClient()
			s.events <- LoginEvent{Err: fmt.Errorf("WhatsApp 登录中断（%s）", item.Event)}
			return
		}
	}
	s.markReady()
}

func (s *waLoginSession) Events() <-chan LoginEvent {
	return s.events
}

func (s *waLoginSession) RequestPairCode(ctx context.Context, phoneDigits string) (string, error) {
	select {
	case <-s.readyCh:
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(15 * time.Second):
		return "", errors.New("连接 WhatsApp 超时，无法申请配对码")
	}
	code, err := s.cli.PairPhone(ctx, phoneDigits, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return "", err
	}
	return FormatPairingCode(code), nil
}

func (s *waLoginSession) stopClient() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.cli.RemoveEventHandler(s.hid)
		s.cli.Disconnect()
	})
}

func (s *waLoginSession) Cancel() {
	s.stopClient()
}

func formatAccountLabel(pushName, jid string) string {
	phone := FormatPhoneDisplay(PhoneFromJID(jid))
	pushName = strings.TrimSpace(pushName)
	switch {
	case pushName != "" && phone != "":
		return fmt.Sprintf("%s (%s)", pushName, phone)
	case phone != "":
		return phone
	case pushName != "":
		return pushName
	default:
		return jid
	}
}
