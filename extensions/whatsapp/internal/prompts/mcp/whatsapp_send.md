Send a WhatsApp text message as the user to a chat identified by its canonical JID, optionally quoting another message via `reply_to`. Write action: always requires confirmation on the user's phone with recipient, visibility, and full message text before sending. Returns JSON {sent, chat}.

```json
{
  "type": "object",
  "properties": {
    "chat": {"type": "string", "description": "Canonical chat JID from whatsapp_list_chats or whatsapp_list_contacts (e.g. <phone>@s.whatsapp.net or <group>@g.us)"},
    "text": {"type": "string", "description": "Message text to send (at most 4096 characters)"},
    "reply_to": {"type": "string", "description": "Optional message id in this chat to quote-reply to"}
  },
  "required": ["chat", "text"],
  "additionalProperties": false
}
```
