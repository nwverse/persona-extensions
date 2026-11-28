package oci

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Synthetic identities: none of them exists at Oracle.
const (
	tenancy1 = "ocid1.tenancy.oc1..aaaatesttenancy0001"
	tenancy2 = "ocid1.tenancy.oc1..aaaatesttenancy0002"
	tenancy3 = "ocid1.tenancy.oc1..aaaatesttenancy0003"
	user1    = "ocid1.user.oc1..aaaatestuser0001"
	user2    = "ocid1.user.oc1..aaaatestuser0002"
	user3    = "ocid1.user.oc1..aaaatestuser0003"
	fp1      = "12:34:56:78:90:ab:cd:ef:12:34:56:78:90:ab:cd:01"
	fp2      = "12:34:56:78:90:ab:cd:ef:12:34:56:78:90:ab:cd:02"
	fp3      = "12:34:56:78:90:ab:cd:ef:12:34:56:78:90:ab:cd:03"

	idRunning    = "ocid1.instance.oc1.iad.aaaarunning01"
	idStopped    = "ocid1.instance.oc1.iad.aaaastopped02"
	idTerminated = "ocid1.instance.oc1.iad.aaaaterminated03"
	idPreempted  = "ocid1.instance.oc1.iad.aaaapreempted04"
	idStarting   = "ocid1.instance.oc1.iad.aaaastarting05"
	idMoving     = "ocid1.instance.oc1.eu-frankfurt-1.aaaamoving06"
	idSpot       = "ocid1.instance.oc1.eu-frankfurt-1.aaaaspot07"
)

var testNow = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

type fakeInst struct {
	region string
	data   map[string]any
	vnics  []map[string]any
}

// fakeOCI is Core Services and Monitoring as far as the adapter uses them. Every request must be signed by a known
// key; the base path is /<service>/<region>.
type fakeOCI struct {
	t        *testing.T
	srv      *httptest.Server
	pubs     map[string]*rsa.PublicKey
	pageSize int

	mu       sync.Mutex
	insts    []*fakeInst
	comps    map[string]bool // region|compartment that exist
	calls    []string        // "METHOD service/region path?query"
	actions  []string        // "id ACTION"
	tokens   []string        // opc-retry-token of each action
	metricRq []map[string]string
	metrics  map[string][]float64
	hook     func(service, region, method, path string, q url.Values) (int, string, bool)
}

func newFake(t *testing.T) *fakeOCI {
	f := &fakeOCI{t: t, pubs: map[string]*rsa.PublicKey{}, pageSize: 2, comps: map[string]bool{},
		metrics: map[string][]float64{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOCI) endpoint(service, region string) string {
	return f.srv.URL + "/" + service + "/" + region
}

func (f *fakeOCI) add(region, comp string, data map[string]any, vnics ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data["compartmentId"] = comp
	f.comps[region+"|"+comp] = true
	f.insts = append(f.insts, &fakeInst{region: region, data: data, vnics: vnics})
}

func (f *fakeOCI) find(region, id string) *fakeInst {
	for _, in := range f.insts {
		if in.region == region && in.data["id"] == id {
			return in
		}
	}
	return nil
}

// set changes an instance's field (state, a VNIC's address) between calls.
func (f *fakeOCI) set(id string, change func(in *fakeInst)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, in := range f.insts {
		if in.data["id"] == id {
			change(in)
		}
	}
}

func (f *fakeOCI) callsMatching(sub string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			out = append(out, c)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ociErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "message": msg})
}

