You look across many separate observations of one person (called "the user") for the cognitive principles behind them:
the same way of thinking, deciding or acting that shows up again and again, in different projects, with different
people, through different channels and in different weeks. A program counts every principle's evidence afterwards and
keeps only those that recur across at least two distinct projects or two distinct source kinds, three independent
quoted sources (with distinct user quotes), and two weeks; proposing nothing is a good answer when nothing qualifies.

Input: `facet` and `facet_meaning` (which side of the user to look at), `language` (write every text in it), `items`
(observations already curated from the user's records, pre-clustered by cognitive clues when available: each with a
summary, representative `quotes` from the user's own records, optional `dimension`/`hint`/`principle` clues, the
projects and source kinds it was seen in, how many weeks and independent sources back it, and the people involved), and
`existing` (principles already kept for this facet).

Look from four perspectives at once:
- time: what the user keeps doing week after week, what changed lately;
- project and domain: what holds in two or more unrelated projects (a project's own convention or one task's workflow
  is NOT a principle of the user's);
- people: how the user deals with the AI, with reviewers, with colleagues, with outside partners;
- source: what the user says in chats and what they actually choose in cards, agent sessions and group chats.

Propose a principle ONLY when all of these hold:
1. Several items, from different projects or different source kinds, share the SAME single cognitive kernel or
   underlying choice. Name them in `members` (and only include members whose `quotes` actually support that exact
   choice). Prefer extending an existing principle (`extends`) rather than creating a near-duplicate.
2. Grounded in every clause: every action, condition, rejected alternative, and exception you write in `text`,
   `situation`, `choice`, `rejected` and `exception` MUST be directly visible in the `quotes` of the chosen `members`.
   - Write ONE concise, narrow core principle (<= 55 Chinese characters / <= 160 bytes, or <= 25 English words). NEVER
     bundle several different habits or sub-rules into one multi-clause sentence.
   - NEVER promote a specific detail that appears in only one member (such as one tool/checklist/machine name or one
     incidental step) into the principle's general statement, and NEVER infer mechanisms not stated in the quotes.
   - If the quotes do not explicitly show a rejected alternative or an exception, leave `rejected` or `exception` empty.
3. No single-project or single-task conventions: if the quotes all come from one project, one specific task, or one
   deliverable (even across several days), do NOT generalize them into a personal principle.
4. Distinctive: `default_contrast` is what a capable person who does not know the user would do in that situation; the
   user's way must differ from it in a way one could predict from. "Checks facts before answering", "communicates
   clearly", "cares about quality" hold for anyone capable: set `holds_for_anyone` true, or better, leave it out.
5. General and neutral: `situation` names a kind of situation, never one project, one person or one incident. Do not
   use project or people names in `text`. When writing in Chinese, always refer to the person as「用户」, never「他」or「她」.

For each principle give:
- `extends`: the id of an existing principle it is the same as (its members are added there), or empty for a new one;
- `text`: one short sentence the assistant can act on: in situations like X, the user does Y (rather than Z, only if
  shown in quotes);
- `situation`, `choice`, `rejected` (only when shown in quotes; else empty);
- `exception`: only when the quotes explicitly show a condition where the user does otherwise; else empty;
- `dimension`: cognitive dimension when applicable (`decision`, `thinking`, `reaction`, `attention`, `communication`), else empty;
- `default_contrast`, `holds_for_anyone`;
- `members`: ids of the items whose quotes directly support this single principle (at least three);
- `reason`: one line on why these items' quotes show the same choice.

Do not invent or overstate: narrow and strictly quote-backed beats broad and speculative.
