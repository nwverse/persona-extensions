package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/wechat/internal/wechat/ilink"
	"github.com/nwverse/persona/sdk/go/wire"
)

// answer scripts what the next calls of path answer (fakeILink.answers).
func (f *fakeILink) answer(path string, as ...fakeAnswer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.answers == nil {
		f.answers = map[string][]fakeAnswer{}
	}
	f.answers[path] = append(f.answers[path], as...)
}

// unanswered is how many scripted answers are left.
func (f *fakeILink) unanswered() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, as := range f.answers {
		n += len(as)
	}
	return n
}

// cdnUploads are the requests the CDN got for an upload, taken or not.
func (f *fakeILink) cdnUploads() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeRequest
	for _, r := range f.requests {
		if r.Path == "/c2c/upload" {
			out = append(out, r)
		}
	}
	return out
}

// tally is what iLink and the CDN were asked so far: sendmessage (repeats too), getuploadurl, and CDN uploads.
type tally struct{ sends, uploadURLs, cdn int }

func (r *rig) tally() tally {
	sends, urls, _ := r.ilink.calls()
	return tally{sends, urls, len(r.ilink.cdnUploads())}
}

func (a tally) minus(b tally) tally {
	return tally{a.sends - b.sends, a.uploadURLs - b.uploadURLs, a.cdn - b.cdn}
}

// countWaits records each wait between tries instead of waiting.
func countWaits(t *testing.T) func() []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var waits []time.Duration
	prev := retryWait
	retryWait = func(ctx context.Context, d time.Duration) bool {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return ctx.Err() == nil
	}
	t.Cleanup(func() { retryWait = prev })
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(waits)
	}
}

func samePicture(data []byte, want wire.ChannelBlob) bool {
	return base64.StdEncoding.EncodeToString(data) == want.Data
}

const oneNotSent = "有 1 张图片没发出去。"

// A picture WeChat refuses in a way that does not pass (the CDN answers 400; sendmessage a non-zero ret) is tried once
// and skipped; the pictures after it still go, and a last text says how many were not sent. The send succeeds.
func TestSendSkipsAPictureWeChatRefuses(t *testing.T) {
	r := pictureSendRig(t)
	ctx := context.Background()
	logs := captureLog(t)
	r.ilink.answer("/c2c/upload", fakeAnswer{}, fakeAnswer{status: http.StatusBadRequest}) // picture 2's upload
	p := wire.ChannelSendParams{Ref: "run-11:1", ReplyTo: "message:60", Text: "三张图", Kind: wire.ChannelSendKindAnswer,
		Images: []wire.ChannelBlob{testPicture(1), testPicture(2), testPicture(3)}}
	if _, err := r.bot.Send(ctx, p); err != nil {
		t.Fatal(err)
	}
	if n := len(r.ilink.cdnUploads()); n != 3 {
		t.Fatalf("%d CDN uploads, want one for each picture: the refused one is not tried again", n)
	}
	_, plain := r.ilink.sentPictures(t)
	if len(plain) != 2 || !samePicture(plain[0], testPicture(1)) || !samePicture(plain[1], testPicture(3)) {
		t.Fatalf("iLink holds %d pictures, want pictures 1 and 3", len(plain))
	}
	texts := r.ilink.texts()
	if len(texts) != 2 || texts[0].Text != "三张图" || texts[1].Text != oneNotSent {
		t.Fatalf("texts %+v", texts)
	}
	ids := newSendIDs(p.Ref, owner, splitText(p.Text), p.Images)
	r.ilink.mu.Lock()
	last := r.ilink.clientIDs[len(r.ilink.clientIDs)-1]
	r.ilink.mu.Unlock()
	if last != ids.note(1) {
		t.Fatal("the note is not the last message")
	}
	if _, ok := r.progressOf(p.Ref); ok {
		t.Fatal("progress is kept after the send went through")
	}
	lines := sendLogLines(logs.String())
	if len(lines) != 5 {
		t.Fatalf("%d send logs, want one each for the text, three pictures and the note: %q", len(lines), lines)
	}
	for i, want := range [][]string{
		{"part=1/1", "result=success"},
		{"image=1/3", "result=success"},
		{`ref="run-11:1"`, "image=2/3", "result=skipped", "reason=refused", "error_type=http", "error=picture upload answered HTTP 400"},
		{"image=3/3", "result=success"},
		{`reply_to="message:60"`, `ref="run-11:1"`, " note ", "result=success"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[i], w) {
				t.Errorf("log %q does not contain %q", lines[i], w)
			}
		}
	}

	// sendmessage refusing a picture: asked once, the picture not uploaded again.
	r.ilink.answer("/ilink/bot/sendmessage", fakeAnswer{body: map[string]any{"ret": -2}})
	before := r.tally()
	alone := wire.ChannelSendParams{Ref: "run-11:2", Kind: wire.ChannelSendKindAnswer, Images: []wire.ChannelBlob{testPicture(4), testPicture(5)}}
	if _, err := r.bot.Send(ctx, alone); err != nil {
		t.Fatal(err)
	}
	if got := r.tally().minus(before); got != (tally{sends: 3, uploadURLs: 2, cdn: 2}) { // picture 4 refused, picture 5, the note
		t.Fatalf("calls %+v", got)
	}
	_, plain = r.ilink.sentPictures(t)
	texts = r.ilink.texts()
	if len(plain) != 3 || !samePicture(plain[2], testPicture(5)) || texts[len(texts)-1].Text != oneNotSent {
		t.Fatalf("%d pictures, last text %q", len(plain), texts[len(texts)-1].Text)
	}
}

