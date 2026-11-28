# WhatsApp

Connect your own WhatsApp account to Persona using the native multi-device protocol (`go.mau.fi/whatsmeow`, pure Go with `CGO_ENABLED=0` and `modernc.org/sqlite`): QR scan or 8-digit phone pairing code login, quiet presence by default, real-time local indexing and full-text search, on-demand history backfill from your primary phone, inbox (unread DMs and group @mentions/replies with direct reply), knowledge-base archiving, conversation threads, and assistant MCP tools with mandatory phone confirmation for every write action.

- [Extension guide, login, network requirements and tool reference](../../docs/extensions/whatsapp.md)
- [Installation](../../docs/installation.md)
- [Development and tests](../../docs/development.md)
- [Manifest](persona-extension.json)

The shipped [WhatsApp skill](skills/whatsapp/SKILL.md) is runtime assistant input and keeps its original language.
