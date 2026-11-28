// Package aws is the cloud-hosts adapter for Amazon Web Services: EC2 instances (the Query API) and Lightsail
// instances (the JSON API), signed with a pure-Go AWS Signature Version 4 signer. An account is one profile of the
// user's shared credentials file in one region, named "<profile>/<region>".
package aws

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Config is NewEC2's and NewLightsail's.
type Config struct {
	// Credentials is the text of an AWS shared credentials file: every [profile] with aws_access_key_id and
	// aws_secret_access_key (and optionally aws_session_token) is one profile.
	Credentials string
	// Regions are the EC2 regions to look in (us-east-1 when empty).
	Regions []string
	// LightsailRegions are the Lightsail regions to look in (Lightsail is not offered when empty).
	LightsailRegions []string
	// HTTP is the client (core.NewHTTPClient when nil).
	HTTP *http.Client
	// Now is the clock (time.Now when nil).
	Now func() time.Time
	// Endpoint overrides the base address of service ("ec2" or "lightsail") in region. Tests and overrides only:
	// when nil the vendor's own https://ec2.<region>.amazonaws.com and https://lightsail.<region>.amazonaws.com.
	Endpoint func(service, region string) string
}

// Budgets of the three calls, a little under the hub's bounds (20 s, 15 s, 30 s).
const (
	listBudget     = 18 * time.Second
	instanceBudget = 14 * time.Second
	actionBudget   = 28 * time.Second
	parallel       = 8                 // accounts read at once
	listCap        = core.MaxInstances // instances read of one account: the Provider says a list this long may be incomplete
)

type profile struct {
	name  string
	creds credentials
}

// account is one profile in one region.
type account struct {
	profile profile
	region  string
}

func (a account) name() string { return a.profile.name + "/" + a.region }

// client is what EC2 and Lightsail share: the profiles, the regions, the transport and the signing.
type client struct {
	service  string // the signing name and the endpoint's first label
	vendor   string // how errors name the vendor
	profiles []profile
	regions  []string
	http     *http.Client
	now      func() time.Time
	endpoint func(service, region string) string
}

var regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)

// otherPartition reports whether region lies outside the commercial (aws) and GovCloud (aws-us-gov) partitions, whose
// endpoints are all under amazonaws.com. China (aws-cn, amazonaws.com.cn) and the isolated regions use other hosts and
// sign-ins, which this adapter does not reach.
func otherPartition(region string) bool {
	return strings.HasPrefix(region, "cn-") || strings.HasPrefix(region, "us-iso") || strings.HasPrefix(region, "eu-isoe-")
}

// govCloud reports whether region is in AWS GovCloud (US), whose console is not the commercial one.
func govCloud(region string) bool { return strings.HasPrefix(region, "us-gov-") }

