package wa

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/connector"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/proto"
	"github.com/nwverse/persona-extensions/extensions/whatsapp/internal/wa/spec"
)

const catalogSource = spec.ID

// Catalog exposes all read-only WhatsApp tools to Persona's source subscriptions.
func (c *Conn) Catalog(context.Context) ([]connector.CatalogSource, error) {
	tools := c.Tools()
	out := make([]connector.CatalogTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, connector.CatalogTool{
			Name:        strings.TrimPrefix(t.Name, spec.ID+"_"),
			Description: t.Description,
			InputSchema: t.InputSchema,
			ReadOnly:    true,
		})
	}
	return []connector.CatalogSource{{ID: catalogSource, Name: c.Name(), Tools: out}}, nil
}

// CatalogCall executes one read-only catalog tool; read_messages and search_messages return
// subscription-friendly item records with globally unique `<chat>:<id>` identifiers.
func (c *Conn) CatalogCall(ctx context.Context, q connector.CatalogQuery) (string, error) {
	name := spec.ID + "_" + q.Tool
	if q.Source != catalogSource || !slices.ContainsFunc(c.Tools(), func(t proto.Tool) bool { return t.Name == name }) {
		return "", fmt.Errorf("no tool %s in source %s", q.Tool, q.Source)
	}
	switch name {
	case "whatsapp_read_messages":
		return c.catalogReadMessages(q.Args)
	case "whatsapp_search_messages":
		return c.catalogSearchMessages(q.Args)
	default:
		return c.Call(ctx, name, q.Args)
	}
}

type catalogItem struct {
	ID      string `json:"id"`      // <chat jid>:<message id>
	Thread  string `json:"thread"`  // chat JID
	Title   string `json:"title"`   // chat display name
	Message string `json:"message"` // WhatsApp message ID
	Sender  string `json:"sender,omitempty"`
	Mine    bool   `json:"mine,omitempty"`
	Type    string `json:"type,omitempty"`
	Time    string `json:"time"` // RFC 3339
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to,omitempty"`
}

func (c *Conn) catalogItemOf(ch Chat, m Message) catalogItem {
	mo := c.messageSummary(m)
	return catalogItem{
		ID:      ch.JID + ":" + m.ID,
		Thread:  ch.JID,
		Title:   ch.Name,
		Message: m.ID,
		Sender:  mo.From,
		Mine:    mo.Mine,
		Type:    mo.Type,
		Time:    mo.At,
		Body:    mo.Text,
		ReplyTo: mo.ReplyTo,
	}
}

func (c *Conn) catalogReadMessages(args json.RawMessage) (string, error) {
	if c.store == nil {
		return "", errNotConnected
	}
	a, err := parseToolArgs(args)
	if err != nil {
		return "", err
	}
	since, err := c.parseWhen(a.Since, "since")
	if err != nil {
		return "", err
	}
	until, err := c.parseWhen(a.Until, "until")
	if err != nil {
		return "", err
	}
	ch, err := c.store.ResolveChatInput(a.Chat)
	if err != nil {
		return "", err
	}
	msgs, nextBefore, err := c.store.ReadMessages(ch.JID, a.Before, since, until, bound(a.Limit, messagesDef, messagesMost))
	if err != nil {
		return "", err
	}
	out := struct {
		Chat     chatOut       `json:"chat"`
		More     bool          `json:"more"`
		Before   string        `json:"before,omitempty"`
		Messages []catalogItem `json:"messages"`
	}{
		Chat:     chatRef(ch),
		More:     nextBefore != "",
		Before:   nextBefore,
		Messages: make([]catalogItem, 0, len(msgs)),
	}
	for _, m := range msgs {
		out.Messages = append(out.Messages, c.catalogItemOf(ch, m))
	}
	return jsonOf(out)
}

func (c *Conn) catalogSearchMessages(args json.RawMessage) (string, error) {
	if c.store == nil {
		return "", errNotConnected
	}
	a, err := parseToolArgs(args)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	since, err := c.parseWhen(a.Since, "since")
	if err != nil {
		return "", err
	}
	until, err := c.parseWhen(a.Until, "until")
	if err != nil {
		return "", err
	}
	chatJID := ""
	if strings.TrimSpace(a.Chat) != "" {
		ch, err := c.store.ResolveChatInput(a.Chat)
		if err != nil {
			return "", err
		}
		chatJID = ch.JID
	}
	hits, err := c.store.SearchMessages(a.Query, chatJID, since, until, bound(a.Limit, searchDef, searchMost))
	if err != nil {
		return "", err
	}
	out := struct {
		Query    string        `json:"query"`
		Messages []catalogItem `json:"messages"`
	}{
		Query:    a.Query,
		Messages: make([]catalogItem, 0, len(hits)),
	}
	for _, h := range hits {
		out.Messages = append(out.Messages, c.catalogItemOf(h.Chat, h.Message))
	}
	return jsonOf(out)
}
