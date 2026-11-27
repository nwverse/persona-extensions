"""Live auto handling: the real connector, store and debounce worker, with no account or model service."""
import json
import os
import queue
import sys
import tempfile
import threading
import time
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))

from test_agent import ChannelHub
from test_connector import ME, Backend, message
from larkx_persona import connector, main
from larkx_persona.connector import Connector
from larkx_persona.main import _patterns
from larkx_persona.real import RealBackend, normalize
from larkx_persona.store import Store
from larkx_persona.wire import RpcError

MR = "请看看 https://example.com/git/team/repo/merge_requests/123"
REVIEW = r"(?<![\w./@-])(?:https?://)?example\.com/git/(?:[\w.~-]+/)+merge_requests/[0-9]+\b"


class AutoHub(ChannelHub):
    def __init__(self):
        super().__init__()
        self.calls = []
        self.result = {"json": {"scenario": "oncall", "reason": "指标异常"}}
        self.finished = queue.Queue()
        self.history_events = queue.Queue()
        self.discarded = threading.Event()

    def call(self, method, params):
        if method == "model.complete":
            self.calls.append(params)
            if isinstance(self.result, Exception):
                raise self.result
            return self.result
        return super().call(method, params)

    def log(self, line):
        if line.startswith(("auto bot confirmed by", "auto bot history failed")):
            self.history_events.put(line)
        if line.startswith("auto scenario="):
            self.finished.put(line)
        if line == "auto discarded expired bot wait":
            self.discarded.set()


class AutoTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.db = os.path.join(tmp.name, "larkx.db")
        self.backend, self.hub = Backend(), AutoHub()
        self.c = Connector(self.backend, Store(self.db), self.hub, suffix="agent生成")
        self.addCleanup(self.c.close)
        self.c._ready.set()
        self.c._signed_in, self.c._account_id = True, ME
        self.c.auto_handle, self.c.auto_playbook_dir = True, "/books/"
        self.c.auto_review_patterns = _patterns([REVIEW])
        delay = mock.patch.object(connector, "AUTO_DEBOUNCE_SECONDS", 0.025)
        delay.start()
        self.addCleanup(delay.stop)

    def done(self, path):
        line = self.hub.finished.get(timeout=2)
        self.assertRegex(line, "path=%s classify_ms=[0-9]+ dispatch_ms=[0-9]+" % path)

    def live(self, n, text, chat="c", **kw):
        m = message("m%d" % n, chat, n, text=text, **kw)
        self.c._on_live(m)
        return m

    def handed(self):
        return self.hub.received[-1]["message"]

    def answer(self, mid):
        return self.c.channel_send({"ref": "r" + mid, "reply_to": mid, "text": "已处理"})

    def test_route_hint_is_appended_only_when_configured(self):
        text = "service-a 今晚有没有报警？"
        self.c.classify([text], "私聊「示例」")
        base = self.hub.calls[-1]["system"]
        label = "用户补充的分类标准（优先于上面的规则）"
        self.assertNotIn(label, base)
        self.c.auto_route_hint = "测试群里的报警归 oncall。"
        self.c.classify([text], "私聊「示例」")
        call = self.hub.calls[-1]
        self.assertTrue(call["system"].startswith(base))
        self.assertIn(label, call["system"][len(base):])
        self.assertTrue(call["system"].endswith(self.c.auto_route_hint))
        self.assertEqual(call["messages"], [{"role": "user", "content": "会话：私聊「示例」\n本次需要判断的消息：\n" + text}])

    def test_auto_reply_suppresses_only_hub_placeholders(self):
        self.live(1, MR)
        self.done("assistant")
        placeholders = (
            " (No text answer this turn)\n", "This turn stopped without a text answer: timeout",
            "No answer came out of this turn. Try again.", "That computer is no longer in Persona.",
            "（这一轮没有文字回答）", "这一轮没有文字回答就停了：超时",
            "这一轮没产出回答，原因：超时", "这台电脑已经不在 Persona 里了。",
            "（今回のやり取りにはテキストの回答がありません）", "今回のやり取りはテキストの回答なしで止まりました：タイムアウト",
            "今回は回答が出ませんでした。理由：タイムアウト", "このコンピューターは Persona から削除されたため、回答できません。",
            "（这一轮没有文字回答）\n\nagent生成",
            "This chat is gone (closed or deleted).", "这段对话不见了（被关掉或删掉了）。",
            "このチャットは見つかりません（閉じられたか削除されました）。",
        )
        with mock.patch.object(self.hub, "log") as log:
            for n, text in enumerate(placeholders, 1):
                with self.subTest(text=text):
                    result = self.c.channel_send({"ref": str(n), "reply_to": "m1", "text": text})
                    self.assertEqual(result, {})
                    self.assertEqual(self.backend.sent, [])
                    self.assertEqual(self.c.status({})["auto"]["suppressed"], n)
                    log.assert_called_with("auto reply suppressed: placeholder")
        for n, text in enumerate(("(No text answer this turn) is the old message.",
                                  "诊断结果：这一轮没有文字回答就停了：超时")):
            result = self.c.channel_send({"ref": "normal%d" % n, "reply_to": "m1", "text": text})
            self.assertEqual(result, {"message_id": "m_new"})
            self.assertEqual(self.backend.sent[-1], ("c", text + "\n\nagent生成", None))
        self.assertEqual(self.c.status({})["auto"]["suppressed"], len(placeholders))

    def test_auto_reply_drops_a_preamble_meant_for_the_user(self):
        self.live(1, MR)
        self.done("assistant")
        text = "我看完了：没有大问题。下面是要回给对方的内容：\n\n看了，没发现阻断问题，可以合。"
        self.c.channel_send({"ref": "r1", "reply_to": "m1", "text": text})
        self.assertEqual(self.backend.sent[-1], ("c", "看了，没发现阻断问题，可以合。\n\nagent生成", None))
        plain = "看了，有一个问题：缓存 key 没区分实验组。"
        self.c.channel_send({"ref": "r2", "reply_to": "m1", "text": plain})
        self.assertEqual(self.backend.sent[-1], ("c", plain + "\n\nagent生成", None))

    def test_the_handover_carries_the_reply_style(self):
        self.live(1, MR)
        self.done("assistant")
        self.assertIn("不超过 400 字", self.handed()["text"])
        self.c.auto_reply_style = "一句话回答。"
        self.live(2, MR, "c2")
        self.done("assistant")
        self.assertIn("一句话回答。", self.handed()["text"])
        self.assertNotIn("不超过 400 字", self.handed()["text"])

    def test_manual_agent_still_receives_a_placeholder(self):
        self.live(1, "/agent 看一下", sender=ME)
        self.assertTrue(self.hub.got.wait(2))
        text = "（这一轮没有文字回答）"
        result = self.c.channel_send({"ref": "r1", "reply_to": "m1", "text": text})
        self.assertEqual(result, {"message_id": "m_new"})
        self.assertEqual(self.backend.sent, [("c", text + "\n\nagent生成", "m1")])
        self.assertEqual(self.c.status({})["auto"]["suppressed"], 0)

    def test_disabled_keeps_the_manual_dm_and_reply_boundary(self):
        self.c.auto_handle = False
        self.live(1, MR)
        self.live(2, MR, "g", kind="group", at_me=True)
        self.live(3, "/agent 总结", sender=ME)
        self.assertTrue(self.hub.got.wait(2))
        self.assertEqual(self.handed().get("thread", ""), "")
        self.assertEqual(self.handed()["id"], "m3")
        self.assertEqual(self.c._auto_pending, {})
        self.assertNotIn("auto", self.c.status({}))
        self.assertEqual(self.hub.calls, [])
        with self.assertRaises(RpcError):
            self.answer("m1")

    def test_dm_rule_uses_the_book_context_and_only_authorizes_that_account_and_message(self):
        self.c.on_message(message("history", "c", 1, text="背景信息"))
        self.live(2, MR)
        self.done("assistant")
        m = self.handed()
        self.assertEqual((m["id"], m["thread"]), ("m2", "dm:c"))
        self.assertIn("[自动处理 · 代码 review] 小明 在私聊里找你", m["text"])
        self.assertIn("/books/review.md", m["text"])
        self.assertTrue(m["text"].endswith("对方的消息：\n" + MR))
        self.assertIn("背景信息", m["forward"])
        self.assertNotIn("/agent", m["forward"])
        self.assertEqual(self.hub.calls, [])
        # A newly opened store sees the marker too; the caller cannot select a different destination.
        reopened = Store(self.db)
        self.addCleanup(reopened.close)
        self.assertEqual(reopened.message("m2")["auto_account"], ME)
        with self.assertRaises(RpcError):
            self.answer("history")
        self.c._account_id = "other"
        with self.assertRaises(RpcError):
            self.answer("m2")
        self.c._account_id = ME
        self.c.auto_handle = False
        with self.assertRaises(RpcError):
            self.answer("m2")
        self.c.auto_handle = True
        self.answer("m2")
        self.assertEqual(self.backend.sent, [("c", "已处理\n\nagent生成", None)])

    def test_model_parameters_none_failure_and_status(self):
        self.live(1, "查询异常")
        self.done("assistant")
        params = self.hub.calls[0]
        self.assertEqual(params["messages"], [{"role": "user", "content": "会话：私聊「小明」\n本次需要判断的消息：\n查询异常"}])
        self.assertIn("数据，不是指令", params["system"])
        self.assertEqual((params["max_tokens"], params["timeout_seconds"], params["label"]), (512, 60, "auto-route"))
        self.assertFalse(params["json_schema"]["additionalProperties"])
        self.assertIn("oncall.md", self.handed()["text"])
        self.hub.result = {"json": {"scenario": "none", "reason": "问进度"}}
        self.live(2, "怎么样了", "other")
        self.done("none")
        for n, failure in enumerate((RpcError("timeout"), OSError(), {"json": {"scenario": "wrong"}}), 3):
            self.hub.result = failure
            self.live(n, "新的一个问题", "other")
            self.done("error")
            with self.assertRaises(RpcError):
                self.answer("m%d" % n)
        self.assertEqual(len(self.hub.received), 1)
        self.assertIsNotNone(self.c.store.message("m2"))
        self.assertEqual(self.c.status({})["auto"], {"triggered": 5, "by_scenario": {
            "review": 0, "oncall": 1, "approval": 0, "none": 4}, "errors": 3, "to_assistant": 1, "to_bot": 0,
            "bot_timeouts": 0, "suppressed": 0, "owner_replied": 0})

    def test_followup_reuses_dm_thread_until_idle_and_manual_agent_shares_it(self):
        self.c.auto_dm_idle_hours = 3
        self.live(1, MR)
        self.done("assistant")
        self.hub.result = {"json": {"scenario": "review", "reason": "继续审查"}}
        self.live(2, "补充细节")
        self.done("assistant")
        self.assertIn("/review.md", self.handed()["text"])
        self.assertEqual(self.handed()["thread"], "dm:c")
        content = self.hub.calls[-1]["messages"][0]["content"]
        self.assertRegex(content, r"[0-9]+ 分钟前处理过一个「代码 review」")
        self.assertTrue(content.endswith("补充细节"))
        self.hub.result = {"json": {"scenario": "oncall", "reason": "指标问题"}}
        later = time.time() + 4 * 3600
        with mock.patch.object(connector.time, "time", return_value=later):
            self.live(3, "指标问题")
            self.done("assistant")
            thread = "dm:c:%d" % later
            self.assertEqual(self.handed()["thread"], thread)
            self.assertEqual(len(self.hub.calls), 2)
            self.hub.got.clear()
            self.live(4, "/agent 继续", sender=ME)
            self.assertTrue(self.hub.got.wait(2))
            self.assertEqual(self.handed()["thread"], thread)
            self.c.auto_active_minutes = 0  # No lookback; the manual and auto turns can share the thread.
            self.live(5, "继续")
            self.done("assistant")
            self.assertEqual(self.handed()["thread"], thread)

    def test_dm_thanks_does_not_dispatch_or_change_last_handled(self):
        self.live(1, MR)
        self.done("assistant")
        previous = self.c.store.auto_topic(ME, "dm:c")
        self.hub.result = {"json": {"scenario": "none", "reason": "致谢"}}
        self.live(2, "谢谢！")
        self.done("none")
        self.assertIn("代码 review", self.hub.calls[-1]["messages"][0]["content"])
        self.assertEqual(len(self.hub.received), 1)
        self.assertEqual(previous, self.c.store.auto_topic(ME, "dm:c"))
        with self.assertRaises(RpcError):
            self.answer("m2")

    def test_bot_senders_skip_both_paths_except_test_ids(self):
        with mock.patch.object(self.backend, "is_bot", return_value=True), mock.patch.object(self.hub, "log", wraps=self.hub.log) as log:
            self.live(1, MR)
            self.live(2, MR, "g", kind="group", at_me=True)
            self.assertEqual(log.call_args_list.count(mock.call("auto skipped: bot sender")), 2)
            self.assertFalse(self.c._auto_pending)
            self.c.auto_test_senders = {"test-bot"}
            self.c._on_live(dict(message("test", "dm", 1, sender="test-bot", text=MR), sender_bot=True))
            self.done("assistant")
            self.assertEqual([r["message"]["id"] for r in self.hub.received], ["test"])

    def test_sender_lookup_error_logs_and_routes_as_human(self):
        with mock.patch.object(self.backend, "is_bot", side_effect=OSError), mock.patch.object(self.hub, "log", wraps=self.hub.log) as log:
            self.live(1, MR)
            self.done("assistant")
            log.assert_any_call("auto sender lookup failed: OSError")

    def test_group_bot_is_mentioned_once_and_never_sees_copied_text(self):
        self.c.auto_bot_name = "小明"
        self.c.on_message(message("old", "g", 1, sender="7001", kind="group"))
        # Use a different display name from the bot for the requester.
        self.c.store.set_name("7100", "同事")
        m = self.live(2, MR, "g", kind="group", at_me=True, sender="7100")
        self.done("bot")
        self.c._on_live(dict(m))
        self.live(3, "补充", "g", kind="group", at_me=True, root="m2", sender="7100")
        self.done("none")
        self.assertEqual(self.backend.sent, [("g", "@小明 请 review 这个话题里的 MR，按 /books/review.md 处理，结论回在这个话题里。\n\nagent生成",
                                             "m2", [{"id": "7001", "name": "小明"}])])
        self.assertEqual(self.hub.received, [])
        self.assertEqual(self.hub.calls, [])
        self.assertEqual(self.c.status({})["auto"]["to_bot"], 1)
        self.assertEqual(self.c.store.claim_send("auto:m2"), (False, "m_new"))
        with self.assertRaises(RpcError):
            self.answer("m2")

    def test_auto_handoff_uses_current_group_roster_and_topic_routing(self):
        name = self.c.auto_bot_name = "Example Bot"
        self.c.store.add_message(message("old", "another", 1, sender="8000"), name, False)
        bot = {"id": "8001", "name": name, "real_name": name, "kind": "bot"}
        self.backend.members["g"] = [bot]
        self.live(1, MR, "g", kind="group", at_me=True, root="topic")
        self.done("bot")
        self.assertEqual(self.backend.sent[0][3], [{"id": "8001", "name": name}])
        self.backend.members["other"] = [bot, dict(bot, id="8002")]
        self.live(2, MR, "other", kind="group", at_me=True, root="topic")
        self.done("assistant")
        m = self.handed()
        self.assertEqual((m["chat_id"], m["chat_kind"], m["topic_id"], m["thread"]),
                         ("other", "topic", "topic", "other:topic"))
        self.assertIn("Example Bot[BOT](id=8002)", m["forward"])
        self.assertEqual(len(self.backend.sent), 1)

    def test_empty_sender_and_mention_ids_do_not_hide_a_numeric_bot(self):
        store = self.c.store
        name = "Example Bot"
        store.add_message(message("first", "g", 1, sender="8001", at=1), name, False)
        store.add_message(message("empty", "g", 2, sender="", at=2), name, False)
        store.add_message(dict(message("mention", "g", 3, at=3),
                               mentions=[{"label": "@" + name, "id": ""}]), "Example", False)
        self.assertEqual(store.bot_id(name), "8001")

    def test_group_bot_lookup_by_mention_and_empty_playbook(self):
        self.c.auto_bot_name, self.c.auto_playbook_dir = "运维助手", ""
        self.c.on_message(dict(message("old", "g", 1, kind="group"), mentions=[{"id": "888", "label": "@运维助手"}]))
        self.hub.result = {"json": {"scenario": "approval", "reason": "申请"}}
        m = dict(message("m2", "g", 2, kind="group", root="topic", text="请批权限"), mentions=[{"id": ME}])
        self.c._on_live(m)
        self.done("bot")
        self.assertEqual(self.backend.sent[0], ("g", "@运维助手 请处理这个话题里的审批，结果回在这个话题里。\n\nagent生成",
                                               "topic", [{"id": "888", "name": "运维助手"}]))

    def group_request(self, root="topic", n=1):
        self.live(n, MR, "g", kind="group", at_me=True, root=root)
        self.done("bot")

    def set_bot(self):
        self.c.auto_bot_name = "Example Bot"
        self.c.auto_bot_id = "8001"

    def test_group_checks_membership_before_sending_a_template(self):
        self.set_bot()
        real = RealBackend(main.ROOT, "", "test")
        real._client = mock.Mock()
        check, add = "chats.PullChatChattersByIDsRequest", "chats.PutChatChatterRequest"
        for i, (responses, expected, ready) in enumerate((
                ([{"chat": {"id": "g"}}], [add], True),
                ([{}, {"chatters": {"8001": {}}}], [add, check], True),
                ([{"chat": {"id": "other"}}, {}], [add, check], False),
                ([{}, {"cmd": 1}], [add, check], False),
                ([{}, {"chatters": ["8001"]}], [add, check], False)), 1):
            with self.subTest(i=i):
                real._client.api.reset_mock()
                real._client.api.side_effect = responses
                calls_at_send = []
                def send(*args, **kwargs):
                    calls_at_send.extend(c.args[0] for c in real._client.api.call_args_list)
                    return "template"
                with mock.patch.object(self.backend, "ensure_bot_member", real.ensure_bot_member), \
                        mock.patch.object(self.backend, "send", side_effect=send) as sent, \
                        mock.patch.object(self.hub, "log", wraps=self.hub.log) as log:
                    root = "topic%d" % i
                    self.live(i, MR, "g", kind="group", at_me=True, root=root)
                    self.done("bot" if ready else "assistant")
                    self.assertEqual([c.args[0] for c in real._client.api.call_args_list], expected)
                    if ready:
                        sent.assert_called_once()
                        self.assertEqual(calls_at_send, expected)
                        self.c._on_live(message("confirm%d" % i, "g", i + 1, sender="8001", kind="group", root=root))
                    else:
                        sent.assert_not_called()
                        log.assert_any_call("auto bot not in chat: add failed")
                        self.assertEqual(self.handed()["thread"], "g:" + root)
                    self.assertIsNone(self.c.store.next_bot_wait(ME))

    def test_timeout_skips_an_owner_reply_found_in_topic_history(self):
        self.set_bot()
        self.c.auto_bot_timeout_seconds = .1
        reply = message("reply", "g", 2, sender=ME, kind="group", root="topic")
        cleared = threading.Event()
        forget = self.c.store.forget_bot_wait
        def clear(item):
            forget(item)
            cleared.set()
        with mock.patch.object(self.backend, "thread_replies", return_value=[reply]), \
                mock.patch.object(self.c.store, "forget_bot_wait", side_effect=clear), \
                mock.patch.object(self.hub, "log", wraps=self.hub.log) as log:
            self.group_request()
            self.assertTrue(cleared.wait(2))
            log.assert_any_call("auto fallback skipped: owner replied")
        self.assertIsNone(self.c.store.next_bot_wait(ME))
        self.assertFalse(self.hub.received)
        counts = self.c.status({})["auto"]
        self.assertEqual([counts[k] for k in ("owner_replied", "to_assistant", "errors")], [1, 0, 0])

    def test_timeout_ignores_ai_echo_and_owner_replies_elsewhere(self):
        self.set_bot()
        self.c.auto_bot_timeout_seconds = .1
        self.group_request()
        self.live(2, "done " + self.c.suffix, "g", kind="group", sender=ME, root="topic")
        self.live(3, "done", "g", kind="group", sender=ME, root="other")
        self.live(4, "done", "other", kind="group", sender=ME, root="topic")
        self.done("bot_timeout_assistant")
        self.assertEqual(self.handed()["thread"], "g:topic")
        self.assertEqual(self.c.status({})["auto"]["owner_replied"], 0)

    def test_lookup_uses_the_latest_numeric_sender_or_mention_across_chats(self):
        self.c.auto_bot_name = "Example Bot"
        self.c.store.set_name("8002", "Example Bot")
        self.c.on_message(message("old", "elsewhere", 1, sender="8002", at=10))
        def mention(mid, uid, at):
            self.c.on_message(dict(message(mid, "elsewhere", 2, at=at),
                                   mentions=[{"id": uid, "label": "@Example Bot"}]))
        mention("new", "8003", 10)  # Same timestamp: later row wins.
        mention("backfill", "8004", 9)
        mention("invalid", "8x05", 11)
        self.group_request()
        self.assertEqual(self.backend.sent[0][3], [{"id": "8003", "name": "Example Bot"}])
        self.c.on_message(message("latest", "elsewhere", 3, sender="8002", at=12))
        self.group_request(root="another", n=2)
        self.assertEqual(self.backend.sent[1][3][0]["id"], "8002")

    def test_configured_id_takes_precedence_and_dms_still_use_the_assistant(self):
        self.set_bot()
        self.c.store.set_name("8002", "Example Bot")
        self.c.on_message(message("old", "g", 1, sender="8002", kind="group"))
        self.group_request()
        self.assertEqual(self.backend.sent[0][3][0]["id"], "8001")
        self.live(2, MR)
        self.done("assistant")
        self.assertEqual(self.handed()["thread"], "dm:c")
        self.assertEqual(len(self.backend.sent), 1)
        self.c.auto_bot_id = "８００１"
        self.live(3, MR, "g", kind="group", at_me=True)
        self.done("assistant")
        self.assertEqual(self.handed()["thread"], "g:m3")

    def test_bot_responses_confirm_by_topic_message_id_or_unthreaded_post(self):
        self.set_bot()
        for i, (root, reply_id, reply_root) in enumerate((("t1", "r1", "t1"), ("t2", "t2", "outer"), ("t3", "r3", "")), 1):
            with self.subTest(root=root):
                self.group_request(root, i)
                self.c._on_live(dict(message(reply_id, "g", 10 + i, sender="8001", root=reply_root, kind="group"),
                                     sender_bot=True))
                self.assertIsNone(self.c.store.next_bot_wait(ME))
        self.assertEqual(self.hub.received, [])
        self.assertEqual(self.c.status({})["auto"]["bot_timeouts"], 0)

    def test_response_during_send_is_not_lost(self):
        self.set_bot()
        send = self.backend.send
        def immediate(*args, **kwargs):
            self.live(2, "done", "g", sender="8001", root="topic", kind="group")
            return send(*args, **kwargs)
        with mock.patch.object(self.backend, "send", side_effect=immediate):
            self.group_request()
        self.assertIsNone(self.c.store.next_bot_wait(ME))
        self.assertEqual(self.hub.received, [])

    def test_unrelated_live_replies_and_passive_backfill_do_not_confirm(self):
        self.set_bot()
        self.c.auto_bot_timeout_seconds = 0.25
        self.live(1, MR, "g", kind="group", at_me=True, root="topic")
        self.live(2, "more context", "g", kind="group", at_me=True, root="topic")
        self.done("bot")
        self.live(3, "reply", "elsewhere", sender="8001", root="topic", kind="group")
        self.live(4, "reply", "g", sender="8001", root="other", kind="group")
        self.live(5, "reply", "g", sender="8002", root="topic", kind="group")
        self.c.on_message(message("history", "g", 6, sender="8001", root="topic", kind="group"))
        self.done("bot_timeout_assistant")
        self.assertEqual((self.handed()["thread"], self.handed()["id"]), ("g:topic", "m2"))
        self.assertIn("群里的机器人没有响应，由你直接处理", self.handed()["text"])
        self.assertTrue(self.handed()["text"].endswith(MR + "\nmore context"))
        self.answer("m2")
        self.assertEqual(self.backend.sent[-1][2], "topic")
        with self.assertRaises(RpcError):
            self.answer("m1")
        counts = self.c.status({})["auto"]
        self.assertEqual([counts[k] for k in ("triggered", "to_bot", "bot_timeouts", "to_assistant")], [1, 1, 1, 1])
        self.assertIsNone(self.c.store.next_bot_wait(ME))

    def test_thread_card_confirms_without_reading_chat_history(self):
        self.set_bot()
        self.c.auto_bot_timeout_seconds = 0.1
        page = [message("card", "g", 0, sender="8001", kind="group", root="topic", msg_type="CARD", at=time.time()),
                message("later", "g", 0, kind="group", root="topic")]
        with mock.patch.object(self.backend, "thread_replies", return_value=page) as read, \
                mock.patch.object(self.backend, "history") as history:
            self.group_request()
            self.assertEqual(self.hub.history_events.get(timeout=2), "auto bot confirmed by thread")
            with self.c._session_lock:
                self.assertIsNotNone(self.c.store.message("later"))
            read.assert_called_once_with("g", "topic")
            history.assert_not_called()
        self.assertEqual(self.c.store.message("card")["type"], "CARD")
        self.assertIsNone(self.c.store.next_bot_wait(ME))
        self.assertFalse(self.hub.received)

    def test_thread_with_only_a_peer_reply_falls_back(self):
        self.set_bot()
        self.c.auto_bot_timeout_seconds = 0.1
        with mock.patch.object(self.backend, "thread_replies", return_value=[
                message("peer", "g", 0, kind="group", root="topic"),
                message("old-bot-card", "g", 0, kind="group", root="topic", sender="8001", at=time.time() - 3600)]):
            self.group_request()
            self.done("bot_timeout_assistant")
        self.assertIsNotNone(self.c.store.message("peer"))
        self.assertEqual(self.handed()["thread"], "g:topic")

    def test_failed_thread_read_retries_then_confirms(self):
        self.set_bot()
        card = message("card", "g", 0, kind="group", sender="8001", root="topic", msg_type="CARD", at=time.time())
        with mock.patch.object(self.backend, "thread_replies", side_effect=[OSError(), [card]]) as read, \
                mock.patch.object(connector.time, "time", return_value=time.time()) as clock:
            self.group_request()
            for attempt in range(2):
                with self.c._auto_changed:
                    wait = self.c.store.next_bot_wait(ME)
                    if attempt:
                        self.assertEqual(wait["deadline"], clock.return_value + 30)
                    clock.return_value = wait["deadline"]
                    self.c._auto_changed.notify_all()
                event = self.hub.history_events.get(timeout=2)
                self.assertIn("retry 1/3" if not attempt else "confirmed by thread", event)
        self.assertEqual(read.call_count, 2)
        self.assertFalse(self.hub.received)

    def test_debug_thread_is_read_only_and_truncates_each_preview(self):
        args = {"name": "larkx_debug_thread", "args": {"chat": "g", "root": "topic"}}
        card = message("card", "g", 0, kind="group", root="topic", sender="8001", text="a" * 75, msg_type="CARD", at=1)
        with mock.patch.object(self.backend, "thread_replies", return_value=[card]) as read:
            result = self.c.tools_call(args)
            self.assertEqual(result["text"], "8001  CARD  1970-01-01T00:00:01Z  " + "a" * 60)
            self.c._signed_in = False
            with self.assertRaises(RpcError):
                self.c.tools_call(args)
            read.assert_called_once_with("g", "topic")
        self.assertFalse(self.backend.sent)
        self.assertIsNone(self.c.store.message("card"))

    def test_history_card_confirms_without_a_push_and_pages_are_stored(self):
        self.set_bot()
        self.c.auto_bot_timeout_seconds = 0.25
        self.backend.chats["g"] = {"last_position": 250}
        self.backend.older[51] = message("early", "g", 51, kind="group")
        self.backend.older[250] = message("card", "g", 250, kind="group", sender="8001", root="topic", msg_type="CARD")
        with mock.patch.object(self.backend, "history", wraps=self.backend.history) as history:
            self.group_request()
            self.assertEqual(self.hub.history_events.get(timeout=2), "auto bot confirmed by history")
        self.assertEqual(history.call_args_list, [mock.call("g", list(range(p, p + 50))) for p in (51, 101, 151, 201)])
        self.assertIsNotNone(self.c.store.message("early"))
        self.assertEqual(self.c.store.message("card")["type"], "CARD")
        self.assertIsNone(self.c.store.next_bot_wait(ME))
        self.assertEqual(self.hub.received, [])

    def test_unrelated_history_is_stored_before_fallback(self):
        self.set_bot()
        self.c.auto_bot_timeout_seconds = 0.25
        self.backend.chats["g"] = {"last_position": 15}
        self.backend.older[13] = message("other-topic", "g", 13, kind="group", sender="8001", root="other")
        self.backend.older[15] = message("other-sender", "g", 15, kind="group", sender="8002", root="topic")
        with mock.patch.object(self.backend, "history", wraps=self.backend.history) as history:
            self.group_request(n=12)
            self.done("bot_timeout_assistant")
        self.assertEqual(history.call_args_list, [mock.call("g", [12, 13, 14, 15]),
                                                 mock.call("g", list(range(1, 16)))])
        self.assertIsNotNone(self.c.store.message("other-topic"))
        self.assertIsNotNone(self.c.store.message("other-sender"))
        self.assertEqual(len(self.hub.received), 1)
        self.assertIn("必须给出最终回复", self.handed()["text"])
        self.assertIn("只回复一句'机器人已在处理'", self.handed()["text"])

    def test_failed_history_retries_three_times_at_thirty_second_intervals(self):
        self.set_bot()
        self.backend.chats["g"] = {"last_position": 80}
        with mock.patch.object(connector.time, "time", return_value=time.time()) as clock, \
                mock.patch.object(self.backend, "history", side_effect=OSError("offline")) as history:
            self.group_request(n=0)  # No trigger position: start at last_position - 50.
            for attempt in range(4):
                with self.c._auto_changed:
                    clock.return_value = self.c.store.next_bot_wait(ME)["deadline"]
                    self.c._auto_changed.notify_all()
                event = self.hub.history_events.get(timeout=2)
                self.assertIn("OSError", event)
                if attempt < 3:
                    self.assertEqual(self.c.store.next_bot_wait(ME)["deadline"], clock.return_value + 30)
                    self.assertEqual(self.hub.received, [])
                else:
                    self.assertIn("falling back after 3 retries", event)
                if attempt == 0:
                    self.restart()  # The next sign-in must not reset the retry budget.
            self.done("bot_timeout_assistant")
        self.assertEqual(history.call_args_list, [mock.call("g", list(range(30, 80)))] * 4 +
                         [mock.call("g", list(range(31, 81)))])
        self.assertEqual(len(self.hub.received), 1)
        self.assertIsNone(self.c.store.next_bot_wait(ME))

    def test_live_reply_during_history_cancels_only_its_own_fallback(self):
        self.set_bot()
        self.backend.chats["g"] = {"last_position": 3}

        def history(chat, positions):
            if positions[0] == 1:
                self.live(3, "working", chat, kind="group", sender="8001", root="first", msg_type="CARD")
            return []

        with mock.patch.object(self.backend, "history", side_effect=history), \
                mock.patch.object(connector.time, "time", return_value=time.time()) as clock:
            self.group_request("first", 1)
            clock.return_value += 1
            self.group_request("second", 2)
            with self.c._auto_changed:
                clock.return_value += self.c.auto_bot_timeout_seconds
                self.c._auto_changed.notify_all()
            self.done("bot_timeout_assistant")
            self.assertEqual([r["message"]["thread"] for r in self.hub.received], ["g:second"])
            self.assertIsNone(self.c.store.next_bot_wait(ME))

    def restart(self, account=ME):
        self.c.close()
        self.c = Connector(self.backend, Store(self.db), self.hub)
        self.addCleanup(self.c.close)
        self.c.auto_handle = True
        self.c._ready.set()
        self.c._begin({"id": account, "name": "Example"})

    def test_restart_resumes_only_the_signed_in_accounts_unanswered_request(self):
        self.set_bot()
        self.group_request()
        deadline = self.c.store.next_bot_wait(ME)["deadline"]
        self.c.close()
        with mock.patch.object(connector.time, "time", return_value=deadline + 1):
            self.restart("other")
            self.restart()
            self.done("bot_timeout_assistant")
        self.assertEqual(len(self.hub.received), 1)
        self.assertEqual(self.handed()["thread"], "g:topic")
        self.assertEqual(len(self.backend.sent), 1)
        self.assertEqual(self.c.store.message("m1")["auto_account"], ME)

    def test_restart_waits_for_the_original_future_deadline_without_new_messages(self):
        self.set_bot()
        self.group_request()
        deadline = self.c.store.next_bot_wait(ME)["deadline"]
        self.c.close()
        # Advance wall time without changing how long condition waits take.
        clock = time.time
        offset = deadline - clock() - 0.35
        with mock.patch.object(connector.time, "time", side_effect=lambda: clock() + offset):
            self.restart()
            self.done("bot_timeout_assistant")
            self.assertGreaterEqual(time.time(), deadline)
        self.assertEqual(self.handed()["id"], "m1")

    def test_restart_discards_waits_overdue_by_more_than_twice_the_original_timeout(self):
        self.set_bot()
        self.c.auto_bot_timeout_seconds = 30
        self.group_request()
        deadline = self.c.store.next_bot_wait(ME)["deadline"]
        self.c.close()
        with mock.patch.object(connector.time, "time", return_value=deadline + 61):
            self.restart()
            self.assertTrue(self.hub.discarded.wait(2))
        self.assertIsNone(self.c.store.next_bot_wait(ME))
        self.assertEqual(self.hub.received, [])
        self.assertEqual(self.c.status({})["auto"]["bot_timeouts"], 0)

    def test_missing_bot_falls_back_to_group_topic_even_when_manual_topics_off(self):
        self.c.auto_bot_name, self.c.topics = "运维助手", False
        self.backend.chats["g"] = {"name": "项目群"}
        self.live(1, MR, "g", kind="group", at_me=True, root="topic")
        self.done("assistant")
        self.assertEqual(self.handed()["thread"], "g:topic")
        self.assertIn("群「项目群」", self.handed()["text"])
        self.answer("m1")
        self.assertEqual(self.backend.sent[0][2], "topic")
        self.live(2, "请继续", "g", kind="group", at_me=True, root="topic")
        self.done("none")
        self.assertEqual(len(self.hub.received), 1)
        self.assertEqual(self.hub.calls, [])

    def test_ineligible_messages_and_catch_up_still_go_to_the_inbox(self):
        self.live(1, MR, sender=ME)
        self.live(2, MR + "\n\nagent生成 ")
        self.live(3, MR, at=time.time() - connector.AGENT_FRESH_SECONDS - 1)
        self.live(4, MR, msg_type="IMAGE")
        self.live(5, "@Example " + MR, "g", kind="group")
        m = message("m6", "g", 6, kind="group", text="@所有人 " + MR, at_me=True)
        self.c._on_live(dict(m, mentions=[{"id": "all", "label": "@所有人"}]))
        self.c._on_live(dict(message("m7", "c", 7, text=MR), sender_bot=True))
        self.backend.older[8] = message("m8", "c", 8, text=MR)
        self.backend.chats["c"] = {"last_position": 8}
        self.c.catch_up()
        self.assertIsNotNone(self.c.store.message("m8"))
        self.assertEqual(self.c._auto_pending, {})
        self.assertEqual((self.hub.calls, self.hub.received, self.backend.sent), ([], [], []))

    def test_burst_is_sorted_and_images_use_their_own_message_ids(self):
        with mock.patch.object(self.backend, "download", wraps=self.backend.download) as download:
            self.live(2, "第二条")
            self.live(1, MR, msg_type="POST", media=[{"kind": "image", "key": "pic"}])
            self.done("assistant")
            m = self.handed()
            self.assertEqual((len(self.hub.received), m["id"]), (1, "m2"))
            self.assertTrue(m["text"].endswith(MR + "\n第二条"))
            self.assertEqual(m["images"][0]["mime"], "image/png")
            download.assert_called_once_with("m1", "c", "pic", connector.CHANNEL_IMAGE_MOST)
        with self.assertRaises(RpcError):
            self.answer("m1")

    def test_different_senders_messages_in_a_topic_are_not_combined(self):
        self.hub.result = {"json": {"scenario": "none", "reason": ""}}
        self.backend.chats["g"] = {"name": "Example review"}
        self.live(1, "在吗", "g", kind="group", at_me=True, root="t")
        self.live(2, MR, "g", kind="group", at_me=True, root="t", sender="second")
        self.done("none")
        self.done("assistant")
        self.assertEqual(self.hub.calls[0]["messages"][0]["content"], "会话：群「Example review」\n本次需要判断的消息：\n在吗")
        self.assertEqual(self.handed()["text"].split("对方的消息：\n")[1], MR)

    def test_rule_accepts_a_link_next_to_chinese_text_but_not_a_lookalike_domain(self):
        self.assertEqual(self.c.classify(["请看https://example.com/git/a/b/merge_requests/123谢谢"], "群「示例」")[0], "review")
        self.assertEqual(self.hub.calls, [])
        self.assertEqual(self.c.classify(["https://example.com/redirect/example.com/git/a/b/merge_requests/123"], "群「示例」")[0], "oncall")
        self.assertEqual(len(self.hub.calls), 1)

    def test_no_review_patterns_means_every_message_asks_the_model(self):
        self.c.auto_review_patterns = []
        self.assertEqual(self.c.classify([MR], "群「示例」")[0], "oncall")
        self.assertEqual(len(self.hub.calls), 1)

    def test_a_broken_review_pattern_is_skipped(self):
        self.assertEqual([p.pattern for p in _patterns(["(", REVIEW, 3])], [REVIEW, "3"])
        self.assertEqual(_patterns("not a list"), [])

    def test_broadcast_without_a_user_id_does_not_use_at_me(self):
        label = "@所有人".encode()
        rich = {"elements": {"dictionary": {"1": {"tag": 5, "property": bytes([18, len(label)]) + label}}}}
        m = normalize({"msg_id": "broadcast", "chat_id": "g", "from_id": "77", "msg_type_name": "TEXT",
                       "content": "@所有人", "at_me": True, "content_data": {"richText": rich}}, lambda *_: "")
        self.c._on_live(m)
        self.assertEqual(self.c._auto_pending, {})
        self.assertEqual(self.hub.calls, [])

    def test_rejected_handover_does_not_leave_reply_permission_or_start_followups(self):
        with mock.patch.object(self.hub, "call", side_effect=RpcError("offline")), mock.patch.object(connector, "AGENT_RETRY_SECONDS", []):
            self.live(1, MR)
            self.done("error")
        with self.assertRaises(RpcError):
            self.answer("m1")
        self.live(2, "另一个问题")
        self.done("assistant")
        self.assertEqual(len(self.hub.calls), 1)

    def test_owner_activity_at_debounce_checks_both_sides_of_trigger(self):
        now = time.time()
        with mock.patch.object(self.hub, "log", wraps=self.hub.log) as log:
            self.live(1, "request", at=now)
            self.live(2, "already answered", sender=ME, at=now)
            self.done("none")
            for n, age in ((3, 599), (4, 600), (5, 601)):
                chat = "dm%d" % n
                self.c.on_message(message("owner%d" % n, chat, 1, sender=ME, at=now - age))
                self.live(n, "request", chat, at=now)
                self.done("assistant" if age > 600 else "none")
            self.assertEqual(log.call_args_list.count(mock.call("auto skipped: owner active")), 3)
        self.assertEqual(len(self.hub.calls), 1)
        self.assertEqual([r["message"]["id"] for r in self.hub.received], ["m5"])

    def test_owner_checks_are_scoped_to_topic_and_ignore_ai_echo(self):
        self.c.on_message(message("root", "g", 1, sender=ME, kind="group"))
        self.live(2, "request", "g", kind="group", root="root", at_me=True)
        self.done("none")
        self.c.on_message(message("echo", "g", 3, sender=ME, kind="group", root="other",
                                  text="previous answer\n\n" + self.c.suffix))
        self.live(4, "request", "g", kind="group", root="other", at_me=True)
        self.done("assistant")
        self.live(5, "unrelated", "g", kind="group", sender=ME, root="root")
        self.live(6, "handled", "g", kind="group", sender=ME, root="other")
        with mock.patch.object(self.hub, "log") as log:
            self.assertEqual(self.answer("m4"), {})
            log.assert_called_with("auto reply skipped: owner replied")
        self.assertEqual(self.backend.sent, [])
        self.assertEqual(self.c.status({})["auto"]["owner_replied"], 1)

    def test_classify_supplies_the_last_eight_messages_and_direction(self):
        now = time.time()
        previous = ["oldest", "ignored", "turn 2", "x" * 205, "turn 4", "turn 5", "turn 6", "turn 7",
                    "turn 8", "帮我 merge 示例改动"]
        for i, text in enumerate(previous):
            self.c.on_message(message("prior%d" % i, "c", i, sender=ME if i % 2 else "u_a",
                                      at=now - 900 + i, text=text))
        self.hub.result = {"json": {"scenario": "none", "reason": "对方答应帮我"}}
        self.live(20, "稍晚帮你 merge", at=now)
        self.done("none")
        params, = self.hub.calls
        expected = [("我：" if i % 2 else "对方：") + t[:200] for i, t in enumerate(previous) if i >= 2]
        self.assertEqual(params["messages"][0]["content"].splitlines(),
                         ["会话：私聊「小明」", "最近的对话：", *expected, "本次需要判断的消息：", "稍晚帮你 merge"])
        for rule in ("回应我之前的请求", "答应 / 推迟 / 拒绝", "请我做事", "正常来回聊天", "到公司再帮你 merge", "今天没空查"):
            self.assertIn(rule, params["system"])
        self.assertEqual(self.hub.received, [])

    def test_manifest_and_initialize_enable_the_settings(self):
        with open(os.path.join(main.ROOT, "persona-extension.json"), encoding="utf-8") as f:
            manifest = json.load(f)
        self.assertEqual(manifest["version"], "0.5.2")
        self.assertIn("models", manifest["permissions"]["hub"])
        settings = manifest["configuration"]["properties"]
        self.assertEqual(settings["auto_active_minutes"]["default"], 10)
        hint = settings["auto_route_hint"]
        self.assertEqual((hint["type"], hint["default"], hint["maxLength"]), ("string", "", 2000))
        keys = ("auto_handle", "auto_bot_name", "auto_playbook_dir", "auto_dm_idle_hours")
        self.assertEqual([settings[k]["default"] for k in keys], [False, "", "", 24])
        self.assertEqual((settings[keys[-1]]["minimum"], settings[keys[-1]]["maximum"]), (1, 168))
        self.assertEqual(settings["auto_bot_id"]["default"], "")
        senders = settings["auto_test_senders"]
        self.assertEqual((senders["type"], senders["items"]["type"], senders["default"]), ("array", "string", []))
        timeout = settings["auto_bot_timeout_seconds"]
        self.assertEqual([timeout[k] for k in ("default", "minimum", "maximum")], [180, 30, 1800])
        plugin = main.Plugin(manifest, lambda *_: self.backend)
        with mock.patch.object(Connector, "start"):
            plugin.initialize({"protocol": main.PROTOCOL, "extension": {"data_dir": os.path.dirname(self.db)},
                               "config": dict(zip(keys, [True, "小明", "/books", 6]), auto_bot_id="8001",
                                              auto_bot_timeout_seconds=45, auto_active_minutes=2, auto_test_senders=["test-bot"], auto_route_hint="  规则  ")})
        self.addCleanup(plugin.connector.close)
        self.assertEqual(plugin.connector.auto_active_minutes, 2)
        self.assertEqual(plugin.connector.auto_test_senders, {"test-bot"})
        self.assertEqual(plugin.connector.auto_route_hint, "规则")
        self.assertEqual((plugin.connector.auto_bot_id, plugin.connector.auto_bot_timeout_seconds), ("8001", 45))
        self.assertEqual([getattr(plugin.connector, k) for k in keys], [True, "小明", "/books", 6])

    def test_logout_while_classifying_cannot_send_as_the_next_account(self):
        started, release = threading.Event(), threading.Event()

        def slow(texts, conversation, context):
            started.set()
            release.wait(2)
            return "approval", "工单"

        with mock.patch.object(self.c, "classify", side_effect=slow):
            self.live(1, "请审批")
            self.assertTrue(started.wait(2))
            self.c.logout({})
            self.c._signed_in, self.c._account_id = True, "new"
            release.set()
            self.done("none")
        self.assertEqual(self.hub.received, [])
        self.assertEqual(self.backend.sent, [])


if __name__ == "__main__":
    unittest.main()
