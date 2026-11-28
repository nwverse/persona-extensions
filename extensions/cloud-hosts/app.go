package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"

	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/aws"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/core"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/digitalocean"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/gcp"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/generic"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/hetzner"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/oci"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/proxmox"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/pterodactyl"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/solusvm"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/virtualizor"
	"github.com/nwverse/persona-extensions/extensions/cloud-hosts/internal/vultr"
	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// The names of the manifest's secrets.
const (
	secretAWS          = "aws_credentials"
	secretGCP          = "gcp_service_account"
	secretOCIConfig    = "oci_config"
	secretOCIKey       = "oci_private_key"
	secretPterodactyl  = "pterodactyl_api_key"
	secretSolusVM      = "solusvm_credentials"
	secretSolusVM2     = "solusvm2_api_token"
	secretVirtualizor  = "virtualizor_credentials"
	secretProxmox      = "proxmox_api_token"
	secretHetzner      = "hetzner_api_token"
	secretDigitalOcean = "digitalocean_api_token"
	secretVultr        = "vultr_api_key"
	secretCustomToken  = "custom_webhook_token"
)

// settings is the manifest's configuration as the hub sends it.
type settings struct {
	AWSRegions             []string `json:"aws_regions"`
	AWSLightsailRegions    []string `json:"aws_lightsail_regions"`
	GCPProjects            []string `json:"gcp_projects"`
	OCICompartments        []string `json:"oci_compartments"`
	PterodactylURL         string   `json:"pterodactyl_url"`
	SolusVMURL             string   `json:"solusvm_url"`
	SolusVMFingerprint     string   `json:"solusvm_tls_fingerprint"`
	SolusVM2URL            string   `json:"solusvm2_url"`
	VirtualizorURL         string   `json:"virtualizor_url"`
	VirtualizorFingerprint string   `json:"virtualizor_tls_fingerprint"`
	ProxmoxURL             string   `json:"proxmox_url"`
	ProxmoxFingerprint     string   `json:"proxmox_tls_fingerprint"`
	CustomWebhookURL       string   `json:"custom_webhook_url"`
	CustomHosts            []string `json:"custom_hosts"`
}

// allowHTTP lets a panel address be plain http. It is false in the program: the hub's egress proxy lets the
// config: addresses through as https only, and a key must not travel in the clear. Tests point the adapters at
// httptest servers and set it.
var allowHTTP = false

// app holds the providers of this run, by contribution id.
type app struct {
	mu        sync.RWMutex
	providers map[string]*core.Provider
}

// declared are the manifest's cloud_hosts contributions: their ids in order, and each one's custom actions with
// their safety levels. The manifest is the one place they are written.
func declared() (ids []string, custom map[string]map[wire.CloudCustomActionID]wire.CloudSafety) {
	var m struct {
		Contributes struct {
			CloudHosts []wire.CloudHostItem `json:"cloud_hosts"`
		} `json:"contributes"`
	}
	if err := json.Unmarshal(manifest, &m); err != nil {
		panic(err)
	}
	custom = map[string]map[wire.CloudCustomActionID]wire.CloudSafety{}
	for _, c := range m.Contributes.CloudHosts {
		id := string(c.ID)
		ids = append(ids, id)
		custom[id] = map[wire.CloudCustomActionID]wire.CloudSafety{}
		for _, a := range c.CustomActions {
			custom[id][a.ID] = a.Safety
		}
	}
	return ids, custom
}

// init builds the providers from what the hub sent and says which contributions this run has: the ones the user set
// up. The others stay registered and answer not_signed_in with what is missing, for a hub that asks anyway.
func (a *app) init(in *ext.Init) (map[string]any, error) {
	var cfg settings
	if err := in.Config(&cfg); err != nil {
		return nil, &ext.Error{Message: "the configuration could not be read"}
	}
	built := build(cfg, in.Secrets, in.Info().DataDir)
	ids, custom := declared()
	redactor := &core.Redactor{}
	for _, v := range in.Secrets {
		redactor.Add(v)
	}
	providers := map[string]*core.Provider{}
	active := []map[string]string{}
	for _, id := range ids {
		b, ok := built[id]
		if !ok {
			b = result{adapter: missing{core.NotSignedIn("This provider is not part of this version.")}}
		}
		providers[id] = core.NewProvider(b.adapter, core.Options{ID: id, Custom: custom[id], Redactor: redactor})
		if b.configured {
			active = append(active, map[string]string{"id": id})
		}
	}
	a.mu.Lock()
	a.providers = providers
	a.mu.Unlock()
	return map[string]any{"cloud_hosts": active}, nil
}

