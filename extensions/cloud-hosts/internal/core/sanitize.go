package core

import (
	"math"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
)

// The limits of cloud.schema.json: an answer over one of them would fail the hub's schema check as a whole.
const (
	MaxInstances     = 500
	MaxSnapshots     = 200
	maxIPs           = 16
	maxDisplayName   = 200
	maxStateDetail   = 300
	maxPlace         = 80
	maxSize          = 80
	maxArch          = 20
	maxOS            = 80
	maxConsoleURL    = 2048
	maxActionURL     = 4096
	maxMessage       = 300
	maxSnapshotName  = 200
	maxRequestName   = 80  // CloudActionParams.name
	maxAccountID     = 128 // CloudAccountID
	maxInstanceID    = 256 // CloudInstanceID
	maxSnapshotID    = 256 // CloudSnapshotID
	maxRequestID     = 64  // CloudRequestID
	maxListDetail    = 300
	maxCPU           = 4096
	maxStandardKinds = 9
	maxCustomActions = 8
)

// validID reports whether s can be an id on the wire: 1 to max characters, none of them a control character.
func validID(s string, max int) bool {
	return s != "" && utf8.RuneCountInString(s) <= max && utf8.ValidString(s) &&
		!strings.ContainsFunc(s, func(c rune) bool { return c < ' ' || c == 0x7f })
}

// PlainIP is s as a plain IPv4 or IPv6 address ("" when it is not one): no port, no zone, no CIDR suffix, no brackets.
func PlainIP(s string) string {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || a.Zone() != "" || !a.IsValid() || a.Is4In6() || a.IsUnspecified() {
		return "" // an IPv4-mapped IPv6 address is a notation, not an address to reach it by
	}
	return a.String()
}

// PlainIPs are the plain addresses among in, each once, in order, at most 16.
func PlainIPs(in ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		ip := PlainIP(s)
		if ip == "" || seen[ip] {
			continue
		}
		seen[ip] = true
		if out = append(out, ip); len(out) == maxIPs {
			break
		}
	}
	return out
}

// HTTPSURL is s when it is an https address with a host and without credentials, and "" otherwise.
func HTTPSURL(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > max || strings.ContainsFunc(s, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return ""
	}
	if out := u.String(); len(out) <= max { // escaping may have made it longer
		return out
	}
	return ""
}

// wireTime is t in UTC when JSON can carry it (RFC 3339 has years 1 to 9999), and the zero time otherwise: a panel's
// "never expires" in year 9999 of a zone west of UTC would otherwise fail the whole answer.
func wireTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	t = t.UTC()
	if y := t.Year(); y < 1 || y > 9999 {
		return time.Time{}
	}
	return t
}

// finite is f when it is a number within [0, max], and 0 otherwise.
func finite(f, max float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > max {
		return 0
	}
	return f
}

