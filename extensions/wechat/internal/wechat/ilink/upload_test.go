package ilink

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"
)

// cdnRequest is a CDN upload the fake got.
type cdnRequest struct {
	host, path, query, auth, contentType string
	body                                 []byte
}

// uploadFake is iLink and the CDN: getuploadurl answers uploadURL (an upload_param when nil), the CDN answers cdn (the
// download reference "DL" when nil), sendmessage answers send; every CDN request is recorded.
type uploadFake struct {
	*fake
	uploadURL func(w http.ResponseWriter, body map[string]any) bool // true: answered
	cdn       func(w http.ResponseWriter) bool
	send      func(w http.ResponseWriter, body map[string]any) bool
	uploadReq []map[string]any
	uploadAut []string
	cdnReqs   []cdnRequest
	sends     []map[string]any
}

func newUploadFake(t *testing.T) *uploadFake {
	u := &uploadFake{}
	u.fake = newFake(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			u.uploadReq = append(u.uploadReq, body)
			u.uploadAut = append(u.uploadAut, r.Header.Get("Authorization"))
			if u.uploadURL == nil || !u.uploadURL(w, body) {
				writeJSON(w, map[string]any{"ret": 0, "upload_param": "UP"})
			}
		case "/c2c/upload":
			data, _ := io.ReadAll(r.Body)
			u.cdnReqs = append(u.cdnReqs, cdnRequest{host: u.hosts[len(u.hosts)-1], path: r.URL.Path, query: r.URL.RawQuery,
				auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), body: data})
			if u.cdn == nil || !u.cdn(w) {
				w.Header().Set("x-encrypted-param", "DL")
				w.WriteHeader(http.StatusOK)
			}
		case "/ilink/bot/sendmessage":
			u.sends = append(u.sends, body)
			if u.send == nil || !u.send(w, body) {
				writeJSON(w, map[string]any{"ret": 0, "message_id": 42})
			}
		default:
			t.Errorf("unexpected %s", r.URL)
		}
	})
	return u
}

// A picture is encrypted with a fresh key, named to getuploadurl (with the bot token), posted to the CDN where it says
// (without the bot token) and sent as an image_item naming the CDN's download reference; the send answers the server
// message id.
func TestUploadImageAndSendImage(t *testing.T) {
	f := newUploadFake(t)
	c := f.client("BT", "")
	ctx := context.Background()
	pic := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{3}, 40)...)
	up, err := c.UploadImage(ctx, "owner@im", pic)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.uploadReq) != 1 || len(f.cdnReqs) != 1 {
		t.Fatalf("%d getuploadurl, %d CDN requests", len(f.uploadReq), len(f.cdnReqs))
	}
	req, cdn := f.uploadReq[0], f.cdnReqs[0]
	sum := md5.Sum(pic)
	if req["media_type"] != float64(mediaImage) || req["to_user_id"] != "owner@im" || req["rawsize"] != float64(len(pic)) ||
		req["rawfilemd5"] != hex.EncodeToString(sum[:]) || req["filesize"] != float64(len(cdn.body)) || req["no_need_thumb"] != true {
		t.Errorf("getuploadurl %v", req)
	}
	if f.uploadAut[0] != "Bearer BT" {
		t.Errorf("getuploadurl Authorization %q", f.uploadAut[0])
	}
	if cdn.host != "novac2c.cdn.weixin.qq.com" || cdn.query != "encrypted_query_param=UP&filekey="+req["filekey"].(string) ||
		cdn.contentType != "application/octet-stream" {
		t.Errorf("CDN upload %+v", cdn)
	}
	if cdn.auth != "" {
		t.Errorf("the CDN got the bot token: %q", cdn.auth)
	}
	key, _ := hex.DecodeString(req["aeskey"].(string))
	if plain, err := DecryptECB(cdn.body, key); err != nil || !bytes.Equal(plain, pic) {
		t.Fatalf("the CDN got something else: %v", err)
	}
	if up != (Uploaded{Query: "DL", KeyHex: req["aeskey"].(string), CipherSize: len(cdn.body)}) {
		t.Fatalf("uploaded %+v", up)
	}

	id, err := c.SendImage(ctx, "owner@im", "CT", "cid", up)
	if err != nil || id != "42" {
		t.Fatalf("SendImage = %q, %v", id, err)
	}
	msg := f.sends[0]["msg"].(map[string]any)
	item := msg["item_list"].([]any)[0].(map[string]any)
	img := item["image_item"].(map[string]any)
	media := img["media"].(map[string]any)
	if msg["to_user_id"] != "owner@im" || msg["client_id"] != "cid" || msg["context_token"] != "CT" || item["type"] != float64(itemImage) ||
		media["encrypt_query_param"] != "DL" || media["aes_key"] != base64.StdEncoding.EncodeToString([]byte(up.KeyHex)) ||
		media["encrypt_type"] != float64(1) || img["mid_size"] != float64(up.CipherSize) {
		t.Errorf("sendmessage %v", msg)
	}

	if _, err := c.SendImage(ctx, "owner@im", "CT", "cid", Uploaded{Query: "DL", KeyHex: "not hex", CipherSize: 16}); CodeOf(err) != CodeProtocol || Retryable(err) {
		t.Errorf("a bad key: %v", err)
	}
	if len(f.sends) != 1 {
		t.Error("an incomplete picture message was sent")
	}
}

