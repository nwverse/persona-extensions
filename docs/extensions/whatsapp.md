# WhatsApp

[Documentation](../README.md) · [Source](../../extensions/whatsapp/)

Connect your own WhatsApp account to Persona using the native multi-device protocol ([whatsmeow](https://github.com/tulir/whatsmeow)) and pure-Go SQLite (`modernc.org/sqlite`, `CGO_ENABLED=0`). No Node.js, headless browser, or CGO toolchain is required.

| Extension point | Contribution | Behavior |
| --- | --- | --- |
| `connectors` | `whatsapp` | Dual-mode login (QR scan or 8-digit phone pairing code), status, read-only MCP tools, read-only subscriptions catalog, confirmed write actions (`confirm_always`), real-time events, inbox (unread DMs and group @mentions/replies with direct reply), knowledge-base archive, and conversation threads. |
| `assistant` | `whatsapp` skill | The shipped [skill](../../extensions/whatsapp/skills/whatsapp/SKILL.md) explains the MCP tools, JID conventions, on-demand history backfill, and mandatory phone approval rules. |

## Login and encrypted credentials

Sign in through **Settings → Connectors → WhatsApp**. Two linking modes are supported:

1. **8-digit phone pairing code** (recommended on a single phone): enter your phone number with international country code (for example `+12025550123`). Persona displays an 8-character code (`XXXX-XXXX`) to enter in **WhatsApp → Settings → Linked Devices → Link a Device → Link with phone number instead**.
2. **QR code**: enter `qr` to display a scannable WhatsApp QR code (refreshed automatically if it expires) for **WhatsApp → Settings → Linked Devices → Link a Device**.

All WhatsApp device state and Signal protocol keys (identity keys, prekeys, sessions, sender keys, and app-state sync keys) run in an in-memory SQLite database inside the extension and are serialized, gzip-compressed, and persisted exclusively in the hub's encrypted `connector_secrets` store under connector id `whatsapp`. No plaintext credentials or keys are written to the extension directory or disk. Restarting or upgrading the extension preserves the login session; signing out unlinks the companion device (when reachable) and deletes both the encrypted hub secret and the local search index.

## Real-time sync, quiet presence and local index

- **Quiet presence**: enabled by default (`quiet_presence: true`). After connecting, the connector reports `unavailable` presence so contacts do not see the account online 24/7.
- **Local index and full-text search**: synced chats, messages, contacts, and LID/phone JID mappings are stored in a local pure-Go SQLite database (`whatsapp-index.db`) in the extension's data directory with SQLite FTS5 indexing and CJK substring search fallback.
- **Storage cap protection**: `max_storage_mb` (default `128`, range `16..1024`) bounds local index size by pruning the oldest cached messages while preserving each chat's latest summary, encrypted credentials, and content already archived to the knowledge base.
- **On-demand history backfill**: `whatsapp_history_backfill` sends a WhatsApp multi-device peer history sync request to your primary phone for older messages in a chosen chat and indexes the returned messages locally without messaging any contacts.

## Inbox triage and direct replies

The [manifest](../../extensions/whatsapp/persona-extension.json) declares `kinds: ["dm", "mention"]`, `fallback`, and `triage_rules`:

- **Direct messages (`dm`)**: unread private chats where the other participant wrote last; items close automatically when read on another device or replied to.
- **Group mentions (`mention`)**: group chats with unread `@` mentions or replies to your messages; items close when read. Muted and archived chats stay out of the inbox while remaining eligible for knowledge-base archiving.
- **Triage rules and fallback**: when `triage_rules` is empty, items default to requiring your attention (`action`). When set, the hub's triage model applies your rules, with `extra.rule_tier: action` as fallback.
- **Direct replies**: Inbox replies and in-thread replies send directly as your confirmed action, deduplicated by `client_id`.

## MCP tools and mandatory write approval

Read-only tools (available directly to the assistant and through the `catalog` capability for source subscriptions):

| Tool | Purpose |
| --- | --- |
| `whatsapp_list_chats` | List synced private chats and groups (newest first) with canonical JID, unread/mention counts, mute/archive flags, and last message preview. |
| `whatsapp_read_messages` | Paginate one chat's messages oldest-first (`before`, `since`, `until`); never marks messages read. |
| `whatsapp_search_messages` | Full-text search across all chats or one chat in the local SQLite index. |
| `whatsapp_get_message` | Fetch one message by chat and message id. |
| `whatsapp_list_contacts` | List or filter synced contacts with normalized phone JIDs, mapped LIDs, push names, and saved names. |
| `whatsapp_history_backfill` | Request older history for one chat from the user's primary phone into the local index only. |

Write actions (all listed in `confirm_always`, so every assistant invocation shows a phone confirmation card with recipient, visibility, and full content):

| Action | Purpose |
| --- | --- |
| `whatsapp_send` | Send a text message (or quote-reply via `reply_to`) to an explicit canonical JID. |
| `whatsapp_react` | Send or remove (`emoji: ""`) an emoji reaction on a message. |
| `whatsapp_mark_read` | Send read receipts for a chat or specific message ids (visible to the sender). |

## Network and proxy

The manifest declares egress allowlist entries for `web.whatsapp.com`, `*.whatsapp.net`, and `*.whatsapp.com`. When the hub passes proxy environment variables (`HTTPS_PROXY`, `ALL_PROXY`, `HTTP_PROXY`, `NO_PROXY`), both the WhatsApp Web websocket (`http://`, `https://`, `socks5://`, `socks5h://`) and profile picture fetches honor them.

## Tests and protocol

From the repository root:

```sh
go -C extensions/whatsapp vet ./...
go -C extensions/whatsapp test ./...
```

Unit tests use an in-memory SQLite store, synthetic fixtures, and a fake backend so no real WhatsApp credentials or network connections are required. [plugin_test.go](../../extensions/whatsapp/plugin_test.go) checks manifest parity, `confirm_always` declarations, inbox settings rules, and SDK `exttest` process protocol conformance.
