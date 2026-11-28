Get a single WhatsApp message by chat and message id from the local index, returning JSON {chat, message}. Read-only.

```json
{
  "type": "object",
  "properties": {
    "chat": {"type": "string", "description": "Chat JID, phone number, or exact chat name"},
    "id": {"type": "string", "description": "WhatsApp message id"}
  },
  "required": ["chat", "id"],
  "additionalProperties": false
}
```
