package gcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// needsPreemptionCheck: only a spot VM that is off may have been taken back.
func needsPreemptionCheck(g *gInstance) bool {
	return g.spot() && (g.Status == "TERMINATED" || g.Status == "SUSPENDED")
}

// preemptedByMessage is the fallback when the operations cannot be read: the VM's own status message.
func preemptedByMessage(g *gInstance) bool {
	return strings.Contains(strings.ToLower(g.StatusMessage), "preempt")
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// preempted reports whether Google took the spot VM back since it last started: the newest
// compute.instances.preempted operation on it is newer than its lastStartTimestamp.
func (a *adapter) preempted(ctx context.Context, acc *account, g *gInstance) bool {
	zone := lastSegment(g.Zone)
	if g.SelfLink == "" || !zoneRe.MatchString(zone) {
		return preemptedByMessage(g)
	}
	q := url.Values{
		"filter": {`(operationType="compute.instances.preempted") AND (targetLink="` + g.SelfLink + `")`},
		// Newest first: by default Google lists operations by name, which for system events is oldest first.
		"orderBy":    {"creationTimestamp desc"},
		"maxResults": {"50"},
	}
	var ans struct {
		Items []gOperation `json:"items"`
	}
	if err := a.call(ctx, acc, http.MethodGet, "projects/"+seg(acc.project)+"/zones/"+seg(zone)+"/operations", q, nil, &ans); err != nil {
		return preemptedByMessage(g)
	}
	var newest time.Time
	for _, op := range ans.Items {
		if op.OperationType != "" && op.OperationType != "compute.instances.preempted" {
			continue
		}
		if op.TargetLink != "" && op.TargetLink != g.SelfLink {
			continue
		}
		t := parseTime(op.InsertTime)
		if t.IsZero() {
			t = parseTime(op.CreationTimestamp)
		}
		if t.IsZero() {
			t = parseTime(op.StartTime)
		}
		if t.After(newest) {
			newest = t
		}
	}
	if newest.IsZero() {
		return false
	}
	started := parseTime(g.LastStartTimestamp)
	return started.IsZero() || newest.After(started)
}

// toInstance is g as the hub reads it, its state detail in loc.
func (a *adapter) toInstance(loc, project string, g *gInstance, preempted bool) wire.CloudInstance {
	zone := lastSegment(g.Zone)
	region := zone
	if i := strings.LastIndexByte(zone, '-'); i > 0 {
		region = zone[:i]
	}
	in := wire.CloudInstance{
		Account:     wire.CloudAccountID(project),
		ID:          wire.CloudInstanceID(zone + "/" + g.Name),
		DisplayName: g.Name,
		Region:      region,
		Zone:        zone,
		ObservedAt:  a.now(),
	}
	switch g.Status {
	case "PROVISIONING", "STAGING":
		in.State = wire.CloudInstanceStateStarting
	case "RUNNING":
		in.State = wire.CloudInstanceStateRunning
	case "STOPPING", "SUSPENDING":
		in.State = wire.CloudInstanceStateStopping
	case "TERMINATED", "SUSPENDED":
		in.State = wire.CloudInstanceStateStopped
		if g.spot() && preempted {
			in.State = wire.CloudInstanceStatePreempted
			in.StateDetail = core.T(loc, "Google Cloud took this spot VM back.", "Google Cloud 收回了这台竞价虚拟机。")
		} else if g.Status == "SUSPENDED" {
			in.StateDetail = core.T(loc, "Suspended.", "已挂起。")
		}
	case "REPAIRING":
		in.State = wire.CloudInstanceStateUnknown
		in.StateDetail = core.T(loc, "Google Cloud is repairing this VM.", "Google Cloud 正在修复这台虚拟机。")
	default:
		in.State = wire.CloudInstanceStateUnknown
		if g.Status != "" {
			in.StateDetail = core.T(loc, "Google Cloud reports the state ", "Google Cloud 报告的状态：") + g.Status
		}
	}

	var public, private []string
	for _, n := range g.NetworkInterfaces {
		for _, ac := range n.AccessConfigs {
			public = append(public, ac.NatIP)
		}
		for _, ac := range n.IPv6AccessConfigs {
			public = append(public, ac.ExternalIPv6)
		}
		private = append(private, n.NetworkIP, n.IPv6Address)
	}
	in.PublicIps = core.PlainIPs(public...)
	in.PrivateIps = core.PlainIPs(private...)

	source, diskGb, arch, licenses := g.bootDisk()
	spec := specFor(lastSegment(g.MachineType))
	spec.DiskGb = diskGb
	switch strings.ToUpper(arch) {
	case "X86_64":
		spec.Arch = "x86_64"
	case "ARM64":
		spec.Arch = "arm64"
	default:
		if p := strings.ToLower(g.CPUPlatform); strings.Contains(p, "ampere") || strings.Contains(p, "axion") {
			spec.Arch = "arm64"
		}
	}
	if len(licenses) > 0 {
		spec.OS = lastSegment(licenses[0])
	}
	if spec != (wire.CloudSpec{}) {
		in.Spec = &spec
	}
	if g.spot() {
		in.Lifecycle = &wire.CloudLifecycle{Spot: true}
	}
	if nameRe.MatchString(g.Name) && zoneRe.MatchString(zone) && projectRe.MatchString(project) {
		in.ConsoleURL = "https://console.cloud.google.com/compute/instancesDetail/zones/" + seg(zone) + "/instances/" +
			seg(g.Name) + "?" + url.Values{"project": {project}}.Encode()
	}

	snap := source != ""
	switch in.State {
	case wire.CloudInstanceStateRunning:
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindHardReset, wire.CloudActionKindSyncIp}
		if snap {
			in.Actions = append(in.Actions, wire.CloudActionKindSnapshotList, wire.CloudActionKindSnapshotCreate)
		}
	case wire.CloudInstanceStateStopped, wire.CloudInstanceStatePreempted:
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindStart, wire.CloudActionKindSyncIp}
		if snap {
			in.Actions = append(in.Actions, wire.CloudActionKindSnapshotList, wire.CloudActionKindSnapshotCreate)
		}
	default:
		in.Actions = []wire.CloudActionKind{wire.CloudActionKindSyncIp}
		if snap {
			in.Actions = append(in.Actions, wire.CloudActionKindSnapshotList)
		}
	}
	if in.ConsoleURL != "" {
		in.Actions = append(in.Actions, wire.CloudActionKindConsole)
	}
	return in
}

