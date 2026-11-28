package core

import (
	"strings"
)

// NamedToken is one account's credential.
type NamedToken struct {
	Account string
	Token   string
}

// DefaultAccount names the account of a secret that holds a single credential.
const DefaultAccount = "default"

// ParseNamedTokens reads a secret that holds one credential, or several accounts as one "name=credential" per line
// (blank lines and lines starting with # skipped). A single line without a usable name is the account "default".
// An account name is 1-40 characters of a-z, A-Z, 0-9, dot, underscore and dash; a later line with the same name
// is skipped.
func ParseNamedTokens(secret string) []NamedToken {
	var out []NamedToken
	seen := map[string]bool{}
	var lines []string
	for _, l := range strings.FieldsFunc(secret, func(c rune) bool { return c == '\n' || c == '\r' }) {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			lines = append(lines, l)
		}
	}
	for _, l := range lines {
		name, token := DefaultAccount, l
		// A name is what stands before the first '=', when a credential follows: a token that merely ends in '='
		// (base64 padding) is a token.
		if i := strings.IndexByte(l, '='); i > 0 && ValidAccountName(strings.TrimSpace(l[:i])) {
			switch rest := strings.TrimSpace(l[i+1:]); {
			case rest == "" && len(lines) > 1:
				continue // a name without a credential
			case strings.Trim(rest, "=") == "":
				// base64 padding: the whole line is the token
			default:
				name, token = strings.TrimSpace(l[:i]), rest
			}
		}
		if token == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, NamedToken{Account: name, Token: token})
	}
	return out
}

// ValidAccountName reports whether s can name an account: it is shown to people and kept in bindings.
func ValidAccountName(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// ParseINI reads an INI file as the vendors' command lines write them: [section] headers, key = value lines, #
// and ; comments. Keys before any header go to the section "default"; section and key names are lowercased, and a
// "profile " prefix of a section is dropped. A header may be followed by a comment. Two sections whose names differ
// only in case are not merged (that could pair one profile's key id with another's secret): the first is kept and
// the later one skipped.
func ParseINI(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	section, spelled, skip := "default", map[string]string{}, false
	for _, l := range strings.FieldsFunc(text, func(c rune) bool { return c == '\n' || c == '\r' }) {
		l = strings.TrimSpace(l)
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		if end := strings.IndexByte(l, ']'); l[0] == '[' && end > 0 {
			if rest := strings.TrimSpace(l[end+1:]); rest == "" || rest[0] == '#' || rest[0] == ';' {
				as := strings.TrimSpace(l[1:end])
				section = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(as), "profile "))
				if section == "" {
					section = "default"
				}
				first, known := spelled[section]
				skip = known && first != as && as != ""
				if !known && as != "" {
					spelled[section] = as
				}
				continue
			}
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || skip {
			continue
		}
		if out[section] == nil {
			out[section] = map[string]string{}
		}
		out[section][strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

// CleanList are the trimmed, non-empty, distinct strings of in, in order.
func CleanList(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
