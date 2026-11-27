package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// YouTrack answers resolved as a timestamp in milliseconds, or null for an issue that is still unresolved. The tool
// output gives the timestamp or says there is none; it must never print the address of the decoded pointer.
func TestIssueOutputGivesResolvedTime(t *testing.T) {
	resolved := int64(1762710000000)
	text := formatIssue(issue{ID: "2-42", IDReadable: "READ-42", Summary: "Closed", Updated: 1762720000000, Resolved: &resolved}, nil, false)
	if !strings.Contains(text, "updated: 1762720000000 | resolved: 1762710000000\n") || strings.Contains(text, "0x") {
		t.Fatalf("resolved issue:\n%s", text)
	}
}

func TestIssueToolSaysNoneForUnresolvedIssue(t *testing.T) {
	a := newFakeYouTrack(t).app()
	for name, args := range map[string]string{"youtrack_issues": `{}`, "youtrack_issue": `{"issue_id":"2-42"}`} {
		text, err := a.callTool(t.Context(), name, json.RawMessage(args))
		if err != nil || !strings.Contains(text, "resolved: none\n") || strings.Contains(text, "<nil>") {
			t.Fatalf("%s: %v\n%s", name, err, text)
		}
	}
}
