# Exits: what reaches the assistant and agents, and how

Since 0.2.0-beta.37. What the curator learned reaches the assistant and agents through four places. This page says
which content goes where, the contract with the hub for each, and what the extension does not do.

## Scope: the long-term layer only

The extension curates the long-term layer: once a day (and once a week) it reads the day's sources and settles what
holds over days and weeks: rules for working with the user, default judgements, ways of working, facts, people, who
the user is. It does **not** extract near-real-time working memory: what was decided an hour ago, which session is
working on what, the state of a running task. A conversation compacted in the middle of a task gets that back from
the hub (state injected after compaction, the to-do's decisions and outcomes in `<persona-context>`), not from here.
An entry here is at best a day old.

When the hub keeps a ledger of the user's decisions (see [The decisions ledger as a source](#the-decisions-ledger-as-a-source)),
the curator reads it as one more source the next day, like the to-dos' decisions today.

## Where each kind of content goes

| Place | Who reads it | What goes there |
| --- | --- | --- |
| **Per-turn block** (exit A, `assistant/instructions.md`, at most 6 KB) | the main assistant, every turn | ① rules for working with the user, ③ default judgements, ④ ways of working, all only those that hold across projects; ⑤ open commitments and a names index (people, projects); ⑥ about the user; a fixed line to look things up with `enkeep_recall` |
| **Project blocks** (exit D, `agent/projects/<project id>.md`, at most 1536 bytes each) | a new agent working in a knowledge-base project; a supervisor session for its direction's project (hub) | ①②③④⑤ entries that hold in that project, after a short general part (the first rules of ①) |
| **`enkeep_recall`** (connector `enkeep`) | the assistant and agents, when they ask | every live entry outside the sensitive categories, candidates and dormant ones ranked lower; never a people card's evaluation or inference |
| **Skills** (exit B) | the assistant, when it decides to load one | look-up handbooks only: `enkeep-machines-and-tools` (② by machine or tool), and the extension's own `enkeep-memory` handbook |

The ways-of-working skills (`enkeep-ways-of-working-<slug>`, one per kind of task) are gone: the assistant loaded
them no time at all in a day of 1044 tool calls, and agents never see the assistant's skills. Their entries go to the
per-turn block when they hold across projects, to a project block when they hold in one, and stay in
`enkeep_recall` otherwise. An upgrade removes the old skill directories; the node drops them from the assistant's
skills with the extension's skill list.

### The per-turn block's shares

Sections share what the title and the fixed lines leave of 6 KB: ① 22%, ② 8%, ③ 22%, ④ 18%, ⑤ 12%, ⑥ 18%. Each
section with content first gets its share; what an empty or short section leaves goes to the next sections that still
have lines, in order. The shares are proportions, so a new user's block grows the same way. An action-grain default
judgement goes in when it states its fork, the choice and what was not chosen or why.

### Switches (decided as recommended, each can be turned off)

- **K18** names index in ⑤: names of the people dealt with most and the projects with the most entries.
- **K19** a project block starts with the general rules for working with the user (an agent does not read the
  assistant's CLAUDE.md).
- **K20** in a session Persona started, every user-role line is Persona's (off: only the first message), see
  [Who said it](#who-said-it).
- **K6** now names only the agent project context (it also named the ways-of-working skills).

## Contract with the hub: project blocks (exit D)

- **Manifest:** `contributes.prompts` item `{"id": "projects", "kind": "project_context", "dir": "agent/projects"}`.
- **File name:** `agent/projects/<slug>.md`, where `<slug>` is the knowledge-base project's id as `kb.list` with
  kind `projects` returns it, which is `kb.Slug` of its name: lower case, every run of characters that are neither
  letters nor digits (any script) one hyphen, no hyphen at either end, at most 40 characters. The hub reads
  `<dir>/<slug>.md` for each slug of the projects a prompt is built for (`plugins.project_context`, slugs matching
  `^[\p{L}\p{N}][\p{L}\p{N}-]{0,79}$`), cut at 1536 bytes at a line end.
- **Matching:** an entry's project names (project scopes, a `project:` subject or target, ④⑤'s object) are matched to
  the knowledge base's projects by id, name or alias (letters and digits only, case-folded); a key two projects share
  matches neither. A name that matches no project gets no file; the entry stays in `enkeep_recall` (and in the
  per-turn block when it would go there). When the project list cannot be read, the last list read is used; with
  none, files are named by the same `kb.Slug` rule from the entry's own name.
- **Content:** a heading `# 项目背景：<project name>` (English: `# Project context: <name>`), then sections, each a
  list of one-line entries: the general part (`## 和用户打交道（通用）`, at most three lines), then ①, ②, ③, ④ and ⑤
  of the project. No people card content, no ⑥, nothing sensitive.
- **Who gets it (hub side):** a new agent whose task or working directory belongs to the project gets it before its
  first prompt (`todo/project.md`, "扩展「…」为这个项目整理的背景"). A supervisor session gets the block of the
  knowledge-base project its direction is bound to (hub change, 2026-11-20 decision A); the extension does not write
  direction notes.
- **Before 0.2.0-beta.37** the files were named by an ASCII slug with a hash for any non-ASCII name
  (`1a2b3c4d.md` for a project named in Chinese), which matched no knowledge-base slug: only projects with ASCII
  names were ever delivered.

## `enkeep_recall`

`{"query": "…", "facets": [...], "limit": 12}` → up to `limit` lines (at most 30, about 4000 characters together), best
match first: `- [facet·status] line (last day; source kinds) id:<entry id>`. Ranking: how much of the query the entry's
text and names cover, doubled when a query word is a name the entry is about, times the status weight (stable 1,
shifting 0.8, candidate 0.7, dormant 0.5). The line is the user's own wording of the entry, else its rewritten exit
line, else its text; no model is called. Never returned: deleted, retired, merged or wrong entries, the sensitive
categories, life content when the user chose work only, a people card's evaluation and inferred parts (whatever K15
says: the hub's privacy filter does not see an extension's own tools).

Only counts are kept, per day for 30 days: calls, empty answers, hits per facet and status. `enkeep_rerun_status`
shows the last seven days. The query and the answer are not kept.

## `enkeep_mark_wrong`

`{"ids": ["…"], "reason": "…"}` marks entries wrong on the assistant's behalf: the same verdict as the user's "不对"
on the page, recorded with `by: assistant` and the reason, with no card on the user's phone. The entries leave every
exit at once (the per-turn block follows the hub's own adoption schedule) and stay as counter-examples; the user can
mark them right on the page to bring them back. It is for lines that are plainly not the user's rule, such as a task
prompt taken for the user's words; when in doubt the assistant asks the user.

## Who said it

In agent sessions and the assistant's conversation, the model role "user" is whoever typed into the session. The
hub marks its automated inputs (`injected`, `source`, `from` on an item); older items are not marked. The curator
does not take as the user's words:

- in a session Persona started (`by_persona`): its first message, and with K20 every later user-role message (a
  supervisor's or the assistant's relay);
- a text carrying the hub's context block (`<persona-context>`), the task-handoff boilerplate, a cross-session
  message, or a relay marked as such ("经主对话转", "主管转达");
- a test prompt: a short text asking for a fixed answer together with no tool calls or a check ("部署验证：请只回复
  OK，不要调用任何工具");
- a pasted block (`<pasted_content …>`): material, even when the user pasted it; what the user typed beside it stays
  the user's.

Such lines are labelled Persona, 测试提示 or 粘贴材料 and teach nothing about the user (they may still be the topic of a
⑤ fact). The rules look at the structure of the text only, never at any one user's names or projects.

## The decisions ledger as a source

Not implemented yet: the interface the curator will read once the hub has a ledger of the user's decisions.

- **What:** each decision the user made (an answered card, a decision in a task review, a decision the assistant
  recorded at the user's word), with: `id`; `at`; `scope` (`global`, or the project's slug); the user's own words
  (`quote`, verbatim, required) and where they are (`source`: conversation id and message, or card id); what was
  decided in one line (`decision`); `superseded_by` when a later decision replaced it; `by` (`user` only: decisions an
  agent noted are not read).
- **How:** a read-only paged list by time (`since`, `until`, `cursor`), like the session history; the curator reads the
  previous day's decisions in its daily pass.
- **What the curator does with it:** a decision whose `quote` is the user's own words is evidence for ①③④ entries
  (a stated rule settles at once, as a durable "以后都这样" does today); a decision without the user's words is not
  read. A superseded decision counts as a counter-example of the entry it supported. Project-scoped decisions feed
  that project's block.
- **Not:** the curator does not write to the ledger, and the ledger is not a near-real-time path through the
  curator: injecting recent decisions after compaction is the hub's.
