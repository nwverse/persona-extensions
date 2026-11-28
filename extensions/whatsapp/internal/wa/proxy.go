package wa

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/proxy"
)

// ProxyConfig holds the resolved proxy configuration from environment variables
// (HTTPS_PROXY / ALL_PROXY / HTTP_PROXY / NO_PROXY) for WhatsApp Web websocket and media requests.
type ProxyConfig struct {
	URL     *url.URL // resolved proxy URL for https://web.whatsapp.com, nil when direct
	EnvFunc func(*url.URL) (*url.URL, error)
}

// ResolveProxyFromEnv reads the current process environment (unlike http.ProxyFromEnvironment
// which caches once per process) and computes the proxy configuration for WhatsApp Web.
func ResolveProxyFromEnv() (ProxyConfig, error) {
	return resolveProxy(os.Getenv)
}

func resolveProxy(getenv func(string) string) (ProxyConfig, error) {
	httpsProxy := firstNonEmpty(getenv("HTTPS_PROXY"), getenv("https_proxy"))
	httpProxy := firstNonEmpty(getenv("HTTP_PROXY"), getenv("http_proxy"))
	allProxy := firstNonEmpty(getenv("ALL_PROXY"), getenv("all_proxy"))
	noProxy := firstNonEmpty(getenv("NO_PROXY"), getenv("no_proxy"))

	if httpsProxy == "" && allProxy != "" {
		httpsProxy = allProxy
	}
	if httpProxy == "" && allProxy != "" {
		httpProxy = allProxy
	}
	cfg := httpproxy.Config{
		HTTPProxy:  httpProxy,
		HTTPSProxy: httpsProxy,
		NoProxy:    noProxy,
		CGI:        getenv("REQUEST_METHOD") != "",
	}
	proxyFn := cfg.ProxyFunc()
	waURL := &url.URL{Scheme: "https", Host: "web.whatsapp.com"}
	u, err := proxyFn(waURL)
	if err != nil {
		return ProxyConfig{}, fmt.Errorf("invalid proxy setting: %w", err)
	}
	if u != nil {
		scheme := strings.ToLower(u.Scheme)
		switch scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return ProxyConfig{}, fmt.Errorf("unsupported proxy scheme %q (expected http, https, socks5, or socks5h)", u.Scheme)
		}
	}
	return ProxyConfig{URL: u, EnvFunc: proxyFn}, nil
}

// ApplyToClient configures a whatsmeow.Client to respect the hub's proxy environment variables.
// When no proxy applies, whatsmeow dials directly.
func (p ProxyConfig) ApplyToClient(cli *whatsmeow.Client) error {
	if cli == nil {
		return nil
	}
	if p.URL == nil {
		cli.SetProxy(nil)
		return nil
	}
	scheme := strings.ToLower(p.URL.Scheme)
	switch scheme {
	case "http", "https":
		fn := p.EnvFunc
		cli.SetProxy(func(req *http.Request) (*url.URL, error) {
			if req == nil || req.URL == nil {
				return p.URL, nil
			}
			return fn(req.URL)
		})
		return nil
	case "socks5", "socks5h":
		return cli.SetProxyAddress(p.URL.String())
	default:
		return fmt.Errorf("unsupported proxy scheme %q", p.URL.Scheme)
	}
}

// HTTPClient returns an *http.Client that honors this proxy configuration (for avatar fetches).
func (p ProxyConfig) HTTPClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	if p.URL != nil {
		scheme := strings.ToLower(p.URL.Scheme)
		switch scheme {
		case "http", "https":
			fn := p.EnvFunc
			tr.Proxy = func(req *http.Request) (*url.URL, error) {
				if req == nil || req.URL == nil {
					return p.URL, nil
				}
				return fn(req.URL)
			}
		case "socks5", "socks5h":
			var auth *proxy.Auth
			if p.URL.User != nil {
				pw, _ := p.URL.User.Password()
				auth = &proxy.Auth{User: p.URL.User.Username(), Password: pw}
			}
			if dialer, err := proxy.SOCKS5("tcp", p.URL.Host, auth, proxy.Direct); err == nil {
				if cd, ok := dialer.(proxy.ContextDialer); ok {
					tr.DialContext = cd.DialContext
				} else {
					tr.Dial = dialer.Dial
				}
			}
		}
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// Redacted returns a safe, credential-free description of the proxy (empty when direct).
func (p ProxyConfig) Redacted() string {
	if p.URL == nil {
		return ""
	}
	return p.URL.Redacted()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
