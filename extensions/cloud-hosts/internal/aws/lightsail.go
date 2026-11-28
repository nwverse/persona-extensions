package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// lightsailTarget prefixes the X-Amz-Target of every Lightsail operation.
const lightsailTarget = "Lightsail_20161128."

type lightsailAdapter struct{ *client }

// NewLightsail is the Lightsail adapter for the profiles of cfg.Credentials in cfg.LightsailRegions. Without
// Lightsail regions it is not configured (a not_signed_in error), and the program does not offer it.
func NewLightsail(cfg Config) (core.Adapter, error) {
	regions := core.CleanList(cfg.LightsailRegions)
	if len(regions) == 0 {
		return nil, core.NotSignedIn("Lightsail is not configured: no Lightsail regions are set.")
	}
	c, err := newClient(cfg, "lightsail", "AWS Lightsail", regions)
	if err != nil {
		return nil, err
	}
	return &lightsailAdapter{c}, nil
}

type lsInstance struct {
	Name     string `json:"name"`
	Location struct {
		AvailabilityZone string `json:"availabilityZone"`
		RegionName       string `json:"regionName"`
	} `json:"location"`
	BlueprintName string   `json:"blueprintName"`
	BundleID      string   `json:"bundleId"`
	PrivateIP     string   `json:"privateIpAddress"`
	PublicIP      string   `json:"publicIpAddress"`
	IPv6          []string `json:"ipv6Addresses"`
	Hardware      struct {
		CPUCount float64 `json:"cpuCount"`
		RAMGb    float64 `json:"ramSizeInGb"`
		Disks    []struct {
			SizeInGb     int64 `json:"sizeInGb"`
			IsSystemDisk bool  `json:"isSystemDisk"`
		} `json:"disks"`
	} `json:"hardware"`
	State struct {
		Name string `json:"name"`
	} `json:"state"`
}

type lsSnapshot struct {
	Name             string  `json:"name"`
	CreatedAt        float64 `json:"createdAt"`
	State            string  `json:"state"`
	FromInstanceName string  `json:"fromInstanceName"`
	SizeInGb         float64 `json:"sizeInGb"`
}

type lsOperations struct {
	Operations []struct {
		Status       string `json:"status"`
		ErrorCode    string `json:"errorCode"`
		ErrorDetails string `json:"errorDetails"`
	} `json:"operations"`
}

// call is one Lightsail operation: in as the JSON body, out decoded from the answer.
func (l *lightsailAdapter) call(ctx context.Context, a account, op string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return core.Failed("the request to %s could not be made", l.vendor)
	}
	resp, err := l.post(ctx, a, "application/x-amz-json-1.1", http.Header{"X-Amz-Target": {lightsailTarget + op}}, body)
	if err != nil {
		return err
	}
	if !resp.OK() {
		return l.vendorError(resp)
	}
	if out != nil {
		return resp.JSON(out)
	}
	return nil
}

// vendorError maps Lightsail's {"__type", "message"} to the hub's codes.
func (l *lightsailAdapter) vendorError(resp *core.Response) error {
	var r struct {
		Type     string `json:"__type"`
		Code     string `json:"code"`
		Message  string `json:"message"`
		Message2 string `json:"Message"`
	}
	_ = json.Unmarshal(resp.Body, &r)
	typ := r.Type
	if typ == "" {
		typ = resp.Header.Get("X-Amzn-Errortype")
	}
	if i := strings.LastIndexByte(typ, '#'); i >= 0 {
		typ = typ[i+1:]
	}
	if i := strings.IndexByte(typ, ':'); i >= 0 {
		typ = typ[:i]
	}
	text := r.Message
	if text == "" {
		text = r.Message2
	}
	text = l.clean(text)
	if typ == "" {
		return core.StatusError(l.vendor, resp.Status, text)
	}
	msg := l.vendor + " " + typ
	if text != "" {
		msg += ": " + text
	}
	lower := strings.ToLower(text)
	switch {
	case typ == "NotFoundException" || r.Code == "DoesNotExist":
		return core.NotFound("%s", msg)
	case typ == "AccessDeniedException", typ == "UnauthenticatedException", typ == "UnrecognizedClientException",
		typ == "InvalidSignatureException", typ == "ExpiredTokenException", typ == "IncompleteSignature",
		typ == "MissingAuthenticationToken":
		return core.NotSignedIn("%s", msg)
	case (typ == "OperationFailureException" || typ == "InvalidInputException") && aboutState(lower):
		return core.InvalidState("%s", msg)
	case typ == "ThrottlingException":
		return core.RateLimited("%s", msg)
	case typ == "ServiceException" && (strings.Contains(lower, "capacity") || strings.Contains(lower, "insufficient")):
		return core.Capacity("%s", msg)
	}
	if resp.Status == http.StatusBadRequest {
		return core.Failed("%s", msg)
	}
	return core.StatusError(l.vendor, resp.Status, typ+": "+text)
}

