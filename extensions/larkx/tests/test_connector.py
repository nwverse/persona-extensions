"""The connector over a backend that records what it is asked: what the protocol test cannot reach (actions.do and inbox.reply
are never called there, and its fixture has no way to fail).
Run them: python3 -m unittest discover -s tests, in extensions/larkx."""
import base64
import json
import os
import re
import sqlite3
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from larkx_persona.connector import DM_UNKNOWN, KEEP_DAYS, Connector, _channel_routing, mention_request  # noqa: E402
from larkx_persona.real import normalize  # noqa: E402
from larkx_persona.store import Store  # noqa: E402
from larkx_persona.wire import RpcError  # noqa: E402

ME = "u_me"


class Expired(Exception):
    pass


class Backend:
    Expired = Expired

    def __init__(self):
        self.sent = []
        self.send_result = "m_new"
        self.send_error = None
        self.chats = {}
        self.older = {}
        self.restored = []
        self.restore_error = None
        self.poll = "waiting"
        self.finish_error = None
        self.saver = None

    def prepare(self):
        pass

    def on_credentials(self, save):
        self.saver = save

    def restore(self, saved):
        self.restored.append(saved)
        if self.restore_error:
            error, self.restore_error = self.restore_error, None
            raise error
        return {"id": ME, "name": "我的账号"}

    def login_start(self):
        return "qr-new"

    def login_qr(self):
        return "qr-same"

    def login_poll(self):
        return self.poll

    def login_finish(self):
        if self.finish_error:
            raise self.finish_error
        return {"id": ME, "name": "我的账号"}, {"cookies": {"session": "s"}}

    def logout(self):
        pass

    def listen(self, on_message, on_link, stop):
        on_link("up")
        stop.wait()

    def send(self, chat, text, root, mentions=()):
        if self.send_error:
            raise self.send_error
        self.sent.append((chat, text, root) if not mentions else (chat, text, root, mentions))
        return self.send_result

    def create_chat(self, user):
        return "c_" + user

    def own_chat(self):
        return "c_me"

    def search(self, query):
        return []

    def history(self, chat, positions):
        return [self.older[p] for p in positions if p in self.older]

    def chat_info(self, chat):
        return self.chats.get(chat, {"name": "", "last_position": 0})

    def user_name(self, user, chat):
        return {"u_a": "小明", "7001": "小明"}.get(user)

    def download(self, msg_id, chat, key, most):
        return "image/png", b"png"


class Hub:
    def __init__(self):
        self.saved = None
        self.deleted = False
        self.events = []

    def call(self, method, params):
        if method.endswith("load"):
            return {"data": self.saved} if self.saved else {}
        if method.endswith("save"):
            self.saved = params["data"]
        if method.endswith("delete"):
            self.deleted = True
            self.saved = None
        return {}

    def emit(self, event, params):
        self.events.append((event, params))

    def log(self, line):
        pass


def message(msg_id, chat, position, sender="u_a", text="你好", kind="dm", at_me=False, msg_type="TEXT", at=None, root="", parent="", media=None):
    return {"id": msg_id, "chat": chat, "chat_kind": kind, "position": position, "sender": sender, "type": msg_type, "text": text,
            "at": at or time.time() - 60 + position, "root": root, "parent": parent, "at_me": at_me, "media": media or []}


class ConnectorTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.backend = Backend()
        self.hub = Hub()
        self.c = Connector(self.backend, Store(os.path.join(self.dir.name, "x.db")), self.hub)
        self.addCleanup(self.c.close)

    def sign_in(self):
        self.c._ready.set()
        self.backend.poll = "done"
        self.assertTrue(self.c.login_step({"step": "scan"})["done"])
        self.addCleanup(lambda: None)

    def thing(self, thread):
        return next(t for t in self.c.inbox_collect({})["things"] if t["thread"] == thread)

    def test_a_dm_waits_until_the_user_answers(self):
        self.sign_in()
        self.c.on_message(message("m1", "c1", 1))
        thing = self.thing("c1")
        self.assertEqual(thing["kind"], "dm")
        self.assertEqual(thing["title"], "小明")
        self.assertNotIn("tier", thing)
        self.assertNotIn("done", thing)
        self.c.on_message(message("m2", "c1", 2, sender=ME, text="好"))
        self.assertEqual(self.thing("c1")["done"], "replied")

    def test_a_group_message_is_folded_and_a_mention_is_not(self):
        self.sign_in()
        self.backend.chats["g1"] = {"name": "项目群", "last_position": 0}
        self.c.on_message(message("m1", "g1", 1, kind="group"))
        thing = self.thing("g1")
        self.assertEqual((thing["kind"], thing["tier"]), ("group", "noise"))
        self.c.on_message(message("m2", "g1", 2, kind="group", at_me=True))
        thing = self.thing("g1")
        self.assertEqual(thing["kind"], "mention")
        self.assertNotIn("tier", thing)

    def test_every_item_carries_a_fallback_tier_and_the_users_rules(self):
        self.sign_in()
        self.backend.chats["g1"] = {"name": "项目群", "last_position": 0}
        self.c.on_message(message("m1", "c1", 1))
        self.c.on_message(message("m2", "g1", 1, kind="group"))
        self.assertEqual(self.thing("c1")["extra"]["rule_tier"], "action")
        self.assertEqual(self.thing("g1")["extra"]["rule_tier"], "noise")
        self.assertNotIn("rules", self.thing("c1"))
        self.c.on_message(message("m3", "c1", 2, sender=ME, text="好"))
        self.assertEqual(self.thing("c1")["extra"]["rule_tier"], "fyi")
        rules = Connector(self.backend, self.c.store, self.hub, "  领导的私聊都要处理 " + "x" * 5000)
        self.assertEqual(rules.inbox_collect({})["things"][0]["rules"][:9], "领导的私聊都要处理")
        self.assertEqual(len(rules.inbox_collect({})["things"][0]["rules"]), 4000)

    def test_a_mention_the_user_answered_does_not_count_again(self):
        self.sign_in()
        self.c.on_message(message("m1", "g1", 1, kind="group", at_me=True))
        self.c.on_message(message("m2", "g1", 2, kind="group", sender=ME))
        self.c.on_message(message("m3", "g1", 3, kind="group"))
        self.assertEqual(self.thing("g1")["kind"], "group")

    def test_old_chats_leave_the_inbox(self):
        self.sign_in()
        self.c.on_message(message("m1", "c1", 1, at=time.time() - 30 * 86400))
        self.assertEqual(self.c.inbox_collect({})["things"], [])

    def test_a_message_is_kept_once(self):
        self.sign_in()
        self.c.on_message(message("m1", "c1", 1))
        self.c.on_message(message("m1", "c1", 1))
        self.assertEqual(len(self.c.store.messages("c1", 0, 10)), 1)

    def test_an_action_runs_once_per_request(self):
        self.sign_in()
        args = {"chat": "c1", "text": "三点见"}
        first = self.c.actions_do({"name": "larkx_send", "args": args, "request_id": "r1"})
        again = self.c.actions_do({"name": "larkx_send", "args": args, "request_id": "r1"})
        self.assertEqual(first, again)
        self.assertEqual(self.backend.sent, [("c1", "三点见", None)])

    def test_explicit_mentions_reach_the_backend_but_a_plain_at_name_does_not(self):
        self.sign_in()
        args = {"chat": "c1", "text": "@小明 周五见", "mentions": [{"id": "7001", "name": "小明"}]}
        self.c.actions_do({"name": "larkx_send", "args": args, "request_id": "r1"})
        self.assertEqual(self.backend.sent, [("c1", "@小明 周五见", None, args["mentions"])])
        self.c.actions_do({"name": "larkx_send", "args": {"chat": "c1", "text": "@小明 明天见"}, "request_id": "r2"})
        self.assertEqual(self.backend.sent[-1], ("c1", "@小明 明天见", None))

    def test_an_unknown_or_unwritten_mention_is_refused_before_sending(self):
        self.sign_in()
        for mentions in ([{"id": "ou_wrong", "name": "小明"}], [{"id": "7001", "name": "小亮"}]):
            args = {"chat": "c1", "text": "@小明 周五见", "mentions": mentions}
            with self.assertRaises(RpcError):
                self.c.actions_do({"name": "larkx_send", "args": args, "request_id": "r1"})
        self.assertEqual(self.backend.sent, [])

    def test_an_agent_reply_mentions_only_a_known_participant(self):
        self.sign_in()
        self.c.on_message(message("m0", "g1", 1, sender="7001", text="小明", kind="group"))
        m = message("m1", "g1", 2, kind="group")
        m["sender_name"] = "小明"
        m["sender"] = ME
        self.c.on_message(m)
        with mock.patch.object(self.c, "_agent_request", return_value="提醒小明"):
            self.c.channel_send({"ref": "r1", "reply_to": "m1", "text": "@小明 请确认"})
        self.assertEqual(self.backend.sent[0][3], [{"id": "7001", "name": "小明"}])

    def test_a_send_that_may_have_gone_out_is_never_repeated(self):
        self.sign_in()
        self.backend.send_result = None
        args = {"chat": "c1", "text": "三点见"}
        for _ in range(2):
            with self.assertRaises(RpcError) as e:
                self.c.actions_do({"name": "larkx_send", "args": args, "request_id": "r1"})
            self.assertIn("不要重发", e.exception.message)
        self.assertEqual(len(self.backend.sent), 1)

    def test_a_send_that_raises_says_so_and_is_not_repeated(self):
        self.sign_in()
        self.backend.send_error = OSError("no route")
        with self.assertRaises(RpcError) as e:
            self.c.inbox_reply({"thread": "c1", "text": "好", "client_id": "k1"})
        self.assertIn("不要重发", e.exception.message)
        self.backend.send_error = None
        with self.assertRaises(RpcError):
            self.c.inbox_reply({"thread": "c1", "text": "好", "client_id": "k1"})
        self.assertEqual(self.backend.sent, [])

    def test_a_reply_goes_out_once_per_client_id(self):
        self.sign_in()
        for _ in range(2):
            self.assertEqual(self.c.inbox_reply({"thread": "c1", "text": "好", "client_id": "k1"}), {})
        self.assertEqual(self.backend.sent, [("c1", "好", None)])

    def test_a_reply_to_a_topic_stays_in_it(self):
        self.sign_in()
        self.c.on_message(message("m2", "g1", 2, kind="group", root="m1"))
        self.c.actions_do({"name": "larkx_send", "args": {"chat": "g1", "text": "好", "reply_to": "m2"}, "request_id": "r1"})
        self.assertEqual(self.backend.sent, [("g1", "好", "m1")])

    def test_larkx_messages_includes_ids_and_topic_linkage_while_inbox_context_stays_clean(self):
        self.sign_in()
        self.backend.chats["g1"] = {"name": "项目群", "last_position": 0}
        self.c.on_message(message("m1", "g1", 1, kind="group", text="周五开会吗"))
        self.c.on_message(message("m2", "g1", 2, kind="group", text="三点可以", root="m1", parent="m1"))
        self.c.on_message(message("m3", "g1", 3, kind="group", text="我也行", root="m1", parent="m2"))
        self.c.on_message(message("m4", "g1", 4, kind="group", text="附议", root="", parent="m1"))
        self.c.on_message(message("m5", "g1", 5, kind="group", text="收到", root="0", parent="0"))

        lines = self.c.tools_call({"name": "larkx_messages", "args": {"chat": "g1"}})["text"].splitlines()
        self.assertEqual(len(lines), 5)
        self.assertTrue(lines[0].startswith("[id=m1] 小明（"))
        self.assertNotIn("root=", lines[0])
        self.assertNotIn("parent=", lines[0])
        self.assertTrue(lines[1].startswith("[id=m2 root=m1] 小明（"))
        self.assertNotIn("parent=", lines[1])
        self.assertTrue(lines[2].startswith("[id=m3 root=m1 parent=m2] 小明（"))
        self.assertTrue(lines[3].startswith("[id=m4 parent=m1] 小明（"))
        self.assertNotIn("root=", lines[3])
        self.assertTrue(lines[4].startswith("[id=m5] 小明（"))
        self.assertNotIn("root=", lines[4])
        self.assertNotIn("parent=", lines[4])

        # Inbox context keeps _line(m) clean without raw message or topic ids.
        context = self.thing("g1")["context"]
        self.assertIn("周五开会吗", context)
        self.assertIn("我也行", context)
        self.assertNotIn("[id=", context)
        self.assertNotIn("root=", context)
        self.assertNotIn("parent=", context)

    def test_larkx_messages_id_flows_into_larkx_send_reply_to(self):
        self.sign_in()
        self.backend.chats["g1"] = {"name": "项目群", "last_position": 0}
        self.c.on_message(message("m1", "g1", 1, kind="group", text="周五开会吗"))
        self.c.on_message(message("m2", "g1", 2, kind="group", text="三点可以", root="m1", parent="m1"))

        text = self.c.tools_call({"name": "larkx_messages", "args": {"chat": "g1"}})["text"]
        match = re.search(r"^\[id=([^\s\]]+)[^\]]*\] [^\n]*三点可以$", text, re.MULTILINE)
        self.assertIsNotNone(match)
        target_id = match.group(1)
        self.assertEqual(target_id, "m2")

        card = self.c.actions_describe({"name": "larkx_send", "args": {"chat": "g1", "text": "收到", "reply_to": target_id}})
        self.assertIn("回复：小明 的消息「三点可以」（发在这条消息的话题里）", card["summary"])
        self.assertTrue(card["summary"].endswith("内容（全文）：\n收到"))

        self.c.actions_do({"name": "larkx_send", "args": {"chat": "g1", "text": "收到", "reply_to": target_id}, "request_id": "r_topic"})
        self.assertEqual(self.backend.sent, [("g1", "收到", "m1")])

    def test_tool_schemas_document_larkx_messages_ids_for_reply_to(self):
        tools = {t["name"]: t for t in self.c.tools_list({})["tools"]}
        self.assertIn("id", tools["larkx_messages"]["description"])
        self.assertIn("reply_to", tools["larkx_messages"]["description"])
        actions = {a["name"]: a for a in self.c.actions_list({})["tools"]}
        self.assertIn("larkx_messages", actions["larkx_send"]["input_schema"]["properties"]["reply_to"]["description"])

    def test_a_dm_to_a_person_opens_their_chat(self):
        self.sign_in()
        self.c.actions_do({"name": "larkx_send_dm", "args": {"user": "u_b", "text": "好"}, "request_id": "r1"})
        self.assertEqual(self.backend.sent, [("c_u_b", "好", None)])

    def test_nothing_is_sent_signed_out(self):
        self.c._ready.set()
        with self.assertRaises(RpcError):
            self.c.actions_do({"name": "larkx_send", "args": {"chat": "c1", "text": "好"}, "request_id": "r1"})
        self.assertEqual(self.backend.sent, [])

    def test_the_card_says_who_sees_it_and_the_whole_text(self):
        self.sign_in()
        self.backend.chats["g1"] = {"name": "项目群", "last_position": 0}
        self.c.on_message(message("m1", "g1", 1, kind="group"))
        out = self.c.actions_describe({"name": "larkx_send", "args": {"chat": "g1", "text": "周五 见"}})
        self.assertEqual(out["action"]["visible"], "群『项目群』里的所有人")
        self.assertIn("以你本人的身份", out["summary"])
        self.assertIn("周五 见", out["summary"])

    def test_an_action_without_a_request_id_is_refused(self):
        self.sign_in()
        args = {"chat": "c1", "text": "三点见"}
        for p in ({}, {"request_id": ""}, {"request_id": "  "}, {"request_id": None}):
            with self.assertRaises(RpcError) as e:
                self.c.actions_do(dict(p, name="larkx_send", args=args))
            self.assertIn("request_id", e.exception.message)
            with self.assertRaises(RpcError):
                self.c.actions_do(dict(p, name="larkx_send_dm", args={"user": "u_b", "text": "好"}))
        self.assertEqual(self.backend.sent, [])
        # Two requests are two sends, each once.
        self.c.actions_do({"name": "larkx_send", "args": args, "request_id": "r1"})
        self.c.actions_do({"name": "larkx_send", "args": args, "request_id": "r2"})
        self.assertEqual(len(self.backend.sent), 2)

    def test_the_card_shows_the_message_a_reply_quotes(self):
        self.sign_in()
        self.backend.chats["g1"] = {"name": "项目群", "last_position": 0}
        self.c.on_message(message("m1", "g1", 1, kind="group", text="周五开会吗"))
        out = self.c.actions_describe({"name": "larkx_send", "args": {"chat": "g1", "text": "开", "reply_to": "m1"}})
        self.assertIn("回复：小明 的消息「周五开会吗」（发在这条消息的话题里）", out["summary"])
        self.assertTrue(out["summary"].endswith("内容（全文）：\n开"))
        out = self.c.actions_describe({"name": "larkx_send", "args": {"chat": "g1", "text": "开", "reply_to": "m_gone"}})
        self.assertIn("这里没有它的内容", out["summary"])
        out = self.c.actions_describe({"name": "larkx_send", "args": {"chat": "g1", "text": "开"}})
        self.assertNotIn("回复：", out["summary"])

    def test_a_dm_seen_only_through_my_messages_names_no_recipient(self):
        self.sign_in()
        self.c.on_message(message("m1", "c9", 1, sender=ME, text="备忘"))
        self.assertEqual(self.c.store.chat("c9")["name"], "")
        out = self.c.actions_describe({"name": "larkx_send", "args": {"chat": "c9", "text": "好"}})
        self.assertIn("发给：%s〔私聊〕" % DM_UNKNOWN, out["summary"])
        self.assertEqual(out["action"]["visible"], "这个私聊里的对方（还不知道是谁）")
        self.assertIn(DM_UNKNOWN, self.c.tools_call({"name": "larkx_chats", "args": {}})["text"])
        # The other person's first message names it.
        self.c.on_message(message("m2", "c9", 2, text="收到"))
        out = self.c.actions_describe({"name": "larkx_send", "args": {"chat": "c9", "text": "好"}})
        self.assertIn("发给：小明〔私聊〕", out["summary"])
        self.assertEqual(out["action"]["visible"], "『小明』一个人")

    def test_a_placeholder_name_from_0_1_is_cleared(self):
        path = os.path.join(self.dir.name, "old.db")
        store = Store(path)
        self.addCleanup(store.close)
        store.touch_chat("c1", "dm", 1)
        store.name_chat("c1", "私聊")
        store.touch_chat("g1", "group", 1)
        store.name_chat("g1", "私聊")
        store = Store(path)
        self.assertEqual(store.chat("c1")["name"], "")
        self.assertEqual(store.chat("g1")["name"], "私聊")

    def test_old_messages_go_once_the_archive_has_them(self):
        self.sign_in()
        old = time.time() - (KEEP_DAYS + 1) * 86400
        self.c.on_message(message("m1", "c1", 1, at=old))
        self.c.on_message(message("m2", "c1", 2))
        first = self.c.archive_changes({"cursor": "", "limit": 20})
        self.c.on_message(message("m3", "c2", 1, at=old))
        self.c.archive_changes({"cursor": first["next"], "limit": 20})
        kept = [m["id"] for m in self.c.store.after(0, 10)]
        # m1 is old and archived; m2 is recent; m3 is old but the hub has not confirmed it yet.
        self.assertEqual(kept, ["m2", "m3"])

    def test_a_page_of_a_pruned_chat_comes_from_the_server(self):
        self.sign_in()
        self.c.store.touch_chat("c1", "dm", time.time())
        self.backend.chats["c1"] = {"name": "", "last_position": 3}
        self.backend.older = {p: message("m%d" % p, "c1", p) for p in (1, 2, 3)}
        page = self.c.thread_page({"thread": "c1", "limit": 2})
        self.assertEqual([m["id"] for m in page["messages"]], ["m2", "m3"])
        self.assertEqual(page["before"], "2")

    def test_a_page_reads_older_messages_from_the_server(self):
        self.sign_in()
        self.c.on_message(message("m5", "c1", 5))
        self.backend.older = {p: message("m%d" % p, "c1", p) for p in range(1, 5)}
        page = self.c.thread_page({"thread": "c1", "limit": 3})
        self.assertEqual([m["id"] for m in page["messages"]], ["m3", "m4", "m5"])
        self.assertEqual(page["before"], "3")
        page = self.c.thread_page({"thread": "c1", "limit": 3, "before": "3"})
        self.assertEqual([m["id"] for m in page["messages"]], ["m1", "m2"])
        self.assertNotIn("before", page)

    def test_a_page_shows_a_picture_and_not_its_placeholder(self):
        self.sign_in()
        self.c.on_message(message("m1", "c1", 1, text="[图片]", msg_type="IMAGE", media=[{"kind": "image", "key": "k"}]))
        m = self.c.thread_page({"thread": "c1", "limit": 5})["messages"][0]
        self.assertNotIn("text", m)
        self.assertEqual(self.c.thread_media({"ref": m["media"][0]["ref"]})["data"], base64.b64encode(b"png").decode())

    def test_the_archive_walks_to_the_end_and_never_repeats(self):
        self.sign_in()
        for i in range(1, 4):
            self.c.on_message(message("a%d" % i, "c%d" % i, 1))
        first = self.c.archive_changes({"cursor": "", "limit": 2})
        self.assertEqual(len(first["batches"]), 2)
        self.assertTrue(first["more"])
        rest = self.c.archive_changes({"cursor": first["next"], "limit": 2})
        self.assertEqual([b["chat"]["id"] for b in rest["batches"]], ["c3"])
        self.assertFalse(rest["more"])
        self.assertEqual(self.c.archive_changes({"cursor": rest["next"], "limit": 2})["batches"], [])

    def test_catching_up_reads_what_came_in_while_down(self):
        self.sign_in()
        self.c.on_message(message("m1", "c1", 1))
        self.backend.chats["c1"] = {"name": "小明", "last_position": 3}
        self.backend.older = {2: message("m2", "c1", 2), 3: message("m3", "c1", 3)}
        self.c.catch_up()
        self.assertEqual([m["id"] for m in self.c.store.messages("c1", 0, 10)], ["m1", "m2", "m3"])

    def test_reconnects_do_not_overlap_catch_up_and_close_waits_for_history(self):
        self.sign_in()
        self.c.on_message(message("m1", "c1", 1))
        self.backend.chats["c1"] = {"name": "小明", "last_position": 3}
        release = threading.Event()
        entered = threading.Event()

        def history(chat, positions):
            entered.set()
            self.assertTrue(release.wait(5))
            return [self.backend.older[p] for p in positions if p in self.backend.older]

        self.backend.older = {2: message("m2", "c1", 2), 3: message("m3", "c1", 3)}
        self.backend.history = history
        first = threading.Thread(target=self.c.catch_up)
        first.start()
        self.assertTrue(entered.wait(5))
        self.c._on_link("up")
        closing = threading.Thread(target=self.c.close)
        closing.start()
        self.assertTrue(closing.is_alive())
        release.set()
        first.join(5)
        self.assertFalse(first.is_alive())
        closing.join(5)
        self.assertFalse(closing.is_alive())
        with self.assertRaises(sqlite3.ProgrammingError):
            self.c.store.messages("c1", 0, 10)

    def test_sign_in_keeps_the_credentials_in_the_hub(self):
        self.sign_in()
        self.assertEqual(json.loads(base64.b64decode(self.hub.saved)), {"cookies": {"session": "s"}})
        self.assertEqual(self.c.status({})["state"], "ok")
        self.assertFalse(any(f.endswith(".json") for f in os.listdir(self.dir.name)))

    def test_the_qr_is_the_same_until_it_expires(self):
        self.c._ready.set()
        self.assertEqual(self.c.login_step({"step": ""})["url"], "qr-new")
        self.assertEqual(self.c.login_step({"step": "scan"})["url"], "qr-same")
        self.backend.poll = "expired"
        self.assertEqual(self.c.login_step({"step": "scan"})["url"], "qr-new")

    def test_a_network_failure_during_sign_in_is_said_in_words(self):
        self.c._ready.set()
        self.backend.login_start = lambda: (_ for _ in ()).throw(OSError("no route"))
        with self.assertRaises(RpcError) as e:
            self.c.login_step({"step": ""})
        self.assertIn("连不上飞书", e.exception.message)

    def test_a_poll_that_did_not_get_through_keeps_the_qr(self):
        self.c._ready.set()
        self.backend.login_poll = lambda: (_ for _ in ()).throw(OSError("no route"))
        self.assertEqual(self.c.login_step({"step": "scan"})["url"], "qr-same")

    def test_a_scan_that_did_not_sign_in_asks_again(self):
        self.c._ready.set()
        self.backend.poll = "done"
        self.backend.finish_error = Expired()
        prompt = self.c.login_step({"step": "scan"})
        self.assertEqual(prompt["kind"], "qr")
        self.assertTrue(prompt["error"])
        self.assertIsNone(self.hub.saved)

    def test_logout_forgets_the_session(self):
        self.sign_in()
        self.c.logout({})
        self.assertTrue(self.hub.deleted)
        self.assertEqual(self.c.status({})["state"], "not_signed_in")

    def test_logout_during_restore_does_not_activate_the_old_session(self):
        self.hub.saved = base64.b64encode(b'{"cookies": {"session": "s"}}').decode()
        self.c._stop.set()

        def restore(saved):
            self.backend.restored.append(saved)
            self.c.logout({})
            return {"id": ME, "name": "Synthetic account"}

        self.backend.restore = restore
        with mock.patch.object(self.backend, "listen", wraps=self.backend.listen) as listen:
            self.c._boot()
            self.assertEqual(self.c.status({})["state"], "not_signed_in")
            self.assertIsNone(self.hub.saved)
            listen.assert_not_called()

    def test_logout_cancels_a_saved_session_retry(self):
        import larkx_persona.connector as module
        self.hub.saved = base64.b64encode(b'{"cookies": {"session": "s"}}').decode()
        self.backend.restore_error = ConnectionError("synthetic network failure")
        self.hub.log = lambda line: self.c.logout({})
        self.c._stop.set()
        with mock.patch.object(module, "RESTORE_RETRY_SECONDS", 0):
            self.c._boot()
        self.assertEqual(len(self.backend.restored), 1)
        self.assertEqual(self.c.status({})["state"], "not_signed_in")
        self.assertIsNone(self.hub.saved)

    def test_logout_waits_for_an_inflight_restore_without_reactivating_it(self):
        self.hub.saved = base64.b64encode(b'{"cookies": {"session": "s"}}').decode()
        entered, release = threading.Event(), threading.Event()
        errors = []

        def restore(saved):
            entered.set()
            if not release.wait(2):
                raise TimeoutError("synthetic restore timed out")
            return {"id": ME, "name": "Synthetic account"}

        def run(action):
            try:
                action()
            except Exception as error:
                errors.append(error)

        self.backend.restore = restore
        boot = threading.Thread(target=run, args=(self.c._boot,), daemon=True)
        logout = threading.Thread(target=run, args=(lambda: self.c.logout({}),), daemon=True)
        self.c._stop.set()
        self.addCleanup(release.set)
        with mock.patch.object(self.backend, "listen", wraps=self.backend.listen) as listen:
            boot.start()
            self.assertTrue(entered.wait(2))
            logout.start()
            self.assertTrue(self.c._restore_cancel.wait(2))
            release.set()
            boot.join(2)
            logout.join(2)
            self.assertFalse(boot.is_alive())
            self.assertFalse(logout.is_alive())
            self.assertEqual(errors, [])
            self.assertEqual(self.c.status({})["state"], "not_signed_in")
            self.assertIsNone(self.hub.saved)
            listen.assert_not_called()

    def test_a_new_scan_supersedes_the_saved_session_retry(self):
        import larkx_persona.connector as module
        self.hub.saved = base64.b64encode(b'{"cookies": {"session": "s"}}').decode()
        self.backend.restore_error = ConnectionError("synthetic network failure")
        self.backend.poll = "done"
        self.c._stop.set()

        def scan(line):
            self.c._ready.set()
            self.assertTrue(self.c.login_step({"step": "scan"})["done"])

        self.hub.log = scan
        with mock.patch.object(module, "RESTORE_RETRY_SECONDS", 0):
            self.c._boot()
        self.assertEqual(len(self.backend.restored), 1)
        self.assertEqual(self.c.status({})["state"], "ok")
        self.assertFalse(self.hub.deleted)

    def test_an_expired_session_asks_to_scan_again(self):
        self.sign_in()
        self.c._on_link("expired")
        status = self.c.status({})
        self.assertEqual(status["state"], "not_signed_in")
        self.assertIn("失效", status["error"])

    def test_a_network_failure_keeps_the_session(self):
        self.hub.saved = base64.b64encode(b'{"cookies": {"session": "s"}}').decode()
        self.backend.restore_error = ConnectionError("no route")
        import larkx_persona.connector as module
        wait, module.RESTORE_RETRY_SECONDS = module.RESTORE_RETRY_SECONDS, 0
        self.addCleanup(setattr, module, "RESTORE_RETRY_SECONDS", wait)
        self.addCleanup(lambda: None)
        self.c._boot()
        self.assertEqual(len(self.backend.restored), 2)
        self.assertEqual(self.c.status({})["state"], "ok")
        self.assertFalse(self.hub.deleted)

    def test_an_expired_saved_session_is_said_and_kept_out_of_the_way(self):
        self.hub.saved = base64.b64encode(b'{"cookies": {"session": "s"}}').decode()
        self.backend.restore_error = Expired()
        self.c._boot()
        self.assertIn("失效", self.c.status({})["error"])


