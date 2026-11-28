package wa

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
)

type fakeLoginSession struct {
	events    chan LoginEvent
	pairCode  string
	pairErr   error
	gotPhone  string
	cancelled bool
	mu        sync.Mutex
}

func newFakeLoginSession() *fakeLoginSession {
	return &fakeLoginSession{events: make(chan LoginEvent, 8), pairCode: "ABCD-EF12"}
}

func (f *fakeLoginSession) Events() <-chan LoginEvent { return f.events }

func (f *fakeLoginSession) RequestPairCode(_ context.Context, digits string) (string, error) {
	f.mu.Lock()
	f.gotPhone = digits
	code, err := f.pairCode, f.pairErr
	f.mu.Unlock()
	return code, err
}

func (f *fakeLoginSession) Cancel() {
	f.mu.Lock()
	f.cancelled = true
	f.mu.Unlock()
}

type fakeBackend struct {
	mu            sync.Mutex
	hasSession    bool
	connected     bool
	account       string
	jid           string
	lid           string
	lastQuiet     bool
	connectCalls  int
	connectErrs   []error
	logoutCalls   int
	nextLogin     *fakeLoginSession
	startLoginErr error
	cb            BackendCallbacks

	sentTexts   []fakeSent
	sentReacts  []fakeReact
	sentReads   []fakeRead
	backfillReq []BackfillAnchor
	onBackfill  func(anchor BackfillAnchor, count int)
	avatars     map[string]*AvatarData
}

type fakeSent struct {
	chat    string
	text    string
	replyTo string
}

type fakeReact struct {
	chat   string
	id     string
	sender string
	fromMe bool
	emoji  string
}

type fakeRead struct {
	chat     string
	bySender map[string][]string
}

func (f *fakeBackend) HasSession(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hasSession
}

func (f *fakeBackend) AccountInfo() (string, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.account, f.jid, f.lid
}

func (f *fakeBackend) Connect(ctx context.Context, quietPresence bool, cb BackendCallbacks) error {
	f.mu.Lock()
	f.connectCalls++
	f.lastQuiet = quietPresence
	f.cb = cb
	if len(f.connectErrs) > 0 {
		err := f.connectErrs[0]
		f.connectErrs = f.connectErrs[1:]
		f.mu.Unlock()
		return err
	}
	f.connected = true
	acc, jid, lid := f.account, f.jid, f.lid
	f.mu.Unlock()
	if cb.OnConnected != nil {
		cb.OnConnected(acc, jid, lid)
	}
	<-ctx.Done()
	f.mu.Lock()
	f.connected = false
	f.mu.Unlock()
	return ctx.Err()
}

func (f *fakeBackend) Disconnect() {
	f.mu.Lock()
	f.connected = false
	f.mu.Unlock()
}

func (f *fakeBackend) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeBackend) StartLogin(context.Context) (LoginSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startLoginErr != nil {
		return nil, f.startLoginErr
	}
	if f.nextLogin == nil {
		f.nextLogin = newFakeLoginSession()
	}
	return f.nextLogin, nil
}

func (f *fakeBackend) SendText(_ context.Context, chatJID, text string, replyTo *Message) (SendReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rid := ""
	if replyTo != nil {
		rid = replyTo.ID
	}
	f.sentTexts = append(f.sentTexts, fakeSent{chat: chatJID, text: text, replyTo: rid})
	return SendReceipt{ID: "SENT_" + time.Now().Format("150405.000"), At: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeBackend) SendReaction(_ context.Context, chatJID, messageID, senderJID string, fromMe bool, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentReacts = append(f.sentReacts, fakeReact{chat: chatJID, id: messageID, sender: senderJID, fromMe: fromMe, emoji: emoji})
	return nil
}

func (f *fakeBackend) MarkRead(_ context.Context, chatJID string, bySender map[string][]string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := map[string][]string{}
	for k, v := range bySender {
		cp[k] = append([]string(nil), v...)
	}
	f.sentReads = append(f.sentReads, fakeRead{chat: chatJID, bySender: cp})
	return nil
}

func (f *fakeBackend) RequestHistorySync(_ context.Context, anchor BackfillAnchor, count int) error {
	f.mu.Lock()
	f.backfillReq = append(f.backfillReq, anchor)
	fn := f.onBackfill
	f.mu.Unlock()
	if fn != nil {
		fn(anchor, count)
	}
	return nil
}

func (f *fakeBackend) FetchAvatar(_ context.Context, jid string) (*AvatarData, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.avatars[jid], nil
}

func (f *fakeBackend) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logoutCalls++
	f.hasSession = false
	f.connected = false
	f.account, f.jid, f.lid = "", "", ""
	return nil
}

func (f *fakeBackend) Flush(context.Context, bool) error { return nil }
func (f *fakeBackend) Close(context.Context) error       { return nil }

