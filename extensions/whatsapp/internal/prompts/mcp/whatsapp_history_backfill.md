Request older message history for a specific WhatsApp chat from the user's primary phone (on-demand history sync) and store the returned messages in the local index. Does not send any message to contacts. Returns JSON {chat, before_id, requested_count, fetched, total_in_chat, status}.

```json
{
  "type": "object",
  "properties": {
    "chat": {"type": "string", "description": "Chat JID, phone number, or exact chat name"},
    "count": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Number of older messages to request from the primary phone (default 50)"},
    "before": {"type": "string", "description": "Optional anchor message id (defaults to the oldest known message in this chat)"}
  },
  "required": ["chat"],
  "additionalProperties": false
}
```
