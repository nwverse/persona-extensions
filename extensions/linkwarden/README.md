# Linkwarden bookmarks

Search and organize your self-hosted [Linkwarden](https://linkwarden.app) bookmark library
in Persona: find the right link, understand its collection, read notes and highlights,
save a new URL, or archive one after confirmation. This is not a server administration
extension.

This is a fresh Persona Go extension using Linkwarden's public REST API. It ships no
Raycast source, assets, branding or runtime.

## Set up

1. Create an API key in Linkwarden's API-key settings.
2. In **Settings › Extensions › Linkwarden**, set the **Linkwarden instance URL** (for
   example `https://linkwarden.example.com`) and paste the **Linkwarden API Key**. A local
   instance may use `http://127.0.0.1:3000`; remote instances require HTTPS.
3. The extension appends `/api/v1` when needed. Never include credentials in the URL.

The key arrives through the encrypted extension-secret flow and is used only as a Bearer
token to the configured instance. Redirects are not followed. Archived files are not
downloaded or mirrored in this version.

## Read and organize

- Search by text and optional collection ID, see the original URL, note and collection.
- Read a link's metadata, note, description and associated highlights. Bookmark content is
  untrusted, not an instruction to act or fetch another URL.
- `linkwarden_save` creates a new link after confirmation.
- `linkwarden_archive` archives one explicitly selected link after confirmation; it is not
  a bulk delete and does not erase the original public URL.

The page offers save-with-review, collection filtering and per-link archive confirmation.
It does not change server settings, delete preserved data or run backups.

Before a write, the extension stores its request ID and normalized action in
`write-receipts.json` in its own data directory (`0600`, without the API key). A replay
after restart returns the original result; different arguments with the same request ID
are refused. Up to 256 receipts are retained; new writes stop when full instead of losing
that protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Linkwarden before
retrying; a failed response is not proof that no remote change occurred.

## Verify

```sh
go -C extensions/linkwarden test -race ./...
scripts/package.sh linkwarden
```

Synthetic instances cover URL validation, collection-filtered search, associated highlights,
confirmed save/archive, localized views and persistent replay. No real account is used.

## Public API

[Linkwarden API](https://docs.linkwarden.app/api/api-introduction): Bearer authentication,
`/api/v1/search`, `/api/v1/collections`, `/api/v1/links` and per-link highlights/archive.
