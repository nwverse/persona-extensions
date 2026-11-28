package gcp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/coretest"
)

// The fixtures are synthetic: generated keys, example.com identities, documentation addresses.

var (
	keysOnce sync.Once
	testKeys []*rsa.PrivateKey
)

func rsaKeys(t testing.TB) []*rsa.PrivateKey {
	keysOnce.Do(func() {
		for i := 0; i < 2; i++ {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			testKeys = append(testKeys, k)
		}
	})
	return testKeys
}

func pemPKCS8(t testing.TB, k *rsa.PrivateKey) string {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func pemPKCS1(k *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func keyJSON(t testing.TB, project, email, kid, pemText string) string {
	raw, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": project, "private_key_id": kid, "private_key": pemText,
		"client_email": email, "client_id": "100000000000000000001",
		"token_uri": "https://token.example.org/token", // never used
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const (
	emailAlpha = "sa-alpha@example.com"
	emailBeta  = "sa-beta@example.com"
	kidAlpha   = "test-key-id-0001aaaaaaaaaaaaaaaaaaaaaaaa"
	kidBeta    = "test-key-id-0002bbbbbbbbbbbbbbbbbbbbbbbb"
	tokAlpha   = "test-access-token-0001"
	tokBeta    = "test-access-token-0002"
	apiHost    = "https://www.googleapis.com/compute/v1/"
)

var testNow = time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func selfLink(project, zone, name string) string {
	return apiHost + "projects/" + project + "/zones/" + zone + "/instances/" + name
}

func diskLink(project, zone, name string) string {
	return apiHost + "projects/" + project + "/zones/" + zone + "/disks/" + name
}

// vm is a Compute Engine instance as instances.get answers it.
func vm(project, zone, name, status string) map[string]any {
	return map[string]any{
		"kind": "compute#instance", "id": "4000000000000000001", "name": name, "status": status,
		"creationTimestamp": "2026-10-01T08:00:00.000-07:00",
		"zone":              apiHost + "projects/" + project + "/zones/" + zone,
		"machineType":       apiHost + "projects/" + project + "/zones/" + zone + "/machineTypes/e2-standard-2",
		"selfLink":          selfLink(project, zone, name),
		"cpuPlatform":       "Intel Broadwell",
		"networkInterfaces": []any{map[string]any{
			"name": "nic0", "networkIP": "10.128.0.2",
			"accessConfigs": []any{map[string]any{"type": "ONE_TO_ONE_NAT", "name": "External NAT", "natIP": "203.0.113.10"}},
		}},
		"disks": []any{map[string]any{
			"boot": true, "deviceName": name, "source": diskLink(project, zone, name), "diskSizeGb": "20",
			"architecture": "X86_64",
			"licenses":     []any{apiHost + "projects/debian-cloud/global/licenses/debian-12-bookworm"},
		}},
		"scheduling":         map[string]any{"provisioningModel": "STANDARD", "automaticRestart": true},
		"lastStartTimestamp": "2026-11-20T08:00:00.000-08:00",
	}
}

func spot(m map[string]any) map[string]any {
	m["scheduling"] = map[string]any{"provisioningModel": "SPOT", "preemptible": false, "instanceTerminationAction": "STOP"}
	return m
}

func setIP(m map[string]any, ip string) {
	nic := m["networkInterfaces"].([]any)[0].(map[string]any)
	if ip == "" {
		nic["accessConfigs"] = []any{map[string]any{"type": "ONE_TO_ONE_NAT", "name": "External NAT"}}
		return
	}
	nic["accessConfigs"] = []any{map[string]any{"type": "ONE_TO_ONE_NAT", "name": "External NAT", "natIP": ip}}
}

// fake is Google: the token endpoint and the Compute Engine API, asserting what it is asked.
type fake struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	pubs        map[string]*rsa.PublicKey // client_email -> key
	kids        map[string]string
	tokens      map[string]string // client_email -> access token it hands out
	projectTok  map[string]string // project -> the token that may read it
	tokenCalls  int
	tokenAnswer func(w http.ResponseWriter) bool
	calls       []string
	instances   map[string][]map[string]any // project -> VMs
	ops         map[string][]map[string]any // instance self link -> operations
	snapshots   []map[string]any
	pageSize    int
	posts       map[string]int // verb -> count
	nextIP      string
	override    func(w http.ResponseWriter, r *http.Request, rest string) bool
	lastBody    map[string]any
	lastQuery   url.Values
}

func newFake(t *testing.T) *fake {
	keys := rsaKeys(t)
	f := &fake{t: t,
		pubs:       map[string]*rsa.PublicKey{emailAlpha: &keys[0].PublicKey, emailBeta: &keys[1].PublicKey},
		kids:       map[string]string{emailAlpha: kidAlpha, emailBeta: kidBeta},
		tokens:     map[string]string{emailAlpha: tokAlpha, emailBeta: tokBeta},
		projectTok: map[string]string{"proj-alpha": tokAlpha, "proj-gamma": tokAlpha, "proj-beta": tokBeta},
		instances:  map[string][]map[string]any{},
		ops:        map[string][]map[string]any{},
		posts:      map[string]int{},
		pageSize:   500,
		nextIP:     "203.0.113.77",
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func gErr(w http.ResponseWriter, status int, reason, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": status, "message": msg, "errors": []any{map[string]any{"message": msg, "domain": "global", "reason": reason}},
	}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	_ = json.NewEncoder(w).Encode(v)
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	t := f.t
	if r.URL.Path == "/token" {
		f.serveToken(w, r)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/compute/v1/projects/")
	if !ok {
		t.Errorf("unexpected path %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	project, rest, _ := strings.Cut(rest, "/")
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	f.lastQuery = r.URL.Query()
	want := f.projectTok[project]
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+want || want == "" {
		gErr(w, http.StatusUnauthorized, "authError", "Request had invalid authentication credentials.")
		return
	}
	if f.override != nil && f.override(w, r, rest) {
		return
	}
	parts := strings.Split(rest, "/")
	switch {
	case r.Method == http.MethodGet && rest == "aggregated/instances":
		f.serveList(w, r, project)
	case r.Method == http.MethodGet && len(parts) == 4 && parts[0] == "zones" && parts[2] == "instances":
		if m := f.find(project, parts[1], parts[3]); m != nil {
			f.mu.Lock()
			raw, _ := json.Marshal(m)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(raw)
			return
		}
		gErr(w, http.StatusNotFound, "notFound", "The resource 'projects/"+project+"/zones/"+parts[1]+"/instances/"+parts[3]+"' was not found")
	case r.Method == http.MethodPost && len(parts) == 5 && parts[0] == "zones" && parts[2] == "instances":
		f.servePower(w, r, project, parts[1], parts[3], parts[4])
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "zones" && parts[2] == "operations":
		f.serveOps(w, r)
	case r.Method == http.MethodGet && rest == "global/snapshots":
		f.serveSnapshots(w, r)
	case r.Method == http.MethodPost && len(parts) == 5 && parts[0] == "zones" && parts[2] == "disks" && parts[4] == "createSnapshot":
		f.serveCreateSnapshot(w, r, project, parts[1], parts[3])
	default:
		t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
		gErr(w, http.StatusNotFound, "notFound", "no such method")
	}
}

func (f *fake) find(project, zone, name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.instances[project] {
		if m["name"] == name && strings.HasSuffix(m["zone"].(string), "/zones/"+zone) {
			return m
		}
	}
	return nil
}

func (f *fake) serveToken(w http.ResponseWriter, r *http.Request) {
	t := f.t
	f.mu.Lock()
	f.tokenCalls++
	answer := f.tokenAnswer
	f.mu.Unlock()
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Errorf("token: %s %q", r.Method, r.Header.Get("Content-Type"))
	}
	if r.Header.Get("Authorization") != "" {
		t.Errorf("token: an Authorization header was sent")
	}
	body, _ := io.ReadAll(r.Body)
	form, err := url.ParseQuery(string(body))
	if err != nil || form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Errorf("token: bad form %q", body)
	}
	parts := strings.Split(form.Get("assertion"), ".")
	if len(parts) != 3 {
		t.Errorf("token: assertion is not a JWT")
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	var hdr struct{ Alg, Typ, Kid string }
	var claims struct {
		Iss, Scope, Aud string
		Iat, Exp        int64
	}
	dec := func(s string, v any) {
		raw, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Errorf("token: not base64url: %v", err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Errorf("token: %v", err)
		}
	}
	dec(parts[0], &hdr)
	dec(parts[1], &claims)
	f.mu.Lock()
	pub, kid, tok := f.pubs[claims.Iss], f.kids[claims.Iss], f.tokens[claims.Iss]
	f.mu.Unlock()
	if hdr.Alg != "RS256" || hdr.Typ != "JWT" || hdr.Kid != kid {
		t.Errorf("token: header %+v", hdr)
	}
	if claims.Scope != "https://www.googleapis.com/auth/compute" || claims.Aud != f.srv.URL+"/token" ||
		claims.Exp != claims.Iat+3600 || claims.Iat == 0 {
		t.Errorf("token: claims %+v", claims)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if pub == nil || rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`)
		return
	}
	if answer != nil && answer(w) {
		return
	}
	writeJSON(w, map[string]any{"access_token": tok, "expires_in": 3599, "token_type": "Bearer"})
}

func (f *fake) serveList(w http.ResponseWriter, r *http.Request, project string) {
	q := r.URL.Query()
	if q.Get("maxResults") != "500" || q.Get("returnPartialSuccess") != "true" {
		f.t.Errorf("list query %v", q)
	}
	f.mu.Lock()
	all := f.instances[project]
	size := f.pageSize
	f.mu.Unlock()
	start := 0
	if tok := q.Get("pageToken"); tok != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(tok, "page-"))
		if err != nil {
			f.t.Errorf("bad page token %q", tok)
		}
		start = n
	}
	end := min(start+size, len(all))
	items := map[string]any{}
	for _, m := range all[start:end] {
		zone := m["zone"].(string)
		key := "zones/" + zone[strings.LastIndex(zone, "/")+1:]
		e, _ := items[key].(map[string]any)
		if e == nil {
			e = map[string]any{"instances": []any{}}
		}
		e["instances"] = append(e["instances"].([]any), m)
		items[key] = e
	}
	items["zones/europe-west1-b"] = map[string]any{"warning": map[string]any{"code": "NO_RESULTS_ON_PAGE", "message": "There are no results for scope 'zones/europe-west1-b' on this page."}}
	ans := map[string]any{"kind": "compute#instanceAggregatedList", "id": "projects/" + project + "/aggregated/instances", "items": items}
	if end < len(all) {
		ans["nextPageToken"] = "page-" + strconv.Itoa(end)
	}
	f.mu.Lock()
	raw, _ := json.Marshal(ans)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (f *fake) servePower(w http.ResponseWriter, r *http.Request, project, zone, name, verb string) {
	q := r.URL.Query()
	if !uuidRe.MatchString(q.Get("requestId")) {
		f.t.Errorf("%s: requestId %q is not a UUID", verb, q.Get("requestId"))
	}
	m := f.find(project, zone, name)
	if m == nil {
		gErr(w, http.StatusNotFound, "notFound", "The resource 'projects/"+project+"/zones/"+zone+"/instances/"+name+"' was not found")
		return
	}
	f.mu.Lock()
	f.posts[verb]++
	status, _ := m["status"].(string)
	switch verb {
	case "start":
		if status == "SUSPENDED" {
			f.mu.Unlock()
			f.t.Errorf("start sent to a SUSPENDED VM; Compute Engine wakes it only with resume")
			gErr(w, http.StatusBadRequest, "resourceNotReady", "The resource is not ready")
			return
		}
		m["status"] = "RUNNING"
		m["lastStartTimestamp"] = "2026-11-27T02:00:00.000-08:00"
		setIP(m, f.nextIP)
	case "resume":
		if status != "SUSPENDED" {
			f.mu.Unlock()
			gErr(w, http.StatusBadRequest, "resourceNotReady", "The resource is not ready")
			return
		}
		m["status"] = "RUNNING"
		m["lastStartTimestamp"] = "2026-11-27T02:00:00.000-08:00"
		setIP(m, f.nextIP)
	case "stop":
		m["status"] = "STOPPING"
	case "reset":
	default:
		f.t.Errorf("unexpected verb %s", verb)
	}
	f.mu.Unlock()
	writeJSON(w, map[string]any{"kind": "compute#operation", "id": "7000000000000000001", "name": "operation-1764237600000-" + verb,
		"zone": apiHost + "projects/" + project + "/zones/" + zone, "operationType": verb, "targetLink": selfLink(project, zone, name),
		"status": "RUNNING", "progress": 0, "insertTime": "2026-11-27T02:00:00.000-08:00"})
}

var targetRe = regexp.MustCompile(`targetLink="([^"]+)"`)

func (f *fake) serveOps(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	if !strings.Contains(filter, `(operationType="compute.instances.preempted")`) {
		f.t.Errorf("operations filter %q", filter)
	}
	m := targetRe.FindStringSubmatch(filter)
	if m == nil {
		f.t.Errorf("operations filter without targetLink: %q", filter)
		writeJSON(w, map[string]any{})
		return
	}
	// Google lists by name unless asked otherwise; only "creationTimestamp desc" puts the newest first.
	q := r.URL.Query()
	if q.Get("orderBy") != "creationTimestamp desc" {
		f.t.Errorf("operations orderBy %q", q.Get("orderBy"))
	}
	limit, err := strconv.Atoi(q.Get("maxResults"))
	if err != nil || limit < 1 || limit > 500 {
		f.t.Errorf("operations maxResults %q", q.Get("maxResults"))
		limit = 500
	}
	f.mu.Lock()
	items := slices.Clone(f.ops[m[1]])
	f.mu.Unlock()
	if q.Get("orderBy") == "creationTimestamp desc" {
		sort.SliceStable(items, func(i, j int) bool {
			return fmt.Sprint(items[i]["insertTime"]) > fmt.Sprint(items[j]["insertTime"])
		})
	} else {
		sort.SliceStable(items, func(i, j int) bool { return fmt.Sprint(items[i]["name"]) < fmt.Sprint(items[j]["name"]) })
	}
	if len(items) > limit {
		items = items[:limit]
	}
	writeJSON(w, map[string]any{"kind": "compute#operationList", "items": items})
}

func (f *fake) serveSnapshots(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	var out []any
	f.mu.Lock()
	for _, s := range f.snapshots {
		if filter == `sourceDisk="`+s["sourceDisk"].(string)+`"` {
			out = append(out, s)
		}
	}
	f.mu.Unlock()
	writeJSON(w, map[string]any{"kind": "compute#snapshotList", "items": out})
}

func (f *fake) serveCreateSnapshot(w http.ResponseWriter, r *http.Request, project, zone, disk string) {
	if !uuidRe.MatchString(r.URL.Query().Get("requestId")) {
		f.t.Errorf("createSnapshot: requestId %q", r.URL.Query().Get("requestId"))
	}
	if r.Header.Get("Content-Type") != "application/json" {
		f.t.Errorf("createSnapshot content type %q", r.Header.Get("Content-Type"))
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.t.Errorf("createSnapshot body: %v", err)
	}
	f.mu.Lock()
	f.lastBody = body
	f.posts["createSnapshot"]++
	f.mu.Unlock()
	writeJSON(w, map[string]any{"kind": "compute#operation", "name": "operation-snap", "operationType": "createSnapshot",
		"targetLink": diskLink(project, zone, disk), "status": "PENDING"})
}

func (f *fake) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fake) add(project string, m map[string]any) {
	f.mu.Lock()
	f.instances[project] = append(f.instances[project], m)
	f.mu.Unlock()
}

// setup is the adapter on fake with the alpha and beta keys (and gamma read with the alpha key).
type setup struct {
	f       *fake
	ad      core.Adapter
	p       *core.Provider
	clock   *clock
	secrets []string
}

func newSetup(t *testing.T, mutate ...func(*Config)) *setup {
	f := newFake(t)
	keys := rsaKeys(t)
	pa, pb := pemPKCS8(t, keys[0]), pemPKCS1(keys[1])
	c := &clock{t: testNow}
	cfg := Config{
		ServiceAccounts: keyJSON(t, "proj-alpha", emailAlpha, kidAlpha, pa) + "\n" + keyJSON(t, "proj-beta", emailBeta, kidBeta, pb),
		Projects:        []string{"proj-gamma"},
		HTTP:            f.srv.Client(),
		Now:             c.now,
		ComputeBase:     f.srv.URL + "/compute/v1",
		TokenURL:        f.srv.URL + "/token",
	}
	for _, m := range mutate {
		m(&cfg)
	}
	ad, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	secrets := []string{pa, pb, kidAlpha, kidBeta, tokAlpha, tokBeta}
	for _, k := range []string{pa, pb} {
		lines := strings.Split(strings.TrimSpace(k), "\n")
		secrets = append(secrets, lines[1], lines[len(lines)/2], lines[len(lines)-2])
	}
	return &setup{f: f, ad: ad, p: coretest.Provider(ad, nil), clock: c, secrets: secrets}
}
