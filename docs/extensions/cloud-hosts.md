# Cloud Hosts

[Documentation](../README.md) · [Source](../../extensions/cloud-hosts/)

Cloud Hosts is a provider for Persona's `cloud_hosts` extension point. It lists the servers you rent (cloud instances, VPS behind a hosting panel, machines without any API), says whether each one is running, and starts, shuts down and restarts them, reads their addresses back, takes snapshots and opens their console. Persona puts those operations on the computers' page, in the offline sheet (**Start and reconnect**) and in the assistant's hands, and holds every change behind its own confirmation gate before this extension hears of it.

It needs a hub with the `cloud_hosts` extension point (build `202611270000` or later). The program is one Go binary with no dependency beyond the Go standard library and the Persona SDK: request signing (AWS Signature Version 4, Google service-account JWT, OCI HTTP signatures) is implemented here, and no vendor SDK is bundled.

## Providers

Each vendor is its own provider (a `cloud_hosts` contribution). A provider you have not set up is not offered.

| Provider (id) | Vendor badge | Account | Instance id | Standard actions | Custom actions |
| --- | --- | --- | --- | --- | --- |
| Amazon EC2 (`aws-ec2`) | `aws` | `<profile>/<region>` | EC2 instance id | `start`, `stop`, `reboot`, `force_stop`, `sync_ip`, `snapshot_list`, `snapshot_create`, `console` | none |
| Amazon Lightsail (`aws-lightsail`) | `aws` | `<profile>/<region>` | instance name | `start`, `stop`, `reboot`, `force_stop`, `sync_ip`, `snapshot_list`, `snapshot_create`, `console` | none |
| Google Compute Engine (`gcp`) | `gcp` | project id | `<zone>/<name>` | `start`, `stop`, `hard_reset`, `sync_ip`, `snapshot_list`, `snapshot_create`, `console` | none |
| Oracle Cloud Infrastructure (`oci`) | `oci` | profile name | instance OCID | `start`, `stop`, `reboot`, `hard_reset`, `force_stop`, `sync_ip`, `console` | `keepalive` (read) |
| Pterodactyl / Pelican Panel (`pterodactyl`) | `pterodactyl` | key name | server identifier | `start`, `stop`, `reboot`, `force_stop`, `sync_ip`, `snapshot_list`, `snapshot_create` (backups), `console` | none |
| SolusVM (`solusvm`) | `solusvm` | `v1` | your label (`vps` for a single `key:hash`) | `start`, `stop`, `reboot`, `sync_ip`, `console` | none |
| SolusVM 2 (`solusvm2`) | `solusvm` | token name | server id | `start`, `stop`, `reboot`, `force_stop`, `sync_ip`, `snapshot_list`, `snapshot_create`, `console` | none |
| Virtualizor (`virtualizor`) | `virtualizor` | credential name | VPS id | `start`, `stop`, `reboot`, `force_stop`, `sync_ip`, `console` | none |
| Proxmox VE (`proxmox`) | `proxmox` | token name | `<qemu or lxc>/<vmid>`, such as `qemu/100` (the node is the zone) | `start`, `stop`, `reboot`, `hard_reset` (VMs), `force_stop`, `sync_ip`, `snapshot_list`, `snapshot_create`, `console` | none |
| Hetzner Cloud (`hetzner`) | `hetzner` | token name | server id | `start`, `stop`, `reboot`, `hard_reset`, `force_stop`, `sync_ip`, `snapshot_list`, `snapshot_create`, `console` | none |
| DigitalOcean (`digitalocean`) | `digitalocean` | token name | droplet id | `start`, `stop`, `reboot`, `hard_reset`, `force_stop`, `sync_ip`, `snapshot_list`, `snapshot_create`, `console` | none |
| Vultr (`vultr`) | `vultr` | key name | instance id | `start`, `hard_reset`, `force_stop`, `sync_ip`, `snapshot_create`, `console` | none |
| Custom hosts (`custom`) | `custom` | `custom` | the host's `name` | the ones you gave a webhook for, `sync_ip`, `console` | `renew` (confirm), `probe` (read) |

