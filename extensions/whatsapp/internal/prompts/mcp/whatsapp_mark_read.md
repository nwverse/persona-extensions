Mark a WhatsApp chat (or specific incoming message ids in that chat) as read, sending read receipts that the sender may see. Write action: always requires confirmation on the user's phone. Returns JSON {read, marked}.

```json
{
  "type": "object",
  "properties": {
    "chat": {"type": "string", "description": "Canonical chat JID"},
    "ids": {
      "type": "array",
      "items": {"type": "string"},
      "maxItems": 50,
      "description": "Optional specific message ids to mark as read (defaults to all unread incoming messages in the chat)"
    }
  },
  "required": ["chat"],
  "additionalProperties": false
}
```
