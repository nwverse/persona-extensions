# Immich photo library

Search your self-hosted [Immich](https://immich.app) photo and video library in Persona, read asset metadata and albums, and change favorite status with explicit confirmation.

This is a fresh native Persona Go extension built from Immich's public OpenAPI specification. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an Immich API key with asset/album read permission. Favorite updates additionally need asset update permission; other write permissions are unnecessary.
2. In **Settings › Extensions › Immich**, enter the instance URL and API key. For example `https://immich.example.com`; a local instance can use `http://127.0.0.1:2283`. The extension appends `/api` unless it is already present.

Remote instances require HTTPS. URLs must not contain credentials. The key arrives through the encrypted extension-secret flow and is sent only as `x-api-key` to the configured instance. Redirects are not followed, so a media redirect cannot receive the key.

## Library

- Filename and favorite searches use `POST /search/metadata` with current structured `filter.originalFileName` / `filter.isFavorite`, `size=40` and `nextCursor`. They do not use the deprecated top-level `query` or `page` fields.
- Natural-language searches use `POST /search/smart` with `query`, `size=40` and EXIF enabled. They do not invent cursor behavior when the smart-search response has no continuation contract.
- `GET /assets/{id}` reads filenames, capture dates, EXIF and people. `GET /albums` and `GET /albums/{id}` read album metadata and assets. Private filesystem paths and raw media are not returned to the agent.
- Metadata, filenames and descriptions are untrusted content, not instructions to update or share media. The extension does not automatically download originals, thumbnails or videos.

## Confirmed favorites

`PUT /assets/{id}` sends only `isFavorite`. No deletion, description/location edits, sharing, album membership changes or user administration are exposed.

Before a write, the normalized operation and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without the API key). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Immich before retrying; an error response is not proof that the favorite update did or did not happen.

## Verify

```sh
go -C extensions/immich test -race ./...
scripts/package.sh immich
```

Synthetic fixtures cover `x-api-key` authentication, current search filters, smart search, cursors, asset/album metadata, favorite-only updates, persistent replay and unknown outcomes. No real photo library or media is used.

## Public API

[Immich OpenAPI specification](https://github.com/immich-app/immich/blob/main/open-api/immich-openapi-specs.json) and [API documentation](https://immich.app/docs/api/): `POST /api/search/metadata`, `POST /api/search/smart`, `GET/PUT /api/assets/{id}`, and `GET /api/albums`, `GET /api/albums/{id}`.
