package wechat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/wechat/internal/wechat/ilink"
	"github.com/nwverse/persona/sdk/go/wire"
)

// memSecrets keeps the sealed blob in memory.
type memSecrets struct {
	mu   sync.Mutex
	data []byte
}

func (s *memSecrets) Load(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data, nil
}
func (s *memSecrets) Save(_ context.Context, d []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append([]byte(nil), d...)
	return nil
}
func (s *memSecrets) Delete(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = nil
	return nil
}

func (s *memSecrets) state(t *testing.T) state {
	s.mu.Lock()
	defer s.mu.Unlock()
	var st state
	if len(s.data) > 0 {
		if err := json.Unmarshal(s.data, &st); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

// fakeILink is iLink: QR statuses as scripted, messages as queued, what the bot sends recorded.
type fakeILink struct {
	t         *testing.T
	srv       *httptest.Server
	mu        sync.Mutex
	statuses  []map[string]any
	queue     []map[string]any
	seq       int
	replaced  bool
	sent      []sentText
	typing    []bool
	downloads map[string][]byte
	// failSends: how many sendmessage calls to keep and then answer HTTP 500 (the answer lost on the way back).
	failSends int
	// sendID is the server message_id answered for a successful send.
	sendID string
	// failedSend signals that a scripted HTTP 500 response has been written, before the retry wait.
	failedSend chan struct{}
	clientIDs  []string        // every sendmessage's client_id, repeated ones too
	kept       map[string]bool // client_ids iLink has: a repeat is not posted again
}

type sentText struct{ To, Context, Text string }

func newFakeILink(t *testing.T) *fakeILink {
	f := &fakeILink{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeILink) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(f.srv.URL)
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host, r2.Host = u.Scheme, u.Host, u.Host
	return http.DefaultTransport.RoundTrip(r2)
}

func (f *fakeILink) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	out := map[string]any{"ret": 0}
	f.mu.Lock()
	switch r.URL.Path {
	case "/ilink/bot/get_bot_qrcode":
		out = map[string]any{"qrcode": "QC", "qrcode_img_content": "https://liteapp.weixin.qq.com/q/example"}
	case "/ilink/bot/get_qrcode_status":
		out = map[string]any{"status": "wait"}
		if len(f.statuses) > 0 {
			out, f.statuses = f.statuses[0], f.statuses[1:]
		}
	case "/ilink/bot/getupdates":
		if f.replaced {
			out = map[string]any{"errcode": -14}
			break
		}
		msgs := f.queue
		f.queue = nil
		if len(msgs) == 0 {
			f.mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			f.mu.Lock()
		}
		f.seq++
		out = map[string]any{"ret": 0, "msgs": msgs, "get_updates_buf": "cursor-" + strconv.Itoa(f.seq)}
	case "/ilink/bot/sendmessage":
		msg := body["msg"].(map[string]any)
		id := msg["client_id"].(string)
		f.clientIDs = append(f.clientIDs, id)
		if !f.kept[id] {
			if f.kept == nil {
				f.kept = map[string]bool{}
			}
			f.kept[id] = true
			item := msg["item_list"].([]any)[0].(map[string]any)
			f.sent = append(f.sent, sentText{To: msg["to_user_id"].(string), Context: msg["context_token"].(string),
				Text: item["text_item"].(map[string]any)["text"].(string)})
		}
		if f.failSends > 0 {
			f.failSends--
			failed := f.failedSend
			f.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			if failed != nil {
				failed <- struct{}{}
			}
			return
		}
		if f.sendID != "" {
			out["message_id"] = f.sendID
		}
	case "/ilink/bot/getconfig":
		out["typing_ticket"] = "TT"
	case "/ilink/bot/sendtyping":
		f.typing = append(f.typing, body["status"] == float64(1))
	case "/c2c/download":
		data := f.downloads[r.URL.Query().Get("encrypted_query_param")]
		f.mu.Unlock()
		if data == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
		return
	}
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(out)
}

var msgSeq struct {
	sync.Mutex
	n int
}

// say queues a message from `from` (with items, text when none are given).
func (f *fakeILink) say(from, text string, items ...map[string]any) {
	msgSeq.Lock()
	msgSeq.n++
	id := msgSeq.n
	msgSeq.Unlock()
	if len(items) == 0 {
		items = []map[string]any{{"type": 1, "text_item": map[string]any{"text": text}}}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, map[string]any{"message_id": id, "from_user_id": from, "context_token": "ctx-" + strconv.Itoa(id),
		"message_type": 1, "item_list": items})
}

func (f *fakeILink) texts() []sentText {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentText(nil), f.sent...)
}

