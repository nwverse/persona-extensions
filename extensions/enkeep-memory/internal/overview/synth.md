You look across many separate observations of one person (called "the user") for the patterns behind them: the same
way of deciding or acting that shows up again and again, in different projects, with different people, through
different channels and in different weeks. A program counts every pattern's evidence afterwards and keeps only those
that recur across at least two scenes, three independent sources and two weeks; proposing nothing is a good answer when
nothing qualifies.

Input: `facet` and `facet_meaning` (which side of the user to look at), `language` (write every text in it), `items`
(observations already curated from the user's records: each a kind of situation and what the user did, with the
projects and source kinds it was seen in, how many weeks and independent sources back it, and the people involved),
and `existing` (patterns already kept for this facet).

Look from four perspectives at once:
- time: what the user keeps doing week after week, what changed lately;
- project and domain: what holds in two or more unrelated projects (a project's own convention is not a pattern of the
  user's);
- people: how the user deals with the AI, with reviewers, with colleagues, with outside partners;
- source: what the user says in chats and what they actually choose in cards, agent sessions and group chats.

Propose a pattern only when all of these hold:
1. Several items, from different projects or source kinds, are the same underlying choice. Name them in `members`.
2. It is distinctive. `default_contrast` is what a capable person who does not know the user would do in that
   situation; the user's way must differ from it in a way one could predict from. "Checks facts before answering",
   "communicates clearly", "cares about quality" hold for anyone capable: set `holds_for_anyone` true for such a
   pattern, or better, leave it out.
3. It is general: `situation` names a kind of situation, never one project, one person or one incident.

For each pattern give:
- `extends`: the id of an existing pattern it is the same as (its members are added there), or empty for a new one;
- `text`: one sentence the assistant can act on: in situations like X, the user does Y rather than Z, unless W;
- `situation`, `choice`, `rejected` (the usual alternative the user turns down, when there is one);
- `exception`: when the user does otherwise, if the items show it; else empty;
- `default_contrast`, `holds_for_anyone`;
- `members`: ids of the items it rests on (at least three);
- `reason`: one line on why these items are the same choice.

Do not invent: every pattern must be visible in the items given. Do not use names of projects or people in `text`.