class NormalizeTest(unittest.TestCase):
    def key(self, msg_type, data):
        return data.get("key", "")

    def test_a_text(self):
        m = normalize({"msg_id": "1", "chat_id": "2", "chat_type": 1, "position": "7", "from_id": "3", "msg_type": 4, "msg_type_name": "TEXT",
                       "content": "你好", "create_time": "1793000000", "root_id": "0", "parent_id": "0"}, self.key)
        self.assertEqual((m["id"], m["chat"], m["chat_kind"], m["position"], m["at"], m["root"], m["parent"]),
                         ("1", "2", "dm", 7, 1793000000.0, "", ""))

    def test_milliseconds_become_seconds(self):
        self.assertEqual(normalize({"create_time": 1793000000000}, self.key)["at"], 1793000000.0)

    @staticmethod
    def prop(*fields):
        """A rich text element's property: (field number, string) pairs, as protobuf writes them."""
        out = b""
        for number, value in fields:
            raw = value.encode()
            out += bytes([number << 3 | 2, len(raw)]) + raw
        return out

    def rich(self, *elements):
        """TextContent / PostContent as protobuf_to_dict gives it: (tag, property) elements in order."""
        ids = [str(i) for i in range(1, len(elements) + 1)]
        return {"richText": {"elementIds": ids, "elements": {"dictionary": {
            i: {"tag": tag, "property": prop} for i, (tag, prop) in zip(ids, elements)}}}}

    def test_a_pasted_doc_link_keeps_its_title_and_address(self):
        # 2026-11-12: LarkAgentX's text dropped the A element whole, so only the words before the link arrived.
        url = "https://example.feishu.cn/docx/AbC123"
        data = self.rich((1, self.prop((1, "请看一下 "))), (6, self.prop((1, url), (2, "示例文档"))))
        m = normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": "请看一下 ", "content_data": data}, self.key)
        self.assertEqual(m["text"], "请看一下 示例文档 " + url)

    def test_a_link_in_the_middle_keeps_its_place_and_stays_apart_from_the_next_words(self):
        url = "https://example.com/a"
        data = self.rich((1, self.prop((1, "看"))), (6, self.prop((1, url), (3, url))), (1, self.prop((1, "，周五前回复"))),
                         (5, self.prop((1, "ou_1"), (2, "@小王"))))
        m = normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": "看，周五前回复@小王", "content_data": data}, self.key)
        self.assertEqual(m["text"], "看 " + url + " ，周五前回复@小王")

    def test_a_post_with_a_link_keeps_larkagentxs_title_line(self):
        data = self.rich((3, self.prop((1, "正文"))), (6, self.prop((1, "https://example.com/b"), (2, "链接"))))
        data["title"] = "周报"
        m = normalize({"msg_type": 2, "msg_type_name": "POST", "content": "[富文本] 周报\n正文\n", "content_data": data}, self.key)
        self.assertEqual(m["text"], "[富文本] 周报\n正文\n链接 https://example.com/b")

    def test_without_a_link_larkagentxs_text_stays(self):
        data = self.rich((1, self.prop((1, "你好"))), (10, self.prop((1, "smile"))))
        m = normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": "你好[表情]", "content_data": data}, self.key)
        self.assertEqual(m["text"], "你好[表情]")

    def test_a_malformed_link_property_does_not_break_the_message(self):
        data = self.rich((1, self.prop((1, "看 "))), (6, b"\x0a\xff\x01"))
        m = normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": "看 ", "content_data": data}, self.key)
        self.assertEqual(m["text"], "看 ")

    def test_a_link_that_renders_empty_keeps_larkagentxs_text(self):
        # LarkAgentX falls back to its text when the rich text renders as nothing; so does the rebuild.
        data = self.rich((6, self.prop((4, "x"))))
        m = normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": "原文", "content_data": data}, self.key)
        self.assertEqual(m["text"], "原文")

    def test_a_repeated_link_field_reads_as_protobuf_reads_it(self):
        # A non-repeated field written twice is its last value in protobuf.
        data = self.rich((6, self.prop((1, "https://example.com/old"), (1, "https://example.com/new"))))
        m = normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": "", "content_data": data}, self.key)
        self.assertEqual(m["text"], "https://example.com/new")

    def test_a_link_and_a_self_mention_share_one_text(self):
        # The spans follow the text with its link: a span counted in LarkAgentX's text, which has no link, would miss.
        url = "https://example.com/c"
        data = self.rich((1, self.prop((1, "看 "))), (6, self.prop((1, url), (3, url))), (5, self.prop((1, "7001"), (2, "@Example"))),
                         (1, self.prop((1, " 总结一下"))))
        m = normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": "看 @Example 总结一下", "content_data": data}, self.key)
        self.assertEqual(m["text"], "看 " + url + " @Example 总结一下")
        self.assertEqual(m["mentions"], [{"id": "7001", "start": len("看 " + url + " "), "end": len("看 " + url + " @Example"),
                                          "label": "@Example"}])
        self.assertEqual(mention_request(m["text"], m["mentions"], "7001"), "看 " + url + "  总结一下")

    def test_a_post_with_a_link_shifts_its_spans_past_the_title(self):
        url = "https://example.com/d"
        data = self.rich((5, self.prop((1, "7001"), (2, "@Example"))), (1, self.prop((1, " 看 "))), (6, self.prop((1, url))))
        data["title"] = "周报"
        m = normalize({"msg_type": 4, "msg_type_name": "POST", "content": "[富文本] 周报\n@Example 看 ", "content_data": data}, self.key)
        self.assertEqual(m["text"], "[富文本] 周报\n@Example 看 " + url)
        span = m["mentions"][0]
        self.assertEqual(m["text"][span["start"]:span["end"]], "@Example")

    def test_without_a_link_the_spans_stay_on_larkagentxs_text(self):
        data = self.rich((5, self.prop((1, "7001"), (2, "@Example"))), (1, self.prop((1, " 总结"))))
        m = normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": "@Example 总结", "content_data": data}, self.key)
        self.assertEqual((m["text"], m["mentions"]), ("@Example 总结", [{"id": "7001", "start": 0, "end": 8, "label": "@Example"}]))

    def text_of(self, data, content="原文"):
        return normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": content, "content_data": data}, self.key)["text"]

    def test_a_decoder_of_another_shape_keeps_larkagentxs_text(self):
        # Review of #570: none of these may raise; each keeps the text LarkAgentX gave.
        link = (6, self.prop((1, "https://example.com/e")))
        for dictionary in (None, [], "x", 7):
            self.assertEqual(self.text_of({"richText": {"elementIds": ["1"], "elements": {"dictionary": dictionary}}}), "原文")
        for rich in (None, [], "x", {"elements": None}, {"elements": []}, {"elements": {"dictionary": {}}, "elementIds": "12"}):
            m = normalize({"msg_type": 4, "msg_type_name": "TEXT", "content": "原文", "content_data": {"richText": rich}}, self.key)
            self.assertEqual((m["text"], m["mentions"]), ("原文", []))
        data = self.rich((1, self.prop((1, "看 "))), link)
        data["richText"]["elementIds"] = [["1"], {"k": 1}, None, "1", "2"]  # unhashable and non-string ids
        self.assertEqual(self.text_of(data), "看 https://example.com/e")
        data = self.rich(link)
        data["richText"]["elements"]["dictionary"]["3"] = None  # an element that is not a dictionary
        data["richText"]["elements"]["dictionary"]["4"] = "x"
        self.assertEqual(self.text_of(data), "https://example.com/e")
        data = self.rich((1, "看"), link)  # a property decoded as a string instead of bytes
        self.assertEqual(self.text_of(data), "https://example.com/e")
        data = self.rich((6, "https://example.com/e"))
        self.assertEqual(self.text_of(data), "原文")

    def test_a_length_past_the_end_reads_no_address(self):
        # Review of #570: a truncated href is not taken as a shorter one; with no link added LarkAgentX's text stays.
        truncated = bytes([1 << 3 | 2, 50]) + b"https://ex"
        self.assertEqual(self.text_of(self.rich((1, self.prop((1, "看 "))), (6, truncated)), "看 "), "看 ")
        fixed = bytes([2 << 3 | 1]) + b"\x00" * 3  # a fixed64 with only 3 of its 8 bytes
        self.assertEqual(self.text_of(self.rich((6, fixed)), "原文"), "原文")

    def test_an_overlong_varint_ends_the_read_at_once(self):
        # Review of #570: a run of continuation bytes is not read as one huge number.
        from larkx_persona.real import _fields
        overlong = b"\x80" * 100000 + b"\x01"
        started = time.monotonic()
        self.assertEqual(_fields(overlong), {})
        self.assertEqual(_fields(bytes([1 << 3 | 2]) + b"\xff" * 11 + b"\x01"), {})
        self.assertLess(time.monotonic() - started, 1)
        self.assertEqual(self.text_of(self.rich((1, self.prop((1, "看 "))), (6, overlong)), "看 "), "看 ")

    def test_a_linked_post_keeps_its_body_s_leading_lines(self):
        url = "https://example.com/f"
        data = self.rich((1, self.prop((1, "\n  缩进的第一行 "))), (6, self.prop((1, url))))
        data["title"] = "周报"
        m = normalize({"msg_type": 2, "msg_type_name": "POST", "content": "[富文本] 周报\n\n  缩进的第一行", "content_data": data}, self.key)
        self.assertEqual(m["text"], "[富文本] 周报\n\n  缩进的第一行 " + url)

    def test_an_empty_element_after_a_link_keeps_it_apart_from_the_next_words(self):
        data = self.rich((6, self.prop((1, "https://example.com"))), (6, self.prop((2, ""))), (1, self.prop((1, "next"))))
        self.assertEqual(self.text_of(data), "https://example.com next")

    def test_an_anchor_with_no_readable_address_keeps_larkagentxs_text(self):
        # Review of #570: replace LarkAgentX's text only when a link was really added.
        self.assertEqual(self.text_of(self.rich((1, self.prop((1, "看 "))), (6, self.prop((2, "只有标题")))), "看 "), "看 ")
        anchored = {"richText": {"elementIds": [], "anchorIds": ["9"], "elements": {"dictionary": {}}}}
        self.assertEqual(self.text_of(anchored, "看 "), "看 ")
        unreadable = self.rich((1, self.prop((1, "看 "))), (6, b"\xff\xff"))
        self.assertEqual(self.text_of(unreadable, "看 "), "看 ")

    def test_media(self):
        image = normalize({"msg_type_name": "IMAGE", "content_data": {"key": "k"}}, self.key)
        self.assertEqual(image["media"], [{"kind": "image", "key": "k"}])
        document = normalize({"msg_type_name": "FILE", "content_data": {"key": "k", "name": "a.pdf"}}, self.key)
        self.assertEqual(document["media"], [{"kind": "file", "name": "a.pdf"}])

    def test_routing_topic_id_extraction(self):
        dm = normalize({"msg_id": "10", "chat_id": "c_dm", "chat_type": 1, "root_id": "9", "scope": "chat", "anchor": ""}, self.key)
        self.assertEqual((dm["chat_kind"], dm["topic_id"]), ("dm", ""))

        reg = normalize({"msg_id": "11", "chat_id": "g1", "chat_type": 2, "root_id": "0", "scope": "chat", "anchor": ""}, self.key)
        self.assertEqual((reg["chat_kind"], reg["topic_id"]), ("group", ""))

        reply = normalize({"msg_id": "12", "chat_id": "g1", "chat_type": 2, "root_id": "11", "scope": "topic", "anchor": "11"}, self.key)
        self.assertEqual((reply["chat_kind"], reply["topic_id"]), ("group", "11"))

        topic_group_root = normalize({"msg_id": "13", "chat_id": "g_topic", "chat_type": 3, "msg_type_name": "TEXT", "root_id": "0"}, self.key)
        self.assertEqual((topic_group_root["chat_kind"], topic_group_root["topic_id"]), ("group", "13"))

        dispatched_topic_root = normalize({"msg_id": "14", "chat_id": "g_topic2", "chat_type": 2, "scope": "topic", "anchor": "14"}, self.key)
        self.assertEqual((dispatched_topic_root["chat_kind"], dispatched_topic_root["topic_id"]), ("group", "14"))

        explicit_thread = normalize({"msg_id": "15", "chat_id": "g1", "chat_type": 2, "thread_id": "omt_99", "root_id": "11"}, self.key)
        self.assertEqual((explicit_thread["chat_kind"], explicit_thread["topic_id"]), ("group", "omt_99"))

        # Regression: scope="chat" with no root ("0" or "") still honors an explicit nonempty thread_id in groups.
        chat_scope_zero_root = normalize(
            {"msg_id": "16", "chat_id": "g1", "chat_type": 2, "scope": "chat", "root_id": "0", "thread_id": "omt_99"}, self.key
        )
        self.assertEqual((chat_scope_zero_root["chat_kind"], chat_scope_zero_root["topic_id"]), ("group", "omt_99"))
        self.assertEqual(_channel_routing(chat_scope_zero_root), {"chat_id": "g1", "chat_kind": "topic", "topic_id": "omt_99"})

        chat_scope_empty_root = normalize(
            {"msg_id": "17", "chat_id": "g1", "chat_type": 2, "scope": "chat", "root_id": "", "thread_id": "omt_100"}, self.key
        )
        self.assertEqual((chat_scope_empty_root["chat_kind"], chat_scope_empty_root["topic_id"]), ("group", "omt_100"))
        self.assertEqual(_channel_routing(chat_scope_empty_root), {"chat_id": "g1", "chat_kind": "topic", "topic_id": "omt_100"})

        dm_with_thread = normalize(
            {"msg_id": "18", "chat_id": "c_dm", "chat_type": 1, "scope": "chat", "root_id": "9", "thread_id": "omt_99"}, self.key
        )
        self.assertEqual((dm_with_thread["chat_kind"], dm_with_thread["topic_id"]), ("dm", ""))
        self.assertEqual(_channel_routing(dm_with_thread), {"chat_id": "c_dm", "chat_kind": "direct"})


if __name__ == "__main__":
    unittest.main()
