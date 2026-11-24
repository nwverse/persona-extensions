package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestDetailViewWithLifecycleOnlyActivities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		makePR := func(id int64, title string) pullRequest {
			var pr pullRequest
			pr.ID, pr.Title, pr.Description, pr.State, pr.Open = id, title, "Summary", "OPEN", true
			pr.Author.User = user{Name: "author", DisplayName: "Author"}
			pr.FromRef.DisplayID, pr.ToRef.DisplayID = "feat", "main"
			return pr
		}
		switch r.URL.Path {
		case "/projects/ENG/repos/reader/pull-requests/1":
			_ = json.NewEncoder(w).Encode(makePR(1, "Lifecycle only"))
		case "/projects/ENG/repos/reader/pull-requests/1/activities":
			fmt.Fprint(w, `{"values":[{"id":1,"action":"OPENED","user":{"name":"author","displayName":"Author"}},{"id":2,"action":"APPROVED","user":{"name":"rev","displayName":"Reviewer"}}]}`)
		case "/projects/ENG/repos/reader/pull-requests/2":
			_ = json.NewEncoder(w).Encode(makePR(2, "With comment"))
		case "/projects/ENG/repos/reader/pull-requests/2/activities":
			fmt.Fprint(w, `{"values":[{"id":1,"action":"OPENED","user":{"name":"author","displayName":"Author"}},{"id":2,"action":"COMMENTED","comment":{"text":"Looks good","author":{"name":"rev","displayName":"Reviewer"}}}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	a := &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}

	for _, tc := range []struct {
		name         string
		id           string
		wantSections int
	}{
		{"lifecycle only", "1", 2},
		{"with comment", "2", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ext.UIRequest{UIParams: wire.UIParams{
				Locale:    "zh-Hans",
				UIVersion: 6,
				Action:    "detail",
				State:     ext.EncodeState(pageState{Project: "ENG", Repo: "reader"}),
				Values:    map[string]string{"id": tc.id},
			}}
			res, err := a.action(t.Context(), r)
			if err != nil || res.View == nil {
				t.Fatalf("detail: %+v, %v", res, err)
			}
			if err := wire.CheckView(res.View); err != nil {
				t.Fatalf("CheckView: %v", err)
			}
			if len(res.View.Sections) != tc.wantSections {
				t.Fatalf("sections=%d, want %d", len(res.View.Sections), tc.wantSections)
			}
		})
	}
}
