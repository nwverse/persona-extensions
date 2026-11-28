package proxmox

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// instance is g as the hub sees it.
//
// States: status running is running and stopped is stopped; a node the cluster cannot reach reports its guests as
// unknown. A running QEMU guest whose QEMU says paused, suspended or prelaunch is not doing anything, cannot be
// started and is not reachable: it is stopped with a detail and offers no power action (it is resumed in Proxmox VE
// itself). One that is migrating or saving its state stays running with a detail and no power action; one whose
// guest panicked or hit an I/O or internal error stays running with a detail and offers only hard_reset and
// force_stop. A guest that holds a lock (backup, migrate, snapshot, rollback, ...) keeps its state, says which lock
// and offers no power action and no snapshot_create. The exception is a hibernated QEMU guest (status stopped, lock
// "suspended", its RAM saved to disk): it is stopped and offers start, which is what resumes it in Proxmox VE, but no
// snapshot_create (the lock refuses it).
//
// The list reads QEMU's own status (qmpstatus) for its running QEMU guests too, so the list and Instance agree.
// Every state detail is the extension's own words in loc, with Proxmox VE's own terms (a QEMU status, a lock) quoted.
func (a *adapter) instance(loc string, acc *account, g *guest, now time.Time) wire.CloudInstance {
	in := wire.CloudInstance{
		Account:     wire.CloudAccountID(acc.name),
		ID:          g.id(),
		DisplayName: g.name,
		Zone:        g.node,
		PublicIps:   g.publicIPs,
		PrivateIps:  g.privateIP,
		ObservedAt:  now,
	}
	if in.DisplayName == "" {
		in.DisplayName = "VM " + strconv.Itoa(g.vmid)
		if g.typ == "lxc" {
			in.DisplayName = "CT " + strconv.Itoa(g.vmid)
		}
	}
	spec := wire.CloudSpec{Cpu: g.cpus, MemoryMb: g.maxmem / (1 << 20), DiskGb: g.maxdisk / (1 << 30)}
	if g.typ == "qemu" {
		spec.Size = "QEMU VM"
	} else {
		spec.Size = "LXC container"
	}
	in.Spec = &spec
	in.ConsoleURL = a.consoleURL(g.ref)

	const (
		power  = iota // every action the state allows
		reads         // reads only: no power action, no snapshot_create
		rescue        // hard_reset and force_stop besides the reads
	)
	mode := power
	switch g.status {
	case "running":
		in.State = wire.CloudInstanceStateRunning
		switch g.qmp {
		case "", "running":
		case "paused", "suspended", "prelaunch":
			in.State = wire.CloudInstanceStateStopped
			in.StateDetail = core.T(loc, "Paused in Proxmox VE (QEMU reports "+g.qmp+"): resume it there.",
				"已在 Proxmox VE 中暂停（QEMU 状态："+g.qmp+"），请在 Proxmox VE 中恢复。")
			mode = reads
		case "guest-panicked", "internal-error", "io-error":
			in.StateDetail = core.T(loc, "The guest system stopped working (QEMU reports "+g.qmp+"): only a hard reset or a force stop helps.",
				"客户机系统已停止工作（QEMU 状态："+g.qmp+"），只能强制重启或强制关机。")
			mode = rescue
		case "shutdown":
			in.State = wire.CloudInstanceStateStopping
			mode = reads
		default:
			in.StateDetail = core.T(loc, "QEMU reports "+g.qmp+".", "QEMU 状态："+g.qmp+"。")
			mode = reads
		}
	case "stopped":
		in.State = wire.CloudInstanceStateStopped
	case "unknown", "":
		in.State = wire.CloudInstanceStateUnknown
		in.StateDetail = core.T(loc, "Its node is not answering the cluster.", "它所在的节点没有响应集群。")
		mode = reads
	default:
		in.State = wire.CloudInstanceStateUnknown
		in.StateDetail = core.T(loc, "Proxmox VE reports "+g.status+".", "Proxmox VE 状态："+g.status+"。")
		mode = reads
	}
	hibernated := false
	if g.lock == "suspended" && g.status == "stopped" {
		hibernated = true
		in.StateDetail = core.T(loc, "Hibernated: starting it resumes it.", "已休眠：启动即可恢复。")
	} else if g.lock != "" {
		in.StateDetail = joinDetail(loc, core.T(loc, "Locked by Proxmox VE ("+g.lock+").", "已被 Proxmox VE 锁定（"+g.lock+"）。"),
			in.StateDetail)
		mode = reads
	}

	var acts []wire.CloudActionKind
	switch {
	case mode == power && in.State == wire.CloudInstanceStateRunning:
		acts = append(acts, wire.CloudActionKindStop, wire.CloudActionKindReboot)
		if g.typ == "qemu" {
			acts = append(acts, wire.CloudActionKindHardReset)
		}
		acts = append(acts, wire.CloudActionKindForceStop)
	case mode == power && in.State == wire.CloudInstanceStateStopped:
		acts = append(acts, wire.CloudActionKindStart)
	case mode == rescue:
		if g.typ == "qemu" {
			acts = append(acts, wire.CloudActionKindHardReset)
		}
		acts = append(acts, wire.CloudActionKindForceStop)
	}
	acts = append(acts, wire.CloudActionKindSyncIp)
	if in.State != wire.CloudInstanceStateUnknown {
		acts = append(acts, wire.CloudActionKindSnapshotList)
		if mode == power && !hibernated && (in.State == wire.CloudInstanceStateRunning || in.State == wire.CloudInstanceStateStopped) {
			acts = append(acts, wire.CloudActionKindSnapshotCreate)
		}
	}
	if in.ConsoleURL != "" {
		acts = append(acts, wire.CloudActionKindConsole)
	}
	in.Actions = acts
	in.StateDetail = joinDetail(loc, in.StateDetail, g.addrNote)
	return in
}