// aboutState reports whether a Lightsail message refuses because of the instance's state.
func aboutState(lower string) bool {
	for _, w := range []string{"state", "not running", "not stopped", "already running", "already stopped", "is stopping", "is starting", "is pending"} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// Instances lists every account's Lightsail instances.
func (l *lightsailAdapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	return l.eachAccount(ctx, func(ctx context.Context, a account) ([]wire.CloudInstance, error) {
		var out []wire.CloudInstance
		token := ""
		for {
			in := map[string]string{}
			if token != "" {
				in["pageToken"] = token
			}
			var r struct {
				Instances     []lsInstance `json:"instances"`
				NextPageToken string       `json:"nextPageToken"`
			}
			if err := l.call(ctx, a, "GetInstances", in, &r); err != nil {
				return nil, err
			}
			for _, i := range r.Instances {
				out = append(out, l.toInstance(a, i))
			}
			if r.NextPageToken == "" || r.NextPageToken == token || len(out) >= listCap {
				return out, nil
			}
			token = r.NextPageToken
		}
	})
}

// lightsailNamePattern is a Lightsail instance name: it starts with a letter, digit or underscore and holds only those,
// dashes and periods. A ref that is not one is not asked about at all, so no slash, percent sign or space ever
// reaches a request or a console link.
var lightsailNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func validLightsailName(id wire.CloudInstanceID) bool {
	return len(id) <= 255 && lightsailNamePattern.MatchString(string(id))
}

// get reads one Lightsail instance now.
func (l *lightsailAdapter) get(ctx context.Context, ref wire.CloudInstanceRef) (account, lsInstance, error) {
	loc := core.Locale(ctx)
	a, err := l.account(loc, ref.Account)
	if err != nil {
		return account{}, lsInstance{}, err
	}
	if !validLightsailName(ref.ID) {
		return account{}, lsInstance{}, noInstance(loc, l.vendor, ref.ID)
	}
	var r struct {
		Instance *lsInstance `json:"instance"`
	}
	if err := l.call(ctx, a, "GetInstance", map[string]string{"instanceName": string(ref.ID)}, &r); err != nil {
		return account{}, lsInstance{}, err
	}
	if r.Instance == nil || r.Instance.Name != string(ref.ID) {
		return account{}, lsInstance{}, noInstance(loc, l.vendor, ref.ID)
	}
	return a, *r.Instance, nil
}

// Instance reads one Lightsail instance now.
func (l *lightsailAdapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	a, in, err := l.get(ctx, ref)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	return l.toInstance(a, in), nil
}

func lightsailConsoleURL(region, name string) string {
	return "https://lightsail.aws.amazon.com/ls/webapp/" + region + "/instances/" + url.PathEscape(name) + "/connect"
}

func (l *lightsailAdapter) toInstance(a account, in lsInstance) wire.CloudInstance {
	out := wire.CloudInstance{
		Account:     wire.CloudAccountID(a.name()),
		ID:          wire.CloudInstanceID(in.Name),
		DisplayName: in.Name,
		Region:      a.region,
		Zone:        in.Location.AvailabilityZone,
		PublicIps:   core.PlainIPs(append([]string{in.PublicIP}, in.IPv6...)...),
		PrivateIps:  core.PlainIPs(in.PrivateIP),
		ObservedAt:  l.now(),
	}
	if in.Location.RegionName != "" {
		out.Region = in.Location.RegionName
	}
	var power []wire.CloudActionKind
	live, canSnap := true, false
	switch in.State.Name {
	case "pending", "starting":
		out.State = wire.CloudInstanceStateStarting
	case "running":
		out.State = wire.CloudInstanceStateRunning
		power = []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindForceStop}
		canSnap = true
	case "stopping", "shutting-down":
		out.State = wire.CloudInstanceStateStopping
		power = []wire.CloudActionKind{wire.CloudActionKindForceStop}
	case "stopped":
		out.State = wire.CloudInstanceStateStopped
		power = []wire.CloudActionKind{wire.CloudActionKindStart}
		canSnap = true
	case "terminated", "terminating":
		out.State = wire.CloudInstanceStateTerminated
		live = false
	default:
		out.State = wire.CloudInstanceStateUnknown
		live = false
	}
	spec := wire.CloudSpec{Size: in.BundleID, Cpu: in.Hardware.CPUCount, OS: in.BlueprintName,
		MemoryMb: int64(math.Round(in.Hardware.RAMGb * 1024))}
	for _, d := range in.Hardware.Disks {
		if d.IsSystemDisk {
			spec.DiskGb = d.SizeInGb
		}
	}
	if spec != (wire.CloudSpec{}) {
		out.Spec = &spec
	}
	if regionPattern.MatchString(out.Region) && validLightsailName(wire.CloudInstanceID(in.Name)) {
		out.ConsoleURL = lightsailConsoleURL(out.Region, in.Name)
	}
	out.Actions = actionsFor(power, live, canSnap, out.ConsoleURL != "" && out.State != wire.CloudInstanceStateTerminated)
	return out
}

