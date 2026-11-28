package aws

import (
	"context"
	"encoding/xml"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// ec2Version is the EC2 Query API version spoken.
const ec2Version = "2016-11-15"

// requestTag is the tag a snapshot carries with the request_id that made it, so a retry finds it instead of taking
// a second one.
const requestTag = "persona-request-id"

type ec2Adapter struct{ *client }

// NewEC2 is the EC2 adapter for the profiles of cfg.Credentials in cfg.Regions (us-east-1 when none).
func NewEC2(cfg Config) (core.Adapter, error) {
	regions := core.CleanList(cfg.Regions)
	if len(regions) == 0 {
		regions = []string{"us-east-1"}
	}
	c, err := newClient(cfg, "ec2", "AWS EC2", regions)
	if err != nil {
		return nil, err
	}
	return &ec2Adapter{c}, nil
}

// The parts of EC2's XML answers that are read.

type ec2Tag struct {
	Key   string `xml:"key"`
	Value string `xml:"value"`
}

type ec2Interface struct {
	DeviceIndex int    `xml:"attachment>deviceIndex"`
	PrivateIP   string `xml:"privateIpAddress"`
	PublicIP    string `xml:"association>publicIp"`
	Private     []struct {
		IP       string `xml:"privateIpAddress"`
		PublicIP string `xml:"association>publicIp"`
	} `xml:"privateIpAddressesSet>item"`
	IPv6 []string `xml:"ipv6AddressesSet>item>ipv6Address"`
}

type ec2Instance struct {
	InstanceID  string `xml:"instanceId"`
	State       string `xml:"instanceState>name"`
	Reason      string `xml:"reason"`
	StateReason struct {
		Code    string `xml:"code"`
		Message string `xml:"message"`
	} `xml:"stateReason"`
	InstanceType   string `xml:"instanceType"`
	Zone           string `xml:"placement>availabilityZone"`
	PrivateIP      string `xml:"privateIpAddress"`
	PublicIP       string `xml:"ipAddress"`
	IPv6           string `xml:"ipv6Address"`
	RootDeviceName string `xml:"rootDeviceName"`
	RootDeviceType string `xml:"rootDeviceType"`
	Blocks         []struct {
		DeviceName string `xml:"deviceName"`
		VolumeID   string `xml:"ebs>volumeId"`
	} `xml:"blockDeviceMapping>item"`
	Lifecycle       string         `xml:"instanceLifecycle"`
	Arch            string         `xml:"architecture"`
	Tags            []ec2Tag       `xml:"tagSet>item"`
	Interfaces      []ec2Interface `xml:"networkInterfaceSet>item"`
	CoreCount       int            `xml:"cpuOptions>coreCount"`
	ThreadsPerCore  int            `xml:"cpuOptions>threadsPerCore"`
	PlatformDetails string         `xml:"platformDetails"`
}

type ec2DescribeInstances struct {
	Reservations []struct {
		Instances []ec2Instance `xml:"instancesSet>item"`
	} `xml:"reservationSet>item"`
	NextToken string `xml:"nextToken"`
}

type ec2StateChange struct {
	Items []struct {
		InstanceID string `xml:"instanceId"`
		Current    string `xml:"currentState>name"`
	} `xml:"instancesSet>item"`
}

type ec2Snapshot struct {
	SnapshotID  string   `xml:"snapshotId"`
	VolumeID    string   `xml:"volumeId"`
	Status      string   `xml:"status"`
	StartTime   string   `xml:"startTime"`
	VolumeSize  float64  `xml:"volumeSize"`
	Description string   `xml:"description"`
	Tags        []ec2Tag `xml:"tagSet>item"`
}

type ec2DescribeSnapshots struct {
	Snapshots []ec2Snapshot `xml:"snapshotSet>item"`
	NextToken string        `xml:"nextToken"`
}

type ec2ErrorResponse struct {
	Errors []struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Errors>Error"`
}

// call is one EC2 Query API action: params as a form body, out decoded from the XML answer.
func (e *ec2Adapter) call(ctx context.Context, a account, action string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	params.Set("Action", action)
	params.Set("Version", ec2Version)
	resp, err := e.post(ctx, a, "application/x-www-form-urlencoded; charset=utf-8", nil, []byte(params.Encode()))
	if err != nil {
		return err
	}
	if !resp.OK() {
		return e.vendorError(resp)
	}
	if out != nil {
		if err := xml.Unmarshal(resp.Body, out); err != nil {
			return core.Failed("%s", core.T(core.Locale(ctx), "The answer of "+e.vendor+" could not be read.", "无法读取 "+e.vendor+" 的应答。"))
		}
	}
	return nil
}

// vendorError maps EC2's <Response><Errors><Error><Code> to the hub's codes.
func (e *ec2Adapter) vendorError(resp *core.Response) error {
	var r ec2ErrorResponse
	if xml.Unmarshal(resp.Body, &r) != nil || len(r.Errors) == 0 || r.Errors[0].Code == "" {
		return core.StatusError(e.vendor, resp.Status, "")
	}
	code := r.Errors[0].Code
	msg := e.vendor + " " + code
	if m := e.clean(r.Errors[0].Message); m != "" {
		msg += ": " + m
	}
	switch {
	case code == "InvalidInstanceID.NotFound", code == "InvalidInstanceID.Malformed", code == "InvalidVolume.NotFound":
		return core.NotFound("%s", msg)
	case code == "AuthFailure", strings.HasPrefix(code, "AuthFailure."), code == "UnauthorizedOperation",
		code == "InvalidClientTokenId", code == "SignatureDoesNotMatch", code == "ExpiredToken",
		code == "RequestExpired", code == "OptInRequired", code == "MissingAuthenticationToken":
		return core.NotSignedIn("%s", msg)
	case code == "IncorrectInstanceState", code == "IncorrectState", code == "IncorrectSpotRequestState":
		return core.InvalidState("%s", msg)
	case code == "RequestLimitExceeded", strings.HasPrefix(code, "Throttling"):
		return core.RateLimited("%s", msg)
	case code == "InsufficientInstanceCapacity", code == "SpotMaxPriceTooLow", code == "MaxSpotInstanceCountExceeded",
		code == "InstanceLimitExceeded", code == "InsufficientFreeAddressesInSubnet", code == "VcpuLimitExceeded",
		code == "Unsupported":
		return core.Capacity("%s", msg)
	case code == "OperationNotPermitted", code == "UnsupportedOperation":
		return core.Refused("%s", msg)
	}
	return core.StatusError(e.vendor, resp.Status, code+": "+e.clean(r.Errors[0].Message))
}

// Instances lists every account's instances.
func (e *ec2Adapter) Instances(ctx context.Context) ([]wire.CloudInstance, []string, error) {
	return e.eachAccount(ctx, func(ctx context.Context, a account) ([]wire.CloudInstance, error) {
		var out []wire.CloudInstance
		token := ""
		for {
			params := url.Values{"MaxResults": {"200"}}
			if token != "" {
				params.Set("NextToken", token)
			}
			var r ec2DescribeInstances
			if err := e.call(ctx, a, "DescribeInstances", params, &r); err != nil {
				return nil, err
			}
			for _, res := range r.Reservations {
				for _, in := range res.Instances {
					out = append(out, e.toInstance(core.Locale(ctx), a, in))
				}
			}
			if r.NextToken == "" || r.NextToken == token || len(out) >= listCap {
				return out, nil
			}
			token = r.NextToken
		}
	})
}

func validInstanceID(id wire.CloudInstanceID) bool {
	s := string(id)
	return len(s) > 2 && len(s) <= 64 && strings.HasPrefix(s, "i-") &&
		!strings.ContainsFunc(s[2:], func(c rune) bool { return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') })
}

// describe reads one instance now.
func (e *ec2Adapter) describe(ctx context.Context, ref wire.CloudInstanceRef) (account, ec2Instance, error) {
	loc := core.Locale(ctx)
	a, err := e.account(loc, ref.Account)
	if err != nil {
		return account{}, ec2Instance{}, err
	}
	if !validInstanceID(ref.ID) {
		return account{}, ec2Instance{}, noInstance(loc, e.vendor, ref.ID)
	}
	var r ec2DescribeInstances
	if err := e.call(ctx, a, "DescribeInstances", url.Values{"InstanceId.1": {string(ref.ID)}}, &r); err != nil {
		return account{}, ec2Instance{}, err
	}
	for _, res := range r.Reservations {
		for _, in := range res.Instances {
			if in.InstanceID == string(ref.ID) {
				return a, in, nil
			}
		}
	}
	return account{}, ec2Instance{}, noInstance(loc, e.vendor, ref.ID)
}

// Instance reads one instance from EC2 now.
func (e *ec2Adapter) Instance(ctx context.Context, ref wire.CloudInstanceRef) (wire.CloudInstance, error) {
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	a, in, err := e.describe(ctx, ref)
	if err != nil {
		return wire.CloudInstance{}, err
	}
	return e.toInstance(core.Locale(ctx), a, in), nil
}

// spotInterrupted reports whether EC2 stopped or terminated the instance for a Spot interruption
// (a stopped one is preempted; a terminated one stays terminated, with its own state detail).
func spotInterrupted(in ec2Instance) bool {
	if strings.HasPrefix(in.StateReason.Code, "Server.SpotInstance") {
		return true
	}
	text := strings.ToLower(in.StateReason.Message + " " + in.Reason)
	return strings.Contains(text, "spot") && (strings.Contains(text, "interrupt") || strings.Contains(text, "spotinstance"))
}

// ebsRoot reports whether the instance's root device is an EBS volume (EC2 reports "ebs" or, for old answers, nothing).
func ebsRoot(in ec2Instance) bool {
	return in.RootDeviceType == "" || in.RootDeviceType == "ebs"
}

// rootVolume is the EBS volume of the instance's root device ("" for an instance-store root).
func rootVolume(in ec2Instance) string {
	if !ebsRoot(in) {
		return ""
	}
	for _, b := range in.Blocks {
		if b.DeviceName == in.RootDeviceName && b.VolumeID != "" {
			return b.VolumeID
		}
	}
	return ""
}

func ec2ConsoleURL(region, id string) string {
	return "https://" + region + ".console.aws.amazon.com/ec2/home?region=" + region + "#InstanceDetails:instanceId=" + id
}

// toInstance is in as the hub sees it; loc words the state details this adapter writes itself.
func (e *ec2Adapter) toInstance(loc string, a account, in ec2Instance) wire.CloudInstance {
	out := wire.CloudInstance{
		Account:     wire.CloudAccountID(a.name()),
		ID:          wire.CloudInstanceID(in.InstanceID),
		DisplayName: in.InstanceID,
		Region:      a.region,
		Zone:        in.Zone,
		ObservedAt:  e.now(),
	}
	for _, t := range in.Tags {
		if t.Key == "Name" && strings.TrimSpace(t.Value) != "" {
			out.DisplayName = t.Value
		}
	}
	spot := in.Lifecycle == "spot"
	if spot {
		out.Lifecycle = &wire.CloudLifecycle{Spot: true}
	}
	var power []wire.CloudActionKind
	live := true
	switch in.State {
	case "pending":
		out.State = wire.CloudInstanceStateStarting
	case "running":
		out.State = wire.CloudInstanceStateRunning
		if ebsRoot(in) {
			power = []wire.CloudActionKind{wire.CloudActionKindStop, wire.CloudActionKindReboot, wire.CloudActionKindForceStop}
		} else {
			// EC2 cannot stop an instance with an instance-store root; it can only be rebooted (or terminated).
			power = []wire.CloudActionKind{wire.CloudActionKindReboot}
		}
	case "stopping":
		out.State = wire.CloudInstanceStateStopping
		power = []wire.CloudActionKind{wire.CloudActionKindForceStop}
	case "shutting-down":
		out.State = wire.CloudInstanceStateStopping
		live = false
	case "stopped":
		out.State = wire.CloudInstanceStateStopped
		if spot && spotInterrupted(in) {
			out.State = wire.CloudInstanceStatePreempted
		}
		power = []wire.CloudActionKind{wire.CloudActionKindStart}
	case "terminated":
		// Deleted is terminated even after a Spot interruption: preempted is for an instance that can be started again.
		out.State = wire.CloudInstanceStateTerminated
		live = false
	default:
		out.State = wire.CloudInstanceStateUnknown
	}
	switch {
	case out.State == wire.CloudInstanceStatePreempted:
		out.StateDetail = core.T(loc, "AWS took this spot instance back.", "AWS 收回了这台竞价实例。")
	case out.State == wire.CloudInstanceStateTerminated && spot && spotInterrupted(in):
		out.StateDetail = core.T(loc, "AWS took this spot instance back and terminated it. It cannot be started again.", "AWS 收回并终止了这台竞价实例，无法再次启动。")
	case in.StateReason.Message != "" && in.State != "running":
		out.StateDetail = in.StateReason.Message // the vendor's own words
	}

	// Addresses: the primary interface's first, then the others in device order.
	pub := []string{in.PublicIP}
	priv := []string{in.PrivateIP}
	var v6 []string
	if in.IPv6 != "" {
		v6 = append(v6, in.IPv6)
	}
	ifs := append([]ec2Interface{}, in.Interfaces...)
	sort.SliceStable(ifs, func(i, j int) bool { return ifs[i].DeviceIndex < ifs[j].DeviceIndex })
	for _, n := range ifs {
		pub = append(pub, n.PublicIP)
		priv = append(priv, n.PrivateIP)
		for _, p := range n.Private {
			pub = append(pub, p.PublicIP)
			priv = append(priv, p.IP)
		}
		v6 = append(v6, n.IPv6...)
	}
	out.PublicIps = core.PlainIPs(append(pub, v6...)...)
	out.PrivateIps = core.PlainIPs(priv...)

	spec := wire.CloudSpec{Size: in.InstanceType, Arch: in.Arch, OS: in.PlatformDetails}
	if in.CoreCount > 0 {
		t := in.ThreadsPerCore
		if t <= 0 {
			t = 1
		}
		spec.Cpu = float64(in.CoreCount * t)
	}
	if spec != (wire.CloudSpec{}) {
		out.Spec = &spec
	}
	// GovCloud has its own console host; rather than guess it, its instances carry no console link.
	if regionPattern.MatchString(a.region) && !govCloud(a.region) && validInstanceID(wire.CloudInstanceID(in.InstanceID)) {
		out.ConsoleURL = ec2ConsoleURL(a.region, in.InstanceID)
	}
	snaps := live && rootVolume(in) != "" && out.State != wire.CloudInstanceStateUnknown
	canSnap := in.State == "running" || in.State == "stopped"
	out.Actions = actionsFor(power, snaps, canSnap, out.ConsoleURL != "")
	return out
}

// Action asks EC2 for a power change or a snapshot.
func (e *ec2Adapter) Action(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	ctx, cancel := context.WithTimeout(ctx, actionBudget)
	defer cancel()
	var op, target string
	params := url.Values{"InstanceId.1": {string(act.Ref.ID)}}
	switch act.Kind {
	case wire.CloudActionKindStart:
		op, target = "StartInstances", "running"
	case wire.CloudActionKindStop:
		op, target = "StopInstances", "stopped"
	case wire.CloudActionKindForceStop:
		op, target = "StopInstances", "stopped"
		params.Set("Force", "true")
	case wire.CloudActionKindReboot:
		op = "RebootInstances"
	case wire.CloudActionKindSnapshotCreate:
		return e.createSnapshot(ctx, act)
	default:
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale, "EC2 does not offer this action.", "EC2 不支持这个操作。"))
	}
	a, err := e.account(act.Locale, act.Ref.Account)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	if !validInstanceID(act.Ref.ID) {
		return wire.CloudActionResult{}, noInstance(act.Locale, e.vendor, act.Ref.ID)
	}
	var r ec2StateChange
	if err := e.call(ctx, a, op, params, &r); err != nil {
		return wire.CloudActionResult{}, err
	}
	res := wire.CloudActionResult{Status: wire.CloudActionStatusAccepted}
	for _, it := range r.Items {
		if it.InstanceID == string(act.Ref.ID) && target != "" && it.Current == target {
			res.Status = wire.CloudActionStatusDone
		}
	}
	return res, nil
}

func snapshotState(status string) wire.CloudSnapshotState {
	switch status {
	case "completed":
		return wire.CloudSnapshotStateReady
	case "error":
		return wire.CloudSnapshotStateFailed
	}
	return wire.CloudSnapshotStateCreating
}

func toSnapshot(s ec2Snapshot) wire.CloudSnapshot {
	out := wire.CloudSnapshot{ID: wire.CloudSnapshotID(s.SnapshotID), Name: s.Description, State: snapshotState(s.Status), SizeGb: s.VolumeSize}
	for _, t := range s.Tags {
		if t.Key == "Name" && strings.TrimSpace(t.Value) != "" {
			out.Name = t.Value
		}
	}
	if t, err := time.Parse(time.RFC3339, s.StartTime); err == nil {
		out.CreatedAt = t
	}
	return out
}

// Snapshots lists the EBS snapshots of the instance's root volume.
func (e *ec2Adapter) Snapshots(ctx context.Context, ref wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, instanceBudget)
	defer cancel()
	a, in, err := e.describe(ctx, ref)
	if err != nil {
		return nil, err
	}
	vol := rootVolume(in)
	if vol == "" {
		return nil, core.Unsupported("%s", core.T(core.Locale(ctx), "This instance has no EBS root volume to take snapshots of.", "这台实例没有 EBS 根卷，无法列出快照。"))
	}
	var out []wire.CloudSnapshot
	token := ""
	for page := 0; page < 5; page++ {
		params := url.Values{"Owner.1": {"self"}, "Filter.1.Name": {"volume-id"}, "Filter.1.Value.1": {vol}, "MaxResults": {"200"}}
		if token != "" {
			params.Set("NextToken", token)
		}
		var r ec2DescribeSnapshots
		if err := e.call(ctx, a, "DescribeSnapshots", params, &r); err != nil {
			return nil, err
		}
		for _, s := range r.Snapshots {
			out = append(out, toSnapshot(s))
		}
		if r.NextToken == "" || r.NextToken == token {
			break
		}
		token = r.NextToken
	}
	return out, nil
}