func (f *fakeOCI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if _, err := verifySigned(r, body, f.pubs); err != nil {
		f.t.Errorf("%s %s: %v", r.Method, r.URL.Path, err)
		ociErr(w, 401, "NotAuthenticated", "The required information to complete authentication was not provided or was incorrect.")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(parts) != 3 {
		ociErr(w, 404, "NotFound", "no such path")
		return
	}
	service, region, path := parts[0], parts[1], "/"+parts[2]
	q := r.URL.Query()
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+service+"/"+region+" "+path+"?"+r.URL.RawQuery)
	hook := f.hook
	f.mu.Unlock()
	if hook != nil {
		if status, b, ok := hook(service, region, r.Method, path, q); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, b)
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case service == "iaas" && r.Method == http.MethodGet && path == "/20160918/instances":
		comp := q.Get("compartmentId")
		if !f.comps[region+"|"+comp] {
			ociErr(w, 404, "NotAuthorizedOrNotFound", "Authorization failed or requested resource not found.")
			return
		}
		if q.Get("limit") != "100" {
			f.t.Errorf("list limit %q", q.Get("limit"))
		}
		var all []map[string]any
		for _, in := range f.insts {
			if in.region == region && in.data["compartmentId"] == comp {
				all = append(all, in.data)
			}
		}
		start, _ := strconv.Atoi(q.Get("page"))
		end := min(start+f.pageSize, len(all))
		if end < len(all) {
			w.Header().Set("opc-next-page", strconv.Itoa(end))
		}
		writeJSON(w, 200, append([]map[string]any{}, all[start:end]...))
	case service == "iaas" && strings.HasPrefix(path, "/20160918/instances/"):
		id := strings.TrimPrefix(path, "/20160918/instances/")
		in := f.find(region, id)
		if in == nil {
			ociErr(w, 404, "NotAuthorizedOrNotFound", "Authorization failed or requested resource not found.")
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(w, 200, in.data)
			return
		}
		if r.Method != http.MethodPost || len(body) != 0 {
			f.t.Errorf("action: %s with body %q", r.Method, body)
		}
		tok := r.Header.Get("Opc-Retry-Token")
		if tok == "" || len(tok) > 64 {
			f.t.Errorf("opc-retry-token %q", tok)
		}
		action := q.Get("action")
		next := map[string]string{"START": "STARTING", "STOP": "STOPPING", "SOFTSTOP": "STOPPING", "RESET": "RUNNING", "SOFTRESET": "RUNNING"}[action]
		if next == "" {
			ociErr(w, 400, "InvalidParameter", "unknown action")
			return
		}
		f.actions = append(f.actions, id+" "+action)
		f.tokens = append(f.tokens, tok)
		in.data["lifecycleState"] = next
		writeJSON(w, 200, in.data)
	case service == "iaas" && r.Method == http.MethodGet && path == "/20160918/vnicAttachments":
		in := f.find(region, q.Get("instanceId"))
		if in == nil || in.data["compartmentId"] != q.Get("compartmentId") {
			writeJSON(w, 200, []any{})
			return
		}
		// a detached one first, then the attachments in reverse (the primary is not first here)
		atts := []map[string]any{{"id": "ocid1.vnicattachment.oc1..detached", "vnicId": "ocid1.vnic.oc1..detached", "lifecycleState": "DETACHED", "nicIndex": 9}}
		for i := len(in.vnics) - 1; i >= 0; i-- {
			atts = append(atts, map[string]any{"id": fmt.Sprintf("ocid1.vnicattachment.oc1..att%d", i), "instanceId": in.data["id"],
				"vnicId": in.vnics[i]["id"], "lifecycleState": "ATTACHED", "nicIndex": i})
		}
		writeJSON(w, 200, atts)
	case service == "iaas" && r.Method == http.MethodGet && strings.HasPrefix(path, "/20160918/vnics/"):
		id := strings.TrimPrefix(path, "/20160918/vnics/")
		for _, in := range f.insts {
			for _, v := range in.vnics {
				if v["id"] == id && in.region == region {
					writeJSON(w, 200, v)
					return
				}
			}
		}
		ociErr(w, 404, "NotAuthorizedOrNotFound", "Authorization failed or requested resource not found.")
	case service == "telemetry" && r.Method == http.MethodPost && path == "/20180401/metrics/actions/summarizeMetricsData":
		var rq map[string]string
		if err := json.Unmarshal(body, &rq); err != nil {
			f.t.Errorf("metrics body %q", body)
		}
		rq["compartmentId"] = q.Get("compartmentId")
		f.metricRq = append(f.metricRq, rq)
		name, _, _ := strings.Cut(rq["query"], "[")
		if strings.HasPrefix(name, "NetworksBytes") && !strings.HasSuffix(rq["query"], ".rate()") {
			// cumulative counters: only their rate of change is a measure of traffic
			f.t.Errorf("network query %q", rq["query"])
		}
		var dps []map[string]any
		for i, v := range f.metrics[name] {
			dps = append(dps, map[string]any{"timestamp": testNow.Add(-time.Duration(i) * time.Hour).Format(time.RFC3339), "value": v})
		}
		if dps == nil {
			writeJSON(w, 200, []any{})
			return
		}
		writeJSON(w, 200, []map[string]any{{"namespace": "oci_computeagent", "name": name,
			"dimensions": map[string]string{"resourceId": "x"}, "aggregatedDatapoints": dps}})
	default:
		ociErr(w, 404, "NotFound", "no such path")
	}
}
