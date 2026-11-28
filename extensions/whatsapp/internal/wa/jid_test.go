package wa

import "testing"

type mapResolver map[string]string

func (m mapResolver) ResolveLIDToPN(lid string) (string, bool) {
	v, ok := m[NormalizeJID(lid)]
	return v, ok
}

func TestNormalizeJIDAndKind(t *testing.T) {
	tests := []struct {
		in        string
		want      string
		kind      string
		supported bool
		phone     string
	}{
		{"12025550123:4@s.whatsapp.net", "12025550123@s.whatsapp.net", "dm", true, "12025550123"},
		{"12025550123@c.us", "12025550123@s.whatsapp.net", "dm", true, "12025550123"},
		{"9988776655:2@lid", "9988776655@lid", "dm", true, ""},
		{"120363000000001@g.us", "120363000000001@g.us", "group", true, ""},
		{"status@broadcast", "status@broadcast", "", false, ""},
		{"120363000000999@newsletter", "120363000000999@newsletter", "", false, ""},
		{"", "", "", false, ""},
	}
	for _, tc := range tests {
		if got := NormalizeJID(tc.in); got != tc.want {
			t.Errorf("NormalizeJID(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := ChatKind(tc.in); got != tc.kind {
			t.Errorf("ChatKind(%q) = %q, want %q", tc.in, got, tc.kind)
		}
		if got := IsSupportedChatJID(tc.in); got != tc.supported {
			t.Errorf("IsSupportedChatJID(%q) = %v, want %v", tc.in, got, tc.supported)
		}
		if got := PhoneFromJID(tc.in); got != tc.phone {
			t.Errorf("PhoneFromJID(%q) = %q, want %q", tc.in, got, tc.phone)
		}
	}
}

func TestCanonicalizeChatJIDWithLIDResolver(t *testing.T) {
	r := mapResolver{"9988776655@lid": "12025550123@s.whatsapp.net"}
	if got := CanonicalizeChatJID("9988776655:3@lid", r); got != "12025550123@s.whatsapp.net" {
		t.Fatalf("CanonicalizeChatJID(lid) = %q, want 12025550123@s.whatsapp.net", got)
	}
	if got := CanonicalizeChatJID("11223344@lid", r); got != "11223344@lid" {
		t.Fatalf("unmapped LID = %q, want 11223344@lid", got)
	}
}

func TestNormalizePhoneNumberAndPairingCode(t *testing.T) {
	good := map[string]string{
		"+1 (202) 555-0123": "12025550123",
		"12025550123":       "12025550123",
		"+44 20 7946 0958":  "442079460958",
	}
	for in, want := range good {
		got, err := NormalizePhoneNumber(in)
		if err != nil || got != want {
			t.Errorf("NormalizePhoneNumber(%q) = (%q, %v), want (%q, nil)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "qr", "0123456789", "12345", "1234567890123456", "+1-555-abc"} {
		if got, err := NormalizePhoneNumber(bad); err == nil {
			t.Errorf("NormalizePhoneNumber(%q) unexpectedly succeeded with %q", bad, got)
		}
	}
	if got := FormatPairingCode("abcd1234"); got != "ABCD-1234" {
		t.Fatalf("FormatPairingCode = %q, want ABCD-1234", got)
	}
	if got := FormatPairingCode("ABCD-1234"); got != "ABCD-1234" {
		t.Fatalf("FormatPairingCode(hyphen) = %q, want ABCD-1234", got)
	}
}