// fakeHub is the hub as the program reaches it (channel.receive): what it was handed.
type fakeHub struct {
	mu   sync.Mutex
	msgs []wire.ChannelMessage
}

func (h *fakeHub) Receive(_ context.Context, m wire.ChannelMessage) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, m)
	return nil
}

func (h *fakeHub) got() []wire.ChannelMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]wire.ChannelMessage(nil), h.msgs...)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

type rig struct {
	t       *testing.T
	ilink   *fakeILink
	hub     *fakeHub
	secrets *memSecrets
	clock   *clock
	bot     *Bot
	cancel  context.CancelFunc
}

const owner = "owner@im.wechat"

// newRig is a bot bound to owner's WeChat, running against the fakes.
func newRig(t *testing.T, bound bool) *rig {
	r := &rig{t: t, ilink: newFakeILink(t), hub: &fakeHub{}, secrets: &memSecrets{}, clock: &clock{t: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)}}
	if bound {
		st := state{Creds: &ilink.Credentials{Token: "BT", BotID: "bot@im.bot", UserID: owner, BaseURL: "https://ilinkai.weixin.qq.com/"}}
		data, _ := json.Marshal(st)
		r.secrets.data = data
	}
	r.bot = New(Options{Hub: r.hub, Agent: "Persona/test", Now: r.clock.now, DataDir: t.TempDir(),
		NewClient: func(base, token string) (*ilink.Client, error) {
			return ilink.New(ilink.Options{BaseURL: base, Token: token, Agent: "Persona/test", HTTP: &http.Client{Transport: r.ilink}})
		}}, r.secrets)
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	t.Cleanup(cancel)
	go r.bot.Run(ctx)
	return r
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	writer, flags := log.Writer(), log.Flags()
	log.SetOutput(&out)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
	})
	return &out
}

func sendLogLines(s string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.HasPrefix(line, "wechat: send ") {
			out = append(out, line)
		}
	}
	return out
}

// sentTo waits for the bot to have sent a text to peer containing want; it returns that text.
func (r *rig) sentTo(peer, want string) sentText {
	r.t.Helper()
	var got sentText
	eventually(r.t, fmt.Sprintf("a message to %s with %q (sent: %+v)", peer, want, r.ilink.texts()), func() bool {
		for _, s := range r.ilink.texts() {
			if s.To == peer && strings.Contains(s.Text, want) {
				got = s
				return true
			}
		}
		return false
	})
	return got
}

