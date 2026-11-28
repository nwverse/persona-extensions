"""Local pictures and files that larkx_send and larkx_send_dm send as the user. Pure and LarkAgentX-free: the connector
reads a file here for the confirmation card (actions.describe) and again, checked against what the card showed, for
the send (actions.do). Nothing here uploads; only the backend does, and only from actions.do.

The path rules guard against an assistant mistake, a credential sent instead of a screenshot: they are no sandbox (the
program runs as the hub's user and can read what that user can), and the phone confirmation stays the real gate."""
import hashlib
import mimetypes
import os
import stat
import struct
import unicodedata

KINDS = ("image", "file")
# Feishu's own client takes larger files; these keep one actions.do call short and the file in memory once.
IMAGE_MOST = 10 << 20
FILE_MOST = 30 << 20
PATH_MOST = 4096
NAME_MOST = 255
# Shown on the card: enough of the SHA-256 to tell two versions of a file apart at a glance.
DIGEST_SHOWN = 12

# Directories whose files are credentials or the hub's and this program's own state, by path component (any case).
SECRET_DIRS = frozenset((
    ".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".password-store", ".persona", ".larkx", ".lark-cli",
    ".claude", ".codex",
))
# Pairs and triples of consecutive components, for credential stores inside an ordinary config directory.
SECRET_RUNS = (
    (".config", "gcloud"), (".config", "gh"), (".config", "op"), (".config", "shared-agent-config"),
    (".local", "share", "keyrings"), ("library", "keychains"), ("library", "cookies"),
)
# Whole system trees: devices, kernel state and system configuration are never a user's picture or document.
SYSTEM_ROOTS = ("/proc", "/sys", "/dev", "/run", "/etc", "/private/etc", "/var/run", "/private/var/run")
SECRET_NAMES = frozenset((
    ".netrc", "_netrc", ".git-credentials", ".npmrc", ".pypirc", ".pgpass", ".boto", ".s3cfg", ".dockercfg",
    "credentials", "credentials.json", "hub.json", "secrets.env", "authorized_keys", "known_hosts", "shadow",
    "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "id_ecdsa_sk", "id_ed25519_sk", "larkx.db",
))
SECRET_SUFFIXES = (".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".kdbx", ".keychain", ".keychain-db",
                   ".gpg", ".asc", ".ppk", ".ovpn")

# Picture formats Feishu shows inline, by their first bytes: anything else goes as a file.
IMAGE_TYPES = (
    (b"\x89PNG\r\n\x1a\n", "image/png", "PNG"),
    (b"\xff\xd8\xff", "image/jpeg", "JPEG"),
    (b"GIF87a", "image/gif", "GIF"),
    (b"GIF89a", "image/gif", "GIF"),
    (b"BM", "image/bmp", "BMP"),
)


class MediaError(ValueError):
    """A local file that cannot be sent; the message says why and what to do, in the user's language."""


def media_args(args):
    """The media an action asks for, in sending order: [(kind, path)]. Raises MediaError for a malformed value."""
    out = []
    for kind in KINDS:
        value = args.get(kind)
        if value is None or value == "":
            continue
        if not isinstance(value, str) or not value.strip():
            raise MediaError("%s 要是一个本地文件的绝对路径" % kind)
        out.append((kind, value))
    return out


