# DEV Community reading

Read public programming articles from [DEV Community](https://dev.to) in Persona:
follow a tag, choose an article, read its body and return to the discussion. No personal
API token or publishing permission is required.

This is a fresh Persona Go extension using DEV's public article endpoints. It ships no
Raycast source, assets, branding or runtime.

## Read

- Filter by tag, title/author query and minimum reaction count. Saved interests compose
  with the current filter instead of being silently replaced.
- See reading time, author, tags, reaction/comment counts and the original URL.
- Read `body_markdown` and nested comments. Comment HTML is reduced to text; scripts,
  styles, forms and frames are excluded. Article text is never an instruction to act.
- Follow up to six levels of comments with bounded output. Original links remain available;
  this version does not publish, react, save or fetch a private reading list.

Responses are bounded to 4 MiB and cached briefly. Stale fallback data is reported as stale,
not presented as fresh. Only `dev.to` is requested.

## Verify

```sh
go -C extensions/devto test -race ./...
scripts/package.sh devto
```

Synthetic fixtures cover tag/reaction filtering, body/comment rendering, nested threads,
cache behavior, localized views and long text bounds. No personal account is needed.

## Public endpoints

- `GET /api/articles` with `tag`, `page` and `per_page`
- `GET /api/articles/<id>`
- `GET /api/comments?a_id=<id>`