func TestBindByQRCode(t *testing.T) {
	r := newRig(t, false)
	ctx := context.Background()
	eventually(t, "not signed in", func() bool { return r.bot.Status(ctx).State == wire.ConnectorStateNotSignedIn })
	r.ilink.statuses = []map[string]any{{"status": "scaned"}, {"status": "need_verifycode"},
		{"status": "confirmed", "bot_token": "BT", "ilink_bot_id": "bot@im.bot", "ilink_user_id": owner, "baseurl": "https://ilinkai.weixin.qq.com"}}
	p, err := r.bot.LoginStep(ctx, "", nil)
	if err != nil || p.Kind != wire.ConnectorPromptKindQr || p.URL != "https://liteapp.weixin.qq.com/q/example" || p.Interval == 0 {
		t.Fatalf("%+v %v", p, err)
	}
	if p, _ = r.bot.LoginStep(ctx, "qr", nil); !strings.Contains(p.Hint, "已经扫码") {
		t.Fatalf("scanned %+v", p)
	}
	if p, _ = r.bot.LoginStep(ctx, "qr", nil); p.Step != "code" || len(p.Fields) != 1 {
		t.Fatalf("code %+v", p)
	}
	if p, _ = r.bot.LoginStep(ctx, "code", map[string]string{"code": "x1"}); p.Error == "" {
		t.Fatalf("a bad code %+v", p)
	}
	if p, _ = r.bot.LoginStep(ctx, "code", map[string]string{"code": "123456"}); !p.Done || !strings.Contains(p.Account, "微信用户") {
		t.Fatalf("done %+v", p)
	}
	if st := r.secrets.state(t); st.Creds == nil || st.Creds.Token != "BT" || st.Creds.UserID != owner {
		t.Fatalf("stored %+v", st)
	}
	eventually(t, "serving", func() bool { return r.bot.Status(ctx).State == wire.ConnectorStateOk })
	if err := r.bot.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	st := r.secrets.state(t)
	if st.Creds != nil || len(st.Old) != 1 || st.Old[0] != "BT" {
		t.Fatalf("after logout %+v", st)
	}
	if s := r.bot.Status(ctx); s.State != wire.ConnectorStateNotSignedIn {
		t.Fatalf("status %+v", s)
	}
}

