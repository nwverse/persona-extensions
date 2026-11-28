package generic

import (
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// MaxHosts is the most hosts one configuration may hold: one fewer than the most cloud.instances answers, since the
// Provider takes a list of that many as one that may have more.
const MaxHosts = core.MaxInstances - 1

// defaultTermDays is how far renew moves the dates when term_days is not set.
const defaultTermDays = 30

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// hostRE is a host name used as a probe target.
var hostRE = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)*\.?$`)

// powerKinds are the standard kinds a webhook can serve, in the order instances list them.
var powerKinds = []wire.CloudActionKind{
	wire.CloudActionKindStart, wire.CloudActionKindStop, wire.CloudActionKindReboot,
	wire.CloudActionKindHardReset, wire.CloudActionKindForceStop,
}

// probeSpec is how a host is probed.
type probeSpec struct {
	tcp    bool   // a TCP connect; else an HTTP(S) GET
	target string // host:port for tcp, the URL for http
	host   string // the host name or address alone, for people
}

// host is one configured host.
type host struct {
	name           string
	title          string
	address        string // a plain IP or a host name
	private        string // a plain IP
	probe          *probeSpec
	hooks          map[wire.CloudActionKind]*url.URL
	renewHook      *url.URL
	method         string
	console        string
	expires        time.Time
	renews         time.Time
	termDays       int
	autoRenew      bool
	spot           bool
	keepaliveDays  int
	region, zone   string
	size, arch, os string
	cpu            float64
	memoryMB       int64
	diskGB         int64
}

// cfgError is a configuration error that names the host and the key, never the value.
func cfgError(label, key, what string) *core.Error {
	if key == "" {
		return core.Failed("custom hosts: host %s: %s", label, what)
	}
	return core.Failed("custom hosts: host %s, key %q: %s", label, core.OneLine(key, 40), what)
}

// parseBase is the WebhookBase: an https address (http only as the user's explicit choice), without credentials,
// query or fragment.
func parseBase(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Opaque != "" {
		return nil, core.Failed("custom hosts: the webhook base address is not a usable https URL")
	}
	if u.User != nil {
		return nil, core.Failed("custom hosts: the webhook base address may not carry a user name or password (use the webhook token)")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, core.Failed("custom hosts: the webhook base address may not carry a query or fragment (use the webhook token)")
	}
	return u, nil
}

// resolveHook is a webhook value as a URL: absolute https (http only when the base is http), or a path resolved
// against base. The answer's string is a reason without the value.
func resolveHook(v string, base *url.URL) (*url.URL, string) {
	if strings.ContainsFunc(v, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
		return nil, "is not a usable URL"
	}
	if strings.Contains(v, "://") {
		u, err := url.Parse(v)
		if err != nil || u.Host == "" || u.Opaque != "" {
			return nil, "is not a usable URL"
		}
		switch {
		case u.Scheme == "https":
		case u.Scheme == "http" && base != nil && base.Scheme == "http":
		default:
			return nil, "must be an https URL"
		}
		if u.User != nil {
			return nil, "may not carry a user name or password (use the webhook token)"
		}
		u.Fragment, u.RawFragment = "", ""
		return u, ""
	}
	if base == nil {
		return nil, "is a relative path but no webhook base address is set"
	}
	p, err := url.Parse(v)
	if err != nil || p.Scheme != "" || p.Host != "" || p.Opaque != "" || p.Path == "" {
		return nil, "is not a usable path"
	}
	u := *base
	// Joined in the escaped form, so an escaped slash or percent sign the user wrote stays what it was.
	escaped := strings.TrimRight(base.EscapedPath(), "/") + "/" + strings.TrimLeft(p.EscapedPath(), "/")
	plain, err := url.PathUnescape(escaped)
	if err != nil {
		return nil, "is not a usable path"
	}
	u.Path, u.RawPath = plain, escaped
	u.RawQuery = p.RawQuery
	u.Fragment, u.RawFragment = "", ""
	return &u, ""
}

// parseDate is a date ("2006-01-02", as midnight UTC) or an RFC 3339 time.
func parseDate(v string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), true
	}
	return time.Time{}, false
}

// parseProbe is a probe value: tcp:<port>, http:<port>[/path], https:<port>[/path] or a full https:// URL.
func parseProbe(v, address string) (*probeSpec, string) {
	if strings.HasPrefix(v, "https://") {
		u, err := url.Parse(v)
		if err != nil || u.Hostname() == "" || u.Opaque != "" || strings.ContainsFunc(v, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
			return nil, "is not a usable URL"
		}
		if u.User != nil {
			return nil, "may not carry a user name or password"
		}
		u.Fragment, u.RawFragment = "", ""
		return &probeSpec{target: u.String(), host: u.Hostname()}, ""
	}
	scheme, rest, ok := strings.Cut(v, ":")
	if !ok || (scheme != "tcp" && scheme != "http" && scheme != "https") {
		return nil, `must be "tcp:<port>", "http:<port>[/path]", "https:<port>[/path]" or an https URL`
	}
	portText, path := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		portText, path = rest[:i], rest[i:]
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, "has no usable port"
	}
	if address == "" {
		return nil, "needs an address to probe"
	}
	hp := joinHostPort(address, port)
	if scheme == "tcp" {
		if path != "" {
			return nil, "a tcp probe takes no path"
		}
		return &probeSpec{tcp: true, target: hp, host: address}, ""
	}
	if strings.ContainsFunc(path, func(c rune) bool { return c <= ' ' || c == 0x7f }) {
		return nil, "has an unusable path"
	}
	u, err := url.Parse(scheme + "://" + hp + path)
	if err != nil {
		return nil, "has an unusable path"
	}
	u.Fragment, u.RawFragment = "", ""
	return &probeSpec{target: u.String(), host: address}, ""
}

func joinHostPort(h string, port int) string {
	h = strings.TrimSuffix(h, ".")
	if strings.Contains(h, ":") {
		return "[" + h + "]:" + strconv.Itoa(port)
	}
	return h + ":" + strconv.Itoa(port)
}

// parseHost reads one host string. index (1-based) names it in errors until its name is known.
func parseHost(index int, spec string, base *url.URL) (*host, error) {
	label := "#" + strconv.Itoa(index)
	type kv struct{ key, value string }
	var pairs []kv
	seen := map[string]bool{}
	for _, part := range strings.Split(spec, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		if !ok || k == "" {
			return nil, cfgError(label, "", "every entry must be key=value")
		}
		if seen[k] {
			return nil, cfgError(label, k, "is given twice")
		}
		seen[k] = true
		pairs = append(pairs, kv{k, strings.TrimSpace(v)})
	}
	if len(pairs) == 0 {
		return nil, cfgError(label, "", "is empty")
	}
	h := &host{hooks: map[wire.CloudActionKind]*url.URL{}, method: "POST", termDays: defaultTermDays}
	for _, p := range pairs {
		if p.key == "name" {
			if !nameRE.MatchString(p.value) {
				return nil, cfgError(label, "name", "must be 1-63 characters of a-z, 0-9, dot, underscore and dash, starting with a letter or digit")
			}
			h.name = p.value
			label = strconv.Quote(p.value)
		}
	}
	if h.name == "" {
		return nil, cfgError(label, "name", "is required")
	}
	// The address first: the probe depends on it.
	for _, p := range pairs {
		switch p.key {
		case "address":
			if ip := core.PlainIP(p.value); ip != "" {
				h.address = ip
			} else if len(p.value) <= 253 && hostRE.MatchString(p.value) {
				h.address = strings.ToLower(p.value)
			} else {
				return nil, cfgError(label, p.key, "must be a plain IP address or a host name")
			}
		case "private_address":
			if h.private = core.PlainIP(p.value); h.private == "" {
				return nil, cfgError(label, p.key, "must be a plain IP address")
			}
		}
	}
	text := func(key, v string, max int) (string, error) {
		if v == "" || len([]rune(v)) > max || strings.ContainsFunc(v, func(c rune) bool { return c < ' ' || c == 0x7f }) {
			return "", cfgError(label, key, "must be 1-"+strconv.Itoa(max)+" characters of text")
		}
		return v, nil
	}
	var err error
	for _, p := range pairs {
		k, v := p.key, p.value
		switch k {
		case "name", "address", "private_address":
		case "title":
			h.title, err = text(k, v, 200)
		case "region":
			h.region, err = text(k, v, 80)
		case "zone":
			h.zone, err = text(k, v, 80)
		case "size":
			h.size, err = text(k, v, 80)
		case "arch":
			h.arch, err = text(k, v, 20)
		case "os":
			h.os, err = text(k, v, 80)
		case "probe":
			if v == "" {
				continue
			}
			ps, why := parseProbe(v, h.address)
			if why != "" {
				return nil, cfgError(label, k, why)
			}
			h.probe = ps
		case "start", "stop", "reboot", "hard_reset", "force_stop", "renew":
			u, why := resolveHook(v, base)
			if why != "" {
				return nil, cfgError(label, k, why)
			}
			if k == "renew" {
				h.renewHook = u
			} else {
				h.hooks[wire.CloudActionKind(k)] = u
			}
		case "method":
			switch m := strings.ToUpper(v); m {
			case "POST", "GET":
				h.method = m
			default:
				return nil, cfgError(label, k, "must be POST or GET")
			}
		case "console":
			if h.console = core.HTTPSURL(v, 2048); h.console == "" {
				return nil, cfgError(label, k, "must be an https URL without a user name or password")
			}
		case "expires", "renews":
			t, ok := parseDate(v)
			if !ok {
				return nil, cfgError(label, k, `must be a date such as 2027-01-31 or an RFC 3339 time`)
			}
			if k == "expires" {
				h.expires = t
			} else {
				h.renews = t
			}
		case "term_days", "keepalive_days":
			n, e := strconv.Atoi(v)
			lo := 1
			if k == "keepalive_days" {
				lo = 0
			}
			if e != nil || n < lo || n > 3650 {
				return nil, cfgError(label, k, "must be a whole number of days from "+strconv.Itoa(lo)+" to 3650")
			}
			if k == "term_days" {
				h.termDays = n
			} else {
				h.keepaliveDays = n
			}
		case "auto_renew", "spot":
			b, e := strconv.ParseBool(v)
			if e != nil {
				return nil, cfgError(label, k, "must be true or false")
			}
			if k == "spot" {
				h.spot = b
			} else {
				h.autoRenew = b
			}
		case "cpu":
			f, e := strconv.ParseFloat(v, 64)
			if e != nil || f < 0 || f > 4096 {
				return nil, cfgError(label, k, "must be a number of CPUs from 0 to 4096")
			}
			h.cpu = f
		case "memory_mb", "disk_gb":
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n < 0 || n > 1<<40 {
				return nil, cfgError(label, k, "must be a whole number from 0")
			}
			if k == "memory_mb" {
				h.memoryMB = n
			} else {
				h.diskGB = n
			}
		default:
			return nil, cfgError(label, k, "is not a known key")
		}
		if err != nil {
			return nil, err
		}
	}
	return h, nil
}

// isPrivate reports whether address is a plain IP of a private, loopback or link-local range.
func isPrivate(ip string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && (a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast())
}

// credentialKeyRE is a query key whose value is a credential whatever it looks like.
var credentialKeyRE = regexp.MustCompile(`(?i)(token|key|sig|secret|auth|pass|pwd|code|hash|hmac|cred|session|ticket)`)

// publicSet is what answers show anyway: the hosts' names, titles, addresses, places and specs, the console links
// and their parts, and the action names. A webhook's query value or path that repeats one carries no secret, and
// registering it would blank the names and details of every answer, and (inside a console link) drop the link.
type publicSet struct {
	values   map[string]bool
	consoles []string
}

// public is the publicSet of hosts.
func public(hosts []*host) publicSet {
	p := publicSet{values: map[string]bool{}}
	for _, h := range hosts {
		h.addPublic(p.values)
		if h.console != "" {
			p.consoles = append(p.consoles, strings.ToLower(h.console))
		}
	}
	return p
}

// shown reports whether v is something answers show anyway.
func (p publicSet) shown(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if p.values[v] {
		return true
	}
	for _, c := range p.consoles {
		if strings.Contains(c, v) {
			return true
		}
	}
	return false
}

func (h *host) addPublic(pub map[string]bool) {
	add := func(vs ...string) {
		for _, v := range vs {
			if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
				pub[v] = true
			}
		}
	}
	add(h.name, h.title, h.address, h.private, h.region, h.zone, h.size, h.arch, h.os, "true", "false",
		string(CustomRenew), string(CustomProbe), "webhook", "webhooks")
	for _, k := range powerKinds {
		add(string(k))
	}
	if u, err := url.Parse(h.console); err == nil && h.console != "" {
		add(u.Hostname(), u.Path)
		add(strings.Split(u.Path, "/")...)
		for _, vs := range u.Query() {
			add(vs...)
		}
	}
}

// secretSegment reports whether a path segment looks like a token: long, or of letters mixed with digits.
func secretSegment(seg string) bool {
	if len(seg) >= 16 {
		return true
	}
	return len(seg) >= 8 && strings.ContainsAny(seg, "0123456789") && strings.ContainsFunc(seg, func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
	})
}

// minQuerySecret is the shortest query value registered as a secret: a shorter one (an id, a mode, a flag) is an
// ordinary word far more often than a credential, and blanking it would blank the answers' words and drop links.
const minQuerySecret = 8

// urlSecrets are what of u may be a secret: the URL itself, its path and query (a relative webhook value as the user
// wrote it), every query value of at least minQuerySecret characters (except one that repeats a public value of h
// under a key that does not look like a credential), and every path segment that looks like a token (a path such as
// /hooks/<id>/start or a chat service's /services/<team>/<secret>).
func urlSecrets(u *url.URL, pub publicSet) []string {
	if u == nil {
		return nil
	}
	var out []string
	add := func(v string, always bool) {
		if v != "" && (always || !pub.shown(v)) {
			out = append(out, v)
		}
	}
	add(u.String(), true)
	add(u.RequestURI(), false)
	add(u.EscapedPath(), false)
	add(u.Path, false)
	for k, vs := range u.Query() {
		for _, v := range vs {
			if len(v) >= minQuerySecret {
				add(v, credentialKeyRE.MatchString(k))
			}
		}
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if secretSegment(seg) {
			add(seg, false)
		}
	}
	return out
}
