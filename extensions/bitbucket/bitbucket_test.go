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

const testToken = "synthetic-bitbucket-token"

type fakeBitbucket struct {
	srv        *httptest.Server
	mu         sync.Mutex
	paths      []string
	approves   int
	unapproves int
}

func newFakeBitbucket(t *testing.T) *fakeBitbucket {
	f := &fakeBitbucket{}
	var sample pullRequest
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/projects/ENG/repos/reader/pull-requests":
			sample = pullRequest{ID: 42, Title: "Preserve reading context", Description: "Keep notes with their source", State: "OPEN", Open: true}
			sample.Author.User = user{Name: "author", DisplayName: "Example Author"}
			sample.FromRef.DisplayID = "feature/reading"
			sample.FromRef.Repository.Slug = "reader"
			sample.FromRef.Repository.Name = "Reader"
			sample.FromRef.Repository.Project.Key = "ENG"
			sample.FromRef.Repository.Project.Name = "Engineering"
			sample.ToRef.DisplayID = "main"
			sample.Reviewers = []struct {
				User     user   `json:"user"`
				Approved bool   `json:"approved"`
				Status   string `json:"status"`
			}{{User: user{Name: "reviewer", DisplayName: "Reviewer"}, Approved: false, Status: "UNAPPROVED"}}
			sample.Links.Self = []struct {
				Href string `json:"href"`
			}{{Href: "https://bitbucket.example.com/projects/ENG/repos/reader/pull-requests/42"}}
			_ = json.NewEncoder(w).Encode(prPage{Values: []pullRequest{sample}, Size: 1, Limit: 50, Start: 0, IsLastPage: true})
		case "/projects/ENG/repos/reader/pull-requests/42/activities":
			fmt.Fprint(w, `{"values":[{"id":1,"action":"COMMENTED","comment":{"text":"Please preserve the context","author":{"name":"reviewer","displayName":"Reviewer"}},"user":{"name":"reviewer","displayName":"Reviewer"},"createdDate":1762710000000}]}`)
		case "/projects/ENG/repos/reader/pull-requests/42":
			_ = json.NewEncoder(w).Encode(sample)
		case "/projects/ENG/repos/reader/pull-requests/42/approve":
			f.mu.Lock()
			if r.Method == http.MethodPost {
				f.approves++
			} else if r.Method == http.MethodDelete {
				f.unapproves++
			}
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBitbucket) app() *app {
	return &app{client: newClient(f.srv.URL, testToken), receipts: newWriteStore("")}
}

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeBitbucket(t).app())))
}

func TestManifest(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "bitbucket" || len(m.Secrets) != 1 || m.Secrets[0].Name != "access_token" {
		t.Fatalf("%+v", m)
	}
	if len(m.Contributes.Connectors[0].ConfirmAlways) != 2 {
		t.Fatal("approve and unapprove must be confirmed")
	}
	if !slices.Equal(m.Permissions.Network, []string{}) {
		t.Fatal(m.Permissions.Network)
	}
}

func TestInstanceURLValidation(t *testing.T) {
	for raw, want := range map[string]string{
		"https://bitbucket.example.com": "https://bitbucket.example.com/rest/api/1.0",
		"http://127.0.0.1:7990":         "http://127.0.0.1:7990/rest/api/1.0",
	} {
		got, err := instanceURL(raw)
		if err != nil || got != want {
			t.Fatalf("%q: %v %q", raw, err, got)
		}
	}
	for _, raw := range []string{"http://bitbucket.example.com", "https://me@example.com", "not-a-url"} {
		if _, err := instanceURL(raw); err == nil {
			t.Fatal("accepted " + raw)
		}
	}
}

func TestPRReadAndReviewState(t *testing.T) {
	f := newFakeBitbucket(t)
	text, err := f.app().callTool(t.Context(), "bitbucket_prs", json.RawMessage(`{"project":"ENG","repo":"reader","start":0}`))
	if err != nil || !strings.Contains(text, "PR #42") || !strings.Contains(text, "Preserve reading context") {
		t.Fatalf("%v %s", err, text)
	}
	text, err = f.app().callTool(t.Context(), "bitbucket_pr", json.RawMessage(`{"project":"ENG","repo":"reader","id":42}`))
	if err != nil || !strings.Contains(text, "Please preserve the context") || !strings.Contains(text, "Reviewer") {
		t.Fatalf("%v %s", err, text)
	}
}

