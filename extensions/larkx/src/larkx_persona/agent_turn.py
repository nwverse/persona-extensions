"""Identify one request in a hub transcript and decide when its answer is final."""

from itertools import chain

POLL_SECONDS = 3
QUIET_SECONDS = 20
AUTO_TIMEOUT = 30 * 60
OWNER_TIMEOUT = 60 * 60


def marker(request):
    return "[飞书请求 %s]" % request


def _text(item):
    value = item.get("text") or item.get("content") or ""
    if isinstance(value, dict):
        value = value.get("text")
    if isinstance(value, list):
        value = "\n".join(part["text"] for part in value if isinstance(part, dict)
                          and part.get("type") in (None, "text") and isinstance(part.get("text"), str))
    return value.strip() if isinstance(value, str) else ""


def _role(item):
    return item.get("role") or item.get("sender")


def _user_message(item):
    return _role(item) == "user" and item.get("kind") in (None, "", "text", "message", "user")


def _answer(item, marked):
    # Older marked transcripts also represent a text message with just its role and content.
    if _role(item) == "assistant" and (item.get("kind") == "text" or (marked and not item.get("kind"))):
        return _text(item)
    return ""


def observe(page, run, quiet, now):
    """Return (cursor, quiet observation, final text or None). A final empty string means no answer."""
    rows = page.get("items") or page.get("messages") or []
    tag = marker(run["request"])
    # Re-read the turn, including streaming edits to already seen items. The cursor is a hint:
    # a hub can also return a shorter window after compaction.
    end = min(run["cursor"], len(rows))
    indices = chain(range(end - 1, -1, -1), range(end, len(rows)))
    start = next((i for i in indices if _user_message(rows[i]) and _text(rows[i]).partition("\n")[0] == tag), None)
    if start is not None:
        run["location"] = "marker"
        first = start + 1
    else:
        baseline = run.get("baseline")
        if baseline is None:
            return len(rows), None, None
        rows = [item for item in rows if item.get("seq", 0) > baseline]
        if not rows:
            return 0, None, None  # An idle session may not have recorded this submit yet.
        run["location"] = "baseline"
        first = 0
    stop = next((i for i in range(first, len(rows)) if _user_message(rows[i])), len(rows))
    turn = rows[first:stop]
    texts = (_answer(item, start is not None) for item in reversed(turn))
    answer = next((text for text in texts if text), "")
    ended = any(it.get("turn_duration") is not None or it.get("kind") in ("turn_end", "interrupted", "error") for it in turn)
    if stop < len(rows):
        # The session state now belongs to someone else's turn. Its completion cannot finalize our narration.
        return stop, None, answer if ended else ""
    session = page.get("session") or page.get("conversation") or {}
    state = (session.get("state") or session.get("status") or page.get("state") or page.get("status") or "").lower()
    if state in ("working", "running", "queued") or page.get("working") or page.get("running"):
        return stop, None, None
    if ended:
        return stop, None, answer
    if state in ("done", "idle", "stopped", "interrupted", "failed"):
        return stop, None, answer
    # Without an end marker, only an unchanged terminal text qualifies. A tool after it is still work.
    if turn and _answer(turn[-1], start is not None):
        if quiet is None or quiet[0] != turn:
            quiet = turn, now
        elif now - quiet[1] >= QUIET_SECONDS:
            return stop, quiet, answer
        return stop, quiet, None
    return stop, None, None
