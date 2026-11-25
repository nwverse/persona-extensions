// Command space-conversations is the optional domain-space product surface.
// Core retains track execution and privacy policy; this program draws pages.
package main

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"

	"github.com/nwverse/persona-extensions/pluginkit"
)

var manifest = pluginkit.PackagedManifest(authorManifest)

//go:embed persona-extension.json
var authorManifest json.RawMessage

func main() { ext.Main(program()) }

type App struct {
	mu     sync.RWMutex
	client *Client
}

func program() ext.Program {
	a := &App{}
	return ext.Program{
		ID: "space-conversations", Version: "0.1.2", Manifest: manifest,
		Init: func(_ context.Context, in *ext.Init, _ *ext.Hub) (map[string]any, error) {
			a.mu.Lock()
			a.client = NewClient(in.Hub.URL, in.Hub.Token)
			a.mu.Unlock()
			return nil, nil
		},
		UI: map[string]ext.Page{
			"home":  {Render: a.render, Action: a.action},
			"space": {Render: a.render, Action: a.action},
			"new":   {Render: a.render, Action: a.action},
			"edit":  {Render: a.render, Action: a.action},
		},
	}
}

func (a *App) current() (*Client, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.client == nil {
		return nil, errors.New("空间对话：扩展尚未初始化")
	}
	return a.client, nil
}

func (a *App) render(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
	c, err := a.current()
	if err != nil {
		return nil, err
	}
	slug := strings.TrimSpace(r.State)
	switch r.Contribution {
	case "new":
		return newView(), nil
	case "edit":
		if slug == "" {
			// Opened as an entry of its own (an extension's settings list every page): no space is chosen yet.
			return pickerView(ctx, c, "编辑 SPACE.md", "选择要编辑 SPACE.md 的空间。", "edit")
		}
		return editView(ctx, c, slug)
	case "space":
		if slug == "" {
			return pickerView(ctx, c, "空间", "选择一个空间。", "open")
		}
		return spaceView(ctx, c, slug)
	default:
		return homeView(ctx, c)
	}
}

// actionSlug is the space an action is about: the action's own value (UI version 4 and later), else the page's
// state, which every client sends back with an action.
func actionSlug(r *ext.UIRequest) string {
	return cmp.Or(strings.TrimSpace(r.Values["slug"]), strings.TrimSpace(r.State))
}

// notice answers an action with a hint instead of an error: an action error reaches the app as a failed page.
func notice(text string) (*wire.ActionResult, error) {
	return &wire.ActionResult{Toast: text}, nil
}

func action(id string, values map[string]string) *wire.Action {
	return &wire.Action{ID: id, Values: values}
}

func button(id, title string, values map[string]string) wire.Component {
	return wire.Component{Type: wire.ComponentButton, Title: title, Action: action(id, values)}
}

func homeView(ctx context.Context, c *Client) (*wire.View, error) {
	tracks, err := c.Tracks(ctx)
	if err != nil {
		return nil, err
	}
	items := spaceRows([]wire.Component{button("new", "新建空间", nil)}, tracks, "open")
	return &wire.View{Title: "空间对话", Sections: []wire.Section{{Title: "空间", Items: items}}, RefreshAfter: 60}, nil
}

// spaceRows appends a row per active space, each sending actionID with its slug, or an empty state when there is none.
func spaceRows(items []wire.Component, tracks []Track, actionID string) []wire.Component {
	count := 0
	for _, t := range tracks {
		if t.Archived || t.Builtin || t.Config.Sidebar.Group != "spaces" {
			continue
		}
		if len(items) >= wire.ItemsMost {
			break
		}
		items = append(items, wire.Component{
			Type:     wire.ComponentRow,
			Title:    clipRunes(cmp.Or(strings.TrimSpace(t.Title), t.Slug), wire.StringMost),
			Subtitle: clipRunes(strings.TrimSpace(t.Description), wire.StringMost),
			Icon:     "chat",
			Action:   action(actionID, map[string]string{"slug": t.Slug}),
		})
		count++
	}
	if count == 0 {
		items = append(items, wire.Component{Type: wire.ComponentEmpty, Title: "还没有空间", Detail: "为一件长期关心的事建立空间，相关对话和 SPACE.md 会留在这里。"})
	}
	return items
}

