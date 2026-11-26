# The periodic cross-event overview

Since 0.2.0-beta.48. The daily curation reads one day at a time and files what it sees: a correction here, a choice
there. It cannot see that the same way of deciding shows up in two unrelated projects, in a card answer and in an
agent session, week after week; nor that a way the user held last month is being dropped now. The overview
(`internal/overview`) is the layer that does: it reads the curated entries across days and keeps **patterns**.

It never changes the curated state. It reads the last published snapshot (`curate.Live`, the same copy the pages
read, so it never waits for a running pass and never holds one up) and keeps its own file, `overview-state.json`
beside `state.json`, written atomically (temporary file, fsync, rename).

## Four layers

| Layer | When | What | Model calls |
| --- | --- | --- | --- |
| L0 daily | `daily_at` | unchanged: actions, classes, signals | as before |
| **L1 weekly synthesis** | after the week's curation pass (Mondays, `weekly_at`), before the weekly report | the forward check, then per facet: candidates → synthesis → counter-search → gate | ≤ 12 synthesis (Sonnet), ≤ 6+ counter-search and 2 per arm of the forward check (Haiku) |
| **L2 monthly consolidation** | the first Monday of a month, after that week's L1, for the month before; once for the last complete month when none was consolidated yet | rescue quiet classes, merge alike patterns, counter-search again, lifecycle rules, then the spot check | ≤ 6 rescue + 6 merge + 3 spot check (Sonnet), counter-search (Haiku) |
| **L3 light refresh** | every snapshot the curator publishes (after a run, a mark, a delete, a forgotten source) | recount every pattern from what is still there | none |

All calls name their own models (`model.Request.Models`): `model_long` and `model_short` when they are Claude models,
else `claude-sonnet-5-5` and `claude-haiku-5-5`. The route's primary models are not used for the overview. Every call
waits for a turn of the process's shared limiter (four at once).

## L1: from scattered entries to a pattern

1. **Candidates.** For each of the six facets (people's cards feed ⑤ with their facts and commitments only; their
   evaluations and inferences never come in), the classes and abstract entries, and actions without a live class,
   with quoted evidence in the four weeks before the week's end (twelve on a first run, when no week was looked at
   yet: the history the user already has). They are what `memory.entries` may answer
   (`memsrc.Pool`), never an inferred entry, and no life content for a "work only" user. Most spread first; at most
   two batches of 90.
2. **Four-perspective projection.** Each candidate carries, counted by code, the projects and source kinds its
   evidence comes from (chat, card, agent, messages, self, knowledge base, recording), its ISO weeks, its independent
   sources (quoted `ref@day`) and the people involved. The synthesis prompt looks across time, project, people and
   source at once and proposes patterns with their member items, a `default_contrast` (what a capable stranger would
   do) and `holds_for_anyone`. Existing patterns are shown so a proposal extends one instead of duplicating it; a
   pattern's id is a hash of its facet, situation and choice, so it is found again.
3. **Gate (code).** A pattern is through only when its live evidence has at least **2 scenes** (projects or source
   kinds), **3 independent quoted unit·days**, **2 ISO weeks**, every member has a quoted line, and it has a default
   that differs from its text and does not hold for anyone. These never relax with little data: a new user's patterns
   stay candidates until the evidence is there.
4. **Counter-search.** For each pattern through the gate: its classes' recorded counter-examples, then the entries of
   its facet over sixty days most alike in words and not among its evidence (at most 8), judged by a model as real
   counter-examples or not, with an exception and whether the pattern is overturned. Then: overturned → `retired`;
   counter-examples ≥ 25% of counter-examples plus independent sources, mostly in the last thirty days → `drifting`;
   an exception → `scoped`; else `core_stable`. A pattern whose counter-search could not be answered stays a
   candidate: nothing is used unchecked.

The perspective a pattern is filed under is where its evidence differs most: source kinds, then projects, people,
time.

## L2: consolidation and drift

