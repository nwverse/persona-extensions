package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Oracle reclaims an idle Always Free instance (VM.Standard.E2.1.Micro, VM.Standard.A1.Flex): one whose 95th
// percentile CPU utilization over 7 days is under 20%, whose network utilization is under 20% and, for A1 shapes,
// whose memory utilization is under 20%. One measure at or over the threshold is enough to count as active.
const (
	idleWindow    = 7 * 24 * time.Hour
	idleThreshold = 20.0 // percent
	maxProbes     = 1000 // instances whose last probe is kept
	probeFile     = "oci-keepalive.json"
)

// keepaliveAt is lifecycle.keepalive_at: the latest time by which the instance has to show activity. The rule: when
// a keepalive probe found the instance active, that probe's time + 7 days (as long as that is still ahead of now,
// the observation time); otherwise, never probed active or the last active probe is older than 7 days, now + 7 days,
// since 7 idle days from now on are what would get it reclaimed.
func (a *adapter) keepaliveAt(id string, now time.Time) time.Time {
	if at := a.probes.activeAt(id); !at.IsZero() {
		if until := at.Add(idleWindow); until.After(now) {
			return until.UTC().Truncate(time.Second)
		}
	}
	return now.Add(idleWindow).UTC().Truncate(time.Second)
}

// keepalive is the read custom action: it asks the Monitoring API for the last 7 days of the instance's CPU (and,
// when there is data, memory and network) and says whether it looks active enough. It changes nothing at OCI.
func (a *adapter) keepalive(ctx context.Context, p *profile, id, locale string) (wire.CloudActionResult, error) {
	in, err := a.getInstance(ctx, p, id)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	if !alwaysFreeShape(in.Shape) {
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(locale,
			"This instance is not an Always Free shape, so Oracle does not reclaim it for being idle.",
			"这台实例不是 Always Free 规格，Oracle 不会因闲置回收它。"))
	}
	if in.terminatedState() {
		return wire.CloudActionResult{}, core.InvalidState("%s", core.T(locale, "The instance is terminated.", "这台实例已终止。"))
	}
	now := a.now()
	cpu, err := a.metric(ctx, p, in, "CpuUtilization", "percentile(0.95)", now)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	if len(cpu) == 0 {
		a.probes.record(id, now, false)
		return wire.CloudActionResult{Status: wire.CloudActionStatusDone, Message: core.T(locale,
			"No CPU data from the last 7 days (is the Oracle Cloud Agent's Compute Instance Monitoring plugin on?), so its activity cannot be judged.",
			"过去 7 天没有 CPU 数据（Oracle Cloud Agent 的计算实例监控插件是否已开启？），无法判断它是否活跃。")}, nil
	}
	cpuP95 := percentile95(cpu)
	active := cpuP95 >= idleThreshold
	statsEN := []string{fmt.Sprintf("CPU p95 %.1f%%", cpuP95)}
	statsZH := []string{fmt.Sprintf("CPU p95 %.1f%%", cpuP95)}
	if strings.Contains(in.Shape, ".A1.") {
		if mem, err := a.metric(ctx, p, in, "MemoryUtilization", "percentile(0.95)", now); err == nil && len(mem) > 0 {
			m := percentile95(mem)
			active = active || m >= idleThreshold
			statsEN = append(statsEN, fmt.Sprintf("memory p95 %.1f%%", m))
			statsZH = append(statsZH, fmt.Sprintf("内存 p95 %.1f%%", m))
		}
	}
	if in.ShapeConfig != nil && in.ShapeConfig.NetworkingBandwidthInGbps > 0 {
		gbps := in.ShapeConfig.NetworkingBandwidthInGbps
		var best float64
		found := false
		for _, name := range []string{"NetworksBytesIn", "NetworksBytesOut"} {
			// NetworksBytesIn/Out are cumulative counters (per Oracle Cloud Agent session, reset at every OS
			// restart), so summing or averaging them says nothing about traffic. MQL rate() is the average rate of
			// change over each 1-hour interval in bytes per second, and it copes with counter resets.
			bytesPerSecond, err := a.metric(ctx, p, in, name, "rate()", now)
			if err != nil || len(bytesPerSecond) == 0 {
				continue
			}
			util := make([]float64, len(bytesPerSecond))
			for i, b := range bytesPerSecond {
				util[i] = max(b, 0) * 8 / (gbps * 1e9) * 100
			}
			found = true
			best = math.Max(best, percentile95(util))
		}
		if found {
			active = active || best >= idleThreshold
			statsEN = append(statsEN, fmt.Sprintf("network p95 %.1f%%", best))
			statsZH = append(statsZH, fmt.Sprintf("网络 p95 %.1f%%", best))
		}
	}
	a.probes.record(id, now, active)
	en, zh := strings.Join(statsEN, ", "), strings.Join(statsZH, "、")
	msg := core.T(locale,
		"At risk of being reclaimed as idle: over the last 7 days "+en+", under the 20% Oracle uses to reclaim idle Always Free instances.",
		"有被当作闲置回收的风险：过去 7 天 "+zh+"，低于 Oracle 回收闲置 Always Free 实例的 20% 门槛。")
	if active {
		msg = core.T(locale,
			"Looks active enough: over the last 7 days "+en+"; Oracle reclaims an Always Free instance only when CPU, network and memory all stay under 20%.",
			"看起来足够活跃：过去 7 天 "+zh+"；只有 CPU、网络和内存都低于 20% 时 Oracle 才会回收 Always Free 实例。")
	}
	return wire.CloudActionResult{Status: wire.CloudActionStatusDone, Message: msg}, nil
}

