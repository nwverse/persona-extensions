# X

[Documentation](../README.md) · [Source](../../extensions/x/)

This extension reads your own X (formerly Twitter) account through the official X API v2 (`api.x.com`), with keys of a developer app you own. It brings your mentions into the inbox, keeps a few days of your home timeline (or chosen lists) within a daily budget, and turns them into an organized digest on an X page and for the assistant. It only reads: it never posts, likes, follows or reads direct messages.

| Extension point | Contribution | Behavior |
| --- | --- | --- |
| `connectors` | `x`, capabilities `status`, `tools`, `inbox`, `login`, `fallback` | Sign-in, the inbox (mentions), and the read tools `x_digest`, `x_feed`, `x_following`, `x_post`. |
| `ui` | `x` (`nav_page`) | The X page: today's or the last three days' digest, not a timeline. |
| `assistant` | `x` (skill `skills/x`) | Tells the assistant how to write a daily X digest with the tools, and the rules below. |

## Cost and X's terms

X sells API access as pay-per-use credits, billed to the developer app; there is no free read tier. The extension runs on your own app, so your credits pay for what it reads:

- **Home timeline and lists**: X bills about $0.005 per post and $0.010 per user returned. The extension counts every post and every user in each answer (`data`, `includes.tweets`, `includes.users`) against `daily_post_budget` per UTC day. Before each request it sizes the page so that its worst case (6 resources a post: the post, two posts it references, three authors) fits what is left, and holds that worst case until the answer is counted, so the day's count never passes the budget, even with `x_post` running at the same time. When it is spent the feed stops until 00:00 UTC. The default of 200 costs about $1–2 a day (about $30–60 a month), depending on how many of the 200 are users, before X's same-day deduplication. Reading a few lists instead of the home timeline costs less.
- **Mentions, your own posts**: X's "Owned Reads" (the app owner reading their own data, $0.001 per resource), not counted against the budget. Mentions are read at most 300 per sync (three pages of 100); a larger backlog comes in on the next syncs.
- **Who you follow**: read whole on sign-in and then once a week (up to 20,000 accounts), not counted against the budget. At Owned Reads prices each refresh costs about $1 per 1,000 accounts you follow (5,000 follows: about $5 a week, about $20 a month). It is not confirmed that Owned Reads apply to OAuth 1.0a requests; at the $0.010 Following: Read price it would be about $10 per 1,000 accounts per refresh. Check the usage in the X console after the first day, and set a spending limit there.
- **`x_post`**: a live read of one post, counted against the budget (and saved at once, so a restart does not forget it), refused when less than one post's worst case is left.

X's developer terms shape what the extension does:

- **Official API only.** No scraping of x.com, no browser cookies, no unofficial GraphQL endpoints, not even as a fallback.
- **Short retention.** X requires stored copies of posts to be deleted when they are deleted on X. The hub's knowledge base cannot delete a post, so posts never go there: they stay in the extension's own data directory for `retention_days` (1 to 7, default 3), mentions for 8 days, and everything is deleted when you sign out.
- **No training, no profiling.** X forbids using its content to train models and inferring sensitive traits (health, politics, religion, ethnicity, sexuality, finances, union membership, crimes) about people. The assistant skill and every tool result say so, and mark post text as untrusted third-party content.
- **Your own app.** X's policy discourages tools that require end users to register developer apps. Persona is self-hosted: you are the developer and the only user of your app, and the extension is a personal reader. Describe your app's use case accordingly in the console. X has not confirmed this reading.

## Setup

