package oci

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Keys are generated at run time: no key material lives in the repository.
var (
	keysOnce sync.Once
	testKey1 *rsa.PrivateKey
	testKey2 *rsa.PrivateKey
)

func keys(t testing.TB) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	keysOnce.Do(func() {
		var err error
		if testKey1, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if testKey2, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return testKey1, testKey2
}

func pemPKCS1(k *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func pemPKCS8(t testing.TB, k *rsa.PrivateKey) string {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

var authRE = regexp.MustCompile(`^Signature version="1",keyId="([^"]+)",algorithm="rsa-sha256",headers="([^"]+)",signature="([^"]+)"$`)

// verifySigned checks a request as OCI's gateway would: the header list for its method, the body digest, and the
// signature over the signing string with the public key of the key id. It answers the key id.
func verifySigned(r *http.Request, body []byte, pubs map[string]*rsa.PublicKey) (string, error) {
	m := authRE.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil {
		return "", fmt.Errorf("bad Authorization header %q", r.Header.Get("Authorization"))
	}
	keyID, headers, sig := m[1], m[2], m[3]
	want := "(request-target) date host"
	if hasBody(r.Method) {
		want = "(request-target) date host x-content-sha256 content-type content-length"
		if got := r.Header.Get("X-Content-Sha256"); got != bodySHA256(body) {
			return "", fmt.Errorf("x-content-sha256 %q does not match the body", got)
		}
	}
	if headers != want {
		return "", fmt.Errorf("signed headers %q, want %q", headers, want)
	}
	if _, err := http.ParseTime(r.Header.Get("Date")); err != nil {
		return "", fmt.Errorf("bad Date %q", r.Header.Get("Date"))
	}
	pub := pubs[keyID]
	if pub == nil {
		return "", fmt.Errorf("unknown key id %q", keyID)
	}
	text := signingString(strings.Fields(headers), func(n string) string {
		switch n {
		case "(request-target)":
			return strings.ToLower(r.Method) + " " + r.URL.RequestURI()
		case "host":
			return r.Host
		case "content-length":
			if v := r.Header.Get("Content-Length"); v != "" {
				return v
			}
			return strconv.FormatInt(r.ContentLength, 10)
		}
		return r.Header.Get(n)
	})
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(text))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], raw); err != nil {
		return "", fmt.Errorf("signature does not verify: %v", err)
	}
	return keyID, nil
}

func TestSignGET(t *testing.T) {
	k, _ := keys(t)
	s := newSigner("ocid1.tenancy.oc1..aaaatesttenancy0001", "ocid1.user.oc1..aaaatestuser0001", "aa:bb:cc:dd", k)
	req, _ := http.NewRequest(http.MethodGet, "https://iaas.region-1.example.test/20160918/instances?compartmentId=ocid1.tenancy.oc1..x&limit=100", nil)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	if err := s.sign(req, nil, now); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Date"); got != "Fri, 20 Nov 2026 10:00:00 GMT" {
		t.Fatalf("Date %q", got)
	}
	m := authRE.FindStringSubmatch(req.Header.Get("Authorization"))
	if m == nil {
		t.Fatalf("Authorization %q", req.Header.Get("Authorization"))
	}
	if m[1] != "ocid1.tenancy.oc1..aaaatesttenancy0001/ocid1.user.oc1..aaaatestuser0001/aa:bb:cc:dd" {
		t.Fatalf("keyId %q", m[1])
	}
	if m[2] != "(request-target) date host" {
		t.Fatalf("headers %q", m[2])
	}
	if req.Header.Get("X-Content-Sha256") != "" {
		t.Fatal("a GET must not carry x-content-sha256")
	}
	text := "(request-target): get /20160918/instances?compartmentId=ocid1.tenancy.oc1..x&limit=100\n" +
		"date: Fri, 20 Nov 2026 10:00:00 GMT\nhost: iaas.region-1.example.test"
	raw, _ := base64.StdEncoding.DecodeString(m[3])
	d := sha256.Sum256([]byte(text))
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, d[:], raw); err != nil {
		t.Fatalf("the signature does not verify over the expected signing string: %v", err)
	}
}

func TestSignPOST(t *testing.T) {
	k, _ := keys(t)
	s := newSigner("ocid1.tenancy.oc1..t", "ocid1.user.oc1..u", "11:22", k)
	pubs := map[string]*rsa.PublicKey{s.keyID: &k.PublicKey}
	for _, body := range [][]byte{{}, []byte(`{"namespace":"oci_computeagent"}`)} {
		req, _ := http.NewRequest(http.MethodPost, "https://iaas.region-1.example.test/20160918/instances/ocid1.instance.oc1.iad.x?action=START", bytes.NewReader(body))
		if err := s.sign(req, body, time.Now()); err != nil {
			t.Fatal(err)
		}
		m := authRE.FindStringSubmatch(req.Header.Get("Authorization"))
		if m == nil || m[2] != "(request-target) date host x-content-sha256 content-type content-length" {
			t.Fatalf("Authorization %q", req.Header.Get("Authorization"))
		}
		if len(body) == 0 && req.Header.Get("X-Content-Sha256") != "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=" {
			t.Fatalf("x-content-sha256 of the empty body %q", req.Header.Get("X-Content-Sha256"))
		}
		if req.Header.Get("Content-Length") != strconv.Itoa(len(body)) || req.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content headers %v", req.Header)
		}
		// verify as the server would see it
		req.Host = req.URL.Host
		got, _ := io.ReadAll(req.Body)
		if _, err := verifySigned(req, got, pubs); err != nil {
			t.Fatal(err)
		}
		// a changed body breaks it
		if _, err := verifySigned(req, append(got, 'x'), pubs); err == nil {
			t.Fatal("a tampered body verified")
		}
	}
}

func TestParseKey(t *testing.T) {
	k, _ := keys(t)
	for _, text := range []string{pemPKCS1(k), pemPKCS8(t, k)} {
		got, err := parseKey(text)
		if err != nil || !got.Equal(k) {
			t.Fatalf("parseKey: %v", err)
		}
	}
	enc := "-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----\n"
	if _, err := parseKey(enc); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("encrypted key: %v", err)
	}
	if _, err := parseKey("not a key"); err == nil {
		t.Fatal("garbage parsed")
	}
}
