# Space Conversations

A space keeps one part of your life together: its own Persona conversations, a SPACE.md with long-lived instructions, and an information boundary. This is the optional product surface for Persona's domain Spaces, not the schema-driven records and boards of the separate `spaces` extension.

## What it does

- Lists existing domain spaces and creates a new one with a stable slug and optional information categories.
- Opens a space's recent conversations and starts a new one in the existing native chat experience.
- Reads and edits the human-owned part of SPACE.md with the core's revision and managed-block checks.
- Archives and restores spaces without deleting their conversations, files or SPACE.md.

## Identity and data

The extension ID is `space-conversations`; its display name is `空间对话`. The existing `spaces` extension and its own records are unchanged.

Track records remain in the assistant's Postgres tables and SPACE.md remains in the track's existing directory on the assistant's computer. The extension does not copy these into a private JSON database. Disabling or uninstalling the extension hides the product surface; it does not delete those records.

The declarative `track_kinds` contribution supplies the `space` kind. Core retains execution, directory isolation, information policy, conversation filtering and managed SPACE.md behavior.

## Permissions

- `spaces`: manage domain-space metadata and SPACE.md, including creating and archiving a space.
- `assistant.ask`: list a space's conversations and start a new one.

This scope is explicit and is not inherited from the legacy every-scope token. Sensitive tracks, including Health, remain subject to their existing extension-denial policy.

## Upgrade behavior

On a hub that already has domain-space records, the core migration installs this extension once from the signed official marketplace and starts it. A hub with no such records does not receive it automatically. An already installed extension, including one the owner disabled, always wins over migration. Existing conversations and files are not migrated or deleted.

## Development

```sh
go -C extensions/space-conversations test ./...
scripts/package.sh space-conversations
```

The UI uses only declarative SDK views. No JavaScript or executable code runs in the clients. The conversation handoff is a narrow app-owned route containing a computer, space and session, not a general internal-link permission.
