package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// countingPinboard saves every bookmark it is sent and counts the requests, so a test can tell that a tag was refused
// before anything was sent. Pinboard documents a tag as "up to 255 characters"; what it does with a longer one is not
// documented, so the fake does not pretend to know.
type countingPinboard struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests int
	tags     []string
}

func newCountingPinboard(t *testing.T) *countingPinboard {
	t.Helper()
	f := &countingPinboard{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.tags = append(f.tags, r.URL.Query().Get("tags"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"result_code": "done"})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *countingPinboard) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func addWithTags(tags string) []byte {
	raw, _ := json.Marshal(map[string]string{"url": "https://articles.example.com/context", "title": "Reading context", "tags": tags})
	return raw
}

func TestTagOver255CharactersIsRefusedBeforeAnyRequest(t *testing.T) {
	for name, tags := range map[string]string{
		"ascii":  "reading " + strings.Repeat("a", 256),
		"han":    strings.Repeat("字", 256),
		"single": strings.Repeat("t", 300),
	} {
		f := newCountingPinboard(t)
		args := addWithTags(tags)
		if _, err := describeAction("pinboard_add", args); err == nil {
			t.Fatalf("%s: describeAction accepted a tag of more than 255 characters", name)
		}
		if _, err := f.app().doAction(t.Context(), "tag-"+name, "pinboard_add", args); err == nil {
			t.Fatalf("%s: doAction accepted a tag of more than 255 characters", name)
		}
		if f.requests != 0 {
			t.Fatalf("%s: %d requests were sent for a tag Pinboard does not document as valid", name, f.requests)
		}
	}
}

func TestTagOf255CharactersIsSent(t *testing.T) {
	for name, tags := range map[string]string{
		"ascii": "reading " + strings.Repeat("a", 255),
		"han":   strings.Repeat("字", 255),
	} {
		f := newCountingPinboard(t)
		if _, err := f.app().doAction(t.Context(), "tag-"+name, "pinboard_add", addWithTags(tags)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if f.requests != 1 || f.tags[0] != tags {
			t.Fatalf("%s: %d requests with tags %q", name, f.requests, f.tags)
		}
	}
}
