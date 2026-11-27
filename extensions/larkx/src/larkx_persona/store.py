"""What the program has seen, in SQLite in the plugin's data directory: status and inbox.collect answer from here without
the network, and the archive's cursor is a row number."""
import json
import os
import sqlite3
import threading

SCHEMA = """
CREATE TABLE IF NOT EXISTS chats (
  id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT 'group', last_at REAL NOT NULL DEFAULT 0,
  mute TEXT NOT NULL DEFAULT 'unknown', checked_at REAL NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS messages (
  seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, chat TEXT NOT NULL, position INTEGER NOT NULL DEFAULT 0,
  sender TEXT NOT NULL DEFAULT '', sender_name TEXT NOT NULL DEFAULT '', mine INTEGER NOT NULL DEFAULT 0,
  type TEXT NOT NULL DEFAULT '', text TEXT NOT NULL DEFAULT '', at REAL NOT NULL DEFAULT 0,
  root TEXT NOT NULL DEFAULT '', parent TEXT NOT NULL DEFAULT '', at_me INTEGER NOT NULL DEFAULT 0,
  media TEXT NOT NULL DEFAULT '[]', mentions TEXT NOT NULL DEFAULT '[]', markdown TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS messages_chat ON messages (chat, position);
CREATE INDEX IF NOT EXISTS messages_topic ON messages (chat, root) WHERE root NOT IN ('', '0');
CREATE TABLE IF NOT EXISTS names (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS chatter_types (id TEXT PRIMARY KEY, bot INTEGER NOT NULL, expires REAL NOT NULL);
CREATE TABLE IF NOT EXISTS thread_ids (chat TEXT NOT NULL, root TEXT NOT NULL, thread TEXT NOT NULL, PRIMARY KEY (chat, root));
CREATE TABLE IF NOT EXISTS sends (key TEXT PRIMARY KEY, result TEXT);
CREATE TABLE IF NOT EXISTS auto_topics (
  account TEXT NOT NULL, scope TEXT NOT NULL, scenario TEXT NOT NULL DEFAULT '', handled REAL NOT NULL DEFAULT 0,
  thread TEXT NOT NULL DEFAULT '', active REAL NOT NULL DEFAULT 0, PRIMARY KEY (account, scope));
CREATE TABLE IF NOT EXISTS agent_sessions (
  account TEXT NOT NULL, thread TEXT NOT NULL, machine TEXT NOT NULL, session TEXT NOT NULL, updated REAL NOT NULL,
  PRIMARY KEY (account, thread));
CREATE TABLE IF NOT EXISTS agent_runs (
  account TEXT NOT NULL, request TEXT NOT NULL, thread TEXT NOT NULL, machine TEXT NOT NULL, session TEXT NOT NULL,
  sent REAL NOT NULL, auto INTEGER NOT NULL, cursor INTEGER NOT NULL DEFAULT 0, baseline INTEGER, PRIMARY KEY (account, request));
CREATE TABLE IF NOT EXISTS auto_bot_waits (
  account TEXT NOT NULL, chat TEXT NOT NULL, root TEXT NOT NULL, bot TEXT NOT NULL, scenario TEXT NOT NULL,
  batch TEXT NOT NULL, timeout REAL NOT NULL, deadline REAL NOT NULL, sending INTEGER NOT NULL,
  PRIMARY KEY (account, chat, root));
"""


