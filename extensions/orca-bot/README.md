# Orca bot

Orca bot (Orca 机器人) hands selected new Persona to-dos to a bot of the [Orca](https://github.com/stablyai/orca) desktop app on one of your Macs. Each to-do opens a new chat with the bot, which prepares it (research, a breakdown, a draft). When the bot's turn ends, Persona notifies you; the answer stays in the bot's Orca chat. The bot can read the to-do handed to its chat and write a summary of its preparation back onto it through an MCP tool this extension ships. It cannot reach any other to-do; the trust boundary is your macOS user on that Mac.

- [Extension guide: setup, settings, safety and troubleshooting](../../docs/extensions/orca-bot.md)
- [Installation](../../docs/installation.md)
- [Development and tests](../../docs/development.md)
- [Manifest](persona-extension.json)

The package contains one program, `orca-bot`, in three roles: `hub` (started by the hub, never talks to Orca), `node` (started on the Mac you pick, talks to Orca's local runtime socket only) and `mcp` (the stdio MCP server you register with the bot). No account, key or Orca Cloud connection is needed.

## Setup in short

1. In Settings › Extensions › Orca 机器人, pick the Mac that runs Orca, and set a project and/or keywords: nothing is forwarded until at least one is set.
2. Open the extension's **Orca 机器人** page and pick a bot from the list Orca reports on that Mac.
3. On that Mac, run once the command the page shows. It has this shape (the page fills in the real paths, the bot's id and its token):

   ```sh
   orca bot tools add "<data dir>/bin/orca-bot mcp --socket <data dir>/mcp.sock --bot <bot id> --token <token>" --name persona-todos --bot <bot id>
   ```

From then on, each new to-do of yours that matches is handed to the bot once, at most 10 an hour by default.
