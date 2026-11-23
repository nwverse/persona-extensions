package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/exttest"
)

func viewJSON(t *testing.T, w *world) string {
	t.Helper()
	v := w.app.view(w.ctx, "Asia/Shanghai")
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := exttest.CheckView(raw, false); err != nil {
		t.Fatalf("the page is not a valid view: %v\n%s", err, raw)
	}
	return string(raw)
}

func TestThePageListsTheBotsAndPicksOne(t *testing.T) {
	w := newWorld(t, workConfig())
	page := viewJSON(t, w)
	for _, want := range []string{"没有在转发", "还没选机器人", "Example Helper", "Second Bot", "已暂停", "pick:bot-alpha", "9.9.9-test"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "orca bot tools add") {
		t.Error("the MCP command is shown before a bot is picked")
	}
	if toast := w.app.act(w.ctx, "pick:bot-not-listed"); !strings.Contains(toast, "没有这个机器人") {
		t.Fatalf("a bot that is not listed: %s", toast)
	}
	if w.app.st.Bot != nil {
		t.Fatal("a bot that is not listed was kept")
	}
	w.pick("bot-alpha")
	page = viewJSON(t, w)
	for _, want := range []string{"正在转发", "正在用", "orca bot tools add", " mcp --socket ", "--bot bot-alpha", "--name persona-todos",
		"--bot 'Example Helper'", w.node.socket} {
		if !strings.Contains(page, want) {
			t.Errorf("after picking, the page lacks %q:\n%s", want, page)
		}
	}
	if toast := w.app.act(w.ctx, "clear"); !strings.Contains(toast, "不再转发") {
		t.Fatal(toast)
	}
	if ok, _ := w.app.ready(); ok {
		t.Fatal("still forwarding after clearing the bot")
	}
}

func TestThePageWithoutAMac(t *testing.T) {
	w := newWorld(t, config{Project: "work"})
	if page := viewJSON(t, w); !strings.Contains(page, "还没选 Mac") {
		t.Fatalf("page: %s", page)
	}
	if toast := w.app.act(w.ctx, "pick:bot-alpha"); !strings.Contains(toast, "Mac") {
		t.Fatalf("pick without a Mac: %s", toast)
	}
}

func TestThePageWhenOrcaIsDown(t *testing.T) {
	w := newWorld(t, workConfig())
	w.pick("bot-alpha")
	w.orca.stop()
	page := viewJSON(t, w)
	if !strings.Contains(page, "没在运行") || strings.Contains(page, "pick:") {
		t.Fatalf("page: %s", page)
	}
	w.link.down.Store(true)
	if page := viewJSON(t, w); !strings.Contains(page, "连不上") {
		t.Fatalf("page with the Mac offline: %s", page)
	}
}

func TestToolsAddCommandQuotes(t *testing.T) {
	got := toolsAddCommand("/Users/someone/My Data/bin/orca-bot", "/tmp/s.sock", &botChoice{ID: "bot-1", Name: `It's "Bot"`})
	want := `orca bot tools add "'/Users/someone/My Data/bin/orca-bot' mcp --socket /tmp/s.sock --bot bot-1" --name persona-todos --bot 'It'\''s "Bot"'`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
