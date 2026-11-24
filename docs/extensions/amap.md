# AMap (高德地图)

[Documentation](../README.md) · [Source](../../extensions/amap/)

Places, routes and weather in China for the Persona assistant, from the [AMap Web Service API](https://lbs.amap.com/api/webservice/summary) with the user's own key. Design: [#590](https://github.com/nwverse/persona-extensions/issues/590).

Who it serves: everyday assistant users and anyone who wants an in-app map/route/quota workbench. The user fills in a key (and optionally fixed places such as `家`, `公司` and other frequent places) in 设置 › 扩展 › 高德地图, and gets eleven read-only assistant tools plus a dedicated「高德地图」page and overview card.

| Extension point | Contribution | Behavior |
| --- | --- | --- |
| `connectors` | `amap`, capabilities `status` and `tools` | `amap_search`, `amap_input_tips`, `amap_nearby`, `amap_geocode`, `amap_reverse_geocode`, `amap_district`, `amap_route`, `amap_distance`, `amap_static_map`, `amap_weather` and `amap_places`, all read-only, with no confirmation card. |
| `ui` | `amap` (`nav_page`, icon `location`), `amap-card` (`overview_card`) | Interactive place search, route planner (with driving waypoints and one-tap commute), static map preview with zoom/traffic toggles and "save to places" served via `ui.image`, `常用地点` saved-place manager (`places.json`), city/place weather, and a zero-network monthly usage overview card. |
| `skills` | `amap` | When to use which tool, using fixed/saved places (`amap_places` / direct aliases) or phone location first, and how to treat the quota. |

## The key

| Secret | Meaning |
| --- | --- |
| `api_key` | An AMap key of the **Web服务** (Web Service) platform: at lbs.amap.com, 控制台 › 应用管理 › 我的应用, create an application, then 添加 Key with 服务平台 Web服务. Keys of the JS API or mobile SDK platforms are refused (`10009`). Digital signatures are not supported; an IP allowlist must include the hub machine's public address. The settings show the last four characters. |

The key is sent only to `restapi.amap.com`, in the query string AMap requires. It never appears in configuration, logs, errors, status or tool output: a failed request is worded without its URL, and the key is replaced in any text that could carry it.

**Checked before it is saved** (`settings_check`): one geocoding request with the new key (a public landmark). AMap refusing the key keeps the old settings and says why in words. A used-up allowance or a busy answer still means AMap knows the key, so it is saved. The first key of a new install cannot be checked, because the extension does not run until it has one. The program therefore tries the key once when it starts, and a refused key shows in the connector status straight away.

## Configuration (fixed places)

Fixed places can be configured in **设置 › 扩展 › 高德地图** and/or managed inside the app on the **高德地图 › 常用地点** tab (`places.json` in the extension's data directory, `0600`).

| Setting | Type | Meaning |
| --- | --- | --- |
| `home` | `string` | Saved as `家` (with built-in aliases `home`, `我家`, `家里`, `my home`). Accepts a full address, landmark/POI name, WGS-84 `lat,lng`, or pinned `<label> <lat>,<lng>`. |
| `company` | `string` | Saved as `公司` (with built-in aliases `company`, `work`, `office`, `单位`, `办公室`, `my office`). Same formats as `home`. |
| `places` | `string[]` (max 20) | Additional named places: `名称: 目标` (ASCII `:` or full-width `：`), `名称 纬度,经度`, or a place name directly. Targets support addresses, POI names, or WGS-84 coordinates. |

- **Settings validation**: `settings.check` validates syntax, coordinate ranges (`[-90,90]` / `[-180,180]`, not `0,0`), non-empty names, no bare unnamed coordinates, and duplicate names offline before probing AMap with the API key.
- **Precedence**: Extension Settings entries take precedence over UI-saved entries in `places.json` on duplicate names or the canonical `home` / `company` slots, so UI state never silently shadows Settings.
- **No extra quota when pinned with coordinates**: fixed places given with WGS-84 coordinates resolve in memory without querying AMap search or geocoding. Named/address fixed places are resolved globally (without inheriting another route endpoint's city hint) and cached like normal lookups.

## The tools

Every coordinate the tools take or give is **WGS-84 `纬度,经度`**, the system of Persona's phone location (`phone_location_current`), Apple Maps tools (`phone_maps_*`) and saved places (`list_places`, `save_place`), so the assistant passes points between them unchanged.

| Tool | Arguments | Answer, one line each |
| --- | --- | --- |
| `amap_search` | `keywords`; `city` (a preference, not a limit); `city_only`; `limit` 1–10, default 5 | Name (category), address, point, phone, today's opening hours, rating, cost per person in ¥. |
| `amap_input_tips` | `keywords`; `city`; `city_only`; optional `latitude` + `longitude` bias; `limit` 1–10, default 5 | Autocomplete suggestions: name, district, address, point (when available), adcode. |
| `amap_nearby` | `latitude` + `longitude`, or `place` (address, POI name, or fixed place such as `家`/`公司`, preferring `city` when not a fixed place); `keywords`; `radius_meters` 1–50000, default 1000; `sort` `distance` (default) or `weight`; `limit` | As above with the distance in metres, nearest first. Without keywords AMap's default categories: food, shopping, daily services. |
| `amap_geocode` | `address`; `city` | Up to three matches: formatted address, point, match level (门址, 兴趣点, 区县…), city, district, adcode. |
| `amap_reverse_geocode` | `latitude`, `longitude` | Address, street number with direction and distance, the areas the point is inside, business areas, three nearby places. |
| `amap_district` | optional `keywords` (province/city/district name or adcode, empty for China); `subdistrict` 0–2, default 1 | Administrative hierarchy: level, adcode, citycode, WGS-84 center and bounded child districts. |
| `amap_route` | `mode` `driving` (default), `walking`, `cycling`, `ebike` or `transit`; each end as `*_latitude` + `*_longitude` or a fixed place name/alias, address, or POI name (`from`, `to`); `waypoints` (up to 16 stops for `driving`, fixed places, names/addresses, or WGS-84 `lat,lng`); `city` (where to look unconfigured names up); `prefer` (driving: `recommended`, `fastest`, `avoid_congestion`, `avoid_tolls`, `avoid_highways`); `depart_at` (transit, Beijing time `2026-11-16 08:30`); `alternatives` 1–3; `steps` | Distance in km and time in minutes. Driving adds traffic lights, tolls, a restriction that cannot be avoided, the three main roads and AMap's taxi fare estimate. Transit gives each plan's time, fare, total walking and legs: `步行 880 米 → 地铁2号线内环 前门→宣武门（2 站） → …`. `steps` adds up to 15 turn-by-turn instructions. |
| `amap_distance` | `origins` (1–20 fixed places, names, addresses, or WGS-84 `lat,lng`); `destination` or `destination_latitude` + `destination_longitude`; `mode` `driving` (default), `walking` or `straight`; `city`; `sort` `input` (default), `distance` or `duration` | Batch distance and duration from each origin to the destination, optionally sorted by nearest or fastest. |
| `amap_static_map` | `latitude` + `longitude` or `place` (supports fixed place names); `markers` (up to 10, labelled A–J); `path` (2–20 points connected with a blue polyline); `city`; `zoom` 1–17; `width` / `height` 100–1024 (default 600×400); `traffic` | A standalone text summary plus a PNG map image (`ConnectorToolImage`, auto-falling back from `scale=2` to `scale=1` if over 1 MiB). |
| `amap_weather` | `city` (name, 6-digit adcode, fixed place name such as `家`/`公司`, or WGS-84 `lat,lng`); `days` 0–4, default 3 | The live report (weather, °C, humidity, wind direction and force in 级, report time) and each day's day and night weather, low~high °C and wind. Data from the China Meteorological Administration through AMap. |
| `amap_places` | optional `name` (fixed place name or alias such as `家`, `work`) | Read-only (0 AMap quota): lists fixed places configured in Settings or saved in `常用地点`, or details for a matching entry with source and aliases. |

A place given by name (a route's end, or the centre of a nearby search) is first checked against configured/saved fixed places (`家`, `公司`, custom entries and built-in aliases), which resolve globally without adopting another endpoint's `city` hint. An unconfigured name or address is looked up in one of two ways:

- An **address**, meaning text with a number and a road, lane or house word (`阜通东大街6号`), is geocoded, which spends the larger allowance. A match no finer than a district (国家, 省, 市, 区县, 开发区, 乡镇, 未知) is searched for instead.
- A **name** (`东方明珠`, `人民广场`) costs one keyword search. Geocoding matches bare names poorly: without a city, 东方明珠 went to a shop of that name in another province.

Each falls back on the other, and a lookup is cached for a month. Names repeat across cities, so a route looks an unconfigured name up in the `city` given, else in the other end's city: a point's city (or a fixed place's city, reverse-geocoded when needed) comes from that endpoint, and when only the destination is a fixed place or coordinate and `city` is omitted, the destination resolves first to supply the origin's city hint. The answer shows each resolved place with its city and district (`东方明珠（上海市浦东新区）`), so a wrong match is visible. Transit needs each end's city code, which comes from the same lookups. A route between two points less than 20 m apart is not asked for.

When the user says "here" or "near me", the skill has the assistant call `phone_location_current` first. For "home" or "work", the user can pass `"家"` / `"公司"` directly to the AMap tools (or inspect them quota-free with `amap_places`), falling back to Persona's generic `list_places` if none are configured in the extension. It plans routes but does not open navigation; `phone_maps_navigate` does.

### Coordinates

AMap reads and writes GCJ-02 inside China. The program converts every point at AMap's edge: WGS-84 → GCJ-02 with the published formula, and GCJ-02 → WGS-84 by iterating that formula (well under a metre off). This spends no quota; AMap's own conversion endpoint converts only one way. Checked against AMap on 2026-11-15: its points in mainland China, Hong Kong and Taiwan are all GCJ-02. Converted public landmarks land 14–47 m from AMap's point, unconverted ones 500–640 m away. So the shift applies to all three, within a coarse outline that cuts out the neighbouring countries. Outside China points pass through unchanged.

## Quota and pacing

AMap's free allowances are monthly and shared by everything in a group. For a verified individual developer, from 2025-05-20: 150,000 a month for 基础 LBS (geocoding, reverse geocoding, districts, routes, distance measurement and static maps), 5,000 for 基础搜索 (keyword, around search and input tips) and 5,000 for weather. Unverified accounts get less, and the per-second limit is shown only in the AMap console.

- **Cache** (memory only): geocoding and reverse geocoding 30 days (at most 2000 answers in all); searches 15 minutes; weather 20 minutes live, 1 hour forecast; driving 2 minutes (live traffic); other routes 15 minutes. The same request asked while one is on its way waits for that one.
- **Pacing**: requests start at most three a second. One that would wait more than 3 seconds for its turn is refused as `rate_limited`.
- **Too frequent** (`10004`, `10014`–`10016`, `10019`–`10021`): tried once more after a second, then `rate_limited`.
- **Allowance used up**: `10003` and `10044` (and the overseas `10029`, `10045`) pause that group until midnight Beijing time; `40000` and `40003` until the 1st of next month. While paused, the group's tools answer at once without asking AMap, saying which tools still work. The pause survives a restart (`usage.json`).
- **Counts**: every request that reached AMap is counted per group and month (Beijing time) in `usage.json` in the extension's data directory (0600), with a 32-bit fingerprint of the key so a new key starts from zero. The connector status shows them: `本月已用：搜索 12 次 · 地址和路线 30 次 · 天气 3 次`, and any paused group with when it resumes.

## Status

| State | When |
| --- | --- |
| `ok` | Normally, with this month's counts as the account line and the last successful request. A paused group or a failed request (network, AMap busy, a route AMap cannot plan) leaves it `ok`: the other tools still work, and the hub offers tools only while a connector is `ok`. |
| `error` | AMap refused the key itself (`10001`, `10005`–`10010`, `10013`, `10026`, `10041`, `40002`): what it means, steps to fix it and the console link. Saving a new key restarts the program. A service the key lacks (`10002`, `10012`) is said by that tool only, since it may concern one service. |
| `not_configured` | No key (the hub normally shows the extension as needing setup instead). |

## Privacy

Search words, addresses and coordinates (including resolved fixed places and the user's current position when the assistant passes it on) go from the hub machine to `restapi.amap.com`. Nothing else is sent, and `permissions.network` lists only that host. Query results are cached in memory only. On disk in the extension's data directory, `usage.json` (0600) keeps only request counts and quota pauses, and if the user adds places in the in-app `常用地点` tab, those places are stored locally in `places.json` (0600). Places configured in Extension Settings are stored in Persona's own extension configuration and read via `initialize`. Logs carry error codes, counts, and config parse warnings without secrets.

## Development

```sh
go -C extensions/amap test -race -count=1 -timeout=90s ./...
scripts/package.sh amap
```

The tests run against a fake AMap (`fake_test.go`) answering scrubbed recordings of public landmarks (fake phone numbers) under [`extensions/amap/testdata`](../../extensions/amap/testdata). They need no network or key. They cover the coordinate conversion against AMap's own points, the key staying out of every error, the cache, pacing, retry, allowance pauses and counts, each tool's request and answer, the settings check and the protocol.
