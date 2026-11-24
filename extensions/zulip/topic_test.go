package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

func TestLongTopicNameRendersTopicsAndPagesMessages(t *testing.T) {
	for _, tc := range []struct {
		name  string
		topic string
	}{
		{name: "han_500", topic: strings.Repeat("字", 500)},
		{name: "ascii_500", topic: strings.Repeat("a", 500)},
		{name: "short_control", topic: strings.Repeat("a", 50)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/users/me/subscriptions":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"result":        "success",
						"subscriptions": []stream{{ID: 7, Name: "reading", Description: "Reading"}},
					})
				case "/api/v1/users/me/7/topics":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"result": "success",
						"topics": []topic{{Name: tc.topic, MaxID: 42}},
					})
				case "/api/v1/messages":
					foundOldest := r.URL.Query().Get("anchor") == "42"
					_ = json.NewEncoder(w).Encode(map[string]any{
						"result":       "success",
						"found_oldest": foundOldest,
						"messages": []message{{
							ID:             42,
							SenderFullName: "Ann",
							Subject:        tc.topic,
							Content:        "First unread message",
							StreamID:       7,
						}},
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			a := &app{client: newClient(srv.URL, testEmail, testToken)}

			// Also verify messages state round-trip directly so readState is exercised independently of topics row keys.
			msgView, err := a.page(t.Context(), &ext.UIRequest{
				UIParams: wire.UIParams{Locale: "en", UIVersion: 6},
			}, pageState{Mode: "messages", StreamID: 7, Topic: tc.topic})
			if err != nil {
				t.Fatalf("messages page: %v", err)
			}
			if err := wire.CheckView(msgView); err != nil {
				t.Fatalf("messages CheckView: %v", err)
			}
			if msgView.Sections[0].More == nil {
				t.Fatal("expected More action on unread messages view")
			}
			olderDirect, err := a.action(t.Context(), &ext.UIRequest{
				UIParams: wire.UIParams{
					Locale:    "en",
					UIVersion: 6,
					State:     msgView.State,
					Action:    "more",
					Values:    msgView.Sections[0].More.Action.Values,
				},
			})
			if err != nil {
				t.Fatalf("more from messages state: %v", err)
			}
			if err := wire.CheckView(olderDirect.View); err != nil {
				t.Fatalf("more CheckView: %v", err)
			}

			topicsRes, err := a.action(t.Context(), &ext.UIRequest{
				UIParams: wire.UIParams{
					Locale:    "en",
					UIVersion: 6,
					Action:    "stream",
					Values:    map[string]string{"stream_id": "7"},
				},
			})
			if err != nil {
				t.Fatalf("stream: %v", err)
			}
			if err := wire.CheckView(topicsRes.View); err != nil {
				t.Fatalf("topics CheckView: %v", err)
			}

			row := topicsRes.View.Sections[0].Items[0]
			if row.Action == nil {
				t.Fatal("missing topic row action")
			}
			openRes, err := a.action(t.Context(), &ext.UIRequest{
				UIParams: wire.UIParams{
					Locale:    "en",
					UIVersion: 6,
					State:     topicsRes.View.State,
					Action:    "topic",
					Values:    row.Action.Values,
				},
			})
			if err != nil {
				t.Fatalf("topic: %v", err)
			}
			if err := wire.CheckView(openRes.View); err != nil {
				t.Fatalf("open messages CheckView: %v", err)
			}

			backRes, err := a.action(t.Context(), &ext.UIRequest{
				UIParams: wire.UIParams{
					Locale:    "en",
					UIVersion: 6,
					State:     openRes.View.State,
					Action:    "back",
				},
			})
			if err != nil {
				t.Fatalf("back: %v", err)
			}
			if backRes.View == nil {
				t.Fatal("missing back view")
			}
		})
	}
}