def load(kind, path, private=()):
    """The file at path as it will be sent: a dict with kind, path, name, size, mime, label, sha256, width/height for
    a picture, and data, its bytes. Read through one descriptor that refuses a symbolic link, so what is checked is
    what is read. private: directories (this program's data directory) nothing may be sent from."""
    if kind not in KINDS:
        raise MediaError("不支持的媒体类型：%s" % kind)
    full = _checked_path(path)
    _refuse_secret(full)
    real = os.path.realpath(full)
    _refuse_secret(real)
    for root in private:
        if root and _inside(real, os.path.realpath(root)):
            raise MediaError("不能发送这个插件自己的数据（里面有登录凭据和聊天记录）：%s" % full)
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_NONBLOCK", 0) | getattr(os, "O_CLOEXEC", 0)
    try:
        fd = os.open(full, flags)
    except FileNotFoundError:
        raise MediaError("找不到这个文件：%s" % full) from None
    except IsADirectoryError:
        raise MediaError("这是一个目录，不是文件：%s" % full) from None
    except PermissionError:
        raise MediaError("没有权限读这个文件：%s" % full) from None
    except OSError as e:
        if os.path.islink(full):
            raise MediaError("这是一个符号链接，请直接给出它指向的文件：%s" % full) from None
        raise MediaError("打不开这个文件（%s）：%s" % (type(e).__name__, full)) from None
    most = IMAGE_MOST if kind == "image" else FILE_MOST
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode):
            raise MediaError("这不是普通文件（目录、设备、管道等不能发）：%s" % full)
        if st.st_size <= 0:
            raise MediaError("这个文件是空的：%s" % full)
        if st.st_size > most:
            raise MediaError("%s太大了：%s，上限 %s：%s" % ("图片" if kind == "image" else "文件", human_size(st.st_size),
                                                     human_size(most), full))
        chunks, size = [], 0
        while size <= most:
            chunk = os.read(fd, min(1 << 20, most + 1 - size))
            if not chunk:
                break
            chunks.append(chunk)
            size += len(chunk)
    finally:
        os.close(fd)
    data = b"".join(chunks)
    if len(data) > most:
        raise MediaError("文件在读取时变大了，超过上限 %s：%s" % (human_size(most), full))
    if not data:
        raise MediaError("这个文件是空的：%s" % full)
    name = os.path.basename(full)
    out = {"kind": kind, "path": full, "name": name, "size": len(data), "data": data,
           "sha256": hashlib.sha256(data).hexdigest()}
    if kind == "image":
        found = image_type(data)
        if not found:
            raise MediaError("这不是 PNG、JPEG、GIF、WebP 或 BMP 图片，要按原文件发送请改用 file：%s" % full)
        out["mime"], out["label"] = found
        size = image_size(data)
        if size:
            out["width"], out["height"] = size
    else:
        out["mime"] = mimetypes.guess_type(name)[0] or ""
        out["label"] = out["mime"] or "未知类型"
    return out


def fingerprint(item):
    """What the card promised about one file, compared when it is sent."""
    return item["kind"], item["path"], item["size"], item["sha256"]


def card_lines(item):
    """The confirmation card's lines for one file, without its bytes."""
    head = "图片" if item["kind"] == "image" else "文件"
    facts = [item["label"]]
    if item.get("width") and item.get("height"):
        facts.append("%d×%d" % (item["width"], item["height"]))
    facts.append(human_size(item["size"]))
    return ["%s：%s（%s）" % (head, item["name"], "，".join(facts)),
            "  位置：%s" % item["path"],
            "  SHA-256：%s…" % item["sha256"][:DIGEST_SHOWN]]


def human_size(n):
    if n < 1024:
        return "%d 字节" % n
    for unit in ("KB", "MB", "GB"):
        n /= 1024.0
        if n < 1024 or unit == "GB":
            return "%.1f %s" % (n, unit)
    return "%.1f GB" % n


def image_type(data):
    """(mime, label) for a picture Feishu shows inline, or None."""
    for magic, mime, label in IMAGE_TYPES:
        if data.startswith(magic):
            return mime, label
    if len(data) >= 12 and data[:4] == b"RIFF" and data[8:12] == b"WEBP":
        return "image/webp", "WebP"
    return None