// pickerView is a page that needs a space but was opened without one: the spaces to choose from.
func pickerView(ctx context.Context, c *Client, title, hint, actionID string) (*wire.View, error) {
	tracks, err := c.Tracks(ctx)
	if err != nil {
		return nil, err
	}
	items := spaceRows([]wire.Component{{Type: wire.ComponentText, Text: hint, Style: "secondary"}}, tracks, actionID)
	if len(items) < wire.ItemsMost {
		items = append(items, button("new", "新建空间", nil))
	}
	return &wire.View{Title: title, Sections: []wire.Section{{Items: items}}, RefreshAfter: 60}, nil
}

func spaceView(ctx context.Context, c *Client, slug string) (*wire.View, error) {
	tracks, err := c.Tracks(ctx)
	if err != nil {
		return nil, err
	}
	var track *Track
	for i := range tracks {
		if tracks[i].Slug == slug {
			track = &tracks[i]
			break
		}
	}
	if track == nil {
		items := spaceRows([]wire.Component{{Type: wire.ComponentEmpty, Title: "这个空间不存在", Detail: "它可能已被删除。可以从下面选择其他空间。"}}, tracks, "open")
		return &wire.View{Title: "空间不可用", Sections: []wire.Section{{Items: items}}}, nil
	}
	machine, conversations, err := c.Conversations(ctx, slug)
	if err != nil {
		return nil, err
	}
	title := clipRunes(cmp.Or(strings.TrimSpace(track.Title), track.Slug), wire.StringMost)
	var items []wire.Component
	if desc := clipRunes(strings.TrimSpace(track.Description), wire.StringMost); desc != "" {
		items = append(items, wire.Component{Type: wire.ComponentText, Text: desc, Style: "secondary"})
	}
	items = append(items, button("edit", "编辑 SPACE.md", map[string]string{"slug": slug}))
	if track.Archived {
		items = append(items, button("unarchive", "恢复空间", map[string]string{"slug": slug}))
	} else {
		items = append(items, button("archive", "归档空间", map[string]string{"slug": slug}))
	}
	sections := []wire.Section{{Title: title, Items: items}}
	fixedItems := len(items)
	if !track.Archived {
		sections = append(sections, wire.Section{Title: "新对话", Items: []wire.Component{
			{Type: wire.ComponentField, Name: "prompt", Label: "消息", Kind: wire.FieldKindMultiline, Placeholder: "说说想在这个空间里处理的事"},
			{Type: wire.ComponentButton, Title: "开始对话", Action: &wire.Action{ID: "start", Submit: true, Values: map[string]string{"slug": slug}}},
		}})
		fixedItems += 2
	}
	maxRows := wire.ItemsMost - fixedItems
	rows := []wire.Component{}
	for _, chat := range conversations {
		if len(rows) >= maxRows {
			break
		}
		rows = append(rows, wire.Component{
			Type:     wire.ComponentRow,
			Title:    clipRunes(cmp.Or(strings.TrimSpace(chat.Title), strings.TrimSpace(chat.SessionID), "未命名对话"), wire.StringMost),
			Subtitle: clipRunes(strings.TrimSpace(chat.LastText), wire.StringMost),
			Icon:     "chat",
			Action:   action("chat", map[string]string{"slug": slug, "sid": chat.SessionID, "mid": cmp.Or(chat.MachineID, machine)}),
		})
	}
	if len(rows) == 0 {
		rows = append(rows, wire.Component{Type: wire.ComponentEmpty, Title: "还没有对话"})
	}
	sections = append(sections, wire.Section{Title: "最近对话", Items: rows})
	return &wire.View{Title: title, State: slug, Sections: sections, RefreshAfter: 30}, nil
}

func newView() *wire.View {
	return &wire.View{Title: "新建空间", Sections: []wire.Section{{Items: []wire.Component{
		{Type: wire.ComponentField, Name: "title", Label: "名称", Kind: wire.FieldKindText, Placeholder: "例如：阅读、旅行、家庭"},
		{Type: wire.ComponentField, Name: "slug", Label: "标识", Kind: wire.FieldKindText, Placeholder: "小写字母、数字和连字符"},
		{Type: wire.ComponentField, Name: "description", Label: "简介", Kind: wire.FieldKindMultiline, Placeholder: "这个空间用来做什么"},
		{Type: wire.ComponentField, Name: "categories", Label: "信息分类（可选）", Kind: wire.FieldKindText, Placeholder: "finance, relationships, emotion, legal_id"},
		{Type: wire.ComponentButton, Title: "创建空间", Action: &wire.Action{ID: "create", Submit: true}},
	}}}}
}

