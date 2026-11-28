package core

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseNamedTokens(t *testing.T) {
	proxmox := "root@pam!persona=00000000-0000-0000-0000-000000000001"
	for _, c := range []struct {
		name, in string
		want     []NamedToken
	}{
		{"single", "test-token-0001", []NamedToken{{"default", "test-token-0001"}}},
		{"single trimmed", "  test-token-0001 \r\n\n", []NamedToken{{"default", "test-token-0001"}}},
		{"single named", "prod=test-token-0001", []NamedToken{{"prod", "test-token-0001"}}},
		{"single with = not after a name", "tok+en/=abc", []NamedToken{{"default", "tok+en/=abc"}}},
		{"single starting with =", "=abc", []NamedToken{{"default", "=abc"}}},
		{"single proxmox token", proxmox, []NamedToken{{"default", proxmox}}},
		{"named lines", "# accounts\nprod = test-token-0001\n\n  staging=test-token-0002=x\r\n; not a comment=1\n",
			[]NamedToken{{"prod", "test-token-0001"}, {"staging", "test-token-0002=x"}, {"default", "; not a comment=1"}}},
		{"duplicates skipped", "prod=a-token-1\nprod=a-token-2\nlab=a-token-3",
			[]NamedToken{{"prod", "a-token-1"}, {"lab", "a-token-3"}}},
		{"empty token skipped", "prod=\nlab=test-token-0003", []NamedToken{{"lab", "test-token-0003"}}},
		{"named proxmox tokens", "lab=" + proxmox + "\nhome=root@pve!x=00000000-0000-0000-0000-000000000002",
			[]NamedToken{{"lab", proxmox}, {"home", "root@pve!x=00000000-0000-0000-0000-000000000002"}}},
		{"unnamed lines share default", "token-a\ntoken-b", []NamedToken{{"default", "token-a"}}},
		{"invalid name", "my prod=tok-1\nlab=tok-2", []NamedToken{{"default", "my prod=tok-1"}, {"lab", "tok-2"}}},
		{"only comments", "# a\n\n#b", nil},
		{"empty", "", nil},
	} {
		if got := ParseNamedTokens(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestBugParseNamedTokensBase64Padding(t *testing.T) {
	got := ParseNamedTokens("dGVzdC10b2tlbi0wMDAx==")
	want := []NamedToken{{"default", "dGVzdC10b2tlbi0wMDAx=="}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
}

func TestValidAccountName(t *testing.T) {
	for s, want := range map[string]bool{
		"prod": true, "prod.eu_1-a": true, "A9": true, strings.Repeat("a", 40): true,
		"": false, strings.Repeat("a", 41): false, "a b": false, "ü": false, "a/b": false, "a=b": false, "a@b": false,
		"a\n": false,
	} {
		if ValidAccountName(s) != want {
			t.Errorf("ValidAccountName(%q) != %v", s, want)
		}
	}
}

func TestParseINI(t *testing.T) {
	in := "region = top\n# comment\n; also a comment\n[Default]\nAWS_Access_Key_ID = AKIAEXAMPLEEXAMPLE00\r\n" +
		"aws_secret_access_key=abc=def==\n\n[profile Dev]\nregion = us-test-1\nnot a pair\n[ staging ]\nkey = \n[]\nx = y\n[profile ]\nz=1"
	got := ParseINI(in)
	want := map[string]map[string]string{
		"default": {"region": "top", "aws_access_key_id": "AKIAEXAMPLEEXAMPLE00", "aws_secret_access_key": "abc=def==", "x": "y"},
		"profile": {"z": "1"}, // "[profile ]" is trimmed to "profile" before the prefix is looked for
		"dev":     {"region": "us-test-1"},
		"staging": {"key": ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v\nwant %v", got, want)
	}
	if got := ParseINI(""); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	// A header alone makes no section.
	if got := ParseINI("[lonely]"); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestCleanList(t *testing.T) {
	got := CleanList([]string{" a ", "", "b", "a", "  ", "c", "b"})
	if fmt.Sprint(got) != "[a b c]" {
		t.Fatalf("%v", got)
	}
	if CleanList(nil) != nil || CleanList([]string{" "}) != nil {
		t.Fatal("nothing is nil")
	}
}
