"""Pictures and files larkx_send and larkx_send_dm send: the local file checks, the confirmation card, and that nothing is
uploaded or sent before actions.do, and only what the card showed. Synthetic files only; no network.
Run them: python3 -m unittest discover -s tests, in extensions/larkx."""
import hashlib
import os
import struct
import sys
import tempfile
import threading
import unittest
from unittest import mock

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "src"))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from test_connector import Backend, Hub, message  # noqa: E402

from larkx_persona import outbound  # noqa: E402
from larkx_persona.connector import ACTIONS, Connector  # noqa: E402
from larkx_persona.fixture import FixtureBackend  # noqa: E402
from larkx_persona.main import PROTOCOL, Plugin  # noqa: E402
from larkx_persona.real import UploadError  # noqa: E402
from larkx_persona.store import Store  # noqa: E402
from larkx_persona.wire import RpcError  # noqa: E402


def png(width=640, height=480, tail=b"pixels"):
    return b"\x89PNG\r\n\x1a\n" + b"\x00\x00\x00\x0dIHDR" + struct.pack(">II", width, height) + b"\x08\x02\x00\x00\x00" + tail


def jpeg(width=800, height=600):
    app0 = b"\xff\xe0" + struct.pack(">H", 16) + b"JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"
    sof = b"\xff\xc0" + struct.pack(">HBHHB", 17, 8, height, width, 3) + b"\x01\x22\x00\x02\x11\x01\x03\x11\x01"
    return b"\xff\xd8" + app0 + sof + b"\xff\xd9"


def gif(width=32, height=16):
    return b"GIF89a" + struct.pack("<HH", width, height) + b"\x00\x00\x00;"


def bmp(width=20, height=10):
    return b"BM" + b"\x00" * 16 + struct.pack("<ii", width, -height) + b"\x00" * 8


def webp(chunk, width=300, height=200):
    if chunk == b"VP8X":
        body = b"\x00\x00\x00\x00" + (width - 1).to_bytes(3, "little") + (height - 1).to_bytes(3, "little")
    elif chunk == b"VP8L":
        body = b"\x2f" + ((width - 1) | ((height - 1) << 14)).to_bytes(4, "little")
    else:
        body = b"\x00\x00\x00\x9d\x01\x2a" + struct.pack("<HH", width, height)
    data = b"WEBP" + chunk + struct.pack("<I", len(body)) + body
    return b"RIFF" + struct.pack("<I", len(data)) + data


class MediaBackend(Backend):
    """The connector test's backend, with uploads and media messages recorded in the order they happen."""

    def __init__(self):
        super().__init__()
        self.events = []
        self.uploads = []
        self.media_sent = []
        self.upload_error = None
        self.media_results = []
        self.created = []

    def send(self, chat, text, root, mentions=()):
        self.events.append(("text", chat, text, root))
        return super().send(chat, text, root, mentions)

    def create_chat(self, user):
        self.created.append(user)
        return super().create_chat(user)

    def upload(self, item):
        if self.upload_error:
            raise self.upload_error
        self.uploads.append((item["kind"], item["name"], item["size"], item["sha256"], item["data"]))
        self.events.append(("upload", item["kind"], item["name"]))
        if item["kind"] == "image":
            return {"kind": "image", "key": "img_key_%d" % len(self.uploads)}
        return {"kind": "file", "key": "file_key_%d" % len(self.uploads), "name": item["name"], "size": item["size"],
                "mime": item["mime"]}

    def send_media(self, chat, uploaded, root):
        self.events.append(("media", chat, uploaded["kind"], uploaded["key"], root))
        self.media_sent.append((chat, uploaded, root))
        if self.media_results:
            result = self.media_results.pop(0)
            if isinstance(result, Exception):
                raise result
            return result
        return "m_%s_%d" % (uploaded["kind"], len(self.media_sent))


