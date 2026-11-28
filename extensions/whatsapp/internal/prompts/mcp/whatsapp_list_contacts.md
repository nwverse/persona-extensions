List synced WhatsApp contacts from the local index (including normalized phone JIDs, mapped LIDs, push names, saved names, and business names), optionally filtered by a query string. Returns JSON {contacts: [{id, lid, phone, name, push_name, business_name}], more}. Read-only.

```json
{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Optional substring filter matching name, push name, phone, or JID"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 200, "description": "Maximum number of contacts to return (default 50)"}
  },
  "additionalProperties": false
}
```