// handle answers one cloud.* call with the provider its contribution names.
func (a *app) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	id := ext.Contribution(params)
	a.mu.RLock()
	p := a.providers[id]
	a.mu.RUnlock()
	if p == nil {
		return nil, ext.NoContribution(id)
	}
	return p.Methods().For(id)(ctx, method, params)
}

// result is one contribution's adapter and whether the user set it up (credentials or hosts are there, usable or not).
type result struct {
	adapter    core.Adapter
	configured bool
}

// missing is the adapter of a provider that cannot work: every call answers why.
type missing struct{ err *core.Error }

func (m missing) Instances(context.Context) ([]wire.CloudInstance, []string, error) {
	return nil, nil, m.err
}
func (m missing) Instance(context.Context, wire.CloudInstanceRef) (wire.CloudInstance, error) {
	return wire.CloudInstance{}, m.err
}
func (m missing) Action(context.Context, core.Action) (wire.CloudActionResult, error) {
	return wire.CloudActionResult{}, m.err
}
func (m missing) Snapshots(context.Context, wire.CloudInstanceRef) ([]wire.CloudSnapshot, error) {
	return nil, m.err
}

// notSetUp is the adapter of a provider the user gave nothing for.
func notSetUp(what string) result {
	return result{adapter: missing{core.NotSignedIn("Not set up: add %s in this extension's settings.", what)}}
}

// made is the result of an adapter's constructor for a provider the user did set up: a constructor that refuses
// (credentials it cannot read) leaves a provider that says so.
func made(adapter core.Adapter, err error) result {
	if err != nil {
		return result{adapter: missing{core.AsError(err)}, configured: true}
	}
	return result{adapter: adapter, configured: true}
}