class Files(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = os.path.realpath(self.tmp.name)

    def write(self, rel, data):
        path = os.path.join(self.root, *rel.split("/"))
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as f:
            f.write(data)
        return path


class OutboundTest(Files):
    def test_a_picture_has_its_name_type_size_digest_and_dimensions(self):
        data = png()
        item = outbound.load("image", self.write("shot.png", data))
        self.assertEqual((item["kind"], item["name"], item["size"], item["mime"], item["label"]),
                         ("image", "shot.png", len(data), "image/png", "PNG"))
        self.assertEqual((item["width"], item["height"]), (640, 480))
        self.assertEqual(item["sha256"], hashlib.sha256(data).hexdigest())
        self.assertEqual(item["data"], data)
        lines = outbound.card_lines(item)
        self.assertEqual(lines[0], "图片：shot.png（PNG，640×480，%d 字节）" % len(data))
        self.assertEqual(lines[1], "  位置：%s" % item["path"])
        self.assertEqual(lines[2], "  SHA-256：%s…" % item["sha256"][:12])

    def test_every_inline_picture_format_reads_its_dimensions(self):
        cases = [(jpeg(), "image/jpeg", (800, 600)), (gif(), "image/gif", (32, 16)), (bmp(), "image/bmp", (20, 10)),
                 (webp(b"VP8X"), "image/webp", (300, 200)), (webp(b"VP8L"), "image/webp", (300, 200)),
                 (webp(b"VP8 "), "image/webp", (300, 200))]
        for i, (data, mime, size) in enumerate(cases):
            with self.subTest(mime=mime, i=i):
                item = outbound.load("image", self.write("p%d.img" % i, data))
                self.assertEqual(item["mime"], mime)
                self.assertEqual((item.get("width"), item.get("height")), size)

    def test_a_picture_with_an_unreadable_header_still_goes_without_dimensions(self):
        item = outbound.load("image", self.write("cut.jpg", b"\xff\xd8\xff\xe0\x00"))
        self.assertEqual(item["mime"], "image/jpeg")
        self.assertNotIn("width", item)
        self.assertNotIn("×", outbound.card_lines(item)[0])

    def test_something_else_is_no_picture_but_goes_as_a_file(self):
        path = self.write("report.pdf", b"%PDF-1.7 example")
        with self.assertRaises(outbound.MediaError) as e:
            outbound.load("image", path)
        self.assertIn("改用 file", str(e.exception))
        item = outbound.load("file", path)
        self.assertEqual((item["mime"], item["label"]), ("application/pdf", "application/pdf"))
        self.assertEqual(outbound.load("file", self.write("blob.unknownext", b"x"))["label"], "未知类型")

    def test_the_path_must_be_absolute_and_one_clean_line(self):
        self.write("a.png", png())
        for bad in ("a.png", "./a.png", "", "   ", self.root + "/a\n.png", self.root + "/a‮.png",
                    self.root + "/a\t.png", "/" + "x" * 5000):
            with self.subTest(bad=bad), self.assertRaises(outbound.MediaError):
                outbound.load("image", bad)

    def test_a_home_relative_path_is_expanded(self):
        self.write("Desktop/a.png", png())
        with mock.patch.dict(os.environ, {"HOME": self.root}):
            self.assertEqual(outbound.load("image", "~/Desktop/a.png")["path"], os.path.join(self.root, "Desktop", "a.png"))

    def test_a_missing_file_a_directory_an_empty_file_and_a_pipe_are_refused(self):
        os.makedirs(os.path.join(self.root, "dir.png"))
        empty = self.write("empty.png", b"")
        pipe = os.path.join(self.root, "pipe.png")
        os.mkfifo(pipe)
        for path, words in ((os.path.join(self.root, "gone.png"), "找不到"), (os.path.join(self.root, "dir.png"), "目录"),
                            (empty, "空的"), (pipe, "不是普通文件")):
            with self.subTest(path=path), self.assertRaises(outbound.MediaError) as e:
                outbound.load("file", path)
            self.assertIn(words, str(e.exception))

    def test_a_symbolic_link_is_refused_and_one_to_a_secret_names_the_secret(self):
        target = self.write("real.png", png())
        link = os.path.join(self.root, "link.png")
        os.symlink(target, link)
        with self.assertRaises(outbound.MediaError) as e:
            outbound.load("image", link)
        self.assertIn("符号链接", str(e.exception))
        key = self.write("keys/.ssh/id_ed25519", b"not a real key")
        sneaky = os.path.join(self.root, "holiday.png")
        os.symlink(key, sneaky)
        with self.assertRaises(outbound.MediaError) as e:
            outbound.load("file", sneaky)
        self.assertIn(".ssh", str(e.exception))
        # A link in a parent directory is followed, and the real place is what is checked.
        os.symlink(os.path.join(self.root, "keys", ".ssh"), os.path.join(self.root, "photos"))
        with self.assertRaises(outbound.MediaError):
            outbound.load("file", os.path.join(self.root, "photos", "id_ed25519"))

    def test_credentials_keys_and_the_hubs_state_are_refused(self):
        for rel in (".ssh/config", "x/.gnupg/pubring.kbx", ".aws/config", ".kube/config", ".docker/config.json",
                    ".persona/hub.json", ".persona/data/chart.png", ".larkx/credentials.json",
                    ".config/gcloud/application_default_credentials.json", "Library/Keychains/login.keychain-db",
                    "proj/.env", "proj/.env.local", "proj/prod.env", "proj/.netrc", "proj/.git-credentials",
                    "proj/.npmrc", "proj/.pgpass", "proj/id_rsa", "proj/id_rsa.pub", "proj/server.pem",
                    "proj/tls.key", "proj/cert.p12", "proj/vault.kdbx", "proj/credentials.json",
                    "proj/larkx.db", "PROJ/.SSH/ID_RSA"):
            with self.subTest(rel=rel):
                path = self.write(rel, b"example")
                with self.assertRaises(outbound.MediaError):
                    outbound.load("file", path)
        for path in ("/etc/hosts", "/proc/self/status", "/dev/null", "/sys/kernel"):
            with self.subTest(path=path), self.assertRaises(outbound.MediaError) as e:
                outbound.load("file", path)
            self.assertIn("系统目录", str(e.exception))

    def test_ordinary_names_near_secret_ones_are_fine(self):
        for rel in ("docs/environment.png", "docs/keynote-notes.txt", "docs/ssh-guide.pdf", "docs/id_card_scan.png",
                    "docs/env/readme.md", "docs/persona/notes.txt"):
            with self.subTest(rel=rel):
                kind = "image" if rel.endswith(".png") else "file"
                self.assertEqual(outbound.load(kind, self.write(rel, png()))["name"], os.path.basename(rel))

    def test_the_programs_own_data_directory_is_refused(self):
        data_dir = os.path.join(self.root, "data")
        path = self.write("data/larkagentx-0000/notes.txt", b"example")
        with self.assertRaises(outbound.MediaError) as e:
            outbound.load("file", path, private=(data_dir,))
        self.assertIn("插件自己的数据", str(e.exception))
        self.assertEqual(outbound.load("file", self.write("data-other/notes.txt", b"ok"), private=(data_dir,))["size"], 2)

    def test_size_limits_are_checked_before_reading(self):
        big = self.write("big.bin", b"x" * 11)
        with mock.patch.object(outbound, "FILE_MOST", 10):
            with self.assertRaises(outbound.MediaError) as e:
                outbound.load("file", big)
        self.assertIn("太大", str(e.exception))
        with mock.patch.object(outbound, "IMAGE_MOST", 10):
            with self.assertRaises(outbound.MediaError):
                outbound.load("image", self.write("big.png", png()))
        self.assertEqual(outbound.IMAGE_MOST, 10 << 20)
        self.assertEqual(outbound.FILE_MOST, 30 << 20)
        self.assertEqual(outbound.human_size(1536), "1.5 KB")
        self.assertEqual(outbound.human_size(3 << 20), "3.0 MB")

    def test_media_args_are_paths(self):
        self.assertEqual(outbound.media_args({"text": "hi"}), [])
        self.assertEqual(outbound.media_args({"file": "/a", "image": "/b"}), [("image", "/b"), ("file", "/a")])
        self.assertEqual(outbound.media_args({"image": "", "file": None}), [])
        for bad in ({"image": 3}, {"file": ["/a"]}, {"image": "  "}):
            with self.subTest(bad=bad), self.assertRaises(outbound.MediaError):
                outbound.media_args(bad)


class ConnectorMediaTest(Files):
    def setUp(self):
        super().setUp()
        self.backend = MediaBackend()
        self.hub = Hub()
        self.c = Connector(self.backend, Store(os.path.join(self.root, "x.db")), self.hub)
        self.addCleanup(self.c.close)
        self.c._ready.set()
        self.backend.poll = "done"
        self.assertTrue(self.c.login_step({"step": "scan"})["done"])
        self.image = self.write("out/shot.png", png())
        self.file = self.write("out/report.pdf", b"%PDF-1.7 example report")

    def describe(self, args, name="larkx_send"):
        return self.c.actions_describe({"name": name, "args": args})

    def do(self, args, request_id="r1", name="larkx_send"):
        return self.c.actions_do({"name": name, "args": args, "request_id": request_id})

    def nothing_left_the_machine(self):
        self.assertEqual(self.backend.uploads, [])
        self.assertEqual(self.backend.sent, [])
        self.assertEqual(self.backend.media_sent, [])

    def test_the_schemas_take_a_picture_and_a_file_and_text_is_optional(self):
        actions = {a["name"]: a["input_schema"] for a in ACTIONS}
        self.assertEqual(actions["larkx_send"]["required"], ["chat"])
        self.assertEqual(actions["larkx_send_dm"]["required"], ["user"])
        for schema in actions.values():
            self.assertEqual(schema["properties"]["image"]["type"], "string")
            self.assertEqual(schema["properties"]["file"]["type"], "string")
            self.assertIn("Absolute path", schema["properties"]["file"]["description"])
        self.assertIn("nothing is uploaded before that", ACTIONS[0]["description"])

    def test_a_picture_alone_is_shown_then_uploaded_and_sent_only_after_approval(self):
        args = {"chat": "c1", "image": self.image}
        card = self.describe(args)
        summary = card["summary"]
        self.assertTrue(summary.startswith("以你本人的身份发飞书消息\n发给："))
        self.assertIn("图片：shot.png（PNG，640×480，", summary)
        self.assertIn("  位置：%s" % self.image, summary)
        self.assertIn("SHA-256：%s…" % hashlib.sha256(png()).hexdigest()[:12], summary)
        self.assertIn("只发这一条消息", summary)
        self.assertTrue(summary.endswith("内容：没有文字"))
        # The card is all that happened: the hub has not let the write through yet.
        self.nothing_left_the_machine()
        self.assertEqual(self.do(args), {"text": "已发出（m_image_1）。"})
        self.assertEqual(self.backend.events, [("upload", "image", "shot.png"), ("media", "c1", "image", "img_key_1", None)])
        self.assertEqual(self.backend.uploads[0][4], png())
        self.assertEqual(self.backend.sent, [])

    def test_a_refused_or_expired_confirmation_never_uploads(self):
        # The hub calls actions.do only after the user approves; refused or timed out, it never comes.
        for args in ({"chat": "c1", "image": self.image}, {"chat": "c1", "text": "附件", "file": self.file}):
            self.describe(args)
        self.describe({"user": "u_b", "file": self.file}, name="larkx_send_dm")
        self.nothing_left_the_machine()
        self.assertEqual(self.backend.created, [])

    def test_text_then_picture_then_file_each_as_its_own_message(self):
        args = {"chat": "c1", "text": "这是周报", "image": self.image, "file": self.file}
        summary = self.describe(args)["summary"]
        self.assertIn("文件：report.pdf（application/pdf，", summary)
        self.assertIn("按文字、图片、文件的顺序发，每样各是一条消息", summary)
        self.assertTrue(summary.endswith("内容（全文）：\n这是周报"))
        self.assertLess(summary.index("图片：shot.png"), summary.index("文件：report.pdf"))
        out = self.do(args)
        self.assertEqual(out, {"text": "已发出（m_new，m_image_1，m_file_2）。"})
        self.assertEqual([e[0] for e in self.backend.events], ["upload", "upload", "text", "media", "media"])
        self.assertEqual(self.backend.sent, [("c1", "这是周报", None)])
        file_up = self.backend.media_sent[1][1]
        self.assertEqual((file_up["name"], file_up["mime"], file_up["size"]), ("report.pdf", "application/pdf", 23))

    def test_a_topic_reply_carries_the_picture_into_the_topic(self):
        self.backend.chats["g1"] = {"name": "项目群", "last_position": 0}
        self.c.on_message(message("m1", "g1", 1, kind="group", text="截图发一下"))
        self.c.on_message(message("m2", "g1", 2, kind="group", text="在这", root="m1"))
        args = {"chat": "g1", "text": "给", "image": self.image, "reply_to": "m2"}
        card = self.describe(args)
        self.assertEqual(card["action"]["visible"], "群『项目群』里的所有人")
        self.assertIn("回复：小明 的消息「在这」（发在这条消息的话题里）", card["summary"])
        self.do(args)
        self.assertEqual(self.backend.sent, [("g1", "给", "m1")])
        self.assertEqual(self.backend.media_sent[0][0], "g1")
        self.assertEqual(self.backend.media_sent[0][2], "m1")

    def test_mentions_still_reach_the_text_and_need_it(self):
        mentions = [{"id": "7001", "name": "小明"}]
        args = {"chat": "c1", "text": "@小明 看图", "mentions": mentions, "image": self.image}
        self.describe(args)
        self.do(args)
        self.assertEqual(self.backend.sent, [("c1", "@小明 看图", None, mentions)])
        with self.assertRaises(RpcError) as e:
            self.do({"chat": "c1", "mentions": mentions, "image": self.image}, request_id="r2")
        self.assertEqual(e.exception.code, "invalid_params")
        self.assertEqual(len(self.backend.uploads), 1)

    def test_a_direct_message_with_a_file(self):
        args = {"user": "u_b", "text": "合同", "file": self.file}
        summary = self.describe(args, name="larkx_send_dm")["summary"]
        self.assertIn("发给：u_b〔私聊〕", summary)
        self.assertIn("文件：report.pdf", summary)
        self.assertEqual(self.do(args, name="larkx_send_dm"), {"text": "已发出（m_new，m_file_1）。"})
        self.assertEqual(self.backend.created, ["u_b"])
        self.assertEqual(self.backend.sent, [("c_u_b", "合同", None)])
        self.assertEqual(self.backend.media_sent[0][0], "c_u_b")
        args = {"user": "u_b", "image": self.image}
        self.describe(args, name="larkx_send_dm")
        self.do(args, request_id="r2", name="larkx_send_dm")
        self.assertEqual(self.backend.media_sent[-1][1]["kind"], "image")

    def test_text_only_sends_are_unchanged(self):
        card = self.describe({"chat": "c1", "text": "好"})
        self.assertNotIn("图片", card["summary"])
        self.assertTrue(card["summary"].endswith("内容（全文）：\n好"))
        self.assertEqual(self.do({"chat": "c1", "text": "好"}), {"text": "已发出（m_new）。"})
        for args in ({"chat": "c1"}, {"chat": "c1", "text": "  "}):
            with self.subTest(args=args), self.assertRaises(RpcError) as e:
                self.describe(args)
            self.assertIn("缺少 text", e.exception.message)
        self.assertEqual(self.backend.uploads, [])

    def test_a_send_without_its_card_is_refused_before_anything_happens(self):
        args = {"chat": "c1", "image": self.image}
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("没有找到这次发送的确认卡", e.exception.message)
        self.nothing_left_the_machine()
        # Nothing was claimed: once confirmed, the same request goes.
        self.describe(args)
        self.assertEqual(self.do(args), {"text": "已发出（m_image_1）。"})

    def test_a_file_changed_after_the_card_is_refused(self):
        args = {"chat": "c1", "text": "图", "image": self.image}
        self.describe(args)
        self.write("out/shot.png", png(tail=b"other pixels"))
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("文件在确认之后变了", e.exception.message)
        self.nothing_left_the_machine()
        os.remove(self.image)
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("找不到", e.exception.message)
        self.nothing_left_the_machine()

    def test_a_card_is_used_once(self):
        args = {"chat": "c1", "image": self.image}
        self.describe(args)
        first = self.do(args)
        self.assertEqual(self.do(args), first)  # the same request: its first result, nothing again
        with self.assertRaises(RpcError) as e:
            self.do(args, request_id="r2")  # another request needs a card of its own
        self.assertIn("确认卡", e.exception.message)
        self.assertEqual(len(self.backend.uploads), 1)
        self.assertEqual(len(self.backend.media_sent), 1)

    def test_two_sends_at_once_cannot_share_one_card(self):
        args = {"chat": "c1", "image": self.image}
        self.describe(args)
        gate = threading.Barrier(2)
        original = outbound.load

        def load_together(*a, **k):
            item = original(*a, **k)
            if a[0] == "image":
                try:
                    gate.wait(timeout=2)
                except threading.BrokenBarrierError:
                    pass
            return item

        results = []

        def run(request_id):
            try:
                results.append(self.do(args, request_id=request_id)["text"])
            except RpcError as e:
                results.append(e.message)

        with mock.patch.object(outbound, "load", load_together):
            workers = [threading.Thread(target=run, args=(r,)) for r in ("r1", "r2")]
            for w in workers:
                w.start()
            for w in workers:
                w.join()
        self.assertEqual(len(self.backend.media_sent), 1)
        self.assertEqual(sum("已发出" in r for r in results), 1)
        self.assertEqual(sum("确认卡" in r for r in results), 1)

    def test_a_repeated_request_answers_from_the_first_even_after_the_file_is_gone(self):
        args = {"chat": "c1", "file": self.file}
        self.describe(args)
        first = self.do(args)
        os.remove(self.file)
        self.assertEqual(self.do(args), first)
        self.assertEqual(len(self.backend.uploads), 1)

    def test_an_upload_that_fails_sends_nothing_and_may_be_retried(self):
        args = {"chat": "c1", "text": "附件", "file": self.file}
        self.describe(args)
        self.backend.upload_error = UploadError("上传第 1 块失败：HTTP 500")
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("文件「report.pdf」没有上传成功（上传第 1 块失败：HTTP 500），什么都没有发出", e.exception.message)
        self.assertEqual(self.backend.sent, [])
        self.assertEqual(self.backend.media_sent, [])
        self.backend.upload_error = ConnectionError("reset with a secret address")
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("（ConnectionError）", e.exception.message)
        self.assertNotIn("secret", e.exception.message)
        self.backend.upload_error = None
        self.assertEqual(self.do(args), {"text": "已发出（m_new，m_file_1）。"})
        self.assertEqual(self.backend.sent, [("c1", "附件", None)])

    def test_text_that_may_have_gone_out_stops_the_media_and_is_never_repeated(self):
        args = {"chat": "c1", "text": "图", "image": self.image}
        self.describe(args)
        self.backend.send_result = None
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("不要重发", e.exception.message)
        self.assertIn("图片还没有发", e.exception.message)
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("不要重发", e.exception.message)
        self.assertEqual(self.backend.media_sent, [])
        self.assertEqual(len(self.backend.uploads), 1)

    def test_a_media_message_that_may_have_gone_out_says_what_was_sent(self):
        args = {"chat": "c1", "text": "材料", "image": self.image, "file": self.file}
        self.describe(args)
        self.backend.media_results = [None]
        with self.assertRaises(RpcError) as e:
            self.do(args)
        text = e.exception.message
        self.assertIn("已发出（m_new）", text)
        self.assertIn("文件「report.pdf」没有发", text)
        self.assertIn("图片「shot.png」可能已经发出", text)
        self.assertIn("不要重发", text)
        # Never again: the same request gets what the first one ended with.
        self.assertEqual(self.do(args), {"text": text})
        self.assertEqual(len(self.backend.media_sent), 1)
        self.assertEqual(len(self.backend.sent), 1)

    def test_a_media_message_that_raises_with_no_text_is_unknown(self):
        args = {"chat": "c1", "image": self.image}
        self.describe(args)
        self.backend.media_results = [OSError("gateway")]
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("可能已经发出", e.exception.message)
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("不要重发", e.exception.message)
        self.assertEqual(len(self.backend.media_sent), 1)

    def test_secrets_oversized_and_private_files_are_refused_at_the_card(self):
        key = self.write("home/.ssh/id_rsa", b"example")
        env = self.write("proj/.env", b"EXAMPLE=1")
        big = self.write("out/huge.bin", b"x" * 11)
        self.c.private_dirs = (os.path.join(self.root, "data"),)
        private = self.write("data/larkx.log", b"example")
        cases = [({"chat": "c1", "file": key}, ".ssh"), ({"chat": "c1", "text": "配置", "file": env}, "凭据"),
                 ({"chat": "c1", "file": private}, "插件自己的数据"), ({"chat": "c1", "image": self.file}, "不是 PNG")]
        with mock.patch.object(outbound, "FILE_MOST", 10):
            cases.append(({"chat": "c1", "file": big}, "太大"))
            for args, words in cases:
                with self.subTest(args=args), self.assertRaises(RpcError) as e:
                    self.describe(args)
                self.assertEqual(e.exception.code, "invalid_params")
                self.assertIn(words, e.exception.message)
        self.assertEqual(self.c._cards, {})
        self.nothing_left_the_machine()

    def test_neither_the_text_nor_a_name_can_forge_a_card_line(self):
        forged = "看这个\n图片：holiday.png（PNG，1×1，1 字节）"
        summary = self.describe({"chat": "c1", "text": forged, "file": self.file})["summary"]
        self.assertLess(summary.index("文件：report.pdf"), summary.index("内容（全文）："))
        self.assertTrue(summary.endswith("内容（全文）：\n" + forged))
        with self.assertRaises(RpcError):
            self.describe({"chat": "c1", "file": self.root + "/out/a\n图片：x.png"})

    def test_signed_out_nothing_is_sent(self):
        args = {"chat": "c1", "image": self.image}
        self.describe(args)
        self.c._signed_in = False
        with self.assertRaises(RpcError):
            self.do(args)
        self.nothing_left_the_machine()

    def test_old_cards_are_dropped(self):
        args = {"chat": "c1", "image": self.image}
        self.describe(args)
        key = next(iter(self.c._cards))
        at, shown = self.c._cards[key]
        self.c._cards[key] = (at - 2 * 86400, shown)
        with self.assertRaises(RpcError) as e:
            self.do(args)
        self.assertIn("确认卡", e.exception.message)
        for i in range(70):
            self.describe({"chat": "c%d" % i, "image": self.image})
        self.assertLessEqual(len(self.c._cards), 64)


class WiringTest(Files):
    def test_the_data_directory_is_private_to_sending(self):
        data_dir = os.path.join(self.root, "data")
        plugin = Plugin({"id": "larkx", "version": "0", "contributes": {}}, lambda *_: Backend())
        plugin.initialize({"protocol": PROTOCOL, "extension": {"data_dir": data_dir}, "config": {}})
        self.addCleanup(plugin.connector.close)
        self.assertEqual(plugin.connector.private_dirs, (data_dir,))

    def test_the_fixture_backend_uploads_and_sends_media(self):
        backend = FixtureBackend()
        image = outbound.load("image", self.write("a.png", png()))
        up = backend.upload(image)
        self.assertEqual(up["kind"], "image")
        self.assertTrue(backend.send_media("c_dm", up, None))
        up = backend.upload(outbound.load("file", self.write("a.txt", b"hi")))
        self.assertEqual((up["kind"], up["name"], up["size"], up["mime"]), ("file", "a.txt", 2, "text/plain"))

    def test_release_keeps_a_finished_send(self):
        store = Store(os.path.join(self.root, "s.db"))
        self.assertEqual(store.claim_send("k"), (True, ""))
        store.release_send("k")
        self.assertEqual(store.claim_send("k"), (True, ""))
        store.finish_send("k", "已发出（m1）。")
        store.release_send("k")
        self.assertEqual(store.claim_send("k"), (False, "已发出（m1）。"))


if __name__ == "__main__":
    unittest.main()
