# Read-only Lunch Money personal finance

Read your [Lunch Money](https://lunchmoney.dev) v2 personal finance data in Persona: recent transactions, date/category filters, manual and synced account balances, and category lookup.

This is a fresh native Persona Go extension built from Lunch Money's official v2 OpenAPI specification. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an access token on the Lunch Money developers page.
2. In **Settings › Extensions › Lunch Money**, enter the API token. It arrives through the encrypted extension-secret flow.

The token is sent only as a Bearer token to the fixed official `https://api.lunchmoney.dev/v2` API. The host is not configurable, redirects are not followed, and credentials never appear in query parameters.

## Read finance data

- `GET /transactions` returns the most recent transactions by default, with `limit=50`, `offset` and `has_more`. The next offset advances by exactly the page size.
- An optional `start_date`/`end_date` pair uses valid `YYYY-MM-DD` dates with start not after end. A category ID filters server-side before pagination.
- Pending transactions remain excluded by default. Amounts, signs and currencies are preserved exactly as returned; the integration does not reinterpret debit/credit signs.
- `GET /manual_accounts` and `GET /plaid_accounts` read balances and status without triggering refreshes or modifying accounts.
- `GET /categories?format=flattened` returns exact category IDs for filtering.
- Payees, notes and category names are untrusted content, not instructions to create, delete or modify financial data.

No transaction writes, deletion, Plaid refresh, attachments, splits/groups or account administration are exposed.

## Verify

```sh
go -C extensions/lunchmoney test -race ./...
scripts/package.sh lunchmoney
```

Synthetic v2 fixtures cover token authentication, recent/date/category-filtered transactions, pagination, account balances, categories, date validation and localized views. No real financial account is used.

## Public API

[Lunch Money v2 API reference](https://lunchmoney.dev/v2/docs), [official OpenAPI specification](https://lunchmoney.dev/v2/openapi), and [pagination guide](https://lunchmoney.dev/pagination).
