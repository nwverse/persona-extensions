package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	assetCDNHost  = "shared.fastly.steamstatic.com"
	avatarCDNHost = "avatars.steamstatic.com"
	assetCDNBase  = "https://" + assetCDNHost
	avatarCDNBase = "https://" + avatarCDNHost

	imageConcurrency    = 6
	imageReadTimeout    = 6 * time.Second
	maxImageBytes       = 1 << 20 // 1 MiB, well below wire.ImageBytesMost (4 MiB)
	imageCacheMaxItems  = 256
	imageCacheMaxBytes  = 16 << 20 // 16 MiB in-memory cache ceiling
	imageCacheTTL       = 24 * time.Hour
	imageMaxAge         = wire.ImageMaxAgeMost // 86400s for Hub persistence
	artworkRegistryCap  = 2048
	avatarHashHexLength = 40

	refPrefixAppHeader  = "app-header:"
	refPrefixAppCapsule = "app-capsule:"
	refPrefixAvatar     = "avatar:"
)

// appArtwork holds verified relative Steam CDN paths extracted from StoreBrowse assets.
type appArtwork struct {
	HeaderPath  string `json:"header_path,omitempty"`
	CapsulePath string `json:"capsule_path,omitempty"`
}

func appHeaderRef(appID uint32) string {
	return refPrefixAppHeader + appIDText(appID)
}

func appCapsuleRef(appID uint32) string {
	return refPrefixAppCapsule + appIDText(appID)
}

func avatarRef(hash string) string {
	if !validAvatarHash(hash) {
		return ""
	}
	return refPrefixAvatar + strings.ToLower(hash)
}

func validAvatarHash(s string) bool {
	if len(s) != avatarHashHexLength {
		return false
	}
	nonZero := false
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			if c != '0' {
				nonZero = true
			}
		case c >= 'a' && c <= 'f':
			nonZero = true
		case c >= 'A' && c <= 'F':
			nonZero = true
		default:
			return false
		}
	}
	return nonZero
}

// parseAvatarURL extracts a validated 40-hex SHA-1 avatar hash from official Steam avatar URLs.
func parseAvatarURL(raws ...string) string {
	for _, raw := range raws {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || !isOfficialAvatarHost(strings.ToLower(u.Hostname())) {
			continue
		}
		base := path.Base(u.Path)
		lower := strings.ToLower(base)
		var candidate string
		switch {
		case strings.HasSuffix(lower, "_full.jpg"):
			candidate = lower[:len(lower)-len("_full.jpg")]
		case strings.HasSuffix(lower, "_medium.jpg"):
			candidate = lower[:len(lower)-len("_medium.jpg")]
		case strings.HasSuffix(lower, ".jpg"):
			candidate = lower[:len(lower)-len(".jpg")]
		default:
			continue
		}
		if validAvatarHash(candidate) {
			return candidate
		}
	}
	return ""
}

func isOfficialAvatarHost(host string) bool {
	switch host {
	case avatarCDNHost,
		"avatars.fastly.steamstatic.com",
		"avatars.akamai.steamstatic.com",
		"avatars.cloudflare.steamstatic.com",
		"steamcdn-a.akamaihd.net",
		"cdn.akamai.steamstatic.com",
		"cdn.cloudflare.steamstatic.com":
		return true
	default:
		return false
	}
}

// parseAvatarDigest parses public_data.sha_digest_avatar from GetPlayerLinkDetails,
// supporting both Valve's standard base64-encoded 20-byte SHA-1 and 40-char hex strings.
func parseAvatarDigest(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if validAvatarHash(raw) {
		return strings.ToLower(raw)
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		decoded, err := enc.DecodeString(raw)
		if err == nil && len(decoded) == 20 {
			h := hex.EncodeToString(decoded)
			if validAvatarHash(h) {
				return h
			}
			return ""
		}
	}
	return ""
}

// resolveStoreArtwork extracts verified Steam CDN asset paths from StoreBrowse storeAssets.
func resolveStoreArtwork(assets *storeAssets) *appArtwork {
	if assets == nil || assets.URLFormat == "" {
		return nil
	}
	header := buildStoreAssetPath(assets.URLFormat, assets.Header)
	if header == "" {
		header = buildStoreAssetPath(assets.URLFormat, assets.MainCapsule)
	}
	capsule := buildStoreAssetPath(assets.URLFormat, assets.SmallCapsule)
	if capsule == "" {
		capsule = buildStoreAssetPath(assets.URLFormat, assets.MainCapsule)
	}
	if capsule == "" {
		capsule = header
	}
	if header == "" && capsule == "" {
		return nil
	}
	return &appArtwork{HeaderPath: header, CapsulePath: capsule}
}