// A poll that fails for good (here iLink confirms with a base URL outside weixin.qq.com) must not leave the dead QR
// in place: asking again with the same code would fail the same way forever.
func TestFailedPollForgetsThePendingQR(t *testing.T) {
	r := newRig(t, false)
	ctx := context.Background()
	r.ilink.statuses = []map[string]any{{"status": "confirmed", "bot_token": "BT", "ilink_bot_id": "bot@im.bot", "ilink_user_id": owner, "baseurl": "https://ilink.example.com"}}
	if _, err := r.bot.LoginStep(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	_, err := r.bot.LoginStep(ctx, "qr", nil)
	if err == nil || ilink.Retryable(err) {
		t.Fatalf("want a final error, got %v", err)
	}
	r.bot.mu.Lock()
	pending := r.bot.qr
	r.bot.mu.Unlock()
	if pending != nil {
		t.Fatal("the QR that can no longer succeed is still pending")
	}
	if p, err := r.bot.LoginStep(ctx, "qr", nil); err != nil || p.Kind != wire.ConnectorPromptKindQr {
		t.Fatalf("a new QR should start, got %+v %v", p, err)
	}
	if st := r.secrets.state(t); st.Creds != nil {
		t.Fatalf("credentials from the rejected answer were stored: %+v", st)
	}
}

// The owner's message goes to the hub as it is; someone else hears once whose bot it is, and nothing reaches the hub.
func TestOwnerMessageGoesToTheHubStrangerHearsOnce(t *testing.T) {
	r := newRig(t, true)
	r.ilink.say("someone@im.wechat", "你好")
	r.sentTo("someone@im.wechat", "只为绑定它的人服务")
	r.ilink.say("someone@im.wechat", "在吗")
	r.ilink.say(owner, "明天有什么会？")
	eventually(t, "the message at the hub", func() bool { return len(r.hub.got()) == 1 })
	m := r.hub.got()[0]
	if m.Text != "明天有什么会？" || !strings.HasPrefix(m.ID, "message:") {
		t.Fatalf("handed %+v", m)
	}
	n := 0
	for _, s := range r.ilink.texts() {
		if s.To == "someone@im.wechat" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("stranger heard %d times", n)
	}
	if st := r.secrets.state(t); st.Cursor == "" || st.Context == "" {
		t.Fatalf("cursor and context not kept: %+v", st)
	}
}

func TestTokenReplacedUnbinds(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	eventually(t, "serving", func() bool { return r.bot.Status(ctx).State == wire.ConnectorStateOk })
	r.ilink.mu.Lock()
	r.ilink.replaced = true
	r.ilink.mu.Unlock()
	eventually(t, "unbound", func() bool {
		s := r.bot.Status(ctx)
		return s.State == wire.ConnectorStateNotSignedIn && strings.Contains(s.Error, "失效")
	})
	if st := r.secrets.state(t); st.Creds != nil || len(st.Old) != 1 {
		t.Fatalf("%+v", st)
	}
}

// A picture, documents and a video go with the message, all downloaded and decrypted. Videos are files to the hub.
func TestMediaGoesWithTheMessage(t *testing.T) {
	r := newRig(t, true)
	key, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	pdf := append([]byte("%PDF-1.7\n"), []byte("synthetic PDF bytes")...)
	word := []byte("synthetic Word document bytes")
	video := append([]byte{0, 0, 0, 24}, []byte("ftypmp42synthetic video bytes")...)
	downloads := map[string][]byte{}
	for query, plain := range map[string][]byte{"I": png, "F": pdf, "W": word, "V": video} {
		enc, err := ilink.EncryptECB(plain, key)
		if err != nil {
			t.Fatal(err)
		}
		downloads[query] = enc
	}
	r.ilink.mu.Lock()
	r.ilink.downloads = downloads
	r.ilink.mu.Unlock()
	key64 := base64.StdEncoding.EncodeToString(key)
	r.ilink.say(owner, "",
		map[string]any{"type": 2, "image_item": map[string]any{"aeskey": hex.EncodeToString(key),
			"media": map[string]any{"encrypt_query_param": "I"}}},
		map[string]any{"type": 4, "file_item": map[string]any{"file_name": "report.pdf", "len": strconv.Itoa(len(pdf)),
			"media": map[string]any{"encrypt_query_param": "F", "aes_key": key64}}},
		map[string]any{"type": 4, "file_item": map[string]any{"file_name": "notes.doc",
			"media": map[string]any{"encrypt_query_param": "W", "aes_key": key64}}},
		map[string]any{"type": 5, "video_item": map[string]any{
			"media": map[string]any{"encrypt_query_param": "V", "aes_key": key64}}})
	eventually(t, "the message at the hub", func() bool { return len(r.hub.got()) == 1 })
	m := r.hub.got()[0]
	if len(m.Images) != 1 || m.Images[0].Mime != "image/png" || m.Images[0].Data != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("image handed %+v", m.Images)
	}
	if len(m.Files) != 3 || len(m.Unsupported) != 0 {
		t.Fatalf("handed files=%+v unsupported=%v", m.Files, m.Unsupported)
	}
	for i, want := range []struct {
		name, mime string
		plain      []byte
	}{{"report.pdf", "application/pdf", pdf}, {"notes.doc", "application/msword", word}, {"video.mp4", "video/mp4", video}} {
		got := m.Files[i]
		if got.Name != want.name || got.Mime != want.mime || got.Data != base64.StdEncoding.EncodeToString(want.plain) {
			t.Errorf("file %d handed %+v, want name=%q mime=%q plaintext", i, got, want.name, want.mime)
		}
	}
}

// One failed download does not hide the other attachments; a wholly failed file still reaches the hub as unsupported.
func TestFailedMediaDownloadKeepsTheRest(t *testing.T) {
	r := newRig(t, true)
	key, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	pdf := []byte("%PDF-1.7\nsynthetic PDF bytes")
	enc, err := ilink.EncryptECB(pdf, key)
	if err != nil {
		t.Fatal(err)
	}
	r.ilink.mu.Lock()
	r.ilink.downloads = map[string][]byte{"F": enc}
	r.ilink.mu.Unlock()
	key64 := base64.StdEncoding.EncodeToString(key)
	r.ilink.say(owner, "",
		map[string]any{"type": 4, "file_item": map[string]any{"file_name": "report.pdf",
			"media": map[string]any{"encrypt_query_param": "F", "aes_key": key64}}},
		map[string]any{"type": 5, "video_item": map[string]any{
			"media": map[string]any{"encrypt_query_param": "missing", "aes_key": key64}}})
	eventually(t, "the mixed message at the hub", func() bool { return len(r.hub.got()) == 1 })
	m := r.hub.got()[0]
	if len(m.Files) != 1 || m.Files[0].Name != "report.pdf" || len(m.Unsupported) != 1 || m.Unsupported[0] != "一段视频" {
		t.Fatalf("mixed message handed %+v", m)
	}

	r.ilink.say(owner, "", map[string]any{"type": 4, "file_item": map[string]any{"file_name": "broken.pdf",
		"media": map[string]any{"encrypt_query_param": "broken"}}})
	eventually(t, "the failed file at the hub", func() bool { return len(r.hub.got()) == 2 })
	m = r.hub.got()[1]
	if len(m.Files) != 0 || len(m.Unsupported) != 1 || m.Unsupported[0] != "文件「broken.pdf」" {
		t.Fatalf("failed file handed %+v", m)
	}
}

