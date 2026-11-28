package gcp

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
)

// Token-shaped text a vendor message must never carry on: OAuth access tokens, JWTs and bearer headers.
var tokenRe = regexp.MustCompile(`ya29\.[A-Za-z0-9._\-]+|eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]*|(?i:bearer)\s+\S+`)

// scrub is s without any credential the adapter holds or has been given.
func (a *adapter) scrub(s string) string {
	s = a.redact.Redact(s)
	for _, k := range a.keys {
		if tok := k.current(); len(tok) >= 6 {
			s = strings.ReplaceAll(s, tok, "[redacted]")
		}
	}
	return tokenRe.ReplaceAllString(s, "[redacted]")
}

// apiError is a Compute Engine error answer as a coded error.
func (a *adapter) apiError(loc string, resp *core.Response) error {
	var body struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
			Errors  []struct {
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(resp.Body, &body)
	reasons := []string{body.Error.Status}
	msg := body.Error.Message
	for _, e := range body.Error.Errors {
		reasons = append(reasons, e.Reason)
		if msg == "" {
			msg = e.Message
		}
	}
	return classify(loc, resp.Status, reasons, a.scrub(core.OneLine(msg, 0)))
}

// opError is the error an Operation answered at once carries (nil when it has none). An operation exists, so Google
// took the request: whatever its error says (a rate limit included), it is never transient.
func (a *adapter) opError(loc string, op *gOperation) error {
	if op.Error == nil || len(op.Error.Errors) == 0 {
		return nil
	}
	var reasons []string
	msg := ""
	for _, e := range op.Error.Errors {
		reasons = append(reasons, e.Code)
		if msg == "" {
			msg = e.Message
		}
	}
	status := op.HTTPErrorStatusCode
	if status == 0 {
		status = http.StatusBadRequest
	}
	e := classify(loc, status, reasons, a.scrub(core.OneLine(msg, 0)))
	e.Transient = false
	return e
}

func has(reasons []string, msg string, words ...string) bool {
	lmsg := strings.ToLower(msg)
	for _, w := range words {
		lw := strings.ToLower(w)
		for _, r := range reasons {
			if strings.EqualFold(r, w) || (strings.HasSuffix(lw, "*") && strings.HasPrefix(strings.ToLower(r), strings.TrimSuffix(lw, "*"))) {
				return true
			}
		}
		if strings.HasPrefix(lw, "~") && strings.Contains(lmsg, lw[1:]) {
			return true
		}
	}
	return false
}

// classify maps Google's status, reasons (error reasons, a status name, operation error codes) and message to the
// hub's codes, in loc for the words the adapter writes itself. Only a rate limit is transient (core.RateLimited):
// Google refused it before doing anything.
func classify(loc string, status int, reasons []string, msg string) *core.Error {
	// own is the adapter's words, already in loc; the vendor's message wins when there is one.
	text := func(own string) string {
		if msg == "" {
			return vendor + ": " + own
		}
		return vendor + ": " + msg
	}
	stockout := has(reasons, msg, "~stockout", "~resource pool", "~enough resources", "~zone_resource_pool_exhausted")
	switch {
	case has(reasons, msg, "ZONE_RESOURCE_POOL_EXHAUSTED*", "QUOTA_EXCEEDED", "quotaExceeded", "~does not have enough resources",
		"~zone_resource_pool_exhausted", "~quota_exceeded") ||
		(has(reasons, msg, "RESOURCE_EXHAUSTED") && stockout):
		return core.Capacity("%s", text(core.T(loc, "there is no capacity or quota for this VM right now", "目前没有足够的容量或配额来运行这台虚拟机。")))
	case status == http.StatusTooManyRequests || has(reasons, msg, "rateLimitExceeded", "userRateLimitExceeded",
		"RATE_LIMIT_EXCEEDED", "RESOURCE_EXHAUSTED"):
		return core.RateLimited("%s", text(core.T(loc, "too many requests; try again shortly", "请求过多，请稍后再试。")))
	case status == http.StatusNotFound || has(reasons, msg, "notFound", "NOT_FOUND", "RESOURCE_NOT_FOUND"):
		return core.NotFound("%s", text(core.T(loc, "no such VM", "没有这台虚拟机。")))
	case status == http.StatusUnauthorized || status == http.StatusForbidden ||
		has(reasons, msg, "authError", "forbidden", "accessNotConfigured", "insufficientPermissions",
			"UNAUTHENTICATED", "PERMISSION_DENIED", "invalid_grant"):
		return core.NotSignedIn("%s", text(core.T(loc, "the service account may not do this", "服务账号没有执行这个操作的权限。")))
	case (status == http.StatusBadRequest || status == http.StatusPreconditionFailed || status == http.StatusConflict) &&
		has(reasons, msg, "resourceNotReady", "RESOURCE_NOT_READY", "failedPrecondition", "FAILED_PRECONDITION",
			"conditionNotMet", "CONDITION_NOT_MET", "~is not ready", "~not ready"):
		return core.InvalidState("%s", text(core.T(loc, "the VM cannot do this in its current state", "虚拟机当前的状态不能执行这个操作。")))
	case has(reasons, msg, "alreadyExists", "ALREADY_EXISTS", "RESOURCE_ALREADY_EXISTS"):
		// A snapshot (or other resource) of that name is there already: nothing to do with the VM's state.
		return core.Refused("%s", text(core.T(loc, "a resource of that name already exists", "同名的资源已经存在。")))
	case status == http.StatusPreconditionFailed:
		return core.InvalidState("%s", text(core.T(loc, "the VM cannot do this in its current state", "虚拟机当前的状态不能执行这个操作。")))
	}
	return core.StatusError(vendor, status, msg)
}
