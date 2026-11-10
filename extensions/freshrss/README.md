# FreshRSS unread reading

Read your self-hosted [FreshRSS](https://freshrss.org) unread and starred articles in Persona, browse feed subscriptions, read sanitized article text, and mark a single article read with explicit confirmation.

This is a fresh native Persona Go extension built from FreshRSS's Google Reader-compatible API documentation and implementation. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Enable API access in FreshRSS and set the dedicated API password. Use that API password, not the ordinary account password.
2. In **Settings › Extensions › FreshRSS**, enter the full API address, username and API password. For example `https://freshrss.example.com/api/greader.php`; a local instance may use HTTP only on loopback.

The username and password arrive through the encrypted extension-secret flow and are used only in a POST body to `/accounts/ClientLogin`. They are never placed in URLs, logs, article output or write receipts. The returned session credential is kept in memory and sent only as `Authorization: GoogleLogin auth=…` to the configured instance. Redirects are not followed.

## Reading

- `stream/contents/reading-list` uses `xt=user/-/state/com.google/read` to keep the unread view unread. `stream/contents/user/-/state/com.google/starred` reads the starred view. Both use `n=30` and documented continuation pagination.
- Article IDs retain the full `tag:google.com,2005:reader/item/<hex>` identifier, including leading zeroes. They are not incorrectly converted to decimal.
- Article HTML is parsed into plain text. Scripts, styles, forms, iframes, embedded objects, SVG and similar active elements are discarded. Remote article text is untrusted content, not instructions.
- Original article links are displayed only for valid HTTPS URLs. Media and enclosures are not automatically downloaded.
- `subscription/list` is read-only: no refresh, subscribe, unsubscribe or feed administration.
- The detail view keeps its current unread/starred view instead of looking up articles in an unrelated collection.

## Confirmed read updates

`edit-tag` adds only `user/-/state/com.google/read` to one exact item ID. The extension first obtains the documented action token and then submits the confirmed change. No bulk mark-read, starred changes, label edits or subscription changes are exposed.

Before a write, the normalized operation and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without API credentials). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect FreshRSS before retrying; an error response is not proof that the article was or was not marked read.

## Verify

```sh
go -C extensions/freshrss test -race ./...
scripts/package.sh freshrss
```

Synthetic fixtures cover POST authentication, session reuse, unread/starred streams, continuation pagination, HTML sanitization, subscriptions, unread detail lookup, single-item read updates, replay and unknown outcomes. No real account or article is used.

## Public API

[FreshRSS Google Reader-compatible API documentation](https://freshrss.github.io/FreshRSS/en/developers/06_GoogleReader_API.html), [API implementation](https://github.com/FreshRSS/FreshRSS/blob/edge/p/api/greader.php), and [article response model](https://github.com/FreshRSS/FreshRSS/blob/edge/app/Models/Entry.php).
