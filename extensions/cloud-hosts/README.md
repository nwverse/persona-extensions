# Cloud Hosts

[Documentation](../../docs/extensions/cloud-hosts.md) · [All extensions](../../docs/README.md)

Cloud Hosts is a provider for Persona's `cloud_hosts` extension point: it lists the servers you rent, says whether each is running, and starts, shuts down and restarts them, reads their addresses back, takes snapshots and opens their console, from the computers' page, the offline sheet's **Start and reconnect** and the assistant. It covers Amazon EC2 and Lightsail, Google Compute Engine, Oracle Cloud Infrastructure (with a keep-alive check for Always Free instances), Pterodactyl / Pelican, SolusVM 1 and 2, Virtualizor, Proxmox VE, Hetzner Cloud, DigitalOcean, Vultr, and servers without an API through your own webhooks and reachability probes.

Request signing (AWS Signature Version 4, Google service-account JWT, OCI HTTP signatures) is plain Go standard library; no vendor SDK is bundled. Credentials live in the hub's sealed secrets and never appear in an answer, an error, a log or a console link.

See the [full guide](../../docs/extensions/cloud-hosts.md) for the provider matrix, every setting and secret, the custom host syntax and the network the program reaches.

## Layout

```text
main.go, app.go          The program: providers built from the configuration and the secrets
internal/core            The adapter contract and the provider: idempotency, schema limits, redaction
internal/aws             EC2 and Lightsail, Signature Version 4
internal/gcp             Compute Engine, service-account OAuth2
internal/oci             OCI Compute, HTTP signatures, the keep-alive check
internal/pterodactyl     Pterodactyl and Pelican client API
internal/solusvm         SolusVM 1 client API and SolusVM 2
internal/virtualizor     Virtualizor end-user API
internal/proxmox         Proxmox VE
internal/hetzner, internal/digitalocean, internal/vultr
internal/generic         Custom hosts: webhooks, probes, renewal dates
```

## Verify

```sh
go -C extensions/cloud-hosts vet ./...
go -C extensions/cloud-hosts test -race -count=1 ./...
scripts/package.sh cloud-hosts
```
