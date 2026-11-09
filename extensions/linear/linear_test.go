package main

import (
	"encoding/json"
	"fmt"
	"io"
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

const testKey = "synthetic-linear-key"

type fakeLinear struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []graphRequest
}

func newFakeLinear(t *testing.T) *fakeLinear {
	f := &fakeLinear{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req graphRequest
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
		f.mu.Lock()
		f.requests = append(f.requests, req)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "query Assigned"):
			fmt.Fprint(w, `{"data":{"viewer":{"assignedIssues":{"nodes":[{"id":"issue-uuid-1","identifier":"ENG-123","title":"Review offline reading","priority":2,"url":"https://linear.app/example/issue/ENG-123","dueDate":"2026-11-15","updatedAt":"2026-11-09T10:00:00Z","state":{"id":"state-started","name":"In Progress","type":"started"},"team":{"id":"team-eng","key":"ENG","name":"Engineering"},"assignee":{"id":"user-1","name":"Example User","displayName":"Example User"},"project":{"id":"project-reader","name":"Reader"}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-next"}}}}}`)
		case strings.Contains(req.Query, "query Issue"):
			fmt.Fprint(w, `{"data":{"issue":{"id":"issue-uuid-1","identifier":"ENG-123","title":"Review offline reading","description":"Keep article context and notes together.","priority":2,"url":"https://linear.app/example/issue/ENG-123","dueDate":"2026-11-15","createdAt":"2026-11-01T10:00:00Z","updatedAt":"2026-11-09T10:00:00Z","state":{"id":"state-started","name":"In Progress","type":"started"},"team":{"id":"team-eng","key":"ENG","name":"Engineering"},"assignee":{"id":"user-1","displayName":"Example User"},"comments":{"nodes":[{"id":"comment-1","body":"Please preserve the reading context.","createdAt":"2026-11-09T11:00:00Z","user":{"id":"user-2","displayName":"Reviewer"}}]}}}}`)
		case strings.Contains(req.Query, "query Teams"):
			fmt.Fprint(w, `{"data":{"teams":{"nodes":[{"id":"team-eng","key":"ENG","name":"Engineering","states":{"nodes":[{"id":"state-started","name":"In Progress","type":"started","position":1},{"id":"state-done","name":"Done","type":"completed","position":2}]}},{"id":"team-design","key":"DES","name":"Design","states":{"nodes":[{"id":"design-done","name":"Done","type":"completed","position":2}]}}]}}}`)
		case strings.Contains(req.Query, "mutation Create"):
			fmt.Fprint(w, `{"data":{"issueCreate":{"success":true,"issue":{"id":"issue-new","identifier":"ENG-124","title":"New issue","url":"https://linear.app/example/issue/ENG-124"}}}}`)
		case strings.Contains(req.Query, "mutation Update"):
			fmt.Fprint(w, `{"data":{"issueUpdate":{"success":true}}}`)
		case strings.Contains(req.Query, "mutation Comment"):
			fmt.Fprint(w, `{"data":{"commentCreate":{"success":true}}}`)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": "unexpected operation"}}})
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLinear) app() *app {
	return &app{client: newClient(f.srv.URL, testKey), receipts: newWriteStore("")}
}

func TestConformance(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, program(newFakeLinear(t).app())))
}

func TestManifest(t *testing.T) {
	if err := wire.ValidateJSON("Manifest", manifest); err != nil {
		t.Fatal(err)
	}
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != "linear" || len(m.Secrets) != 1 || m.Secrets[0].Name != "api_key" {
		t.Fatalf("%+v", m)
	}
	if !slices.Equal(m.Permissions.Network, []string{"api.linear.app"}) {
		t.Fatal(m.Permissions.Network)
	}
	if len(m.Contributes.Connectors[0].ConfirmAlways) != 3 {
		t.Fatal("create, update and comment must be confirmed")
	}
}

