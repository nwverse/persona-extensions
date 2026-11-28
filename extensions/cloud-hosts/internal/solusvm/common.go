// Package solusvm speaks for SolusVM panels, the control panels of many inexpensive KVM and NAT VPS hosts: the
// SolusVM 1 client API (one key and hash per server, NewV1) and the SolusVM 2 REST API (a bearer token per panel
// login, NewV2).
package solusvm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The budgets of the calls, a little under the hub's own bounds (20 s, 15 s and 30 s).
const (
	listBudget   = 18 * time.Second
	readBudget   = 13 * time.Second
	actionBudget = 27 * time.Second
	parallel     = 8 // accounts or servers read at once
)

// cgnat is the shared address space carriers and NAT VPS hosts use (RFC 6598): not reachable from outside.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// isPrivate reports whether ip (a plain address) is one only its own network reaches.
func isPrivate(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
}

// splitIPs are the plain addresses of in, in order, as public and private ones.
func splitIPs(in []string) (public, private []string) {
	for _, ip := range core.PlainIPs(in...) {
		if isPrivate(ip) {
			private = append(private, ip)
		} else {
			public = append(public, ip)
		}
	}
	return public, private
}

// each runs fn for 0..n-1, at most parallel at once, and waits for all of them.
func each(n int, fn func(i int)) {
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

// budget is ctx bounded to d.
func budget(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

// flexString is a JSON string, number or boolean as text (panels are not consistent about which they send).
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "null":
		*f = ""
	case strings.HasPrefix(s, `"`):
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = flexString(v)
	case strings.HasPrefix(s, "{") || strings.HasPrefix(s, "["):
		*f = ""
	default:
		*f = flexString(s)
	}
	return nil
}

// flexNum is a JSON number or a numeric string (0 when it is neither, and for NaN or an infinity, which JSON cannot
// carry).
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	var s flexString
	if err := s.UnmarshalJSON(b); err != nil {
		return err
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(s)), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		v = 0
	}
	*f = flexNum(v)
	return nil
}

// withConsole is actions with console added when there is a link to open.
func withConsole(actions []wire.CloudActionKind, consoleURL string) []wire.CloudActionKind {
	if consoleURL != "" {
		actions = append(actions, wire.CloudActionKindConsole)
	}
	return actions
}

// hasAny reports whether s contains any of words.
func hasAny(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// allDigits reports whether s is a non-empty decimal number of at most 20 digits.
func allDigits(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// validPin says whether a configured certificate pin is a SHA-256 fingerprint as a browser, openssl ("SHA256
// Fingerprint=AB:CD…") or a panel shows it: 64 hex digits once the label and separators are gone. A pin that is
// not would be ignored by core.NewHTTPClient, leaving the user believing the certificate is pinned.
func validPin(s string) bool {
	s = strings.NewReplacer("sha256 fingerprint", "", "sha-256", "", "sha256", "", ":", "", " ", "", "=", "").
		Replace(strings.ToLower(strings.TrimSpace(s)))
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
