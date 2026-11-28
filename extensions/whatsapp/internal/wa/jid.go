package wa

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"go.mau.fi/whatsmeow/types"
)

// Server domain constants mirroring whatsmeow/types.
const (
	ServerUser       = types.DefaultUserServer // s.whatsapp.net
	ServerLID        = types.HiddenUserServer  // lid
	ServerGroup      = types.GroupServer       // g.us
	ServerBroadcast  = types.BroadcastServer   // broadcast
	ServerNewsletter = types.NewsletterServer  // newsletter
)

// NormalizeJID parses raw into a canonical non-AD JID string (stripping device/agent suffixes
// such as `:4` in `12025550123:4@s.whatsapp.net`). Returns "" if invalid or empty.
func NormalizeJID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	j, err := types.ParseJID(raw)
	if err != nil || j.IsEmpty() {
		return ""
	}
	j = j.ToNonAD()
	j.Server = strings.ToLower(j.Server)
	// Legacy c.us user JIDs normalize to s.whatsapp.net.
	if j.Server == "c.us" {
		j.Server = ServerUser
	}
	return j.String()
}

// ParseCanonicalJID parses a raw JID string and returns a non-AD types.JID.
func ParseCanonicalJID(raw string) (types.JID, error) {
	norm := NormalizeJID(raw)
	if norm == "" {
		return types.EmptyJID, fmt.Errorf("invalid WhatsApp JID %q", raw)
	}
	return types.ParseJID(norm)
}

// CanonicalJIDFromTypes normalizes a types.JID to its non-AD canonical string.
func CanonicalJIDFromTypes(j types.JID) string {
	if j.IsEmpty() {
		return ""
	}
	j = j.ToNonAD()
	j.Server = strings.ToLower(j.Server)
	if j.Server == "c.us" {
		j.Server = ServerUser
	}
	return j.String()
}

// IsLID reports whether jid is a WhatsApp hidden user (LID) JID.
func IsLID(jid string) bool {
	j, err := types.ParseJID(strings.TrimSpace(jid))
	return err == nil && strings.EqualFold(j.Server, ServerLID) && j.User != ""
}

// IsPhoneJID reports whether jid is a regular phone-number user JID (@s.whatsapp.net or legacy @c.us).
func IsPhoneJID(jid string) bool {
	j, err := types.ParseJID(strings.TrimSpace(jid))
	if err != nil || j.User == "" {
		return false
	}
	return strings.EqualFold(j.Server, ServerUser) || strings.EqualFold(j.Server, "c.us")
}

// IsGroupJID reports whether jid is a WhatsApp group JID (@g.us).
func IsGroupJID(jid string) bool {
	j, err := types.ParseJID(strings.TrimSpace(jid))
	return err == nil && strings.EqualFold(j.Server, ServerGroup) && j.User != ""
}

// IsSupportedChatJID reports whether a chat JID is a supported DM (PN or LID) or group chat.
// Status broadcasts, broadcast lists, and newsletters are excluded from chats/inbox/archive.
func IsSupportedChatJID(jid string) bool {
	norm := NormalizeJID(jid)
	if norm == "" {
		return false
	}
	j, err := types.ParseJID(norm)
	if err != nil || j.User == "" {
		return false
	}
	switch j.Server {
	case ServerUser, ServerLID, ServerGroup:
		return true
	default:
		return false
	}
}

// ChatKind returns "group" for @g.us chats, "dm" for user/LID chats, or "" for unsupported chats.
func ChatKind(jid string) string {
	norm := NormalizeJID(jid)
	if norm == "" {
		return ""
	}
	j, err := types.ParseJID(norm)
	if err != nil || j.User == "" {
		return ""
	}
	switch j.Server {
	case ServerGroup:
		return "group"
	case ServerUser, ServerLID:
		return "dm"
	default:
		return ""
	}
}

// PhoneFromJID extracts the E.164 digits (without leading '+') from a phone JID, or "" if not a phone JID.
func PhoneFromJID(jid string) string {
	norm := NormalizeJID(jid)
	if !IsPhoneJID(norm) {
		return ""
	}
	j, err := types.ParseJID(norm)
	if err != nil {
		return ""
	}
	for _, r := range j.User {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return j.User
}

// FormatPhoneDisplay formats digits as "+<digits>" when non-empty.
func FormatPhoneDisplay(digits string) string {
	digits = strings.TrimSpace(digits)
	if digits == "" {
		return ""
	}
	if strings.HasPrefix(digits, "+") {
		return digits
	}
	return "+" + digits
}

// NormalizePhoneNumber cleans a user-entered phone number into E.164 digits without '+'
// (as required by whatsmeow PairPhone and s.whatsapp.net JIDs).
// Accepts optional leading '+', spaces, dashes, dots, and parentheses; rejects leading '0' and
// non-E.164 lengths (must be 7..15 ASCII digits).
func NormalizePhoneNumber(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("phone number is empty")
	}
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' || unicode.IsSpace(r):
			continue
		default:
			return "", fmt.Errorf("invalid character %q in phone number", r)
		}
	}
	digits := b.String()
	if len(digits) < 7 || len(digits) > 15 {
		return "", errors.New("phone number must have 7 to 15 digits including country code")
	}
	if digits[0] == '0' {
		return "", errors.New("phone number must include international country code without leading 0")
	}
	return digits, nil
}

// PhoneToJID converts a user-entered phone number into a canonical <digits>@s.whatsapp.net JID.
func PhoneToJID(raw string) (string, error) {
	digits, err := NormalizePhoneNumber(raw)
	if err != nil {
		return "", err
	}
	return digits + "@" + ServerUser, nil
}

// FormatPairingCode formats an 8-character WhatsApp pairing code as "XXXX-XXXX" for readability.
func FormatPairingCode(code string) string {
	clean := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if len(clean) == 8 {
		return clean[:4] + "-" + clean[4:]
	}
	return strings.TrimSpace(code)
}

// JIDResolver resolves LID JIDs to canonical phone JIDs when a mapping is known.
type JIDResolver interface {
	ResolveLIDToPN(lid string) (string, bool)
}

// CanonicalizeChatJID normalizes a chat JID and maps @lid -> @s.whatsapp.net when known.
func CanonicalizeChatJID(raw string, r JIDResolver) string {
	norm := NormalizeJID(raw)
	if norm == "" {
		return ""
	}
	if IsLID(norm) && r != nil {
		if pn, ok := r.ResolveLIDToPN(norm); ok && IsPhoneJID(pn) {
			return NormalizeJID(pn)
		}
	}
	return norm
}

// BestDisplayName picks the cleanest display name for a contact or chat, never returning empty
// when a JID is known.
func BestDisplayName(savedName, businessName, pushName, jid string) string {
	for _, s := range []string{savedName, businessName, pushName} {
		if t := strings.TrimSpace(s); t != "" {
			return t
		}
	}
	if p := PhoneFromJID(jid); p != "" {
		return "+" + p
	}
	return NormalizeJID(jid)
}

// OpenChatURL returns a wa.me link for phone-based DMs so the user can open the chat in WhatsApp.
func OpenChatURL(jid string) string {
	if p := PhoneFromJID(jid); p != "" {
		return "https://wa.me/" + p
	}
	return ""
}
