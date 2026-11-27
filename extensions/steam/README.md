# Steam

Steam is a read-only Persona extension for player status, two weeks of playtime, the searchable game library, achievements, official game updates and wishlist prices. Open **Personal › Steam** for details or use the Overview card and the `steam_*` assistant tools.

New installations use Steam Mobile QR by default. In **Settings › Extensions › Steam**, select **Sign In**, scan with Steam Guard in the Steam Mobile App, and approve the login on the phone. The Hub encrypts the refresh token in its connector secret store. The extension keeps the access token and QR account cache in memory.

To use the read-only Web API key fallback, select **Web API key** and enter a SteamID64 or profile vanity name and a key. Existing installations with both values use this mode automatically. The optional Store country defaults to `US`. [Setup, permissions and sign-out](../../docs/extensions/steam.md)

## Achievements and game updates

- Ask `steam_achievement_hunter` for one game's AppID to find the most commonly unlocked achievements you still need and see your recent unlocks. If Steam supplies only counts or the public unlock rates, the tool shows the progress Steam shares and global rankings. Locked hidden achievements display their hidden status with their names and descriptions withheld.
- Open **Personal › Steam › Recent official updates** to browse the latest headlines for your played games. Ask `steam_game_news` for a specific AppID or omit it to check up to four games by default. The tool uses recent playtime first and falls back to lifetime playtime if the recent list is empty or unavailable. [Tool arguments and limits](../../docs/extensions/steam.md#assistant-tools)

## Token settings

These runtime gates and budgets govern wishlist alerts, update excerpts and the planned memory and discovery features. Each disabled feature uses zero extra LLM tokens. [Issue #1282](https://github.com/nwverse/persona-extensions/issues/1282)

| Setting | Default | Token impact |
| --- | --- | --- |
| `inbox_events_enabled` | Off | Starts wishlist scans for alerts about prices, releases, demos and limited-time giveaways. Turning it off stops scans, Inbox notifications and their token use. |
| `inbox_max_events_per_day` | 5 (1–50) | Caps new alerts per Steam account and UTC day. |
| `patch_notes_summary_enabled` | Off | Adds plain-text excerpts of up to 700 characters per post for assistant summaries. The default returns headline metadata only. |
| `sale_roundup_enabled` | Off | Replaces individual discount alerts with a top-five roundup when three or more wishlist games are on sale. Requires Inbox events. |
| `memory_export_enabled` | Off | Allows the gaming profile to use assistant context via `memory_sources`. |
| `memory_max_items` | 10 (1–30) | Caps profile items added to context. |
| `discovery_tools_enabled` | On | Allows high-context game research tools and their skill. |
| `discovery_max_results` | 15 (1–50) | Caps candidates returned per research request. |

Choose the triggers in `wishlist_event_triggers`. The discount threshold defaults to 50%, and `wishlist_target_prices` accepts optional rules such as `123:USD:999`. A local-low alert needs an earlier price in the same currency. Steam checks at startup or QR sign-in and every six hours while alerts are enabled. Each price is announced once per sale window. Releases and demos use their own versions. QR sign-out clears the in-memory observations and Inbox queue. Web API key mode stores these snapshots atomically under `steam-store-v1`. [Wishlist alerts and deduplication](../../docs/extensions/steam.md#wishlist-inbox-alerts)

Use `steam_wishlist_deals` for an on-demand list of deals, target prices met, local lows and giveaways, including when Inbox alerts are off.

## Development

From the repository root, run the focused race tests and build a package.

```sh
go -C extensions/steam test -race -v ./...
./scripts/package.sh steam
```

The package supports Linux and macOS on amd64 and arm64. The tests use synthetic data and local HTTP test servers.