// createSnapshot takes an EBS snapshot of the root volume, named and tagged with the request id. A snapshot already
// tagged with this request id is answered instead of a second one.
func (e *ec2Adapter) createSnapshot(ctx context.Context, act core.Action) (wire.CloudActionResult, error) {
	a, in, err := e.describe(ctx, act.Ref)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	vol := rootVolume(in)
	if vol == "" {
		return wire.CloudActionResult{}, core.Unsupported("%s", core.T(act.Locale, "This instance has no EBS root volume to take a snapshot of.", "这台实例没有可做快照的 EBS 根卷。"))
	}
	rid := string(act.RequestID)
	var prior ec2DescribeSnapshots
	err = e.call(ctx, a, "DescribeSnapshots", url.Values{"Owner.1": {"self"},
		"Filter.1.Name": {"volume-id"}, "Filter.1.Value.1": {vol},
		"Filter.2.Name": {"tag:" + requestTag}, "Filter.2.Value.1": {rid}}, &prior)
	if err != nil {
		return wire.CloudActionResult{}, err
	}
	// EC2 filter values take * and ? as wildcards, so only a snapshot whose tag is exactly this request id counts.
	for _, ps := range prior.Snapshots {
		if !hasTag(ps.Tags, requestTag, rid) {
			continue
		}
		s := toSnapshot(ps)
		return wire.CloudActionResult{Status: wire.CloudActionStatusAccepted, Snapshot: &s}, nil
	}
	name := strings.TrimSpace(act.Name)
	if name == "" {
		display := in.InstanceID
		for _, t := range in.Tags {
			if t.Key == "Name" && strings.TrimSpace(t.Value) != "" {
				display = core.OneLine(t.Value, 120)
			}
		}
		name = display + "-" + e.now().UTC().Format("20060102-150405")
	}
	params := url.Values{
		"VolumeId":                        {vol},
		"Description":                     {"Snapshot of " + in.InstanceID + " taken by Persona"},
		"TagSpecification.1.ResourceType": {"snapshot"},
		"TagSpecification.1.Tag.1.Key":    {"Name"},
		"TagSpecification.1.Tag.1.Value":  {name},
		"TagSpecification.1.Tag.2.Key":    {requestTag},
		"TagSpecification.1.Tag.2.Value":  {rid},
	}
	var s ec2Snapshot
	if err := e.call(ctx, a, "CreateSnapshot", params, &s); err != nil {
		return wire.CloudActionResult{}, err
	}
	if s.SnapshotID == "" {
		return wire.CloudActionResult{}, core.Failed("%s", core.T(act.Locale, e.vendor+" did not name the new snapshot.", e.vendor+" 没有给出新快照的编号。"))
	}
	snap := toSnapshot(s)
	if snap.Name == "" || snap.Name == s.Description {
		snap.Name = name
	}
	if snap.CreatedAt.IsZero() {
		snap.CreatedAt = e.now()
	}
	status := wire.CloudActionStatusAccepted
	if snap.State == wire.CloudSnapshotStateReady {
		status = wire.CloudActionStatusDone
	}
	return wire.CloudActionResult{Status: status, Snapshot: &snap}, nil
}

// noInstance is the not_found answer for an instance id that is not there (or cannot be one).
func noInstance(loc, vendor string, id wire.CloudInstanceID) error {
	n := core.OneLine(string(id), 0)
	return core.NotFound("%s", core.T(loc, vendor+" has no instance "+n+".", vendor+" 中没有实例 "+n+"。"))
}

// hasTag reports whether tags hold key with exactly value.
func hasTag(tags []ec2Tag, key, value string) bool {
	for _, t := range tags {
		if t.Key == key && t.Value == value {
			return true
		}
	}
	return false
}
