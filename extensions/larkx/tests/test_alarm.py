"""Alarm handling over synthetic search, history, membership and assistant transports."""
import json
import os
import sys
import tempfile
import time
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))
from larkx_persona import agent_turn, alarm, main
from larkx_persona.connector import Connector
from larkx_persona.real import RealBackend, normalize
from larkx_persona.store import Store
from larkx_persona.wire import RpcError
from test_auto import AutoHub
from test_connector import ME, Backend, message
from test_engine import FakeAPI

SOURCE, TARGET = "Example source", "sample.service:blue"
TITLE = "[high] [test] Latency - 目标：" + TARGET
GROUP = {"id": "g", "name": "[day]-[high]-[Latency - 目标: sample.ser…", "kind": "group", "active": 10}


def card(n, title=TITLE):
    body = {"service": "example", "cluster": "blue", "elements": [
        {"text": {"content": "Rule: delay"}, "url": "https://example.com/rule"},
        {"tag": "button", "text": {"content": "Example join"}, "value": {"text": "opaque callback"}}]}
    return message("card%d" % n, "source", n, msg_type="CARD",
                   text="[卡片] " + title + " " + json.dumps(body))


class AlarmBackend(Backend):
    def __init__(self):
        super().__init__()
        self.last, self.groups, self.pages = 0, [], []
        self.search_chats = mock.Mock(side_effect=lambda q: [
            {"id": "source", "name": SOURCE, "kind": "dm", "active": 0}
        ] if q == SOURCE else self.groups if q == TARGET[:8] else [])
        self.ensure_bot_member = mock.Mock()

    def chat_info(self, chat, require_position=False):
        return {"last_position": self.last if chat == "source" else 0}

    def history(self, chat, positions):
        self.pages.append((chat, positions))
        return super().history(chat, positions) if chat == "source" else []


class AlarmTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.db = os.path.join(tmp.name, "alarms.db")
        self.backend, self.hub = AlarmBackend(), AutoHub()
        self.connect()

    def connect(self):
        self.c = Connector(self.backend, Store(self.db), self.hub, suffix="test suffix")
        self.addCleanup(self.c.close)
        self.c.auto_handle = self.c._signed_in = True
        self.c._account_id = ME
        self.c._ready.set()
        self.c.alarm_sources = [SOURCE]
        self.c.auto_playbook_dir, self.c.auto_bot_name, self.c.auto_bot_id = "/books/", "Example Bot", "8001"
        self.cache = {}

    def poll(self):
        self.c._poll_alarms(ME, self.c._stop, self.cache)

    def append(self, *msgs):
        for m in msgs:
            self.backend.older[m["position"]] = m
            self.backend.last = max(m["position"], self.backend.last)

    def counts(self):
        return self.c.status({})["auto"]["alarms"]

    def test_disabled_or_empty_never_starts_poller(self):
        for enabled, sources in ((True, []), (False, [SOURCE])):
            self.c.auto_handle, self.c.alarm_sources = enabled, sources
            with mock.patch.object(self.c, "_alarm_worker") as worker:
                self.c._begin({"id": ME, "name": "Example"})
                self.poll()
                worker.assert_not_called()
        self.backend.search_chats.assert_not_called()

    def test_boot_starts_configured_worker_and_close_interrupts_sleep(self):
        self.c._begin({"id": ME, "name": "Example"})
        end = time.monotonic() + 2
        while self.c.store.alarm_position(ME, "source") is None:
            if time.monotonic() > end:
                self.fail("poller did not record baseline")
            time.sleep(.005)
        self.c.close()
        self.backend.search_chats.assert_called_once_with(SOURCE)

    def test_baseline_paging_markers_and_dedup_across_restart(self):
        self.append(card(3))
        self.poll()
        self.assertEqual(self.backend.pages, [])
        self.assertIsNone(self.c.store.message("card3"))
        self.c.alarm_skip_markers = ["[closed]"]
        self.append(card(4, "[closed] " + TITLE), dict(card(5), type="TEXT"), card(6), card(59, "[other] " + TITLE))
        self.poll()
        self.assertEqual([p for _, p in self.backend.pages], [list(range(4, 54)), list(range(54, 60))])
        self.assertEqual(self.counts(), {"detected": 1, "handled": 1, "no_group": 1, "errors": 0})
        self.assertTrue(all(self.c.store.message("card%d" % n) for n in (4, 5, 6, 59)))
        self.assertEqual(self.backend.search_chats.call_args_list.count(mock.call(SOURCE)), 1)
        self.c.close()
        self.connect()
        self.append(card(60))
        self.poll()
        self.assertEqual(self.counts()["detected"], 0)
        self.c.store.alarm_handled(ME, alarm.card(card(6))[1], time.time() - 1801)
        self.append(card(61))
        self.poll()
        self.assertEqual([r["message"]["id"] for r in self.hub.received], ["card6", "card61"])

    def test_match_requires_exact_target_or_explicit_long_truncation(self):
        for text, expected in ((TARGET, True), (TARGET + "]", True), (TARGET[:8] + "...", True),
                               (TARGET[:9] + "…]", True), (TARGET[:7] + "…", False),
                               (TARGET[:-1], False), (TARGET + ":other", False), ("sample.set…", False)):
            with self.subTest(suffix=text):
                self.assertEqual(alarm.matches("[x]-[description - 目标：" + text, TARGET), expected)
        self.assertFalse(alarm.matches(TARGET, TARGET))
        self.assertEqual(alarm.card(card(1, "[high] Plain rule"))[1:3], ("Plain rule", "Plain rule"))
        self.backend.groups = [GROUP, dict(GROUP, id="older", active=1), dict(GROUP, name="different", active=100)]
        self.assertEqual(alarm.group(self.backend, TARGET), GROUP)

    def test_new_root_mention_wait_and_live_confirmation(self):
        self.backend.groups = [GROUP]
        self.poll()
        self.append(card(1))
        self.poll()
        self.assertEqual(self.backend.sent, [("g", "@Example Bot 告警 " + TITLE +
            "：这条报警刚给我打了电话。请按 /books/oncall.md 先判断影响大盘吗、要不要处理，结论回在这个话题里。\n\ntest suffix",
            None, [{"id": "8001", "name": "Example Bot"}])])
        self.backend.ensure_bot_member.assert_called_once_with("g", "8001")
        wait = self.c.store.next_bot_wait(ME)
        self.assertEqual((wait["chat"], wait["root"], wait["scenario"]), ("g", "m_new", "alarm"))
        self.c._on_live(message("other", "g", 2, "8001", kind="group", root="unrelated"))
        self.assertIsNotNone(self.c.store.next_bot_wait(ME))
        self.c._on_live(message("confirmation", "g", 3, "8001", kind="group", root="m_new"))
        self.assertIsNone(self.c.store.next_bot_wait(ME))
        self.assertFalse(self.hub.received)

    def test_unsupported_button_dispatches_to_self_and_not_the_source(self):
        self.c.alarm_join_button = "Example join"
        self.poll()
        self.append(card(1))
        self.poll()
        handed = self.hub.received[0]["message"]
        for value in (TITLE, "example", "blue", "Rule: delay", "https://example.com/rule"):
            self.assertIn(value, handed["text"])
        self.assertNotIn("opaque callback", str(handed))
        self.assertEqual(self.backend.sent, [])
        self.c.channel_send({"ref": "answer", "reply_to": handed["id"], "text": "result", "chat": "source"})
        self.assertEqual(self.backend.sent, [("c_me", "result\n\ntest suffix", None)])
        self.c._account_id = "other"
        with self.assertRaises(RpcError):
            self.c.channel_send({"ref": "other", "reply_to": handed["id"], "text": "result"})

    def test_membership_error_hands_alarm_to_agent_without_a_bot_template(self):
        self.backend.groups = [GROUP]
        self.backend.ensure_bot_member.side_effect = OSError
        self.c.agent_engine, self.c.hub_api = "lark_agent", FakeAPI()
        with mock.patch.object(agent_turn, "POLL_SECONDS", .005):
            self.poll()
            self.append(card(1))
            self.poll()
            end = time.monotonic() + 3
            while not self.backend.sent:
                if time.monotonic() > end:
                    self.fail("agent did not reply")
                time.sleep(.005)
        self.assertEqual(self.backend.sent, [("g", "final\n\ntest suffix", None)])
        self.assertIsNone(self.c.store.next_bot_wait(ME))
        prompt = self.c.hub_api.posts[0][1]["prompt"]
        for part in (TITLE, "https://example.com/rule", "blue"):
            self.assertIn(part, prompt)
        self.assertEqual(self.counts()["errors"], 1)
        self.assertFalse(self.hub.received)

    def test_confirmed_alarm_membership_keeps_timeout_reply_in_the_new_topic(self):
        self.backend.groups = [GROUP]
        self.c.auto_bot_timeout_seconds = .12
        self.poll()
        self.append(card(1))
        self.poll()
        self.assertTrue(self.hub.got.wait(2))
        handed = self.hub.received[0]["message"]
        self.assertIn("机器人没有响应", handed["text"])
        self.c.channel_send({"ref": "answer", "reply_to": handed["id"], "text": "final"})
        self.assertEqual(self.backend.sent[-1], ("g", "final\n\ntest suffix", "m_new"))
        self.assertEqual(self.counts()["errors"], 0)

    def test_debug_reads_five_latest_cards_without_side_effects(self):
        self.backend.groups = [GROUP]
        self.append(*(card(n, "[%s] %s" % (n, TITLE)) for n in range(1, 7)), dict(card(90), type="TEXT"))
        reply = self.c.tools_call({"name": "larkx_debug_alarm", "args": {"source": SOURCE}})["text"]
        rows = reply.splitlines()
        self.assertIn("source", rows[0])
        self.assertEqual(len(rows), 6)
        for n, row in zip(range(6, 1, -1), rows[1:]):
            self.assertIn("[%s]" % n, row)
            self.assertIn(GROUP["name"] + " (g)", row)
        self.assertFalse(self.backend.sent or self.hub.received or self.c.store.message("card6"))
        self.assertIsNone(self.c.store.alarm_position(ME, "source"))
        self.backend.ensure_bot_member.assert_not_called()

    def test_raw_card_fields_survive_truncated_upstream_preview(self):
        raw = json.dumps({"padding": "z" * 3200, "service": "example", "url": "https://example.com/end",
                          "content": "x" * 4000})
        msg = normalize({"msg_type_name": "CARD", "content": "[卡片] Head (desc) " + raw[:2000],
                         "content_data": {"cardHeader": {"title": "Head"}, "jsonCard": raw}}, lambda *_: "")
        title, key, target, body = alarm.card(msg)
        self.assertEqual((title, key, target), ("Head", "Head", "Head"))
        text = alarm.details(title, body)
        self.assertEqual(len(text), 3000)
        self.assertIn("https://example.com/end", text)
        self.assertNotIn("zzz", text)

    def test_agent_without_group_uses_self_dm(self):
        self.c.agent_engine, self.c.hub_api = "lark_agent", FakeAPI()
        self.poll()
        self.append(card(1))
        with mock.patch.object(agent_turn, "POLL_SECONDS", .005):
            self.poll()
            end = time.monotonic() + 2
            while not self.backend.sent:
                if time.monotonic() > end:
                    self.fail("missing self DM")
                time.sleep(.005)
        self.assertEqual(self.backend.sent, [("c_me", "final\n\ntest suffix", None)])
        self.assertIn(TITLE, self.c.hub_api.posts[0][1]["prompt"])
        self.assertFalse(self.hub.received)

    def test_missing_or_ambiguous_source_never_reads_history(self):
        self.backend.search_chats.return_value = []
        self.backend.search_chats.side_effect = [[{"id": "a", "name": SOURCE}, {"id": "b", "name": SOURCE}],
                                                [{"id": "a", "name": SOURCE + " extra"}]]
        self.poll()
        self.poll()
        self.assertFalse(self.backend.pages)
        self.assertEqual(self.counts()["errors"], 2)
        self.assertEqual(self.cache, {})

    def test_settings_defaults_and_poll_bounds(self):
        with open(os.path.join(main.ROOT, "persona-extension.json"), encoding="utf-8") as f:
            manifest = json.load(f)
        props = manifest["configuration"]["properties"]
        defaults = {"alarm_sources": [], "alarm_skip_markers": [], "alarm_join_button": "",
                    "alarm_dedup_minutes": 30, "alarm_poll_seconds": 60}
        self.assertEqual({k: props[k]["default"] for k in defaults}, defaults)
        for seconds, bounded in ((5, 20), (60, 60), (999, 600)):
            plugin = main.Plugin(manifest, lambda *_: self.backend)
            with mock.patch.object(Connector, "start"):
                plugin.initialize({"protocol": 1, "extension": {"data_dir": os.path.dirname(self.db)},
                                   "config": {"alarm_sources": [" Example ", "Example", ""], "alarm_poll_seconds": seconds}})
            self.addCleanup(plugin.connector.close)
            self.assertEqual(plugin.connector.alarm_sources, ("Example",))
            self.assertEqual(plugin.connector.alarm_poll_seconds, bounded)

    def test_failed_history_does_not_advance_baseline(self):
        self.poll()
        self.append(card(1))
        with mock.patch.object(self.backend, "history", side_effect=OSError):
            self.poll()
        self.assertEqual(self.c.store.alarm_position(ME, "source"), 0)
        self.poll()
        self.assertEqual(self.counts(), {"detected": 1, "handled": 1, "errors": 1, "no_group": 1})


