# Homebox household inventory

Search your self-hosted [Homebox](https://homebox.software) inventory in Persona, read serial/model numbers, warranty details, locations and tags, and register an item with explicit confirmation.

This is a fresh native Persona Go extension built from Homebox's current `/v1/entities` API. It contains no Raycast source, assets, branding or legacy item/location paths.

## Set up

1. Create an API token in your Homebox profile.
2. In **Settings › Extensions › Homebox**, enter the instance URL and API token. For example `https://homebox.example.com`; a local instance can use `http://127.0.0.1:7745`. The extension appends `/api` unless it is already present.

Remote instances require HTTPS. URLs must not contain credentials. The token arrives through the encrypted extension-secret flow and is sent only as a Bearer token to the configured instance. Redirects are not followed.

## Inventory

- `GET /v1/entities` searches names, descriptions, serial/model numbers, manufacturers, notes and tags with `page`, `pageSize=30` and newest-first ordering. Another page is shown only while `page * pageSize < total`.
- `GET /v1/entities/{id}` reads a specific entity by its UUID. Inventory notes and descriptions are untrusted content, not instructions to create or modify items.
- `GET /v1/entity-types` distinguishes item types from location types.
- `GET /v1/entities/tree?withItems=false` returns the location hierarchy, including nested locations. Depth is bounded to 50 levels when flattening for display.
- `GET /v1/tags` returns exact tag UUIDs for confirmed item creation.

## Confirmed item registration

Create with `POST /v1/entities` using a discovered non-location `entityTypeId`, a location-tree `parentId`, name, description, positive quantity and optional discovered tag UUIDs. `parentId` is the ordinary location relationship; the separate `locationId` override is not set. The UI selects a non-location type and location, then reviews the item before creating it.

No deletion, attachments, maintenance records, arbitrary field updates, location overrides or group administration are exposed.

Before a write, the normalized operation and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without the token). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full rather than losing replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Homebox before retrying; an error response is not proof that an item was or was not created.

## Verify

```sh
go -C extensions/homebox test -race ./...
scripts/package.sh homebox
```

Synthetic fixtures cover search/pagination, entity metadata, location trees, parent-location creation, replay after restart, unknown outcomes and localized views. No real inventory or account write is used.

## Public API

[Homebox Swagger specification](https://github.com/sysadminsmedia/homebox/blob/main/backend/app/api/static/docs/swagger.json) and [entity repository definitions](https://github.com/sysadminsmedia/homebox/blob/main/backend/internal/data/repo/repo_entities.go): `/v1/entities`, `/v1/entities/{id}`, `/v1/entity-types`, `/v1/entities/tree`, and `/v1/tags`.