// metric is one oci_computeagent metric of the instance over the last 7 days at a 1-hour interval: the values of
// every hour that has data. stat is the query's statistic, such as percentile(0.95) or rate().
func (a *adapter) metric(ctx context.Context, p *profile, in ociInstance, name, stat string, now time.Time) ([]float64, error) {
	body, _ := json.Marshal(map[string]string{
		"namespace":  "oci_computeagent",
		"query":      name + `[1h]{resourceId = "` + in.ID + `"}.` + stat,
		"startTime":  now.Add(-idleWindow).UTC().Format(time.RFC3339),
		"endTime":    now.UTC().Format(time.RFC3339),
		"resolution": "1h",
	})
	var data []struct {
		AggregatedDatapoints []struct {
			Value float64 `json:"value"`
		} `json:"aggregatedDatapoints"`
	}
	q := url.Values{"compartmentId": {in.CompartmentID}}
	if _, err := a.call(ctx, p, "telemetry", http.MethodPost, monitoringAPI+"/metrics/actions/summarizeMetricsData", q, body, nil, &data); err != nil {
		if e := core.AsError(err); e.Code == wire.CloudErrorCodeNotFound {
			// a 404 here is the monitoring data, not the instance
			return nil, core.Failed("%s", core.T(core.Locale(ctx), "Oracle Cloud's monitoring data could not be read: ", "未能读取 Oracle Cloud 的监控数据：")+e.Message)
		}
		return nil, err
	}
	var out []float64
	for _, d := range data {
		for _, dp := range d.AggregatedDatapoints {
			if !math.IsNaN(dp.Value) && !math.IsInf(dp.Value, 0) {
				out = append(out, dp.Value)
			}
		}
	}
	return out, nil
}

// percentile95 is the nearest-rank 95th percentile of the hourly values. Over hourly 95th percentiles this is an
// approximation of Oracle's own 7-day figure, which is computed from the raw samples.
func percentile95(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := int(math.Ceil(0.95*float64(len(s)))) - 1
	if i < 0 {
		i = 0
	}
	return s[i]
}

// probeRecord is the last keepalive probe of one instance. It holds no credential.
type probeRecord struct {
	ProbedAt time.Time `json:"probed_at"`
	Active   bool      `json:"active"`
	ActiveAt time.Time `json:"active_at,omitzero"` // the last probe that found it active
}

// probeStore keeps the probes in memory and, with a directory, in oci-keepalive.json (0600).
type probeStore struct {
	mu   sync.Mutex
	path string
	m    map[string]probeRecord
}

func newProbeStore(dir string) *probeStore {
	s := &probeStore{m: map[string]probeRecord{}}
	if strings.TrimSpace(dir) == "" {
		return s
	}
	s.path = filepath.Join(dir, probeFile)
	if raw, err := os.ReadFile(s.path); err == nil {
		var f struct {
			Instances map[string]probeRecord `json:"instances"`
		}
		if json.Unmarshal(raw, &f) == nil && f.Instances != nil {
			s.m = f.Instances
		}
	}
	return s
}

func (s *probeStore) activeAt(id string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id].ActiveAt
}

// record keeps a probe and writes the file (best effort: a probe still answers when the file cannot be written).
func (s *probeStore) record(id string, at time.Time, active bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.m[id]
	r.ProbedAt, r.Active = at.UTC().Truncate(time.Second), active
	if active {
		r.ActiveAt = r.ProbedAt
	}
	s.m[id] = r
	if len(s.m) > maxProbes {
		ids := make([]string, 0, len(s.m))
		for k := range s.m {
			ids = append(ids, k)
		}
		sort.Slice(ids, func(i, j int) bool { return s.m[ids[i]].ProbedAt.Before(s.m[ids[j]].ProbedAt) })
		for _, k := range ids[:len(s.m)-maxProbes] {
			delete(s.m, k)
		}
	}
	if s.path == "" {
		return
	}
	raw, err := json.MarshalIndent(map[string]any{"version": 1, "instances": s.m}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".oci-keepalive-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	_ = tmp.Chmod(0o600)
	_, werr := tmp.Write(raw)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(name, s.path) != nil {
		_ = os.Remove(name)
	}
}