var (
	customRe = regexp.MustCompile(`^(?:[a-z0-9]+-)?custom-(\d+)-(\d+)(?:-ext)?$`)
	// The GPU families (a2-highgpu-1g, a3-megagpu-8g, ...) are left out: their number counts GPUs, not vCPUs.
	predefinedRe = regexp.MustCompile(`^[a-z0-9]+-(?:standard|highcpu|highmem|ultramem|megamem|hypermem|highlssd|standardlssd)-(\d+)`)
)

// sharedCore are the shared-core machine types: their CPU share and memory.
var sharedCore = map[string]wire.CloudSpec{
	"e2-micro":  {Cpu: 0.25, MemoryMb: 1024},
	"e2-small":  {Cpu: 0.5, MemoryMb: 2048},
	"e2-medium": {Cpu: 1, MemoryMb: 4096},
	"f1-micro":  {Cpu: 0.2, MemoryMb: 614},
	"g1-small":  {Cpu: 0.5, MemoryMb: 1740},
}

// specFor is what the machine type's name says of its size.
func specFor(machineType string) wire.CloudSpec {
	s := wire.CloudSpec{Size: machineType}
	if c, ok := sharedCore[machineType]; ok {
		s.Cpu, s.MemoryMb = c.Cpu, c.MemoryMb
	} else if m := customRe.FindStringSubmatch(machineType); m != nil {
		cpu, _ := strconv.Atoi(m[1])
		mem, _ := strconv.ParseInt(m[2], 10, 64)
		s.Cpu, s.MemoryMb = float64(cpu), mem
	} else if m := predefinedRe.FindStringSubmatch(machineType); m != nil {
		cpu, _ := strconv.Atoi(m[1])
		s.Cpu = float64(cpu)
	}
	return s
}

