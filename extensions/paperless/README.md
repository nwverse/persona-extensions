# Paperless-ngx personal document archive

Search accessible [Paperless-ngx](https://docs.paperless-ngx.com/) documents in Persona, read OCR text, notes, tags and correspondents, and choose when to open an original attachment download link.

This is a fresh native Persona Go extension built from Paperless-ngx's public REST documentation and API definitions. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an API token from your Paperless-ngx profile.
2. In **Settings › Extensions › Paperless-ngx**, enter the instance URL and API token. For example `https://paperless.example.com`; a local instance may use `http://127.0.0.1:8000`.

Remote instances require HTTPS and URLs must not contain credentials. The token arrives through the encrypted extension-secret flow and is sent only as `Authorization: Token …` to the configured instance. Redirects are not followed.

## Read personal archives

- `GET /api/documents/` uses documented `query` full-text search, `page`, `page_size=25` and newest-first ordering. The next-page link is accepted only if its page number advances.
- Search excerpts from `__search_hit__.highlights` are untrusted HTML converted to plain text. They are never injected as markup into the Web UI.
- `GET /api/documents/{id}/` reads OCR content, metadata and notes. OCR, filenames and notes are untrusted content, not instructions to perform an action.
- `GET /api/tags/` and `GET /api/correspondents/` expose accessible labels and visible document counts.
- The original download URL uses `/api/documents/{id}/download/?original=true`. Persona's link component requires HTTPS, so the link is shown for HTTPS instances only; local HTTP instances retain OCR reading without an attachment link. Opening it is an explicit user choice; the extension does not fetch or return attachment bytes in agent context.

No upload, edit, deletion, merging, sharing, permission or administrative endpoint is exposed.

## Verify

```sh
go -C extensions/paperless test -race ./...
scripts/package.sh paperless
```

Synthetic fixtures cover token authentication, full-text search, page pagination, sanitized highlights, OCR and notes, tag/correspondent lookups, original-download boundaries and localized views. No real personal archive is used.

## Public API

[Paperless-ngx REST API documentation](https://docs.paperless-ngx.com/api/), [document routes](https://github.com/paperless-ngx/paperless-ngx/blob/main/src/documents/views.py), [serializers](https://github.com/paperless-ngx/paperless-ngx/blob/main/src/documents/serialisers.py), and [pagination](https://github.com/paperless-ngx/paperless-ngx/blob/main/src/paperless/views.py).