// The raw attachment budget applies before Base64 reaches the process RPC. Unknown sizes are checked after download;
// a declared oversize attachment is skipped without downloading it.
func TestMessageMediaBudget(t *testing.T) {
	f := newFakeILink(t)
	key, _ := hex.DecodeString("00112233445566778899aabbccddeeff")
	plain := make([]byte, 8<<20)
	copy(plain, "\x89PNG\r\n\x1a\n")
	enc, err := ilink.EncryptECB(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.downloads = map[string][]byte{"F": enc}
	f.mu.Unlock()
	c, err := ilink.New(ilink.Options{BaseURL: "https://ilinkai.weixin.qq.com", Token: "BT", Agent: "Persona/test",
		HTTP: &http.Client{Transport: f}})
	if err != nil {
		t.Fatal(err)
	}
	key64 := base64.StdEncoding.EncodeToString(key)
	media := make([]ilink.Media, 8)
	media[0] = ilink.Media{Kind: ilink.MediaImage, Query: "F", KeyHex: hex.EncodeToString(key)}
	for i := 0; i < 7; i++ {
		media[i+1] = ilink.Media{Kind: ilink.MediaFile, Query: "F", KeyB64: key64,
			FileName: fmt.Sprintf("%d.pdf", i), Size: int64(len(plain))}
	}
	media[5].Size = 0 // the fifth file is still downloaded, then rejected by the decrypted-size guard
	got := (&Bot{}).compose(context.Background(), c, owner, ilink.Message{Media: media})
	if len(got.Images) != 1 || len(got.Files) != 4 || len(got.Unsupported) != 3 ||
		got.Unsupported[0] != "文件「4.pdf」" || got.Unsupported[1] != "文件「5.pdf」" || got.Unsupported[2] != "文件「6.pdf」" {
		t.Fatalf("budget handed %d images, %d files and %d unsupported, want 1, 4 and 3", len(got.Images), len(got.Files), len(got.Unsupported))
	}
}

func TestDuplicateMessageHandledOnce(t *testing.T) {
	r := newRig(t, true)
	item := []map[string]any{{"type": 1, "text_item": map[string]any{"text": "一次就好"}}}
	r.ilink.mu.Lock()
	for range 2 {
		r.ilink.queue = append(r.ilink.queue, map[string]any{"message_id": 9001, "from_user_id": owner, "context_token": "c", "message_type": 1, "item_list": item})
	}
	r.ilink.mu.Unlock()
	eventually(t, "the message at the hub", func() bool { return len(r.hub.got()) >= 1 })
	time.Sleep(100 * time.Millisecond)
	if n := len(r.hub.got()); n != 1 {
		t.Fatalf("handed %d times", n)
	}
}

// channel.send: nothing to send with before the owner's first message (cannot_send); then WeChat's Markdown, in the
// chat of their latest message.
func TestSendNeedsTheOwnersChat(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	eventually(t, "serving", func() bool { return r.bot.Status(ctx).State == wire.ConnectorStateOk })
	if _, err := r.bot.Send(ctx, wire.ChannelSendParams{Ref: "a", Text: "早"}); !errors.Is(err, errCannotSend) {
		t.Fatalf("before the first message: %v", err)
	}
	r.ilink.say(owner, "在吗")
	eventually(t, "the message at the hub", func() bool { return len(r.hub.got()) == 1 })
	if _, err := r.bot.Send(ctx, wire.ChannelSendParams{Ref: "b", Text: "**明天** 10 点有周会。![图](x.png)", Kind: wire.ChannelSendKindAnswer}); err != nil {
		t.Fatal(err)
	}
	got := r.sentTo(owner, "10 点有周会")
	if got.Context == "" || !strings.HasPrefix(got.Text, "**明天**") || strings.Contains(got.Text, "x.png") { // WeChat shows bold, not pictures
		t.Fatalf("sent %+v", got)
	}
}

// Every piece gets one final send log with its inbound message, hub ref, result and duration; the text stays out.
func TestSendLogsEachPiece(t *testing.T) {
	wait := sendRetryWait
	sendRetryWait = time.Millisecond
	t.Cleanup(func() { sendRetryWait = wait })
	r := newRig(t, true)
	ctx := context.Background()
	r.ilink.say(owner, "在吗")
	eventually(t, "the message at the hub", func() bool { return len(r.hub.got()) == 1 })
	eventually(t, "serving", func() bool { return r.bot.Status(ctx).State == wire.ConnectorStateOk })
	logs := captureLog(t)

	p := wire.ChannelSendParams{Ref: "run-1:4", ReplyTo: "message:42", Text: strings.Repeat("answer-body-", 300),
		Kind: wire.ChannelSendKindAnswer}
	if _, err := r.bot.Send(ctx, p); err != nil {
		t.Fatal(err)
	}
	lines := sendLogLines(logs.String())
	if len(lines) != 2 {
		t.Fatalf("got %d send logs, want one for each of two pieces: %q", len(lines), logs.String())
	}
	for i, line := range lines {
		for _, want := range []string{`reply_to="message:42"`, `ref="run-1:4"`, "part=" + strconv.Itoa(i+1) + "/2",
			"result=success", "duration="} {
			if !strings.Contains(line, want) {
				t.Errorf("piece %d log %q does not contain %q", i+1, line, want)
			}
		}
	}
	if strings.Contains(logs.String(), "answer-body-") {
		t.Fatal("send logs contain the answer text")
	}

	logs.Reset()
	r.ilink.mu.Lock()
	r.ilink.failSends = 3
	r.ilink.mu.Unlock()
	p = wire.ChannelSendParams{Ref: "run-2:5", ReplyTo: "message:43", Text: "not-delivered-body",
		Kind: wire.ChannelSendKindAnswer}
	if _, err := r.bot.Send(ctx, p); err == nil {
		t.Fatal("the send should fail after all three attempts")
	}
	lines = sendLogLines(logs.String())
	if len(lines) != 1 {
		t.Fatalf("got %d failure logs, want one: %q", len(lines), logs.String())
	}
	for _, want := range []string{`reply_to="message:43"`, `ref="run-2:5"`, "part=1/1", "result=failure",
		"duration=", "error_type=http", "error=iLink answered HTTP 500"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("failure log %q does not contain %q", lines[0], want)
		}
	}
	if strings.Contains(logs.String(), p.Text) {
		t.Fatal("failure log contains the answer text")
	}
}

