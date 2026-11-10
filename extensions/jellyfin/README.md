# Jellyfin resume watching and favorites

Continue watching your self-hosted [Jellyfin](https://jellyfin.org) library in Persona: see resume items, search movies and episodes, read media metadata and playback progress, and change favorite status with explicit confirmation.

This is a fresh native Persona Go extension built from Jellyfin's stable OpenAPI specification. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an API key in the Jellyfin dashboard's API Keys section.
2. In **Settings › Extensions › Jellyfin**, enter the instance URL and API key. For example `https://jellyfin.example.com`; a local instance can use `http://127.0.0.1:8096`.

Remote instances require HTTPS. URLs must not contain credentials. The key arrives through the encrypted extension-secret flow and is sent only as `X-Emby-Token` to the configured instance. Redirects are not followed.

## Library

- `GET /Users/Me` resolves the current user. Every library or favorite operation uses that exact user ID rather than another account.
- `GET /UserItems/Resume` returns current-user resume items. `GET /Items` supports favorites and bounded `searchTerm` queries with `startIndex`, `limit=30` and `enableUserData=true`.
- Details use `GET /Users/{userId}/Items/{itemId}` and include type, series/season/episode metadata, runtime, playback position and favorite state. Media descriptions are untrusted content, not instructions to play or edit items.
- No playback, media downloads, private file paths, playback-position updates, metadata edits, sharing or user administration are exposed.

## Confirmed favorites

`POST /UserFavoriteItems/{itemId}` marks an item favorite and `DELETE /UserFavoriteItems/{itemId}` unmarks it. No other user data is changed.

Before a write, the normalized operation and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without the API key). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Jellyfin before retrying; an error response is not proof that the favorite update did or did not happen.

## Verify

```sh
go -C extensions/jellyfin test -race ./...
scripts/package.sh jellyfin
```

Synthetic fixtures cover `X-Emby-Token` authentication, current-user resume/favorites/search, bounded pagination, item details, favorite-only writes, persistent replay and unknown outcomes. No real library or media is used.

## Public API

[Jellyfin stable OpenAPI specification](https://api.jellyfin.org/openapi/jellyfin-openapi-stable.json) and [API documentation](https://api.jellyfin.org/): `/Users/Me`, `/UserItems/Resume`, `/Items`, `/Users/{userId}/Items/{itemId}`, and `/UserFavoriteItems/{itemId}`.
