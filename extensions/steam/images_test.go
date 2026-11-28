package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/exttest"
	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	fixtureAvatarHex  = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	fixtureAvatarHex2 = "1234567890abcdef1234567890abcdef12345678"
)

func syntheticPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func syntheticJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fakeCDN struct {
	server *httptest.Server
	client *http.Client

	mu      sync.Mutex
	hits    map[string]int
	handler func(http.ResponseWriter, *http.Request)
}

func newFakeCDN(t *testing.T, timeout time.Duration, handler func(http.ResponseWriter, *http.Request)) *fakeCDN {
	t.Helper()
	c := &fakeCDN{hits: map[string]int{}, handler: handler}
	c.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Header.Get("X-Test-Original-Host")
		c.mu.Lock()
		c.hits[host+r.URL.Path]++
		h := c.handler
		c.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(c.server.Close)
	u, err := url.Parse(c.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet || req.URL.Scheme != "https" ||
			(req.URL.Host != assetCDNHost && req.URL.Host != avatarCDNHost) {
			t.Errorf("unexpected CDN request %s %s", req.Method, req.URL.String())
			return nil, fmt.Errorf("unexpected cdn origin")
		}
		if req.Header.Get("X-WebAPI-Key") != "" || req.Header.Get("Authorization") != "" ||
			req.Header.Get("Cookie") != "" || req.URL.Query().Has("access_token") {
			t.Error("image proxy sent credentials to a public CDN")
			return nil, fmt.Errorf("unexpected credential")
		}
		clone := req.Clone(req.Context())
		clone.Header = req.Header.Clone()
		clone.Header.Set("X-Test-Original-Host", req.URL.Host)
		clone.URL.Scheme, clone.URL.Host = u.Scheme, u.Host
		return c.server.Client().Transport.RoundTrip(clone)
	})
	c.client = &http.Client{Transport: transport, Timeout: timeout}
	return c
}

func (c *fakeCDN) hit(host, path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[host+path]
}

func TestImageProxyArtworkAndAvatars(t *testing.T) {
	headerImg := syntheticJPEG(t, 46, 22, color.RGBA{R: 32, G: 120, B: 220, A: 255})
	capsuleImg := syntheticPNG(t, 23, 9, color.RGBA{R: 50, G: 180, B: 90, A: 255})
	avatarImg := syntheticJPEG(t, 18, 18, color.RGBA{R: 220, G: 140, B: 40, A: 255})

	storeBrowseHeader := "/store_item_assets/steam/apps/" + appIDText(firstApp) + "/customhash/header_schinese.jpg"
	storeBrowseCapsule := "/store_item_assets/steam/apps/" + appIDText(firstApp) + "/customhash/capsule_231x87_schinese.jpg"
	stdHeader2 := "/" + standardHeaderPath(secondApp)
	stdCapsule2 := "/" + standardCapsulePath(secondApp)

	cdn := newFakeCDN(t, 2*time.Second, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case storeBrowseHeader:
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(headerImg)
		case storeBrowseCapsule:
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(capsuleImg)
		case stdHeader2:
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(headerImg)
		case stdCapsule2:
			// Simulate missing capsule_231x87.jpg to verify fallback to standard header.jpg.
			http.NotFound(w, r)
		case "/" + fixtureAvatarHex + "_full.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(avatarImg)
		case "/" + fixtureAvatarHex2 + "_full.jpg":
			http.NotFound(w, r)
		case "/" + fixtureAvatarHex2 + "_medium.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(avatarImg)
		default:
			http.NotFound(w, r)
		}
	})

	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	proxy := newImageProxy(cdn.client, func() time.Time { return now })
	art := resolveStoreArtwork(&storeAssets{
		URLFormat:    "steam/apps/" + appIDText(firstApp) + "/customhash/${FILENAME}",
		Header:       "header_schinese.jpg?t=1700000000",
		SmallCapsule: "capsule_231x87_schinese.jpg?t=1700000000",
	})
	if art == nil {
		t.Fatal("failed to resolve valid StoreBrowse artwork")
	}
	proxy.recordArtwork(map[uint32]appArtwork{firstApp: *art})

	for _, tc := range []struct {
		ref      string
		wantMime string
		wantW    int
	}{
		{appHeaderRef(firstApp), "image/jpeg", 46},
		{appCapsuleRef(firstApp), "image/png", 23},
		{appCapsuleRef(secondApp), "image/jpeg", 46},
		{avatarRef(fixtureAvatarHex), "image/jpeg", 18},
		{avatarRef(fixtureAvatarHex2), "image/jpeg", 18},
	} {
		img, err := proxy.serve(t.Context(), tc.ref)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		checked, err := wire.CheckImage(img)
		if err != nil {
			t.Fatalf("%s failed wire.CheckImage: %v", tc.ref, err)
		}
		if checked.Mime != tc.wantMime || checked.Width != tc.wantW || checked.MaxAge != wire.ImageMaxAgeMost {
			t.Errorf("%s: got mime=%s w=%d maxAge=%d, want %s %d %d", tc.ref, checked.Mime, checked.Width, checked.MaxAge, tc.wantMime, tc.wantW, wire.ImageMaxAgeMost)
		}
	}
	if cdn.hit(assetCDNHost, storeBrowseHeader) != 1 || cdn.hit(assetCDNHost, storeBrowseCapsule) != 1 {
		t.Fatal("StoreBrowse artwork paths were not preferred")
	}
	if cdn.hit(assetCDNHost, stdCapsule2) != 1 || cdn.hit(assetCDNHost, stdHeader2) != 1 {
		t.Fatal("missing standard capsule did not fall back to standard header")
	}
	if cdn.hit(avatarCDNHost, "/"+fixtureAvatarHex2+"_full.jpg") != 1 || cdn.hit(avatarCDNHost, "/"+fixtureAvatarHex2+"_medium.jpg") != 1 {
		t.Fatal("missing full avatar did not fall back to medium avatar")
	}
}