func TestAssignedUsesFixedQueryAndCursor(t *testing.T) {
	f := newFakeLinear(t)
	text, err := f.app().callTool(t.Context(), "linear_assigned", json.RawMessage(`{"team":"ENG","query":"offline","cursor":"cursor-old"}`))
	if err != nil || !strings.Contains(text, "ENG-123") || !strings.Contains(text, "In Progress") || !strings.Contains(text, "cursor-next") {
		t.Fatalf("%v %s", err, text)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 || f.requests[0].Variables["after"] != "cursor-old" || strings.Contains(f.requests[0].Query, "mutation") {
		t.Fatal(f.requests)
	}
}

func TestIssueDetailsAndComments(t *testing.T) {
	f := newFakeLinear(t)
	text, err := f.app().callTool(t.Context(), "linear_issue", json.RawMessage(`{"id":"ENG-123"}`))
	if err != nil || !strings.Contains(text, "Keep article context") || !strings.Contains(text, "Please preserve") {
		t.Fatalf("%v %s", err, text)
	}
}

func TestTeamWorkflowIsScoped(t *testing.T) {
	f := newFakeLinear(t)
	a := f.app()
	teams, err := a.teamsRead(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := validState(teams, "team-eng", "state-done"); err != nil {
		t.Fatal(err)
	}
	if err := validState(teams, "team-eng", "design-done"); err == nil {
		t.Fatal("another team's status was accepted")
	}
}

func TestCreateTypedFieldsAndPersistentReplay(t *testing.T) {
	f := newFakeLinear(t)
	a := f.app()
	store, err := openWriteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.receipts = store
	args := []byte(`{"team_id":"team-eng","title":"New issue","description":"Context","priority":2,"state_id":"state-started"}`)
	for range 2 {
		out, err := a.doAction(t.Context(), "create-1", "linear_create", args)
		if err != nil || !strings.Contains(out["text"].(string), "ENG-124") {
			t.Fatalf("%v %+v", err, out)
		}
	}
	a.receipts, err = openWriteStore(filepath.Dir(store.path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.doAction(t.Context(), "create-1", "linear_create", args); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	creates := 0
	for _, req := range f.requests {
		if strings.Contains(req.Query, "mutation Create") {
			creates++
		}
	}
	if creates != 1 {
		t.Fatal("replay created another issue")
	}
}

func TestStatusUpdateChecksIssueTeam(t *testing.T) {
	f := newFakeLinear(t)
	a := f.app()
	if _, err := a.doAction(t.Context(), "update-1", "linear_update", []byte(`{"id":"issue-uuid-1","state_id":"design-done"}`)); err == nil {
		t.Fatal("cross-team status accepted")
	}
	if _, err := a.doAction(t.Context(), "update-2", "linear_update", []byte(`{"id":"issue-uuid-1","state_id":"state-done"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestCommentRequiresExplicitBodyAndDedupes(t *testing.T) {
	f := newFakeLinear(t)
	a := f.app()
	if _, err := a.doAction(t.Context(), "comment-1", "linear_comment", []byte(`{"id":"issue-uuid-1","body":""}`)); err == nil {
		t.Fatal("empty comment accepted")
	}
	for range 2 {
		if _, err := a.doAction(t.Context(), "comment-2", "linear_comment", []byte(`{"id":"issue-uuid-1","body":"Please keep context"}`)); err != nil {
			t.Fatal(err)
		}
	}
	comments := 0
	for _, req := range f.requests {
		if strings.Contains(req.Query, "mutation Comment") {
			comments++
		}
	}
	if comments != 1 {
		t.Fatal("comment replay wrote twice")
	}
}

func TestGraphQLErrorIsFailure(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"synthetic GraphQL failure"}]}`))
	}))
	defer s.Close()
	if err := newClient(s.URL, testKey).call(t.Context(), `{ viewer { id } }`, nil, nil); err == nil || !strings.Contains(err.Error(), "GraphQL failure") {
		t.Fatal(err)
	}
}

func TestPartialUpdateDoesNotClearOtherFields(t *testing.T) {
	f := newFakeLinear(t)
	a := f.app()
	if _, err := a.doAction(t.Context(), "priority-1", "linear_update", []byte(`{"id":"issue-uuid-1","priority":1}`)); err != nil {
		t.Fatal(err)
	}
	var last graphRequest
	for _, req := range f.requests {
		if strings.Contains(req.Query, "mutation Update") {
			last = req
		}
	}
	input := last.Variables["input"].(map[string]any)
	if len(input) != 1 || input["priority"] != float64(1) {
		t.Fatal(input)
	}
}

func TestUnknownOutcomeStopsRetry(t *testing.T) {
	f := newFakeLinear(t)
	a := f.app()
	store, err := openWriteStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.receipts = store
	args := []byte(`{"team_id":"team-eng","title":"Persistent issue"}`)
	if _, err := a.doAction(t.Context(), "persist-1", "linear_create", args); err != nil {
		t.Fatal(err)
	}
	key, _ := json.Marshal(map[string]any{"action": "linear_create", "input": map[string]any{"teamId": "team-eng", "title": "Persistent issue"}})
	raw, _ := json.Marshal(map[string]writeReceipt{"persist-1": {Key: string(key), Status: "pending", StartedAt: time.Now()}})
	if err := os.WriteFile(store.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	a.receipts, _ = openWriteStore(filepath.Dir(store.path))
	if _, err := a.doAction(t.Context(), "persist-1", "linear_create", args); err == nil || !strings.Contains(err.Error(), "inspect") {
		t.Fatal("uncertain create was repeated")
	}
}

func TestLocalizedViewsAndCreateReview(t *testing.T) {
	f := newFakeLinear(t)
	a := f.app()
	for _, locale := range []string{"en", "ja", "zh-Hans"} {
		for _, ui := range []int{1, 3, 6} {
			r := &ext.UIRequest{UIParams: wire.UIParams{Locale: locale, UIVersion: ui}}
			v, err := a.render(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			if err := wire.CheckView(v); err != nil {
				t.Fatalf("%s %d: %v", locale, ui, err)
			}
		}
	}
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "zh-Hans", UIVersion: 6, State: ext.EncodeState(pageState{Mode: "create", DraftID: "draft-1"}), Action: "review-create", Values: map[string]string{"team_id": "team-eng", "title": "New issue", "description": "Details"}}}
	res, err := a.action(t.Context(), r)
	if err != nil || res.View == nil || !strings.Contains(res.View.State, "review") {
		t.Fatalf("%v %+v", err, res)
	}
	if err := wire.CheckView(res.View); err != nil {
		t.Fatal(err)
	}
	r.State, r.Action = res.View.State, "confirm-create"
	res, err = a.action(t.Context(), r)
	if err != nil || !res.Pop || res.Toast == "" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestDetailBounds(t *testing.T) {
	var item issue
	item.Identifier, item.Title, item.Description, item.URL = "ENG-1", "Title", strings.Repeat("context ", 2000), "https://linear.app/example/ENG-1"
	v := detailView(&ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}, pageState{IssueID: "issue-uuid-1", Mode: "detail"}, item)
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}

func TestPageCarriesForwardCursor(t *testing.T) {
	f := newFakeLinear(t)
	a := f.app()
	r := &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 6}}
	v, err := a.render(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.State, r.Action = v.State, "more"
	res, err := a.action(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if res.View == nil || !strings.Contains(res.View.State, "cursor-next") {
		t.Fatal("next page cursor was not carried forward")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 2 || f.requests[1].Variables["after"] != "cursor-next" {
		t.Fatal(f.requests)
	}
}

func TestNoToken(t *testing.T) {
	a := newApp()
	if a.status()["state"] != "not_configured" {
		t.Fatal(a.status())
	}
	v, err := a.render(t.Context(), &ext.UIRequest{})
	if err != nil || !strings.Contains(v.Sections[0].Items[0].Title, "API key") {
		t.Fatalf("%v %+v", err, v)
	}
	if err := newClient("http://localhost", "").call(t.Context(), "x", nil, nil); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatal("missing API key was accepted")
	}
}
