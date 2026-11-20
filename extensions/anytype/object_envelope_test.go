package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveObjects answers as Anytype's API does: reading or creating one object answers {"object": {...}}
// (ObjectResponse), not the bare object.
func serveObjects(t *testing.T) *app {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/spaces/space-1/objects/object-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"object": sampleObject()})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/spaces/space-1/types":
			_, _ = w.Write([]byte(`{"data":[{"id":"type-page","key":"page","name":"Page","layout":"basic"}],"pagination":{"offset":0,"limit":50,"total":1,"has_more":false}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/spaces/space-1/objects":
			item := sampleObject()
			item.ID, item.Name = "created-object", "New note"
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"object": item})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &app{client: newClient(srv.URL, testToken), receipts: newWriteStore("")}
}

func TestObjectIsReadFromTheObjectField(t *testing.T) {
	a := serveObjects(t)
	text, err := a.callTool(t.Context(), "anytype_object", json.RawMessage(`{"space_id":"space-1","object_id":"object-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "object-1") || !strings.Contains(text, "Reading context") || !strings.Contains(text, "Keep source context") {
		t.Fatalf("the object is empty: %s", text)
	}
}

func TestCreatedObjectIsReadFromTheObjectField(t *testing.T) {
	a := serveObjects(t)
	out, err := a.doAction(t.Context(), "create-1", "anytype_create_object", []byte(`{"space_id":"space-1","type_key":"page","name":"New note","body":"Text"}`))
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := out["text"].(string); !strings.Contains(text, "created-object") || !strings.Contains(text, "New note") {
		t.Fatalf("the confirmation is empty: %v", out)
	}
}
