package gcp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
)

// serviceKey is one service-account key and the access token it was last exchanged for. The token is the only thing
// the adapter keeps between calls: it is a credential, not what the vendor said of an instance.
type serviceKey struct {
	email   string
	keyID   string
	project string
	pemText string
	priv    *rsa.PrivateKey

	mu     sync.Mutex
	token  string
	expiry time.Time // when the token is no longer used (shortly before Google's own expiry)
	flight *tokenFlight
}

// tokenFlight is one refresh that callers arriving meanwhile wait for instead of asking again.
type tokenFlight struct {
	done  chan struct{}
	token string
	err   error
	// cancelled: the refresh failed because the caller that ran it gave up, not because Google said no; a waiter
	// whose own context is still alive tries again instead of taking that error.
	cancelled bool
}

// keyFile is the JSON of a service-account key; its token_uri is deliberately not read.
type keyFile struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
}

// parseKeys reads one key, a JSON array of keys, or keys one after another.
func parseKeys(text string) ([]*serviceKey, error) {
	if strings.TrimSpace(text) == "" {
		return nil, core.NotSignedIn("no Google Cloud service-account key is set up")
	}
	var raws []json.RawMessage
	dec := json.NewDecoder(strings.NewReader(text))
	for {
		var raw json.RawMessage
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, core.NotSignedIn("the Google Cloud service-account key is not valid JSON")
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 && raw[0] == '[' {
			var arr []json.RawMessage
			if err := json.Unmarshal(raw, &arr); err != nil {
				return nil, core.NotSignedIn("the Google Cloud service-account key is not valid JSON")
			}
			raws = append(raws, arr...)
		} else {
			raws = append(raws, raw)
		}
	}
	var keys []*serviceKey
	seen := map[string]bool{}
	for i, raw := range raws {
		var f keyFile
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, core.NotSignedIn("Google Cloud key %d is not a JSON object", i+1)
		}
		if f.Type != "" && f.Type != "service_account" {
			return nil, core.NotSignedIn("Google Cloud key %d is not a service-account key", i+1)
		}
		if strings.TrimSpace(f.ClientEmail) == "" {
			return nil, core.NotSignedIn("Google Cloud key %d has no client_email", i+1)
		}
		if strings.TrimSpace(f.PrivateKey) == "" {
			return nil, core.NotSignedIn("Google Cloud key %d has no private_key", i+1)
		}
		priv, err := parsePrivateKey(f.PrivateKey)
		if err != nil {
			return nil, core.NotSignedIn("Google Cloud key %d: its private_key is not a usable RSA private key", i+1)
		}
		id := f.ClientEmail + "\x00" + f.PrivateKeyID
		if seen[id] {
			continue
		}
		seen[id] = true
		keys = append(keys, &serviceKey{email: strings.TrimSpace(f.ClientEmail), keyID: strings.TrimSpace(f.PrivateKeyID),
			project: strings.TrimSpace(f.ProjectID), pemText: f.PrivateKey, priv: priv})
	}
	if len(keys) == 0 {
		return nil, core.NotSignedIn("no Google Cloud service-account key is set up")
	}
	return keys, nil
}

// parsePrivateKey reads a PEM RSA key in PKCS#8 or PKCS#1.
func parsePrivateKey(text string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if r, ok := k.(*rsa.PrivateKey); ok {
			return r, nil
		}
		return nil, errors.New("not an RSA key")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// assertion is the signed JWT that asks aud for a Compute Engine access token.
func (k *serviceKey) assertion(now time.Time, aud string) (string, error) {
	hdr, err := json.Marshal(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid,omitempty"`
	}{"RS256", "JWT", k.keyID})
	if err != nil {
		return "", err
	}
	iat := now.Unix()
	claims, err := json.Marshal(struct {
		Iss   string `json:"iss"`
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}{k.email, computeScope, aud, iat, iat + 3600})
	if err != nil {
		return "", err
	}
	signing := b64(hdr) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.priv, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + b64(sig), nil
}

