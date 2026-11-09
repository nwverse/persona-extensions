package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const testToken = "synthetic-gitlab-token"

type fakeGitLab struct {
	srv        *httptest.Server
	mu         sync.Mutex
	paths      []string
	approves   int
	unapproves int
	comments   []string
}

func sampleMR() mergeRequest {
	item := mergeRequest{ID: 920, IID: 42, ProjectID: 17, Title: "Preserve reading context", Description: "Keep notes with their source", State: "opened", SourceBranch: "feature/reading", TargetBranch: "main", WebURL: "https://gitlab.example.com/group/repo/-/merge_requests/42", UserNotesCount: 1}
	item.Author = user{ID: 1, Username: "author", Name: "Example Author"}
	item.Reviewers = []user{{ID: 2, Username: "reviewer", Name: "Example Reviewer"}}
	item.References.Full = "group/repo!42"
	return item
}

func newFakeGitLab(t *testing.T) *fakeGitLab {
	f := &fakeGitLab{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/merge_requests":
			if r.URL.Query().Get("state") != "opened" || r.URL.Query().Get("scope") == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.URL.Query().Get("page") == "2" {
				w.Header().Set("X-Next-Page", "")
				_ = json.NewEncoder(w).Encode([]mergeRequest{})
				return
			}
			w.Header().Set("X-Next-Page", "2")
			_ = json.NewEncoder(w).Encode([]mergeRequest{sampleMR()})
		case "/projects/17/merge_requests/42", "/projects/group%2Frepo%2Freader/merge_requests/42":
			_ = json.NewEncoder(w).Encode(sampleMR())
		case "/projects/17/merge_requests/42/notes", "/projects/group%2Frepo%2Freader/merge_requests/42/notes":
			if r.Method == http.MethodPost {
				var in struct {
					Body string `json:"body"`
				}
				if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Body == "" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				f.mu.Lock()
				f.comments = append(f.comments, in.Body)
				f.mu.Unlock()
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(note{ID: 5, Body: in.Body})
				return
			}
			_ = json.NewEncoder(w).Encode([]note{{ID: 1, Body: "Please preserve the context", Author: user{Username: "reviewer", Name: "Example Reviewer"}}, {ID: 2, Body: "changed a label", System: true}})
		case "/projects/17/merge_requests/42/approve":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			f.mu.Lock()
			f.approves++
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"approved": true})
		case "/projects/17/merge_requests/42/unapprove":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			f.mu.Lock()
			f.unapproves++
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"approved": false})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitLab) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeGitLab(t).app())))
}

func TestManifest(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "gitlab" || len(m.Secrets) != 1 || m.Secrets[0].Name != "access_token" {
		t.Fatalf("%+v", m)
	}
	if len(m.Contributes.Connectors[0].ConfirmAlways) != 3 {
		t.Fatal("approve, unapprove and comment must be confirmed")
	}
	if !slices.Equal(m.Permissions.Network, []string{}) {
		t.Fatal(m.Permissions.Network)
	}
}

func TestInstanceURLValidation(t *testing.T) {
	for raw, want := range map[string]string{
		"https://gitlab.example.com":        "https://gitlab.example.com/api/v4",
		"https://gitlab.example.com/api/v4": "https://gitlab.example.com/api/v4",
		"http://127.0.0.1:8080":             "http://127.0.0.1:8080/api/v4",
	} {
		got, err := instanceURL(raw)
		if err != nil || got != want {
			t.Fatalf("%q: %v %q", raw, err, got)
		}
	}
	for _, raw := range []string{"http://gitlab.example.com", "https://me@example.com", "not-a-url"} {
		if _, err := instanceURL(raw); err == nil {
			t.Fatal("accepted " + raw)
		}
	}
}

