package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/dingtalk/internal/dingtalk/api"
	"github.com/nwverse/persona-extensions/extensions/dingtalk/internal/dingtalk/api/apitest"
)

func client(t *testing.T, f *apitest.Fake, secret string) *api.Client {
	t.Helper()
	c, err := api.New(api.Options{ClientID: apitest.ClientID, ClientSecret: secret, BaseURL: f.URL(), HTTP: f.Client(), Agent: "Persona/test"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRefusesPlainHTTPAndMissingCredentials(t *testing.T) {
	if _, err := api.New(api.Options{ClientID: "a", ClientSecret: "b", BaseURL: "http://example.com"}); err == nil {
		t.Error("an http open platform was taken")
	}
	if _, err := api.New(api.Options{ClientID: "a"}); err == nil {
		t.Error("a client without a secret was made")
	}
}

func TestTokenIsCachedAndWrongSecretIsUnauthorized(t *testing.T) {
	f := apitest.New(t)
	c := client(t, f, apitest.ClientSecret)
	a, err := c.Token(context.Background())
	if err != nil || a == "" {
		t.Fatalf("token: %q, %v", a, err)
	}
	b, _ := c.Token(context.Background())
	if a != b {
		t.Error("the token was not kept")
	}
	_, err = client(t, f, "wrong").Token(context.Background())
	if !api.Unauthorized(err) || api.Retryable(err) {
		t.Fatalf("a wrong secret: %v (unauthorized %v)", err, api.Unauthorized(err))
	}
}

func TestSendMarkdownAndReceivers(t *testing.T) {
	f := apitest.New(t)
	c := client(t, f, apitest.ClientSecret)
	ctx := context.Background()
	if err := c.SendMarkdown(ctx, "staff-1", "Title", "**hi**"); err != nil {
		t.Fatal(err)
	}
	if s := f.Sent(); len(s) != 1 || s[0] != (apitest.Sent{StaffID: "staff-1", Title: "Title", Text: "**hi**"}) {
		t.Fatalf("sent %+v", s)
	}
	f.Invalid["gone"] = true
	if err := c.SendMarkdown(ctx, "gone", "t", "x"); !errors.Is(err, api.ErrNotReachable) {
		t.Errorf("an unknown receiver: %v", err)
	}
	f.Held["busy"] = true
	if err := c.SendMarkdown(ctx, "busy", "t", "x"); !errors.Is(err, api.ErrFlowControlled) || !api.Retryable(err) {
		t.Errorf("a held receiver: %v", err)
	}
	f.Fail = http.StatusServiceUnavailable
	if err := c.SendMarkdown(ctx, "staff-1", "t", "x"); !api.Retryable(err) {
		t.Errorf("a busy DingTalk is not retried: %v", err)
	}
}

func TestDownloadLimitsSize(t *testing.T) {
	f := apitest.New(t)
	c := client(t, f, apitest.ClientSecret)
	f.Files["code-1"] = []byte("synthetic picture bytes")
	data, err := c.Download(context.Background(), "code-1", 1<<20)
	if err != nil || string(data) != "synthetic picture bytes" {
		t.Fatalf("download: %q, %v", data, err)
	}
	if _, err := c.Download(context.Background(), "code-1", 4); !errors.Is(err, api.ErrTooLarge) {
		t.Errorf("an oversized download: %v", err)
	}
	// DingTalk gives its OSS addresses as http: fetched over https (the fake serves TLS only)
	f.PlainDownloads = true
	if data, err := c.Download(context.Background(), "code-1", 1<<20); err != nil || string(data) != "synthetic picture bytes" {
		t.Fatalf("an http download address: %q, %v", data, err)
	}
}

func TestReplyOnlyToDingTalkWebhooks(t *testing.T) {
	f := apitest.New(t)
	c := client(t, f, apitest.ClientSecret)
	if err := c.Reply(context.Background(), f.Webhook("w1"), "hello"); err != nil {
		t.Fatal(err)
	}
	if r := f.Replies(); len(r) != 1 || r[0] != "hello" {
		t.Fatalf("replies %q", r)
	}
	for _, bad := range []string{"https://example.com/robot/send", "http://oapi.dingtalk.com/robot/send", "https://dingtalk.com.example.com/x"} {
		if err := c.Reply(context.Background(), bad, "x"); err == nil {
			t.Errorf("replied through %s", bad)
		}
	}
}

func TestStreamAnswersPingsAndCallbacksInOrder(t *testing.T) {
	f := apitest.New(t)
	c := client(t, f, apitest.ClientSecret)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 10)
	done := make(chan error, 1)
	go func() {
		done <- c.Stream(ctx, []string{api.BotTopic}, nil, func(_ context.Context, fr api.Frame) bool {
			var m api.Message
			_ = json.Unmarshal([]byte(fr.Data), &m)
			got <- m.ID
			return m.ID != "refused"
		})
	}()
	f.WaitConnected(t)
	f.Push(t, map[string]any{"specVersion": "1.0", "type": "SYSTEM", "headers": map[string]string{"topic": "ping", "messageId": "p1"}, "data": `{"opaque":"x"}`})
	f.PushMessage(t, "f1", map[string]any{"msgId": "m1", "msgtype": "text", "text": map[string]string{"content": "one"}})
	f.PushMessage(t, "f2", map[string]any{"msgId": "m2", "msgtype": "text", "text": map[string]string{"content": "two"}})
	f.PushMessage(t, "f3", map[string]any{"msgId": "refused", "msgtype": "text"})
	f.Push(t, map[string]any{"type": "EVENT", "headers": map[string]string{"topic": "other", "messageId": "e1"}})
	for _, want := range []string{"m1", "m2", "refused"} {
		select {
		case id := <-got:
			if id != want {
				t.Fatalf("got %s, want %s", id, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no callback")
		}
	}
	apitest.Eventually(t, "the acks", func() bool { return f.AckOf("e1") != nil })
	if a := f.AckOf("p1"); a == nil || a["code"] != float64(200) || a["data"] != `{"opaque":"x"}` {
		t.Errorf("the pong: %v", a)
	}
	for _, id := range []string{"f1", "f2"} {
		if a := f.AckOf(id); a == nil || a["code"] != float64(200) {
			t.Errorf("the ack of %s: %v", id, a)
		}
	}
	if a := f.AckOf("f3"); a != nil {
		t.Errorf("a refused callback was answered: %v", a)
	}
	if a := f.AckOf("e1"); a["code"] != float64(404) {
		t.Errorf("an unknown frame: %v", a)
	}
	f.Push(t, map[string]any{"type": "SYSTEM", "headers": map[string]string{"topic": "disconnect", "messageId": "d1"}})
	select {
	case err := <-done:
		if !errors.Is(err, api.ErrMoved) {
			t.Fatalf("after a disconnect: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end on disconnect")
	}
}

func TestStreamRefusesWrongCredentials(t *testing.T) {
	f := apitest.New(t)
	err := client(t, f, "wrong").Stream(context.Background(), []string{api.BotTopic}, nil, nil)
	if !api.Unauthorized(err) {
		t.Fatalf("wrong credentials: %v", err)
	}
}

func TestMessageParts(t *testing.T) {
	var m api.Message
	raw := `{"msgId":"m","msgtype":"richText","content":{"richText":[{"text":"look "},{"type":"picture","downloadCode":"p1"},{"text":"here"}]}}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	parts := m.Parts()
	if len(parts) != 3 || parts[1].Kind != api.KindPicture || parts[1].DownloadCode != "p1" || m.PlainText() != "look here" {
		t.Fatalf("rich text: %+v / %q", parts, m.PlainText())
	}
	_ = json.Unmarshal([]byte(`{"msgId":"a","msgtype":"audio","content":{"recognition":"spoken words","downloadCode":"a1"}}`), &m)
	if p := m.Parts(); len(p) != 1 || p[0].Kind != api.KindAudio || p[0].Text != "spoken words" {
		t.Fatalf("audio: %+v", p)
	}
	m = api.Message{}
	_ = json.Unmarshal([]byte(`{"msgId":"f","msgtype":"file","content":{"downloadCode":"f1","fileName":"report.pdf"}}`), &m)
	if p := m.Parts(); len(p) != 1 || p[0].FileName != "report.pdf" || p[0].DownloadCode != "f1" {
		t.Fatalf("file: %+v", p)
	}
}
