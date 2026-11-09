# OpenProject assigned work packages

Work through your self-hosted [OpenProject](https://www.openproject.org) work packages in Persona: see assigned work packages, read subjects/descriptions/dates/status/type, choose a project and valid type, and create a work package with explicit confirmation.

This is a fresh Persona Go extension using OpenProject's documented HAL+JSON API. It ships no Raycast source, assets, branding or runtime.

## Set up

1. Create an `opapi-...` API token in your OpenProject account page.
2. In **Settings › Extensions › OpenProject**, enter the **instance URL** and **API token**. For example `https://openproject.example.com`; a local instance may use `http://127.0.0.1`. Remote instances require HTTPS and URLs must not contain credentials.

The token arrives through the encrypted extension-secret flow and is sent only as `Authorization: Bearer …` to the configured instance. Redirects are not followed.

## Work packages

- Discover the current user with `/api/v3/users/me`, then filter assigned work packages by the numeric assignee ID using documented `filters`, `pageSize=50`, `offset` and updated-at sorting. Do not assume a special `me` filter exists.
- Read work package IDs, subject, raw description, start/due dates and HAL project/type/status links. Content is untrusted, not instructions to create or modify work.
- Browse active projects and available types. Create with `subject`, `description.raw`, optional `startDate`/`dueDate`, and exact project/type `_links`. The default non-milestone type is shown in the review page; type ID is never assumed to be 1.
- No deletion, arbitrary filters, custom-field updates, attachments, state transitions or project administration.

Before a write, the extension stores its normalized operation and request ID in `write-receipts.json` in its own data directory (`0600`, without the token). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing that protection.

A timeout or lost response leaves the outcome **unknown**. Inspect OpenProject before retrying; a failed response is not proof that the work package was or was not created.

## Verify

```sh
go -C extensions/openproject test -race ./...
scripts/package.sh openproject
```

Synthetic HAL+JSON fixtures cover current-user filtering, pagination, project/type links, creation payload, localized views, replay and a failed final write with an unknown outcome. No real account is used.

## Public API

[OpenProject API introduction](https://www.openproject.org/docs/api/introduction/), [collections](https://www.openproject.org/docs/api/collections/) and [work packages](https://www.openproject.org/docs/api/endpoints/work-packages/): `GET /api/v3/users/me`, `GET/POST /api/v3/work_packages`, `GET /api/v3/work_packages/{id}`, `GET /api/v3/projects`, and `GET /api/v3/types`.
