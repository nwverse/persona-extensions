"""The plugin protocol around the connector: the handshake and the methods' namespaces (docs/plugins.md)."""
import json
import os
import re
import sys
import tempfile

from .connector import ID, Connector
from .hub_api import HubAPI
from .hooks import Hooks
from .store import Store
from .wire import Peer, RpcError

PROTOCOL = 1
# The package's root: the manifest and requirements.txt are there, this program in src/larkx_persona/.
ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
NAMESPACE = "connector."
CHANNEL_NAMESPACE = "channel."


class Hub:
    """What the connector needs of the hub: calls, events and a log with no body or secret in it."""

    def __init__(self, peer):
        self._peer = peer

    def call(self, method, params):
        if method == "model.complete":
            return self._peer.call(method, params, timeout=params["timeout_seconds"] + 5)
        return self._peer.call(method, params)

    def emit(self, event, params):
        self._peer.emit(event, params)

    def log(self, line):
        print("larkx: " + line, file=sys.stderr, flush=True)


class Plugin:
    def __init__(self, manifest, make_backend):
        self.manifest = manifest
        self.make_backend = make_backend
        self.peer = None
        self.connector = None

    def handle(self, method, params):
        if method == "manifest":
            return self.manifest
        if method == "initialize":
            return self.initialize(params)
        if self.connector is None:
            raise RpcError("initialize first", "not_initialized")
        if method == "ping":
            return {}
        for namespace, methods in ((NAMESPACE, self.connector.methods), (CHANNEL_NAMESPACE, self.connector.channel_methods)):
            if not method.startswith(namespace):
                continue
            name = method[len(namespace):]
            fn = methods.get(name)
            if fn is None:
                raise RpcError("unknown method " + method, "unknown_method")
            if (params.get("contribution") or ID) != ID:
                raise RpcError("no contribution " + str(params.get("contribution")), "no_contribution")
            return fn(params)
        raise RpcError("unknown method " + method, "unknown_method")

    def initialize(self, params):
        if params.get("protocol") != PROTOCOL:
            raise RpcError("this program speaks plugin protocol %d" % PROTOCOL, "wrong_protocol")
        if self.connector is None:
            info = params.get("extension") or params.get("plugin") or {}
            data_dir = (info.get("data_dir") or os.environ.get("PERSONA_EXTENSION_DATA") or os.environ.get("PERSONA_PLUGIN_DATA")
                        or tempfile.mkdtemp(prefix="larkx-"))
            os.makedirs(data_dir, mode=0o700, exist_ok=True)
            config = params.get("config") or {}
            store = Store(os.path.join(data_dir, "larkx.db"))
            backend = self.make_backend(data_dir, config.get("region") or "feishu", store)
            hub = Hub(self.peer)
            self.connector = Connector(backend, store, hub,
                                       config.get("triage_rules") or "", config.get("agent_reply_suffix") or "",
                                       config.get("agent_prefix") or "")
            hub_info = params.get("hub") or {}
            self.connector.hub_api = HubAPI(hub_info.get("url") or os.environ.get("PERSONA_HUB_URL", ""),
                                            hub_info.get("token") or os.environ.get("PERSONA_HUB_TOKEN", ""))
            self.connector.agent_engine = config.get("agent_engine", "channel")
            self.connector.inbox_triage_chats = frozenset(config.get("inbox_triage_chats") or ())
            self.connector.mention = config.get("agent_mention", True) is not False
            self.connector.topics = config.get("agent_group_topics", True) is not False
            self.connector.auto_handle = config.get("auto_handle") is True
            self.connector.auto_route_hint = (config.get("auto_route_hint") or "").strip()[:2000].rstrip()
            self.connector.auto_bot_name = (config.get("auto_bot_name") or "").strip()
            self.connector.auto_bot_id = (config.get("auto_bot_id") or "").strip()
            self.connector.auto_bot_timeout_seconds = max(30, min(1800, config.get("auto_bot_timeout_seconds", 180)))
            self.connector.auto_playbook_dir = (config.get("auto_playbook_dir") or "").strip()
            senders = config.get("auto_test_senders")
            self.connector.auto_test_senders = {s.strip() for s in senders if isinstance(s, str) and s.strip()} if isinstance(senders, list) else set()
            self.connector.auto_review_patterns = _patterns(config.get("auto_review_patterns"))
            self.connector.auto_reply_style = (config.get("auto_reply_style") or "").strip()[:1000]
            self.connector.auto_dm_idle_hours = max(1, min(168, config.get("auto_dm_idle_hours", 24)))
            self.connector.hooks = Hooks.from_config(config, params.get("secrets"), hub.log)
            self.connector.start()
        return {"protocol": PROTOCOL, "id": self.manifest["id"], "version": self.manifest["version"], "contributes": self.manifest["contributes"]}


def make_backend(data_dir, region, store):
    # Only a test sets this: an installed plugin gets a short list of the hub's variables, and this is not on it.
    if os.environ.get("LARKX_PERSONA_BACKEND") == "fixture":
        from .fixture import FixtureBackend
        return FixtureBackend()
    from .real import RealBackend
    return RealBackend(ROOT, data_dir, region, store)


def main():
    name = "persona-extension.json" if os.path.exists(os.path.join(ROOT, "persona-extension.json")) else "persona-plugin.json"
    with open(os.path.join(ROOT, name), encoding="utf-8") as f:
        manifest = json.load(f)
    stdin = open(sys.stdin.fileno(), encoding="utf-8", closefd=False)
    stdout = open(sys.stdout.fileno(), "w", encoding="utf-8", closefd=False)
    # stdout is the protocol: anything a library prints goes to the log instead.
    sys.stdout = sys.stderr
    plugin = Plugin(manifest, make_backend)
    plugin.peer = Peer(stdin, stdout, plugin.handle)
    plugin.peer.serve()
    # The hub closed stdin: let history writes finish before the data directory is cleaned up.
    if plugin.connector is not None:
        plugin.connector.close()


def _patterns(values):
    """The review patterns that compile (ASCII classes, so a link right next to Chinese text still matches); a
    broken one is skipped rather than failing the whole start."""
    out = []
    for value in values if isinstance(values, list) else []:
        try:
            out.append(re.compile(str(value), re.I | re.ASCII))
        except re.error:
            pass
    return out
