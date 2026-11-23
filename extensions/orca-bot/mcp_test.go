package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/exttest"
)

func TestMCPProtocol(t *testing.T) {
	var asked []mcpRequest
	s := &mcpServer{bot: "bot-alpha", session: "claude_chat_1", ask: func(req mcpRequest) (mcpReply, error) {
		asked = append(asked, req)
		if req.TodoID == "todo_x" {
			return mcpReply{Error: notHanded}, nil
		}
		return mcpReply{OK: true, Result: map[string]any{"status": "written"}}, nil
	}}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"todo_write_preparation","arguments":{"todo_id":"todo_1","text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"todo_get","arguments":{"todo_id":"todo_x"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"todo_get","arguments":{"todo_id":"../etc"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"nope"}`,
		`not json`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := s.serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 7 { // the notification gets no answer
		t.Fatalf("%d answers:\n%s", len(lines), out.String())
	}
	var res []map[string]any
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		res = append(res, m)
	}
	if init := toJSON(res[0]); !strings.Contains(init, `"tools"`) || !strings.Contains(init, "persona-todos") {
		t.Fatalf("initialize: %s", init)
	}
	tools := toJSON(res[1])
	for _, name := range []string{"todo_list", "todo_get", "todo_write_preparation"} {
		if !strings.Contains(tools, `"`+name+`"`) {
			t.Fatalf("tools/list lacks %s: %s", name, tools)
		}
	}
	if r := toJSON(res[2]); !strings.Contains(r, "written") || strings.Contains(r, "isError") {
		t.Fatalf("write: %s", r)
	}
	if r := toJSON(res[3]); !strings.Contains(r, `"isError":true`) {
		t.Fatalf("a to-do not handed over: %s", r)
	}
	if r := toJSON(res[4]); !strings.Contains(r, `"isError":true`) {
		t.Fatalf("an odd id: %s", r)
	}
	if r := toJSON(res[5]); !strings.Contains(r, "-32601") {
		t.Fatalf("unknown method: %s", r)
	}
	if r := toJSON(res[6]); !strings.Contains(r, "-32700") {
		t.Fatalf("parse error: %s", r)
	}
	if len(asked) != 2 || asked[0].Method != "write" || asked[0].Bot != "bot-alpha" || asked[0].Session != "claude_chat_1" {
		t.Fatalf("asked: %+v", asked)
	}
}

func TestMCPCommandNeedsItsFlags(t *testing.T) {
	var errOut bytes.Buffer
	if code := runMCP(nil, strings.NewReader(""), &bytes.Buffer{}, &errOut); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if code := runMCP([]string{"--socket", "/tmp/x.sock", "--bot", "bot-alpha"}, strings.NewReader(""), &bytes.Buffer{}, &errOut); code != 2 {
		t.Fatalf("without a token: exit %d", code)
	}
	if code := runMCP([]string{"--socket", "/tmp/x.sock", "--bot", "bot-alpha", "--token", "t"}, strings.NewReader(""), &bytes.Buffer{}, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

func TestMCPWithoutTheNodeProgram(t *testing.T) {
	s := &mcpServer{socket: "/tmp/no-such-orca-bot.sock", bot: "bot-alpha"}
	if r := toJSON(s.call("todo_list", "", "")); !strings.Contains(r, "连不上") {
		t.Fatalf("got %s", r)
	}
}

func TestProgramsSpeakTheProtocol(t *testing.T) {
	exttest.Check(t, manifest, exttest.Serve(t, newHubApp(&fakeHub{}, nil).program()))
	// The node program serves no page (the hub program does): it is checked against the manifest without one.
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	delete(m["contributes"].(map[string]any), "ui")
	nodeManifest, _ := json.Marshal(m)
	exttest.Check(t, nodeManifest, exttest.Serve(t, newNodeApp(newOrcaClient(func() string { return "" })).program()))
}