func TestLoginFlowQRAndRotation(t *testing.T) {
	ctx := context.Background()
	st, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ls := newFakeLoginSession()
	ls.events <- LoginEvent{QRCode: "2@qr-initial-token"}
	be := &fakeBackend{nextLogin: ls}
	conn := NewWithDeps(be, st, time.UTC, DefaultConfig())

	// Step "" -> method selector.
	p, err := conn.LoginStep(ctx, "", nil)
	if err != nil || p.Step != stepMethod || len(p.Fields) != 1 {
		t.Fatalf("step '': got %+v err=%v", p, err)
	}

	// Step "method" with "qr" -> QR prompt.
	p, err = conn.LoginStep(ctx, stepMethod, map[string]string{"method": "qr"})
	if err != nil || p.Step != stepQR || p.Kind != connector.PromptQRCode || p.URL != "2@qr-initial-token" || p.Interval != loginPollInterval {
		t.Fatalf("step qr: got %+v err=%v", p, err)
	}

	// Simulate QR rotation and poll again.
	ls.events <- LoginEvent{QRCode: "2@qr-rotated-token"}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p, err = conn.LoginStep(ctx, stepQR, nil)
		if err != nil {
			t.Fatal(err)
		}
		if p.URL == "2@qr-rotated-token" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.URL != "2@qr-rotated-token" {
		t.Fatalf("expected rotated QR, got %+v", p)
	}

	// Simulate pairing completion.
	be.mu.Lock()
	be.hasSession = true
	be.account = "Alice (+12025550199)"
	be.jid = "12025550199@s.whatsapp.net"
	be.mu.Unlock()
	ls.events <- LoginEvent{Paired: true, Account: "Alice (+12025550199)", JID: "12025550199@s.whatsapp.net", LID: "77776666@lid"}

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p, err = conn.LoginStep(ctx, stepQR, nil)
		if err != nil {
			t.Fatal(err)
		}
		if p.Done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !p.Done || p.Account != "Alice (+12025550199)" {
		t.Fatalf("expected Done after QR pairing, got %+v", p)
	}
	if st := conn.Status(ctx); st.State != connector.StateOK || st.Account != "Alice (+12025550199)" {
		t.Fatalf("status after pairing: %+v", st)
	}
}

func TestLoginFlowPhonePairingCodeAndLogout(t *testing.T) {
	ctx := context.Background()
	st, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ls := newFakeLoginSession()
	ls.pairCode = "wxyz5678"
	be := &fakeBackend{nextLogin: ls}
	conn := NewWithDeps(be, st, time.UTC, DefaultConfig())

	// Invalid input re-asks method step with helpful error.
	p, err := conn.LoginStep(ctx, stepMethod, map[string]string{"method": "0123"})
	if err != nil || p.Step != stepMethod || p.Error == "" {
		t.Fatalf("invalid input should re-ask method with error: %+v err=%v", p, err)
	}

	// Valid E.164 phone requests 8-digit pairing code rendered as device_code.
	p, err = conn.LoginStep(ctx, stepMethod, map[string]string{"method": "+1 (202) 555-0199"})
	if err != nil || p.Step != stepCode || p.Kind != connector.PromptDeviceCode || p.UserCode != "WXYZ-5678" || p.URL == "" || p.Interval <= 0 {
		t.Fatalf("pair_code prompt: %+v err=%v", p, err)
	}
	if ls.gotPhone != "12025550199" {
		t.Fatalf("RequestPairCode got %q, want 12025550199", ls.gotPhone)
	}

	// Polling while waiting keeps returning the same pairing code.
	p, err = conn.LoginStep(ctx, stepCode, nil)
	if err != nil || p.Done || p.UserCode != "WXYZ-5678" {
		t.Fatalf("poll waiting: %+v err=%v", p, err)
	}

	// Simulate pair success.
	ls.events <- LoginEvent{Paired: true, Account: "+12025550199", JID: "12025550199@s.whatsapp.net"}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p, err = conn.LoginStep(ctx, stepCode, nil)
		if err != nil {
			t.Fatal(err)
		}
		if p.Done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !p.Done {
		t.Fatalf("expected Done after phone pairing, got %+v", p)
	}

	// Logout clears session, wipes store, and transitions to StateNotSignedIn.
	if err := conn.Logout(ctx); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if be.logoutCalls != 1 {
		t.Fatalf("expected backend Logout called once, got %d", be.logoutCalls)
	}
	if got := conn.Status(ctx); got.State != connector.StateNotSignedIn {
		t.Fatalf("status after logout = %q, want not_signed_in", got.State)
	}
}

func TestRunQuietPresenceAndReconnectBackoff(t *testing.T) {
	origRetryFirst := retryFirst
	retryFirst = 20 * time.Millisecond
	t.Cleanup(func() { retryFirst = origRetryFirst })

	st, err := OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	be := &fakeBackend{
		hasSession:  true,
		account:     "Demo (+12025550199)",
		jid:         "12025550199@s.whatsapp.net",
		connectErrs: []error{errors.New("temporary websocket reset")},
	}
	conn := NewWithDeps(be, st, time.UTC, DefaultConfig())

	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		conn.Run(runCtx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if conn.Status(context.Background()).State == connector.StateOK {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got := conn.Status(context.Background()); got.State != connector.StateOK {
		cancel()
		<-done
		t.Fatalf("expected StateOK after reconnect retry, got %+v", got)
	}
	be.mu.Lock()
	calls := be.connectCalls
	quiet := be.lastQuiet
	be.mu.Unlock()
	if calls < 2 {
		t.Fatalf("expected at least 2 connect attempts (1 fail + 1 success), got %d", calls)
	}
	if !quiet {
		t.Fatal("expected quietPresence=true by default")
	}

	cancel()
	<-done
}