1. Sign in at [console.x.com](https://console.x.com) and create a project and an app.
2. Buy pay-per-use credits for the account (and set a spending limit if you like).
3. In the app's settings, set the app permission to **Read**.
4. Under **Keys and tokens**, copy the **API Key** and **API Key Secret**, then generate the **Access Token** and **Access Token Secret** for your own account.
5. In Persona, open **Settings → Connectors → X**, sign in and paste the four values. The extension checks them with `GET /2/users/me` and shows your `@username`.

The keys are sealed in the hub's connector secret. They never appear in logs, statuses, errors, tool results or the page. Signing out deletes the secret and everything the extension stored.

Requests are signed with OAuth 1.0a (HMAC-SHA1, user context), implemented in the extension and tested against the RFC 5849 / OAuth Core 1.0 signature vectors.

## What it reads

One loop, every 15 minutes (sooner right after sign-in or a refresh):

- **Following**: `GET /2/users/:id/following`, 1,000 a page (at most 20 pages), on sign-in and then once a week.
- **Feed** (`feed_source`):
  - `home`: `GET /2/users/:id/timelines/reverse_chronological` with `since_id` (the cursor is kept), up to 100 a page as the budget allows. The first read reaches back `retention_days`. A read that stops halfway (an error, a `429`, the sync's time limit) keeps the cursor and remembers the oldest post it read; the next sync first reads the gap below it (`until_id`), then what is newer. Only running out of budget skips older posts for good.
  - `lists`: `GET /2/lists/:id/tweets` for each of `list_ids`, stopping at the first post already kept or older than `retention_days` (lists have no `since_id`).
  - `off`: no feed; mentions only. A budget of 0 also turns the feed off.

  A long post's full text comes from `note_tweet`.
- **Mentions**: `GET /2/users/:id/mentions` with `since_id` and the same gap handling as the home timeline, three pages of 100 a sync; the first read reaches back 8 days.
- **Your own posts** (`GET /2/users/:id/tweets`), only while a mention is still open, to tell whether you answered it.

Rate limits: the extension keeps `x-rate-limit-remaining` and `x-rate-limit-reset` per endpoint and does not ask an endpoint whose window is used up. A `429` sets the connector's status to `rate_limited` until the reset (waits are capped at 15 minutes). Every request has a 30-second timeout and every sync a 3-minute one; failures back off from one minute to 15.

Status: `not_signed_in`; `ok` with `@username`; `rate_limited`; `error` with a guide when X refuses the keys (`401`: regenerate the access token and sign in again) or the account (`402` / `403`: add credits, or check the app is enrolled for the X API).

## The inbox

One item per mention (a post that mentions you or replies to you), kind `mention`:

- the thread is the post's conversation, the sender `@username`, the link the post's permalink (`https://x.com/<username>/status/<id>`);
- the connector's rule tier is "needs you" (`action`), also stamped as the fallback tier (`extra.rule_tier`), so items never wait for a triage model; your `triage_rules` are carried on every item;
- an item is done once you posted in that conversation after the mention;
- mentions older than 8 days are not collected.

Replying from the inbox is not supported (the extension does not write to X). Open the post on X to answer.

## The page and the digest

The X page is a digest, not a timeline. Its header shows the account, posts read today against the budget, the last sync and the state; a segmented control switches between 24 hours and 3 days (rows on apps before UI version 2), and a refresh button syncs now. Signed out, it shows how to sign in.

The digest is computed the same way every time from what is kept, without a model. Each section shows at most 10:

1. **Top posts**: original posts, reposts collapsed into the original (with who reposted it). Score: likes + 2 × reposts + 2 × quotes + replies, divided by log2(author's followers + 2); ties go to the most recent.
2. **Most shared links**: expanded links from the posts, grouped by a normalized URL (host without `www.`, no fragment or tracking parameters, posts on X itself skipped), ranked by how many accounts shared them.
3. **Threads**: an author's chain of three or more replies to themself in one conversation.
4. **Most active accounts you follow**: posts in the window per followed account.

Plus totals: posts seen and the budget used today. Each post shows "Name @username", its full unaltered text (long posts in pieces, up to about 10,000 characters, with the rest on X), its counts and time, and a link to the post. Page text is localized (zh-Hans, en, ja).

## Assistant tools

All read-only. Every result starts with a note that post text and bios are third-party content.

| Tool | Arguments | What it returns |
| --- | --- | --- |
| `x_digest` | `window`: `24h` (default) or `3d` | The digest as text with permalinks. No X request. |
| `x_feed` | `query`, `author`, `hours` (default 24), `limit` (≤ 50) | Kept posts matching (case-insensitive substrings), newest first. No X request. |
| `x_following` | `query`, `limit` (≤ 100) | Followed accounts: name, `@username`, bio, followers, posts in the last 3 days. No X request. |
| `x_post` | `id` | One post live (`GET /2/tweets/:id`) with the post it reposts, quotes or replies to. Counted against the budget. |

## Configuration

[Manifest and settings schema](../../extensions/x/persona-extension.json)

| Setting | Meaning |
| --- | --- |
| `triage_rules` | Your own rules for sorting X's inbox items; empty uses the inbox's global rules. |
| `feed_source` | `home` (default), `lists` or `off`. |
| `list_ids` | Numeric list ids (from `x.com/i/lists/<id>`), read when `feed_source` is `lists`. |
| `daily_post_budget` | Posts and users read for the feed per UTC day, 0 to 5,000 (default 200). 0 turns the feed off. |
| `retention_days` | Days posts are kept, 1 to 7 (default 3). Mentions are kept 8 days. |
| `proxy` | An `http://`, `https://`, `socks5://` or `socks5h://` proxy (`[user:pass@]host:port`) for every request. A malformed value fails every request rather than going direct. Empty uses the environment's proxy, or goes direct. |

Network access: `api.x.com`, `x.com` and `*.twimg.com`.

## Privacy

- Keys: only in the hub's sealed connector secret and the extension's memory.
- Data: `following.json`, `posts.json`, `mentions.json` and `state.json` in the extension's data directory, each written atomically (a temporary file renamed over it). Nothing goes to the knowledge base. Sign-out deletes the whole directory.
- Nothing is shared beyond your hub; X's content stays scoped to the account that authorized it.

## Limits and not yet

- **Writes**: no posting, replying, liking or reposting.
- **Direct messages**: not read (they need express consent and cost more).
- **Knowledge-base ingestion**: posts are not archived to the knowledge base until the hub can delete a post when X does.
- **Sign-in**: pasting the four keys is the only way today. X's OAuth 1.0a PIN flow and OAuth 2.0 need a sign-in step that shows a link and a field together, or a redirect into the hub, which the apps do not have yet.
- **Deleted posts**: kept posts are not re-checked against X; they simply expire after `retention_days`.
- **Media**: images and videos are not shown; the page links to the post.
- The X brand mark is not shown; the apps use the generic connector icon.

## Tests

From the repository root:

```sh
go test ./extensions/x/...
```

The tests run against a fake X API (`internal/xtest`, synthetic accounts and keys only) and cover the signature vectors, sign-in (success, `401`, `402`), following pagination, home timeline `since_id` resume, resuming after a failed page and the budget's ceiling (also with concurrent `x_post` calls, and across a restart), lists stopping at a known post, mentions as inbox items with their tier and a backlog larger than one sync, `429` handling, error statuses, retention pruning, sign-out wiping the data directory (also with a save in flight), the digest's ranking, links, threads and active accounts, the tools, the page (signed out and in, every UI version and language) against the SDK's and the hub's view checks, the manifest against the program, the SDK's conformance check, and that no key appears in any status, tool result, error or page.
