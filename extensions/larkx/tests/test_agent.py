"""/agent: the user's own message goes to the hub as a channel message, and the answer comes back under it.
Run them: python3 -m unittest discover -s tests, in extensions/larkx."""
import base64
import json
import os
import sys
import tempfile
import threading
import time
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from test_connector import ME, Backend, Hub, message  # noqa: E402

from larkx_persona import main  # noqa: E402
from larkx_persona.connector import AGENT_SUFFIX, AGENT_SUFFIX_LEAST, AGENT_SUFFIX_MOST, Connector, agent_request  # noqa: E402
from larkx_persona.main import Plugin  # noqa: E402
from larkx_persona.real import own_chat_ok  # noqa: E402
from larkx_persona.store import Store  # noqa: E402
from larkx_persona.wire import RpcError  # noqa: E402


class ChannelHub(Hub):
    def __init__(self):
        super().__init__()
        self.received = []
        self.got = threading.Event()

    def call(self, method, params):
        if method == "channel.receive":
            self.received.append(params)
            self.got.set()
            return {}
        return super().call(method, params)


class AgentRequestTest(unittest.TestCase):
    def test_what_counts_as_a_request(self):
        self.assertEqual(agent_request("/agent 总结一下"), "总结一下")
        self.assertEqual(agent_request("  /Agent：帮我看看"), "帮我看看")
        self.assertEqual(agent_request("/agent\n第二行"), "第二行")
        self.assertEqual(agent_request("/agent"), "")
        self.assertIsNone(agent_request("/agents 列表"))
        self.assertIsNone(agent_request("你好 /agent 在中间"))
        self.assertIsNone(agent_request(""))


class AgentTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.addCleanup(self.dir.cleanup)
        self.backend = Backend()
        self.hub = ChannelHub()
        self.c = Connector(self.backend, Store(os.path.join(self.dir.name, "x.db")), self.hub)
        self.c._ready.set()
        self.backend.poll = "done"
        self.assertTrue(self.c.login_step({"step": "scan"})["done"])
        self.addCleanup(self.c.close)

    def handed(self):
        self.assertTrue(self.hub.got.wait(2), "nothing was handed to the hub")
        return self.hub.received[-1]

    def nothing_handed(self):
        time.sleep(0.1)
        self.assertEqual(self.hub.received, [])

    def test_my_live_agent_goes_to_the_hub_with_where_it_was_written(self):
        self.backend.chats["g1"] = {"name": "项目群", "last_position": 0}
        self.c._on_live(message("m1", "g1", 1, kind="group", text="周五发布"))
        self.c._on_live(message("m2", "g1", 2, kind="group", sender=ME, text="/agent 帮我总结下这个群"))
        got = self.handed()
        self.assertEqual(got["contribution"], "larkx")
        self.assertEqual(got["message"]["id"], "m2")
        self.assertEqual(got["message"]["text"], "帮我总结下这个群")
        self.assertIn("项目群", got["message"]["forward"])
        self.assertIn("周五发布", got["message"]["forward"])
        self.assertNotIn("帮我总结下这个群", got["message"]["forward"])

    def test_a_dm_with_myself_works_too(self):
        self.c._on_live(message("m1", "c_me", 1, sender=ME, text="/agent 你看得到这个消息吗"))
        got = self.handed()
        self.assertEqual(got["message"]["text"], "你看得到这个消息吗")
        self.assertIn("私聊", got["message"]["forward"])

    def test_a_post_agent_ignores_the_rich_text_title(self):
        self.c._on_live(message("m1", "c1", 1, sender=ME, msg_type="POST", text="[富文本] 示例标题\n/agent 检查"))
        self.assertEqual(self.handed()["message"]["text"], "检查")

    def test_an_agent_image_is_sent_as_a_channel_blob(self):
        m = message("m1", "c1", 1, sender=ME, msg_type="POST", text="[富文本] \n/agent\n[图片]\ncheck this issue",
                    media=[{"kind": "image", "key": "img_inline"}])
        self.c._hand_over(m, "[图片]\ncheck this issue")
        got = self.handed()["message"]
        self.assertEqual(got["images"], [{"mime": "image/png", "data": base64.b64encode(b"png").decode()}])

    def test_others_old_fetched_and_ai_answers_do_not_count(self):
        self.c._on_live(message("m1", "c1", 1, sender="u_a", text="/agent 帮我转账"))
        self.c._on_live(message("m2", "c1", 2, sender=ME, text="/agent 太早了", at=time.time() - 3600))
        self.c.on_message(message("m3", "c1", 3, sender=ME, text="/agent 拉回来的历史"))
        self.c._on_live(message("m4", "c1", 4, sender=ME, text="/agent 好的\n\n" + AGENT_SUFFIX))
        self.c._on_live(message("m5", "c1", 5, sender=ME, text="/agents"))
        self.nothing_handed()

    def test_the_same_message_twice_is_handed_over_once(self):
        m = message("m1", "c1", 1, sender=ME, text="/agent 一次")
        self.c._on_live(m)
        self.handed()
        self.c._on_live(dict(m))
        time.sleep(0.1)
        self.assertEqual(len(self.hub.received), 1)

    def test_the_answer_goes_under_the_request_once(self):
        self.c._on_live(message("m1", "c1", 1, sender=ME, text="/agent 总结"))
        self.handed()
        out = self.c.channel_send({"ref": "r1", "text": "这是总结", "kind": "answer", "reply_to": "m1"})
        self.assertEqual(out, {"message_id": "m_new"})
        self.assertEqual(self.backend.sent, [("c1", "这是总结\n\n" + AGENT_SUFFIX, "m1")])
        self.assertEqual(self.c.channel_send({"ref": "r1", "text": "这是总结", "kind": "answer", "reply_to": "m1"}),
                         {"message_id": "m_new"})
        self.assertEqual(len(self.backend.sent), 1)

    def test_an_answer_in_a_topic_stays_in_the_topic(self):
        self.c._on_live(message("m2", "g1", 2, kind="group", sender=ME, text="/agent 看看", root="m_root"))
        self.handed()
        self.c.channel_send({"ref": "r1", "text": "好", "kind": "answer", "reply_to": "m2"})
        self.assertEqual(self.backend.sent[0][2], "m_root")

    def test_nothing_goes_where_the_user_did_not_ask(self):
        self.c.on_message(message("m1", "c1", 1, sender="u_a", text="在吗"))
        # Only a push goes out without a request (to the user's own chat, AgentPushTest); a push answering a message
        # still needs that message to be one of the user's own requests.
        for p in ({"ref": "r1", "text": "x", "kind": "note"}, {"ref": "r2", "text": "x", "kind": "answer", "reply_to": "m1"},
                  {"ref": "r3", "text": "x", "kind": "answer", "reply_to": "m_unknown"},
                  {"ref": "r4", "text": "x", "kind": "push", "reply_to": "m1"}):
            with self.assertRaises(RpcError) as e:
                self.c.channel_send(p)
            self.assertEqual(e.exception.code, "cannot_send")
        with self.assertRaises(RpcError):
            self.c.channel_send({"text": "x", "kind": "answer", "reply_to": "m1"})
        self.assertEqual(self.backend.sent, [])

    def test_an_answer_only_goes_to_an_agent_request(self):
        # A normal message from the user is not an /agent, and our own answer is not one either.
        self.c.on_message(message("m_plain", "c1", 1, sender=ME, text="ordinary message"))
        self.c.on_message(message("m_ai", "c1", 2, sender=ME, text="答\n\n" + AGENT_SUFFIX))
        for mid in ("m_plain", "m_ai"):
            with self.assertRaises(RpcError) as e:
                self.c.channel_send({"ref": "r_" + mid, "text": "x", "kind": "answer", "reply_to": mid})
            self.assertEqual(e.exception.code, "cannot_send")
        self.assertEqual(self.backend.sent, [])

    def test_an_unknown_result_is_never_sent_again(self):
        self.c._on_live(message("m1", "c1", 1, sender=ME, text="/agent 发"))
        self.handed()
        self.backend.send_error = OSError("cut off")
        with self.assertRaises(RpcError):
            self.c.channel_send({"ref": "r1", "text": "x", "kind": "answer", "reply_to": "m1"})
        self.backend.send_error = None
        with self.assertRaises(RpcError):
            self.c.channel_send({"ref": "r1", "text": "x", "kind": "answer", "reply_to": "m1"})
        self.assertEqual(self.backend.sent, [])


