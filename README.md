# Persona Extensions

[简体中文](README.zh-CN.md)

Public extensions for Persona: connect data sources, add assistant tools and skills, and contribute views rendered by the apps. This repository is also an extension market, using one embedded-signature catalog and digest-pinned OCI packages on GHCR. The initial catalog conversion retains frozen cleaned Release references until the authorized cutover bootstrap and anonymous public gate complete; it does not publish packages by itself.

New catalog signatures also cover their predecessor and source inventory, which PR CI checks against the real base commit. Publication stays checks-only in next; after cutover, the final repository identity enables trusted main publication without an extra repository setting. Package visibility is the only manual package setting. See [releases](docs/releases.md) for continuity, immutable versions and dedicated catalog-update issues.

[![Persona Extensions promo: add the market with one line, then pick the extensions you need](docs/assets/promo-preview.webp)](#video-tour)

[Watch the video tour (with voice-over)](#video-tour)

## Video tour

https://github.com/user-attachments/assets/0198ecd2-1e5f-4ca1-95f4-0473b0488670

[Download the HD version and subtitles](https://github.com/nwverse/persona-extensions/releases/tag/promo-persona-extensions-v1)

## Quick start

In the Persona app, open **Settings → Extensions → Extension markets → Add** and enter:

```text
github:nwverse/persona-extensions
```

Then choose an extension to install. This public market does not require a GitHub token. Use a hub with embedded catalog/OCI support and the current official key pin; older hubs can still install local packages.

On the hub's machine, you can register the same market with:

```sh
personad extension market add github:nwverse/persona-extensions
```

See [installation](docs/installation.md) for local packages, older hubs and extension-specific setup.

## Extensions

See the [`extensions/`](extensions/) directory for the available extensions. Each extension documents itself in its own directory, with longer guides under [`docs/extensions/`](docs/extensions/).

The bypass extension's source is now `agent-bypass-allow` (formerly `claude-bypass-allow`). Its same-market update requires explicit identity confirmation and preserves installed settings, rules and data. Wait for its package/catalog publication and upgrade the hub and nodes first; do not uninstall/reinstall to migrate. See [its guide](docs/extensions/agent-bypass-allow.md#upgrading-from-the-former-id).

### Water and built-in Health

Persona releases that include [nwverse/persona#14228](https://github.com/nwverse/persona/pull/14228) have water logging, reminders and widgets built into Health. On iPhone or iPad, the Health home offers a card to import the water extension's records and settings and disable the extension. **Migrate before uninstalling**, which deletes its data. Replace extension widgets, watch-face complications and shortcuts with built-in ones; enabling both reminder systems causes duplicate reminders. Web-only and Android-only users can keep using the extension. See the [migration guide](docs/extensions/water.md#move-water-tracking-into-health).

## Documentation and contributing

- [Documentation index](docs/README.md)
- [Development guide](docs/development.md)
- [Manifest, configuration and protocol reference](docs/reference.md)
- [Release and market signing guide](docs/releases.md)
- [Contributing](CONTRIBUTING.md)

Documentation and manually maintained code comments default to English. The Chinese README is a corresponding translation; runtime localization and meaningful test fixtures are kept in their original language.