- **Rescue.** Dormant classes seen at least four times in the year before go through the synthesis again: the 40
  active-day dormancy no longer decides alone that a principle the user rarely needs is gone.
- **Merge.** Per facet, a model groups patterns that say the same thing; the kept one takes the others' evidence, the
  others are retired as merged.
- **Drift and eras.** Every pattern through the gate is put to its counter-examples again over the sixty days before
  the month's end. Drifting in two monthly passes in a row → retired. A used pattern whose every line was about a
  project and that has had no evidence for ninety days → `era_closed` (kept, not used).

## L3: the user's marks take effect at once

A pattern keeps the ids of every entry it rests on. On each published snapshot the evidence is counted again from the
entries still usable; a used pattern that no longer passes the gate (an entry marked wrong, deleted, a source
forgotten) leaves `memory.entries` at once, keeping the lifecycle it had in `withdrawn`; when the evidence comes back
(marked right, restored) so does the lifecycle. A pattern whose entries are all deleted is retired. A curation merge of
classes is followed (`merged_into`). A snapshot that changes nothing for the patterns writes nothing.

## Exit: memory.entries

Used patterns (`core_stable`, `scoped`) are answered by the extension's own memory source beside the curated entries
(`memsrc.PageWith`), with the same order and cursor. The connector and contribution ids are unchanged; nothing is
filtered by scene here.

| Entry | Standing | Rank | Categories, space |
| --- | --- | --- | --- |
| `ov:pattern:<id>`: no category and no space among its evidence and counter-examples | when chosen for the standing set (docs/exits.md, by score with the curated entries) | the set's rank when standing, else its score × 9 | none |
| `ov:cat:<category>:<id>`: any categorised evidence, or said in a space | never | 900 | the union of the evidence's (taint), and the space |

The text is one line: `跨事模式（<facet>）：<pattern>（例外：<exception>）`; a pattern whose evidence goes both ways (beta.52) says both ends instead, each with when it holds: `（两端：<A>时，<X>；<B>时，<Y>）`. Subjects are the evidence's names.

## The weekly report

The weekly report waits for the week's L1 (or its failure) and is written in full, with the overview's section
(coverage per facet, the patterns confirmed or moved, the drifting ones, the last forward check), to
`overview-reports/weekly/<week>.md`; the monthly one to `overview-reports/monthly/<month>.md`. The push is a summary of
at most **300 characters** pointing to the start tab's **Periodic overview** page, where every report is shown in
parts a page carries. The hub refuses a notification body over 1000 characters (`body is too long`); categorised
patterns are counted, never quoted, in the report and the summary.

## Checking it works

- **Forward check** (every week once a pattern is used; on a first run, once, as a backtest; and `enkeep_overview_run`
  `backtest`): the real choices first seen in a week (actions
  of ①–④ with a situation, a choice and what was turned down; at most 30, picked by a hash) become two-option
  questions, the options' order by a hash. A model answers them three times: with the used patterns kept before the
  week, with the scattered entries in memory.entries' order in as many characters, and with no memory. The answer is
  scored by code. Patterns resting on a question's entry or its class, and those entries themselves, are left out of
  both memories; nothing categorised is in either. A backtest builds its patterns only from evidence before the week,
  in a scratch state (`overview-backtest.json`); the kept patterns are not touched.
- **Spot check** (after every monthly pass, and `enkeep_overview_run` `audit`): up to 30 used patterns, spread over the facets, each judged by a
  model from its quotes alone (right, mostly right, one-off, generic, unsupported). It is a model's verdict kept for the
  user to go over, not the user's.

## Tools

- `enkeep_overview_status` (read): patterns per facet by lifecycle, the last runs, forward checks and spot check, the
  weekly report's push, and the overview entries memory.entries answers.
- `enkeep_overview_run` (write, through the hub's gate): `weekly` (period `YYYY-Www`), `monthly` (`YYYY-MM`),
  `backtest` (`YYYY-Www`), `audit`, or `report` (`YYYY-Www`: send a week's summary again).