An instance offers only what fits its state: a stopped one offers `start`, a running one the ways to stop and restart it. An action a vendor does not have is left out rather than faked: Vultr has no graceful shutdown, so `stop` is not offered there, and its restart is a hard one, offered as `hard_reset`. A token name is `default` when the secret holds a single credential.

How the vendors' states map to Persona's: on its way up is `starting`, on its way down `stopping`; an instance the vendor stopped and that can be started again (an EC2 Spot instance stopped by an interruption, a preempted Compute Engine Spot or preemptible VM) is `preempted` rather than `stopped`, so the apps can say why it is off; deleted or being deleted is `terminated`, also when a preemption ended it for good (an EC2 Spot instance terminated by its interruption, an OCI preemptible instance, which Oracle always terminates); `unknown` when the vendor's state cannot be read or has no equivalent (Compute Engine `REPAIRING`, OCI `MOVING`, a Proxmox node that does not answer), and for a custom host without a probe or whose probe could not decide. A suspended Compute Engine VM reads as `stopped`, and `start` resumes it.

### Start and reconnect

`start` answers as soon as the vendor accepted the request; it never waits for the instance. The hub then follows `cloud.instance` until the state is `running` and reconnects the computer. Addresses are read from the vendor on every `cloud.instance` and `sync_ip`, never from a cache, so an instance that comes back with another public address reports the new one. Only `cloud.instances` without `refresh` may answer a list up to 15 seconds old.

### Oracle Cloud keep-alive check

Oracle reclaims idle Always Free instances (`VM.Standard.E2.1.Micro`, `VM.Standard.A1.Flex`). For those shapes the instance carries `lifecycle.keepalive_at` and offers **Keep-alive check**: a read-only action that asks the Monitoring service for the instance's CPU (and, where reported, memory and network) utilization over the last seven days and says whether it looks active enough. It changes nothing at Oracle and needs the Oracle Cloud Agent's monitoring plugin on the instance. `keepalive_at` is seven days after the last check that found the instance active, or seven days from now when no check in the last seven days did. A preemptible instance offers only `sync_ip` and `console`.

### Custom hosts

For a free machine or a hosted server without an API, write one entry per host in **Custom hosts** as `key=value` pairs separated by semicolons:

```text
name=free-vps; address=203.0.113.10; probe=tcp:22; start=/free-vps/start; stop=/free-vps/stop; console=https://panel.example.com/vps/1; expires=2027-01-31; term_days=30; renew=/free-vps/renew
```

| Key | Meaning |
| --- | --- |
| `name` | Required. The host's id: 1 to 63 lowercase letters, digits, `.`, `_` and `-`, starting with a letter or a digit. |
| `title` | What people call it. |
| `address` | Its address: a plain IP (reported), or a host name (used for probing only). |
| `private_address` | Its private address: a plain IP. |
| `probe` | How to tell whether it is up: `tcp:<port>`, `http:<port>[/path]`, `https:<port>[/path]` or a full `https://` address. Without a probe the state is `unknown`. |
| `start`, `stop`, `reboot`, `hard_reset`, `force_stop` | A webhook for that action: a path added to **Custom hosts: webhook address**, or a full `https://` address. Only actions with a webhook are offered. |
| `renew` | A webhook called by **Renew** before the dates move. |
| `method` | `POST` (default) or `GET`. |
| `console` | An `https://` address of its panel page. |
| `expires`, `renews` | When what was paid for ends and when it renews next: `2027-01-31` or an RFC 3339 time. |
| `term_days` | How far **Renew** moves those dates (30 by default). |
| `auto_renew`, `spot` | `true` or `false`. |
| `keepalive_days` | When set, `keepalive_at` is that many days after the last successful probe. |
| `region`, `zone`, `size`, `cpu`, `memory_mb`, `disk_gb`, `arch`, `os` | Shown with the host. |