func TestImageProxySecurityAndSSRFRejection(t *testing.T) {
	var outbound atomic.Int32
	cdn := newFakeCDN(t, time.Second, func(w http.ResponseWriter, r *http.Request) {
		outbound.Add(1)
		// Redirect attempt to an untrusted host must be refused by CheckRedirect.
		http.Redirect(w, r, "https://evil.example/payload.png", http.StatusFound)
	})
	proxy := newImageProxy(cdn.client, time.Now)

	badRefs := []string{
		"",
		"https://shared.fastly.steamstatic.com/store_item_assets/steam/apps/570/header.jpg",
		"http://127.0.0.1:8080/internal",
		"file:///etc/passwd",
		"app-header:0",
		"app-header:0123",
		"app-header:-1",
		"app-header:570/../secret",
		"app-header:570?foo=bar",
		"app-header:4294967296",
		"app-capsule:abc",
		"avatar:short",
		"avatar:" + strings.Repeat("0", 40),
		"avatar:" + strings.ToUpper(fixtureAvatarHex),
		"avatar:" + fixtureAvatarHex + "/../x",
		"unknown:123",
		strings.Repeat("a", 200),
	}
	for _, ref := range badRefs {
		var extErr *ext.Error
		if _, err := proxy.serve(t.Context(), ref); !errors.As(err, &extErr) || extErr.Code != "invalid" {
			t.Errorf("ref %q should be rejected as invalid, got %v", ref, err)
		}
	}
	if outbound.Load() != 0 {
		t.Fatalf("invalid refs triggered %d outbound requests", outbound.Load())
	}

	// Verify StoreBrowse asset sanitization rejects traversal, schemes, and non-images.
	for _, bad := range []*storeAssets{
		{URLFormat: "https://evil.example/${FILENAME}", Header: "a.jpg"},
		{URLFormat: "steam/apps/570/${FILENAME}", Header: "../../etc/passwd.jpg"},
		{URLFormat: "steam/apps/570/${FILENAME}", Header: "evil.svg"},
		{URLFormat: "other/prefix/${FILENAME}", Header: "header.jpg"},
		{URLFormat: "steam/apps/570/${FILENAME}", Header: "bad space.jpg"},
	} {
		if got := resolveStoreArtwork(bad); got != nil {
			t.Errorf("unsafe StoreBrowse asset %+v was accepted: %+v", bad, got)
		}
	}

	// Verify HTTP redirect is not followed.
	if _, err := proxy.serve(t.Context(), appHeaderRef(firstApp)); err == nil {
		t.Fatal("redirect response was accepted as an image")
	}
}

