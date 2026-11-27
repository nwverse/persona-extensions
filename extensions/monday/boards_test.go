package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

var (
	boardsLimit = regexp.MustCompile(`boards\([^)]*limit: (\d+)`)
	boardsPage  = regexp.MustCompile(`boards\([^)]*page: (\d+)`)
)

// documentedBoards serves the root boards query the way monday's boards reference documents it: `limit` is the number
// of boards to return and defaults to 25, `page` starts at 1. total boards (ids 1 to total) are served.
func documentedBoards(t *testing.T, total int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || !strings.Contains(in.Query, "boards") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		limit, page := 25, 1
		if m := boardsLimit.FindStringSubmatch(in.Query); m != nil {
			limit, _ = strconv.Atoi(m[1])
		}
		if m := boardsPage.FindStringSubmatch(in.Query); m != nil {
			page, _ = strconv.Atoi(m[1])
		}
		boards := []map[string]string{}
		for number := (page-1)*limit + 1; number <= min(page*limit, total); number++ {
			boards = append(boards, map[string]string{"id": strconv.Itoa(number), "name": fmt.Sprintf("Board %d", number), "url": fmt.Sprintf("https://example.monday.com/boards/%d", number)})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"boards": boards}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBoardsReadEveryPageOfTheBoardsQuery(t *testing.T) {
	a := &app{client: newClient(documentedBoards(t, 120).URL, testToken), receipts: newWriteStore("")}
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil || len(v.Sections[0].Items) != 120 {
		t.Fatalf("board list: %d rows, %v", len(v.Sections[0].Items), err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	text, err := a.callTool(t.Context(), "monday_boards", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(text, "Board #120: Board 120") || !strings.Contains(text, "Board #26: Board 26") {
		t.Fatalf("tool: %v (%d bytes)", err, len(text))
	}
}

func TestBoardsStopAtViewLimit(t *testing.T) {
	a := &app{client: newClient(documentedBoards(t, 400).URL, testToken), receipts: newWriteStore("")}
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{})
	if err != nil || len(v.Sections[0].Items) != 250 {
		t.Fatalf("board list: %d rows, %v", len(v.Sections[0].Items), err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
}
