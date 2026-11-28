package oci

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// signer signs requests the way OCI's API gateway checks them: draft-cavage HTTP Signatures, version 1, rsa-sha256,
// with the key id "<tenancy OCID>/<user OCID>/<key fingerprint>".
type signer struct {
	keyID string
	key   *rsa.PrivateKey
}

func newSigner(tenancy, user, fingerprint string, key *rsa.PrivateKey) *signer {
	return &signer{keyID: tenancy + "/" + user + "/" + fingerprint, key: key}
}

// The headers signed for a request without and with a body.
var (
	signedPlain = []string{"(request-target)", "date", "host"}
	signedBody  = []string{"(request-target)", "date", "host", "x-content-sha256", "content-type", "content-length"}
)

// hasBody reports whether OCI wants the body headers signed: POST, PUT and PATCH always (an empty body included).
func hasBody(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}

// bodySHA256 is x-content-sha256 of body: base64 of its SHA-256.
func bodySHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// signingString is the text that is signed: one lowercase "name: value" line per header of names, joined by "\n".
func signingString(names []string, value func(string) string) string {
	lines := make([]string, len(names))
	for i, n := range names {
		lines[i] = n + ": " + value(n)
	}
	return strings.Join(lines, "\n")
}

// sign sets Date (and for a request with a body x-content-sha256, content-type and content-length) and the
// Authorization header. body is exactly what the request sends.
func (s *signer) sign(req *http.Request, body []byte, now time.Time) error {
	req.Header.Set("Date", now.UTC().Format(http.TimeFormat))
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	names := signedPlain
	if hasBody(req.Method) {
		names = signedBody
		req.Header.Set("X-Content-Sha256", bodySHA256(body))
		if req.Header.Get("Content-Type") == "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	}
	text := signingString(names, func(n string) string {
		switch n {
		case "(request-target)":
			return strings.ToLower(req.Method) + " " + req.URL.RequestURI()
		case "host":
			return host
		}
		return req.Header.Get(n)
	})
	digest := sha256.Sum256([]byte(text))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return errors.New("the request could not be signed")
	}
	req.Header.Set("Authorization", `Signature version="1",keyId="`+s.keyID+`",algorithm="rsa-sha256",headers="`+
		strings.Join(names, " ")+`",signature="`+base64.StdEncoding.EncodeToString(sig)+`"`)
	return nil
}

// parseKey reads an unencrypted RSA private key in PEM (PKCS#8 or PKCS#1). Its errors never quote the key.
func parseKey(text string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(text)))
	if block == nil {
		return nil, errors.New("is not a PEM private key")
	}
	if strings.Contains(block.Type, "ENCRYPTED") || block.Headers["Proc-Type"] != "" {
		return nil, errors.New("is encrypted (give it without a passphrase)")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if r, ok := k.(*rsa.PrivateKey); ok {
			return r, nil
		}
		return nil, errors.New("is not an RSA key")
	}
	if r, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return r, nil
	}
	return nil, errors.New("could not be read as an RSA private key")
}
