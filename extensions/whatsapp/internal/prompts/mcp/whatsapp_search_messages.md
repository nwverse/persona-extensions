Full-text search WhatsApp messages in the local SQLite index across all chats or within one chat, newest matches first, as JSON {query, results: [{chat, message}]}. Supports both Latin full-text terms and CJK substrings. Read-only.

```json
{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Search keywords"},
    "chat": {"type": "string", "description": "Optional chat JID, phone number, or exact chat name to restrict search"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum number of matches to return (default 20)"},
    "since": {"type": "string", "description": "Only messages at or after this time (RFC 3339 or YYYY-MM-DD)"},
    "until": {"type": "string", "description": "Only messages before this time (RFC 3339 or YYYY-MM-DD)"}
  },
  "required": ["query"],
  "additionalProperties": false
}
```
