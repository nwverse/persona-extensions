# Steam

Steam researches Store games and reads player status, two weeks of playtime, the searchable game library, achievements, official game updates and wishlist prices. Open **Personal › Steam** to see these details, review your backlog and annotate games locally. The Overview card opens the same page. Use the `steam_*` assistant tools to work with your library and get picks for tonight.

New installations use Steam Mobile QR by default. In **Settings › Extensions › Steam**, select **Sign In**, scan with Steam Guard in the Steam Mobile App, and approve the login on the phone. The Hub encrypts the refresh token in its connector secret store. The extension keeps the access token and QR account cache in memory.

To use the read-only Web API key fallback, select **Web API key** and enter a SteamID64 or profile vanity name and a key. Existing installations with both values use this mode automatically. The optional Store country defaults to `US`. [Setup, permissions and sign-out](../../docs/extensions/steam.md)

## Achievements and game updates

- Ask `steam_achievement_hunter` for one game's AppID to find the most commonly unlocked achievements you still need and see your recent unlocks. If Steam supplies only counts or the public unlock rates, the tool shows the progress Steam shares and global rankings. Locked hidden achievements display their hidden status with their names and descriptions withheld.
- Open **Personal › Steam › Recent official updates** to browse the latest headlines for your played games. Ask `steam_game_news` for a specific AppID or omit it to check up to four games by default. The tool uses recent playtime first and falls back to lifetime playtime if the recent list is empty or unavailable. [Tool arguments and limits](../../docs/extensions/steam.md#assistant-tools)

## Backlog and tonight's picks

Open a game from the library to select and save a local status. The **Backlog & local progress** section shows library counts, and annotated games display their status on their rows. Use **Clear local annotation** in game details to remove the status, note and tags.

- `steam_backlog_summary` counts games by lifetime minutes, recent activity and local status. Pass `bucket` or `status` to list matching games in pages of up to 50. Counts can overlap.
- `steam_backlog_mark` sets `status` for one `appid`, with an optional `note` and `tags`. Omitted fields retain their values. Use `clear: true` with the AppID to clear the annotation.
- `steam_tonight_pick` ranks games from your library by `available_minutes` and `mood` and gives a reason for each pick. Filter by local `tag` or title `query`. It returns five candidates by default, up to 20.

Both sign-in modes save explicit annotations by SteamID64 in `PERSONA_EXTENSION_DATA_DIR/steam-store-v1/backlog.json`. Signing out keeps those annotations. Steam account access stays read-only. [Tool arguments and storage details](../../docs/extensions/steam.md#backlog-and-tonights-picks)

## Game research

Use `steam_store_tags` to find localized Store tags, `steam_store_search` to filter by gameplay, price, sale, language and platform, and `steam_game_details` to compare a shortlist. Search marks games in your library or wishlist when Steam shares those datasets. Details include current regional prices, Deck status, player modes, and overall and 30-day review percentages and counts. [Research arguments and data sources](../../docs/extensions/steam.md#game-research)

The **Steam game discovery** skill is an optional contribution. Enable it under **Settings › Extensions › Steam › Contributions**. Turning off **Steam game research tools and skill** hides the three tools and withdraws the skill from the assistant's next refresh.

## Friends Radar

**Friends Radar** is off by default. Enable it in **Settings › Extensions › Steam** to show **Friends in game** on the Steam page and expose `steam_friends_radar` and `steam_coop_overlap`. While it is off, the extension sends zero friend requests and adds zero friend tool definitions to assistant context.

Web API key mode uses the account's publicly visible friends list and presence. Mobile QR uses the signed-in account's access token and reports any friend endpoints Steam restricts for that session. Shared library results use the friends and game details Steam makes visible to the current sign-in method. Friend lists and presence stay in memory for a 90-second freshness window. QR sign-out clears that cache, and shared library reads use request-local data. [Friends Radar inputs, paging and privacy](../../docs/extensions/steam.md#friends-radar)

## Token settings

These runtime gates and budgets cover game research, update excerpts, wishlist alerts and the planned memory features. Each disabled feature uses zero extra LLM tokens. Price and playtime snapshots reuse data already read from Steam. [Issue #1282](https://github.com/nwverse/persona-extensions/issues/1282)

| Setting | Default | Token impact |
| --- | --- | --- |
| `inbox_events_enabled` | Off | Starts wishlist scans for alerts about prices, releases, demos and limited-time giveaways. Turning it off stops scans, Inbox notifications and their token use. |
| `inbox_max_events_per_day` | 5 (1–50) | Caps new alerts per Steam account and UTC day. |
| `patch_notes_summary_enabled` | Off | Adds plain-text excerpts of up to 700 characters per post for assistant summaries. The default returns headline metadata only. |
| `sale_roundup_enabled` | Off | Replaces individual discount alerts with a top-five roundup when three or more wishlist games are on sale. Requires Inbox events. |
| `memory_export_enabled` | Off | Allows the gaming profile to use assistant context via `memory_sources`. |
| `memory_max_items` | 10 (1–30) | Caps profile items added to context. |
| `discovery_tools_enabled` | On | Allows game research tools and makes the separately enabled skill available. |
| `discovery_max_results` | 15 (1–50) | Caps tags and search candidates per call. Details batches also have a ceiling of 10 games. |

Choose the triggers in `wishlist_event_triggers`. The discount threshold defaults to 50%, and `wishlist_target_prices` accepts optional rules such as `123:USD:999`. A local-low alert needs an earlier price in the same currency. Steam checks at startup or QR sign-in and every six hours while alerts are enabled. Each price is announced once per sale window. Releases and demos use their own versions. QR sign-out clears the in-memory observations and Inbox queue. Web API key mode stores these snapshots atomically under `steam-store-v1`. [Wishlist alerts and deduplication](../../docs/extensions/steam.md#wishlist-inbox-alerts)

Use `steam_wishlist_deals` for an on-demand list of deals, target prices met, local lows and giveaways, including when Inbox alerts are off.

## Development

From the repository root, run the focused race tests and build a package.

```sh
go -C extensions/steam test -race -count=1 ./...
go -C extensions/steam vet ./...
./scripts/package.sh steam
```

The package supports Linux and macOS on amd64 and arm64. The tests use synthetic data and local HTTP test servers.
