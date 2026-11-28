Send or remove an emoji reaction on a WhatsApp message as the user. Write action: always requires confirmation on the user's phone before sending. Pass an empty string for `emoji` to remove your existing reaction. Returns JSON {reacted, chat, id}.

```json
{
  "type": "object",
  "properties": {
    "chat": {"type": "string", "description": "Canonical chat JID"},
    "id": {"type": "string", "description": "Target message id"},
    "emoji": {"type": "string", "description": "Single emoji (e.g. 👍), or empty string to remove your reaction"}
  },
  "required": ["chat", "id", "emoji"],
  "additionalProperties": false
}
```
