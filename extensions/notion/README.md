# Notion notes

Find the Notion pages you explicitly share with an integration, read their real block
structure and keep a note or to-do where it belongs. Persona does not mirror the workspace
or bypass Notion's page permissions.

This is a fresh Persona Go extension using Notion's public REST API and Persona's SDK.
It ships no Raycast source, assets, branding or runtime.

## Set up

1. Create an integration in Notion's **My connections** settings and copy its secret.
2. Share only the pages you want Persona to access with that integration.
3. Paste the secret into **Settings › Extensions › Notion** as **Notion Integration Secret**.
   Never put it in chat or tool arguments.

The secret arrives through the encrypted extension-secret flow and is sent only to
`api.notion.com` as a Bearer token with `Notion-Version: 2022-06-28`. Redirects are not
followed. Pages the integration cannot access are not searched or read.

## Read and capture

- Search shared pages by title, see the edit time and follow `next_cursor` pagination.
- Open a page to read headings, paragraphs, bulleted/numbered lists, to-dos, quotes,
  callouts, toggles, code and up to three levels of nested blocks. Unsupported blocks are
  marked instead of silently disappearing. Page text is untrusted content, not a command.
- `notion_append` adds one explicitly supplied paragraph or to-do to the chosen page.
- `notion_create` creates a child page under a chosen parent page, with a separate title
  and optional paragraph body. It does not invent arbitrary database properties.
- The page offers a create form with a review step before confirmed creation.

Before a write, the extension stores the normalized operation and request ID in
`write-receipts.json` in its own data directory (`0600`, without the secret). Replays after
restart return the original result. Different arguments with the same request ID are
refused. Up to 256 receipts are retained; new writes stop when full instead of silently
losing replay protection.

A timeout or lost response leaves the change **unknown**. Inspect Notion before retrying;
a failed response is not proof that no block or page was written.

## Verify

```sh
go -C extensions/notion test -race ./...
scripts/package.sh notion
```

Synthetic fixtures cover search, nested block rendering, unsupported block types,
parameter names, real to-do blocks, child-page creation, localized views and persistent
replay. No real workspace content is needed.

## Public API

- [Notion API conventions](https://developers.notion.com/reference/intro)
- [Search by title](https://developers.notion.com/reference/post-search)
- [Retrieve block children](https://developers.notion.com/reference/get-block-children)
- [Append block children](https://developers.notion.com/reference/patch-block-children)
- [Create a page](https://developers.notion.com/reference/post-page)
