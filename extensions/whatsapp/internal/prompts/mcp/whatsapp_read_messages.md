Read one page of messages from a WhatsApp chat in the local index, oldest first, as JSON {chat, messages: [{id, at, from, from_id, mine, type, text, reply_to, reply_to_text, mentioned, edited, media, reactions}], next_before}. Pass `next_before` as `before` to page backward; use `since` and `until` to bound time. Read-only: does not mark messages as read.

```json
{
  "type": "object",
  "properties": {
    "chat": {"type": "string", "description": "Chat JID from whatsapp_list_chats, phone number with country code, or exact chat name"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Maximum number of messages to return (default 30)"},
    "before": {"type": "string", "description": "Return only messages older than this message id or cursor (next_before of the previous page)"},
    "since": {"type": "string", "description": "Only messages at or after this time (RFC 3339 or YYYY-MM-DD in user's timezone)"},
    "until": {"type": "string", "description": "Only messages before this time (RFC 3339 or YYYY-MM-DD in user's timezone)"}
  },
  "required": ["chat"],
  "additionalProperties": false
}
```
