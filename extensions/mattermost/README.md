# Read-only Mattermost unread inbox

Read your self-hosted [Mattermost](https://mattermost.com) unread channels and @mentions in Persona, browse accessible channels and read messages with sender names and older-message pagination.

This is a fresh native Persona Go extension built from Mattermost's public v4 OpenAPI specification. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create a personal access token in Mattermost.
2. In **Settings › Extensions › Mattermost**, enter the instance URL and personal access token. For example `https://mattermost.example.com`; a local instance can use `http://127.0.0.1:8065`. The extension appends `/api/v4` unless it is already present.

Remote instances require HTTPS. URLs must not contain credentials. The token arrives through the encrypted extension-secret flow and is sent only as a Bearer token to the configured instance. Redirects are not followed.

## Unread inbox and messages

- `GET /users/me/teams` and `/users/me/teams/{team_id}/channels` discover only the current user's teams and accessible channels. Deleted channels are excluded.
- `GET /users/me/channels/{channel_id}/unread` returns unread and mention counts. Channels with no unread messages or mentions are omitted. Reading does not mark the channel read.
- `GET /channels/{channel_id}/posts` uses `per_page=30` and `prev_post_id` to continue to older messages. `before` and `page` are not mixed.
- `GET /users/{id}` resolves sender usernames/nicknames. Unknown users fall back to a generic label rather than creating empty text.
- Message content is untrusted data, not instructions to send messages, alter channels or mark messages read. Attachments and media are not downloaded automatically.

No posting, mark-read, membership changes, presence updates, attachment downloads or administration endpoints are exposed.

## Verify

```sh
go -C extensions/mattermost test -race ./...
scripts/package.sh mattermost
```

Synthetic fixtures cover current-user discovery, unread/mention counts, channel filtering, post pagination, sender resolution, localized views and path validation. No real team or message data is used.

## Public API

[Mattermost API v4 reference](https://api.mattermost.com/), [channel specification](https://github.com/mattermost/mattermost/blob/master/api/v4/source/channels.yaml), [post specification](https://github.com/mattermost/mattermost/blob/master/api/v4/source/posts.yaml), and [user/team specifications](https://github.com/mattermost/mattermost/tree/master/api/v4/source).