def image_size(data):
    """(width, height) read from a picture's header, or None when it cannot be read. Only for the card."""
    try:
        if data.startswith(b"\x89PNG\r\n\x1a\n") and data[12:16] == b"IHDR":
            return struct.unpack(">II", data[16:24])
        if data[:6] in (b"GIF87a", b"GIF89a"):
            return struct.unpack("<HH", data[6:10])
        if data.startswith(b"BM") and len(data) >= 26:
            width, height = struct.unpack("<ii", data[18:26])
            return abs(width), abs(height)
        if data[:4] == b"RIFF" and data[8:12] == b"WEBP":
            chunk = data[12:16]
            if chunk == b"VP8X":
                return (int.from_bytes(data[24:27], "little") + 1, int.from_bytes(data[27:30], "little") + 1)
            if chunk == b"VP8L":
                bits = int.from_bytes(data[21:25], "little")
                return (bits & 0x3FFF) + 1, ((bits >> 14) & 0x3FFF) + 1
            if chunk == b"VP8 ":
                width, height = struct.unpack("<HH", data[26:30])
                return width & 0x3FFF, height & 0x3FFF
        if data.startswith(b"\xff\xd8"):
            return _jpeg_size(data)
    except struct.error:
        return None
    return None


def _jpeg_size(data):
    i = 2
    while i + 9 < len(data):
        if data[i] != 0xFF:
            return None
        marker = data[i + 1]
        if marker == 0xFF:
            i += 1
            continue
        if marker in (0xD8, 0x01) or 0xD0 <= marker <= 0xD7:
            i += 2
            continue
        length = struct.unpack(">H", data[i + 2:i + 4])[0]
        # SOF0..SOF15, except DHT (C4), JPG (C8) and DAC (CC): height then width.
        if 0xC0 <= marker <= 0xCF and marker not in (0xC4, 0xC8, 0xCC):
            height, width = struct.unpack(">HH", data[i + 5:i + 9])
            return width, height
        i += 2 + length
    return None


def _checked_path(path):
    if not isinstance(path, str) or not path.strip():
        raise MediaError("要给出本地文件的绝对路径")
    if len(path) > PATH_MOST:
        raise MediaError("路径太长了")
    # A control character in a name could forge a line of the confirmation card.
    if any(unicodedata.category(char) in ("Cc", "Cf", "Zl", "Zp") for char in path):
        raise MediaError("路径里不能有换行、制表符或其他控制字符")
    if path.startswith("~/"):
        path = os.path.expanduser(path)
    if not os.path.isabs(path):
        raise MediaError("要用绝对路径（以 / 开头），不能是相对路径：%s" % path)
    full = os.path.normpath(path)
    name = os.path.basename(full)
    if not name or len(name.encode("utf-8", "surrogatepass")) > NAME_MOST:
        raise MediaError("文件名是空的或太长了：%s" % full)
    return full


def _refuse_secret(path):
    lowered = path.replace("\\", "/").lower()
    for root in SYSTEM_ROOTS:
        if lowered == root or lowered.startswith(root + "/"):
            raise MediaError("不能发送系统目录里的文件：%s" % path)
    parts = [part for part in lowered.split("/") if part]
    name = parts[-1] if parts else ""
    dirs = parts[:-1]
    secret = next((part for part in dirs if part in SECRET_DIRS), None)
    if secret is None:
        for run in SECRET_RUNS:
            if any(tuple(dirs[i:i + len(run)]) == run for i in range(len(dirs) - len(run) + 1)):
                secret = "/".join(run)
                break
    if secret is not None:
        raise MediaError("不能发送 %s 目录里的文件，那里放的是密钥、登录凭据或程序自己的数据：%s" % (secret, path))
    if (name in SECRET_NAMES or name.startswith(".env") or name.endswith(".env") or name.endswith(SECRET_SUFFIXES)
            or any(name.startswith(key + ".") for key in ("id_rsa", "id_dsa", "id_ecdsa", "id_ed25519"))):
        raise MediaError("这个文件看起来是密钥、证书或凭据，不能发送：%s" % path)


def _inside(path, root):
    root = root.rstrip(os.sep) or os.sep
    return path == root or path.startswith(root + os.sep)