func editView(ctx context.Context, c *Client, slug string) (*wire.View, error) {
	md, err := c.SpaceMD(ctx, slug)
	if err != nil {
		return nil, err
	}
	return &wire.View{Title: "编辑 SPACE.md", State: slug, Sections: []wire.Section{{Items: []wire.Component{
		{Type: wire.ComponentText, Text: "SPACE.md 是这个空间长期保留的说明。Persona 管理的状态块不可在这里修改。", Style: "secondary"},
		{Type: wire.ComponentField, Name: "content", Label: "SPACE.md", Kind: wire.FieldKindMultiline, Value: md.Content},
		{Type: wire.ComponentButton, Title: "保存", Action: &wire.Action{ID: "save", Submit: true, Values: map[string]string{"slug": slug, "digest": md.Digest}}},
	}}}}, nil
}

func (a *App) action(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
	c, err := a.current()
	if err != nil {
		return nil, err
	}
	slug := actionSlug(r)
	switch r.Action {
	case "home":
		return &wire.ActionResult{Push: &wire.Push{UI: "home"}}, nil
	case "new":
		return &wire.ActionResult{Push: &wire.Push{UI: "new"}}, nil
	case "open":
		return &wire.ActionResult{Push: &wire.Push{UI: "space", State: slug}}, nil
	case "edit":
		return &wire.ActionResult{Push: &wire.Push{UI: "edit", State: slug}}, nil
	case "chat":
		return &wire.ActionResult{OpenURL: conversationURL(r.Values["mid"], slug, r.Values["sid"])}, nil
	case "create":
		title := strings.TrimSpace(r.Values["title"])
		if title == "" || utf8.RuneCountInString(title) > 80 {
			return notice("请输入不超过 80 个字符的空间名称")
		}
		slug = strings.TrimSpace(r.Values["slug"])
		if !validSlug(slug) {
			return notice("标识须以字母开头，只含小写字母、数字和连字符，且不能使用保留名称")
		}
		var cats []string
		for _, cat := range strings.Split(r.Values["categories"], ",") {
			if cat = strings.TrimSpace(cat); cat != "" {
				cats = append(cats, cat)
			}
		}
		track, err := c.Create(ctx, title, slug, strings.TrimSpace(r.Values["description"]), cats)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{Push: &wire.Push{UI: "space", State: track.Slug}, Toast: "空间已创建"}, nil
	case "save":
		if slug == "" {
			return notice("先选择一个空间")
		}
		if err := c.PutSpaceMD(ctx, slug, r.Values["content"], r.Values["digest"]); err != nil {
			return nil, err
		}
		return &wire.ActionResult{Push: &wire.Push{UI: "space", State: slug}, Toast: "SPACE.md 已保存"}, nil
	case "archive", "unarchive":
		tracks, err := c.Tracks(ctx)
		if err != nil {
			return nil, err
		}
		archived := r.Action == "archive"
		for _, track := range tracks {
			if track.Slug == slug {
				if err := c.Archive(ctx, track, archived); err != nil {
					return nil, err
				}
				v, err := homeView(ctx, c)
				if err != nil {
					return nil, err
				}
				return &wire.ActionResult{View: v, Toast: "空间状态已更新"}, nil
			}
		}
		return notice("空间不存在")
	case "start":
		prompt := strings.TrimSpace(r.Values["prompt"])
		if prompt == "" {
			return notice("先写一条消息")
		}
		if slug == "" {
			return notice("先选择一个空间")
		}
		if _, _, err := c.Start(ctx, slug, prompt); err != nil {
			return nil, err
		}
		// The refreshed page lists the new conversation; its row opens it. No persona://tracks link here: hubs before
		// the handoff was accepted refuse the whole answer, which would fail a conversation that did start.
		v, err := spaceView(ctx, c, slug)
		if err != nil {
			return nil, err
		}
		return &wire.ActionResult{View: v, Toast: "对话已开始"}, nil
	default:
		return nil, fmt.Errorf("未知操作 %q", r.Action)
	}
}

func clipRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

func conversationURL(machine, slug, sid string) string {
	return "persona://tracks/" + url.PathEscape(machine) + "/" + url.PathEscape(slug) + "/" + url.PathEscape(sid)
}

func validSlug(slug string) bool {
	if len(slug) < 2 || len(slug) > 32 || slug[0] < 'a' || slug[0] > 'z' {
		return false
	}
	for _, c := range slug {
		if c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	switch slug {
	case "main", "health", "assistant", "guest", "spaces", "tracks", "new":
		return false
	}
	return true
}
