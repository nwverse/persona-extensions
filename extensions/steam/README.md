# Steam

Steam is a standalone, read-only Persona extension for one configured player's Steam activity. The Personal sidebar page shows the player, two weeks of playtime, the searchable game library, achievements and the public wishlist. The Overview card shows presence, recent playtime and the number of wishlist titles on sale.

Set a SteamID64 or profile vanity name and a Steam Web API key in **Settings › Extensions › Steam**. The optional Store country defaults to `US`. See the [Steam guide](../../docs/extensions/steam.md) for setup, data visibility, assistant tools and cache behavior.

From the repository root, run the focused race tests and build a package.

```sh
go -C extensions/steam test -race ./...
scripts/package.sh steam
```

The package supports Linux and macOS on amd64 and arm64. The tests use synthetic data and local HTTP test servers.
