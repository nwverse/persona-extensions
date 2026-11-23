# Orca bot

[Documentation](../README.md) · [Extension README](../../extensions/orca-bot/README.md) · [Manifest](../../extensions/orca-bot/persona-extension.json)

Orca bot (`orca-bot`, Orca 机器人) sends selected new Persona to-dos to a bot of the [Orca](https://github.com/stablyai/orca) desktop app, and lets that bot write its preparation back onto the to-do. It changes neither Orca nor Persona core.

## What it does

1. **Persona → bot.** The hub program hears `todo.changed` with `what: "created"` (scope `todos.read`). For a to-do that matches your filter, it asks the node program on your Mac to start a new chat with your bot (`bots.session.start`) and send the to-do with a fixed "prepare this to-do" request (`agentSession.send`), over Orca's local runtime socket.
2. **Done → notification.** The node program polls the chat (`agentSession.history`) until the bot's turn ends. The hub program then sends you one notification (scope `notify`): which bot, which to-do, and that the answer is in the bot's Orca chat. The answer itself stays in Orca.
3. **Bot → Persona (write-back over MCP).** The extension ships a stdio MCP command that you register as one of the bot's own tools. Its tools let the bot list and read the to-dos handed to it and write its preparation back onto such a to-do (scope `todos.write`).

## Setup

1. Install the extension. Approve its permissions: the to-do scopes and notifications on the hub, and running a program on the Mac you pick.
2. In **Settings › Extensions › Orca 机器人**:
   - **运行 Orca 的 Mac**: turn on the one Mac where Orca runs (macOS only, one).
   - **只转发这个项目里的待办**: a knowledge-base project slug, as shown in a to-do's project.
   - **标题或备注里含这些词的待办**: keywords; any one of them in the title or the notes matches, ignoring case.

   Set at least one of the project and the keywords: with neither, nothing is forwarded. With both, a to-do must satisfy both.
3. Open the extension's **Orca 机器人** page (in the same settings, under the extension). It lists the bots that Orca on that Mac reports (`bots.list`). Tap one and confirm.
4. The page then shows the command that gives the bot the MCP tool. Run it once in a terminal on that Mac. It has this shape; copy the real one from the page:

   ```sh
   orca bot tools add "<data dir>/bin/orca-bot mcp --socket <data dir>/mcp.sock --bot <bot id>" --name persona-todos --bot "<bot name>"
   ```

   `<data dir>` is the extension's data directory on that Mac (normally `~/.persona/node-plugins/orca-bot/data`). `bin/orca-bot` there is a link the node program keeps pointing at its current version, so the registered command survives upgrades. Check it with `orca bot tools --bot "<bot name>"`. Picking another bot needs the command again, with that bot's id.

Forwarding starts when the Mac, the bot and the filter are all set. Only to-dos made **after** that moment are forwarded, never older ones. Changing the Mac, the bot or the filter starts again from the moment of the change.

### Why the bot is picked on a page

Persona's settings form has no list filled in at run time (no dynamic `enum`), and an extension cannot write its own settings. So the bot is picked on the extension's own `extension_page`, from the live `bots.list` of the chosen Mac. The choice is kept in the hub program's data directory together with the Mac it was listed on. A typed id is never accepted: the page action checks the id against a fresh `bots.list` first.

## What the bot receives

One message per to-do, in a new chat:

- a fixed request: prepare this to-do (research, a breakdown into steps, drafts), and do nothing irreversible on the owner's behalf;
- the to-do as data: id, title, notes, project and due date, as JSON inside a `<todo>` block. `<`, `>` and `&` are escaped, so nothing in the to-do can close the block, and the request says the block is data, not instructions;
- how to finish: write the main points back with `todo_write_preparation`, then summarise in the chat.

## The MCP tools

The server is named `persona-todos` and has three tools:

| Tool | What it does |
| --- | --- |
| `todo_list` | The to-dos handed to this bot |
| `todo_get` | One of them as it was handed over: title, notes, project, due date, and the state of its write-backs |
| `todo_write_preparation` | Writes the bot's preparation onto the to-do: at most 4000 characters a time, 10 times a to-do; the same text is written once |

**Where the write-back goes.** It is written as an outcome decision on the to-do (`POST /api/v1/todos/{id}/decisions`, `source: "outcome"`, with a `ref`). The question names the bot ("Orca 机器人「…」的准备"), the choice is the bot's text. This is the least invasive field the to-do API offers an extension: it appends a record attributed to the extension (`extension:orca-bot`) and never overwrites the owner's title or notes, so there is no race with the owner's edits. The same `ref` written twice records once, and a decision sends no `todo.changed`. The alternative, `PATCH` of `notes`, would rewrite the owner's own text.

**Who can reach what.** The MCP command connects only to the node program's unix socket. The socket sits in the extension's data directory and only your user can open it (mode `0600`). The node program answers only for to-dos it handed to the bot named by `--bot`. When Orca tells the command which chat runs it (`ORCA_AGENT_SESSION_ID`), only the to-do handed to that chat is reachable. The hub program checks a write-back again before writing: the to-do must be in its own mapping, handed to that bot and that chat on the Mac in the settings. Anything else is refused.

## Safety

- **Data, not instructions.** See "What the bot receives".
- **Each to-do is sent once.** The hub program records a to-do as pending before it asks the node program, and the node program records the chat before it sends. A retry reuses the same chat, text and Orca operation id (`clientOperationId`), so Orca answers it as a replay rather than sending twice. Both records survive restarts.
- **Dropped events are caught.** The event queue drops its oldest events past 512 and does not redeliver them. The hub program therefore reads `GET /api/v1/todos` at start and every five minutes. A to-do whose creation time it cannot read is forwarded only from its own event.
- **No loop.** Write-backs are decisions, which send no `todo.changed`. A to-do in the mapping is never handed over again, whatever event comes for it.
- **No message text or secrets in logs.** Logs and the page's status name only routes, error codes and the extension's own words. They never contain to-do text, write-back text, Orca's token or its socket messages.
- **Fail closed.** The node program reads `~/Library/Application Support/orca/orca-runtime.json` (or `$ORCA_USER_DATA_PATH`) again whenever it cannot connect or is refused as unauthorized: Orca changes its token every launch. It checks `status.get` before anything else and refuses a runtime that speaks an incompatible protocol, a method that does not exist, or an answer of an unexpected shape. While Orca is not running, a hand-over stays pending and is retried with backoff (up to 8 times). A bot that is gone fails the hand-over at once, and the page asks you to pick another.
- **Unix socket only.** It never uses Orca's paired WebSocket or Orca Cloud. Streaming methods are refused on the unix socket, so the end of a turn is polled every 10 seconds.

## When the bot finishes

The node program watches each chat it handed a to-do to:

- the turn has ended when `agentSession.history` reports the session no longer `working`, after it was seen working, or after a minute if it never was;
- a chat Orca no longer has ends as "unavailable"; a chat still working after 24 hours ends as "timeout" and is no longer followed.

You get one notification per to-do. If the hub program misses the node program's message, it is sent again every minute until acknowledged. The notification is still sent once.

## Status and troubleshooting

The page shows whether forwarding runs and what is missing, Orca's state on the Mac, the chosen bot, the filter and the latest 20 hand-overs with their state.

| The page says | What to do |
| --- | --- |
| 连不上那台 Mac 上的扩展程序 | The Mac is offline, or the node program is not running there: check the Mac's row in the extension's settings |
| Orca 没在运行 | Open Orca on that Mac; pending hand-overs are retried |
| 版本不兼容 | Update Orca, or this extension, so their runtime protocols meet |
| 选过的「…」不在列表里了 | The bot was deleted or renamed in Orca: pick one again |
| Orca 里的机器人功能关着 | Turn bots on in Orca |

## Permissions

- Hub: `todos.read` (hear and read to-dos), `todos.write` (write the bot's preparation as a decision), `notify` (the notification).
- Mac: runs the node program; `files`, because it reads Orca's `orca-runtime.json` and connects to Orca's socket outside its own data directory. No network access.

## Limits

- One Mac and one bot.
- The answer is not copied into Persona: only the notification and what the bot writes back are.
- The MCP command's restriction to one chat needs Orca to pass `ORCA_AGENT_SESSION_ID` to the bot's tool processes. Without it, the command can reach every to-do handed to that bot, and no others.
- Tested against a fake Orca runtime socket and a fake hub only; Orca's runtime protocol is not documented by Orca, and its shapes follow Orca's source and Persona core's own client.