// Cancelling the wait after a retryable response is still a final failure and must not skip the piece's log.
func TestSendLogsCancellationDuringRetry(t *testing.T) {
	wait := sendRetryWait
	sendRetryWait = time.Hour
	t.Cleanup(func() { sendRetryWait = wait })
	r := newRig(t, true)
	r.ilink.say(owner, "在吗")
	eventually(t, "the message at the hub", func() bool { return len(r.hub.got()) == 1 })
	r.ilink.mu.Lock()
	r.ilink.failSends = 1
	r.ilink.failedSend = make(chan struct{}, 1)
	r.ilink.mu.Unlock()
	logs := captureLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := wire.ChannelSendParams{Ref: "run-3:6", ReplyTo: "message:44", Text: "cancelled", Kind: wire.ChannelSendKindAnswer}
	done := make(chan error, 1)
	go func() {
		_, err := r.bot.Send(ctx, p)
		done <- err
	}()
	select {
	case <-r.ilink.failedSend:
	case <-time.After(5 * time.Second):
		t.Fatal("the first retryable send did not finish")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled send should fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling the retry wait did not stop the send")
	}
	lines := sendLogLines(logs.String())
	if len(lines) != 1 {
		t.Fatalf("got %d cancellation logs, want one: %q", len(lines), logs.String())
	}
	for _, want := range []string{`reply_to="message:44"`, `ref="run-3:6"`, "part=1/1", "result=failure",
		"duration=", "error_type=aborted"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("cancellation log %q does not contain %q", lines[0], want)
		}
	}
}

