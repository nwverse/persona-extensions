package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

type pageState struct {
	Mode      string `json:"m,omitempty"`
	TeamID    string `json:"t,omitempty"`
	ChannelID string `json:"c,omitempty"`
	Before    string `json:"b,omitempty"`
	Page      int    `json:"p,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.Page < 0 || len(st.TeamID) > 64 || len(st.ChannelID) > 64 || len(st.Before) > 64 || len(st.Mode) > 30 {
		return pageState{}, errors.New("invalid page state")
	}
	return st, nil
}

func (a *app) render(ctx context.Context, r *ext.UIRequest) (*wire.View, error) {
	st, err := readState(r)
	if err != nil {
		return nil, err
	}
	return a.page(ctx, r, st)
}

func (a *app) action(ctx context.Context, r *ext.UIRequest) (*wire.ActionResult, error) {
	st, err := readState(r)
	if err != nil {
		return nil, err
	}
	switch r.Action {
	case "team":
		if !validID(r.Values["team_id"]) {
			return nil, errors.New("invalid team ID")
		}
		st = pageState{Mode: "channels", TeamID: r.Values["team_id"]}
	case "channel":
		if !validID(r.Values["channel_id"]) {
			return nil, errors.New("invalid channel ID")
		}
		st.Mode, st.ChannelID, st.Before, st.Page = "posts", r.Values["channel_id"], "", 0
	case "more":
		if !validID(r.Values["before"]) || r.Values["before"] != st.Before {
			return nil, errors.New("invalid previous post ID")
		}
	case "back":
		st.Mode, st.ChannelID, st.Before = "", "", ""
	default:
		return nil, fmt.Errorf("unknown page action %q", r.Action)
	}
	v, err := a.page(ctx, r, st)
	return &wire.ActionResult{View: v}, err
}

func (a *app) page(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if a.client.base == "" || a.client.token == "" {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingConfig}}}}}, nil
	}
	if st.Mode == "channels" {
		items, err := a.client.channels(ctx, st.TeamID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.channels}
		for _, item := range items {
			if !validID(item.ID) || item.DeleteAt != 0 {
				continue
			}
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "channel-" + item.ID, Title: clipText(channelName(item), 1800), Subtitle: item.Type, Action: &wire.Action{ID: "channel", Values: map[string]string{"channel_id": item.ID}}})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noChannels})
		}
		return &wire.View{Title: t.channels, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode == "posts" {
		page, err := a.client.posts(ctx, st.ChannelID, st.Before, st.Page)
		a.record(err)
		if err != nil {
			return nil, err
		}
		users := map[string]user{}
		section := wire.Section{Title: t.messages}
		for _, id := range page.Order {
			post, ok := page.Posts[id]
			if !ok || post.DeleteAt != 0 {
				continue
			}
			author, err := a.postAuthor(ctx, users, post.UserID)
			if err != nil {
				return nil, err
			}
			section.Items = append(section.Items, textComponents(clipText(author+": "+post.Message, 10000))...)
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noMessages})
		}
		if page.PrevPostID != "" {
			st.Before = page.PrevPostID
			section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"before": st.Before}}}
		}
		return &wire.View{Title: t.messages, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode != "" {
		return nil, errors.New("invalid inbox view")
	}
	items, err := a.unreadChannels(ctx)
	a.record(err)
	if err != nil {
		return nil, err
	}
	section := wire.Section{Title: t.inbox}
	for _, item := range items {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "channel-" + item.Channel.ID, Title: clipText(channelName(item.Channel), 1800), Subtitle: fmt.Sprintf(t.unread, item.Unread.MessageCount, item.Unread.MentionCount), Action: &wire.Action{ID: "channel", Values: map[string]string{"channel_id": item.Channel.ID}}})
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noUnread})
	}
	return &wire.View{Title: t.inbox, State: ext.EncodeState(st), Sections: []wire.Section{section}}, nil
}

func textComponents(text string) []wire.Component {
	var out []wire.Component
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > 0 {
			n := min(len(r), 1800)
			out = append(out, wire.Component{Type: wire.ComponentText, Text: string(r[:n])})
			r = r[n:]
			if len(out) >= 220 {
				out[len(out)-1].Text = clipText(out[len(out)-1].Text, 1700) + " […]"
				return out
			}
		}
	}
	if len(out) == 0 {
		out = append(out, wire.Component{Type: wire.ComponentText, Text: " ", Style: wire.TextStyleSecondary})
	}
	return out
}