// Action asks Lightsail for a power change or a snapshot.
func (l *lightsailAdapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, actionBudget)
	defer cancel()
	in := map[string]any{"instanceName": string(act.Ref.ID)}
	var op string
	switch act.Kind {
	case wire.CloudActionKindStart:
		op = "StartInstance"
	case wire.CloudActionKindStop:
		op = "StopInstance"
	case wire.CloudActionKindForceStop:
		op = "StopInstance"
		in["force"] = true
	case wire.CloudActionKindReboot:
		op = "RebootInstance"
	case wire.CloudActionKindSnapshotCreate:
		return l.createSnapshot(ctx, act)
	default:
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale, "Lightsail does not offer this action.", "Lightsail 不支持这个操作。"))
	}
	a, err := l.account(act.Locale, act.Ref.Account)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	if !validLightsailName(act.Ref.ID) {
		return wire.CloudActionResult{}, noInstance(act.Locale, l.vendor, act.Ref.ID)
	}
	var r lsOperations
	if err := l.call(ctx, a, op, in, &r); err != nil {
		return wire.CloudActionResult{}, err
	}
	if err := l.operationsFailed(r); err != nil {
		return wire.CloudActionResult{}, err
	}
	return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}, nil
}

// operationsFailed is an error when Lightsail's answer says an operation failed at once.
func (l *lightsailAdapter) operationsFailed(r lsOperations) error {
	for _, o := range r.Operations {
		if o.Status == "Failed" {
			msg := l.clean(o.ErrorDetails)
			if msg == "" {
				msg = o.ErrorCode
			}
			return core.Failed("%s: %s", l.vendor, msg)
		}
	}
	return nil
}

func lsSnapshotState(s string) wire.CloudSnapshotState {
	switch s {
	case "available":
		return wire.CloudSnapshotStateReady
	case "error":
		return wire.CloudSnapshotStateFailed
	}
	return wire.CloudSnapshotStateCreating
}

