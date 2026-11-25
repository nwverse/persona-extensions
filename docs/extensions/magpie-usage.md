# Magpie Subscription Usage

This extension shows Claude Max and Codex subscription windows from Magpie in the Persona usage sidebar, including the separate Claude Fable weekly window.

## Install and sync

1. Use a Persona hub and manager with [direct usage sources](https://github.com/nwverse/persona/pull/15509). The extension requires hub build `202611251338` or later.
2. Open **Settings → Extensions → Extension markets**, add the [official Persona extension market](../installation.md#register-the-public-market), and install **Magpie Subscription Usage**.
3. Open the subscription usage popover below the sidebar. With Magpie running on the same machine as the Persona manager at `http://127.0.0.1:3425`, the accounts appear automatically during the next poll. If local Magpie uses a different port, change **Local Magpie address** in the extension's settings.

The local connection only reads Magpie's quota snapshot and sends no credential. Persona uses its normal sandbox proxy to reach the manager machine's local Magpie. To stop the reads, disable the contribution or the extension.

## Use a remote Magpie

In the extension settings, save **Remote Magpie HTTPS address**, then enter a dedicated **Remote Magpie quota key**. Use a read-only key with permission to inspect the desired quotas. The extension contacts the saved HTTPS origin over its scoped network permission, sends the key only with the quota request, and refuses redirects. If you change origins, Persona clears the old key. Clear the remote address to return to the local source.

Only window percentages, plan labels, sampling and reset times, and opaque per-account aliases are shown in Persona. Missing reset clocks remain absent. A failed refresh retains the last sample with Persona's normal freshness indicators. Account emails, model keys, estimates and usage ledgers stay out of the sidebar.
