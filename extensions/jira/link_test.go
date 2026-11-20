package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Jira's "self" names the REST resource (https://site/rest/api/3/issue/10001), not the issue's page, so the page's Open
// link and the tools' issue link have to be built from the site and the key.
func newLinkJira(t *testing.T) (*httptest.Server, *app) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		issue := `{"id":"10001","key":"ENG-123","self":"https://jira.example.com/rest/api/3/issue/10001","fields":{"summary":"Review","project":{"key":"ENG","name":"Engineering"},"status":{"name":"To Do"}}}`
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/api/3/issue/ENG-123":
			fmt.Fprint(w, issue)
		case "/rest/api/3/search/jql":
			fmt.Fprint(w, `{"issues":[`+issue+`],"isLast":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &app{client: newClient(srv.URL+"/rest/api/3", testToken), receipts: newWriteStore("")}
}

func TestOpenLinkIsTheIssuePage(t *testing.T) {
	srv, a := newLinkJira(t)
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "detail", IssueKey: "ENG-123"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range v.Sections[0].Items {
		if c.Type == wire.ComponentLink {
			got = append(got, c.URL)
		}
	}
	if len(got) != 1 || got[0] != srv.URL+"/browse/ENG-123" {
		t.Fatalf("open links: %v", got)
	}
}

func TestToolsLinkTheIssuePage(t *testing.T) {
	srv, a := newLinkJira(t)
	for name, args := range map[string]string{"jira_issue": `{"key":"ENG-123"}`, "jira_assigned": `{}`} {
		text, err := a.callTool(t.Context(), name, json.RawMessage(args))
		if err != nil || !strings.Contains(text, srv.URL+"/browse/ENG-123") || strings.Contains(text, "/rest/api/") {
			t.Fatalf("%s: %v\n%s", name, err, text)
		}
	}
}
