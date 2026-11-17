package page

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/github/internal/github"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

// cardGitHub answers the three lists the card reads (the page's 待我 review, 我提的 and 我参与的) with what it is
// given, and counts the requests for them; /user, which the connector's own check reads, is not counted.
type cardGitHub struct {
	review, mine       []string // GraphQL search nodes
	assigned, mentions []string // REST search items
	fail               bool     // the lists answer 502

	mu    sync.Mutex
	asked []string
}

func (f *cardGitHub) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_example" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/user" {
			_, _ = w.Write([]byte(`{"login":"octo"}`))
			return
		}
		f.mu.Lock()
		f.asked = append(f.asked, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		if f.fail {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"message":"Server Error"}`))
			return
		}
		switch r.URL.Path {
		case "/graphql":
			body, _ := io.ReadAll(r.Body)
			var q struct {
				Variables struct{ Q string } `json:"variables"`
			}
			_ = json.Unmarshal(body, &q)
			nodes := f.mine
			if strings.Contains(q.Variables.Q, "review-requested:@me") {
				nodes = f.review
			}
			_, _ = fmt.Fprintf(w, `{"data":{"search":{"nodes":[%s]}}}`, strings.Join(nodes, ","))
		case "/search/issues":
			items := f.mentions
			if strings.Contains(r.URL.Query().Get("q"), "assignee:@me") {
				items = f.assigned
			}
			_, _ = fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// requests is how many requests for the lists GitHub had.
func (f *cardGitHub) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked)
}

// pull is a PR as GraphQL's search has it, its CI rollup ci ("" none).
func pull(n int, title, author, ci string) string {
	rollup := "null"
	if ci != "" {
		rollup = `{"state":"` + ci + `"}`
	}
	return fmt.Sprintf(`{"number":%d,"title":%q,"url":"https://github.com/example-org/example-repo/pull/%d","updatedAt":%q,"isDraft":false,
"reviewDecision":null,"repository":{"nameWithOwner":"example-org/example-repo"},"author":{"login":%q,"avatarUrl":"https://avatars.githubusercontent.com/u/1?v=4"},
"commits":{"nodes":[{"commit":{"statusCheckRollup":%s}}]}}`, n, title, n, now.Add(-2*time.Hour).Format(time.RFC3339), author, rollup)
}

// issue is an issue as REST's search has it.
func issue(n int, title string) string {
	return fmt.Sprintf(`{"number":%d,"title":%q,"state":"open","html_url":"https://github.com/example-org/example-repo/issues/%d",
"repository_url":"https://api.github.com/repos/example-org/example-repo","updated_at":%q,"user":{"login":"carol"}}`, n, title, n, now.Add(-3*time.Hour).Format(time.RFC3339))
}

// busy is a GitHub where two PRs wait for the user's review, two of the user's four PRs failed CI, and two issues are
// assigned to the user or mention them (one both).
func busy() *cardGitHub {
	return &cardGitHub{
		review: []string{pull(12, "Add the cache", "alice", "SUCCESS"), pull(13, "Fix the build", "bob", "FAILURE")},
		mine: []string{pull(20, "Speed up the list", "octo", "SUCCESS"), pull(21, "Retry the upload", "octo", "FAILURE"),
			pull(22, "Split the config", "octo", "ERROR"), pull(23, "Bump the toolchain", "octo", "PENDING")},
		assigned: []string{issue(7, "Crash on start")},
		mentions: []string{issue(7, "Crash on start"), issue(8, "Docs are out of date")},
	}
}

func cardReq(version int) *ext.UIRequest {
	return &ext.UIRequest{UIParams: wire.UIParams{Contribution: CardID, Placement: wire.PlacementOverviewCard, Size: wire.SizeCompact,
		UIVersion: version, TZ: "UTC"}}
}

// card is the card drawn for an app of version, checked as the apps read a card (at most two sections and twelve
// items, no fields), and its text.
func card(t *testing.T, p *Page, version int) (*wire.View, string) {
	t.Helper()
	v, err := p.RenderCard(context.Background(), cardReq(version))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := exttest.CheckView(raw, true); err != nil {
		t.Fatalf("card: %v\n%s", err, raw)
	}
	// it only shows: no buttons, and nothing that would call ui.action on the card
	for _, s := range v.Sections {
		for _, c := range s.Items {
			if c.Type == wire.ComponentButton || c.Button != nil || (c.Action != nil && c.Action.Push == nil) {
				t.Fatalf("an action on the card: %+v", c)
			}
		}
	}
	return v, string(raw)
}

// The counts (the failed CI of the user's PRs in the danger tone), then review requests before the user's PRs whose
// CI failed, three at most, each opening its PR on the page.
func TestCardCountsAndRows(t *testing.T) {
	p := signedInTo(t, busy().serve(t))
	for _, version := range []int{3, 6, 9} {
		v, got := card(t, p, version)
		if len(v.Sections) != 2 || len(v.Sections[0].Items) != 1 || v.Sections[0].Items[0].Type != wire.ComponentStatGrid {
			t.Fatalf("v%d: counts, then rows: %s", version, got)
		}
		stats := v.Sections[0].Items[0].Stats
		want := []wire.Stat{{Label: "待我 review", Value: "2"}, {Label: "我提的 PR", Value: "4", Detail: "2 个 CI 失败", Tone: wire.ToneDanger},
			{Label: "指派、提到我的", Value: "2"}}
		if len(stats) != len(want) {
			t.Fatalf("v%d stats: %+v", version, stats)
		}
		for i := range want {
			if stats[i] != want[i] {
				t.Errorf("v%d stat %d: %+v, want %+v", version, i, stats[i], want[i])
			}
		}
		var order []string
		for _, row := range v.Sections[1].Items {
			order = append(order, row.Title)
			if row.Type != wire.ComponentRow || row.Action == nil || row.Action.Push == nil || row.Action.Push.UI != ID {
				t.Fatalf("v%d: %q does not open on the page: %+v", version, row.Title, row.Action)
			}
			var s state
			if err := json.Unmarshal([]byte(row.Action.Push.State), &s); err != nil || s.Repo != "example-org/example-repo" || s.Number == 0 || s.V != "" {
				t.Fatalf("v%d: %q opens %q (%v)", version, row.Title, row.Action.Push.State, err)
			}
		}
		if strings.Join(order, " | ") != "Add the cache | Fix the build | Retry the upload" {
			t.Fatalf("v%d rows: %v", version, order)
		}
		has(t, "rows", got, "example-org/example-repo#12 · 请你 review · alice · 2 小时前", `"text":"CI 失败","tone":"danger"`, `"push":{"ui":"github","state":"{\"r\":\"example-org/example-repo\",\"n\":21}"`)
	}

	// an app before UI version 3 has no push: its rows open the page, as the card does
	v, got := card(t, p, 2)
	if len(v.Sections) != 2 || len(v.Sections[1].Items) != 3 || strings.Contains(got, `"action"`) {
		t.Fatalf("v2: %s", got)
	}
}

// With nothing that needs the user the card says so in one line, not in counts of nothing.
func TestCardNothingToDo(t *testing.T) {
	p := signedInTo(t, (&cardGitHub{}).serve(t))
	v, got := card(t, p, 6)
	if len(v.Sections) != 1 || len(v.Sections[0].Items) != 1 || v.Sections[0].Items[0].Text != "GitHub 上没有等你处理的" {
		t.Fatalf("nothing to do: %s", got)
	}

	// PRs of the user's with nothing failed: the counts, no rows
	f := &cardGitHub{mine: []string{pull(20, "Speed up the list", "octo", "SUCCESS")}}
	v, got = card(t, signedInTo(t, f.serve(t)), 6)
	if len(v.Sections) != 1 || v.Sections[0].Items[0].Type != wire.ComponentStatGrid || strings.Contains(got, "CI 失败") ||
		!strings.Contains(got, `{"label":"我提的 PR","value":"1"}`) {
		t.Fatalf("only the user's PRs: %s", got)
	}
}

// Signed out, the card says so in one line, as the page does, and asks GitHub nothing.
func TestCardSignedOut(t *testing.T) {
	f := busy()
	c := github.New(github.Config{}, "", nil)
	c.UseAPI(f.serve(t).URL, "https://github.com")
	c.UseSecrets(&memSecrets{})
	p := New(c)
	v, got := card(t, p, 6)
	if len(v.Sections) != 1 || len(v.Sections[0].Items) != 1 || !strings.Contains(got, "还没连上 GitHub") || !strings.Contains(got, "设置 › 连接器 › GitHub") {
		t.Fatalf("signed out: %s", got)
	}
	if n := f.requests(); n != 0 {
		t.Fatalf("signed out, the card asked GitHub %d times", n)
	}
}

// GitHub failing with nothing read before: one line saying so.
func TestCardCannotRead(t *testing.T) {
	f := busy()
	f.fail = true
	v, got := card(t, signedInTo(t, f.serve(t)), 6)
	if len(v.Sections) != 1 || len(v.Sections[0].Items) != 1 || !strings.Contains(got, "读不到 GitHub") {
		t.Fatalf("failing: %s", got)
	}
}

// The card reads the page's lists through their cache: drawn again, or drawn right after the page showed the same
// lists, it makes no request to GitHub; drawn by several apps at once, it reads each list once.
func TestCardReadsThroughThePageCache(t *testing.T) {
	f := busy()
	p := signedInTo(t, f.serve(t))
	card(t, p, 6)
	first := f.requests()
	if first != 4 { // 待我 review and 我提的 (GraphQL), 我参与的 (REST: assigned, mentioning)
		t.Fatalf("the first card: %d requests (%v)", first, f.asked)
	}
	for range 3 {
		card(t, p, 6)
	}
	card(t, p, 3)
	if n := f.requests(); n != first {
		t.Fatalf("the card drawn again asked GitHub %d more times: %v", n-first, f.asked[first:])
	}

	// after the page
	f = busy()
	p = signedInTo(t, f.serve(t))
	for _, tab := range []string{github.TabReview, github.TabMine, github.TabInvolved} {
		if _, err := p.Render(context.Background(), req(6, state{Tab: tab}, "", nil)); err != nil {
			t.Fatal(err)
		}
	}
	read := f.requests()
	if _, got := card(t, p, 6); !strings.Contains(got, "Retry the upload") {
		t.Fatalf("after the page: %s", got)
	}
	if n := f.requests(); n != read {
		t.Fatalf("the card after the page asked GitHub %d more times: %v", n-read, f.asked[read:])
	}

	// several apps at once (Overview on the web and a phone): one read
	f = busy()
	p = signedInTo(t, f.serve(t))
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() { _, _ = p.RenderCard(context.Background(), cardReq(6)) })
	}
	wg.Wait()
	if n := f.requests(); n != 4 {
		t.Fatalf("five cards at once: %d requests (%v)", n, f.asked)
	}
}

// A card GitHub is slow for is a loading line within the page's budget, and the card itself once read.
func TestSlowCardLoadsInTheBackground(t *testing.T) {
	release := make(chan struct{})
	f := busy()
	srv := f.serve(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			<-release
		}
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	p := signedInTo(t, slow)
	p.budget = 50 * time.Millisecond
	v, got := card(t, p, 6)
	if len(v.Sections) != 1 || !strings.Contains(got, "正在从 GitHub 读取") || v.RefreshAfter != 30 {
		t.Fatalf("loading: %s", got)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Contains(got, "正在从 GitHub 读取") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		_, got = card(t, p, 6)
	}
	has(t, "read", got, "Add the cache", "2 个 CI 失败")
}
