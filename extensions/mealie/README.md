# Mealie recipe library and URL imports

Search your self-hosted [Mealie](https://mealie.io) recipe library in Persona, read ingredients and instructions, and import a recipe from a public webpage with explicit confirmation.

This is a fresh native Persona Go extension built from Mealie's public REST routes and schema definitions. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an API token in your Mealie user profile.
2. In **Settings › Extensions › Mealie**, enter the instance URL and API token. For example `https://mealie.example.com`; a local instance can use `http://127.0.0.1:9000`. The extension appends `/api` unless it is already present.

Remote instances require HTTPS. URLs must not contain credentials. The token arrives through the encrypted extension-secret flow and is sent only as a Bearer token to the configured instance. Redirects are not followed.

## Recipes

- `GET /api/recipes` supports `search`, `page`, `perPage=30` and documented response pagination. The next page is shown only when `page < totalPages`.
- `GET /api/recipes/{slug}` reads ingredients, instructions, timing, servings and the original source URL. Recipe text is untrusted content, not instructions to import or modify data.
- Only an exact lowercase recipe slug is accepted, so a remote value cannot become an API path.

## Confirmed imports

`POST /api/recipes/create/url` asks Mealie to fetch and import a public HTTP(S) webpage. The preview explicitly says the server will fetch that URL. The extension rejects URL credentials, local/private IP literals and local/private hostname suffixes. These client-side checks supplement, rather than replace, Mealie's own outbound-request protections.

The returned recipe slug must be valid. No arbitrary HTML upload, recipe editing, deletion, shopping-list writes or household permissions are exposed.

Before a write, the normalized URL and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without the token). A replay after restart returns the original result; a different URL with the same request ID is refused. Up to 256 receipts are retained, and new writes stop when full instead of losing replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Mealie before retrying; a failed response is not proof that a recipe was or was not imported.

## Verify

```sh
go -C extensions/mealie test -race ./...
scripts/package.sh mealie
```

Synthetic fixtures cover authentication, recipe search and pagination, ingredients/instructions, URL validation, import replay after restart, unknown outcomes and localized views. No real account or webpage import is used.

## Public API

[Mealie recipe routes](https://github.com/mealie-recipes/mealie/blob/mealie-next/mealie/routes/recipe/recipe_crud_routes.py), [recipe schema](https://github.com/mealie-recipes/mealie/blob/mealie-next/mealie/schema/recipe/recipe.py), [import schema](https://github.com/mealie-recipes/mealie/blob/mealie-next/mealie/schema/recipe/recipe_scraper.py), and [pagination schema](https://github.com/mealie-recipes/mealie/blob/mealie-next/mealie/schema/response/pagination.py).