func TestImageProxyValidationLimitsAndDegradation(t *testing.T) {
	cdn := newFakeCDN(t, 250*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + standardHeaderPath(firstApp):
			// Oversized payload > maxImageBytes.
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(bytes.Repeat([]byte("A"), maxImageBytes+64))
		case "/" + standardHeaderPath(secondApp):
			// SVG payload (must be rejected).
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
		case "/" + standardHeaderPath(thirdApp):
			// Slow response exceeding client timeout.
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	})
	proxy := newImageProxy(cdn.client, time.Now)

	for _, tc := range []struct {
		name string
		ref  string
		code string
	}{
		{"oversize", appHeaderRef(firstApp), "too_large"},
		{"svg", appHeaderRef(secondApp), "invalid_image"},
		{"timeout", appHeaderRef(thirdApp), string(problemTimeout)},
		{"not found", appHeaderRef(thirdApp + 1), "not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var extErr *ext.Error
			_, err := proxy.serve(t.Context(), tc.ref)
			if !errors.As(err, &extErr) || extErr.Code != tc.code {
				t.Fatalf("got err=%v, want code %q", err, tc.code)
			}
			if strings.Contains(extErr.Message, assetCDNHost) || strings.Contains(extErr.Message, "https://") {
				t.Fatalf("error leaked upstream URL: %q", extErr.Message)
			}
		})
	}
}

func TestImageProxyCacheTTLEvictionAndCoalescing(t *testing.T) {
	pngBytes := syntheticPNG(t, 12, 6, color.RGBA{R: 10, G: 200, B: 120, A: 255})
	gate := make(chan struct{})
	var firstFetchStarted atomic.Bool
	cdn := newFakeCDN(t, 2*time.Second, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/"+standardHeaderPath(firstApp) && firstFetchStarted.CompareAndSwap(false, true) {
			<-gate
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})

	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	proxy := newImageProxy(cdn.client, func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	})

	// Coalesce concurrent requests for the same ref into one upstream call.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			img, err := proxy.serve(t.Context(), appHeaderRef(firstApp))
			if err != nil {
				errs <- err
				return
			}
			if _, err := wire.CheckImage(img); err != nil {
				errs <- err
			}
		})
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if hits := cdn.hit(assetCDNHost, "/"+standardHeaderPath(firstApp)); hits != 1 {
		t.Fatalf("concurrent requests made %d upstream fetches, want 1", hits)
	}

	// Cache hit within TTL should not refetch and should return remaining MaxAge.
	clockMu.Lock()
	now = now.Add(time.Hour)
	clockMu.Unlock()
	img, err := proxy.serve(t.Context(), appHeaderRef(firstApp))
	if err != nil {
		t.Fatal(err)
	}
	if img.MaxAge == nil || *img.MaxAge != int((imageCacheTTL-time.Hour).Seconds()) {
		t.Fatalf("cached image max_age = %v, want %d", img.MaxAge, int((imageCacheTTL - time.Hour).Seconds()))
	}
	if hits := cdn.hit(assetCDNHost, "/"+standardHeaderPath(firstApp)); hits != 1 {
		t.Fatalf("cache hit refetched upstream (%d hits)", hits)
	}

	// After TTL expires, next request refreshes.
	clockMu.Lock()
	now = now.Add(imageCacheTTL)
	clockMu.Unlock()
	if _, err := proxy.serve(t.Context(), appHeaderRef(firstApp)); err != nil {
		t.Fatal(err)
	}
	if hits := cdn.hit(assetCDNHost, "/"+standardHeaderPath(firstApp)); hits != 2 {
		t.Fatalf("expired entry did not refetch (%d hits)", hits)
	}

	// Bounded cache eviction keeps count <= imageCacheMaxItems.
	for i := range imageCacheMaxItems + 10 {
		proxy.putCached(fmt.Sprintf("app-header:%d", i+1), "image/png", pngBytes)
	}
	proxy.mu.Lock()
	count := len(proxy.cache)
	proxy.mu.Unlock()
	if count > imageCacheMaxItems {
		t.Fatalf("cache exceeded max items: %d > %d", count, imageCacheMaxItems)
	}
}

