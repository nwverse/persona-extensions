package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type reviewArgs struct {
	Project string `json:"project"`
	Repo    string `json:"repo"`
	ID      int64  `json:"id"`
}

func (in reviewArgs) validate() error {
	if err := validProjectRepo(in.Project, in.Repo); err != nil {
		return err
	}
	if in.ID <= 0 {
		return errors.New("PR ID must be positive")
	}
	return nil
}

func describeAction(name string, raw json.RawMessage) (map[string]any, error) {
	var in reviewArgs
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	var summary string
	switch name {
	case "bitbucket_approve":
		summary = fmt.Sprintf("Approve PR #%d in %s/%s", in.ID, in.Project, in.Repo)
	case "bitbucket_unapprove":
		summary = fmt.Sprintf("Remove approval from PR #%d in %s/%s", in.ID, in.Project, in.Repo)
	default:
		return nil, fmt.Errorf("unknown action %q", name)
	}
	return map[string]any{"action": map[string]any{"name": name, "visible": "owner"}, "summary": summary}, nil
}

func (a *app) doAction(ctx context.Context, requestID, name string, raw json.RawMessage) (map[string]any, error) {
	var in reviewArgs
	if err := decode(raw, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	keyData, _ := json.Marshal(map[string]any{"action": name, "project": in.Project, "repo": in.Repo, "id": in.ID})
	var fn func() (string, error)
	switch name {
	case "bitbucket_approve":
		fn = func() (string, error) {
			if err := a.client.approve(ctx, in.Project, in.Repo, in.ID); err != nil {
				return "", err
			}
			return fmt.Sprintf("Approved PR #%d", in.ID), nil
		}
	case "bitbucket_unapprove":
		fn = func() (string, error) {
			if err := a.client.unapprove(ctx, in.Project, in.Repo, in.ID); err != nil {
				return "", err
			}
			return fmt.Sprintf("Removed approval from PR #%d", in.ID), nil
		}
	default:
		return nil, fmt.Errorf("unknown action %q", name)
	}
	text, err := a.receipts.commit(requestID, string(keyData), fn)
	if err != nil {
		return nil, err
	}
	a.record(nil)
	if a.hub != nil {
		_ = a.hub.Invalidate(pageUI)
	}
	return map[string]any{"text": text}, nil
}

var _ = strings.TrimSpace