// A send whose answer is lost is sent again with the same client_id, by the bot and by the hub's retry of the same
// ref after the bot gave up: iLink keeps it once (#4530).
func TestSendRetryKeepsTheClientID(t *testing.T) {
	wait := sendRetryWait
	sendRetryWait = time.Millisecond
	t.Cleanup(func() { sendRetryWait = wait })
	r := newRig(t, true)
	ctx := context.Background()
	r.ilink.say(owner, "在吗")
	eventually(t, "the message at the hub", func() bool { return len(r.hub.got()) == 1 })
	eventually(t, "serving", func() bool { return r.bot.Status(ctx).State == wire.ConnectorStateOk })
	r.ilink.mu.Lock()
	r.ilink.failSends, r.ilink.clientIDs, r.ilink.sent = 3, nil, nil // every attempt of the first Send loses its answer
	r.ilink.mu.Unlock()
	p := wire.ChannelSendParams{Ref: "run-1:4", Text: "答案", Kind: wire.ChannelSendKindAnswer}
	if _, err := r.bot.Send(ctx, p); err == nil {
		t.Fatal("the first Send should fail: no attempt got an answer")
	}
	if _, err := r.bot.Send(ctx, p); err != nil { // the hub sends the same ref again
		t.Fatal(err)
	}
	r.ilink.mu.Lock()
	ids, sent := append([]string(nil), r.ilink.clientIDs...), append([]sentText(nil), r.ilink.sent...)
	r.ilink.mu.Unlock()
	if len(ids) != 4 || len(slices.Compact(slices.Clone(ids))) != 1 {
		t.Fatalf("client_ids %q: every attempt must carry the same one", ids)
	}
	if len(sent) != 1 {
		t.Fatalf("the owner got %d copies: %+v", len(sent), sent)
	}
	if clientID("other", 0) == ids[0] || clientID(p.Ref, 1) == ids[0] {
		t.Fatal("another ref or piece must get another client_id")
	}
}

// "typing…" while the hub says so, off when it says it is done.
func TestTypingWhileTheHubSays(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	r.ilink.say(owner, "在吗")
	eventually(t, "the message at the hub", func() bool { return len(r.hub.got()) == 1 })
	if err := r.bot.Typing(ctx, true); err != nil {
		t.Fatal(err)
	}
	_ = r.bot.Typing(ctx, true) // again within 5 s: WeChat is not told twice
	if err := r.bot.Typing(ctx, false); err != nil {
		t.Fatal(err)
	}
	r.ilink.mu.Lock()
	typing := append([]bool(nil), r.ilink.typing...)
	r.ilink.mu.Unlock()
	if len(typing) != 2 || !typing[0] || typing[1] {
		t.Fatalf("typing %v", typing)
	}
}

// What the bot writes for people reads in the language the hub said at its start; Chinese when it said none.
func TestTextInTheUsersLanguage(t *testing.T) {
	if got := New(Options{Lang: langEn}, nil).status.Error; got != "WeChat isn't bound yet" {
		t.Fatal(got)
	}
	if got := New(Options{}, nil).status.Error; got != "还没绑定微信" {
		t.Fatal(got)
	}
	if p := codePrompt(langJa, ""); p.Fields[0].Label != "確認コード" {
		t.Fatal(p.Fields[0].Label)
	}
	c := &ilink.Credentials{UserID: "owner-fixture@example.invalid"}
	if got := account(langEn, c); got != "WeChat user ixture" {
		t.Fatal(got)
	}
}