func toLSSnapshot(s lsSnapshot) wire.CloudSnapshot {
	out := wire.CloudSnapshot{ID: wire.CloudSnapshotID(s.Name), Name: s.Name, State: lsSnapshotState(s.State), SizeGb: s.SizeInGb}
	if s.CreatedAt > 0 {
		sec, frac := math.Modf(s.CreatedAt)
		out.CreatedAt = time.Unix(int64(sec), int64(frac*1e9)).UTC()
	}
	return out
}

// Snapshots lists the instance's Lightsail snapshots (Lightsail lists them all; those of this instance are kept).
func (l *lightsailAdapter) Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	a, _, err := l.get(ctx, ref)
	if err != nil {
		return nil, err
	}
	// GetInstanceSnapshots has no filter by instance: page through the region's snapshots until they end, the
	// instance has core.MaxSnapshots of them, or the budget is spent (then what was read so far is answered).
	var out []wire.CloudSnapshot
	token := ""
	for page := 0; len(out) < core.MaxSnapshots; page++ {
		in := map[string]string{}
		if token != "" {
			in["pageToken"] = token
		}
		var r struct {
			Snapshots     []lsSnapshot `json:"instanceSnapshots"`
			NextPageToken string       `json:"nextPageToken"`
		}
		if err := l.call(ctx, a, "GetInstanceSnapshots", in, &r); err != nil {
			if page > 0 && ctx.Err() != nil {
				break
			}
			return nil, err
		}
		for _, s := range r.Snapshots {
			if s.FromInstanceName == string(ref.ID) && len(out) < core.MaxSnapshots {
				out = append(out, toLSSnapshot(s))
			}
		}
		if r.NextPageToken == "" || r.NextPageToken == token {
			break
		}
		token = r.NextPageToken
	}
	return out, nil
}

// snapshotName is a valid Lightsail resource name (\w[\w-]*\w) from base (or the instance's name), ending in a short
// hash of the request id: unique per request, and the same for its retries.
func snapshotName(base, instance string, rid wire.CloudRequestID) string {
	clean := func(s string) string {
		var b strings.Builder
		dash := false
		for _, c := range s {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
				if dash && b.Len() > 0 {
					b.WriteByte('-')
				}
				dash = false
				b.WriteRune(c)
				if b.Len() >= 200 {
					break
				}
			} else {
				dash = true
			}
		}
		return b.String()
	}
	b := clean(base)
	if b == "" {
		b = clean(instance)
	}
	if b == "" {
		b = "snapshot"
	}
	sum := sha256.Sum256([]byte(rid))
	return b + "-" + hex.EncodeToString(sum[:4])
}

// createSnapshot asks Lightsail for an instance snapshot. A retry whose snapshot already exists (the name is the
// request's) answers that snapshot.
func (l *lightsailAdapter) createSnapshot(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	a, err := l.account(act.Locale, act.Ref.Account)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	if !validLightsailName(act.Ref.ID) {
		return wire.CloudActionResult{}, noInstance(act.Locale, l.vendor, act.Ref.ID)
	}
	name := snapshotName(act.Name, string(act.Ref.ID), act.RequestID)
	var r lsOperations
	err = l.call(ctx, a, "CreateInstanceSnapshot", map[string]string{"instanceName": string(act.Ref.ID), "instanceSnapshotName": name}, &r)
	if err != nil {
		var prior struct {
			Snapshot *lsSnapshot `json:"instanceSnapshot"`
		}
		if e := core.AsError(err); e.Code != wire.CloudErrorCodeNotFound && !e.Transient &&
			l.call(ctx, a, "GetInstanceSnapshot", map[string]string{"instanceSnapshotName": name}, &prior) == nil &&
			prior.Snapshot != nil && prior.Snapshot.FromInstanceName == string(act.Ref.ID) {
			s := toLSSnapshot(*prior.Snapshot)
			return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted, Snapshot: &s}, nil
		}
		return wire.CloudActionResult{}, err
	}
	if err := l.operationsFailed(r); err != nil {
		return wire.CloudActionResult{}, err
	}
	s := wire.CloudSnapshot{ID: wire.CloudSnapshotID(name), Name: name, State: wire.CloudSnapshotStateCreating, CreatedAt: l.now()}
	return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted, Snapshot: &s}, nil
}