// cleanInstance holds an adapter's instance to the schema: the name it was asked by, a known state, plain addresses,
// an https console link without credentials, known actions (never custom, never twice) and only the custom actions
// the manifest declares, text on one line within its length, and no credential anywhere.
func (p *Provider) cleanInstance(in wire.CloudInstance) wire.CloudInstance {
	out := in
	out.DisplayName = OneLine(p.redact.Redact(in.DisplayName), maxDisplayName)
	if !in.State.Valid() {
		out.State = wire.CloudInstanceStateUnknown
	}
	out.StateDetail = OneLine(p.redact.Redact(in.StateDetail), maxStateDetail)
	out.Region = OneLine(p.redact.Redact(in.Region), maxPlace)
	out.Zone = OneLine(p.redact.Redact(in.Zone), maxPlace)
	out.PublicIps = PlainIPs(in.PublicIps...)
	out.PrivateIps = PlainIPs(in.PrivateIps...)
	if in.Spec != nil {
		s := *in.Spec
		s.Size, s.Arch, s.OS = OneLine(p.redact.Redact(s.Size), maxSize), OneLine(p.redact.Redact(s.Arch), maxArch), OneLine(p.redact.Redact(s.OS), maxOS)
		s.Cpu = finite(s.Cpu, maxCPU)
		if s.MemoryMb < 0 {
			s.MemoryMb = 0
		}
		if s.DiskGb < 0 {
			s.DiskGb = 0
		}
		out.Spec = nil
		if s != (wire.CloudSpec{}) {
			out.Spec = &s
		}
	}
	if in.Lifecycle != nil {
		l := *in.Lifecycle
		l.ExpiresAt, l.RenewsAt, l.KeepaliveAt = wireTime(l.ExpiresAt), wireTime(l.RenewsAt), wireTime(l.KeepaliveAt)
		out.Lifecycle = nil
		if l != (wire.CloudLifecycle{}) {
			out.Lifecycle = &l
		}
	}
	out.ConsoleURL = HTTPSURL(in.ConsoleURL, maxConsoleURL)
	if p.redact.Redact(out.ConsoleURL) != out.ConsoleURL {
		out.ConsoleURL = ""
	}
	out.Actions = nil
	seen := map[wire.CloudActionKind]bool{}
	for _, a := range in.Actions {
		if !a.Valid() || a == wire.CloudActionKindCustom || seen[a] || len(out.Actions) == maxStandardKinds {
			continue
		}
		if a == wire.CloudActionKindConsole && out.ConsoleURL == "" && p.consoler == nil {
			continue // nothing to open
		}
		seen[a] = true
		out.Actions = append(out.Actions, a)
	}
	out.CustomActions = nil
	seenCustom := map[wire.CloudCustomActionID]bool{}
	for _, id := range in.CustomActions {
		if _, ok := p.custom[id]; !ok || seenCustom[id] || len(out.CustomActions) == maxCustomActions {
			continue
		}
		seenCustom[id] = true
		out.CustomActions = append(out.CustomActions, id)
	}
	if out.ObservedAt = wireTime(in.ObservedAt); out.ObservedAt.IsZero() {
		out.ObservedAt = p.now().UTC()
	}
	out.ObservedAt = out.ObservedAt.Truncate(time.Second)
	return out
}

func (p *Provider) cleanSnapshot(in wire.CloudSnapshot) wire.CloudSnapshot {
	out := in
	out.Name = OneLine(p.redact.Redact(in.Name), maxSnapshotName)
	if !in.State.Valid() {
		out.State = wire.CloudSnapshotStateCreating
	}
	out.SizeGb = finite(in.SizeGb, math.MaxFloat64)
	out.CreatedAt = wireTime(in.CreatedAt)
	return out
}

// cleanSnapshots are the snapshots with an id, each once, newest first, at most 200.
func (p *Provider) cleanSnapshots(in []wire.CloudSnapshot) []wire.CloudSnapshot {
	out := make([]wire.CloudSnapshot, 0, len(in))
	seen := map[wire.CloudSnapshotID]bool{}
	for _, s := range in {
		if !validID(string(s.ID), maxSnapshotID) || seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		out = append(out, p.cleanSnapshot(s))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > MaxSnapshots {
		out = out[:MaxSnapshots]
	}
	return out
}

func (p *Provider) cleanResult(in wire.CloudActionResult) wire.CloudActionResult {
	out := in
	if !in.Status.Valid() {
		out.Status = wire.CloudActionStatusAccepted
	}
	if in.Instance != nil {
		i := p.cleanInstance(*in.Instance)
		out.Instance = &i
	}
	if in.Snapshot != nil {
		s := p.cleanSnapshot(*in.Snapshot)
		out.Snapshot = &s
		if !validID(string(s.ID), maxSnapshotID) {
			out.Snapshot = nil
		}
	}
	// A console link may be signed, so it is the one answer allowed to carry a short-lived token of the vendor's; the
	// user's own credentials still never appear in it.
	out.URL = HTTPSURL(in.URL, maxActionURL)
	if p.redact.Redact(out.URL) != out.URL {
		out.URL = ""
	}
	out.ExpiresAt = wireTime(in.ExpiresAt)
	if out.URL == "" {
		out.ExpiresAt = time.Time{}
	}
	out.Message = OneLine(p.redact.Redact(in.Message), maxMessage)
	return out
}
