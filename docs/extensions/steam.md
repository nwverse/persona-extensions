# Steam

[Documentation](../README.md) · [Source](../../extensions/steam/README.md)

Steam reads player status, playtime, library, achievements, official game updates and wishlist prices. Open **Personal › Steam** to search the library, sort it by lifetime playtime or name, and open a game to see achievement progress and its Steam Store link. Select **Recent official updates** on the Steam page to see up to five headlines with links to their Steam announcements. Updates load when you open that view. The Overview card opens the main Steam page.

## Sign in with Steam Mobile QR

1. Install Steam on a Persona Hub that supports extension sign-in. For a locally built archive, follow [Install a local package](../installation.md#install-a-local-package).
2. Open **Settings › Extensions › Steam** and select **Sign In**. New installations use QR by default. If you already have a Steam player ID and Web API key, select **Steam Mobile QR** as the sign-in method and save before signing in.
3. Open Steam Guard in the Steam Mobile App, scan the QR code and approve the sign-in on your phone. The extension reads the SteamID of the account you approved. A pending scan expires after five minutes. Start again if the code expires or Steam ends the request. [Steam Guard Mobile Authenticator](https://help.steampowered.com/faqs/view/7EFD-3CAE-64D3-1C31)

QR sign-in can read the signed-in account's private library and wishlist when Steam shares them. Each dataset can be unavailable independently. Presence shows as unknown when Steam omits it. An ambiguous wishlist reply shows as unavailable, and an explicitly empty wishlist shows as empty.

## Use a Web API key

In **Settings › Extensions › Steam**, choose **Web API key** as the sign-in method and save a **Steam player ID** and **Steam Web API key**. The ID is a 17-digit individual SteamID64 or the vanity name after `/id/` in a Steam profile URL. The Hub encrypts the key. Installations that already have both settings continue in this mode when **Sign-in method** is unset. Clear the saved key in the extension settings to disconnect that configuration. [Steam Web API key settings](https://steamcommunity.com/dev/apikey) · [Steamworks ISteamUser reference](https://partner.steamgames.com/doc/webapi/ISteamUser)

Web API key mode uses read-only calls for publicly visible data. Set the profile and **Game details** to **Public** for the activity, library and achievements you want Persona to see. [Steam profile privacy](https://help.steampowered.com/en/faqs/view/588C-C67D-0251-C276)

In either mode, set **Store country or region** to a two-letter ISO 3166-1 alpha-2 code for wishlist prices and currency. The default is `US`. [ISO 3166 country codes](https://www.iso.org/iso-3166-country-codes.html)

## Assistant tools

| Tool | Result |
| --- | --- |
| `steam_profile` | Player name, presence and current game, with the owned game count when Steam shares it. |
| `steam_recent_games` | Games played in the last two weeks, lifetime minutes per game and total minutes played in the last two weeks. |
| `steam_library` | Owned games filtered by an optional `query` for title or AppID. Set `sort` to `playtime` (default, highest first) or `name`. |
| `steam_achievements` | Achievement progress for a single numeric `appid`, including unlocked and total counts. |
| `steam_wishlist` | Wishlist titles with current Store prices and discount percentages for the configured country. |
| `steam_achievement_hunter` | Locked achievements sorted by global unlock rate and a timeline of dated personal unlocks for one numeric `appid`. |
| `steam_game_news` | Latest game updates in descending date order for one optional numeric `appid` or a selection of the account's played games. |

`steam_recent_games`, `steam_library` and `steam_wishlist` accept an `offset` and a `limit` from 1 through 50, with 20 items by default. Continue with `next_offset` using the same search and sort. Playtime is returned in minutes. An unavailable dataset reports a reason. A partial or cached result carries a warning when a request fails.

`steam_achievement_hunter` applies the same `offset` and `limit` to each list and provides `next_offset` when another page is available. The personal timeline uses Unix timestamps in seconds and starts with the most recent unlock. Hidden achievements carry `hidden: true`. Their names, API identifiers and descriptions appear only after a dated unlock. Missing achievement metadata yields `hidden: null` and hides those fields. When Steam withholds individual progress, use `global_easiest` and `global_rarest` as reference lists. Personal progress counts reflect what Steam supplies, with `unlocked: null` if it withholds the unlocked count.

`steam_game_news` accepts a `limit` from 1 through 20, with 10 updates by default. Omitting `appid` checks up to `games_limit` games (default 4, range 1–5) ranked by minutes played over the past two weeks. If the recent list is empty or unavailable, Steam ranks played library games by lifetime minutes. The default `official_only: true` uses Steam's official announcements feed and selects updates, patches and DLC using tags and headlines from up to 40 recent posts per game. This can miss posts without recognized tags or headlines. Set `official_only: false` to include other announcements and third-party media. Publication dates are Unix timestamps in seconds. [Steamworks news API](https://partner.steamgames.com/doc/webapi/ISteamNews)

## Token settings

Under **Settings › Extensions › Steam**, these settings define the defaults and limits for update excerpts and the planned Inbox, memory export and high-context discovery features. A disabled feature uses zero extra LLM tokens. Its gate applies before adding update excerpts, emitting events, exporting context or running discovery. [Issue #1282](https://github.com/nwverse/persona-extensions/issues/1282)

| Setting | Default | Effect |
| --- | --- | --- |
| `inbox_events_enabled` | Off | Allows Steam to send wishlist price drops, releases, sale roundups and major updates to the Inbox, where triage can use tokens. Turning this off stops all Steam Inbox emissions. |
| `inbox_max_events_per_day` | 5 (1–50) | Caps daily Steam events so they can trigger triage only up to this count. |
| `patch_notes_summary_enabled` | Off | Allows `steam_game_news` to share a plain-text excerpt of up to 700 characters per update for assistant summaries. When off, the tool reports `summary_enabled: false` and returns only the AppID, game name, announcement ID, title, date, source and link per update. |
| `sale_roundup_enabled` | Off | Allows wishlist deal roundups during major sales. This also requires `inbox_events_enabled`. Turning either off skips roundup work and its Inbox delivery. |
| `memory_export_enabled` | Off | Allows `memory_sources` to share a personal gaming profile. Turning this off keeps the profile out of assistant context. |
| `memory_max_items` | 10 (1–30) | Caps the number of gaming preference items in the exported profile. |
| `discovery_tools_enabled` | On | Allows high-context game research tools and their skill. Turning this off disables those features before they can add research context. |
| `discovery_max_results` | 15 (1–50) | Caps the candidates that a game search or research request can add to context. |

Token settings use the documented default when a numeric limit is missing or outside its range. The Steam page shows news headlines, dates and links. Steam collects local observations during normal reads.

## Refresh and storage

Profile metadata, vanity resolution and the owned library are cached for eight hours. Presence, recent games and wishlist prices use ten-minute freshness windows. Select **Refresh** to request a new snapshot for all four sections. Game details fetch achievement progress when opened. The global unlock rates, achievement schema and news are cached in memory by AppID, with freshness windows of one hour, eight hours and ten minutes respectively. The news page and tool share cached headline results when summary mode is off.

In QR mode, the extension keeps its data cache in memory for the active signed-in account. Web API key mode keeps one bounded record per dataset in memory and in `steam-cache-v1` under the extension data directory (`PERSONA_EXTENSION_DATA_DIR`, or the data directory sent by the Hub). A temporary request failure can reuse a labeled snapshot up to 24 hours old. A definitive denial for a dataset discards its cached record. Automatic retries for a failing dataset wait 30 seconds, and Refresh can retry immediately.

The navigation and tools browse up to 20,000 library or recent entries and price up to 1,000 wishlist items in batches of 50. The wishlist keeps its full item count alongside the number of Store responses received. The card shows a lower bound when some prices remain unchecked and a dash when none could be checked. Titles without a Steam Store price in the chosen region show a price availability message.

### Local snapshots

QR mode keeps all four snapshot stores in memory for the signed-in session and clears them at sign-out. Web API key mode uses four versioned files under `steam-store-v1` inside the extension data directory. Each file has a 16 MiB limit and is replaced atomically. When the Hub supplies no data directory, Web API key mode also keeps the stores in memory. Price and playtime observations use existing reads without extra network or LLM requests. A failed local write leaves the Steam read result available.

| Store | Contents and limits |
| --- | --- |
| Price history | Fresh, checked wishlist prices are scoped to the Steam account, Store country and AppID. Up to 2,000 series retain 32 sampled UTC days each, the latest quote and the first observation of the local low with its discount. A change in Store currency starts a new series. |
| Playtime | A complete library read records lifetime minutes, the two-week minutes supplied by the library, and counts of owned and unplayed games. The latest snapshot on each UTC day is kept for up to 400 days through the most recent observation, across up to 16 accounts. |
| Backlog | Local per-account and per-game annotations support `unplayed`, `playing`, `queued`, `paused`, `completed` and `dropped`. The store holds up to 2,000 entries, with a 240-character note and up to eight tags of 32 characters each. Edits apply to the local annotation store only. |
| Event cursors | Records of successful deliveries are scoped to the account, event kind, deduplication key and sale window or version. Up to 5,000 cursors expire after 90 days. |

## Network and credentials

QR sign-in uses Valve's first-party `IAuthenticationService` over HTTPS. The extension begins a `MobileApp` session with `BeginAuthSessionViaQR`, follows Valve's poll interval with `PollAuthSessionStatus`, and renews access with `GenerateAccessTokenForApp`. The extension hands the `s.team` challenge URL to the Hub for local QR rendering. The extension's only network permissions are `api.steampowered.com` and `store.steampowered.com`. [Valve authentication message definitions](https://github.com/SteamDatabase/Protobufs/blob/master/webui/service_authentication.proto) · [Persona extension sign-in protocol](https://github.com/nwverse/persona/blob/main/docs/extensions/extensions.md#sign-in-auth)

The QR grant is a Steam mobile-client session with account privileges determined by Valve. The extension uses it for the account reads below and sends the access token only in HTTPS requests to `api.steampowered.com`. Web API key mode uses `X-WebAPI-Key` for the authenticated legacy calls at that host and reads the public wishlist anonymously. Store names and prices are fetched anonymously in both modes. The client refuses redirects and reports fixed diagnostic messages. [Steam Web API overview](https://partner.steamgames.com/doc/webapi_overview)

| Read | Steam Mobile QR | Web API key |
| --- | --- | --- |
| Player name, presence and current game | `IPlayerService/GetPlayerLinkDetails` | `ISteamUser/GetPlayerSummaries` |
| Recent playtime | `IPlayerService/GetRecentlyPlayedGames` | `IPlayerService/GetRecentlyPlayedGames` |
| Owned games and lifetime playtime | `IPlayerService/GetOwnedGames` | `IPlayerService/GetOwnedGames` |
| Achievement progress | `IPlayerService/GetAchievementsProgress` | `ISteamUserStats/GetPlayerAchievements` |
| Individual achievement states and unlock times | `IPlayerService/GetUserAchievements` when Steam shares them | `ISteamUserStats/GetPlayerAchievements` |
| Game achievement schema | Public `IPlayerService/GetGameAchievements` | `ISteamUserStats/GetSchemaForGame` with public `GetGameAchievements` fallback |
| Global achievement percentages | Public `ISteamUserStats/GetGlobalAchievementPercentagesForApp` | Same public endpoint |
| Game news | Public `ISteamNews/GetNewsForApp` | Same public endpoint |
| Wishlist | `IWishlistService/GetWishlist` | Public `IWishlistService/GetWishlist` |

Wishlist names use `IStoreBrowseService/GetItems` in Simplified Chinese or English. Prices come from `store.steampowered.com/api/appdetails` for the selected country. [Player message definitions](https://github.com/SteamDatabase/Protobufs/blob/master/webui/service_player.proto) · [Steamworks IPlayerService](https://partner.steamgames.com/doc/webapi/IPlayerService)

The Hub encrypts the versioned QR sign-in record through `connector.secrets.*`. It holds the refresh token, SteamID, account name and timestamps. The access token stays in the extension process. The extension makes a rotated refresh token usable after the Hub has saved it.

## Sign out or revoke a QR session

**Sign Out** in Persona cancels the extension's pending sign-in and reads, clears the session's cache and snapshots, and removes the Hub's saved QR sign-in. For server-side revocation, open Steam's **Authorized Devices** and remove the corresponding Persona session. When Steam confirms that the refresh token is invalid or revoked, Persona clears the session and prompts you to sign in again. [Steam Authorized Devices](https://store.steampowered.com/account/authorizeddevices) · [Steam Support account security](https://help.steampowered.com/en/faqs/view/6639-EB3C-EC79-FF60)

Switching to Web API key mode uses the ID and key from settings. Switching back to QR restores any saved QR sign-in. Use **Sign Out** while QR is selected if you want to clear that record before switching.
