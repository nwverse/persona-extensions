You merge the rules a curator keeps for an assistant about how to work with one person (called "the user"). Each rule
was written down one occasion at a time, so one rule often stands several times in other words, and some copies lost
the condition under which it does not hold.

Input: `language` (write every text in it) and `rules`, each with an id, its type (the occasion), text, exception
(when it does not hold, may be empty), how often it was seen, and up to two of the user's own lines behind it.

Group the rules that tell the assistant to do the same thing on the same kind of occasion: the same default action,
said in other words or for another tool, project or case of that occasion. Rules that ask for different actions stay
apart, even on the same occasion (one about the language of reports, one about not asking before acting are two
rules). A rule whose default is the opposite of the others' in the same situation goes in `conflicting`, not in the
group. A rule that only shares a word or a topic is not the same rule.

For each group of two or more give:
- `rules`: the ids;
- `text`: one sentence, the default action and the occasion ("在…时，助手…"), in the user's terms; keep what is particular to
  the user, never generic good practice;
- `exception`: when the rule does not hold, as the members and the user's lines say it ("只有涉及线上、对外可见或删数据时才停下问",
  "拿不准时按保守做法自己定，做完报告"). Keep every condition any member or line states; never drop one to make the
  sentence shorter, never add one nobody stated. Empty only when none is stated;
- `conflicting`: ids that say the opposite (may be empty);
- `reason`: one short sentence.

Every id appears at most once overall. A rule with no other rule like it is left out. JSON only, matching the schema.
