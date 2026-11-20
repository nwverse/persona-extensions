package main

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
)

// adfCases are texts and the Atlassian Document they must arrive as: one paragraph per line, a blank line as a
// paragraph without content (a text node may not be empty), one text node per paragraph and nothing parsed.
var adfCases = []struct{ name, text, doc string }{
	{"one line", "Keep context", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Keep context"}]}]}`},
	{"two lines", "first\nsecond", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]},{"type":"paragraph","content":[{"type":"text","text":"second"}]}]}`},
	{"blank line", "first\n\nsecond", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]},{"type":"paragraph"},{"type":"paragraph","content":[{"type":"text","text":"second"}]}]}`},
	{"windows line ends", "first\r\nsecond", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"first"}]},{"type":"paragraph","content":[{"type":"text","text":"second"}]}]}`},
	{"leading and trailing line ends", "\nmiddle\n", `{"type":"doc","version":1,"content":[{"type":"paragraph"},{"type":"paragraph","content":[{"type":"text","text":"middle"}]},{"type":"paragraph"}]}`},
	{"quotes and backslash", `He said "hi" and wrote C:\temp\n`, `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"He said \"hi\" and wrote C:\\temp\\n"}]}]}`},
	{"han characters", "保留来源上下文。\n第二行", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"保留来源上下文。"}]},{"type":"paragraph","content":[{"type":"text","text":"第二行"}]}]}`},
	{"markdown is not parsed", "# Title\n**bold** and `code`", `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"# Title"}]},{"type":"paragraph","content":[{"type":"text","text":"**bold** and ` + "`code`" + `"}]}]}`},
}

func wantDoc(t *testing.T, raw string) any {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestCommentBodyIsSentAsADocument(t *testing.T) {
	for i, tc := range adfCases {
		f := newFakeJira(t)
		args, _ := json.Marshal(map[string]string{"key": "ENG-123", "body": tc.text})
		if _, err := f.app().doAction(t.Context(), "comment-"+strconv.Itoa(i), "jira_comment", args); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(f.comments) != 1 || !reflect.DeepEqual(f.comments[0]["body"], wantDoc(t, tc.doc)) {
			t.Fatalf("%s: the server received %v", tc.name, f.comments)
		}
	}
}

func TestIssueDescriptionIsSentAsADocument(t *testing.T) {
	cases := append([]struct{ name, text, doc string }{
		{"only line ends", "\n\n", `{"type":"doc","version":1,"content":[{"type":"paragraph"},{"type":"paragraph"},{"type":"paragraph"}]}`},
	}, adfCases...)
	for i, tc := range cases {
		f := newFakeJira(t)
		args, _ := json.Marshal(map[string]string{"project_id": "project-eng", "issuetype_id": "task", "summary": "New issue", "description": tc.text})
		if _, err := f.app().doAction(t.Context(), "create-"+strconv.Itoa(i), "jira_create", args); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(f.creates) != 1 {
			t.Fatalf("%s: the server received %d issues", tc.name, len(f.creates))
		}
		if got := f.creates[0]["fields"].(map[string]any)["description"]; !reflect.DeepEqual(got, wantDoc(t, tc.doc)) {
			t.Fatalf("%s: the server received %v", tc.name, got)
		}
	}
}

func TestIssueWithoutDescriptionSendsNone(t *testing.T) {
	f := newFakeJira(t)
	args := []byte(`{"project_id":"project-eng","issuetype_id":"task","summary":"No description"}`)
	if _, err := f.app().doAction(t.Context(), "create-none", "jira_create", args); err != nil {
		t.Fatal(err)
	}
	if _, present := f.creates[0]["fields"].(map[string]any)["description"]; len(f.creates) != 1 || present {
		t.Fatalf("a description was sent: %v", f.creates)
	}
}
