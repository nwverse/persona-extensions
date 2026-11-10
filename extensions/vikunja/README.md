# Vikunja personal tasks and project capture

Work through your self-hosted [Vikunja](https://vikunja.io) tasks in Persona: see open tasks assigned to you, search accessible tasks, read descriptions/comments, choose a project to create a task, and confirm completion.

This is a fresh native Persona Go extension built from Vikunja's public API specification and filter reference. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an API token in Vikunja's user settings.
2. In **Settings › Extensions › Vikunja**, enter the instance URL and API token. For example `https://vikunja.example.com`; a local instance can use `http://127.0.0.1:3456`. The extension appends `/api/v1` unless it is already present.

Remote instances require HTTPS. URLs must not contain credentials. The token arrives through the encrypted extension-secret flow and is sent only as a Bearer token to the configured instance. Redirects are not followed.

## Tasks and projects

- `GET /user` discovers the current username. Without a search term, `GET /tasks` uses `done = false && assignees = '<username>'`, `per_page=50`, page pagination and due-date sorting.
- With a search term, use the documented `s` parameter. `filter` and `s` are never combined, because the filter reference does not allow that combination.
- `GET /tasks/{id}?expand=comments` reads descriptions and comments. All task and comment text is untrusted data, not instructions to create or complete work.
- `GET /projects` returns exact project IDs; archived/deleted projects are excluded from task creation.

## Confirmed writes

- Create with `PUT /projects/{id}/tasks`, a title, optional description and optional RFC3339 due date. The Web flow reviews the complete draft before creating it and caps descriptions at 5,000 UTF-8 bytes to preserve navigation state across languages.
- Complete with `POST /tasks/{id}` and only `done:true`. No deletion, assignee changes, arbitrary field updates, project administration or permission changes.

Before a write, the normalized operation and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without the token). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full rather than losing replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Vikunja before retrying; an error response is not proof that a task was or was not created/completed.

## Verify

```sh
go -C extensions/vikunja test -race ./...
scripts/package.sh vikunja
```

Synthetic fixtures cover current-user filtering, search/filter separation, comments, pagination, project creation, done-only updates, replay after restart, unknown outcomes, localized views and UTF-8 draft bounds. No real account is used.

## Public API

[Vikunja Swagger specification](https://github.com/go-vikunja/vikunja/blob/main/pkg/swagger/swagger.json), [filter reference](https://vikunja.io/docs/filters/), and [API documentation](https://vikunja.io/docs/api/): `GET /api/v1/user`, `GET /api/v1/tasks`, `GET/POST /api/v1/tasks/{id}`, `GET /api/v1/projects`, and `PUT /api/v1/projects/{id}/tasks`.
