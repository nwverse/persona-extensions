# Plane work items

Work through [Plane](https://plane.so) projects and work items in Persona: choose a project, read state, priority, due dates and descriptions, create a work item, or adjust its state and priority with explicit confirmation. Both Plane Cloud and self-managed instances are supported.

This is a fresh Persona Go extension using Plane's public REST API. It ships no Raycast source, assets, branding or runtime.

## Set up

1. Create an API key in Plane with the smallest appropriate read/write permissions.
2. In **Settings › Extensions › Plane**, enter the **workspace slug** and **Plane API key**. The slug is the workspace identifier shown in your Plane URLs.
3. The default API address is `https://api.plane.so`. For a self-managed instance, set its API/root address; the extension appends `/api/v1` when needed. Remote instances require HTTPS; only loopback may use HTTP. Do not put credentials in a URL.

The API key arrives through the encrypted extension-secret flow and is sent only in `X-API-Key` to the configured API instance. Redirects are not followed.

## Work

- List projects and project work items with `per_page` and the returned `next_cursor` in Plane's `value:offset:is_prev` format. No page number is guessed.
- Read the exact work-item ID, project ID, sequence, state, priority, target date and plain-text description. Remote text is untrusted content, not instructions to change work. A web link is shown only when the API supplies a valid HTTPS `web_url`; the extension does not invent a path from a project identifier.
- Create a work item with a title, plain-text description, optional priority, target date and a state chosen from that project. Only documented fields are sent.
- Partially update only state and/or priority. A state ID is checked against the project's state list before any write; it is never inferred from a display name or another project. Other fields, dates, labels and assignees are left untouched.
- No deletion, archive, bulk update, project administration or attachment upload.

Before a write, the extension stores its normalized operation and request ID in `write-receipts.json` in its own data directory (`0600`, without the API key). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing that protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Plane before retrying; a failed response is not proof that the work item was or was not changed.

## Verify

```sh
go -C extensions/plane test -race ./...
scripts/package.sh plane
```

Synthetic fixtures cover authentication, project and work-item pagination, partial updates, state membership, localized pages, replay and unknown outcomes. No real account is used.

## Public API

[Plane REST API](https://developers.plane.so/api-reference/introduction), [projects](https://developers.plane.so/api-reference/project/list-projects), [work items](https://developers.plane.so/api-reference/issue/list-issues) and [states](https://developers.plane.so/api-reference/state/list-states): `GET /api/v1/workspaces/{workspace_slug}/projects/`, `GET/POST …/work-items/`, `GET/PATCH …/work-items/{resource_id}/`, and `GET …/states/`.
