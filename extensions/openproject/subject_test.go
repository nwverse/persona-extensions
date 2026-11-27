package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// subjectOpenProject creates work packages the way OpenProject's work package contract validates them
// (app/contracts/work_packages/base_contract.rb: validates :subject, length: { maximum: 255 }, counted in characters):
// a longer subject is a 422. It counts every request, so a test can tell that a subject was refused before anything was
// sent.
type subjectOpenProject struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests int
	subjects []string
}

func newSubjectOpenProject(t *testing.T) *subjectOpenProject {
	t.Helper()
	f := &subjectOpenProject{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/work_packages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var in struct {
			Subject string `json:"subject"`
		}
		w.Header().Set("Content-Type", "application/hal+json")
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Subject == "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		if utf8.RuneCountInString(in.Subject) > 255 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"_type": "Error", "message": "Subject is too long (maximum is 255 characters)."})
			return
		}
		f.mu.Lock()
		f.subjects = append(f.subjects, in.Subject)
		f.mu.Unlock()
		item := sampleWorkPackage()
		item.ID, item.Subject = 43, in.Subject
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(item)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *subjectOpenProject) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func subjectArgs(subject string) []byte {
	raw, _ := json.Marshal(map[string]any{"project_href": "/api/v3/projects/7", "type_href": "/api/v3/types/1", "subject": subject})
	return raw
}

func TestSubjectOf255CharactersIsCreated(t *testing.T) {
	for name, subject := range map[string]string{"ascii": strings.Repeat("a", 255), "han": strings.Repeat("字", 255)} {
		f := newSubjectOpenProject(t)
		if _, err := f.app().doAction(t.Context(), "create-"+name, "openproject_create_work_package", subjectArgs(subject)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(f.subjects) != 1 || f.subjects[0] != subject {
			t.Fatalf("%s: the server received %d subjects", name, len(f.subjects))
		}
	}
}

func TestSubjectOver255CharactersIsRefusedBeforeAnyRequest(t *testing.T) {
	for name, subject := range map[string]string{"ascii": strings.Repeat("a", 256), "han": strings.Repeat("字", 256)} {
		f := newSubjectOpenProject(t)
		a := f.app()
		if _, err := describeAction("openproject_create_work_package", subjectArgs(subject)); err == nil {
			t.Fatalf("%s: describeAction accepted a subject of 256 characters", name)
		}
		if _, err := a.doAction(t.Context(), "create-"+name, "openproject_create_work_package", subjectArgs(subject)); err == nil {
			t.Fatalf("%s: doAction accepted a subject of 256 characters", name)
		}
		r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", ProjectHref: "/api/v3/projects/7", TypeHref: "/api/v3/types/1", DraftID: "draft-1"}), Values: map[string]string{"subject": subject}}}
		if _, err := a.action(t.Context(), r); err == nil {
			t.Fatalf("%s: the review step accepted a subject of 256 characters", name)
		}
		if f.requests != 0 {
			t.Fatalf("%s: %d requests were sent for a subject OpenProject refuses", name, f.requests)
		}
	}
}

// The reviewed draft travels in the page state, and a Han character is three bytes there.
func TestWebFlowCreatesA255CharacterSubject(t *testing.T) {
	f := newSubjectOpenProject(t)
	a := f.app()
	subject := strings.Repeat("字", 255)
	review, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "review-create", State: ext.EncodeState(pageState{Mode: "create", ProjectHref: "/api/v3/projects/7", ProjectName: "Reading", TypeHref: "/api/v3/types/1", TypeName: "Task", DraftID: "draft-1"}), Values: map[string]string{"subject": subject, "description": "保留来源上下文。"}}})
	if err != nil || review.View == nil || wire.CheckView(review.View) != nil {
		t.Fatalf("review: %v %+v", err, review)
	}
	confirm, err := a.action(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Action: "confirm-create", State: review.View.State}})
	if err != nil || !confirm.Pop {
		t.Fatalf("confirm: %v %+v", err, confirm)
	}
	if len(f.subjects) != 1 || f.subjects[0] != subject {
		t.Fatalf("the server received %d subjects", len(f.subjects))
	}
}
