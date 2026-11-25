# Magpie Subscription Usage

See the 5-hour and weekly subscription allowance for each Claude or Codex account in Magpie, including Claude's separate weekly Fable window when Magpie supplies it.

## Install and configure

1. Use a Persona hub and manager with the [usage extension point](https://github.com/nwverse/persona/pull/12434). Add the [official extension market](../installation.md#register-the-public-market), choose **Magpie Subscription Usage**, and approve access to launcher-backed requests.
2. Create a dedicated launcher in **Settings → Models** with Magpie as its base URL. Use `http://127.0.0.1:3425/v1` when the manager and Magpie run on the same host. Leave the key field empty for a keyless loopback endpoint.
3. For a protected remote Magpie, use an HTTPS address reachable by the manager. Set the launcher's key to a Magpie **read-only** gateway key. If Magpie restricts the key to particular accounts, enable quota visibility for those accounts. Use this launcher for quota collection. A read-only key cannot run models.
4. Add the following item to `usage.sources` in the Persona manager's `manager.json`. Set `launcher` to the actual launcher ID. Keep any existing usage sources. Open the admin **Usage sources** page to reload the saved configuration.

```json
{
  "id": "src-magpie",
  "name": "Magpie",
  "type": "extension",
  "extension": "magpie-usage",
  "launcher": "magpie-usage"
}
```

Open the **Subscription usage** popover in the sidebar and look for the Magpie account rows. The manager normally polls every five minutes. Check that the extension and source are enabled and that Claude or Codex is visible in the usage bar if the rows are missing.

## Quota readings

Persona's gateway transport runs `GET /v1/magpie/quotas?kept=1` against the selected launcher. Magpie serves its saved readings without asking vendors on every poll. The transport uses the launcher key only after an authentication response. The extension receives no key.

The sidebar shows an alias derived from the account identifier using a per-installation secret. The alias stays stable across polls and restarts. It can change if the gateway address or the upstream account identifier changes, or if the extension's data is deleted. The returned rows carry quota percentages, sample and reset times, recognized plan names, and the supported window names. A window with no percentage or known sample time shows no numerical reading. A window without a reset time carries no reset clock. A saved reading retains its original sample time so Persona can age it, and a failed source poll leaves the previous answer marked out of date.

Persona displays subscription allowances only. This connection does not publish emails, raw account labels, prepaid balances, pricing estimates, or token totals.