func buildStoreAssetPath(format, filename string) string {
	format = strings.TrimSpace(format)
	filename = strings.TrimSpace(filename)
	if format == "" || filename == "" || !strings.Contains(format, "${FILENAME}") {
		return ""
	}
	// Strip cache-busting query suffixes if present on the filename.
	if base, _, hasQuery := strings.Cut(filename, "?"); hasQuery {
		filename = base
	}
	combined := strings.ReplaceAll(format, "${FILENAME}", filename)
	if base, _, hasQuery := strings.Cut(combined, "?"); hasQuery {
		combined = base
	}
	combined = strings.TrimPrefix(combined, "/")
	if !strings.HasPrefix(combined, "store_item_assets/") {
		combined = "store_item_assets/" + combined
	}
	if !validStoreAssetPath(combined) {
		return ""
	}
	return combined
}

func validStoreAssetPath(p string) bool {
	if len(p) == 0 || len(p) > 256 {
		return false
	}
	if !strings.HasPrefix(p, "store_item_assets/steam/apps/") {
		return false
	}
	if strings.Contains(p, "..") || strings.Contains(p, "://") || strings.ContainsAny(p, "\\?#%@\r\n\t ") {
		return false
	}
	lower := strings.ToLower(p)
	if !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") &&
		!strings.HasSuffix(lower, ".png") && !strings.HasSuffix(lower, ".webp") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for i := range len(seg) {
			c := seg[i]
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-') {
				return false
			}
		}
	}
	return true
}

func standardHeaderPath(appID uint32) string {
	return "store_item_assets/steam/apps/" + appIDText(appID) + "/header.jpg"
}

func standardCapsulePath(appID uint32) string {
	return "store_item_assets/steam/apps/" + appIDText(appID) + "/capsule_231x87.jpg"
}

// imageProxy fetches, validates, and caches Steam static artwork and player avatars
// for Persona UI v6 ui.image requests without sharing API or Store concurrency slots.
type imageProxy struct {
	http  *http.Client
	slots chan struct{}
	now   func() time.Time

	mu       sync.Mutex
	artwork  map[uint32]appArtwork
	artOrder []uint32
	cache    map[string]*cachedImage
	order    []string // oldest first for LRU eviction
	bytes    int
	inFlight map[string]*imageFlight
}

type cachedImage struct {
	data      []byte
	mime      string
	fetchedAt time.Time
}

type imageFlight struct {
	done chan struct{}
	img  *wire.UIImage
	err  error
}

func newImageProxy(baseClient *http.Client, now func() time.Time) *imageProxy {
	if now == nil {
		now = time.Now
	}
	var transport http.RoundTripper
	if baseClient != nil && baseClient.Transport != nil {
		transport = baseClient.Transport
		if st, ok := transport.(*steamTransport); ok {
			// Bypass the API/Store concurrency slots so artwork fetches never block data reads.
			transport = st.base
		}
	}
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSHandshakeTimeout = 5 * time.Second
		t.ResponseHeaderTimeout = imageReadTimeout
		t.IdleConnTimeout = 45 * time.Second
		transport = t
	}
	timeout := imageReadTimeout
	if baseClient != nil && baseClient.Timeout > 0 && baseClient.Timeout < timeout {
		timeout = baseClient.Timeout
	}
	return &imageProxy{
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			// Never follow redirects to arbitrary hosts.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		slots:    make(chan struct{}, imageConcurrency),
		now:      now,
		artwork:  make(map[uint32]appArtwork),
		cache:    make(map[string]*cachedImage),
		inFlight: make(map[string]*imageFlight),
	}
}

func (p *imageProxy) recordArtwork(items map[uint32]appArtwork) {
	if p == nil || len(items) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for appID, art := range items {
		if appID == 0 || (art.HeaderPath == "" && art.CapsulePath == "") {
			continue
		}
		if art.HeaderPath != "" && !validStoreAssetPath(art.HeaderPath) {
			art.HeaderPath = ""
		}
		if art.CapsulePath != "" && !validStoreAssetPath(art.CapsulePath) {
			art.CapsulePath = ""
		}
		if art.HeaderPath == "" && art.CapsulePath == "" {
			continue
		}
		if _, exists := p.artwork[appID]; !exists {
			if len(p.artOrder) >= artworkRegistryCap {
				evict := p.artOrder[0]
				p.artOrder = p.artOrder[1:]
				delete(p.artwork, evict)
			}
			p.artOrder = append(p.artOrder, appID)
		}
		p.artwork[appID] = art
	}
}

func (p *imageProxy) lookupArtwork(appID uint32) (appArtwork, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	art, ok := p.artwork[appID]
	return art, ok
}