func TestMRReadSearchPaginationAndProjectPath(t *testing.T) {
	f := newFakeGitLab(t)
	text, err := f.app().callTool(t.Context(), "gitlab_merge_requests", json.RawMessage(`{"scope":"reviews_for_me","search":"reading","page":1}`))
	if err != nil || !strings.Contains(text, "MR !42") || !strings.Contains(text, "Next page: 2") || !strings.Contains(text, "project ID: 17") {
		t.Fatalf("%v %s", err, text)
	}
	if _, err := f.app().callTool(t.Context(), "gitlab_merge_requests", json.RawMessage(`{"page":2}`)); err != nil {
		t.Fatal(err)
	}
	text, err = f.app().callTool(t.Context(), "gitlab_merge_request", json.RawMessage(`{"project":"group/repo/reader","iid":42}`))
	if err != nil || !strings.Contains(text, "Please preserve the context") || strings.Contains(text, "changed a label") {
		t.Fatalf("%v %s; paths=%v", err, text, f.paths)
	}
	f.mu.Lock()
	paths := strings.Join(f.paths, "\n")
	f.mu.Unlock()
	for _, want := range []string{"scope=reviews_for_me", "in=title", "search=reading", "page=2", "group%2Frepo%2Freader"} {
		if !strings.Contains(paths, want) {
			t.Fatalf("missing %q in %s", want, paths)
		}
	}
	if _, err := f.app().callTool(t.Context(), "gitlab_merge_requests", json.RawMessage(`{"scope":"all"}`)); err == nil {
		t.Fatal("accepted an unrestricted scope")
	}
}

func TestApproveUnapproveCommentReplay(t *testing.T) {
	f := newFakeGitLab(t)
	a := f.app()
	for range 2 {
		if _, err := a.doAction(t.Context(), "approve-1", "gitlab_approve", []byte(`{"project":"17","iid":42}`)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := a.doAction(t.Context(), "unapprove-1", "gitlab_unapprove", []byte(`{"project":"17","iid":42}`)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if _, err := a.doAction(t.Context(), "comment-1", "gitlab_comment", []byte(`{"project":"17","iid":42,"body":"Looks good with the added test."}`)); err != nil {
			t.Fatal(err)
		}
	}
	if f.approves != 1 || f.unapproves != 1 || len(f.comments) != 1 {
		t.Fatalf("%d %d %v", f.approves, f.unapproves, f.comments)
	}
	for _, name := range []string{"gitlab_approve", "gitlab_unapprove", "gitlab_comment"} {
		args := `{"project":"17","iid":42}`
		if name == "gitlab_comment" {
			args = `{"project":"17","iid":42,"body":"A concise review."}`
		}
		description, err := describeAction(name, []byte(args))
		if err != nil || !strings.Contains(fmt.Sprint(description["summary"]), "!42") {
			t.Fatalf("%s: %v %v", name, err, description)
		}
	}
}

func TestPersistentReplayAndUnknownOutcome(t *testing.T) {
	f := newFakeGitLab(t)
	a := f.app()
	store, err := openWriteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.receipts = store
	args := []byte(`{"project":"17","iid":42}`)
	if _, err := a.doAction(t.Context(), "persist-1", "gitlab_approve", args); err != nil {
		t.Fatal(err)
	}
	a.receipts, err = openWriteStore(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "persist-1", "gitlab_approve", args); err != nil {
		t.Fatal(err)
	}
	if f.approves != 1 {
		t.Fatal("restart repeated approval")
	}
	key, _ := json.Marshal(struct {
		Action  string `json:"action"`
		Project string `json:"project"`
		IID     int64  `json:"iid"`
	}{"gitlab_approve", "17", 42})
	raw, _ := json.Marshal(map[string]writeReceipt{"persist-1": {Key: string(key), Status: "pending", StartedAt: time.Now()}})
	if err := os.WriteFile(store.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.receipts, _ = openWriteStore(filepath.Dir(store.path))
	if _, err := a.doAction(t.Context(), "persist-1", "gitlab_approve", args); err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatal("unknown approval was retried")
	}
	if f.approves != 1 {
		t.Fatal("unknown approval caused another write")
	}
	if _, err := a.doAction(t.Context(), "persist-1", "gitlab_approve", []byte(`{"project":"18","iid":42}`)); err == nil {
		t.Fatal("receipt accepted different arguments")
	}
}

func TestLocalizedViewsAndCommentFlow(t *testing.T) {
	f := newFakeGitLab(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, ui := range []int{1, 3, 6} {
			for _, mode := range []string{"list", "detail", "comment", "review-comment"} {
				r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: ui, Values: map[string]string{}}}
				st := pageState{Scope: "assigned_to_me", Mode: mode, Project: "17", IID: 42, DraftID: "draft-1"}
				if mode == "review-comment" {
					st.Body = "Preserve the source context."
				}
				v, err := a.page(t.Context(), r, st)
				if err != nil {
					t.Fatalf("%s %d %s: %v", locale, ui, mode, err)
				}
				if err := wire.CheckView(v); err != nil {
					t.Fatalf("%s %d %s: %v", locale, ui, mode, err)
				}
			}
		}
	}
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, Values: map[string]string{"body": "保留来源上下文。"}}}
	result, err := a.action(t.Context(), withAction(r, "review-comment", pageState{Mode: "comment", Project: "17", IID: 42, DraftID: "ui-1"}))
	if err != nil || result.View == nil || wire.CheckView(result.View) != nil {
		t.Fatalf("review: %v %+v", err, result)
	}
	result, err = a.action(t.Context(), withAction(r, "confirm-comment", pageState{Mode: "review-comment", Project: "17", IID: 42, DraftID: "ui-1", Body: "保留来源上下文。"}))
	if err != nil || !result.Pop || len(f.comments) != 1 {
		t.Fatalf("confirm: %v %+v %v", err, result, f.comments)
	}
}

