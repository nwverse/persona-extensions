package wa

import (
	"strings"
	"testing"
)

func TestResolveProxy(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	// Direct when no proxy env vars are set.
	p, err := resolveProxy(env(map[string]string{}))
	if err != nil || p.URL != nil {
		t.Fatalf("direct: got (%v, %v), want nil URL", p.URL, err)
	}

	// HTTPS_PROXY respected and credentials redacted.
	p, err = resolveProxy(env(map[string]string{"HTTPS_PROXY": "http://user:secretpass@127.0.0.1:7890"}))
	if err != nil || p.URL == nil || p.URL.Host != "127.0.0.1:7890" {
		t.Fatalf("https_proxy: got (%v, %v)", p.URL, err)
	}
	if strings.Contains(p.Redacted(), "secretpass") {
		t.Fatalf("redacted proxy leaked password: %s", p.Redacted())
	}

	// ALL_PROXY fallback with socks5h.
	p, err = resolveProxy(env(map[string]string{"ALL_PROXY": "socks5h://127.0.0.1:1080"}))
	if err != nil || p.URL == nil || p.URL.Scheme != "socks5h" {
		t.Fatalf("all_proxy socks5h: got (%v, %v)", p.URL, err)
	}

	// NO_PROXY excludes web.whatsapp.com.
	p, err = resolveProxy(env(map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:7890",
		"NO_PROXY":    "whatsapp.com",
	}))
	if err != nil || p.URL != nil {
		t.Fatalf("no_proxy: got (%v, %v), want nil", p.URL, err)
	}

	// Unsupported scheme rejected.
	if _, err := resolveProxy(env(map[string]string{"HTTPS_PROXY": "ftp://127.0.0.1:21"})); err == nil {
		t.Fatal("expected error for unsupported ftp proxy scheme")
	}
}
