// Package core is what every vendor adapter of the cloud-hosts extension shares: the Adapter contract, the coded
// error, and the Provider that stands between an adapter and the hub. The Provider is the only place that answers
// cloud.* calls: it treats request_id as an idempotency key, reads addresses back from the vendor for sync_ip, holds
// every answer to the limits of cloud.schema.json and removes credentials from every word that leaves the program.
package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Error is a vendor's failure in the hub's closed vocabulary (wire.CloudErrorCode) with one line for people. It
// never carries a credential: adapters build the message from the vendor's own error text, and the Provider redacts
// it again before it leaves.
type Error struct {
	Code    wire.CloudErrorCode
	Message string
	// Transient marks a failure after which the vendor certainly did not take the request (a rate limit, a connection
	// that was never made): the same request_id may be tried again instead of being answered from the idempotency
	// record. A timeout or a broken connection after the request went out is not transient: the vendor may have
	// acted, so a retry of the same id is answered with the same failure and the instance's state says the rest.
	Transient bool
}

func (e *Error) Error() string { return e.Message }

// Errf is an Error of the given code.
func Errf(code wire.CloudErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// NotFound, Unsupported, InvalidState, NotSignedIn, RateLimited, Capacity, Refused and Failed are the eight codes.
func NotFound(format string, args ...any) *Error {
	return Errf(wire.CloudErrorCodeNotFound, format, args...)
}
func Unsupported(format string, args ...any) *Error {
	return Errf(wire.CloudErrorCodeUnsupported, format, args...)
}
func InvalidState(format string, args ...any) *Error {
	return Errf(wire.CloudErrorCodeInvalidState, format, args...)
}
func NotSignedIn(format string, args ...any) *Error {
	return Errf(wire.CloudErrorCodeNotSignedIn, format, args...)
}
func RateLimited(format string, args ...any) *Error {
	e := Errf(wire.CloudErrorCodeRateLimited, format, args...)
	e.Transient = true
	return e
}
func Capacity(format string, args ...any) *Error {
	return Errf(wire.CloudErrorCodeCapacity, format, args...)
}
func Refused(format string, args ...any) *Error {
	return Errf(wire.CloudErrorCodeRefused, format, args...)
}
func Failed(format string, args ...any) *Error {
	return Errf(wire.CloudErrorCodeFailed, format, args...)
}

// AsError is err as an *Error: one that already is, a cancelled or timed-out context as failed (not transient: the
// vendor may have acted), and anything else as failed with its text.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		if e == nil { // a typed nil that an adapter returned as an error: a failure with nothing to say
			return &Error{Code: wire.CloudErrorCodeFailed, Message: "the provider failed without saying why"}
		}
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &Error{Code: wire.CloudErrorCodeFailed, Message: "the vendor did not answer in time"}
	}
	return &Error{Code: wire.CloudErrorCodeFailed, Message: err.Error()}
}

// Action is one cloud.action as an adapter hears it. Kind is never sync_ip or snapshot_list (the Provider answers
// the first from Adapter.Instance and the second is cloud.snapshots), and console only reaches an adapter that
// implements Consoler.
type Action struct {
	Ref    wire.CloudInstanceRef
	Kind   wire.CloudActionKind
	Custom wire.CloudCustomActionID // the manifest's custom action id when Kind is custom
	Name   string                   // a new snapshot's name (snapshot_create), may be empty
	// RequestID is the hub's idempotency key. The Provider already answers a repeated id from its record; an adapter
	// passes it on where the vendor takes a client token of its own, so a retry after a restart is not done twice.
	RequestID wire.CloudRequestID
	Locale    string
}

// Adapter speaks for one vendor (one cloud_hosts contribution) with the accounts the user set it up with.
//
// An adapter never caches what it answers, never logs, and answers every failure as an *Error with the matching
// code. Its instances carry the account and id it was asked by, the actions the instance takes now (sync_ip and
// console included when it offers them) and the ids of the manifest's custom actions that apply.
type Adapter interface {
	// Instances lists every instance of every account. An account that could not be read is named in problems
	// (account names only, never a credential) and the others are still listed; err is for when nothing could be.
	// An adapter that stops reading at MaxInstances answers that many: the Provider then says the list may be
	// incomplete.
	Instances(ctx context.Context) (list []wire.CloudInstance, problems []string, err error)
	// Instance reads one instance from the vendor now: not_found when it is gone.
	Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error)
	// Action asks for a write (or a custom action). It answers done when the vendor says it has taken effect and
	// accepted when it only took the request; refused with a message when the vendor said no for a reason of its own.
	Action(ctx context.Context, a Action) (wire.CloudActionResult, error)
	// Snapshots lists an instance's snapshots; an adapter without snapshots answers Unsupported.
	Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error)
}

// Consoler is an adapter whose console link is more than the instance's console_url (a signed, short-lived link).
// Without it the Provider answers console with the instance's console_url.
type Consoler interface {
	Console(ctx context.Context, ref wire.CloudInstanceRef, locale string) (wire.CloudActionResult, error)
}

// SecretHolder is an adapter that names the credentials it holds (keys, tokens, key ids), so the Provider can remove
// them from anything that leaves the program.
type SecretHolder interface {
	SecretValues() []string
}

type localeKey struct{}

// WithLocale is ctx carrying the caller's language: the Provider sets it on every call, so an adapter can word a
// state detail with T(Locale(ctx), …).
func WithLocale(ctx context.Context, locale string) context.Context {
	return context.WithValue(ctx, localeKey{}, locale)
}

// Locale is the caller's language of ctx ("" when none was set).
func Locale(ctx context.Context) string {
	l, _ := ctx.Value(localeKey{}).(string)
	return l
}

// zhLocale reports whether locale is a Chinese one.
func zhLocale(locale string) bool {
	l := strings.ToLower(locale)
	return l == "zh" || strings.HasPrefix(l, "zh-") || strings.HasPrefix(l, "zh_")
}

// T is zh for a Chinese locale and en otherwise: the two languages the extension's own words come in. A vendor's
// words are passed on as the vendor wrote them.
func T(locale, en, zh string) string {
	if zh != "" && zhLocale(locale) {
		return zh
	}
	return en
}

// Offers reports whether actions name kind.
func Offers(actions []wire.CloudActionKind, kind wire.CloudActionKind) bool {
	for _, a := range actions {
		if a == kind {
			return true
		}
	}
	return false
}
