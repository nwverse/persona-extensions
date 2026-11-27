# Steam

[Documentation](../README.md) · [Source](../../extensions/steam/README.md)

Steam v0.1 reads a configured Steam player's profile, playtime, library, achievements and public wishlist. Open **Personal › Steam** to search the library, sort it by lifetime playtime or name, and open a game to see achievement progress and its Steam Store link. The Overview card opens the same page.

## Configuration

1. Install Steam on your Persona Hub. For a locally built archive, follow [Install a local package](../installation.md#install-a-local-package).
2. In **Settings › Extensions › Steam**, enter a 17-digit individual SteamID64 or the vanity name after `/id/` in the Steam profile URL. The extension resolves vanity names through the Steam Web API. [Steamworks ISteamUser reference](https://partner.steamgames.com/doc/webapi/ISteamUser)
3. Enter your key in the **Steam Web API key** secret field. The Hub encrypts it. [Steam Web API key settings](https://steamcommunity.com/dev/apikey)
4. Set **Store country or region** to a two-letter ISO 3166-1 alpha-2 code for the desired wishlist prices and currency. It defaults to `US`. [ISO 3166 country codes](https://www.iso.org/iso-3166-country-codes.html)

Set Steam profile visibility and **Game details** to **Public** for the activity, library and achievements you want Persona to see. Restricted data shows a visibility message, while available sections remain usable. Steam can return an indistinguishable response for a hidden or empty wishlist. The extension presents this as unavailable unless Steam explicitly returns an empty list. [Steam profile privacy](https://help.steampowered.com/en/faqs/view/588C-C67D-0251-C276)

## Assistant tools

| Tool | Result |
| --- | --- |
| `steam_profile` | Player name, presence and current game, with the owned game count when Steam shares it. |
| `steam_recent_games` | Games played in the last two weeks, lifetime minutes per game and total minutes played in the last two weeks. |
| `steam_library` | Owned games filtered by an optional `query` for title or AppID. Set `sort` to `playtime` (default, highest first) or `name`. |
| `steam_achievements` | Achievement progress for a single numeric `appid`, including unlocked and total counts. |
| `steam_wishlist` | Wishlist titles with current Store prices and discount percentages for the configured country. |

The three list tools accept an `offset` and a `limit` from 1 through 50, with 20 items by default. Continue with `next_offset` using the same search and sort. Playtime is returned in minutes. An unavailable dataset reports a reason. A partial or cached result carries a warning when a request fails.

## Refresh and storage

Profile metadata, vanity resolution and the owned library are cached for eight hours. Presence, recent games and wishlist prices use ten-minute freshness windows. Select **Refresh** to request a new snapshot for all four sections. Game details fetch achievement progress when opened.

The extension keeps one record per dataset in memory and in a `steam-cache-v1` subdirectory of the extension data directory (`PERSONA_EXTENSION_DATA_DIR`, or the data directory sent by the Hub). A temporary request failure can reuse a labeled snapshot up to 24 hours old. A definitive denial for a dataset discards its cached record. Automatic retries for a failing dataset wait 30 seconds, and Refresh can retry immediately.

The navigation and tools browse up to 20,000 library or recent entries and price up to 1,000 wishlist items in batches of 50. The wishlist keeps its full item count alongside the number of Store responses received. The card shows a lower bound when some prices remain unchecked and a dash when none could be checked. Titles without a Steam Store price in the chosen region show a price availability message.

## Network and credentials

Account reads go to `api.steampowered.com`. The Web API key is sent in a header over HTTPS for authenticated player calls to that host. The public wishlist and storefront metadata calls use AppIDs or the configured SteamID64. Store price requests go to `store.steampowered.com` with AppIDs and the selected country code. The client refuses redirects and converts upstream failures to fixed messages. Cached records contain parsed response fields and account or region identifiers. [Steam Web API overview](https://partner.steamgames.com/doc/webapi_overview)

The extension uses IPlayerService for games and ISteamUserStats for achievement progress. Wishlist availability comes from Valve's public Wishlist service, with regional pricing from Steam Store AppDetails. [IPlayerService](https://partner.steamgames.com/doc/webapi/IPlayerService), [ISteamUserStats](https://partner.steamgames.com/doc/webapi/ISteamUserStats)
