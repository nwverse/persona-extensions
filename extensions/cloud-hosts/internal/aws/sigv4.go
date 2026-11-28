package aws

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4, as the EC2 Query API and the Lightsail JSON API take it: the request is signed in the
// Authorization header, with its time in X-Amz-Date and a session token, when there is one, in X-Amz-Security-Token.

const (
	sigAlgorithm  = "AWS4-HMAC-SHA256"
	amzDateFormat = "20060102T150405Z"
)

// credentials are one profile's keys.
type credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// signRequest adds X-Amz-Date, X-Amz-Security-Token (with a session token) and Authorization to req, whose body is
// body. The signed headers are host, content-type and every x-amz-* header already on req.
func signRequest(req *http.Request, body []byte, c credentials, region, service string, now time.Time) {
	t := now.UTC()
	req.Header.Del("Authorization")
	req.Header.Set("X-Amz-Date", t.Format(amzDateFormat))
	if c.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	signed := signedHeaderNames(req.Header)
	req.Header.Set("Authorization", authorization(req.Method, host, req.URL, req.Header, signed, hashHex(body), c, region, service, t))
}

// signedHeaderNames are the lowercase names signed: host, content-type and x-amz-*, sorted.
func signedHeaderNames(h http.Header) []string {
	names := []string{"host"}
	for k := range h {
		l := strings.ToLower(k)
		if l == "content-type" || strings.HasPrefix(l, "x-amz-") {
			names = append(names, l)
		}
	}
	sort.Strings(names)
	return names
}

// authorization is the Authorization header's value for the request described.
func authorization(method, host string, u *url.URL, h http.Header, signed []string, payloadHash string, c credentials, region, service string, t time.Time) string {
	t = t.UTC()
	date := t.Format("20060102")
	scope := date + "/" + region + "/" + service + "/aws4_request"
	canonical := canonicalRequest(method, host, u, h, signed, payloadHash)
	toSign := sigAlgorithm + "\n" + t.Format(amzDateFormat) + "\n" + scope + "\n" + hashHex([]byte(canonical))
	key := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	return sigAlgorithm + " Credential=" + c.AccessKeyID + "/" + scope + ", SignedHeaders=" + strings.Join(signed, ";") + ", Signature=" + sig
}

// canonicalRequest is SigV4's canonical form of the request.
func canonicalRequest(method, host string, u *url.URL, h http.Header, signed []string, payloadHash string) string {
	var hb strings.Builder
	for _, name := range signed {
		v := host
		if name != "host" {
			vals := append([]string(nil), h.Values(name)...) // Values is the header's own slice
			for i := range vals {
				vals[i] = strings.Join(strings.Fields(vals[i]), " ")
			}
			v = strings.Join(vals, ",")
		}
		hb.WriteString(name)
		hb.WriteByte(':')
		hb.WriteString(v)
		hb.WriteByte('\n')
	}
	return strings.Join([]string{method, canonicalURI(u), canonicalQuery(u), hb.String(), strings.Join(signed, ";"), payloadHash}, "\n")
}

// canonicalURI is the path with every segment encoded (again, as every service but S3 wants it).
func canonicalURI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = awsEscape(s)
	}
	return strings.Join(segs, "/")
}

// canonicalQuery is the query sorted by name and value, each encoded.
func canonicalQuery(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	q, _ := url.ParseQuery(u.RawQuery)
	var pairs []string
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, awsEscape(k)+"="+awsEscape(v))
		}
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "&")
}

// awsEscape is RFC 3986 percent-encoding: everything but A-Z a-z 0-9 - _ . ~ is encoded, in upper-case hex.
func awsEscape(s string) string {
	const hexdigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexdigits[c>>4])
		b.WriteByte(hexdigits[c&15])
	}
	return b.String()
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