A webhook receives `{"action": …, "host": …, "request_id": …}` as JSON (nothing with `GET`), an `Idempotency-Key` header with the request id and, when **Custom hosts: webhook token** is set, `Authorization: Bearer <token>`. Any 2xx answer accepts the action, unless its JSON body has `"status": "refused"`: the action is then refused and the body's `"message"`, if any, is shown. A webhook token must be at least 8 characters; do not put a short secret in a webhook's query string.

**Renew** asks for a confirmation, calls the `renew` webhook when there is one and then moves `expires` and `renews` forward by `term_days` (counted from today when a date has already passed); the new dates are kept in the extension's data directory. **Check reachability** probes the host now and says how long the answer took.

The hub's sandbox only lets the program reach the webhook address you configured. To probe a host by its own address, or to call a webhook on another host, allow that host for this extension in the admin UI (an installed extension's details › Network and isolation). A probe the sandbox refused reads as `unknown` and says so.

## Settings and secrets

Credentials are the extension's secrets: the hub seals them, hands them only to this program when it starts, and the program keeps them in memory. They are never written to its data directory, to a log, to an answer, to an error text or to a console link; every text that leaves the program is checked for them once more and a match is replaced by `[redacted]`.

| Provider | Settings | Secret | What to put in the secret |
| --- | --- | --- | --- |
| Amazon EC2 | `aws_regions` (default `us-east-1`) | `aws_credentials` | The text of an AWS credentials file: `[default]` and further profiles with `aws_access_key_id`, `aws_secret_access_key` and optionally `aws_session_token`. Every profile is read in every region. |
| Amazon Lightsail | `aws_lightsail_regions` | `aws_credentials` | The same secret. Lightsail is offered only when regions are named. |
| Google Compute Engine | `gcp_projects` (optional, read with the first key) | `gcp_service_account` | A service-account JSON key; several keys one after another for several projects. |
| Oracle Cloud Infrastructure | `oci_compartments` (optional) | `oci_config`, `oci_private_key` | The text of an OCI configuration file (`[DEFAULT]` with `user`, `fingerprint`, `tenancy`, `region`), and the unencrypted PEM private key of that API key. |
| Pterodactyl / Pelican | `pterodactyl_url` | `pterodactyl_api_key` | A client API key; `name=key` per line for several logins. |
| SolusVM | `solusvm_url`, `solusvm_tls_fingerprint` | `solusvm_credentials` | `key:hash`; `label=key:hash` per line for several servers (SolusVM 1 has one pair per server). |
| SolusVM 2 | `solusvm2_url` | `solusvm2_api_token` | An API token; `name=token` per line for several accounts. |
| Virtualizor | `virtualizor_url`, `virtualizor_tls_fingerprint` | `virtualizor_credentials` | `key:password` of the end-user API; `name=key:password` per line for several. |
| Proxmox VE | `proxmox_url`, `proxmox_tls_fingerprint` | `proxmox_api_token` | `user@realm!tokenid=secret`; `name=user@realm!tokenid=secret` per line for several tokens. |
| Hetzner Cloud | none | `hetzner_api_token` | A read and write project token; `name=token` per line for several projects. |
| DigitalOcean | none | `digitalocean_api_token` | A personal access token; `name=token` per line for several accounts. |
| Vultr | none | `vultr_api_key` | The API key; `name=key` per line for several accounts. |
| Custom hosts | `custom_hosts`, `custom_webhook_url` | `custom_webhook_token` (optional) | A Bearer token for your webhooks, at least 8 characters. |