func TestImageProxyDoesNotShareAPISlots(t *testing.T) {
	pngBytes := syntheticPNG(t, 16, 8, color.RGBA{R: 80, G: 90, B: 200, A: 255})
	cdn := newFakeCDN(t, 2*time.Second, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	})
	wrapped := steamHTTP(cdn.client.Transport, 2*time.Second)
	st := wrapped.Transport.(*steamTransport)
	// Fill every API and Store slot to verify imageProxy bypasses steamTransport slots.
	for range steamConcurrency {
		st.api <- struct{}{}
		st.store <- struct{}{}
	}
	defer func() {
		for range steamConcurrency {
			<-st.api
			<-st.store
		}
	}()

	proxy := newImageProxy(wrapped, time.Now)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := proxy.serve(ctx, appHeaderRef(firstApp)); err != nil {
		t.Fatalf("image fetch was blocked by saturated API/Store slots: %v", err)
	}
}

func TestAvatarParsingBothAuthModes(t *testing.T) {
	// Key mode URL parsing
	if got := parseAvatarURL("https://avatars.steamstatic.com/" + fixtureAvatarHex + "_full.jpg"); got != fixtureAvatarHex {
		t.Fatalf("avatarfull parse = %q", got)
	}
	if got := parseAvatarURL("https://evil.example/"+fixtureAvatarHex+"_full.jpg", "https://avatars.akamai.steamstatic.com/"+fixtureAvatarHex2+"_medium.jpg"); got != fixtureAvatarHex2 {
		t.Fatalf("avatarmedium fallback parse = %q", got)
	}
	if got := parseAvatarURL("http://avatars.steamstatic.com/" + fixtureAvatarHex + "_full.jpg"); got != "" {
		t.Fatalf("non-HTTPS avatar URL was accepted: %q", got)
	}

	// QR mode sha_digest_avatar parsing (base64 20-byte SHA-1 and 40-char hex)
	rawBytes, err := hex.DecodeString(fixtureAvatarHex)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.StdEncoding.EncodeToString(rawBytes)
	if got := parseAvatarDigest(b64); got != fixtureAvatarHex {
		t.Fatalf("base64 sha_digest_avatar parse = %q, want %q", got, fixtureAvatarHex)
	}
	if got := parseAvatarDigest(strings.ToUpper(fixtureAvatarHex2)); got != fixtureAvatarHex2 {
		t.Fatalf("hex sha_digest_avatar parse = %q, want %q", got, fixtureAvatarHex2)
	}
	if got := parseAvatarDigest(base64.StdEncoding.EncodeToString(make([]byte, 20))); got != "" {
		t.Fatalf("all-zero sha_digest_avatar was accepted: %q", got)
	}

	// End-to-end profile parsing in Web API key mode
	f := newFakeSteam(t, time.Second)
	f.reply(playerPath, http.StatusOK, fmt.Sprintf(`{"response":{"players":[{"steamid":%q,"personaname":"Fixture Pilot","personastate":1,"communityvisibilitystate":3,"avatarfull":"https://avatars.steamstatic.com/%s_full.jpg"}]}}`, fixtureID, fixtureAvatarHex))
	p, issue := (steamClient{http: f.http, key: f.key}).profile(t.Context(), fixtureID)
	if issue != "" || p == nil || p.AvatarHash != fixtureAvatarHex {
		t.Fatalf("GetPlayerSummaries avatar hash = %+v, issue=%s", p, issue)
	}

	// End-to-end playerLink parsing in QR mode
	qrFake := newFakeSteamMode(t, time.Second, "synthetic-access")
	qrFake.reply(playerLinkPath, http.StatusOK, fmt.Sprintf(`{"response":{"accounts":[{"public_data":{"steamid":%q,"persona_name":"QR Pilot","visibility_state":3,"sha_digest_avatar":%q}}]}}`, fixtureID, b64))
	qp, issue := (steamClient{http: qrFake.http, access: "synthetic-access"}).playerLink(t.Context(), fixtureID)
	if issue != "" || qp == nil || qp.AvatarHash != fixtureAvatarHex {
		t.Fatalf("GetPlayerLinkDetails avatar hash = %+v, issue=%s", qp, issue)
	}
}

