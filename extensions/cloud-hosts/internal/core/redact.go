package core

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Redactor removes known credentials from text. Every string the program answers with (an error's message, a state
// detail, a custom action's message) goes through one, so a vendor that echoes a key back cannot leak it.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string // longest first, so a secret that contains another is replaced whole
}

// redactedMark replaces a credential.
const redactedMark = "[redacted]"

// minSecret is the shortest value treated as a credential: anything shorter would blank ordinary words.
const minSecret = 6

// Add registers credentials. Besides each value as given, the forms a vendor may echo it in are registered (URL
// query and path escaping, JSON string escaping), and the lines of a PEM block, so a key pasted as a file is covered
// line by line. Other multi-line values (a configuration file) are registered whole only: their lines hold ordinary
// words such as a region, and the adapter names the credentials among them itself (SecretHolder).
func (r *Redactor) Add(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, s := range r.secrets {
		seen[s] = true
	}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if len(v) < minSecret || seen[v] {
			return
		}
		seen[v] = true
		r.secrets = append(r.secrets, v)
	}
	forms := func(v string) {
		v = strings.TrimSpace(v)
		add(v)
		add(url.QueryEscape(v))
		add(url.PathEscape(v))
		if b, err := json.Marshal(v); err == nil && len(b) > 2 {
			add(string(b[1 : len(b)-1]))
		}
	}
	for _, v := range values {
		forms(v)
		if strings.Contains(v, "-----BEGIN") {
			for _, line := range strings.FieldsFunc(v, func(c rune) bool { return c == '\n' || c == '\r' }) {
				if line = strings.TrimSpace(line); !strings.HasPrefix(line, "-----") {
					add(line)
				}
			}
		}
	}
	sort.SliceStable(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

// Redact is s without any registered credential.
func (r *Redactor) Redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, secret := range r.secrets {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, redactedMark)
		}
	}
	// A text that was clipped before it came here may end in the first part of a credential.
	body, clipped := strings.CutSuffix(s, "…")
	if !clipped {
		return s
	}
	for _, secret := range r.secrets {
		for n := min(len(secret)-1, len(body)); n >= minSecret; n-- {
			if strings.HasSuffix(body, secret[:n]) {
				return body[:len(body)-n] + redactedMark
			}
		}
	}
	return s
}

// OneLine is s on one line without control characters, clipped to max runes (an ellipsis marks the cut).
func OneLine(s string, max int) string {
	var b strings.Builder
	space := false
	for _, c := range strings.TrimSpace(s) {
		if unicode.IsSpace(c) || unicode.IsControl(c) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(c)
	}
	out := []rune(b.String())
	if max > 0 && len(out) > max {
		out = append(out[:max-1], '…')
	}
	return string(out)
}
