package main

// The secret provider (contributes.secret_providers "vault", scheme op; Persona's docs/extensions/secret-providers.md).
// Persona's manager, and only it, asks this program to name a reference in words (secrets.describe), for the value of
// one when a setting that holds it is used or the user approved handing it to one Agent run (secrets.resolve), and
// tells it of each use (secrets.used), which the connection page lists. A value goes back in the answer to the
// manager's call and nowhere else: never in a log line, an error, the page or the assistant's tools, never kept.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	op "github.com/1password/onepassword-sdk-go"
	"github.com/nwverse/persona/sdk/go/ext"
)

const (
	providerID = "vault"
	// mostUses is how many uses the page lists, newest first; Persona's own audit keeps every one.
	mostUses = 20
	// describeDetailMost is where Persona cuts a description's detail (secretDetailMost): the vault's name.
	describeDetailMost = 40
	describeTitleMost  = 200
	// secretTimeout stays under the manager's 10 seconds a call, so the answer says why rather than the manager timing out.
	secretTimeout = 9 * time.Second
)

// The secrets namespace's messages, as Persona's wire package names them (wire.SecretPurpose, wire.SecretsDescribeParams,
// wire.SecretsResolveParams, wire.SecretsUsedEvent); the SDK copied into this repository predates them.
type secretPurpose struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Machine string `json:"machine,omitempty"`
	Session string `json:"session,omitempty"`
}

type secretsParams struct {
	Contribution string        `json:"contribution"`
	Reference    string        `json:"reference"`
	Purpose      secretPurpose `json:"purpose"`
	At           time.Time     `json:"at"`
	ApprovedBy   string        `json:"approved_by"`
}

// secretUse is one use the manager told of (secrets.used): never a value.
type secretUse struct {
	Reference  string
	Purpose    secretPurpose
	At         time.Time
	ApprovedBy string
}

func (a *app) secrets(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var in secretsParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, &ext.Error{Code: "invalid", Message: "invalid secrets parameters"}
		}
	}
	if in.Contribution != "" && in.Contribution != providerID {
		return nil, ext.NoContribution(in.Contribution)
	}
	switch method {
	case "describe":
		ref, ok := parseReference(in.Reference)
		if !ok {
			return nil, &ext.Error{Code: "invalid", Message: "not a 1Password reference (op://vault/item/field)"}
		}
		ctx, cancel := context.WithTimeout(ctx, secretTimeout)
		defer cancel()
		title, detail := a.describe(ctx, ref)
		return map[string]string{"title": title, "detail": detail}, nil
	case "resolve":
		ctx, cancel := context.WithTimeout(ctx, secretTimeout)
		defer cancel()
		value, err := a.resolve(ctx, in.Reference)
		if err != nil {
			return nil, err
		}
		return map[string]string{"value": value}, nil
	case "used": // an event: no answer goes back
		a.noteUse(in)
		return nil, nil
	}
	return nil, ext.UnknownMethod("secrets." + method)
}

// reference is an op:// reference's parts: op://vault/item/field or op://vault/item/section/field, each a name or an
// ID, and an optional query (?attribute=otp) that only resolving reads.
type reference struct {
	vault, item, section, field string
}

func parseReference(s string) (reference, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "op://")
	if !ok {
		return reference{}, false
	}
	path, _, _ := strings.Cut(rest, "?")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || len(parts) > 4 {
		return reference{}, false
	}
	for i, p := range parts {
		if u, err := url.PathUnescape(p); err == nil {
			p = u
		}
		if p = strings.TrimSpace(p); p == "" {
			return reference{}, false
		}
		parts[i] = p
	}
	r := reference{vault: parts[0], item: parts[1], field: parts[len(parts)-1]}
	if len(parts) == 4 {
		r.section = parts[2]
	}
	return r, true
}

// describe names what a reference points at for people: "item › field", and the vault alone as the detail. The names
// come from the item as 1Password has it (scrubbed of every value, as the tools' are), or from the reference itself when
// it cannot be looked up (not signed in, not found, no answer): Persona shows these, it never decides anything by them.
func (a *app) describe(ctx context.Context, r reference) (title, detail string) {
	title, detail = r.item+" › "+r.field, r.vault
	if vault, item, field, ok := a.lookup(ctx, r); ok {
		title, detail = item+" › "+field, vault
	}
	a.mu.Lock()
	token := a.token
	a.mu.Unlock()
	return oneLine(redact(title, token), describeTitleMost), oneLine(redact(detail, token), describeDetailMost)
}

