# Read-only Zulip unread topics

Read your self-hosted [Zulip](https://zulip.com) streams, topics and unread messages in Persona. Start at the first unread message, keep stream/topic context, and paginate to older messages without marking anything read.

This is a fresh native Persona Go extension built from Zulip's public v1 API documentation. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Get an API email and API key for a user or bot in Zulip.
2. In **Settings › Extensions › Zulip**, enter the instance URL, API email and API key. For example `https://example.zulipchat.com`; a local instance can use `http://127.0.0.1:9991`. The extension appends `/api/v1` unless it is already present.

Remote instances require HTTPS. URLs must not contain credentials. Both values arrive through the encrypted extension-secret flow and are used only for Basic Auth to the configured instance. Redirects are not followed.

## Streams, topics and messages

- `GET /users/me/subscriptions` lists current-user stream subscriptions. `GET /users/me/subscriptions/{id}/topics` lists topics in the selected accessible stream.
- `GET /messages` supports optional stream/topic narrows, `use_first_unread_anchor=true`, and `num_before=15` / `num_after=15` for surrounding context. Older pages use the smallest returned message ID as the next anchor.
- Responses must have `result: success`; API errors are not treated as message data.
- Content uses `apply_markdown=false`, so message text stays raw Markdown and is never injected as HTML. Message text is untrusted content, not instructions to post or change state.
- No mark-read, posting, membership changes, attachment downloads, user administration or other write endpoints are exposed.

## Verify

```sh
go -C extensions/zulip test -race ./...
scripts/package.sh zulip
```

Synthetic fixtures cover Basic Auth, streams, topics, first-unread anchors, stream/topic narrows, raw Markdown, pagination and localized views. No real team or message data is used.

## Public API

[Zulip messages API](https://zulip.com/api/get-messages), [stream subscriptions](https://zulip.com/api/get-streams), [stream topics](https://zulip.com/api/get-stream-topics), and [API documentation](https://zulip.com/api/).