func TestFullVisualViewsAndUIImageConformance(t *testing.T) {
	headerBytes := syntheticJPEG(t, 46, 22, color.RGBA{R: 40, G: 90, B: 180, A: 255})
	capsuleBytes := syntheticPNG(t, 23, 9, color.RGBA{R: 60, G: 160, B: 120, A: 255})
	avatarBytes := syntheticJPEG(t, 18, 18, color.RGBA{R: 200, G: 120, B: 60, A: 255})

	f := newFakeSteam(t, 2*time.Second)
	longName := strings.Repeat("Super Long Adventure Title ", 3) // > 40 runes to verify Tile title truncation
	f.reply(playerPath, http.StatusOK, fmt.Sprintf(`{"response":{"players":[{"steamid":%q,"personaname":"Fixture Pilot","personastate":1,"communityvisibilitystate":3,"gameextrainfo":%q,"gameid":%q,"avatarfull":"https://avatars.steamstatic.com/%s_full.jpg"}]}}`, fixtureID, longName, appIDText(firstApp), fixtureAvatarHex))
	f.reply(recentPath, http.StatusOK, fmt.Sprintf(`{"response":{"total_count":2,"games":[{"appid":%d,"name":%q,"playtime_2weeks":95,"playtime_forever":320},{"appid":%d,"name":"Zeta Rally","playtime_2weeks":30,"playtime_forever":600}]}}`, firstApp, longName, secondApp))

	// Combine fakeSteam API transport with synthetic CDN server for assetCDNHost & avatarCDNHost.
	cdn := newFakeCDN(t, 2*time.Second, func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Test-Original-Host") {
		case avatarCDNHost:
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(avatarBytes)
		case assetCDNHost:
			if strings.Contains(r.URL.Path, "capsule") {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(capsuleBytes)
			} else {
				w.Header().Set("Content-Type", "image/jpeg")
				_, _ = w.Write(headerBytes)
			}
		default:
			http.NotFound(w, r)
		}
	})
	apiTransport := f.http.Transport
	combined := &http.Client{
		Timeout: 2 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == assetCDNHost || req.URL.Host == avatarCDNHost {
				return cdn.client.Transport.RoundTrip(req)
			}
			return apiTransport.RoundTrip(req)
		}),
	}

	dir := t.TempDir()
	t.Setenv("PERSONA_EXTENSION_DATA_DIR", dir)
	h := exttest.StartWith(t, manifest, exttest.Serve(t, program(combined, time.Now)), func(p *wire.InitializeParams) {
		p.Config = json.RawMessage(fmt.Sprintf(`{"steam_id":%q}`, fixtureID))
		p.Secrets = map[string]string{keyName: f.key}
		p.Hub.Locale = "zh-Hans"
	})

	for _, locale := range []string{"zh-Hans", "en-US"} {
		nav := wire.UIParams{Contribution: extensionID, Size: wire.SizeFull, Placement: wire.PlacementNavPage, UIVersion: 12, Locale: locale}
		overview := viewAnswer(t, h, nav)
		refs := wire.ImageRefs(&overview)
		if len(refs) == 0 {
			t.Fatalf("locale %s overview emitted no image refs", locale)
		}
		for _, ref := range refs {
			var img wire.UIImage
			if err := h.Call("ui.image", wire.UIImageParams{Contribution: extensionID, Ref: ref, Width: 640}, &img); err != nil {
				t.Fatalf("ui.image(%s) failed: %v", ref, err)
			}
			if _, err := wire.CheckImage(&img); err != nil {
				t.Fatalf("ui.image(%s) failed wire.CheckImage: %v", ref, err)
			}
		}

		// Verify clicking a recent gallery tile opens detail with hero banner.
		detail := act(t, h, nav, overview, overview.Sections[1].Items[0].Tiles[0].Action.ID, nil)
		if detail.Sections[0].Items[0].Type != wire.ComponentImage {
			t.Fatalf("detail view did not open with hero image")
		}
		for _, ref := range wire.ImageRefs(&detail) {
			var img wire.UIImage
			if err := h.Call("ui.image", wire.UIImageParams{Contribution: extensionID, Ref: ref, Width: 640}, &img); err != nil {
				t.Fatalf("detail ui.image(%s) failed: %v", ref, err)
			}
			if _, err := wire.CheckImage(&img); err != nil {
				t.Fatalf("detail ui.image(%s) failed wire.CheckImage: %v", ref, err)
			}
		}

		// Verify summary overview card and its ui.image contribution handler.
		cardParams := wire.UIParams{Contribution: summaryID, Size: wire.SizeCompact, Placement: wire.PlacementOverviewCard, UIVersion: 12, Locale: locale}
		card := viewAnswer(t, h, cardParams)
		cardRefs := wire.ImageRefs(&card)
		if len(cardRefs) != 1 || cardRefs[0] != avatarRef(fixtureAvatarHex) {
			t.Fatalf("summary card refs = %v, want [%s]", cardRefs, avatarRef(fixtureAvatarHex))
		}
		var img wire.UIImage
		if err := h.Call("ui.image", wire.UIImageParams{Contribution: summaryID, Ref: cardRefs[0], Width: 320}, &img); err != nil {
			t.Fatalf("summary ui.image(%s) failed: %v", cardRefs[0], err)
		}
		if _, err := wire.CheckImage(&img); err != nil {
			t.Fatalf("summary ui.image(%s) failed wire.CheckImage: %v", cardRefs[0], err)
		}

		// Verify playtime recap page renders wide thumbnails and passes wire.CheckView.
		recap := act(t, h, nav, overview, recapAction, nil)
		if len(wire.ImageRefs(&recap)) == 0 {
			t.Fatalf("locale %s recap emitted no thumbnail refs", locale)
		}
	}

	// Verify wishlist visual states: discount (-XX%), free, unpriced, and pending price.
	wl := &wishList{
		Total: 4, Checked: 3, Discounted: 1,
		Items: []wishItem{
			{AppID: firstApp, Name: "Sale Game", PriceChecked: true, Price: &storePrice{Currency: "USD", Final: 999, FinalFormatted: "$9.99", DiscountPercent: 50}},
			{AppID: secondApp, Name: "Free Game", PriceChecked: true, IsFree: true},
			{AppID: thirdApp, Name: "Unpriced Game", PriceChecked: true},
			{AppID: thirdApp + 1, Name: "Pending Game", PriceChecked: false},
		},
	}
	for _, loc := range []*text{&english, &chinese} {
		sec := wishlistSection(loc, reading[wishList]{Data: wl}, &pageState{})
		if err := wire.CheckView(&wire.View{Title: "Steam", Sections: []wire.Section{sec}}); err != nil {
			t.Fatalf("wishlist states failed wire.CheckView: %v", err)
		}
		if sec.Items[0].Badge == nil || sec.Items[0].Badge.Text != "-50%" || sec.Items[0].Badge.Tone != wire.TonePositive {
			t.Fatalf("sale item missing -50%% positive badge: %+v", sec.Items[0].Badge)
		}
		if sec.Items[1].Value != loc.freeToPlay || sec.Items[1].Badge == nil || sec.Items[1].Badge.Tone != wire.TonePositive {
			t.Fatalf("free item missing positive visual state: %+v", sec.Items[1])
		}
		if sec.Items[2].Subtitle != loc.noPrice || sec.Items[2].Badge == nil || sec.Items[2].Badge.Text != loc.unpricedBadge {
			t.Fatalf("unpriced item missing unpriced badge: %+v", sec.Items[2])
		}
		if sec.Items[3].Subtitle != loc.priceUnchecked || sec.Items[3].Badge == nil || sec.Items[3].Badge.Tone != wire.ToneCaution {
			t.Fatalf("pending item missing caution badge: %+v", sec.Items[3])
		}
	}

	// Verify graceful degradation when CDN is unreachable: page still renders completely.
	brokenCDN := &http.Client{
		Timeout: 2 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host == assetCDNHost || req.URL.Host == avatarCDNHost {
				return nil, fmt.Errorf("simulated cdn failure")
			}
			return apiTransport.RoundTrip(req)
		}),
	}
	hBroken := exttest.StartWith(t, manifest, exttest.Serve(t, program(brokenCDN, time.Now)), func(p *wire.InitializeParams) {
		p.Config = json.RawMessage(fmt.Sprintf(`{"steam_id":%q}`, fixtureID))
		p.Secrets = map[string]string{keyName: f.key}
		p.Hub.Locale = "zh-Hans"
	})
	nav := wire.UIParams{Contribution: extensionID, Size: wire.SizeFull, Placement: wire.PlacementNavPage, UIVersion: 12, Locale: "zh-Hans"}
	degradedView := viewAnswer(t, hBroken, nav)
	if len(degradedView.Sections) != 5 {
		t.Fatalf("CDN outage blocked or truncated page rendering: %d sections", len(degradedView.Sections))
	}
	var img wire.UIImage
	if err := hBroken.Call("ui.image", wire.UIImageParams{Contribution: extensionID, Ref: appHeaderRef(firstApp)}, &img); err == nil {
		t.Fatal("expected clean error from ui.image when CDN is unreachable")
	}
}
