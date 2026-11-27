package ilink

import (
	"crypto/md5"
	"encoding/hex"
	"strings"
)

// ResolvePartialQuote finds the text selected by WeChat. Start and end are literal markers and
// indices are occurrence counts; quotemd5 disambiguates when the same text appears more than once.
func ResolvePartialQuote(full string, partial PartialQuote) (string, bool) {
	if full == "" || partial.Start == "" || partial.End == "" || partial.StartIndex < 0 || partial.EndIndex < 0 {
		return "", false
	}
	candidates := []string{partialCandidate(full, partial, false), partialCandidate(full, partial, true)}
	if partial.QuoteMD5 == "" {
		for _, candidate := range candidates {
			if candidate != "" {
				return candidate, true
			}
		}
		return "", false
	}
	want := strings.ToLower(partial.QuoteMD5)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		hash := md5.Sum([]byte(candidate))
		if hex.EncodeToString(hash[:]) == want {
			return candidate, true
		}
	}
	return "", false
}

func partialCandidate(full string, partial PartialQuote, relative bool) string {
	start := nthIndexOf(full, partial.Start, partial.StartIndex, 0)
	if start < 0 {
		return ""
	}
	from := 0
	if relative {
		from = start + len(partial.Start)
	}
	end := nthIndexOf(full, partial.End, partial.EndIndex, from)
	if end < start {
		return ""
	}
	return full[start : end+len(partial.End)]
}

func nthIndexOf(text, value string, occurrence, from int) int {
	if value == "" || occurrence < 0 || from < 0 || from > len(text) {
		return -1
	}
	position := from
	for current := 0; current <= occurrence; current++ {
		relative := strings.Index(text[position:], value)
		if relative < 0 {
			return -1
		}
		position += relative
		if current < occurrence {
			position += len(value)
		}
	}
	return position
}