// getuploadurl's full URL is where the picture goes when it names one; one that is not Tencent's HTTPS gets no request.
func TestUploadImageToTheFullURL(t *testing.T) {
	f := newUploadFake(t)
	full := "https://upload.weixin.qq.com/c2c/upload?encrypted_query_param=FULL&filekey=FK"
	f.uploadURL = func(w http.ResponseWriter, _ map[string]any) bool {
		writeJSON(w, map[string]any{"ret": 0, "upload_full_url": " " + full + " ", "upload_param": "UP"})
		return true
	}
	c := f.client("BT", "")
	if _, err := c.UploadImage(context.Background(), "owner@im", []byte("picture")); err != nil {
		t.Fatal(err)
	}
	if len(f.cdnReqs) != 1 || f.cdnReqs[0].host != "upload.weixin.qq.com" || f.cdnReqs[0].query != "encrypted_query_param=FULL&filekey=FK" {
		t.Fatalf("CDN requests %+v", f.cdnReqs)
	}
	for _, bad := range []string{"http://upload.weixin.qq.com/c2c/upload", "https://upload.example.com/c2c/upload"} {
		f.uploadURL = func(w http.ResponseWriter, _ map[string]any) bool {
			writeJSON(w, map[string]any{"ret": 0, "upload_full_url": bad})
			return true
		}
		if _, err := c.UploadImage(context.Background(), "owner@im", []byte("picture")); CodeOf(err) != CodeProtocol || Retryable(err) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if len(f.cdnReqs) != 1 || slices.Contains(f.hosts, "upload.example.com") {
		t.Fatalf("a rejected upload URL was asked: %v", f.hosts)
	}
}

// What getuploadurl, the CDN and sendmessage answer decides whether a failure may pass when tried again: a refusal
// (a non-zero ret, an HTTP 4xx other than 429) does not; a server error, 429 or an answer missing a part may.
func TestPictureFailuresThatMayPass(t *testing.T) {
	status := func(code int) func(w http.ResponseWriter) bool {
		return func(w http.ResponseWriter) bool { w.WriteHeader(code); return true }
	}
	answer := func(v map[string]any) func(w http.ResponseWriter, _ map[string]any) bool {
		return func(w http.ResponseWriter, _ map[string]any) bool { writeJSON(w, v); return true }
	}
	httpStatus := func(code int) func(w http.ResponseWriter, _ map[string]any) bool {
		return func(w http.ResponseWriter, _ map[string]any) bool { w.WriteHeader(code); return true }
	}
	tests := []struct {
		name      string
		uploadURL func(w http.ResponseWriter, body map[string]any) bool
		cdn       func(w http.ResponseWriter) bool
		code      Code
		retry     bool
		cdnReqs   int
	}{
		{name: "getuploadurl ret", uploadURL: answer(map[string]any{"ret": -1}), code: CodeProtocol},
		{name: "getuploadurl 404", uploadURL: httpStatus(http.StatusNotFound), code: CodeHTTP},
		{name: "getuploadurl 503", uploadURL: httpStatus(http.StatusServiceUnavailable), code: CodeHTTP, retry: true},
		{name: "getuploadurl names no upload URL", uploadURL: answer(map[string]any{"ret": 0}), code: CodeBadResponse, retry: true},
		{name: "CDN 400", cdn: status(http.StatusBadRequest), code: CodeHTTP, cdnReqs: 1},
		{name: "CDN 429", cdn: status(http.StatusTooManyRequests), code: CodeHTTP, retry: true, cdnReqs: 1},
		{name: "CDN 500", cdn: status(http.StatusInternalServerError), code: CodeHTTP, retry: true, cdnReqs: 1},
		{name: "CDN answers no download reference", cdn: status(http.StatusOK), code: CodeBadResponse, retry: true, cdnReqs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUploadFake(t)
			f.uploadURL, f.cdn = tt.uploadURL, tt.cdn
			_, err := f.client("BT", "").UploadImage(context.Background(), "owner@im", []byte("picture"))
			if CodeOf(err) != tt.code || Retryable(err) != tt.retry {
				t.Fatalf("code %q, may pass %v (%v); want %q, %v", CodeOf(err), Retryable(err), err, tt.code, tt.retry)
			}
			if len(f.uploadReq) != 1 || len(f.cdnReqs) != tt.cdnReqs { // one try each: the caller tries again
				t.Fatalf("%d getuploadurl, %d CDN requests", len(f.uploadReq), len(f.cdnReqs))
			}
		})
	}

	f := newUploadFake(t)
	f.send = answer(map[string]any{"ret": -2})
	c := f.client("BT", "")
	up := Uploaded{Query: "DL", KeyHex: "00112233445566778899aabbccddeeff", CipherSize: 16}
	if _, err := c.SendImage(context.Background(), "owner@im", "CT", "cid", up); CodeOf(err) != CodeProtocol || Retryable(err) {
		t.Errorf("a picture iLink refuses: %v", err)
	}
	if _, err := c.SendTextWithID(context.Background(), "owner@im", "CT", "cid", "回答"); CodeOf(err) != CodeProtocol || Retryable(err) {
		t.Errorf("a text iLink refuses: %v", err)
	}
	f.send = httpStatus(http.StatusBadGateway)
	if _, err := c.SendImage(context.Background(), "owner@im", "CT", "cid", up); CodeOf(err) != CodeHTTP || !Retryable(err) {
		t.Errorf("a picture send's server error: %v", err)
	}
}

// An upload still going when the caller's deadline passes is a timeout, as a call's is; a cancelled one is aborted.
func TestUploadImageOutOfTime(t *testing.T) {
	for _, tt := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		code Code
	}{
		{name: "past the deadline", code: CodeTimeout, ctx: func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 100*time.Millisecond)
		}},
		{name: "cancelled", code: CodeAborted, ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(100*time.Millisecond, cancel)
			return ctx, cancel
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newUploadFake(t) // one each: the slow CDN answer outlasts the call
			f.cdn = func(w http.ResponseWriter) bool {
				time.Sleep(300 * time.Millisecond)
				return false
			}
			ctx, cancel := tt.ctx()
			defer cancel()
			if _, err := f.client("BT", "").UploadImage(ctx, "owner@im", []byte("picture")); CodeOf(err) != tt.code || !Retryable(err) {
				t.Errorf("%v, want %s", err, tt.code)
			}
		})
	}
}
