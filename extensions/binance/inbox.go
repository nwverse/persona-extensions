package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/pluginkit"
)

// event is something to tell the user: an inbox item (same thread updates the item) and maybe a push.
type event struct {
	Thread string
	Kind   string
	Tier   pluginkit.Tier
	Title  string
	Body   string
	Push   string // "", "active" or "time-sensitive"
	At     time.Time
}

type thingExtra struct {
	pluginkit.ThingExtra
}

type thing = pluginkit.InboxThing[thingExtra]

// inbox holds the items in memory for inbox.collect (which must not reach the network); a day's worth.
type inbox struct {
	mu     sync.Mutex
	things map[string]thing
	seq    int
}

func newInbox() *inbox { return &inbox{things: map[string]thing{}} }

func (b *inbox) put(e event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	b.things[e.Thread] = thing{Thread: e.Thread, Kind: e.Kind, Label: "币安", Title: e.Title, Sender: "币安",
		Preview: clip(e.Body, 200), Context: e.Title + "\n" + e.Body, At: e.At,
		Last: fmt.Sprintf("%d-%d", e.At.UnixMilli(), b.seq), URL: "https://www.binance.com/en/my/dashboard",
		Tags:  []pluginkit.InboxTag{{Key: "kind", Value: e.Kind, Name: e.Kind}},
		Extra: &thingExtra{pluginkit.ThingExtra{RuleTier: e.Tier}}}
	for k, t := range b.things {
		if e.At.Sub(t.At) > 24*time.Hour {
			delete(b.things, k)
		}
	}
}

func (b *inbox) collect(rules string) []thing {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]thing, 0, len(b.things))
	for _, t := range b.things {
		t.Rules = rules
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// raise puts e in the inbox, wakes the hub and pushes it when it says so.
func (a *app) raise(e event) {
	a.inbox.put(e)
	a.mu.RLock()
	hub, n := a.hub, a.notify
	a.mu.RUnlock()
	if hub != nil {
		_ = hub.Notify("connector.changed", map[string]any{"contribution": connectorID, "scope": "inbox"})
	}
	if e.Push != "" && n != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := n.send(ctx, e); err != nil {
				log.Printf("binance: notify: %v", err)
			}
		}()
	}
}

type notifier interface {
	send(ctx context.Context, e event) error
}

// hubNotifier posts to the hub's POST /api/v1/notify with the extension's token.
type hubNotifier struct {
	url, token string
	http       *http.Client
}

func newHubNotifier(hubURL, token string) *hubNotifier {
	return &hubNotifier{url: strings.TrimRight(hubURL, "/") + "/api/v1/notify", token: token,
		http: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}}
}

func (h *hubNotifier) send(ctx context.Context, e event) error {
	level := "active"
	push := "P1"
	if e.Push == "time-sensitive" {
		level, push = "time-sensitive", "P0"
	}
	id := "binance-" + e.Thread
	if len(id) > 56 {
		id = id[:56]
	}
	body, _ := json.Marshal(map[string]any{"title": clip(e.Title, 110), "body": clip(e.Body, 900), "level": level,
		"push_level": push, "id": id})
	req, err := http.NewRequestWithContext(ctx, "POST", h.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("hub answered %d", resp.StatusCode)
	}
	return nil
}
