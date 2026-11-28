package core

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// How long the Provider keeps what it knows.
const (
	listTTL    = 15 * time.Second // cloud.instances without refresh may answer a list this fresh
	requestTTL = 6 * time.Hour    // how long a request_id's answer is remembered
	maxRecords = 2048             // request_ids remembered at once
)

// Options are a Provider's.
type Options struct {
	// ID is the cloud_hosts contribution's id.
	ID string
	// Custom are the manifest's custom actions of this contribution with their safety level: an instance may offer
	// only these, and a read one is never answered from the idempotency record.
	Custom map[wire.CloudCustomActionID]wire.CloudSafety
	// Redactor removes credentials from every answer; the adapter's own (SecretHolder) are added to it.
	Redactor *Redactor
	// Now is the clock (time.Now when nil).
	Now func() time.Time
}

// Provider answers the cloud.* calls of one contribution from its Adapter.
type Provider struct {
	id       string
	adapter  Adapter
	consoler Consoler
	custom   map[wire.CloudCustomActionID]wire.CloudSafety
	redact   *Redactor
	now      func() time.Time

	mu      sync.Mutex
	list    *wire.CloudInstanceList // the last complete list, with listZh saying which language its words are in
	listZh  bool
	listAt  time.Time
	listGen uint64
	records map[wire.CloudRequestID]*record
}

// record is one request_id: what was asked, and its answer once done is closed.
type record struct {
	key  string
	at   time.Time
	done chan struct{}
	res  wire.CloudActionResult
	err  *Error
}

