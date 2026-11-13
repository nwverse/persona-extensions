package model

import "testing"

func TestRefused(t *testing.T) {
	for msg, want := range map[string]bool{
		"model.complete: ai: the model declined to answer":           true,
		"model.complete: upstream stop_reason refusal":               true,
		"model.complete: finish_reason content_filter":               true,
		"model.complete: dial tcp 127.0.0.1:443: connection refused": false,
		"model.complete: 529 overloaded":                             false,
	} {
		if got := refused(msg); got != want {
			t.Errorf("refused(%q) = %v, want %v", msg, got, want)
		}
	}
}
