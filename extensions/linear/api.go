package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const apiURL = "https://api.linear.app/graphql"

var (
	errToken       = errors.New("Linear rejected the personal API key; replace it in extension settings")
	errUnavailable = errors.New("Linear could not be reached; try again later")
	errNotFound    = errors.New("Linear could not find that issue")
	errRateLimited = errors.New("Linear rate limit reached; try again later")
)

type client struct {
	url, key string
	http     *http.Client
}

func newClient(url, key string) *client {
	return &client{url: url, key: strings.TrimSpace(key), http: &http.Client{
		Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *client) call(ctx context.Context, query string, variables map[string]any, out any) error {
	if c.key == "" {
		return errors.New("Linear personal API key is not configured")
	}
	data, err := json.Marshal(graphRequest{Query: query, Variables: variables})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errUnavailable, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20+1))
	if err != nil {
		return errUnavailable
	}
	if len(body) > 4<<20 {
		return errors.New("Linear returned an oversized response")
	}
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errToken
	case http.StatusNotFound:
		return errNotFound
	case http.StatusTooManyRequests:
		return errRateLimited
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Linear returned HTTP %d", res.StatusCode)
	}
	var envelope graphResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return errors.New("Linear returned invalid JSON")
	}
	if len(envelope.Errors) > 0 {
		return errors.New("Linear rejected the operation: " + clipText(envelope.Errors[0].Message, 200))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return errors.New("Linear returned an unexpected response shape")
	}
	return nil
}

const assignedQuery = `query Assigned($after: String) {
  viewer {
    assignedIssues(first: 50, after: $after, filter: { state: { type: { nin: ["completed", "canceled"] } } }, orderBy: updatedAt) {
      nodes { id identifier title priority url dueDate updatedAt state { id name type } team { id key name } assignee { id name displayName } project { id name } }
      pageInfo { hasNextPage endCursor }
    }
  }
}`

func (c *client) assigned(ctx context.Context, cursor string) (connection[issue], error) {
	var out struct {
		Viewer viewer `json:"viewer"`
	}
	err := c.call(ctx, assignedQuery, map[string]any{"after": nullable(cursor)}, &out)
	return out.Viewer.AssignedIssues, err
}

const issueQuery = `query Issue($id: String!) {
  issue(id: $id) {
    id identifier title description priority url dueDate createdAt updatedAt
    state { id name type } team { id key name } assignee { id name displayName } project { id name }
    comments(first: 50) { nodes { id body createdAt user { id name displayName } } }
  }
}`

func (c *client) issue(ctx context.Context, id string) (issue, error) {
	var out struct {
		Issue *issue `json:"issue"`
	}
	err := c.call(ctx, issueQuery, map[string]any{"id": id}, &out)
	if err == nil && out.Issue == nil {
		return issue{}, errNotFound
	}
	if out.Issue == nil {
		return issue{}, err
	}
	return *out.Issue, err
}

const teamsQuery = `query Teams { teams(first: 100) { nodes { id key name states(first: 100) { nodes { id name type position } } } } }`

func (c *client) teams(ctx context.Context) ([]team, error) {
	var out struct {
		Teams connection[team] `json:"teams"`
	}
	err := c.call(ctx, teamsQuery, nil, &out)
	return out.Teams.Nodes, err
}

func (c *client) create(ctx context.Context, input map[string]any) (issue, error) {
	const mutation = `mutation Create($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier title url } } }`
	var out struct {
		Create struct {
			Success bool  `json:"success"`
			Issue   issue `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := c.call(ctx, mutation, map[string]any{"input": input}, &out); err != nil {
		return issue{}, err
	}
	if !out.Create.Success {
		return issue{}, errors.New("Linear did not create the issue")
	}
	return out.Create.Issue, nil
}

func (c *client) update(ctx context.Context, id string, input map[string]any) error {
	const mutation = `mutation Update($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }`
	var out struct {
		Update struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := c.call(ctx, mutation, map[string]any{"id": id, "input": input}, &out); err != nil {
		return err
	}
	if !out.Update.Success {
		return errors.New("Linear did not update the issue")
	}
	return nil
}

func (c *client) comment(ctx context.Context, id, body string) error {
	const mutation = `mutation Comment($input: CommentCreateInput!) { commentCreate(input: $input) { success } }`
	var out struct {
		Comment struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	if err := c.call(ctx, mutation, map[string]any{"input": map[string]string{"issueId": id, "body": body}}, &out); err != nil {
		return err
	}
	if !out.Comment.Success {
		return errors.New("Linear did not add the comment")
	}
	return nil
}

func nullable(text string) any {
	if text == "" {
		return nil
	}
	return text
}

func clipText(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:max(0, limit-1)]) + "…"
	}
	return text
}
