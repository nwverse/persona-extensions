package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
)

// The hub's REST API with the extension's own token (scopes todos.read, todos.write and notify), and the node
// program through nodes.call.

type decisionPayload struct {
	Question string `json:"question"`
	Choice   string `json:"choice"`
	Reason   string `json:"reason,omitempty"`
	Ref      string `json:"ref"`
	// Source is always "outcome": the only kind an extension may write (said explicitly, not left to the default).
	Source string `json:"source"`
}

// delivery is the hub's answer to a notification: sent, or held by push grading, or not sent and why.
type delivery struct {
	Sent   bool   `json:"sent"`
	Held   bool   `json:"held"`
	Reason string `json:"reason"`
}

type notice struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	ID        string `json:"id,omitempty"`
	PushLevel string `json:"push_level,omitempty"`
}

type hubAPI interface {
	Todos(ctx context.Context) ([]todoRow, error)
	// Todo returns nil when the hub has no such to-do (or does not show it to this extension).
	Todo(ctx context.Context, id string) (*todoRow, error)
	// AddDecision writes an outcome decision; inserted is false when one with the same ref was there.
	AddDecision(ctx context.Context, todoID string, d decisionPayload) (bool, error)
	Notify(ctx context.Context, n notice) (delivery, error)
}

// nodeCaller calls the node program on a computer.
type nodeCaller interface {
	Call(ctx context.Context, machine, method string, params, out any) error
}

type rpcNodes struct{ hub *ext.Hub }

func (n rpcNodes) Call(ctx context.Context, machine, method string, params, out any) error {
	return n.hub.Call(ctx, "nodes.call", map[string]any{"machine": machine, "method": method, "params": params}, out)
}

var errNotFound = errors.New("not found")

type hubClient struct {
	base, token string
	client      *http.Client
}

func newHubClient(base, token string) *hubClient {
	return &hubClient{base: strings.TrimRight(base, "/"), token: token, client: &http.Client{Timeout: 20 * time.Second}}
}

func (c *hubClient) do(ctx context.Context, method, path string, body, out any) error {
	if c.base == "" || c.token == "" {
		return errors.New("the hub gave no API address or token (permissions.hub not granted)")
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Code == "" {
			e.Code = fmt.Sprintf("http_%d", resp.StatusCode)
		}
		// The route without its ids, the hub's code and its words: never a body or a token.
		return fmt.Errorf("%s %s: %s %s", method, routeOf(path), e.Code, clip(e.Error, 200))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// routeOf is the path with the to-do id left out, for errors and logs.
func routeOf(path string) string {
	path = strings.SplitN(path, "?", 2)[0]
	if rest, ok := strings.CutPrefix(path, "/api/v1/todos/"); ok {
		_, tail, _ := strings.Cut(rest, "/")
		if tail == "" {
			return "/api/v1/todos/{id}"
		}
		return "/api/v1/todos/{id}/" + tail
	}
	return path
}

func (c *hubClient) Todos(ctx context.Context) ([]todoRow, error) {
	var out struct {
		Todos []todoRow `json:"todos"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/todos", nil, &out)
	return out.Todos, err
}

func (c *hubClient) Todo(ctx context.Context, id string) (*todoRow, error) {
	var out struct {
		Todo *todoRow `json:"todo"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/todos/"+url.PathEscape(id), nil, &out)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return out.Todo, err
}

func (c *hubClient) AddDecision(ctx context.Context, todoID string, d decisionPayload) (bool, error) {
	var out struct {
		Inserted *bool `json:"inserted"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/todos/"+url.PathEscape(todoID)+"/decisions", d, &out); err != nil {
		return false, err
	}
	return out.Inserted == nil || *out.Inserted, nil
}

func (c *hubClient) Notify(ctx context.Context, n notice) (delivery, error) {
	var d delivery
	err := c.do(ctx, http.MethodPost, "/api/v1/notify", n, &d)
	return d, err
}
