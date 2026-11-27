# Steam

Steam is a read-only Persona extension for player status, two weeks of playtime, the searchable game library, achievements and wishlist prices. Open **Personal › Steam** for details or use the Overview card and the five `steam_*` assistant tools.

New installations use Steam Mobile QR by default. In **Settings › Extensions › Steam**, select **Sign In**, scan with Steam Guard in the Steam Mobile App, and approve the login on the phone. The Hub encrypts the refresh token in its connector secret store. The extension keeps the access token and QR account cache in memory.

To use the read-only Web API key fallback, select **Web API key** and enter a SteamID64 or profile vanity name and a key. Existing installations with both values use this mode automatically. The optional Store country defaults to `US`. [Setup, permissions and sign-out](../../docs/extensions/steam.md)

## Token settings

These runtime gates and budgets apply to planned Steam features. Each disabled feature uses zero extra LLM tokens. Price and playtime observations use existing Steam reads. QR sessions keep their observations in memory until sign-out or restart. Web API key mode stores its observations in `steam-store-v1` under the extension data directory. [Issue #1282](https://github.com/nwverse/persona-extensions/issues/1282)

| Setting | Default | Token impact |
| --- | --- | --- |
| `inbox_events_enabled` | Off | Allows Steam Inbox events that can trigger AI triage. |
| `inbox_max_events_per_day` | 5 (1–50) | Caps daily emissions that can trigger triage. |
| `patch_notes_summary_enabled` | Off | Allows a model to summarize major updates for frequently played games. |
| `sale_roundup_enabled` | Off | Allows sale roundups that can trigger Inbox triage. Also requires Steam Inbox events. |
| `memory_export_enabled` | Off | Allows the gaming profile to use assistant context via `memory_sources`. |
| `memory_max_items` | 10 (1–30) | Caps profile items added to context. |
| `discovery_tools_enabled` | On | Allows high-context game research tools and their skill. |
| `discovery_max_results` | 15 (1–50) | Caps candidates returned per research request. |

## Development

From the repository root, run the focused race tests and build a package.

```sh
go -C extensions/steam test -race -v ./...
./scripts/package.sh steam
```

The package supports Linux and macOS on amd64 and arm64. The tests use synthetic data and local HTTP test servers.
