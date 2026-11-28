package aws

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The AWS SigV4 test suite's public example key and time.
var (
	suiteCreds = credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	suiteTime  = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
)

func TestSigV4SuiteVectors(t *testing.T) {
	cases := []struct {
		name, method, url, contentType, body, service, want string
	}{
		{"get-vanilla", http.MethodGet, "https://example.amazonaws.com/", "", "", "service",
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"post-vanilla", http.MethodPost, "https://example.amazonaws.com/", "", "", "service",
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"},
		{"post-x-www-form-urlencoded", http.MethodPost, "https://example.amazonaws.com/", "application/x-www-form-urlencoded", "Param1=value1", "service",
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=ff11897932ad3f4e8b18135d722051e5ac45fc38421b1da7b9d196a0fe09473a"},
		// The documentation's IAM ListUsers example: a query string and a content type.
		{"iam-list-users", http.MethodGet, "https://iam.amazonaws.com/?Version=2010-05-08&Action=ListUsers", "application/x-www-form-urlencoded; charset=utf-8", "", "iam",
			"AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(c.method, c.url, strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if c.contentType != "" {
				req.Header.Set("Content-Type", c.contentType)
			}
			signRequest(req, []byte(c.body), suiteCreds, "us-east-1", c.service, suiteTime)
			if got := req.Header.Get("Authorization"); got != c.want {
				t.Fatalf("Authorization\n got %s\nwant %s", got, c.want)
			}
			if got := req.Header.Get("X-Amz-Date"); got != "20150830T123600Z" {
				t.Fatalf("X-Amz-Date = %q", got)
			}
			if req.Header.Get("X-Amz-Security-Token") != "" {
				t.Fatal("a security token without a session token")
			}
		})
	}
}

func TestSigV4SessionTokenIsSigned(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://example.amazonaws.com/", strings.NewReader("a=b"))
	c := suiteCreds
	c.SessionToken = "test-session-token-0001"
	signRequest(req, []byte("a=b"), c, "us-east-1", "service", suiteTime)
	if req.Header.Get("X-Amz-Security-Token") != "test-session-token-0001" {
		t.Fatal("no X-Amz-Security-Token")
	}
	if !strings.Contains(req.Header.Get("Authorization"), "SignedHeaders=host;x-amz-date;x-amz-security-token,") {
		t.Fatalf("the token is not signed: %s", req.Header.Get("Authorization"))
	}
	// Signing again replaces the header rather than signing the old Authorization.
	first := req.Header.Get("Authorization")
	signRequest(req, []byte("a=b"), c, "us-east-1", "service", suiteTime)
	if req.Header.Get("Authorization") != first {
		t.Fatal("re-signing changed the signature")
	}
}

func TestSigV4CanonicalParts(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/a%20b/c?b=2&a=x+y&a=1&c=%7E~", nil)
	if got := canonicalQuery(req.URL); got != "a=1&a=x%20y&b=2&c=~~" {
		t.Fatalf("canonicalQuery = %q", got)
	}
	if got := canonicalURI(req.URL); got != "/a%2520b/c" {
		t.Fatalf("canonicalURI = %q", got)
	}
	if got := awsEscape("a/b c*~"); got != "a%2Fb%20c%2A~" {
		t.Fatalf("awsEscape = %q", got)
	}
	req.Header.Set("X-Amz-Target", "  one   two ")
	cr := canonicalRequest(req.Method, "example.amazonaws.com", req.URL, req.Header, []string{"host", "x-amz-target"}, hashHex(nil))
	if !strings.Contains(cr, "\nx-amz-target:one two\n\nhost;x-amz-target\n") {
		t.Fatalf("header values are not trimmed: %q", cr)
	}
}