func TestApproveAndUnapproveReplay(t *testing.T) {
	f := newFakeBitbucket(t)
	a := f.app()
	for range 2 {
		if _, err := a.doAction(t.Context(), "approve-1", "bitbucket_approve", []byte(`{"project":"ENG","repo":"reader","id":42}`)); err != nil {
			t.Fatal(err)
		}
	}
	if f.approves != 1 {
		t.Fatal(f.approves)
	}
	for range 2 {
		if _, err := a.doAction(t.Context(), "unapprove-1", "bitbucket_unapprove", []byte(`{"project":"ENG","repo":"reader","id":42}`)); err != nil {
			t.Fatal(err)
		}
	}
	if f.unapproves != 1 {
		t.Fatal(f.unapproves)
	}
}

func TestPersistentReplayAndUnknownOutcome(t *testing.T) {
	f := newFakeBitbucket(t)
	a := f.app()
	store, err := openWriteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.receipts = store
	args := []byte(`{"project":"ENG","repo":"reader","id":42}`)
	if _, err := a.doAction(t.Context(), "persist-1", "bitbucket_approve", args); err != nil {
		t.Fatal(err)
	}
	a.receipts, err = openWriteStore(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "persist-1", "bitbucket_approve", args); err != nil {
		t.Fatal(err)
	}
	if f.approves != 1 {
		t.Fatal("restart repeated approval")
	}
	key, _ := json.Marshal(map[string]any{"action": "bitbucket_approve", "project": "ENG", "repo": "reader", "id": 42})
	raw, _ := json.Marshal(map[string]writeReceipt{"persist-1": {Key: string(key), Status: "pending", StartedAt: time.Now()}})
	if err := os.WriteFile(store.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.receipts, _ = openWriteStore(filepath.Dir(store.path))
	if _, err := a.doAction(t.Context(), "persist-1", "bitbucket_approve", args); err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatal("unknown approval was retried")
	}
	if f.approves != 1 {
		t.Fatal("unknown approval caused another write")
	}
}

func TestLocalizedViewsAndDetails(t *testing.T) {
	f := newFakeBitbucket(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, ui := range []int{1, 3, 6} {
			r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: ui, State: ext.EncodeState(pageState{Project: "ENG", Repo: "reader"})}}
			v, err := a.render(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			if err := wire.CheckView(v); err != nil {
				t.Fatalf("%s %d: %v", locale, ui, err)
			}
		}
	}
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6}}
	v, err := a.page(t.Context(), r, pageState{Mode: "detail", Project: "ENG", Repo: "reader", PRID: 42})
	if err != nil {
		t.Fatalf("page detail: %v; paths=%v", err, f.paths)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}

func TestPaginationAndApprovalConflict(t *testing.T) {
	f := newFakeBitbucket(t)
	a := f.app()
	if _, err := a.callTool(t.Context(), "bitbucket_prs", json.RawMessage(`{"project":"ENG","repo":"reader","start":50}`)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	found := false
	for _, path := range f.paths {
		if strings.Contains(path, "start=50") {
			found = true
		}
	}
	f.mu.Unlock()
	if !found {
		t.Fatal("start was not forwarded")
	}
	if _, err := a.doAction(t.Context(), "approve-1", "bitbucket_approve", []byte(`{"project":"ENG","repo":"reader","id":42}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "approve-1", "bitbucket_approve", []byte(`{"project":"OTHER","repo":"reader","id":42}`)); err == nil {
		t.Fatal("receipt accepted a different project")
	}
}

func TestDetailBoundsAndNoConfig(t *testing.T) {
	item := pullRequest{ID: 42, Title: "Review", Description: strings.Repeat("context ", 3000)}
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{PRID: 42, Mode: "detail"}, item, nil)
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
}