class AgentTopicTest(unittest.TestCase):
    """A group request carries its topic as the channel message's thread: the hub gives each topic a conversation of its
    own. The key is the topic the answer goes into: the request's root, or the request itself when it opens the topic."""
    setUp, handed, nothing_handed = AgentTest.setUp, AgentTest.handed, AgentTest.nothing_handed

    def test_a_request_outside_a_topic_opens_one(self):
        self.c._on_live(message("m1", "g1", 1, kind="group", sender=ME, text="/agent 新问题"))
        self.assertEqual(self.handed()["message"]["thread"], "g1:m1")
        self.c.channel_send({"ref": "r1", "text": "好", "kind": "answer", "reply_to": "m1"})
        self.assertEqual(self.backend.sent[0][2], "m1")

    def test_a_request_in_a_topic_continues_it(self):
        self.c._on_live(message("m2", "g1", 2, kind="group", sender=ME, text="/agent 接着说", root="m1"))
        self.assertEqual(self.handed()["message"]["thread"], "g1:m1")
        self.c.channel_send({"ref": "r1", "text": "好", "kind": "answer", "reply_to": "m2"})
        self.assertEqual(self.backend.sent[0][2], "m1")

    def test_a_self_mention_in_a_topic_counts_the_same(self):
        m = message("m3", "g1", 3, kind="group", sender=ME, text="@Example 再看看", root="m1")
        m["mentions"] = [{"id": ME, "start": 0, "end": 8, "label": "@Example"}]
        self.c._on_live(m)
        got = self.handed()["message"]
        self.assertEqual((got["text"], got["thread"]), ("再看看", "g1:m1"))

    def test_a_direct_chat_has_no_thread(self):
        self.c._on_live(message("m1", "c_me", 1, sender=ME, text="/agent 私聊", root="m0"))
        self.assertNotIn("thread", self.handed()["message"])

    def test_the_setting_off_sends_no_thread(self):
        self.c.topics = False
        self.c._on_live(message("m1", "g1", 1, kind="group", sender=ME, text="/agent 新问题"))
        self.assertNotIn("thread", self.handed()["message"])

    def test_initialize_and_the_manifest(self):
        got = {}

        class Recorder:
            def __init__(self, *args, **kwargs):
                got["connector"] = self

            def start(self):
                pass

        real = main.Connector
        main.Connector = Recorder
        self.addCleanup(setattr, main, "Connector", real)
        for config, want in (({}, True), ({"agent_group_topics": False}, False), ({"agent_group_topics": True}, True)):
            plugin = Plugin({"id": "larkx", "version": "0", "contributes": {}}, lambda *_: None)
            plugin.initialize({"protocol": main.PROTOCOL, "extension": {"data_dir": self.dir.name}, "config": config})
            self.assertIs(got["connector"].topics, want, config)
        with open(os.path.join(main.ROOT, "persona-extension.json"), encoding="utf-8") as f:
            manifest = json.load(f)
        self.assertIs(manifest["configuration"]["properties"]["agent_group_topics"]["default"], True)
        self.assertIs(manifest["contributes"]["channel"][0]["capabilities"]["threads"], True)


