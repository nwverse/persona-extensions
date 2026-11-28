List WhatsApp private chats and groups from the local real-time index, newest activity first, as JSON {chats: [{id, name, kind, phone, unread, mentions, muted, archived, members, last}], more}. Use the returned `id` (canonical JID such as `<phone>@s.whatsapp.net` or `<group>@g.us`) in other tools. Read-only.

```json
{
  "type": "object",
  "properties": {
    "limit": {"type": "integer", "minimum": 1, "maximum": 100, "description": "Maximum number of chats to return (default 30)"},
    "unread_only": {"type": "boolean", "description": "Only return chats with unread messages"},
    "include_archived": {"type": "boolean", "description": "Include archived chats (default false)"},
    "kind": {"type": "string", "enum": ["all", "dm", "group"], "description": "Filter by chat kind (default all)"}
  },
  "additionalProperties": false
}
```