// How many pictures were not sent is kept with the ref's progress: a picture skipped before the send stopped at a
// failure that may pass is counted when the hub's retry finishes the send, and a note that did not go out is all the
// next retry sends.
func TestSendCountsSkippedPicturesAcrossTheHubsRetry(t *testing.T) {
	r := pictureSendRig(t)
	ctx := context.Background()
	p := wire.ChannelSendParams{Ref: "run-12:1", Kind: wire.ChannelSendKindAnswer,
		Images: []wire.ChannelBlob{testPicture(1), testPicture(2), testPicture(3)}}
	ids := newSendIDs(p.Ref, owner, nil, p.Images)
	// Picture 1 refused by the CDN; picture 2's three uploads fail in a way that may pass.
	failed := fakeAnswer{status: http.StatusInternalServerError}
	r.ilink.answer("/c2c/upload", fakeAnswer{status: http.StatusBadRequest}, failed, failed, failed)
	var sf sendFailed
	if _, err := r.bot.Send(ctx, p); !errors.As(err, &sf) {
		t.Fatalf("the first send: %v", err)
	}
	if pr, ok := r.progressOf(p.Ref); !ok || len(pr.done) != 1 || !pr.skipped[ids.images[0]] {
		t.Fatalf("progress %+v %v", pr, ok)
	}

	// The hub's retry: pictures 2 and 3 go, then the note, whose three sends fail.
	r.ilink.answer("/ilink/bot/sendmessage", fakeAnswer{}, fakeAnswer{}, failed, failed, failed)
	if _, err := r.bot.Send(ctx, p); !errors.As(err, &sf) {
		t.Fatalf("the second send: %v", err)
	}
	if pics, _ := r.ilink.sentPictures(t); len(pics) != 2 || len(r.ilink.texts()) != 0 {
		t.Fatalf("iLink holds %d pictures and %d texts", len(pics), len(r.ilink.texts()))
	}

	// The next retry sends the note only.
	before := r.tally()
	if _, err := r.bot.Send(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := r.tally().minus(before); got != (tally{sends: 1}) {
		t.Fatalf("the last retry made %+v calls, want the note only", got)
	}
	if texts := r.ilink.texts(); len(texts) != 1 || texts[0].Text != oneNotSent {
		t.Fatalf("texts %+v", texts)
	}
	if r.ilink.unanswered() != 0 {
		t.Fatal("scripted answers left")
	}
}

// A picture the CDN took whose send fails in a way that may pass is not uploaded again: the bot's own tries and the
// hub's retry of the ref send the same upload with the same client_id. The tries wait sendRetryWait, then twice that,
// and nothing after the last one, whose error is the one logged.
func TestSendKeepsAnUploadTheCDNTook(t *testing.T) {
	r := pictureSendRig(t)
	ctx := context.Background()
	waits := countWaits(t)
	logs := captureLog(t)
	r.ilink.mu.Lock()
	r.ilink.failSends = 3 // every try's answer is lost
	r.ilink.mu.Unlock()
	p := wire.ChannelSendParams{Ref: "run-13:1", ReplyTo: "message:70", Kind: wire.ChannelSendKindAnswer, Images: []wire.ChannelBlob{testPicture(6)}}
	start := r.tally()
	if _, err := r.bot.Send(ctx, p); err == nil {
		t.Fatal("the send should fail: no try got an answer")
	}
	if got := r.tally().minus(start); got != (tally{sends: 3, uploadURLs: 1, cdn: 1}) {
		t.Fatalf("calls %+v, want one upload and three sends", got)
	}
	if got := waits(); !slices.Equal(got, []time.Duration{sendRetryWait, 2 * sendRetryWait}) {
		t.Fatalf("waits %v, want %v and %v and none after the last try", got, sendRetryWait, 2*sendRetryWait)
	}
	lines := sendLogLines(logs.String())
	if len(lines) != 1 || !strings.Contains(lines[0], "image=1/1 result=failure") || !strings.Contains(lines[0], "error_type=http") ||
		!strings.Contains(lines[0], "error=iLink answered HTTP 500") {
		t.Fatalf("logs %q", lines)
	}
	id := newSendIDs(p.Ref, owner, nil, p.Images).images[0]
	if pr, ok := r.progressOf(p.Ref); !ok || pr.uploadOf != id || pr.upload.Query == "" || len(pr.done) != 0 {
		t.Fatalf("progress %+v %v", pr, ok)
	}

	if _, err := r.bot.Send(ctx, p); err != nil { // the hub sends the ref again
		t.Fatal(err)
	}
	if got := r.tally().minus(start); got != (tally{sends: 4, uploadURLs: 1, cdn: 1}) {
		t.Fatalf("calls %+v, want the hub's retry to send the upload it has", got)
	}
	r.ilink.mu.Lock()
	clientIDs := slices.Clone(r.ilink.clientIDs)
	r.ilink.mu.Unlock()
	if len(slices.Compact(clientIDs)) != 1 || clientIDs[0] != id {
		t.Fatalf("client_ids %q: every send of the picture must carry its own one", clientIDs)
	}
	pics, plain := r.ilink.sentPictures(t)
	if len(pics) != 1 || !samePicture(plain[0], testPicture(6)) {
		t.Fatalf("iLink holds %d pictures", len(pics))
	}
}

// What getuploadurl and the CDN answer decides what happens to the picture: a refusal (a non-zero ret, HTTP 404) skips
// it after one try and the owner is told; a failure that may pass (HTTP 503 or 429, no upload URL, no download
// reference) is tried three times, each with a new upload URL, and fails the send for the hub to try again.
func TestSendPictureUploadFailures(t *testing.T) {
	r := pictureSendRig(t)
	ctx := context.Background()
	ret := func(n int) fakeAnswer { return fakeAnswer{body: map[string]any{"ret": n}} }
	status := func(code int) fakeAnswer { return fakeAnswer{status: code} }
	thrice := func(a fakeAnswer) []fakeAnswer { return []fakeAnswer{a, a, a} }
	tests := []struct {
		name          string
		path          string
		answers       []fakeAnswer
		skipped       bool
		uploadURLs, n int // getuploadurl and CDN requests
	}{
		{name: "getuploadurl refuses", path: "/ilink/bot/getuploadurl", answers: []fakeAnswer{ret(-1)}, skipped: true, uploadURLs: 1},
		{name: "getuploadurl 404", path: "/ilink/bot/getuploadurl", answers: []fakeAnswer{status(http.StatusNotFound)}, skipped: true, uploadURLs: 1},
		{name: "getuploadurl 503", path: "/ilink/bot/getuploadurl", answers: thrice(status(http.StatusServiceUnavailable)), uploadURLs: 3},
		{name: "no upload URL", path: "/ilink/bot/getuploadurl", answers: thrice(ret(0)), uploadURLs: 3},
		{name: "CDN 429", path: "/c2c/upload", answers: thrice(status(http.StatusTooManyRequests)), uploadURLs: 3, n: 3},
		{name: "no download reference", path: "/c2c/upload", answers: thrice(status(http.StatusOK)), uploadURLs: 3, n: 3},
	}
	for i, tt := range tests {
		r.ilink.answer(tt.path, tt.answers...)
		before, texts := r.tally(), len(r.ilink.texts())
		p := wire.ChannelSendParams{Ref: "run-18:" + tt.name, Kind: wire.ChannelSendKindAnswer, Images: []wire.ChannelBlob{testPicture(byte(20 + i))}}
		_, err := r.bot.Send(ctx, p)
		got := r.tally().minus(before)
		if tt.skipped {
			if err != nil || got != (tally{sends: 1, uploadURLs: tt.uploadURLs}) || r.ilink.texts()[texts].Text != oneNotSent {
				t.Errorf("%s: %v, calls %+v: want the picture skipped after one try and the note sent", tt.name, err, got)
			}
		} else if sf := (sendFailed{}); !errors.As(err, &sf) || got != (tally{uploadURLs: tt.uploadURLs, cdn: tt.n}) {
			t.Errorf("%s: %v, calls %+v: want three tries and a failed send", tt.name, err, got)
		}
		if r.ilink.unanswered() != 0 {
			t.Fatalf("%s: scripted answers left", tt.name)
		}
	}
	if pics, _ := r.ilink.sentPictures(t); len(pics) != 0 {
		t.Fatalf("iLink holds %d pictures", len(pics))
	}
}

// getuploadurl's full URL is where the picture goes, without the bot token the iLink calls carry; one that is not
// Tencent's HTTPS gets no request, and its picture is skipped.
func TestSendPictureToTheFullUploadURL(t *testing.T) {
	r := pictureSendRig(t)
	ctx := context.Background()
	full := "https://full.weixin.qq.com/c2c/upload?encrypted_query_param=FULL&filekey=FK"
	r.ilink.answer("/ilink/bot/getuploadurl", fakeAnswer{body: map[string]any{"ret": 0, "upload_full_url": full}})
	if _, err := r.bot.Send(ctx, wire.ChannelSendParams{Ref: "run-14:1", Kind: wire.ChannelSendKindAnswer, Images: []wire.ChannelBlob{testPicture(7)}}); err != nil {
		t.Fatal(err)
	}
	cdn := r.ilink.cdnUploads()
	if len(cdn) != 1 || cdn[0].Host != "full.weixin.qq.com" {
		t.Fatalf("CDN uploads %+v, want one to the full URL", cdn)
	}
	if cdn[0].Auth != "" {
		t.Fatalf("the CDN got the bot token: %q", cdn[0].Auth)
	}
	r.ilink.mu.Lock()
	for _, req := range r.ilink.requests {
		if req.Path == "/ilink/bot/getuploadurl" && req.Auth != "Bearer BT" {
			t.Errorf("getuploadurl without the bot token: %+v", req)
		}
	}
	r.ilink.mu.Unlock()
	pics, plain := r.ilink.sentPictures(t)
	if len(pics) != 1 || pics[0].Query != "DL-FULL-FK" || !samePicture(plain[0], testPicture(7)) {
		t.Fatalf("pictures %+v", pics)
	}
	if len(r.ilink.texts()) != 0 {
		t.Fatal("a note for a send that went through")
	}

	r.ilink.answer("/ilink/bot/getuploadurl", fakeAnswer{body: map[string]any{"ret": 0, "upload_full_url": "https://upload.example.com/c2c/upload"}})
	if _, err := r.bot.Send(ctx, wire.ChannelSendParams{Ref: "run-14:2", Kind: wire.ChannelSendKindAnswer, Images: []wire.ChannelBlob{testPicture(8)}}); err != nil {
		t.Fatal(err)
	}
	r.ilink.mu.Lock()
	for _, req := range r.ilink.requests {
		if req.Host == "upload.example.com" {
			t.Error("a rejected upload URL was asked")
		}
	}
	r.ilink.mu.Unlock()
	if len(r.ilink.cdnUploads()) != 1 {
		t.Fatal("a rejected upload URL made a CDN upload")
	}
	if texts := r.ilink.texts(); len(texts) != 1 || texts[0].Text != oneNotSent {
		t.Fatalf("texts %+v", texts)
	}
}

// A send whose second text piece keeps failing stops there; the hub's retry sends that piece only.
func TestSendResumesAtTheFailedPiece(t *testing.T) {
	r := pictureSendRig(t)
	ctx := context.Background()
	logs := captureLog(t)
	p := wire.ChannelSendParams{Ref: "run-15:1", Text: strings.Repeat("answer-body-", 300), Kind: wire.ChannelSendKindAnswer}
	parts := splitText(p.Text)
	failed := fakeAnswer{status: http.StatusInternalServerError}
	r.ilink.answer("/ilink/bot/sendmessage", fakeAnswer{}, failed, failed, failed)
	if _, err := r.bot.Send(ctx, p); err == nil {
		t.Fatal("the first send should fail at its second piece")
	}
	if texts := r.ilink.texts(); len(parts) != 2 || len(texts) != 1 || texts[0].Text != parts[0] {
		t.Fatalf("%d pieces; iLink holds %d", len(parts), len(texts))
	}
	before := r.tally()
	if _, err := r.bot.Send(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got := r.tally().minus(before); got != (tally{sends: 1}) {
		t.Fatalf("the retry made %+v calls, want the second piece only", got)
	}
	if texts := r.ilink.texts(); len(texts) != 2 || texts[1].Text != parts[1] {
		t.Fatalf("iLink holds %d texts", len(texts))
	}
	if !strings.Contains(logs.String(), `ref="run-15:1" resume text=1/2 image=0/0`) {
		t.Fatalf("no resume log: %q", logs.String())
	}
}

// A picture the bot sent is in the quote index by its server message id: the owner quoting it is restored as a
// picture, like a quote of a picture they sent.
func TestSentPictureIsAvailableForQuoteRestore(t *testing.T) {
	r := pictureSendRig(t)
	r.ilink.mu.Lock()
	r.ilink.sendID = "outbound-picture-id"
	r.ilink.mu.Unlock()
	if _, err := r.bot.Send(t.Context(), wire.ChannelSendParams{Ref: "run-16:1", Kind: wire.ChannelSendKindAnswer, Images: []wire.ChannelBlob{testPicture(9)}}); err != nil {
		t.Fatal(err)
	}
	queueQuoteMessage(r, 9501, map[string]any{"type": 1, "text_item": map[string]any{"text": "这张图"},
		"ref_msg": map[string]any{"svr_id": "outbound-picture-id"}})
	eventually(t, "the quote at the hub", func() bool { return len(r.hub.got()) == 2 })
	if got := r.hub.got()[1]; got.Text != "这张图" || got.Quote != "[图片]" {
		t.Fatalf("quote of the bot's picture = %+v", got)
	}
}

// Without a ref (only tests send so: channel.send needs one) each picture gets a random client_id, so the same picture
// sent twice is two messages, as text pieces are.
func TestSendWithoutARefGivesPicturesRandomIDs(t *testing.T) {
	r := pictureSendRig(t)
	p := wire.ChannelSendParams{Kind: wire.ChannelSendKindAnswer, Images: []wire.ChannelBlob{testPicture(1)}}
	for range 2 {
		if _, err := r.bot.Send(t.Context(), p); err != nil {
			t.Fatal(err)
		}
	}
	r.ilink.mu.Lock()
	clientIDs := slices.Clone(r.ilink.clientIDs)
	r.ilink.mu.Unlock()
	if len(clientIDs) != 2 || clientIDs[0] == clientIDs[1] {
		t.Fatalf("client_ids %q", clientIDs)
	}
	if pics, _ := r.ilink.sentPictures(t); len(pics) != 2 {
		t.Fatalf("iLink holds %d pictures", len(pics))
	}
}

// What was sent to a WeChat no longer bound is forgotten: logging out, or binding another account, drops every ref's
// progress, so a later send of a ref does not skip what only the old account got.
func TestUnbindForgetsSendProgress(t *testing.T) {
	r := pictureSendRig(t)
	r.ilink.mu.Lock()
	r.ilink.failUploads = 3
	r.ilink.mu.Unlock()
	if _, err := r.bot.Send(t.Context(), wire.ChannelSendParams{Ref: "run-17:1", Text: "看图", Kind: wire.ChannelSendKindAnswer,
		Images: []wire.ChannelBlob{testPicture(1)}}); err == nil {
		t.Fatal("the send should fail")
	}
	if _, ok := r.progressOf("run-17:1"); !ok {
		t.Fatal("no progress after the failure")
	}
	if err := r.bot.Logout(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.bot.mu.Lock()
	kept, order := len(r.bot.progress), len(r.bot.progressOrder)
	r.bot.mu.Unlock()
	if kept != 0 || order != 0 {
		t.Fatalf("%d refs kept after logging out", kept)
	}

	b := New(Options{}, &memSecrets{})
	b.st.Creds = &ilink.Credentials{BotID: "bot-a", UserID: "account-a", Token: "token-a"}
	b.loaded = true
	b.keep("ref", 0, sendProgress{done: map[string]bool{"persona-x": true}})
	b.bound(t.Context(), ilink.Credentials{BotID: "bot-a", UserID: "account-a", Token: "token-a2"}) // the same account again
	if len(b.progress) != 1 {
		t.Fatal("binding the same account again dropped the progress")
	}
	b.bound(t.Context(), ilink.Credentials{BotID: "bot-b", UserID: "account-b", Token: "token-b"})
	if len(b.progress) != 0 || len(b.progressOrder) != 0 {
		t.Fatalf("%d refs kept after binding another account", len(b.progress))
	}
}

// A send still going at sendDeadline stops as a failure that may pass, well before the hub's 2-minute call timeout
// and the upload's own 60 s; what it got is kept, and the hub's retry goes on from there.
func TestSendStopsAtTheDeadline(t *testing.T) {
	if sendDeadline <= 0 || sendDeadline > 2*time.Minute-15*time.Second {
		t.Fatalf("sendDeadline %s leaves the hub's 2-minute call timeout too little room", sendDeadline)
	}
	r := pictureSendRig(t)
	ctx := context.Background()
	prev := sendDeadline
	sendDeadline = 300 * time.Millisecond
	t.Cleanup(func() { sendDeadline = prev })
	logs := captureLog(t)
	r.ilink.answer("/c2c/upload", fakeAnswer{}, fakeAnswer{hang: true}) // picture 2's upload never answers
	p := wire.ChannelSendParams{Ref: "run-19:1", ReplyTo: "message:80", Text: "两张图", Kind: wire.ChannelSendKindAnswer,
		Images: []wire.ChannelBlob{testPicture(1), testPicture(2)}}
	started := time.Now()
	_, err := r.bot.Send(ctx, p)
	took := time.Since(started)
	var sf sendFailed
	if !errors.As(err, &sf) {
		t.Fatalf("a send out of time: %v", err)
	}
	if took > 5*time.Second {
		t.Fatalf("the send took %s: it did not stop at its deadline", took)
	}
	ids := newSendIDs(p.Ref, owner, splitText(p.Text), p.Images)
	if pr, ok := r.progressOf(p.Ref); !ok || len(pr.done) != 2 || !pr.done[ids.parts[0]] || !pr.done[ids.images[0]] {
		t.Fatalf("progress %+v %v: want the text and picture 1", pr, ok)
	}
	var picture2 string
	for _, l := range sendLogLines(logs.String()) {
		if strings.Contains(l, "image=2/2") {
			picture2 = l
		}
	}
	if !strings.Contains(picture2, "result=failure") || !strings.Contains(picture2, "error_type=timeout") {
		t.Fatalf("picture 2's log %q", picture2)
	}
	if !strings.Contains(logs.String(), `ref="run-19:1" stopped: out of time (300ms at most)`) {
		t.Fatalf("no deadline log: %q", logs.String())
	}

	sendDeadline = prev
	before := r.tally()
	if _, err := r.bot.Send(ctx, p); err != nil { // the hub sends the ref again
		t.Fatal(err)
	}
	if got := r.tally().minus(before); got != (tally{sends: 1, uploadURLs: 1, cdn: 1}) {
		t.Fatalf("the retry made %+v calls, want picture 2 only", got)
	}
	if pics, _ := r.ilink.sentPictures(t); len(pics) != 2 || len(r.ilink.texts()) != 1 {
		t.Fatalf("iLink holds %d pictures and %d texts", len(pics), len(r.ilink.texts()))
	}
}

// Through the program: a ref that went through and then comes again with other content (another picture) is sent
// again whole, under new client_ids; the same content again is not sent.
func TestProgramSendsChangedContentUnderARef(t *testing.T) {
	r := pictureSendRig(t)
	ctx := context.Background()
	p := &program{bot: r.bot}
	send := func(sp wire.ChannelSendParams) error {
		sp.Contribution = ID
		params, _ := json.Marshal(sp)
		_, err := p.channel(ctx, "send", params)
		return err
	}
	a := wire.ChannelSendParams{Ref: "run-20:1", Text: "看图", Kind: wire.ChannelSendKindAnswer, Images: []wire.ChannelBlob{testPicture(1)}}
	if err := send(a); err != nil {
		t.Fatal(err)
	}
	r.ilink.mu.Lock()
	first := slices.Clone(r.ilink.clientIDs)
	r.ilink.mu.Unlock()
	b := a
	b.Images = []wire.ChannelBlob{testPicture(3)}
	before := r.tally()
	if err := send(b); err != nil {
		t.Fatal(err)
	}
	if got := r.tally().minus(before); got != (tally{sends: 2, uploadURLs: 1, cdn: 1}) {
		t.Fatalf("other content under a sent ref: calls %+v, want the text and the picture again", got)
	}
	r.ilink.mu.Lock()
	again := slices.Clone(r.ilink.clientIDs[len(first):])
	r.ilink.mu.Unlock()
	for _, id := range again {
		if slices.Contains(first, id) {
			t.Fatalf("client_id %s of the first content is used for the second", id)
		}
	}
	_, plain := r.ilink.sentPictures(t)
	if len(plain) != 2 || !samePicture(plain[1], testPicture(3)) || len(r.ilink.texts()) != 2 {
		t.Fatalf("iLink holds %d pictures and %d texts", len(plain), len(r.ilink.texts()))
	}
	before = r.tally()
	if err := send(b); err != nil {
		t.Fatal(err)
	}
	if got := r.tally().minus(before); got != (tally{}) {
		t.Fatalf("the same content again: calls %+v, want none", got)
	}
}

// A send still running when the WeChat is unbound keeps no progress when it then fails: another WeChat bound after it
// gets all of the same ref and content, none of it skipped as sent or its picture taken from the old upload.
func TestSendRunningAcrossAnUnbind(t *testing.T) {
	r := pictureSendRig(t)
	r.ilink.answer("/c2c/upload", fakeAnswer{hang: true})
	p := wire.ChannelSendParams{Ref: "run-21:1", Text: "看图", Kind: wire.ChannelSendKindAnswer, Images: []wire.ChannelBlob{testPicture(1)}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.bot.Send(ctx, p)
		done <- err
	}()
	eventually(t, "the picture's upload", func() bool { return len(r.ilink.cdnUploads()) == 1 })
	if err := r.bot.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel() // the old send fails after the unbind
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the old send should fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the old send did not end")
	}
	if pr, ok := r.progressOf(p.Ref); ok {
		t.Fatalf("a send that ran across the unbind kept its progress: %+v", pr)
	}

	const other = "other@im.wechat"
	r.bot.bound(context.Background(), ilink.Credentials{Token: "BT2", BotID: "bot2@im.bot", UserID: other, BaseURL: "https://ilinkai.weixin.qq.com/"})
	r.ilink.say(other, "你好")
	eventually(t, "the new WeChat's chat", func() bool {
		r.bot.mu.Lock()
		defer r.bot.mu.Unlock()
		return r.bot.client != nil && r.bot.st.Creds != nil && r.bot.st.Creds.UserID == other && r.bot.st.Context != ""
	})
	before := r.tally()
	if _, err := r.bot.Send(context.Background(), p); err != nil { // the hub sends the ref again
		t.Fatal(err)
	}
	if got := r.tally().minus(before); got != (tally{sends: 2, uploadURLs: 1, cdn: 1}) {
		t.Fatalf("the new WeChat's send: calls %+v, want the text, an upload and the picture", got)
	}
	r.sentTo(other, "看图")
	if pics, _ := r.ilink.sentPictures(t); len(pics) != 1 || pics[0].To != other {
		t.Fatalf("pictures %+v", pics)
	}
}
