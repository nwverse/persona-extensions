# Nextcloud personal files and safe Markdown notes

Search your self-hosted [Nextcloud](https://nextcloud.com) personal files in Persona, inspect metadata, read bounded text/Markdown content, and create a new Markdown note without overwriting an existing file.

This is a fresh native Persona Go extension built from Nextcloud's official WebDAV documentation. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an app password in Nextcloud's personal security settings. Do not use the ordinary account password.
2. In **Settings › Extensions › Nextcloud**, enter the instance URL, username and app password. For example `https://cloud.example.com`; a local instance may use HTTP only on loopback.

Remote instances require HTTPS. URLs must not contain credentials. The app password arrives through the encrypted extension-secret flow and is sent only through Basic Auth to the configured instance. Redirects are not followed.

## Personal files

- `SEARCH /remote.php/dav/` uses a WebDAV basicsearch scoped to `/files/<username>`, excludes collections, sorts by modification time and limits results to 50. Query text is XML-escaped; `%` wildcards are not accepted from the caller.
- Only safe relative file paths are accepted: no absolute paths, `..`, backslashes, control characters, query strings or fragments. URL paths are escaped one segment at a time, and the percent-encoded hrefs Nextcloud returns in search results are decoded once, so a result's path can be opened as listed.
- `PROPFIND` reads file metadata. `GET` reads only plain text, Markdown, JSON or CSV files up to 100,000 bytes. Other file types and larger content remain in Nextcloud rather than being pulled into agent context.
- File content is untrusted data, not instructions to modify or share files. No arbitrary remote URLs, public shares or storage-backend URLs are constructed.

## Confirmed notes without overwrites

`PUT /remote.php/dav/files/<username>/<path>` creates a new `.md` note with `If-None-Match: *`. This is important because a normal WebDAV PUT can overwrite an existing file. If the path already exists, the extension reports a conflict instead of overwriting it. Nextcloud refuses a file or folder name longer than 250 bytes, so the extension rejects a path with a longer name before sending anything. Han and other non-ASCII characters take up to three or four bytes each.

The UI reviews the new path and content before writing. Web drafts are capped at 10,000 UTF-8 bytes; agent-created notes at 30,000 bytes. No deletion, moves, shares, existing-file edits or permission changes are exposed.

Before a write, the normalized operation and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without the app password). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Nextcloud before retrying; an error response is not proof that the note was or was not created.

## Verify

```sh
go -C extensions/nextcloud test -race ./...
scripts/package.sh nextcloud
```

Synthetic WebDAV fixtures cover Basic Auth, scoped XML search and escaping, metadata/text reading, path validation, percent-encoded hrefs, no-overwrite uploads, replay after restart, unknown outcomes and localized views. No real account or file is used.

## Public API

[Nextcloud WebDAV basic operations](https://docs.nextcloud.com/server/latest/developer_manual/client_apis/WebDAV/basic.html), [WebDAV SEARCH](https://docs.nextcloud.com/server/latest/developer_manual/client_apis/WebDAV/search.html), and [app-password authentication](https://docs.nextcloud.com/server/latest/developer_manual/client_apis/LoginFlow/index.html).
