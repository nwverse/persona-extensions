# 1Password unattended authorization research

Checked 2026-11-22 against official 1Password documentation. No user account information, credential files or stored
items were read for this research. This report does not configure either alternative.

## Desktop authorization

Reusing the resident extension node avoids creating a new CLI caller for each Agent shell command. It does not create
a permanent grant: authorization expires after 10 minutes without CLI use, at 12 hours regardless of activity, and
when the 1Password App locks. The App's lock revokes every existing authorization. A process already running at
revocation can finish. These limits are the upstream security contract, not a Persona setting.

A five-minute `op whoami` refresh can extend an active idle session. A 1Password employee explicitly confirms that a
successful `whoami` extends biometric authorization by another ten minutes. It cannot avoid the twelve-hour ceiling,
App locking, restarts or authorization changes. Persona discards identity output and stops repeated refresh attempts
on failure; it never authorizes a desktop prompt itself. A session can have been authorized before the node's first
observed successful operation, so its true ceiling may arrive sooner than the node's local estimate.

On macOS, look in **1Password › Settings › Security › Auto-lock**, including **Lock after the device is idle for**.
Sleep/screensaver/user-switch preferences and organization policy can impose additional locking. CLI activity does
not count as keyboard/mouse activity for the App's idle-lock timer. This implementation does not modify those settings.

Sources: [CLI authorization security](https://developer.1password.com/docs/cli/app-integration-security/),
[1Password employee on whoami refresh](https://www.1password.community/developers-69/how-to-check-if-a-session-is-active-feature-request-11367),
[App auto-lock settings](https://support.1password.com/auto-lock/).

## Alternatives that do not require desktop approval per operation

| Choice | Desktop approval during normal operations | Account/access requirement | User setup | Persona support today |
| --- | --- | --- | --- | --- |
| Service account | None: authenticate with a scoped token, independent of desktop unlock | Current official rate-limit tables include Individual, Families, Teams and Business; adequate permissions to create service accounts | Create a dedicated non-default vault, create a service account, grant only required vault permissions, save the one-time token securely in Persona's extension settings | Already supports service-account metadata reads and secret resolution. This release's encrypted writes intentionally require the resident desktop node; service-account write support is not implemented. |
| 1Password Connect | None: clients authenticate to a self-hosted API using access tokens | Official comparison says included with a 1Password subscription; creation requires permission to manage Secrets Automation and an eligible vault | Create a Connect workflow, protect its generated credentials file and access token, operate the API and sync containers (Docker/Kubernetes), configure TLS/network access and recovery | No Connect mode exists in this extension; adopting it requires a separate integration. |

These remove desktop biometric prompts, not all operational failure: token expiry/revocation, vault permissions,
service-account request limits, network faults and Connect availability still apply. Keep Persona's explicit phone
approval and encrypted value path if either write backend is implemented later; a background token is not permission
for an Agent to perform arbitrary writes.

Service-account vault grants cannot include built-in Personal/Private/Employee vaults or the default Shared vault.
The Connect setup guide lists the same vault exclusions. Use a dedicated vault for automation rather than granting
access to unrelated personal entries. Service-account vault/permission grants are immutable: changed grants require a
new account/token. The official API supports item creation and editing with `read_items` plus `write_items`.

Sources: [Service-account setup and limitations](https://www.1password.dev/service-accounts/get-started),
[Account types in official rate limits](https://www.1password.dev/service-accounts/rate-limits),
[Official comparison](https://www.1password.dev/secrets-automation),
[Connect prerequisites and setup](https://www.1password.dev/connect/get-started).

## How the user can check eligibility without sharing account data

The user can sign in to their own 1Password.com account and open **Developer › Directory › Other** under
**Infrastructure Secrets Management**. Current documentation places **Create a Service Account** and **Create a
Connect server** there; created entries appear under Developer's Service accounts / Connect servers. The creation
wizard and permission controls establish actual availability for that account. If the service-account option is
missing, the official guide says to ask the account administrator for permission to create/manage service accounts.

The account's plan can be checked by the user in its billing/account controls if needed; Persona does not need to
query account identity or print `op whoami` output to infer a plan. Do not assume older claims that service accounts
are limited to Business/Teams: the current official limit table explicitly includes personal/family plans.