class AgentPushTest(unittest.TestCase):
    """A push (kind push, no reply_to: a background task's report, a main-chat turn, a reminder) may carry anything the
    assistant knows, so it goes only to the user's own chat with themself, marked like an answer."""
    setUp, handed, nothing_handed = AgentTest.setUp, AgentTest.handed, AgentTest.nothing_handed

    def test_a_push_goes_to_my_own_chat_once(self):
        out = self.c.channel_send({"ref": "p1", "text": "后台任务做完了", "kind": "push"})
        self.assertEqual(out, {"message_id": "m_new"})
        self.assertEqual(self.backend.sent, [("c_me", "后台任务做完了\n\n" + AGENT_SUFFIX, None)])
        self.assertEqual(self.c.channel_send({"ref": "p1", "text": "后台任务做完了", "kind": "push"}), {"message_id": "m_new"})
        self.assertEqual(len(self.backend.sent), 1)

    def test_a_push_never_goes_anywhere_else(self):
        for own in (None, "", OSError("unreachable")):
            def own_chat(own=own):
                if isinstance(own, Exception):
                    raise own
                return own
            self.backend.own_chat = own_chat
            self.c._own_chats.clear()
            with self.assertRaises(RpcError) as e:
                self.c.channel_send({"ref": "p_%r" % own, "text": "x", "kind": "push"})
            self.assertEqual(e.exception.code, "cannot_send")
        self.assertEqual(self.backend.sent, [])

    def test_every_sign_in_checks_the_own_chat_again(self):
        # Review of #843: the chat found for one sign-in is not reused after signing out and in again.
        looked = []
        self.backend.own_chat = lambda: looked.append(1) or "c_me"
        self.c.channel_send({"ref": "p1", "text": "一", "kind": "push"})
        self.c.channel_send({"ref": "p2", "text": "二", "kind": "push"})
        self.assertEqual(len(looked), 1)
        self.c.logout({})
        self.backend.poll = "done"
        self.assertTrue(self.c.login_step({"step": "scan"})["done"])
        self.c.channel_send({"ref": "p3", "text": "三", "kind": "push"})
        self.assertEqual(len(looked), 2)
        self.backend.own_chat = lambda: None  # the new sign-in's check fails: nothing goes out
        self.c.logout({})
        self.assertTrue(self.c.login_step({"step": "scan"})["done"])
        with self.assertRaises(RpcError) as e:
            self.c.channel_send({"ref": "p4", "text": "四", "kind": "push"})
        self.assertEqual(e.exception.code, "cannot_send")

    def test_no_push_while_signed_out(self):
        self.c.logout({})
        with self.assertRaises(RpcError) as e:
            self.c.channel_send({"ref": "p1", "text": "x", "kind": "push"})
        self.assertEqual(e.exception.code, "cannot_send")
        self.assertEqual(self.backend.sent, [])

    def test_an_unknown_push_result_is_never_sent_again(self):
        self.backend.send_error = OSError("cut off")
        with self.assertRaises(RpcError):
            self.c.channel_send({"ref": "p1", "text": "x", "kind": "push"})
        self.backend.send_error = None
        with self.assertRaises(RpcError):
            self.c.channel_send({"ref": "p1", "text": "x", "kind": "push"})
        self.assertEqual(self.backend.sent, [])

    def test_my_pushed_message_coming_back_is_not_a_request(self):
        self.c._on_live(message("m1", "c_me", 1, sender=ME, text="/agent 看一下\n\n" + AGENT_SUFFIX))
        self.nothing_handed()

    def test_only_a_one_person_direct_chat_counts_as_my_own(self):
        # Fails closed (review of #843): only a direct chat with memberCount 1 owned by the user counts. The user's
        # chat with themself on a real account reports type 1, memberCount 1 and ownerId the user, and no userCount.
        self.assertTrue(own_chat_ok({"type": 1, "memberCount": 1, "ownerId": ME}, ME))
        self.assertTrue(own_chat_ok({"type": "P2P", "memberCount": "1", "ownerId": ME, "userCount": 1}, ME))
        for info in (None, {}, {"type": "P2P"}, {"type": 1, "ownerId": ME}, {"type": 1, "memberCount": 1},
                     {"type": 1, "memberCount": 1, "ownerId": "u_other"}, {"type": 1, "memberCount": 2, "ownerId": ME},
                     {"type": 1, "memberCount": 0, "ownerId": ME}, {"type": 1, "memberCount": True, "ownerId": ME},
                     {"type": 1, "memberCount": "x", "ownerId": ME}, {"type": 1, "memberCount": 1, "ownerId": ME, "userCount": 2},
                     {"type": 1, "memberCount": 1, "ownerId": ME, "userCount": None},
                     {"type": 2, "memberCount": 1, "ownerId": ME}, {"type": "GROUP", "memberCount": 1, "ownerId": ME}):
            self.assertFalse(own_chat_ok(info, ME), info)
        self.assertFalse(own_chat_ok({"type": 1, "memberCount": 1, "ownerId": ""}, ""))

    def test_the_manifest_declares_push(self):
        with open(os.path.join(main.ROOT, "persona-extension.json"), encoding="utf-8") as f:
            channel = json.load(f)["contributes"]["channel"][0]
        self.assertIs(channel["capabilities"]["push"], True)


