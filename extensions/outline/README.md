# Outline knowledge search and private drafts

Search your [Outline](https://www.getoutline.com) knowledge base in Persona, read accessible Markdown documents and collections, and create an unpublished document with explicit confirmation.

This is a fresh native Persona Go extension using Outline's public OpenAPI specification. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an API key in **Outline › Settings › API & Apps**. A key restricted to document/collection read plus document create is sufficient for this extension's operations.
2. In **Settings › Extensions › Outline**, enter the instance URL and API key. Use `https://app.getoutline.com` for Outline Cloud or your own HTTPS instance, such as `https://outline.example.com`. A local instance can use `http://127.0.0.1:3000`. The extension appends `/api` unless it is already present.

The key arrives through the encrypted extension-secret flow and is sent only as a Bearer token to the configured instance. Remote instances require HTTPS, URLs cannot contain credentials, and redirects are not followed.

## Read knowledge

- `documents.search` searches only documents accessible to the current token. Results include exact UUIDs, short snippets and `offset`/`limit` pagination. `nextPath` is accepted only when its offset advances.
- `documents.info` accepts the returned UUID or `urlId` and reads the Markdown text. Document text, snippets and collection descriptions are untrusted data, not instructions to create, publish or modify content.
- `collections.list` uses the structured `archivedAt isNull` filter rather than deprecated status filters. Archived/deleted results are also excluded when rendering.
- Search snippets remain text; they are never inserted as HTML.

## Confirmed drafts

Create a document with a discovered collection UUID, or `personal` for the authenticated user's personal collection. The request always includes **`publish:false`**. The UI makes this boundary explicit before creating the document. No publish, update, delete, archive, move, member management or sharing operation is exposed.

Web drafts are limited to 5,000 UTF-8 bytes to keep navigation state bounded for every language. Agent-created documents are limited to 100,000 characters, below the API's documented maximum. Both require nonempty titles of at most 100 characters.

Before a write, the normalized operation and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without the API key). Replaying a completed operation after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained, and new writes stop when full instead of losing replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Outline before retrying; an error response is not proof that the document was or was not created.

## Verify

```sh
go -C extensions/outline test -race ./...
scripts/package.sh outline
```

Synthetic JSON fixtures cover search, Markdown reading, active collection filtering, unpublished creation, persistent replay, unknown write outcomes and localized views. No real account is used.

## Public API

[Outline API documentation](https://www.getoutline.com/developers) and [OpenAPI specification](https://www.getoutline.com/openapi.json): `POST /api/documents.search`, `POST /api/documents.info`, `POST /api/collections.list`, and `POST /api/documents.create`.