func (p *imageProxy) resolveCandidates(ref string) ([]string, bool) {
	if len(ref) == 0 || len(ref) > 128 {
		return nil, false
	}
	switch {
	case strings.HasPrefix(ref, refPrefixAppHeader):
		appID, ok := parseStrictAppID(strings.TrimPrefix(ref, refPrefixAppHeader))
		if !ok {
			return nil, false
		}
		candidates := make([]string, 0, 2)
		if art, found := p.lookupArtwork(appID); found && art.HeaderPath != "" {
			candidates = append(candidates, assetCDNBase+"/"+art.HeaderPath)
		}
		std := assetCDNBase + "/" + standardHeaderPath(appID)
		if len(candidates) == 0 || candidates[0] != std {
			candidates = append(candidates, std)
		}
		return candidates, true

	case strings.HasPrefix(ref, refPrefixAppCapsule):
		appID, ok := parseStrictAppID(strings.TrimPrefix(ref, refPrefixAppCapsule))
		if !ok {
			return nil, false
		}
		candidates := make([]string, 0, 3)
		appendUnique := func(target string) {
			for _, existing := range candidates {
				if existing == target {
					return
				}
			}
			candidates = append(candidates, target)
		}
		if art, found := p.lookupArtwork(appID); found && art.CapsulePath != "" {
			appendUnique(assetCDNBase + "/" + art.CapsulePath)
		}
		appendUnique(assetCDNBase + "/" + standardCapsulePath(appID))
		appendUnique(assetCDNBase + "/" + standardHeaderPath(appID))
		return candidates, true

	case strings.HasPrefix(ref, refPrefixAvatar):
		hash := strings.TrimPrefix(ref, refPrefixAvatar)
		if !validAvatarHash(hash) || hash != strings.ToLower(hash) {
			return nil, false
		}
		return []string{
			avatarCDNBase + "/" + hash + "_full.jpg",
			avatarCDNBase + "/" + hash + "_medium.jpg",
		}, true

	default:
		return nil, false
	}
}

