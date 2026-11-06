package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/x/internal/serve"
	"github.com/nwverse/persona-extensions/extensions/x/internal/x"
	"github.com/nwverse/persona-extensions/extensions/x/internal/xtest"
	"github.com/nwverse/persona-extensions/pluginkit"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The hub reads the connector from persona-extension.json before the program runs and uses it over what the program
// says: the two must be the same, with the inbox kind described, the standard triage rules setting and a fallback.
func TestManifestIsWhatTheProgramDeclares(t *testing.T) {
	raw, err := os.ReadFile("persona-extension.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		ID          string                     `json:"id"`
		Contributes map[string]json.RawMessage `json:"contributes"`
		Runtime     struct {
			Command map[string]string `json:"command"`
		} `json:"runtime"`
		Configuration struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"configuration"`
		Permissions struct {
			Network []string `json:"network"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var items []wire.ConnectorItem
	if err := json.Unmarshal(m.Contributes["connectors"], &items); err != nil || len(items) != 1 {
		t.Fatalf("connectors: %s (%v)", m.Contributes["connectors"], err)
	}
	c := x.New(x.Settings{}, x.Options{})
	want := serve.ItemOf(serve.ManifestOf(c))
	if !reflect.DeepEqual(items[0], want) {
		t.Fatalf("persona-extension.json declares\n%+v\nthe program\n%+v", items[0], want)
	}
	if m.ID != "x" || want.ID != "x" {
		t.Fatalf("id %s / %s", m.ID, want.ID)
	}
	if got, kinds := slices.Sorted(maps.Keys(want.KindDescriptions)), slices.Sorted(slices.Values(want.Kinds)); !slices.Equal(got, kinds) {
		t.Fatalf("described %v, kinds %v", got, kinds)
	}
	if in, err := pluginkit.CheckManifest(raw); err != nil || !in.Fallback("x") {
		t.Fatalf("inbox rules: %v (fallback %v)", err, in.Fallback("x"))
	}
	if err := pluginkit.CheckStandardSettings(raw); err != nil {
		t.Fatal(err)
	}
	if len(m.Runtime.Command) != 4 {
		t.Fatalf("platforms: %v", m.Runtime.Command)
	}
	for p, cmd := range m.Runtime.Command {
		if cmd != "bin/x-"+p {
			t.Fatalf("%s: %s", p, cmd)
		}
	}
	var ui []wire.UIItem
	if err := json.Unmarshal(m.Contributes["ui"], &ui); err != nil || len(ui) != 1 || ui[0].ID != "x" || ui[0].Placement != wire.PlacementNavPage {
		t.Fatalf("ui: %s (%v)", m.Contributes["ui"], err)
	}
	for _, k := range []string{"triage_rules", "feed_source", "list_ids", "daily_post_budget", "retention_days", "proxy"} {
		if _, ok := m.Configuration.Properties[k]; !ok {
			t.Errorf("no setting %s", k)
		}
	}
	if !slices.Equal(m.Permissions.Network, []string{"api.x.com", "x.com", "*.twimg.com"}) {
		t.Fatalf("network %v", m.Permissions.Network)
	}
	if _, err := os.Stat("skills/x/SKILL.md"); err != nil {
		t.Fatal(err)
	}
}

// The program holds to the process protocol as the SDK's own check has it. Signed out, so nothing dials X.
func TestConformance(t *testing.T) {
	init := exttest.Check(t, manifest, exttest.Serve(t, program(&app{})))
	var items []wire.ConnectorItem
	if err := json.Unmarshal(init.Contributes["connectors"], &items); err != nil || len(items) != 1 || items[0].ID != "x" {
		t.Fatalf("contributes %s (%v)", init.Contributes["connectors"], err)
	}
}

// signedIn is the app built against a fake X, signed in and synced once.
func signedIn(t *testing.T) (*app, *x.Connector) {
	t.Helper()
	f := xtest.New(t)
	f.Me = xtest.User{ID: "1000", Name: "Example User", Username: "example_user"}
	f.AddUser(f.Me)
	f.AddUser(xtest.User{ID: "2001", Name: "Example Author", Username: "example_author", Followers: 100})
	f.Following = []xtest.User{{ID: "2001", Name: "Example Author", Username: "example_author", Followers: 100}}
	at := time.Now().Add(-time.Hour)
	long := strings.Repeat("A long line of a long post. ", 600) // about 16,800 characters
	f.Home = []xtest.Tweet{
		{ID: "1800000000000000003", Text: long[:270], Note: long, AuthorID: "2001", CreatedAt: at, Likes: 900, URLs: []string{"https://example.com/story"}},
		{ID: "1800000000000000002", Text: "A short post with a link", AuthorID: "2001", CreatedAt: at.Add(-time.Minute), Likes: 3, URLs: []string{"https://example.com/story"}},
		// Links the hub's view check refuses as link components (user info, no host name): shown as text instead.
		{ID: "1800000000000000001", Text: "Odd links", AuthorID: "2001", CreatedAt: at.Add(-90 * time.Second), URLs: []string{"https://user@example.com/a", "https://:443/a"}},
	}
	for i := range 40 {
		f.Home = append(f.Home, xtest.Tweet{ID: fmt.Sprint(1700000000000000000 + i), Text: fmt.Sprintf("Filler post %d\nwith two lines", i), AuthorID: "2001",
			CreatedAt: at.Add(-time.Duration(i+2) * time.Minute), Likes: i})
	}
	a := &app{base: f.URL()}
	conn, err := a.build(serve.Init{Locale: "en", Timezone: "UTC", DataDir: t.TempDir()}, &xtest.Secrets{})
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*x.Connector)
	p, err := c.LoginStep(context.Background(), "keys", map[string]string{"api_key": xtest.Keys.APIKey, "api_secret": xtest.Keys.APISecret,
		"access_token": xtest.Keys.AccessToken, "access_token_secret": xtest.Keys.AccessTokenSecret})
	if err != nil || !p.Done {
		t.Fatalf("sign-in %+v %v", p, err)
	}
	if err := c.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a, c
}