class Store:
    def __init__(self, path):
        fd = os.open(path, os.O_CREAT | os.O_RDWR, 0o600)
        os.close(fd)
        self._lock = threading.Lock()
        self._db = sqlite3.connect(path, check_same_thread=False)
        self._db.row_factory = sqlite3.Row
        self._db.executescript(SCHEMA)
        # Databases from before self-mentions have no mention metadata column. Keep those messages inert.
        columns = {r["name"] for r in self._db.execute("PRAGMA table_info(messages)")}
        if "auto_account" not in columns:
            self._db.execute("ALTER TABLE messages ADD COLUMN auto_account TEXT NOT NULL DEFAULT ''")
        if "mentions" not in columns:
            self._db.execute("ALTER TABLE messages ADD COLUMN mentions TEXT NOT NULL DEFAULT '[]'")
        # Older messages have no verified safe Markdown representation and keep their original plain-text display.
        if "markdown" not in columns:
            self._db.execute("ALTER TABLE messages ADD COLUMN markdown TEXT NOT NULL DEFAULT ''")
        # Older chats have no verified mute state. Keep their existing inbox behavior until a refresh succeeds.
        columns = {r["name"] for r in self._db.execute("PRAGMA table_info(chats)")}
        if "mute" not in columns:
            self._db.execute("ALTER TABLE chats ADD COLUMN mute TEXT NOT NULL DEFAULT 'unknown'")
        if "checked_at" not in columns:
            self._db.execute("ALTER TABLE chats ADD COLUMN checked_at REAL NOT NULL DEFAULT 0")
        columns = {r["name"] for r in self._db.execute("PRAGMA table_info(auto_bot_waits)")}
        if "retries" not in columns:
            self._db.execute("ALTER TABLE auto_bot_waits ADD COLUMN retries INTEGER NOT NULL DEFAULT 0")
        columns = {r["name"] for r in self._db.execute("PRAGMA table_info(agent_runs)")}
        if "baseline" not in columns:
            # In-flight runs from before positional matching must still find their own marker.
            self._db.execute("ALTER TABLE agent_runs ADD COLUMN baseline INTEGER")
        # 0.1 named a direct chat it saw only through the user's own messages 私聊 ("direct chat") for good.
        self._db.execute("UPDATE chats SET name = '' WHERE kind = 'dm' AND name = '私聊'")
        self._db.commit()

    def close(self):
        with self._lock:
            self._db.close()

    def _all(self, sql, *args):
        with self._lock:
            return [dict(r) for r in self._db.execute(sql, args).fetchall()]

    def _one(self, sql, *args):
        rows = self._all(sql, *args)
        return rows[0] if rows else None

    def _do(self, sql, *args):
        with self._lock:
            cur = self._db.execute(sql, args)
            self._db.commit()
            return cur.rowcount

    def add_message(self, m, sender_name, mine):
        """Keeps a message once; True when it is new."""
        if m.get("thread_id"):
            self.remember_thread(m["chat"], m.get("root") or m["id"], m["thread_id"])
        return self._do(
            "INSERT OR IGNORE INTO messages (id, chat, position, sender, sender_name, mine, type, text, at, root, parent, at_me, media, mentions, markdown)"
            " VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            m["id"], m["chat"], m.get("position") or 0, m.get("sender") or "", sender_name, 1 if mine else 0,
            m.get("type") or "", m.get("text") or "", m.get("at") or 0, m.get("root") or "", m.get("parent") or "",
            1 if m.get("at_me") else 0, json.dumps(m.get("media") or [], ensure_ascii=False),
            json.dumps(m.get("mentions") or [], ensure_ascii=False), m.get("markdown") or "") > 0

    def touch_chat(self, chat, kind, at):
        self._do("INSERT INTO chats (id, kind, last_at) VALUES (?, ?, ?)"
                 " ON CONFLICT (id) DO UPDATE SET last_at = MAX(last_at, excluded.last_at), kind = excluded.kind", chat, kind, at)

    def name_chat(self, chat, name):
        self._do("UPDATE chats SET name = ? WHERE id = ?", name, chat)

    def mute_chat(self, chat, mute, checked_at):
        if mute not in ("muted", "normal", "unknown"):
            raise ValueError("unknown chat mute state")
        self._do("UPDATE chats SET mute = ?, checked_at = ? WHERE id = ?", mute, checked_at, chat)

    def chat(self, chat):
        return self._one("SELECT * FROM chats WHERE id = ?", chat)

    def chats(self, limit, query=""):
        like = "%" + query + "%"
        return self._all("SELECT * FROM chats WHERE name LIKE ? ORDER BY last_at DESC LIMIT ?", like, limit)

    def chats_since(self, at, limit):
        return self._all("SELECT * FROM chats WHERE last_at >= ? ORDER BY last_at DESC LIMIT ?", at, limit)

    def message(self, msg_id):
        return _message(self._one("SELECT * FROM messages WHERE id = ?", msg_id))

    def messages(self, chat, before, limit):
        """Up to limit messages of chat, oldest first: the newest ones, or those before position before."""
        where, args = "chat = ?", [chat]
        if before:
            where, args = "chat = ? AND position < ?", [chat, before]
        rows = self._all("SELECT * FROM messages WHERE %s ORDER BY position DESC, at DESC, seq DESC LIMIT ?" % where, *args, limit)
        return [_message(r) for r in reversed(rows)]

    def inbox_topics(self, chat, since, limit):
        """Recent group messages partitioned by topic, with the empty key for chat-level traffic."""
        rows = self._all(
            """
            WITH scoped AS (
                SELECT m.seq, m.position, m.at,
                    CASE WHEN m.root NOT IN ('', '0') THEN m.root
                         WHEN EXISTS (SELECT 1 FROM messages AS reply
                                      WHERE reply.chat = m.chat AND reply.root = m.id
                                        AND reply.root NOT IN ('', '0') AND reply.type != 'SYSTEM') THEN m.id
                         ELSE '' END AS topic
                FROM messages AS m WHERE m.chat = ? AND m.at >= ? AND m.type != 'SYSTEM'
            ), ranked AS (
                SELECT seq, topic, ROW_NUMBER() OVER (
                    PARTITION BY topic ORDER BY position DESC, at DESC, seq DESC
                ) AS rank FROM scoped
            )
            SELECT messages.*, ranked.topic FROM ranked JOIN messages USING (seq)
            WHERE ranked.rank <= ? ORDER BY ranked.topic, ranked.rank DESC
            """, chat, since, limit)
        topics = {}
        for row in rows:
            topic = row.pop("topic")
            topics.setdefault(topic, []).append(_message(row))
        return topics

    def topic_messages(self, chat, root, before, limit):
        rows = self._all(
            """SELECT * FROM messages WHERE chat = ? AND (root = ? OR id = ?)
               AND (? = 0 OR position < ?) ORDER BY position DESC, at DESC, seq DESC LIMIT ?""",
            chat, root, root, before, before, limit)
        return [_message(row) for row in reversed(rows)]

    def oldest_position(self, chat):
        row = self._one("SELECT MIN(position) AS p FROM messages WHERE chat = ? AND position > 0", chat)
        return (row or {}).get("p") or 0

    def newest_position(self, chat):
        row = self._one("SELECT MAX(position) AS p FROM messages WHERE chat = ?", chat)
        return (row or {}).get("p") or 0

    def after(self, seq, limit):
        """Up to limit messages kept after row seq, in the order they were kept."""
        return [_message(r) for r in self._all("SELECT * FROM messages WHERE seq > ? ORDER BY seq LIMIT ?", seq, limit)]

    def prune(self, before_at, upto_seq):
        """Deletes the messages from before before_at among rows up to upto_seq; how many went."""
        return self._do("DELETE FROM messages WHERE at < ? AND seq <= ?", before_at, upto_seq)

    def name(self, key):
        row = self._one("SELECT name FROM names WHERE id = ?", key)
        return row["name"] if row else ""

    def set_name(self, key, name):
        self._do("INSERT INTO names (id, name) VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET name = excluded.name", key, name)

    def claim_send(self, key):
        """Takes key for one send. Returns (True, "") the first time, else (False, what that send ended with): a send
        that was claimed and has no result may or may not have gone out, and is never tried again."""
        with self._lock:
            cur = self._db.execute("INSERT OR IGNORE INTO sends (key) VALUES (?)", (key,))
            self._db.commit()
            if cur.rowcount > 0:
                return True, ""
            row = self._db.execute("SELECT result FROM sends WHERE key = ?", (key,)).fetchone()
        return False, row["result"] or ""

    def mark_auto_request(self, msg_id, account):
        self._do("UPDATE messages SET auto_account = ? WHERE id = ?", account, msg_id)

    def auto_topic(self, account, scope):
        return self._one("SELECT * FROM auto_topics WHERE account = ? AND scope = ?", account, scope)

    def auto_handled(self, account, scope, scenario, now):
        self._do("INSERT INTO auto_topics (account, scope, scenario, handled) VALUES (?, ?, ?, ?)"
                 " ON CONFLICT(account, scope) DO UPDATE SET scenario = excluded.scenario, handled = excluded.handled",
                 account, scope, scenario, now)

    def dm_thread(self, account, chat, now, idle_hours):
        """Manual and automatic requests arriving together must choose the same conversation."""
        scope = "dm:" + chat
        with self._lock, self._db:
            row = self._db.execute("SELECT thread, active FROM auto_topics WHERE account = ? AND scope = ?",
                                   (account, scope)).fetchone()
            thread = scope
            if row and row["thread"]:
                thread = scope + ":%d" % now if now - row["active"] > idle_hours * 3600 else row["thread"]
            self._db.execute("INSERT INTO auto_topics (account, scope, thread, active) VALUES (?, ?, ?, ?)"
                             " ON CONFLICT(account, scope) DO UPDATE SET thread = excluded.thread, active = excluded.active",
                             (account, scope, thread, now))
            return thread

    def agent_session(self, account, thread):
        return self._one("SELECT * FROM agent_sessions WHERE account = ? AND thread = ?", account, thread)

    def bind_agent_run(self, run, chat, root):
        with self._lock, self._db:
            self._db.execute("INSERT OR REPLACE INTO agent_sessions VALUES (:account, :thread, :machine, :session, :sent)", run)
            self._db.execute("INSERT INTO agent_runs (account, request, thread, machine, session, sent, auto, cursor, baseline)"
                             " VALUES (:account, :request, :thread, :machine, :session, :sent, :auto, :cursor, :baseline)", run)
            # A crash before the bot worker clears its wait must not submit the same fallback a second time.
            self._db.execute("DELETE FROM auto_bot_waits WHERE account = ? AND chat = ? AND root = ? AND sending = 2"
                             " AND json_extract(batch, '$[#-1].id') = ?", (run["account"], chat, root, run["request"]))

    def agent_busy(self, account, thread):
        return self._one("SELECT 1 FROM agent_runs WHERE account = ? AND thread = ? LIMIT 1", account, thread)

    def agent_runs(self, account):
        return self._all("SELECT * FROM agent_runs WHERE account = ? ORDER BY sent", account)

    def agent_cursor(self, run, cursor):
        self._do("UPDATE agent_runs SET cursor = ? WHERE account = ? AND request = ?", cursor, run["account"], run["request"])

    def finish_agent_run(self, run):
        self._do("DELETE FROM agent_runs WHERE account = ? AND request = ?", run["account"], run["request"])

    def remember_thread(self, chat, root, thread):
        # Only distinct ids need a mapping. A decoder may use root as a fallback anchor.
        if thread and thread not in ("0", root):
            self._do("INSERT OR REPLACE INTO thread_ids VALUES (?, ?, ?)", chat, root, thread)

    def thread_id(self, chat, root):
        row = self._one("SELECT thread FROM thread_ids WHERE chat = ? AND root = ?", chat, root)
        return row["thread"] if row else None

    def chatter_type(self, sender, now):
        return self._one("SELECT bot, expires FROM chatter_types WHERE id = ? AND expires > ?", sender, now)

    def set_chatter_type(self, sender, bot, expires):
        self._do("INSERT OR REPLACE INTO chatter_types VALUES (?, ?, ?)", sender, bot, expires)

    def bot_id(self, name, configured=""):
        """Use the explicit numeric id, or the most recently observed numeric match across all chats."""
        if configured:
            return configured if configured.isascii() and configured.isdecimal() else ""
        row = self._one("SELECT id FROM (SELECT sender AS id, at, seq FROM messages WHERE sender_name = ?"
                        " UNION ALL SELECT CAST(json_extract(j.value, '$.id') AS TEXT), m.at, m.seq"
                        " FROM messages m, json_each(m.mentions) j WHERE json_extract(j.value, '$.label') = ?)"
                        " WHERE id != '' AND id NOT GLOB '*[^0-9]*' ORDER BY at DESC, seq DESC LIMIT 1", name, "@" + name)
        return row["id"] if row else ""

    def begin_bot_wait(self, account, chat, root, bot, scenario, batch, timeout, now):
        self._do("INSERT INTO auto_bot_waits (account, chat, root, bot, scenario, batch, timeout, deadline, sending)"
                 " VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)", account, chat, root, bot, scenario, json.dumps(batch), timeout, now + timeout)

    def arm_bot_wait(self, account, chat, root, now):
        # A reply during send already deleted the row. Never recreate it.
        self._do("UPDATE auto_bot_waits SET sending = 0, deadline = ? + timeout WHERE account = ? AND chat = ? AND root = ?",
                 now, account, chat, root)

    def resume_bot_waits(self, account):
        # Resume interrupted bot sends (1) and assistant handovers (2) by checking history, never resending to the bot.
        self._do("UPDATE auto_bot_waits SET sending = 0 WHERE account = ? AND sending != 0", account)

    def confirm_bot(self, account, m, wait_root=None):
        # A history page can predate another wait in the same chat. Only apply it to the wait that requested it.
        return self._do("DELETE FROM auto_bot_waits WHERE account = ? AND chat = ? AND bot = ?"
                        " AND (? = '' OR root = ? OR root = ?) AND (? IS NULL OR root = ?) AND sending != 2",
                        account, m["chat"], m.get("sender"), m.get("root") or "", m.get("root"), m["id"], wait_root, wait_root)

    def defer_bot_wait(self, item, deadline):
        return self._do("UPDATE auto_bot_waits SET deadline = ?, retries = retries + 1 WHERE account = ? AND chat = ? AND root = ?",
                        deadline, item["account"], item["chat"], item["root"])

    def claim_bot_fallback(self, item):
        # A live reply may have cleared the wait while history was in flight.
        return self._do("UPDATE auto_bot_waits SET sending = 2 WHERE account = ? AND chat = ? AND root = ? AND sending = 0",
                        item["account"], item["chat"], item["root"])

    def next_bot_wait(self, account):
        return self._one("SELECT * FROM auto_bot_waits WHERE account = ? AND sending = 0 ORDER BY deadline LIMIT 1", account)

    def forget_bot_wait(self, item):
        self._do("DELETE FROM auto_bot_waits WHERE account = ? AND chat = ? AND root = ?",
                 item["account"], item["chat"], item["root"])

    def finish_send(self, key, result):
        self._do("UPDATE sends SET result = ? WHERE key = ?", result, key)


def _message(row):
    if row is None:
        return None
    row = dict(row)
    row["media"] = json.loads(row.get("media") or "[]")
    row["mentions"] = json.loads(row.get("mentions") or "[]")
    row["mine"] = bool(row.get("mine"))
    row["at_me"] = bool(row.get("at_me"))
    return row