// requestUUID is the requestId Compute Engine dedupes a write by: a UUID derived from the hub's request id, so a
// retry of the same request is the same request to Google.
func requestUUID(act core.Action) string {
	sum := sha256.Sum256([]byte("persona-cloud-hosts/gcp\x00" + string(act.Ref.Account) + "\x00" + string(act.Ref.ID) +
		"\x00" + string(act.Kind) + "\x00" + string(act.RequestID)))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50 // version 5 layout
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// diskRe is a disk's link: its project, zones or regions, location and name.
var diskRe = regexp.MustCompile(`(?:^|/)projects/([^/]+)/(zones|regions)/([a-z][a-z0-9-]{0,62})/disks/([a-z]([-a-z0-9]{0,61}[a-z0-9])?)$`)

// bootDiskOf reads the VM and returns its boot disk's link and the path of that disk, or unsupported.
func (a *adapter) bootDiskOf(ctx context.Context, acc *account, zone, name, locale string) (string, string, error) {
	g, err := a.get(ctx, acc, zone, name)
	if err != nil {
		return "", "", err
	}
	source, _, _, _ := g.bootDisk()
	m := diskRe.FindStringSubmatch(source)
	if m == nil || !projectRe.MatchString(m[1]) {
		return "", "", core.Unsupported("%s", core.T(locale, "This VM has no boot disk to snapshot.", "这台虚拟机没有可做快照的启动盘。"))
	}
	return source, "projects/" + seg(m[1]) + "/" + m[2] + "/" + seg(m[3]) + "/disks/" + seg(m[4]), nil
}

func snapshotState(s string) wire.CloudSnapshotState {
	switch s {
	case "READY":
		return wire.CloudSnapshotStateReady
	case "FAILED":
		return wire.CloudSnapshotStateFailed
	case "DELETING":
		return wire.CloudSnapshotStateDeleting
	}
	return wire.CloudSnapshotStateCreating
}

// Snapshots lists the snapshots of the VM's boot disk.
func (a *adapter) Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	loc := core.Locale(ctx)
	acc, zone, name, err := a.resolve(loc, ref)
	if err != nil {
		return nil, err
	}
	source, _, err := a.bootDiskOf(ctx, acc, zone, name, loc)
	if err != nil {
		return nil, err
	}
	q := url.Values{"filter": {`sourceDisk="` + source + `"`}, "maxResults": {"500"}}
	var out []wire.CloudSnapshot
	for page := 0; page < snapshotPages; page++ {
		var ans struct {
			Items         []gSnapshot `json:"items"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := a.call(ctx, acc, http.MethodGet, "projects/"+seg(acc.project)+"/global/snapshots", q, nil, &ans); err != nil {
			return nil, err
		}
		for _, s := range ans.Items {
			if s.Name == "" || (s.SourceDisk != "" && s.SourceDisk != source) {
				continue
			}
			size := float64(s.DiskSizeGb)
			if s.StorageBytes > 0 {
				size = float64(s.StorageBytes) / (1 << 30)
			}
			out = append(out, wire.CloudSnapshot{ID: wire.CloudSnapshotID(s.Name), Name: s.Name,
				State: snapshotState(s.Status), CreatedAt: parseTime(s.CreationTimestamp), SizeGb: size})
		}
		if ans.NextPageToken == "" {
			break
		}
		q.Set("pageToken", ans.NextPageToken)
	}
	return out, nil
}

// snapshotName is an RFC 1035 name from the name asked for, or the VM's name and the time.
func snapshotName(asked, instance string, now time.Time) string {
	clean := func(s string) string {
		var b strings.Builder
		dash := false
		for _, c := range strings.ToLower(s) {
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
				if dash && b.Len() > 0 {
					b.WriteByte('-')
				}
				dash = false
				b.WriteRune(c)
			} else {
				dash = true
			}
		}
		return b.String()
	}
	fit := func(s string) string {
		if len(s) > 63 {
			s = s[:63]
		}
		return strings.TrimRight(s, "-")
	}
	if n := clean(asked); n != "" {
		if n[0] < 'a' || n[0] > 'z' {
			n = "snap-" + n
		}
		return fit(n)
	}
	suffix := "-" + now.UTC().Format("20060102-150405")
	base := clean(instance)
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "snap-" + base
	}
	if len(base) > 63-len(suffix) {
		base = strings.TrimRight(base[:63-len(suffix)], "-")
	}
	return base + suffix
}

// createSnapshot asks for a snapshot of the VM's boot disk.
func (a *adapter) createSnapshot(ctx context.Context, acc *account, zone, name string, act core.Action) (wire.CloudActionResult, error) {
	_, diskPath, err := a.bootDiskOf(ctx, acc, zone, name, act.Locale)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	snap := snapshotName(act.Name, name, a.now())
	body := map[string]string{"name": snap}
	if act.Name != "" {
		body["description"] = act.Name
	}
	var op gOperation
	q := url.Values{"requestId": {requestUUID(act)}}
	if err := a.call(ctx, acc, http.MethodPost, diskPath+"/createSnapshot", q, body, &op); err != nil {
		return wire.CloudActionResult{}, err
	}
	if err := a.opError(act.Locale, &op); err != nil {
		return wire.CloudActionResult{}, err
	}
	res := wire.CloudActionResult{Status: wire.CloudActionStatusAccepted,
		Snapshot: &wire.CloudSnapshot{ID: wire.CloudSnapshotID(snap), Name: snap, State: wire.CloudSnapshotStateCreating}}
	if op.Status == "DONE" {
		res.Status = wire.CloudActionStatusDone
		res.Snapshot.State = wire.CloudSnapshotStateReady
	}
	return res, nil
}