// panelURL is a panel's configured address when it is usable: https (or http in tests), a host, no credentials.
func panelURL(raw string) (string, *core.Error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "", core.Failed("The configured address is not a usable URL.")
	}
	if u.Scheme != "https" && !(allowHTTP && u.Scheme == "http") {
		return "", core.Failed("The configured address must start with https://.")
	}
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// build makes every provider's adapter from the configuration and the secrets.
func build(cfg settings, secrets map[string]string, dataDir string) map[string]result {
	secret := func(name string) string { return strings.TrimSpace(secrets[name]) }
	out := map[string]result{}

	// AWS: one credentials file for EC2 and Lightsail; Lightsail only where its regions are named.
	if creds := secret(secretAWS); creds == "" {
		out["aws-ec2"] = notSetUp("the AWS credentials")
		out["aws-lightsail"] = notSetUp("the AWS credentials")
	} else {
		awsCfg := aws.Config{Credentials: creds, Regions: core.CleanList(cfg.AWSRegions),
			LightsailRegions: core.CleanList(cfg.AWSLightsailRegions), Endpoint: overrides.aws}
		out["aws-ec2"] = made(aws.NewEC2(awsCfg))
		if len(awsCfg.LightsailRegions) == 0 {
			out["aws-lightsail"] = notSetUp("the Lightsail regions")
		} else {
			out["aws-lightsail"] = made(aws.NewLightsail(awsCfg))
		}
	}

	if keys := secret(secretGCP); keys == "" {
		out["gcp"] = notSetUp("the Google Cloud service account key")
	} else {
		out["gcp"] = made(gcp.New(gcp.Config{ServiceAccounts: keys, Projects: core.CleanList(cfg.GCPProjects),
			ComputeBase: overrides.gcpCompute, TokenURL: overrides.gcpToken}))
	}

	if conf := secret(secretOCIConfig); conf == "" {
		out["oci"] = notSetUp("the Oracle Cloud API configuration and private key")
	} else {
		out["oci"] = made(oci.New(oci.Config{ConfigFile: conf, PrivateKey: secret(secretOCIKey),
			Compartments: core.CleanList(cfg.OCICompartments), DataDir: dataDir, Endpoint: overrides.oci}))
	}

	// The panels: an address in the configuration and a credential in the secrets, both or neither.
	panel := func(id, what, rawURL, credential string, make func(base string) (core.Adapter, error)) {
		if strings.TrimSpace(rawURL) == "" && credential == "" {
			out[id] = notSetUp(what)
			return
		}
		if strings.TrimSpace(rawURL) == "" || credential == "" {
			out[id] = result{adapter: missing{core.NotSignedIn("Not set up: %s needs both its address and its credential.", what)}, configured: true}
			return
		}
		base, err := panelURL(rawURL)
		if err != nil {
			out[id] = result{adapter: missing{err}, configured: true}
			return
		}
		out[id] = made(make(base))
	}
	panel("pterodactyl", "the Pterodactyl / Pelican panel", cfg.PterodactylURL, secret(secretPterodactyl), func(base string) (core.Adapter, error) {
		return pterodactyl.New(pterodactyl.Config{BaseURL: base, APIKey: secret(secretPterodactyl)})
	})
	panel("solusvm", "the SolusVM panel", cfg.SolusVMURL, secret(secretSolusVM), func(base string) (core.Adapter, error) {
		return solusvm.NewV1(solusvm.V1Config{BaseURL: base, Credentials: secret(secretSolusVM), PinSHA256: cfg.SolusVMFingerprint})
	})
	panel("solusvm2", "the SolusVM 2 panel", cfg.SolusVM2URL, secret(secretSolusVM2), func(base string) (core.Adapter, error) {
		return solusvm.NewV2(solusvm.V2Config{BaseURL: base, Token: secret(secretSolusVM2)})
	})
	panel("virtualizor", "the Virtualizor panel", cfg.VirtualizorURL, secret(secretVirtualizor), func(base string) (core.Adapter, error) {
		return virtualizor.New(virtualizor.Config{BaseURL: base, Credentials: secret(secretVirtualizor), PinSHA256: cfg.VirtualizorFingerprint})
	})
	panel("proxmox", "the Proxmox VE node", cfg.ProxmoxURL, secret(secretProxmox), func(base string) (core.Adapter, error) {
		return proxmox.New(proxmox.Config{BaseURL: base, Token: secret(secretProxmox), PinSHA256: cfg.ProxmoxFingerprint})
	})

	// The token-only clouds.
	token := func(id, what, name string, make func(tokens string) (core.Adapter, error)) {
		if t := secret(name); t == "" {
			out[id] = notSetUp(what)
		} else {
			out[id] = made(make(t))
		}
	}
	token("hetzner", "the Hetzner Cloud API token", secretHetzner, func(t string) (core.Adapter, error) {
		return hetzner.New(hetzner.Config{Tokens: t, BaseURL: overrides.bases["hetzner"]})
	})
	token("digitalocean", "the DigitalOcean API token", secretDigitalOcean, func(t string) (core.Adapter, error) {
		return digitalocean.New(digitalocean.Config{Tokens: t, BaseURL: overrides.bases["digitalocean"]})
	})
	token("vultr", "the Vultr API key", secretVultr, func(t string) (core.Adapter, error) {
		return vultr.New(vultr.Config{Tokens: t, BaseURL: overrides.bases["vultr"]})
	})

	// Custom hosts: the list is the set-up; the webhook address and token are optional.
	if hosts := core.CleanList(cfg.CustomHosts); len(hosts) == 0 {
		out["custom"] = notSetUp("at least one custom host")
	} else {
		base := ""
		var bad *core.Error
		if strings.TrimSpace(cfg.CustomWebhookURL) != "" {
			base, bad = panelURL(cfg.CustomWebhookURL)
		}
		if bad != nil {
			out["custom"] = result{adapter: missing{bad}, configured: true}
		} else {
			out["custom"] = made(generic.New(generic.Config{Hosts: hosts, WebhookBase: base,
				WebhookToken: secret(secretCustomToken), DataDir: dataDir}))
		}
	}
	return out
}

// overrides point the adapters with fixed vendor addresses at test servers. All zero in the program, where each
// adapter uses its vendor's own https address.
var overrides struct {
	aws        func(service, region string) string
	gcpCompute string
	gcpToken   string
	oci        func(service, region string) string
	bases      map[string]string // hetzner, digitalocean, vultr
}
