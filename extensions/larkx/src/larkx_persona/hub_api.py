"""The scoped HTTP API for the optional Lark /agent conversation."""
import json
import socket
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen


class HubUnavailable(Exception):
    """The hub could not be reached or explicitly rejected the operation."""


def last_seq(page):
    # A covered line need not produce a visible item. Unsequenced legacy rows have no safe baseline.
    if page.get("to_seq") is not None:
        return page["to_seq"]
    rows = page.get("items") or []
    return max((r["seq"] for r in rows if "seq" in r), default=None if rows else 0)


class HubAPI:
    def __init__(self, url, token):
        self.url = url.rstrip("/")
        self.token = token

    @property
    def available(self):
        return bool(self.url and self.token)

    def _call(self, path, body=None):
        req = Request(self.url + path, data=json.dumps(body).encode() if body is not None else None,
                      headers={"Authorization": "Bearer " + self.token, "Content-Type": "application/json"})
        try:
            with urlopen(req, timeout=120 if body is not None else 30) as response:
                raw = response.read()
        except HTTPError as exc:
            exc.close()
            if exc.code in (401, 403, 404, 405, 501, 503):
                raise HubUnavailable from exc
            raise
        except URLError as exc:
            # A timeout/reset after submitting a POST has an unknown outcome. Do not submit it again.
            if isinstance(exc.reason, TimeoutError):
                raise exc.reason from exc
            if isinstance(exc.reason, (ConnectionRefusedError, socket.gaierror)):
                raise HubUnavailable from exc
            raise
        return json.loads(raw) if raw else {}

    def start(self, prompt):
        session = self._call("/api/v1/assistant/conversations", {"prompt": prompt, "purpose": "lark.agent"})["session"]
        if not session.get("machine_id") or not session.get("session_id"):
            raise ValueError("missing session binding")
        return session["machine_id"], session["session_id"]

    @staticmethod
    def _session(machine, session):
        return "/api/v1/sessions/%s/%s" % (quote(machine, safe=""), quote(session, safe=""))

    def message(self, machine, session, prompt, client_msg_id):
        # The hub refuses a message to some sessions without an id: it keeps a retried POST from
        # being delivered twice, so the id is stable per request.
        self._call(self._session(machine, session) + "/messages", {"text": prompt, "client_msg_id": client_msg_id})

    def items(self, machine, session, after_seq=None):
        path = self._session(machine, session) + "/items"
        page = self._call(path if after_seq is None else path + "?after_seq=%d" % after_seq)
        rows = list(page.get("items") or [])
        # has_more points backwards. Only more_after continues a forward read.
        while page.get("more_after"):
            seq = last_seq(page)
            if seq is None or seq <= (after_seq or 0):
                raise ValueError("hub items cursor did not advance")
            after_seq = seq
            page = self._call(path + "?after_seq=%d" % seq)
            rows.extend(page.get("items") or [])
        return dict(page, items=rows)