func parseStrictAppID(raw string) (uint32, bool) {
	if len(raw) == 0 || len(raw) > 10 || raw[0] == '0' {
		return 0, false
	}
	for i := range len(raw) {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint32(n), true
}

func (p *imageProxy) serve(ctx context.Context, ref string) (*wire.UIImage, error) {
	candidates, ok := p.resolveCandidates(ref)
	if !ok {
		return nil, invalid("Invalid Steam image reference.")
	}
	if cached := p.getCached(ref); cached != nil {
		return cached, nil
	}

	p.mu.Lock()
	if flight, running := p.inFlight[ref]; running {
		p.mu.Unlock()
		select {
		case <-flight.done:
			return flight.img, flight.err
		case <-ctx.Done():
			return nil, &ext.Error{Code: string(sanitizeError(ctx.Err())), Message: "Steam artwork request was canceled."}
		}
	}
	flight := &imageFlight{done: make(chan struct{})}
	p.inFlight[ref] = flight
	p.mu.Unlock()

	fetchCtx, cancel := context.WithTimeout(ctx, imageReadTimeout)
	defer cancel()

	img, err := p.fetchCandidates(fetchCtx, ref, candidates)

	p.mu.Lock()
	flight.img, flight.err = img, err
	delete(p.inFlight, ref)
	close(flight.done)
	p.mu.Unlock()

	return img, err
}

func (p *imageProxy) getCached(ref string) *wire.UIImage {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.cache[ref]
	if !ok {
		return nil
	}
	age := p.now().Sub(entry.fetchedAt)
	if age < 0 || age >= imageCacheTTL {
		p.removeLocked(ref)
		return nil
	}
	p.touchLocked(ref)
	remaining := int((imageCacheTTL - age).Seconds())
	if remaining < 1 {
		remaining = 1
	}
	if remaining > imageMaxAge {
		remaining = imageMaxAge
	}
	return ext.Image(entry.mime, entry.data, remaining)
}

func (p *imageProxy) putCached(ref, mime string, data []byte) {
	if len(data) == 0 || len(data) > maxImageBytes {
		return
	}
	stored := append([]byte(nil), data...)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.cache[ref]; exists {
		p.removeLocked(ref)
	}
	for len(p.order) > 0 && (len(p.cache) >= imageCacheMaxItems || p.bytes+len(stored) > imageCacheMaxBytes) {
		p.removeLocked(p.order[0])
	}
	p.cache[ref] = &cachedImage{data: stored, mime: mime, fetchedAt: p.now()}
	p.order = append(p.order, ref)
	p.bytes += len(stored)
}

func (p *imageProxy) removeLocked(ref string) {
	if entry, ok := p.cache[ref]; ok {
		p.bytes -= len(entry.data)
		delete(p.cache, ref)
	}
	for i, k := range p.order {
		if k == ref {
			p.order = append(p.order[:i], p.order[i+1:]...)
			break
		}
	}
}

func (p *imageProxy) touchLocked(ref string) {
	for i, k := range p.order {
		if k == ref {
			if i == len(p.order)-1 {
				return
			}
			p.order = append(p.order[:i], p.order[i+1:]...)
			p.order = append(p.order, ref)
			return
		}
	}
}

func (p *imageProxy) fetchCandidates(ctx context.Context, ref string, candidates []string) (*wire.UIImage, error) {
	var lastErr error
	for _, target := range candidates {
		data, mime, status, err := p.fetchOne(ctx, target)
		if err == nil {
			p.putCached(ref, mime, data)
			return ext.Image(mime, data, imageMaxAge), nil
		}
		lastErr = err
		if status != http.StatusNotFound && status != http.StatusGone {
			break
		}
	}
	if lastErr == nil {
		lastErr = &ext.Error{Code: "not_found", Message: "Steam artwork is unavailable."}
	}
	return nil, lastErr
}

func (p *imageProxy) fetchOne(ctx context.Context, target string) ([]byte, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil || req.URL.Scheme != "https" || req.URL.User != nil ||
		(req.URL.Host != assetCDNHost && req.URL.Host != avatarCDNHost) {
		return nil, "", 0, invalid("Invalid Steam image target.")
	}
	req.Header.Set("Accept", "image/webp,image/jpeg,image/png,image/gif")

	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return nil, "", 0, &ext.Error{Code: string(sanitizeError(ctx.Err())), Message: "Steam artwork request timed out."}
	}

	resp, err := p.http.Do(req)
	if err != nil {
		issue := sanitizeError(err)
		if issue == problemTimeout {
			p.http.CloseIdleConnections()
		}
		return nil, "", 0, &ext.Error{Code: string(issue), Message: "Steam artwork is temporarily unavailable."}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		code := "unavailable"
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			code = "not_found"
		}
		return nil, "", resp.StatusCode, &ext.Error{Code: code, Message: "Steam artwork is unavailable."}
	}
	if resp.ContentLength > maxImageBytes {
		return nil, "", resp.StatusCode, &ext.Error{Code: "too_large", Message: "Steam artwork exceeds size limit."}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", resp.StatusCode, &ext.Error{Code: string(sanitizeError(err)), Message: "Steam artwork could not be read."}
	}
	if len(data) == 0 || len(data) > maxImageBytes {
		return nil, "", resp.StatusCode, &ext.Error{Code: "too_large", Message: "Steam artwork exceeds size limit."}
	}
	checked, err := wire.CheckImage(ext.Image("", data, imageMaxAge))
	if err != nil {
		return nil, "", resp.StatusCode, &ext.Error{Code: "invalid_image", Message: "Steam returned an unsupported image."}
	}
	return checked.Data, checked.Mime, resp.StatusCode, nil
}

func (a *app) image(ctx context.Context, r *ext.ImageRequest) (*wire.UIImage, error) {
	if a.images == nil {
		return nil, &ext.Error{Code: "unavailable", Message: "Steam artwork is unavailable."}
	}
	return a.images.serve(ctx, r.Ref)
}

func playerAvatar(p *player, fallbackName string) *wire.Avatar {
	initials := avatarInitials(fallbackName)
	var ref string
	if p != nil {
		ref = avatarRef(p.AvatarHash)
	}
	if ref == "" && initials == "" {
		initials = "S"
	}
	return &wire.Avatar{Ref: ref, Text: initials}
}

// avatarInitials produces 1 to 2 runes suitable for wire.Avatar.Text (maxLength 2).
func avatarInitials(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "S"
	}
	words := strings.Fields(name)
	if len(words) >= 2 {
		r1, ok1 := firstVisibleRune(words[0])
		r2, ok2 := firstVisibleRune(words[1])
		if ok1 && ok2 && r1 <= unicode.MaxASCII && r2 <= unicode.MaxASCII {
			return string([]rune{unicode.ToUpper(r1), unicode.ToUpper(r2)})
		}
	}
	if r, ok := firstVisibleRune(name); ok {
		return string(unicode.ToUpper(r))
	}
	return "S"
}

func firstVisibleRune(s string) (rune, bool) {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r, true
		}
	}
	for _, r := range s {
		if !unicode.IsSpace(r) && !unicode.IsControl(r) {
			return r, true
		}
	}
	return 0, false
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			if maxRunes > 1 {
				// Step back one rune to make room for an ellipsis when possible.
				prev := 0
				k := 0
				for j := range s {
					if k == maxRunes-1 {
						prev = j
						break
					}
					k++
				}
				return strings.TrimSpace(s[:prev]) + "…"
			}
			return s[:i]
		}
		n++
	}
	return s
}