// joinDetail is the non-empty sentences of parts, one after the other.
func joinDetail(loc string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, core.T(loc, " ", ""))
}

// consoleURL is the noVNC console of the guest in the user's own browser session: no ticket and no token in it.
func (a *adapter) consoleURL(r ref) string {
	if a.console == "" {
		return ""
	}
	kind := "kvm"
	if r.typ == "lxc" {
		kind = "lxc"
	}
	q := url.Values{"console": {kind}, "novnc": {"1"}, "vmid": {strconv.Itoa(r.vmid)}, "node": {r.node}}
	return a.console + "/?" + q.Encode()
}

// agentIfaces is the QEMU guest agent's network-get-interfaces.
type agentIfaces struct {
	Result []iface `json:"result"`
}

// iface is one interface, as the guest agent and (newer) LXC answers carry it: ip-addresses, or LXC's inet/inet6.
type iface struct {
	Name        string `json:"name"`
	IPAddresses []struct {
		Address string `json:"ip-address"`
	} `json:"ip-addresses"`
	Inet  string `json:"inet"`
	Inet6 string `json:"inet6"`
}

// addresses are a running guest's public and private addresses, read now: from the guest agent for QEMU and from
// the container's interfaces for LXC. A guest without an agent (Proxmox VE answers 500 "QEMU guest agent is not
// running"), or any other failure, has none. A token that may not read them (401/403) has none either, and note says
// which privilege is missing, so the user sees why.
func (a *adapter) addresses(ctx context.Context, acc *account, r ref) (public, private []string, note string) {
	loc := core.Locale(ctx)
	denied := func(err error) string {
		if e := core.AsError(err); e != nil && e.Code == wire.CloudErrorCodeNotSignedIn {
			if r.typ == "qemu" {
				return core.T(loc, "Reading its addresses needs the VM.GuestAgent.Audit privilege (VM.Monitor before Proxmox VE 9).",
					"读取它的地址需要 VM.GuestAgent.Audit 权限（Proxmox VE 9 之前为 VM.Monitor）。")
			}
			return core.T(loc, "Reading its addresses needs the VM.Audit privilege.", "读取它的地址需要 VM.Audit 权限。")
		}
		return ""
	}
	var ifaces []iface
	if r.typ == "qemu" {
		var res agentIfaces
		if err := a.call(ctx, acc, http.MethodGet, r.path("/agent/network-get-interfaces"), nil, nil, &res); err != nil {
			return nil, nil, denied(err)
		}
		ifaces = res.Result
	} else if err := a.call(ctx, acc, http.MethodGet, r.path("/interfaces"), nil, nil, &ifaces); err != nil {
		return nil, nil, denied(err)
	}
	for _, f := range ifaces {
		if skipIface(f.Name) {
			continue
		}
		var raw []string
		for _, ip := range f.IPAddresses {
			raw = append(raw, ip.Address)
		}
		raw = append(raw, strings.Fields(f.Inet)...)
		raw = append(raw, strings.Fields(f.Inet6)...)
		for _, s := range raw {
			if i := strings.IndexByte(s, '/'); i >= 0 {
				s = s[:i]
			}
			ip, err := netip.ParseAddr(strings.TrimSpace(s))
			if err != nil {
				continue
			}
			ip = ip.Unmap()
			if ip.Zone() != "" || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
				ip.IsMulticast() || ip.IsUnspecified() {
				continue
			}
			if isPrivate(ip) {
				private = append(private, ip.String())
			} else {
				public = append(public, ip.String())
			}
		}
	}
	return core.PlainIPs(public...), core.PlainIPs(private...), ""
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isPrivate(ip netip.Addr) bool { return ip.IsPrivate() || cgnat.Contains(ip) }

// skipIface reports an interface whose addresses are not the guest's own: loopback and the bridges and veths of
// container runtimes inside it.
func skipIface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "cali", "lxcbr", "lxdbr"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
