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
	Mode     string `json:"m,omitempty"`
	StreamID int64  `json:"s,omitempty"`
	Topic    string `json:"t,omitempty"`
	Anchor   int64  `json:"a,omitempty"`
}

func readState(r *ext.UIRequest) (pageState, error) {
	var st pageState
	if err := r.DecodeState(&st); err != nil || st.StreamID < 0 || st.Anchor < 0 || len(st.Topic) > 600 || len(st.Mode) > 30 {
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
	case "stream":
		st.StreamID, err = parsePositiveID(r.Values["stream_id"])
		if err != nil {
			return nil, err
		}
		st.Mode, st.Topic, st.Anchor = "topics", "", 0
	case "topic":
		st.Mode, st.Topic, st.Anchor = "messages", r.Values["topic"], 0
	case "more":
		st.Anchor, err = parsePositiveID(r.Values["anchor"])
		if err != nil || st.Anchor == 0 {
			return nil, errors.New("invalid older message anchor")
		}
	case "back":
		st.Mode, st.Topic, st.Anchor = "", "", 0
	default:
		return nil, fmt.Errorf("unknown page action %q", r.Action)
	}
	v, err := a.page(ctx, r, st)
	return &wire.ActionResult{View: v}, err
}

func (a *app) page(ctx context.Context, r *ext.UIRequest, st pageState) (*wire.View, error) {
	t := textsFor(r.Locale)
	if a.client.base == "" || a.client.email == "" || a.client.token == "" {
		return &wire.View{Sections: []wire.Section{{Items: []wire.Component{{Type: wire.ComponentEmpty, Title: t.missingConfig}}}}}, nil
	}
	if st.Mode == "topics" {
		items, err := a.client.topics(ctx, st.StreamID)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.topics}
		for _, item := range items {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: "topic-" + item.Name, Title: clipText(item.Name, 1800), Subtitle: fmt.Sprintf(t.latest, item.MaxID), Action: &wire.Action{ID: "topic", Values: map[string]string{"topic": item.Name}}})
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noTopics})
		}
		return &wire.View{Title: t.topics, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode == "messages" {
		page, err := a.client.messages(ctx, st.StreamID, st.Topic, st.Anchor, st.Anchor == 0)
		a.record(err)
		if err != nil {
			return nil, err
		}
		section := wire.Section{Title: t.messages}
		var oldest int64
		for _, item := range page.Messages {
			if oldest == 0 || item.ID < oldest {
				oldest = item.ID
			}
			section.Items = append(section.Items, textComponents(clipText(fmt.Sprintf("%s · %s\n%s", item.SenderFullName, item.Subject, item.Content), 12000))...)
		}
		if len(section.Items) == 0 {
			section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noMessages})
		}
		if !page.FoundOldest && oldest > 0 {
			st.Anchor = oldest
			section.More = &wire.More{Title: t.more, Action: wire.Action{ID: "more", Values: map[string]string{"anchor": fmt.Sprint(oldest)}}}
		}
		return &wire.View{Title: t.messages, State: ext.EncodeState(st), Sections: []wire.Section{section, {Items: []wire.Component{{Type: wire.ComponentButton, Title: t.back, Action: &wire.Action{ID: "back"}}}}}}, nil
	}
	if st.Mode != "" {
		return nil, errors.New("invalid inbox view")
	}
	items, err := a.client.streams(ctx)
	a.record(err)
	if err != nil {
		return nil, err
	}
	section := wire.Section{Title: t.streams}
	for _, item := range items {
		if item.IsArchived {
			continue
		}
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentRow, Key: fmt.Sprintf("stream-%d", item.ID), Title: clipText(item.Name, 1800), Subtitle: clipText(item.Description, 1800), Action: &wire.Action{ID: "stream", Values: map[string]string{"stream_id": fmt.Sprint(item.ID)}}})
	}
	if len(section.Items) == 0 {
		section.Items = append(section.Items, wire.Component{Type: wire.ComponentEmpty, Title: t.noStreams})
	}
	return &wire.View{Title: t.inbox, State: ext.EncodeState(st), Sections: []wire.Section{section}}, nil
}

func parsePositiveID(raw string) (int64, error) {
	var id int64
	if _, err := fmt.Sscan(raw, &id); err != nil || id <= 0 {
		return 0, errors.New("invalid positive ID")
	}
	return id, nil
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