class AgentSuffixTest(unittest.TestCase):
    """The setting agent_reply_suffix: the line an answer ends with, and what marks a message as an answer."""
    setUp, handed, nothing_handed = AgentTest.setUp, AgentTest.handed, AgentTest.nothing_handed

    def use(self, suffix):
        self.c = Connector(self.backend, self.c.store, self.hub, suffix=suffix)
        self.c._ready.set()
        self.assertTrue(self.c.login_step({"step": "scan"})["done"])
        self.addCleanup(self.c.close)

    def test_an_answer_ends_with_the_set_suffix(self):
        self.use("  🤖 由 AI 助手代答  ")
        self.c._on_live(message("m1", "c1", 1, sender=ME, text="/agent 总结"))
        self.handed()
        self.c.channel_send({"ref": "r1", "text": "这是总结", "kind": "answer", "reply_to": "m1"})
        self.assertEqual(self.backend.sent, [("c1", "这是总结\n\n🤖 由 AI 助手代答", "m1")])

    def test_a_blank_suffix_keeps_the_default(self):
        for blank in ("", "   \n ", None):
            self.assertEqual(Connector(self.backend, self.c.store, self.hub, suffix=blank).suffix, AGENT_SUFFIX)

    def test_a_too_short_suffix_keeps_the_default(self):
        # "AI" would make "/agent 把这段翻译给 AI" look like an answer and be ignored.
        for short in ("AI", " AI ", "x" * (AGENT_SUFFIX_LEAST - 1)):
            self.assertEqual(Connector(self.backend, self.c.store, self.hub, suffix=short).suffix, AGENT_SUFFIX)
        self.assertEqual(Connector(self.backend, self.c.store, self.hub, suffix="AI代答").suffix, "AI代答")
        self.use("AI")
        self.c._on_live(message("m1", "c1", 1, sender=ME, text="/agent 把这段翻译给 AI"))
        self.assertEqual(self.handed()["message"]["text"], "把这段翻译给 AI")

    def test_a_long_suffix_is_cut_and_still_matches_its_own_answer(self):
        self.use("A" * (AGENT_SUFFIX_MOST - 1) + " tail")
        self.assertEqual(self.c.suffix, "A" * (AGENT_SUFFIX_MOST - 1))
        self.c._on_live(message("m1", "c1", 1, sender=ME, text="/agent 好的\n\n" + self.c.suffix + " "))
        self.nothing_handed()

    def test_my_message_ending_with_the_set_suffix_is_an_answer_not_a_request(self):
        self.use("— AI 代答")
        self.c._on_live(message("m1", "c1", 1, sender=ME, text="/agent 好的\n\n— AI 代答"))
        self.nothing_handed()
        with self.assertRaises(RpcError) as e:
            self.c.channel_send({"ref": "r1", "text": "x", "kind": "answer", "reply_to": "m1"})
        self.assertEqual(e.exception.code, "cannot_send")
        self.assertEqual(self.backend.sent, [])

    def test_the_manifest_setting_matches_the_code(self):
        with open(os.path.join(main.ROOT, "persona-extension.json"), encoding="utf-8") as f:
            setting = json.load(f)["configuration"]["properties"]["agent_reply_suffix"]
        self.assertEqual(setting["default"], AGENT_SUFFIX)
        self.assertEqual(setting["maxLength"], AGENT_SUFFIX_MOST)

    def test_initialize_hands_the_setting_to_the_connector(self):
        got = {}

        class Recorder:
            def __init__(self, backend, store, hub, rules="", suffix=""):
                got.update(rules=rules, suffix=suffix)

            def start(self):
                pass

        real = main.Connector
        main.Connector = Recorder
        self.addCleanup(setattr, main, "Connector", real)
        plugin = Plugin({"id": "larkx", "version": "0", "contributes": {}}, lambda *_: None)
        plugin.initialize({"protocol": main.PROTOCOL, "extension": {"data_dir": self.dir.name},
                           "config": {"triage_rules": "规则", "agent_reply_suffix": "— AI 代答"}})
        self.assertEqual(got, {"rules": "规则", "suffix": "— AI 代答"})


class PluginRoutingTest(unittest.TestCase):
    def test_channel_methods_reach_the_connector(self):
        plugin = Plugin({"id": "larkx", "version": "0", "contributes": {}}, lambda *_: None)

        class Fake:
            methods = {}
            channel_methods = {"typing": lambda p: {"typed": True}}

        plugin.connector = Fake()
        self.assertEqual(plugin.handle("channel.typing", {"contribution": "larkx", "on": True}), {"typed": True})
        with self.assertRaises(RpcError) as e:
            plugin.handle("channel.send", {"contribution": "other"})
        self.assertEqual(e.exception.code, "unknown_method")
        with self.assertRaises(RpcError) as e:
            plugin.handle("channel.typing", {"contribution": "other"})
        self.assertEqual(e.exception.code, "no_contribution")


if __name__ == "__main__":
    unittest.main()