// lookup finds a reference's vault, item and field by ID or by name (case aside; a name must match one only).
func (a *app) lookup(ctx context.Context, r reference) (vault, item, field string, ok bool) {
	vaults, err := a.listVaults(ctx)
	if err != nil {
		return "", "", "", false
	}
	v, found := pick(vaults, r.vault, func(v vaultInfo) (string, string) { return v.ID, v.Title })
	if !found {
		return "", "", "", false
	}
	items, err := a.listItems(ctx, v.ID)
	if err != nil {
		return "", "", "", false
	}
	it, found := pick(items, r.item, func(it itemInfo) (string, string) { return it.ID, it.Title })
	if !found {
		return "", "", "", false
	}
	api, err := a.client(ctx)
	if err != nil {
		return "", "", "", false
	}
	raw, err := api.getItem(ctx, v.ID, it.ID)
	if a.record(err) != nil {
		return "", "", "", false
	}
	d := itemFromSDK(raw) // its names scrubbed of the item's values; its fields in raw's order
	sections := map[string]op.ItemSection{}
	for _, sec := range raw.Sections {
		sections[sec.ID] = sec
	}
	match := -1
	for i, f := range raw.Fields {
		if r.section != "" {
			sid := sectionOf(f, sections)
			if sid == "" || sid != r.section && !strings.EqualFold(strings.TrimSpace(sections[sid].Title), r.section) {
				continue
			}
		}
		if f.ID == r.field {
			match = i
			break
		}
		if strings.EqualFold(strings.TrimSpace(f.Title), r.field) {
			if match >= 0 {
				return "", "", "", false // two fields of that name: 1Password would not resolve it either
			}
			match = i
		}
	}
	if match < 0 || match >= len(d.Fields) {
		return "", "", "", false
	}
	return clip(redact(v.Title, ""), describeDetailMost), d.Title, d.Fields[match].Title, true
}

// pick is the one entry whose ID is want, else the only one whose name is want (case aside).
func pick[T any](list []T, want string, key func(T) (id, name string)) (T, bool) {
	var zero, named T
	n := 0
	for _, x := range list {
		id, name := key(x)
		if id == want {
			return x, true
		}
		if strings.EqualFold(strings.TrimSpace(name), want) {
			named = x
			n++
		}
	}
	if n == 1 {
		return named, true
	}
	return zero, false
}

// resolve is a reference's value, through the service account. Its errors carry Persona's codes and words in the hub's
// language; none carries the value or the token.
func (a *app) resolve(ctx context.Context, reference string) (string, error) {
	a.mu.Lock()
	t := textsFor(a.locale)
	a.mu.Unlock()
	if _, ok := parseReference(reference); !ok {
		return "", &ext.Error{Code: "refused", Message: t.secretInvalid}
	}
	api, err := a.client(ctx)
	if err != nil {
		return "", a.secretError(err, t)
	}
	value, err := api.resolve(ctx, reference)
	if err != nil {
		a.record(err)
		return "", a.secretError(err, t)
	}
	a.record(nil)
	if value == "" {
		return "", &ext.Error{Code: "refused", Message: t.secretEmpty}
	}
	return value, nil
}

// secretError is how a failed resolution reads to Persona: no token or one 1Password refuses is not_signed_in, its
// rate limit rate_limited (retry_after: as long as the page says so), a vault, item or field it has not not_found, an
// unreadable reference refused; anything else is a failure in 1Password's words, redacted and on one line.
func (a *app) secretError(err error, t *texts) error {
	a.mu.Lock()
	says := ""
	if !errors.Is(err, context.DeadlineExceeded) {
		says = oneLine(redact(err.Error(), a.token), 200)
	}
	a.mu.Unlock()
	switch {
	case errors.Is(err, errNotConfigured):
		return &ext.Error{Code: "not_signed_in", Message: t.secretNoToken}
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New(t.timedOut)
	case kindOf(err) == errAuth:
		return &ext.Error{Code: "not_signed_in", Message: t.refused + ". " + t.refusedGuide}
	case kindOf(err) == errRate:
		return &ext.Error{Code: "rate_limited", Message: t.rateLimited + ". " + t.rateLimitedGuide,
			Data: json.RawMessage(fmt.Sprintf(`{"retry_after":%d}`, int(rateWindow.Seconds())))}
	case notFound(err):
		return &ext.Error{Code: "not_found", Message: fmt.Sprintf(t.secretNotFound, says)}
	case unreadable(err):
		return &ext.Error{Code: "refused", Message: t.secretInvalid}
	}
	return fmt.Errorf(t.says, says)
}

func notFound(err error) bool {
	return hasAny(err, "not found", "cannot be found", "could not find", "couldn't find", "no item", "no vault", "no field",
		"no matching", "does not exist", "doesn't exist", "too many")
}

func unreadable(err error) bool {
	return hasAny(err, "invalid secret reference", "parsing", "invalid reference", "malformed")
}

func hasAny(err error, words ...string) bool {
	msg := strings.ToLower(err.Error())
	for _, w := range words {
		if strings.Contains(msg, w) {
			return true
		}
	}
	return false
}

// noteUse keeps one use for the page, newest first, the last mostUses of them.
func (a *app) noteUse(in secretsParams) {
	if _, ok := parseReference(in.Reference); !ok {
		return
	}
	use := secretUse{Reference: clip(strings.TrimSpace(in.Reference), 300), At: in.At, ApprovedBy: clip(in.ApprovedBy, 80),
		Purpose: secretPurpose{Kind: clip(in.Purpose.Kind, 40), ID: clip(in.Purpose.ID, 120), Machine: clip(in.Purpose.Machine, 120)}}
	a.mu.Lock()
	defer a.mu.Unlock()
	if use.At.IsZero() {
		use.At = a.now()
	}
	a.uses = append([]secretUse{use}, a.uses...)
	if len(a.uses) > mostUses {
		a.uses = a.uses[:mostUses]
	}
}

func (a *app) recentUses() []secretUse {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]secretUse(nil), a.uses...)
}
