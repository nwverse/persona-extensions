# Kimai personal time tracking

Read your current user's recent and running [Kimai](https://www.kimai.org) time entries in Persona, choose visible projects and activities, create a time entry or start a timer, and stop a running timer with explicit confirmation.

This is a fresh native Persona Go extension built from Kimai's public REST documentation and controller/form definitions. It contains no Raycast source, assets, branding or runtime.

## Set up

1. Create an API token in your Kimai user profile.
2. In **Settings › Extensions › Kimai**, enter the instance URL and API token. For example `https://kimai.example.com`; a local instance can use `http://127.0.0.1:8001`. The extension appends `/api` unless it is already present.

Remote instances require HTTPS. URLs must not contain credentials. The token arrives through the encrypted extension-secret flow and is sent only as a Bearer token to the configured instance. Redirects are not followed.

## Time entries

- `GET /timesheets/recent`, `/timesheets/active` and `/timesheets` use the current user by default; no `user=all` or other-user filter is exposed.
- Visible projects use `GET /projects?visible=1&ignoreDates=1`; activities use `GET /activities?visible=1&project={id}` with the selected project ID.
- Descriptions and project/activity names are untrusted content, not instructions to create, stop or edit time entries.

## Confirmed writes

- `POST /timesheets` creates an entry for the current user with discovered project/activity IDs, an explicit RFC3339 begin timestamp, optional later end timestamp and description. Leaving end empty starts a running timer; the review makes that distinction explicit.
- `PATCH /timesheets/{id}/stop` stops the selected running timer. The UI binds the selected timesheet ID to the confirmed draft before stopping it.
- No deletion, arbitrary time editing, rate/billable changes, other-user administration or permission updates are exposed.

Before a write, the normalized operation and request ID are stored in `write-receipts.json` in the extension data directory (`0600`, without the token). A replay after restart returns the original result; different arguments with the same request ID are refused. Up to 256 receipts are retained; new writes stop when full instead of losing replay protection.

A timeout or lost response leaves the outcome **unknown**. Inspect Kimai before retrying; an error response is not proof that the time entry was or was not created/stopped.

## Verify

```sh
go -C extensions/kimai test -race ./...
scripts/package.sh kimai
```

Synthetic fixtures cover current-user reads, visible project/activity discovery, timestamp validation, time-entry creation, timer ID binding, replay after restart and unknown outcomes. No real account or time entry is used.

## Public API

[Kimai REST documentation](https://www.kimai.org/documentation/rest-api.html), [timesheet controller](https://github.com/kimai/kimai/blob/main/src/API/TimesheetController.php), [project controller](https://github.com/kimai/kimai/blob/main/src/API/ProjectController.php), [activity controller](https://github.com/kimai/kimai/blob/main/src/API/ActivityController.php), and [API time-entry form](https://github.com/kimai/kimai/blob/main/src/Form/API/TimesheetApiEditForm.php).