// NewProvider wraps adapter.
func NewProvider(adapter Adapter, o Options) *Provider {
	p := &Provider{id: o.ID, adapter: adapter, custom: o.Custom, redact: o.Redactor, now: o.Now,
		records: map[wire.CloudRequestID]*record{}}
	if p.redact == nil {
		p.redact = &Redactor{}
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.custom == nil {
		p.custom = map[wire.CloudCustomActionID]wire.CloudSafety{}
	}
	if c, ok := adapter.(Consoler); ok {
		p.consoler = c
	}
	if s, ok := adapter.(SecretHolder); ok {
		p.redact.Add(s.SecretValues()...)
	}
	return p
}

// ID is the contribution's id.
func (p *Provider) ID() string { return p.id }

// Methods are the cloud namespace's methods for this provider.
func (p *Provider) Methods() ext.Methods {
	return ext.Methods{
		"instances": ext.Method(p.Instances),
		"instance":  ext.Method(p.Instance),
		"action":    ext.Method(p.Action),
		"snapshots": ext.Method(p.Snapshots),
	}
}

// fail is err as the wire's error: its code, and its message on one line without credentials.
func (p *Provider) fail(err error) error {
	e := AsError(err)
	if e == nil {
		return nil
	}
	code := e.Code
	if !code.Valid() {
		code = wire.CloudErrorCodeFailed
	}
	msg := OneLine(p.redact.Redact(e.Message), maxMessage)
	if msg == "" {
		msg = string(code)
	}
	return &ext.Error{Code: string(code), Message: msg}
}

func validRef(ref wire.CloudInstanceRef) bool {
	return validID(string(ref.Account), maxAccountID) && validID(string(ref.ID), maxInstanceID)
}

// copyList is l with its own instances, so neither the cache nor a caller can change the other's.
func copyList(l wire.CloudInstanceList) wire.CloudInstanceList {
	l.Instances = append([]wire.CloudInstance(nil), l.Instances...)
	return l
}

// Instances is cloud.instances.
func (p *Provider) Instances(ctx context.Context, params wire.CloudInstancesParams) (wire.CloudInstanceList, error) {
	ctx = WithLocale(ctx, params.Locale)
	p.mu.Lock()
	if !params.Refresh && p.list != nil && p.listZh == zhLocale(params.Locale) && p.now().Sub(p.listAt) < listTTL {
		out := copyList(*p.list)
		p.mu.Unlock()
		return out, nil
	}
	gen := p.listGen
	p.mu.Unlock()

	list, given, err := p.adapter.Instances(ctx)
	if err != nil {
		return wire.CloudInstanceList{}, p.fail(err)
	}
	out := wire.CloudInstanceList{Instances: make([]wire.CloudInstance, 0, len(list))}
	seen := map[wire.CloudInstanceRef]bool{}
	for _, in := range list {
		ref := wire.CloudInstanceRef{Account: in.Account, ID: in.ID}
		if !validRef(ref) || seen[ref] {
			continue
		}
		seen[ref] = true
		out.Instances = append(out.Instances, p.cleanInstance(in))
	}
	var detail []string
	// An adapter stops reading at the limit, so a full list is one that may have more.
	if len(out.Instances) >= MaxInstances {
		out.Instances = out.Instances[:MaxInstances]
		out.Partial = true
		detail = append(detail, T(params.Locale, "Only the first 500 servers are listed.", "只列出前 500 台服务器。"))
	}
	if problems := CleanList(given); len(problems) > 0 { // CleanList copies: the adapter's slice is its own
		out.Partial = true
		sort.Strings(problems)
		detail = append(detail, T(params.Locale, "Could not read: ", "未能读取：")+strings.Join(problems, ", "))
	}
	out.Detail = OneLine(p.redact.Redact(strings.Join(detail, " ")), maxListDetail)

	p.mu.Lock()
	if gen == p.listGen && !out.Partial { // a write since, or an incomplete list, is not worth keeping
		kept := copyList(out)
		p.list, p.listZh, p.listAt = &kept, zhLocale(params.Locale), p.now()
	}
	p.mu.Unlock()
	return out, nil
}

// forget drops the cached list: after a write the instances are not what they were.
func (p *Provider) forget() {
	p.mu.Lock()
	p.list = nil
	p.listGen++
	p.mu.Unlock()
}

// read is one instance from the vendor now, held to the name it was asked by.
func (p *Provider) read(ctx context.Context, ref wire.CloudInstanceRef, locale string) (wire.CloudInstance, *Error) {
	if !validRef(ref) {
		return wire.CloudInstance{}, NotFound("%s", T(locale, "No such server.", "没有这台服务器。"))
	}
	in, err := p.adapter.Instance(WithLocale(ctx, locale), ref)
	if err != nil {
		return wire.CloudInstance{}, AsError(err)
	}
	if in.Account != ref.Account || in.ID != ref.ID {
		return wire.CloudInstance{}, NotFound("%s", T(locale, "No such server.", "没有这台服务器。"))
	}
	out := p.cleanInstance(in)
	// What was just read is newer than the kept list: a list answered from it must not say the old address.
	p.mu.Lock()
	p.listGen++ // a list that was being read meanwhile is older than this read: it is not kept
	if p.list != nil {
		for i := range p.list.Instances {
			if p.list.Instances[i].Account == ref.Account && p.list.Instances[i].ID == ref.ID {
				if p.listZh == zhLocale(locale) {
					p.list.Instances[i] = out
				} else {
					p.list = nil
				}
				break
			}
		}
	}
	p.mu.Unlock()
	return out, nil
}

// Instance is cloud.instance. It always asks the vendor: the hub reads it to follow a start and before every gate.
func (p *Provider) Instance(ctx context.Context, params wire.CloudInstanceParams) (wire.CloudInstance, error) {
	in, err := p.read(ctx, params.Instance, params.Locale)
	if err != nil {
		return wire.CloudInstance{}, p.fail(err)
	}
	return in, nil
}

// Snapshots is cloud.snapshots.
func (p *Provider) Snapshots(ctx context.Context, params wire.CloudSnapshotsParams) (wire.CloudSnapshotList, error) {
	if !validRef(params.Instance) {
		return wire.CloudSnapshotList{}, p.fail(NotFound("%s", T(params.Locale, "No such server.", "没有这台服务器。")))
	}
	list, err := p.adapter.Snapshots(WithLocale(ctx, params.Locale), params.Instance)
	if err != nil {
		return wire.CloudSnapshotList{}, p.fail(err)
	}
	return wire.CloudSnapshotList{Snapshots: p.cleanSnapshots(list)}, nil
}

// Action is cloud.action.
func (p *Provider) Action(ctx context.Context, params wire.CloudActionParams) (wire.CloudActionResult, error) {
	loc := params.Locale
	ctx = WithLocale(ctx, loc)
	if !params.Action.Valid() || params.Action == wire.CloudActionKindSnapshotList {
		return wire.CloudActionResult{}, p.fail(Unsupported("%s", T(loc, "This action is not supported.", "不支持这个操作。")))
	}
	if !validRef(params.Instance) {
		return wire.CloudActionResult{}, p.fail(NotFound("%s", T(loc, "No such server.", "没有这台服务器。")))
	}
	if !validID(string(params.RequestID), maxRequestID) {
		return wire.CloudActionResult{}, p.fail(Failed("%s", T(loc, "The request has no usable id.", "请求缺少可用的编号。")))
	}
	safety := wire.CloudSafetyConfirm
	switch params.Action {
	case wire.CloudActionKindCustom:
		s, ok := p.custom[params.Custom]
		if !ok {
			return wire.CloudActionResult{}, p.fail(Unsupported("%s", T(loc, "This action is not supported.", "不支持这个操作。")))
		}
		safety = s
	case wire.CloudActionKindSyncIp, wire.CloudActionKindConsole:
		safety = wire.CloudSafetyRead
	default:
		if params.Custom != "" {
			return wire.CloudActionResult{}, p.fail(Unsupported("%s", T(loc, "This action is not supported.", "不支持这个操作。")))
		}
	}
	name := ""
	if params.Action == wire.CloudActionKindSnapshotCreate {
		name = OneLine(params.Name, maxRequestName)
	}
	a := Action{Ref: params.Instance, Kind: params.Action, Custom: params.Custom, Name: name,
		RequestID: params.RequestID, Locale: loc}

	// A read changes nothing, so it is always asked anew: sync_ip in particular must be the vendor's answer of now.
	if safety == wire.CloudSafetyRead {
		res, err := p.run(ctx, a)
		if err != nil {
			return wire.CloudActionResult{}, p.fail(err)
		}
		return res, nil
	}

	key := strings.Join([]string{string(a.Ref.Account), string(a.Ref.ID), string(a.Kind), string(a.Custom), a.Name}, "\x00")
	for {
		p.mu.Lock()
		p.expire()
		r, ok := p.records[a.RequestID]
		if !ok {
			r = &record{key: key, at: p.now(), done: make(chan struct{})}
			p.records[a.RequestID] = r
			p.mu.Unlock()
			r.res, r.err = p.run(ctx, a)
			p.forget()
			p.mu.Lock()
			if r.err != nil && r.err.Transient {
				delete(p.records, a.RequestID) // nothing was decided: the same id may be tried again
			}
			p.mu.Unlock()
			close(r.done)
			if r.err != nil {
				return wire.CloudActionResult{}, p.fail(r.err)
			}
			return r.res, nil
		}
		p.mu.Unlock()
		if r.key != key {
			return wire.CloudActionResult{}, p.fail(Failed("%s", T(loc, "This request id was already used for another request.", "这个请求编号已用于另一个请求。")))
		}
		select {
		case <-r.done:
		case <-ctx.Done():
			return wire.CloudActionResult{}, p.fail(ctx.Err())
		}
		if r.err != nil && r.err.Transient {
			continue // the first try decided nothing and its record is gone: this call asks itself
		}
		if r.err != nil {
			return wire.CloudActionResult{}, p.fail(r.err)
		}
		return r.res, nil
	}
}

// expire forgets the request ids past their time, and the oldest ones beyond the cap. Called with mu held.
func (p *Provider) expire() {
	now := p.now()
	for id, r := range p.records {
		if now.Sub(r.at) > requestTTL && finished(r) {
			delete(p.records, id)
		}
	}
	if len(p.records) < maxRecords {
		return
	}
	type aged struct {
		id wire.CloudRequestID
		at time.Time
	}
	var old []aged
	for id, r := range p.records {
		if finished(r) {
			old = append(old, aged{id, r.at})
		}
	}
	sort.Slice(old, func(i, j int) bool { return old[i].at.Before(old[j].at) })
	for i := 0; i < len(old) && len(p.records) >= maxRecords; i++ {
		delete(p.records, old[i].id)
	}
}

func finished(r *record) bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// run asks the adapter once and holds the answer to the schema.
func (p *Provider) run(ctx context.Context, a Action) (wire.CloudActionResult, *Error) {
	switch a.Kind {
	case wire.CloudActionKindSyncIp:
		in, err := p.read(ctx, a.Ref, a.Locale)
		if err != nil {
			return wire.CloudActionResult{}, err
		}
		return wire.CloudActionResult{Status: wire.CloudActionStatusDone, Instance: &in}, nil
	case wire.CloudActionKindConsole:
		var res wire.CloudActionResult
		if p.consoler != nil {
			r, err := p.consoler.Console(ctx, a.Ref, a.Locale)
			if err != nil {
				return wire.CloudActionResult{}, AsError(err)
			}
			res = p.cleanResult(r)
			res.Instance, res.Snapshot, res.Message = nil, nil, ""
		} else {
			in, err := p.read(ctx, a.Ref, a.Locale)
			if err != nil {
				return wire.CloudActionResult{}, err
			}
			res = wire.CloudActionResult{URL: in.ConsoleURL}
		}
		if res.URL == "" {
			return wire.CloudActionResult{}, Unsupported("%s", T(a.Locale, "This server has no console link.", "这台服务器没有控制台链接。"))
		}
		res.Status = wire.CloudActionStatusDone
		return res, nil
	}
	res, err := p.adapter.Action(ctx, a)
	if err != nil {
		return wire.CloudActionResult{}, AsError(err)
	}
	res = p.cleanResult(res)
	if res.Instance != nil && (res.Instance.Account != a.Ref.Account || res.Instance.ID != a.Ref.ID) {
		res.Instance = nil
	}
	if a.Kind != wire.CloudActionKindSnapshotCreate {
		res.Snapshot = nil
	}
	res.URL, res.ExpiresAt = "", time.Time{}
	return res, nil
}