func withAction(r *ext.UIRequest, action string, st pageState) *ext.UIRequest {
	r.Action = action
	r.State = ext.EncodeState(st)
	return r
}

func TestBoundsAndNoConfig(t *testing.T) {
	item := sampleMR()
	item.Description = strings.Repeat("context ", 3000)
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{IID: 42, Project: "17", Mode: "detail"}, item, []note{{Body: strings.Repeat("review ", 3000)}})
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	a := newApp()
	if a.status()["state"] != "not_configured" {
		t.Fatal(a.status())
	}
	v, err := a.render(t.Context(), &ext.UIRequest{})
	if err != nil || !strings.Contains(v.Sections[0].Items[0].Title, "instance URL") {
		t.Fatalf("%v %+v", err, v)
	}
	if _, err := failableComment("bad\x00comment"); err == nil {
		t.Fatal("accepted a control character")
	}
	item.WebURL = "http://gitlab.example.com/invalid-remote-link"
	v = detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{IID: 42, Project: "17", Mode: "detail"}, item, nil)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	if len(v.Sections[0].Items) != 6 {
		t.Fatal("unsafe link should be omitted without dropping the MR details")
	}
	item.WebURL = ""
	item.Reviewers = make([]user, 110)
	notes := make([]note, 100)
	for i := range notes {
		notes[i].Body = strings.Repeat("Detailed review context. ", 1000)
	}
	v = detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{IID: 42, Project: "17", Mode: "detail"}, item, notes)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}

func failableComment(body string) (bool, error) { return false, validComment(body) }

func TestUntrustedContentAndMissingIdentity(t *testing.T) {
	f := newFakeGitLab(t)
	f.srv.Close()
	_, err := f.app().callTool(t.Context(), "gitlab_merge_request", json.RawMessage(`{"project":"17","iid":42}`))
	if err == nil || !strings.Contains(err.Error(), "could not be reached") {
		t.Fatal(err)
	}
	if !strings.Contains(formatMR(mergeRequest{IID: 42, ProjectID: 17}, false), "MR !42") {
		t.Fatal("missing identity should not discard readable MR metadata")
	}
	for _, body := range []string{"", "  ", strings.Repeat("x", 10001), "bad\x00comment", "bad\rcomment"} {
		if err := validComment(body); err == nil {
			t.Fatalf("accepted invalid comment %q", body)
		}
	}
}

func TestActionRequiresProjectAndID(t *testing.T) {
	a := newFakeGitLab(t).app()
	for _, raw := range []string{`{"project":"","iid":42}`, `{"project":"17","iid":0}`, `{"project":"17","iid":42,"body":"unapproved text"}`} {
		if _, err := a.doAction(t.Context(), "invalid-1", "gitlab_approve", []byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"project":"17","iid":42,"body":" "}`, `{"project":"17","iid":42,"body":"bad\rcomment"}`} {
		if _, err := describeAction("gitlab_comment", []byte(raw)); err == nil {
			t.Fatalf("accepted comment %s", raw)
		}
	}
}