Give each credential the least it needs. For AWS, an IAM user limited to `ec2:DescribeInstances`, `ec2:StartInstances`, `ec2:StopInstances`, `ec2:RebootInstances`, `ec2:DescribeSnapshots`, `ec2:CreateSnapshot`, `ec2:CreateTags` and the matching `lightsail:` actions. For Google Cloud, a service account with Compute Instance Admin (v1) on the project. For Oracle Cloud, a policy that allows `manage instance-family`, `read virtual-network-family` and `read metrics` in the compartment. For Proxmox VE, a token with `VM.Audit`, `VM.PowerMgmt` and `VM.Snapshot`, and `VM.GuestAgent.Audit` (`VM.Monitor` before Proxmox VE 9) so the addresses of virtual machines can be read through the guest agent.

A panel address must be `https`. For a SolusVM 1, Virtualizor or Proxmox VE panel with a self-signed certificate, put the SHA-256 fingerprint of that certificate (64 hex digits; colons and spaces are allowed) in the panel's fingerprint setting: the connection is then accepted for exactly that certificate, and certificate checking is never switched off. Pterodactyl / Pelican and SolusVM 2 have no such setting and need a certificate a public authority signed.

## Network

The program reaches only what the manifest's `permissions.network` names:

- `*.amazonaws.com` (the regional EC2 and Lightsail endpoints), `compute.googleapis.com` and `oauth2.googleapis.com`, `*.oraclecloud.com` (the regional Compute and Monitoring endpoints), `api.hetzner.cloud`, `api.digitalocean.com`, `api.vultr.com`;
- the panel and webhook addresses you wrote in the settings (`config:pterodactyl_url`, `config:solusvm_url`, `config:solusvm2_url`, `config:virtualizor_url`, `config:proxmox_url`, `config:custom_webhook_url`), over `https` only.

It follows no redirect, so a credential is never carried to an address an answer points at.

## Errors

A vendor's failure is answered in Persona's closed vocabulary with one line of the vendor's own words:

| Code | When |
| --- | --- |
| `not_found` | The instance or account is gone. |
| `not_signed_in` | The credentials are missing, wrong, expired or lack the permission. |
| `unsupported` | The provider or the instance does not have this action. |
| `invalid_state` | The vendor refuses because of the instance's state (starting something that runs). |
| `rate_limited` | The vendor is throttling; the same request may be sent again. |
| `capacity` | The vendor cannot place the instance now (no Spot capacity, a zone out of resources, a quota). |
| `refused` | The vendor said no for a reason of its own. |
| `failed` | Anything else. |

`cloud.action` treats `request_id` as an idempotency key: the same id again answers what was answered, without asking the vendor twice, and where a vendor takes a client token of its own (Google's `requestId`, Oracle's `opc-retry-token`) the id is passed on. A rate limit, or a connection that was never made, decided nothing and is not remembered, so the same request can be tried again. A timeout or a vendor error after the request went out is remembered: the vendor may have acted, so the same id answers the same failure and the instance's state says the rest. `sync_ip` and `console` are reads and are always asked anew.

## Not in this version

- Creating, resizing or deleting instances, restoring or deleting snapshots, firewall rules and billing.
- More than one panel address per panel family.
- AWS China (`cn-*`) and the isolated partitions: naming one in the regions stops that provider with an error. GovCloud instances are listed and controlled but have no console link.
- EC2 instances with an instance-store root can only be restarted; EC2, Compute Engine and Lightsail snapshots cover the root or boot disk. Vultr snapshots can be taken but not listed per instance; DigitalOcean answers a new snapshot without its id. Hetzner's console link opens the console's start page.
- A list stops at 500 servers per provider and is then marked incomplete.
- AWS IAM roles, SSO profiles and instance profiles (static access keys, with or without a session token, only); encrypted OCI private keys.

## Verify

```sh
go -C extensions/cloud-hosts vet ./...
go -C extensions/cloud-hosts test -race -count=1 ./...
scripts/package.sh cloud-hosts
```

Every vendor is exercised against offline HTTP mocks; the tests need no account and reach no network.
