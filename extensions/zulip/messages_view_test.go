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

// A page reads 31 messages of a topic, and each is a header and a component per line. Pasted logs and code make a
// message a hundred lines, and the view is refused above wire.ItemsMost components.
func TestTopicWithLongMessagesFitsTheView(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var messages []message
		for i := 1; i <= 31; i++ {
			content := fmt.Sprintf("message %d\nsecond line", i)
			if i%10 == 0 {
				content = fmt.Sprintf("message %d\n", i) + strings.Repeat("log line\n", 100)
			}
			messages = append(messages, message{ID: int64(i), SenderFullName: "Ann", Subject: "deploy", Content: content})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "success", "found_oldest": true, "messages": messages})
	}))
	defer srv.Close()
	a := &app{client: newClient(srv.URL, testEmail, testToken)}
	v, err := a.page(t.Context(), &ext.UIRequest{UIParams: wire.UIParams{Locale: "en", UIVersion: 3}}, pageState{Mode: "messages", StreamID: 7, Topic: "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.CheckView(v); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, c := range v.Sections[0].Items {
		texts = append(texts, c.Text)
	}
	all := strings.Join(texts, "\n")
	for _, want := range []string{"message 1\n", "message 31\n", "message 30\n"} {
		if !strings.Contains(all+"\n", want) {
			t.Fatalf("%q is lost: the shortest messages are kept whole and the longest cut", want)
		}
	}
	if !strings.Contains(all, "[…]") {
		t.Fatal("the cut is not shown")
	}
}