class AlarmProtocolTest(unittest.TestCase):
    def setUp(self):
        self.backend = RealBackend(main.ROOT, "", "test")
        self.backend._client = mock.Mock()
        self.backend._auth = mock.Mock()
        self.backend._auth.region.locale = "en_US"
        self.api = self.backend._client.api

    def test_search_preserves_chat_ids_activity_and_pagination(self):
        self.api.side_effect = [
            {"header": {"hasMore": True, "paginationToken": "next"}, "results": [
                {"id": "bot-id", "titleHighlighted": "<em>Example</em> source",
                 "resultMeta": {"botMeta": {"chatId": "dm"}}}]},
            {"results": [{"id": "group", "titleHighlighted": "Group", "resultMeta": {"groupChatMeta": {"lastMessageTime": 23}}},
                         {"id": "peer", "titleHighlighted": "Unknown", "resultMeta": {"userMeta": {}}},
                         {"id": "outside", "resultMeta": {"groupChatMeta": {"isMember": False}}}]}]
        self.assertEqual(self.backend.search_chats("Example"), [
            {"id": "dm", "name": SOURCE, "kind": "dm", "active": 0},
            {"id": "group", "name": "Group", "kind": "group", "active": 23}])
        first, second = (c.args[1]["header"] for c in self.api.call_args_list)
        self.assertEqual(second["paginationToken"], "next")
        self.assertEqual(second["searchSession"], first["searchSession"])
        self.assertEqual([x["type"] for x in first["searchContext"]["entityItems"]], [1, 2, 3])

    def test_exact_peer_without_chat_id_resolves_existing_dm_read_only(self):
        self.api.side_effect = [{"results": [{"id": "peer", "type": 2, "titleHighlighted": SOURCE}]},
                                {"chatterChatMap": {"peer": "dm"}}]
        self.assertEqual(alarm.source(self.backend, SOURCE), "dm")
        self.assertEqual(self.api.call_args.args, ("chats.CheckP2PChatsExistByUserRequest", {"chatterIds": ["peer"]}))

    def test_membership_adds_first_and_falls_back_to_the_profile_lookup(self):
        # Add confirmed -> done in one call.
        self.api.side_effect = [{"chat": {"id": "9000"}}]
        self.backend.ensure_bot_member("9000", "8001")
        self.assertEqual(self.api.call_args.args, ("chats.PutChatChatterRequest", {"chatId": "9000", "chatterIds": ["8001"]}))
        self.assertEqual(self.api.call_count, 1)
        # Add not confirmed, profile lookup knows the bot -> treated as a member.
        self.api.reset_mock()
        self.api.side_effect = [{}, {"chatters": {8001: {}}}]
        self.backend.ensure_bot_member("9000", "8001")
        self.assertEqual(self.api.call_count, 2)
        # Add raises and the lookup is a gateway error -> not confirmed.
        self.api.reset_mock()
        self.api.side_effect = [RuntimeError("boom"), {"cmd": 5023}]
        with self.assertRaises(ValueError):
            self.backend.ensure_bot_member("9000", "8001")
        self.assertEqual(self.api.call_count, 2)
