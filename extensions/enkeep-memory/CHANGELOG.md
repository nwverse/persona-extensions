# enkeep-memory changelog

## 0.2.0-beta.9 (pre-release)

- **Moved to the public extension repository.** The extension now lives in `nwverse/persona-extensions` and is
  published to the official market; its id stays `enkeep-memory`, so a hub that has 0.2.0-beta.8 from another market
  upgrades in place and keeps its data. Its behaviour is unchanged. The offline replay and acceptance tools no
  longer carry environment defaults: `-model` is required, `-provider` is empty (the `pi` command's own default) and
  `-tz` defaults to UTC.

## 0.2.0-beta.8 (pre-release)

The curator is rebuilt around six things it learns about the user, all equally important, and the people the user
deals with: how the user works with the AI, how the AI works with machines and tools, the user's default judgements,
how the user handles kinds of tasks, the user's world (facts, relations, the landscape) and the user as a person.
Needs a hub from build 202611121555 on (data-directory contributions, read-only memory, question history paging and
decision fields, folded work with error counts).

- **One entry structure for every facet**, at three grains (one action, a kind of situation, an overall tendency),
  with evidence that says which source and who said it, an impact that names which assistant action changes and when,
  counts by chances and active days, an open type table per facet, and an append-only event log that replays to the
  same entries. The user's edits, pins, deletions (tombstones) and right / wrong marks are never overwritten.
- **More sources, read lightly.** Besides the knowledge base and the Persona chats: other agents' sessions (text and
  folded work only; a tool segment is opened only when a friction signal points at it), ask_user cards with their
  recommendations and who answered, to-dos and their decisions, "about me", and the assistant's hand-written memory.
  Every source that is missing (no grant, the hub lacks it, the assistant's computer away) is shown as such and
  nothing fails. A first-run replay of up to 90 days builds the start from the user's own history.
- **Speaker attribution before anything is learned from chit-chat.** Each line is resolved to the user, one person
  of the conversation, or nobody; chit-chat is kept when the speaker is known and the user deals with them, and the
  intersection list decides whom people cards are made for. Per-source attribution checks can stop a source from
  teaching people and relations.
- **One extraction prompt** (English instructions, the user's language as a parameter) for every facet, with real
  impact, language, sensitive-category (off by default, per category) and secret checks.
- **Consolidation**: model-judged recall and merging into kinds of situation, counter-examples, adaptive thresholds
  that count chances and active days, and an abstract gate that does not relax with little data. The old memory layer
  is migrated into entries once.
- **People cards**: role and responsibilities, how they work with the user, the user's own words about them, labelled
  inferences with their basis, and commitments either way; a separate tip for drafting that never quotes an
  evaluation. Facts go to people pages now; the full card waits for the hub's sectioned person profiles.
- **Exits**: a per-turn instructions block (6 KB, a budget per facet, empty sections left out), skills for machines
  and tools and for ways of working, agent project context, inbox context and never-quote lines, each rewritten line
  by line with a basis hash and read back from the hub.
- **The correction ledger and friction digs**: corrections filed against entries or as new issues, recurrences
  counted from when an entry took effect, and pitfalls learned from the segments a signal pointed at.
- **The page**: a start card per facet, a sources card, three skippable first-run questions, the facets and the
  cards, switches K1–K17 (per category, per fork type, per source where they apply), a check page for the user, and a
  weekly report through notify.
- **Acceptance tools**: `review` and `accept` work out the readings, samples and smoke checks from a state directory.

## 0.2.0-beta.6 (pre-release)

- **Proposal undo survives interrupted writes.** Each knowledge-base proposal records its original before-image in a
  durable write-ahead record before `kb.update`. Startup replays interrupted writes in the background without
  blocking initialization, one failed record stays queued without blocking later records, and every automatic
  application checkpoints separately, so retries preserve the value undo must restore.

## 0.2.0-beta.5 (pre-release)

- **Forgetting a source also scrubs migration backups.** `Forget` now applies the same source predicate to every
  `state.json.bak-*` without normalizing the rollback snapshot: unknown fields survive, unrelated backups keep their
  exact bytes and mtime, and only changed backups are atomically rewritten.

## 0.2.0-beta.3 (pre-release)

- **Digest past days again with the current prompt.** New setting `rerun_range` (`YYYY-MM-DD..YYYY-MM-DD`, at most 31
  days): each day in it gets a daily pass with the current daily prompt, one day at a time, once per prompt version.
  What an earlier prompt produced stays as it was (an item the new pass does not produce again is not touched, a
  rejected one stays rejected); a claim it produces again folds into the item that has it. It starts without the
  hub's UI: `personad extension config enkeep-memory rerun_range=…`. The log says
  `daily <day> starts (prompt=daily/6 …)` for each.

## 0.2.0-beta.2 (pre-release)

- **Group noise stays out of the memory.** A group's job postings, shop or campaign activities, giveaways and one
  member's answer to another were listed as facts, practices and knowledge ("待确认", "偶现") when the user neither
  said them nor was named in them. The extraction already throws such items away when they rest only on other
  people's lines that do not name the user (`NOT_ABOUT_OWNER`); a regression test now pins the cases. The daily prompt
  (`daily/6`) says so too, so the model stops proposing them.
- **Items from before the check are not left waiting.** The one-off start-up clean keeps an item that rests only on
  other people's words as a candidate. `clean-sources -reject-others` (extension stopped, `-dry` first) marks such
  items rejected instead: hidden, kept in the state with their citations, and backed up first. It never runs by
  itself.

## 0.2.0-beta.1 (pre-release)

A pre-release: for hubs that choose to try it. It replaces 0.1.x on upgrade (a pre-release is newer than 0.1.1, older
than 0.2.0).

### What changed since 0.1.0

- **Only the user's own words are the user's.** A memory entry rests only on lines the user wrote, and needs one that
  says something of its own (a bare "好" or "对" to the assistant does not count). An item rests on the user's words or
  on a person addressing the user by name. The assistant's replies, its paraphrases ("你之前说过…"), agents' drafts
  under the user's name, text pasted from an assistant, and bots are context, never evidence. Every cited line records
  its role (user, assistant, agent_draft, bot, other) and where it is.
- **Persona conversations** are read through the hub's `assistant.conversations` / `assistant.messages` (permission
  `assistant.read`, new): only conversations the user opened (origin user or channel), only words, and the hub's
  typed-in messages (task notices, supervision, inbox notes, wake-ups) dropped. Agents' own sessions are never read:
  what Persona sends them is role=user with no mark.
- **Tiers by source.** Own conversations give every layer; code review gives the user's own judgements and practices
  only; groups the user takes part in give facts and decisions that concern the user; groups the user keeps quiet in
  give people, projects and their relations only; noise days (feeds, alerts, big crowds) are skipped. Settings:
  `tiers`, `engaged_days`, `noise_members`.
- **Items are tightened.** No one-off actions or passing states, no money notices; two independent sources before an
  item is active or proposed for pinning; pins only on a person or project, never on the source chat; an item a
  memory entry already has is folded into it.
- **Fixes.** Generic words for the user ("用户", "我", "owner") never count as the user's name (mention windows); a
  weekly run touches only its own week; coverage alarms only for days a daily run covered.
- **Settings renamed** so the install preview no longer takes them for secrets: `admit_all_below_tokens` →
  `admit_all_below`, `private_keywords` → `privacy_keywords`. Values saved under the old names still count.
- **Upgrading from 0.1.x** cleans the old state once, on start: `state.json` is backed up in the extension's data
  directory (the newest three kept), and what rested on agents, the assistant or bots goes; the counts are on the
  health page and in the log.

### Needs

- A hub built from nwverse/persona at or after #1292 (`engines.persona >=202611090012`). An older hub shows the
  extension as incompatible.

### Known issues

- **Messages Persona sent as the user are not told apart.** A chat reply, comment or mail Persona sent in the user's
  name through a connector is archived like one the user typed. Only those with a recognisable mark (an agent's
  trace, pasted assistant text, the user named in the third person) are excluded; waiting on nwverse/persona#2814.
- **Mute, fold and done** are read when the hub reports them (nwverse/persona#1261, open); until then a group the user
  has not written in for `engaged_days` stands in for one the user never opens.
- **Background groups keep no entities yet**: the graph keeps people and projects only through relations, and there is
  no relation for "this person is in this group".
- **Items are the weakest layer**: in the blind evaluation (docs/extensions/enkeep-memory-tiers-eval.md) about half of
  them were useful. None is active or proposed for pinning from a single source.
- **Agents' drafts in code review** are recognised by their marks and shape (long, structured, status templates); a
  short drafted comment can pass as the user's.
- **Memory is not delivered to the assistant yet**: injection levels are computed and shown in the extension; giving
  them to the assistant needs hub interfaces that do not exist yet.
