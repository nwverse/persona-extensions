package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// textLimitJira refuses a description or comment longer than 32,767 characters the way Jira does ("The entered text is
// too long. It exceeds the allowed limit of 32,767 characters.") and counts every request, so a test can tell that
// a text was refused before anything was sent.
type textLimitJira struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests int
	created  int
	comments int
}

func newTextLimitJira(t *testing.T) *textLimitJira {
	t.Helper()
	f := &textLimitJira{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		tooLong := func(text string) bool {
			if utf8.RuneCountInString(text) <= 32767 {
				return false
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"errorMessages":["The entered text is too long. It exceeds the allowed limit of 32,767 characters."],"errors":{}}`)
			return true
		}
		switch r.URL.Path {
		case "/issuetype":
			fmt.Fprint(w, `[{"id":"task","name":"Task","subtask":false}]`)
		case "/issue":
			var in struct {
				Fields map[string]any `json:"fields"`
			}
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &in)
			if in.Fields["description"] != nil {
				if checkADF(in.Fields["description"]) != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if tooLong(adfText(in.Fields["description"])) {
					return
				}
			}
			f.mu.Lock()
			f.created++
			f.mu.Unlock()
			fmt.Fprint(w, `{"id":"10002","key":"ENG-124","self":"https://jira.example.com/browse/ENG-124"}`)
		case "/issue/ENG-123/comment":
			var in struct {
				Body any `json:"body"`
			}
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &in)
			if checkADF(in.Body) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if tooLong(adfText(in.Body)) {
				return
			}
			f.mu.Lock()
			f.comments++
			f.mu.Unlock()
			fmt.Fprint(w, `{"id":"comment-new"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *textLimitJira) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func textActions(text string) map[string][]byte {
	create, _ := json.Marshal(map[string]string{"project_id": "project-eng", "issuetype_id": "task", "summary": "New issue", "description": text})
	comment, _ := json.Marshal(map[string]string{"key": "ENG-123", "body": text})
	return map[string][]byte{"jira_create": create, "jira_comment": comment}
}

func TestTextOver32767CharactersIsRefusedBeforeAnyRequest(t *testing.T) {
	for name, text := range map[string]string{
		"ascii": strings.Repeat("a", 32768),
		"han":   strings.Repeat("字", 32768),
	} {
		for action, args := range textActions(text) {
			f := newTextLimitJira(t)
			if _, err := describeAction(action, args); err == nil {
				t.Fatalf("%s %s: describeAction accepted a text of more than 32,767 characters", action, name)
			}
			if _, err := f.app().doAction(t.Context(), "long-"+action+name, action, args); err == nil {
				t.Fatalf("%s %s: doAction accepted a text of more than 32,767 characters", action, name)
			}
			if f.requests != 0 {
				t.Fatalf("%s %s: %d requests were sent for a text Jira refuses", action, name, f.requests)
			}
		}
	}
}

func TestTextOf32767CharactersIsSent(t *testing.T) {
	for name, text := range map[string]string{
		"ascii": strings.Repeat("a", 32767),
		"han":   strings.Repeat("字", 32767),
	} {
		f := newTextLimitJira(t)
		for action, args := range textActions(text) {
			if _, err := f.app().doAction(t.Context(), "ok-"+action+name, action, args); err != nil {
				t.Fatalf("%s %s: %v", action, name, err)
			}
		}
		if f.created != 1 || f.comments != 1 {
			t.Fatalf("%s: created %d issues and %d comments", name, f.created, f.comments)
		}
	}
}
