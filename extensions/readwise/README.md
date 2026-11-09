# Readwise reading

Continue your [Readwise Reader](https://readwise.io) reading workflow in Persona: choose
something from the queue, read sanitized article text and associated highlights, and keep
reading progress and notes where they belong.

This is a fresh Persona Go extension using the public Reader v3 API. It ships no Raycast
source, assets, branding or runtime.

## Set up

1. Get your [Readwise access token](https://readwise.io/access_token).
2. In **Settings › Extensions › Readwise**, paste it into **Readwise access token**.
   Do not put the token in chat or tool arguments.

The token arrives through the encrypted extension-secret flow and is used only in the
`Authorization: Token` header to `readwise.io`. Redirects are not followed. The extension
never asks for the temporary Amazon S3 source URLs and never forwards the token to article
or media hosts.

## Read

- Filter **New**, **Later**, **Shortlist**, **Archive** or **Feed**. Search filters the
  loaded pages locally by title, author, site and note; it does not pretend to be a global
  server-side search.
- Load up to four queue pages, keeping previous documents visible. Repeated cursors stop
  pagination rather than causing an endless request loop.
- Read article HTML only when opening a document, bounded to 8 MiB before decoding.
  Scripts, styles, navigation, forms, frames and other non-reading markup are removed;
  article text is not an instruction to invoke tools.
- See associated highlights (`content` and notes), reading progress and the original link.
  Empty article text is reported honestly instead of substituting a summary.

The assistant has `readwise_documents` and `readwise_read` for the same workflows. Document
IDs are opaque strings, never converted to numbers.

## Confirmed changes

- `readwise_save`: save an absolute HTTP(S) URL. Reader may fetch the original page using
  its own service, not this token.
- `readwise_update`: update only explicitly supplied `location`, `reading_progress` (0–1),
  or `notes`. Omitted fields remain unchanged. An explicitly empty note clears the note.
  Reader does not support progress updates for PDFs.

The page also offers save-with-review and **Move to Shortlist**, with the document title in
the confirmation. No deletion, bulk import, automatic cloud mirror or automatic progress
change is included.

Before a write, the extension stores the normalized action and request ID in
`write-receipts.json` in its own data directory (`0600`, without the token). Replays after
restart return the original result; a request ID with different arguments is refused. Up to
256 receipts are retained. When full, new writes stop rather than losing replay protection.

A timeout or interrupted response leaves the outcome **unknown**. Inspect Reader before
retrying; a failed response is not proof that no remote change occurred.

## Verify

```sh
go -C extensions/readwise test -race ./...
scripts/package.sh readwise
```

Tests cover SDK conformance, queue and highlight filtering, HTML sanitization, on-demand
article loading, partial updates, URL validation, localized views and persistent replay.
All fixtures are synthetic; no real account content is needed.

## Public API

[Readwise Reader API](https://readwise.io/reader_api): `/api/v3/list/`, `/api/v3/save/` and
`/api/v3/update/<document_id>/`.
