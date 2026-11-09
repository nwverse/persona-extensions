# Asana personal tasks

Work through your [Asana](https://asana.com) tasks in Persona: choose a workspace, review incomplete tasks assigned to you, read dates, projects and notes, create a task, or mark one complete with explicit confirmation.

This is a fresh Persona Go extension using Asana's public REST API and official OpenAPI specification. It ships no Raycast source, assets, branding or runtime.

## Set up

1. Create a personal access token in Asana with the permissions needed for your tasks.
2. In **Settings › Extensions › Asana**, paste the **Asana personal access token**. No workspace ID or URL is needed up front; the page lists workspaces visible to the token.

The token arrives through the encrypted extension-secret flow and is sent only as a Bearer token to the fixed `https://app.asana.com/api/1.0` host. Redirects are not followed. Task content is untrusted text, not instructions to create or complete tasks.

## Tasks

- List visible workspaces, active projects and assigned incomplete tasks using `limit=100`, `offset` and the API's returned `next_page.offset`. Offsets remain opaque; the extension never constructs one.
- Read exact task GIDs, title, notes, due date/time, completion state, assignee, projects and the original Asana link when it is a valid HTTPS `app.asana.com` URL.
- Create a task assigned to the current user (`assignee=me`) with a title, notes, optional `YYYY-MM-DD` due date and optional project. The project is checked to be active and in the selected workspace before creating the task.
- Complete a task with a partial `PUT` containing only `completed: true`. Other fields, project memberships, notes and dates are not overwritten.
- No deletion, reopening, bulk changes, attachments, project/workspace administration or guessed endpoint paths.

Before a write, the extension stores its normalized operation and request ID in `write-receipts.json` in its own data directory (`0600`, without the token). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing that protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Asana before retrying; a failed response is not proof that the task was or was not created or completed.

## Verify

```sh
go -C extensions/asana test -race ./...
scripts/package.sh asana
```

Synthetic fixtures cover authentication, workspace/project/task pagination, opt-in task fields, cross-workspace project rejection, partial completion, localized pages, replay and unknown outcomes. No real account is used.

## Public API

[Asana API](https://developers.asana.com/reference) and [official OpenAPI specification](https://raw.githubusercontent.com/Asana/developer-docs/master/defs/asana_oas.yaml): `GET /api/1.0/workspaces`, `GET /api/1.0/projects`, `GET /api/1.0/projects/{project_gid}`, `GET/POST /api/1.0/tasks`, and `GET/PUT /api/1.0/tasks/{task_gid}`.