func render(t *testing.T, a *app, params wire.UIParams) (*wire.View, string) {
	t.Helper()
	v, err := a.render(context.Background(), &ext.UIRequest{UIParams: params})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if err := exttest.CheckView(raw, false); err != nil {
		t.Fatalf("view (UI %d, %s): %v\n%s", params.UIVersion, params.Locale, err, raw)
	}
	// The hub's own, stricter check (link URLs, unique keys, segmented, stats).
	if err := wire.CheckView(v); err != nil {
		t.Fatalf("hub view check (UI %d, %s): %v\n%s", params.UIVersion, params.Locale, err, raw)
	}
	return v, string(raw)
}

func TestPageSignedOut(t *testing.T) {
	a := &app{}
	if _, err := a.build(serve.Init{}, &xtest.Secrets{}); err != nil {
		t.Fatal(err)
	}
	for _, ui := range []int{1, 2, 3, 5} {
		for _, loc := range []string{"zh-Hans", "en", "ja"} {
			v, raw := render(t, a, wire.UIParams{Contribution: "x", Placement: wire.PlacementNavPage, Size: wire.SizeFull, UIVersion: ui, Locale: loc})
			if len(v.Sections) != 1 || strings.Contains(raw, "segmented") {
				t.Fatalf("signed out: %s", raw)
			}
		}
	}
}

func TestPageSignedIn(t *testing.T) {
	a, _ := signedIn(t)
	for _, ui := range []int{1, 2, 3, 5} {
		for _, loc := range []string{"zh-Hans", "en", "ja"} {
			for _, w := range []string{"", `{"w":"3d"}`} {
				_, raw := render(t, a, wire.UIParams{Contribution: "x", Placement: wire.PlacementNavPage, Size: wire.SizeFull, UIVersion: ui, Locale: loc, State: w, TZ: "UTC"})
				for _, k := range []string{xtest.Keys.APIKey, xtest.Keys.APISecret, xtest.Keys.AccessToken, xtest.Keys.AccessTokenSecret} {
					if strings.Contains(raw, k) {
						t.Fatal("a key on the page")
					}
				}
				if ui >= 2 && !strings.Contains(raw, `"type":"segmented"`) || ui < 2 && strings.Contains(raw, "segmented") {
					t.Fatalf("UI %d window control: %s", ui, raw)
				}
			}
		}
	}
	v, raw := render(t, a, wire.UIParams{Contribution: "x", Placement: wire.PlacementNavPage, Size: wire.SizeFull, UIVersion: 5, Locale: "en", TZ: "UTC"})
	for _, want := range []string{"@example_user", "Example Author @example_author", "https://x.com/example_author/status/1800000000000000003",
		"Top posts", "Most shared links", "https://example.com/story", "shared by 1", "Most active accounts you follow", "43 posts"} {
		if !strings.Contains(raw, want) {
			t.Errorf("no %q", want)
		}
	}
	// The links the hub would refuse as link components are there, as text.
	odd := 0
	for _, s := range v.Sections {
		for _, it := range s.Items {
			if strings.Contains(it.Text, "user@example.com") || strings.Contains(it.Text, "https://:443/a") {
				odd++
				if it.Type != wire.ComponentText {
					t.Errorf("odd link as %s", it.Type)
				}
			}
			if it.Type == wire.ComponentLink && (strings.Contains(it.URL, "@") || strings.HasPrefix(it.URL, "https://:")) {
				t.Errorf("odd link component %s", it.URL)
			}
		}
	}
	if odd != 2 {
		t.Errorf("odd links shown %d times", odd)
	}
	// The long post comes whole (in pieces) up to the cap, and says the rest is on X.
	pieces := 0
	for _, s := range v.Sections {
		for _, it := range s.Items {
			if strings.HasPrefix(it.Key, "top:1800000000000000003:t") {
				pieces++
			}
		}
	}
	if pieces != 6 || !strings.Contains(raw, "The rest of this post is on X.") {
		t.Fatalf("long post in %d pieces", pieces)
	}
	// The segmented control switches the window.
	res, err := a.action(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "x", Action: "window", Values: map[string]string{"w": "3d"}, UIVersion: 5, Locale: "en"}})
	if err != nil || res.View == nil || res.View.State != `{"w":"3d"}` {
		t.Fatalf("window: %+v %v", res, err)
	}
	// Older apps tap a row.
	res, err = a.action(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "x", Action: "window:24h", UIVersion: 1, Locale: "en", State: `{"w":"3d"}`}})
	if err != nil || res.View.State != `{"w":"24h"}` {
		t.Fatalf("window row: %+v %v", res, err)
	}
	res, err = a.action(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "x", Action: "refresh", UIVersion: 5, Locale: "en"}})
	if err != nil || res.View == nil || res.Toast == "" {
		t.Fatalf("refresh: %+v %v", res, err)
	}
	res, _ = a.action(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "x", Action: "profile:example_author"}})
	if res == nil || res.OpenURL != "https://x.com/example_author" {
		t.Fatalf("profile: %+v", res)
	}
	if res, _ := a.action(context.Background(), &ext.UIRequest{UIParams: wire.UIParams{Contribution: "x", Action: "profile:../evil"}}); res != nil && res.OpenURL != "" {
		t.Fatalf("profile of a bad name: %+v", res)
	}
}