func newClient(cfg Config, service, vendor string, regions []string) (*client, error) {
	profiles, err := parseProfiles(cfg.Credentials)
	if err != nil {
		return nil, err
	}
	for _, r := range regions {
		if !regionPattern.MatchString(r) {
			return nil, core.Failed("%q is not an AWS region name", core.OneLine(r, 0))
		}
		if otherPartition(r) {
			return nil, core.Unsupported("The AWS region %s is in a separate AWS partition (such as AWS China), which is not supported.", r)
		}
	}
	c := &client{service: service, vendor: vendor, profiles: profiles, regions: regions,
		http: cfg.HTTP, now: cfg.Now, endpoint: cfg.Endpoint}
	if c.http == nil {
		c.http = core.NewHTTPClient(core.HTTPOptions{})
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.endpoint == nil {
		c.endpoint = func(service, region string) string { return "https://" + service + "." + region + ".amazonaws.com" }
	}
	return c, nil
}

// parseProfiles reads the shared credentials file: profiles in name order, each with both keys.
func parseProfiles(text string) ([]profile, error) {
	if strings.TrimSpace(text) == "" {
		return nil, core.NotSignedIn("No AWS credentials are set: paste a shared credentials file with aws_access_key_id and aws_secret_access_key.")
	}
	ini := core.ParseINI(text)
	names := make([]string, 0, len(ini))
	for n := range ini {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []profile
	for _, n := range names {
		s := ini[n]
		id, secret := strings.TrimSpace(s["aws_access_key_id"]), strings.TrimSpace(s["aws_secret_access_key"])
		if id == "" || secret == "" || !core.ValidAccountName(n) {
			continue
		}
		out = append(out, profile{name: n, creds: credentials{AccessKeyID: id, SecretAccessKey: secret,
			SessionToken: strings.TrimSpace(s["aws_session_token"])}})
	}
	if len(out) == 0 {
		return nil, core.NotSignedIn("The AWS credentials have no profile with both aws_access_key_id and aws_secret_access_key.")
	}
	return out, nil
}

// SecretValues are every key the adapter holds.
func (c *client) SecretValues() []string {
	var out []string
	for _, p := range c.profiles {
		out = append(out, p.creds.AccessKeyID, p.creds.SecretAccessKey)
		if p.creds.SessionToken != "" {
			out = append(out, p.creds.SessionToken)
		}
	}
	return out
}

// accounts are every profile in every region, in that order.
func (c *client) accounts() []account {
	var out []account
	for _, p := range c.profiles {
		for _, r := range c.regions {
			out = append(out, account{profile: p, region: r})
		}
	}
	return out
}

// account is the account named name; loc words the error.
func (c *client) account(loc string, name wire.CloudAccountID) (account, error) {
	prof, region, ok := strings.Cut(string(name), "/")
	if ok {
		for _, p := range c.profiles {
			if p.name != prof {
				continue
			}
			for _, r := range c.regions {
				if r == region {
					return account{profile: p, region: r}, nil
				}
			}
		}
	}
	return account{}, core.NotFound("%s", core.T(loc,
		"There is no AWS account "+core.OneLine(string(name), 0)+" set up for "+c.vendor+".",
		c.vendor+" 没有设置账户 "+core.OneLine(string(name), 0)+"。"))
}

// eachAccount reads every account with read, parallel at most 8 at a time: the instances in account order, the names
// of the accounts that could not be read, and an error only when none could be.
func (c *client) eachAccount(ctx context.Context, read func(context.Context, account) ([]wire.CloudInstance, error)) ([]wire.CloudInstance, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, listBudget)
	defer cancel()
	accts := c.accounts()
	lists := make([][]wire.CloudInstance, len(accts))
	errs := make([]error, len(accts))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, a := range accts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			lists[i], errs[i] = read(ctx, a)
		}()
	}
	wg.Wait()
	var out []wire.CloudInstance
	var problems []string
	var first error
	for i, a := range accts {
		if errs[i] != nil {
			problems = append(problems, a.name())
			if first == nil {
				first = errs[i]
			}
			continue
		}
		out = append(out, lists[i]...)
	}
	if len(problems) == len(accts) && first != nil {
		return nil, nil, first
	}
	return out, problems, nil
}

// post sends body to the service's endpoint in a's region, signed with a's keys.
func (c *client) post(ctx context.Context, a account, contentType string, extra http.Header, body []byte) (*core.Response, error) {
	u, err := core.JoinURL(c.endpoint(c.service, a.region), "/", nil)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, core.Failed("%s", core.T(core.Locale(ctx), "The request to "+c.vendor+" could not be made.", "无法向 "+c.vendor+" 发出请求。"))
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "*/*")
	for k, v := range extra {
		req.Header[k] = v
	}
	signRequest(req, body, a.profile.creds, a.region, c.service, c.now())
	return core.Do(ctx, c.http, req)
}

var keyIDPattern = regexp.MustCompile(`\b(AKIA|ASIA|AIDA|AROA)[A-Z0-9]{12,}\b`)

// clean is a vendor's message on one line without any key this adapter holds or anything that looks like a key id.
func (c *client) clean(msg string) string {
	for _, s := range c.SecretValues() {
		if len(s) >= 4 {
			msg = strings.ReplaceAll(msg, s, "[redacted]")
		}
	}
	// Not clipped: the Provider redacts first and clips after, so no first part of a credential is left.
	return core.OneLine(keyIDPattern.ReplaceAllString(msg, "[redacted]"), 0)
}

// actionsFor builds an instance's actions: power first, then the reads.
func actionsFor(power []wire.CloudActionKind, snapshots, snapshotCreate, console bool) []wire.CloudActionKind {
	out := append([]wire.CloudActionKind{}, power...)
	out = append(out, wire.CloudActionKindSyncIp)
	if snapshots {
		out = append(out, wire.CloudActionKindSnapshotList)
		if snapshotCreate {
			out = append(out, wire.CloudActionKindSnapshotCreate)
		}
	}
	if console {
		out = append(out, wire.CloudActionKindConsole)
	}
	return out
}
