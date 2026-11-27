"""Rules for recognizing a watched card and matching its destination. No account or transport state."""
import json
import re

TARGET = re.compile(r"目标\s*[:：]\s*")


def card(message):
    text = message.get("text") or ""
    if message.get("type") != "CARD" or not text.startswith("[卡片] "):
        return None
    title, sep, body = text[5:].partition(" {")
    title = (message.get("card_title") or title).strip()
    key = re.sub(r"^(?:\[[^\]]*\]\s*)+", "", title).strip()
    if not key:
        return None
    parts = TARGET.split(key, maxsplit=1)
    return title, key, parts[-1].strip() or key, message.get("card_json") or ("{" + body if sep else "")


def matches(name, target):
    parts = TARGET.split(name, maxsplit=1)
    if len(parts) != 2:
        return False
    suffix = parts[1].strip()
    # Some group names enclose the whole description in a final bracket.
    for value in (suffix, suffix.removesuffix("]").rstrip()):
        if value == target:
            return True
        cut = re.search(r"(?:\.\.\.|…)$", value)
        if cut:
            prefix = value[:cut.start()].rstrip()
            if len(prefix) >= 8 and target.startswith(prefix):
                return True
    return False


def details(title, raw):
    """Keep display text and links, never callback payloads, IDs or structural JSON."""
    try:
        data = json.loads(raw) if isinstance(raw, str) and raw else raw
    except (ValueError, RecursionError):
        data = None
    important, lines = [title], []
    fields = {"service", "cluster", "rule", "link", "服务", "集群", "规则", "链接"}
    display = {"text", "content", "title", "href", "url", "lark_md", "plain_text", "markdown"}
    def visit(value, key="", depth=0):
        if depth > 50 or key in {"action", "actions_value", "value", "callback", "behaviors"}:
            return
        if isinstance(value, dict):
            label, field_value = value.get("key"), value.get("value")
            if isinstance(label, str) and label.lower() in fields and isinstance(field_value, str):
                important.append(label + ": " + field_value.strip())
            for k, v in value.items():
                visit(v, k, depth + 1)
        elif isinstance(value, list):
            for v in value:
                visit(v, key, depth + 1)
        elif isinstance(value, str) and value.strip():
            if key.lower() in fields:
                important.append(key + ": " + value.strip())
            elif key in {"url", "href"}:
                important.append(value.strip())
            elif key in display:
                lines.append(value.strip())
    visit(data)
    return "\n".join(dict.fromkeys(important + lines))[:3000]


def group(backend, target):
    # A full target query can exclude the very groups whose stored names are truncated.
    rows = backend.search_chats(target[:8])
    candidates = [r for r in rows if r["kind"] == "group" and matches(r["name"], target)]
    return max(candidates, key=lambda r: r["active"], default=None)


def source(backend, name):
    ids = {r["id"] for r in backend.search_chats(name) if r["name"] == name}
    if len(ids) != 1:
        return None  # Never read or act on the wrong same-name source.
    return ids.pop()
