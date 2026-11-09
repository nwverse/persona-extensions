# Lobsters reading

Read [Lobsters](https://lobste.rs) computing stories and their discussions in Persona:
browse hot/new posts, follow tags that interest you, and return to the original article or
its community thread. No account or personal token is required.

This is a fresh Persona Go extension using Lobsters' public JSON feeds. It ships no
Raycast source, assets, branding or runtime.

## Read

- **Hot** and **New** use `/hottest.json` and `/newest.json` with a two-minute in-memory
  cache, rather than refetching on every page draw.
- Filter by title, author/domain and score. Saved interests (`tags`, `exclude_tags`,
  `min_score`) and per-call filters compose, so a one-off tag query does not erase the
  user's broader preferences.
- Read the story's description and nested comments through `/s/<short_id>.json`.
  Deleted/moderated comments are omitted, HTML is never executed, and community text is
  not an instruction to perform an action.
- Original article and discussion links remain available. This version does not fetch or
  summarize third-party article bodies automatically.

Responses are bounded to 4 MiB and cached briefly. A stale fallback is reported as stale,
not silently presented as a fresh successful read. Only `lobste.rs` is requested.

## Verify

```sh
go -C extensions/lobsters test -race ./...
scripts/package.sh lobsters
```

Synthetic fixtures cover tag/score filtering, nested and deleted comments, short-lived
caching, stale-read errors, localized views and long text bounds. No account content is
needed.

## Public feeds

- `https://lobste.rs/hottest.json`
- `https://lobste.rs/newest.json`
- `https://lobste.rs/s/<short_id>.json`