// drop forgets tok when it is still the cached one (Google said it is no good).
func (k *serviceKey) drop(tok string) {
	k.mu.Lock()
	if k.token == tok {
		k.token, k.expiry = "", time.Time{}
	}
	k.mu.Unlock()
}

// current is the cached token ("" when there is none).
func (k *serviceKey) current() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.token
}

// token is an access token of k: the cached one while it is fresh, otherwise one refresh that concurrent callers
// share.
func (a *adapter) token(ctx context.Context, k *serviceKey) (string, error) {
	for {
		k.mu.Lock()
		if k.token != "" && a.now().Before(k.expiry) {
			tok := k.token
			k.mu.Unlock()
			return tok, nil
		}
		if f := k.flight; f != nil {
			k.mu.Unlock()
			if a.onJoin != nil {
				a.onJoin()
			}
			select {
			case <-f.done:
				if f.err != nil && f.cancelled && ctx.Err() == nil {
					continue
				}
				if f.err != nil {
					return "", f.err
				}
				return f.token, nil
			case <-ctx.Done():
				return "", core.AsError(ctx.Err())
			}
		}
		f := &tokenFlight{done: make(chan struct{})}
		k.flight = f
		k.mu.Unlock()

		tok, exp, err := a.fetchToken(ctx, k)
		k.mu.Lock()
		f.token, f.err = tok, err
		f.cancelled = err != nil && ctx.Err() != nil
		if err == nil {
			k.token, k.expiry = tok, exp
		}
		k.flight = nil
		k.mu.Unlock()
		close(f.done)
		if err != nil {
			return "", err
		}
		return tok, nil
	}
}

// fetchToken exchanges a fresh assertion at the token endpoint.
func (a *adapter) fetchToken(ctx context.Context, k *serviceKey) (string, time.Time, error) {
	loc := core.Locale(ctx)
	now := a.now()
	asr, err := k.assertion(now, a.tokenURL)
	if err != nil {
		return "", time.Time{}, core.NotSignedIn("%s", core.T(loc, "The Google Cloud service-account key could not sign in.",
			"Google Cloud 服务账号密钥无法登录。"))
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {asr}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, core.Failed("%s", core.T(loc, "The sign-in request could not be written.", "无法生成登录请求。"))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := core.Do(ctx, a.http, req)
	if err != nil {
		return "", time.Time{}, err
	}
	var ans struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(resp.Body, &ans)
	if !resp.OK() {
		desc := a.scrub(core.OneLine(ans.Description, 0))
		switch {
		case resp.Status == http.StatusTooManyRequests:
			return "", time.Time{}, core.RateLimited("%s", core.T(loc, "Google Cloud's sign-in is throttling requests; try again shortly.",
				"Google Cloud 登录请求过于频繁，请稍后再试。"))
		case ans.Error == "invalid_grant" || ans.Error == "invalid_client" || ans.Error == "unauthorized_client" ||
			ans.Error == "access_denied" || ans.Error == "invalid_scope" ||
			resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
			msg := core.T(loc, "Google Cloud did not accept the service-account key", "Google Cloud 不接受这个服务账号密钥")
			if desc != "" {
				msg += core.T(loc, ": ", "：") + desc
			}
			return "", time.Time{}, core.NotSignedIn("%s", msg)
		}
		return "", time.Time{}, core.StatusError(vendor+" sign-in", resp.Status, desc)
	}
	if ans.AccessToken == "" {
		return "", time.Time{}, core.Failed("%s", core.T(loc, "Google Cloud's sign-in answered without an access token.",
			"Google Cloud 登录没有返回访问令牌。"))
	}
	life := time.Duration(ans.ExpiresIn) * time.Second
	if life <= 0 {
		life = time.Hour
	}
	margin := 2 * time.Minute
	if margin > life/2 {
		margin = life / 2
	}
	return ans.AccessToken, now.Add(life - margin), nil
}
