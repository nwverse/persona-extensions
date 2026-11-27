package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// nameLimitNextcloud creates files the way Nextcloud's WebDAV server does: Directory::createFile verifies the name with
// FilenameValidator::validateFilename, which throws FileNameTooLongException when isset($filename[250]), that is when
// the name is longer than 250 bytes, and the request is answered with a 400. It counts every request, so a test can tell
// that a name was refused before anything was sent.
type nameLimitNextcloud struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests int
	created  []string
}

func newNameLimitNextcloud(t *testing.T) *nameLimitNextcloud {
	t.Helper()
	f := &nameLimitNextcloud{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.mu.Unlock()
		if username, password, ok := r.BasicAuth(); !ok || username != testUsername || password != testPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		const root = "/remote.php/dav/files/" + testUsername + "/"
		if r.Method != http.MethodPut || !strings.HasPrefix(r.URL.Path, root) || r.Header.Get("If-None-Match") != "*" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		rel := strings.TrimPrefix(r.URL.Path, root)
		for _, part := range strings.Split(rel, "/") {
			if len(part) > 250 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		f.mu.Lock()
		f.created = append(f.created, rel)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *nameLimitNextcloud) app() *app {
	return &app{client: newClient(f.srv.URL, testUsername, testPassword), receipts: newWriteStore("")}
}

func noteArgs(path string) []byte {
	raw, _ := json.Marshal(map[string]string{"path": path, "text": "# Note\n\nKeep context."})
	return raw
}

func TestNoteNameOf250BytesIsCreated(t *testing.T) {
	for name, path := range map[string]string{
		"ascii": "Notes/" + strings.Repeat("a", 247) + ".md",
		"han":   "Notes/" + strings.Repeat("字", 82) + ".md",
	} {
		f := newNameLimitNextcloud(t)
		if _, err := f.app().doAction(t.Context(), "create-"+name, "nextcloud_create_note", noteArgs(path)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.created) != 1 || f.created[0] != path {
			t.Fatalf("%s: the server created %v", name, f.created)
		}
	}
}

func TestNoteNameOver250BytesIsRefusedBeforeAnyRequest(t *testing.T) {
	for name, path := range map[string]string{
		"ascii":     "Notes/" + strings.Repeat("a", 248) + ".md",
		"han":       "Notes/" + strings.Repeat("字", 83) + ".md",
		"directory": "Notes/" + strings.Repeat("d", 251) + "/note.md",
	} {
		f := newNameLimitNextcloud(t)
		if _, err := describeAction("nextcloud_create_note", noteArgs(path)); err == nil {
			t.Fatalf("%s: describeAction accepted a name of more than 250 bytes", name)
		}
		if _, err := f.app().doAction(t.Context(), "create-"+name, "nextcloud_create_note", noteArgs(path)); err == nil {
			t.Fatalf("%s: doAction accepted a name of more than 250 bytes", name)
		}
		if f.requests != 0 {
			t.Fatalf("%s: %d requests were sent for a name Nextcloud refuses", name, f.requests)
		}
	}
}
