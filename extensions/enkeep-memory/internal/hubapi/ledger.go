package hubapi

import (
	"context"
	"errors"
	"net/url"
	"time"
)

// The hub's entry ledger (unified memory design v2 §5.1, appendix D interface I1): one append-only list of entries the
// user said, relayed or confirmed, and what agents and extensions published. It is not on any hub yet: the fields
// below are the design's, and a hub without the route, or a token without the scope, is skipped without a word
// (LedgerReader's callers treat every error as "no ledger").
//
// The route the extension reads: GET /api/v1/ledger?by=user&since=<RFC 3339>&limit=…&cursor=…, answering
// {"items": [LedgerEntry…], "next_cursor": "…"}.

// LedgerEntry is one ledger entry as the extension reads it.
type LedgerEntry struct {
	ID   string `json:"id"`
	Type string `json:"type"` // fact | preference | rule | decision | hot_state
	Text string `json:"text"`
	// Quote is the user's words verbatim, verified by the hub against the source it names (user-role, not injected).
	Quote string `json:"quote,omitempty"`
	By    string `json:"by"`              // user | agent | extension
	Trust string `json:"trust,omitempty"` // user_verbatim | user_relayed | inferred | agent | repo | import
	// Source is where the quote was said: a session and item, a card, a to-do.
	Source string `json:"source,omitempty"`
	// RelayedVia is the session that relayed the user's words (a supervisor's), when they reached the hub through
	// it; the quote is still checked against the user's own message in Source.
	RelayedVia string `json:"relayed_via,omitempty"`
	// Supersedes is the id of the older entry this one replaces (newer points at older).
	Supersedes string    `json:"supersedes,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	Tags       []string  `json:"tag,omitempty"`
	Scope      struct {
		Executor string `json:"executor,omitempty"` // assistant | supervisor | agent | any
		Machine  string `json:"machine,omitempty"`
		Project  string `json:"project,omitempty"`
		Personal bool   `json:"personal,omitempty"`
	} `json:"scope"`
	// Origin is the session's origin when the entry came from one (user | channel | inbox | task | spawned |
	// automation): automation and test probes never teach anything about the user.
	Origin string    `json:"origin,omitempty"`
	Kind   string    `json:"kind,omitempty"` // "probe": a test probe (never read as the user's)
	At     time.Time `json:"created_at"`
	// replaced is set by Ledger when a later entry of the list supersedes this one.
	replaced bool
}

// Superseded says whether a later entry of the list read replaces this one.
func (e LedgerEntry) Superseded() bool { return e.replaced }

// Expired says whether the entry no longer holds at now.
func (e LedgerEntry) Expired(now time.Time) bool {
	return !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt)
}

// LedgerQuery selects ledger entries.
type LedgerQuery struct {
	By    string    // user | agent | extension; "" all
	Since time.Time // zero: from the start
}

// LedgerReader reads the ledger. It is a separate interface, not part of Hub: a Hub that cannot read it (None,
// Offline, the tests' fakes) simply does not have the method.
type LedgerReader interface {
	Ledger(ctx context.Context, q LedgerQuery) ([]LedgerEntry, error)
}

// ledgerMost bounds one read: pages of ledgerPage, at most this many entries.
const (
	ledgerPage = 500
	ledgerMost = 20000
)

// Ledger reads the ledger's entries (every page, up to ledgerMost), marks the ones a later entry supersedes, and
// leaves out probes, automation and expired entries. A hub without the route is a *WaitingHub{"I1"}.
func (h *HTTP) Ledger(ctx context.Context, q LedgerQuery) ([]LedgerEntry, error) {
	var all []LedgerEntry
	cursor := ""
	for len(all) < ledgerMost {
		v := url.Values{"limit": {"500"}}
		if q.By != "" {
			v.Set("by", q.By)
		}
		if !q.Since.IsZero() {
			v.Set("since", q.Since.UTC().Format(time.RFC3339))
		}
		if cursor != "" {
			v.Set("cursor", cursor)
		}
		var r struct {
			Items []LedgerEntry `json:"items"`
			Next  string        `json:"next_cursor"`
		}
		if err := h.get(ctx, "ledger", "/api/v1/ledger", v, &r); err != nil {
			if errors.Is(err, errNoRoute) {
				return nil, &WaitingHub{Change: "I1"}
			}
			return nil, err
		}
		all = append(all, r.Items...)
		if r.Next == "" || len(r.Items) == 0 {
			break
		}
		cursor = r.Next
	}
	return ledgerClean(all, time.Now()), nil
}

// ledgerClean marks superseded entries and drops probes, automation and expired entries.
func ledgerClean(all []LedgerEntry, now time.Time) []LedgerEntry {
	replaced := map[string]bool{}
	for _, e := range all {
		if e.Supersedes != "" {
			replaced[e.Supersedes] = true
		}
	}
	var out []LedgerEntry
	for _, e := range all {
		if e.Kind == "probe" || e.Origin == "automation" || e.Expired(now) {
			continue
		}
		e.replaced = replaced[e.ID]
		out = append(out, e)
	}
	return out
}

var _ LedgerReader = (*HTTP)(nil)

// LedgerCleanForTest is ledgerClean at the current time, for other packages' tests.
func LedgerCleanForTest(all []LedgerEntry) []LedgerEntry { return ledgerClean(all, time.Now()) }
