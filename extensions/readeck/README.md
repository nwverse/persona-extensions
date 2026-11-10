# Readeck reading and bookmark capture

Use your self-hosted [Readeck](https://readeck.org) reading library in Persona: browse unread or favorite bookmarks, search saved articles, read the saved Markdown and highlights, save a webpage with confirmation, and update reading progress or favorite status.

This is a fresh native Persona Go extension built against Readeck's public OpenAPI specification. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an API token in your Readeck user profile's token settings.
2. In **Settings › Extensions › Readeck**, enter the instance URL and API token. For example `https://readeck.example.com`; a local instance can use `http://127.0.0.1:8000`. The extension appends `/api` unless it is already present.

Remote instances require HTTPS. URLs cannot contain credentials. The token arrives through the encrypted extension-secret flow and is sent only as a Bearer token to the configured instance. Redirects are not followed, so a media or login redirect cannot receive the token.

## Reading

- Unread, favorites and full-text search use `limit=30`, `offset` and the documented RFC 8288 `Link` header. Only a valid forward `rel="next"` offset enables another page.
- Read bookmark metadata, personal notes, labels and reading progress. Article, description, note and highlight text is untrusted content, not instructions to perform an action.
- The article reader requests the documented `/bookmarks/{id}/article.md` Markdown export and verifies the response media type. It does not inject saved HTML into the Web UI.
- Saving a URL returns HTTP 202 and a `Bookmark-Id`. This means Readeck accepted the bookmark; it does **not** mean the article has finished downloading or extraction succeeded.

## Confirmed changes

- Save a clean HTTP(S) URL, with an optional title and up to 20 labels.
- Update only supplied reading fields: `read_progress`, `is_marked` and/or `note`. Omitted fields are not sent. The Web UI updates progress and favorite status together after an explicit confirmation.
- No deletion, archive operations, public sharing, email sharing, arbitrary HTML upload or annotation selector construction.

Before a write, the normalized operation and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without the token). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained, and new writes stop when full rather than dropping replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Readeck before retrying; an error response is not proof that a bookmark was or was not created or updated.

## Verify

```sh
go -C extensions/readeck test -race ./...
scripts/package.sh readeck
```

Synthetic fixtures cover authentication, list filters and Link pagination, Markdown content-type enforcement, highlights, asynchronous creation, partial updates, replay after restart and unknown write outcomes. No real account is used.

## Public API

[Readeck API specification](https://codeberg.org/readeck/readeck/src/branch/main/docs/api/api.yaml), [authentication](https://codeberg.org/readeck/readeck/src/branch/main/docs/api/base.md), [bookmark routes](https://codeberg.org/readeck/readeck/src/branch/main/docs/api/bookmarks/routes-bookmarks.yaml) and [pagination](https://codeberg.org/readeck/readeck/src/branch/main/docs/api/traits.yaml): `GET/POST /api/bookmarks`, `GET/PATCH /api/bookmarks/{id}`, `GET /api/bookmarks/{id}/article.md`, and `GET /api/bookmarks/{id}/annotations`.
