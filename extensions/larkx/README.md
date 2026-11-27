# Feishu through LarkAgentX (larkx)

Feishu as yourself, through [LarkAgentX](https://github.com/cv-cat/LarkAgentX): sign in to the Feishu web client with a QR code, receive every message over its long connection into the inbox and the knowledge base, and send as yourself after you confirm the full text on the phone. Write `/agent <what to do>` in any chat to hand it to the assistant, whose answer appears under it, as you, marked as an AI answer; the setting **/agent 触发前缀** (`agent_prefix`) changes that trigger prefix (must start with `/`, not `//`, be 2–32 characters with no whitespace, control characters or colons; blank or invalid values keep `/agent`), and **/agent 回复末尾** (`agent_reply_suffix`) changes that closing line, and a blank one or one shorter than 4 characters keeps the default, so an answer is always marked. A Python program; Persona needs no change.

Read this before installing:

- **Unofficial protocol.** LarkAgentX reverse-engineers the Feishu web client. It can break whenever Feishu changes, it may not fit Feishu's terms of use, and the account can be flagged. Whether to use it is the account owner's call; for a company account, check the company's rules first.
- **Not in the package.** LarkAgentX has no license, so neither this repository nor the package carries its code. On first start the program downloads it from GitHub at the commit in [`src/larkx_persona/upstream.json`](src/larkx_persona/upstream.json) and checks every file's SHA-256, then installs its dependencies from the hash-locked [`requirements.txt`](requirements.txt).
- **The sign-in is a full web session.** It stays in the hub's encrypted secret store, never in a file or the log.

- [Extension guide](../../docs/extensions/larkx.md)
- [Installation](../../docs/installation.md)
- [Development and tests](../../docs/development.md#add-a-python-extension)
- [Manifest](persona-extension.json)

## Send real mentions

`larkx_send` accepts `mentions: [{id, name}]` using the exact person or bot and numeric id returned by `larkx_members` or `larkx_search`; write `@name` literally in `text` for each target. It builds a rich-text AT element and the notification user-id list, so the target gets a real mention rather than plain text. Without `mentions`, an @name stays ordinary text and the original send path is unchanged. Assistant replies resolve exact names from the chat's member/bot roster (via `PullChatChattersRequest`) as well as recent senders and historical mentions; unknown or ambiguous names are never guessed.

## Invoke the assistant by mentioning yourself

Write your configured trigger prefix (default `/agent`, set via `agent_prefix`), such as `/agent <what to do>`, or mention your own signed-in Feishu account with a real rich-text @ tag in your own message. The @ tag is removed from the request. A mention without any other text uses the same context-based request as an empty trigger-prefix message.

By default only your own fresh live messages qualify: another person mentioning you does not invoke the assistant, and a reply ending with the configured AI-answer marker is ignored. Typing an `@name` as plain text is not a mention. Set **@自己唤起助手** (`agent_mention`, enabled by default) to `false` to allow only the configured trigger prefix (`agent_prefix`, `/agent` by default). Answers to either kind of request appear under the original message with the configured AI-answer marker.

## Topics in groups

In a group, a request outside a topic starts a new assistant conversation, and its answer opens a topic under it. A later request in that topic continues the same conversation; sending `/new` with the configured trigger prefix (for example `/agent /new`) there starts the topic over. A topic group works the same way, one conversation per topic. By default requests in direct chats go to the channel's target, the main chat by default. Set **群话题各开对话** (`agent_group_topics`, enabled by default) to `false` to send manual group requests there too. This needs a hub with per-thread conversations for extension channels; an older hub sends every request to the target.

With automatic handling enabled, or with `agent_engine` set to `lark_agent` instead of the default `channel`, the assistant receives the quoted message and recent discussion: up to 30 messages in a DM, or up to 50 replies in the current topic, 50 group posts from the past three days and ten replies each from the five most recent other posts with replies. When the 12,000-character budget fills, the quote, current topic and messages closest to the request take priority. Cards contribute only their titles.

The dedicated engine allows 120 seconds to create a session or submit a follow-up and 30 seconds to read results. If creation times out without returning a session ID, the connector hands the same request ID to the channel. It does not retry a follow-up POST with an unknown outcome.

## Command hooks

List your own commands, such as `/todo` and `/note`, in **/命令 Hook** (`command_hooks`) and set **Hook 地址** (`command_hook_url`). Your own fresh message that starts with one of them then goes to that address as a JSON POST instead of to the assistant, once per message, without a model call: an `https://` address anywhere, or `http://` on the hub's machine (reached as `host.persona.internal` in the sandbox). The same guards as `/agent` apply: other people's messages, fetched history and AI answers never fire a hook. Set **Hook 签名密钥** (`command_hook_secret`) to sign every request with `X-Larkx-Signature: sha256=<HMAC-SHA256>`; **Hook 附带上文** and **Hook 附带引用** add the messages before the command and the one it replies to. The payload, the signature and the retries are in the [extension guide](../../docs/extensions/larkx.md#command-hooks).

## Automatic handling

Enable `auto_handle` to handle incoming direct messages and personal mentions in groups. The connector collects messages from the same sender in the same conversation for 15 seconds after the last arrival. Only fresh live TEXT and POST messages qualify. History, messages older than ten minutes, mentions of everyone alone and replies ending with the AI marker still reach the inbox but do not trigger a request.

| Setting | Behavior |
| --- | --- |
| `auto_bot_name` | Mention this bot first for eligible group requests. The original topic gets a fixed instruction and the AI marker. The connector checks group membership and adds the bot if absent. If it cannot resolve the bot or confirm membership, the assistant handles the request without a bot mention. Direct messages always use the assistant. |
| `auto_bot_id` | Optional numeric ID for that name. When blank, use an exact, unambiguous bot from the group roster. If the roster has no match, use the most recently seen matching sender or real @ label across chats. |
| `auto_bot_timeout_seconds` | Seconds to wait for a live response before checking the topic and group history. Default: 180; range: 30–1800. |
| `auto_test_senders` | Sender IDs treated as people even if identified as bots. For self-testing only; empty by default. |
| `auto_playbook_dir` | Directory on the assistant's machine containing `oncall.md`, `review.md` and `approval.md`. A request includes the relevant path when set; the extension does not read or upload the files. |
| `auto_active_minutes` | Skip automatic handling if you spoke in the same DM or group topic in the preceding window (default: 10 minutes). Set to 0 to check only for replies after the request. Both engines check again before accepting the request and before sending an automatic answer. |
| `auto_dm_idle_hours` | Hours before a direct chat starts a new assistant conversation. Manual and automatic requests share it. Default: 24; range: 1–168. |

A bot message in the requested topic confirms the handoff. A top-level post from that bot in the same group also confirms outstanding requests there; replies in other topics or groups do not. The connector remembers unanswered requests across restarts for the same signed-in account. A request overdue by more than twice its original timeout is discarded and logged.

At the deadline, the connector first reads the 50 most recent positions in the topic. If no bot reply is found, it checks up to 200 recent group messages for a reply the live connection missed, including a card. If you have already replied in the topic, the wait ends without a handoff; automatic messages ending in the AI marker do not count. Otherwise an unanswered request goes to the assistant in the original topic. If reading history fails, it retries at 30-second intervals up to three times before handing over.

Links matching a regular expression in `auto_review_patterns` go straight to code review. For other text, the hub's `model.complete` classifies the request; a fast, inexpensive model can be bound to `extension.larkx`. Only operations issues, code reviews and approval requests are handled. Small talk, progress checks, notifications and classification failures do not send a message.

For an eligible batch the model sees the DM peer or group name, then up to eight earlier messages with speaker labels. A promise to help with your request is not a new request for your help. In direct chats it also receives the last handled scenario and its age; a follow-up classified as none is left unanswered. Group topics ignore follow-ups for two hours after handling a request. Assistant replies are authorized only for the recorded request and end with the AI marker. The `auto` status counts batches, scenarios, bot and assistant dispatches, bot timeouts and errors for this run. A timeout increments `bot_timeouts` without counting the original batch twice. Routing logs include timings and the fallback path (`bot_timeout_assistant`), without message text.

The connector looks up the sender's chatter type before auto handling and caches known types for 24 hours. Bot and AI senders do not start requests. If the lookup fails, the connector logs the failure and treats the sender as a person for that message. Name history is not a current membership list. Outside configured alarm sources, native CARD messages do not trigger requests; review patterns apply to visible TEXT and POST content.

## Watched alarm cards

With `auto_handle` enabled, set `alarm_sources` to the exact names of alarm DMs or groups. The first poll records the current position; later polls archive new messages and handle matching cards. Polling defaults to 60 seconds (20–600). Set `alarm_skip_markers` to status labels to ignore. Repeated titles with different leading tags count as the same alarm for `alarm_dedup_minutes` (default 30), including after a restart.

The connector looks for the most recently active group whose target matches the card, allowing an explicitly truncated prefix of at least eight characters. It mentions the configured bot in a new topic and checks for a reply before falling back to the assistant there. If no group matches, the assistant answers in your chat with yourself. This protocol version cannot invoke the join button, even if `alarm_join_button` is set. Use the read-only `larkx_debug_alarm` tool with a source name to preview the five latest cards and their group matches without dispatching anything.

## Pushes

Turn on the channel's "Send notifications here too" (推送也发到这里) or "Sync main chat" (同步主对话) switch in the app to get what comes after an answer, such as a background task's report. Pushes go only to your own Feishu chat with yourself, never to a group or anybody else's chat, and end with the AI-answer marker.
