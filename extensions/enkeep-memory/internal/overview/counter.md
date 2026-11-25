You check proposed patterns about one person (called "the user") against records that might contradict them.

Input: `language` (write every text in it) and `patterns`, each with its `text`, `situation` and `choice`, and
`candidates`: records of the user found by a search because they look related. Most candidates are only related, not
contradicting.

For each pattern decide which candidates are real counter-examples: the same kind of situation, and the user chose
otherwise. A candidate in a different situation, or one that agrees, is not a counter-example.

Then give:
- `counters`: the ids of the real counter-examples (may be empty);
- `exception`: when the counter-examples share a condition under which the user does otherwise, that condition as one
  short clause ("when the change touches production", "for outside partners"); else empty;
- `poles`: when the user plainly goes both ways, each in its own kind of situation, both ends: `{when, choice}` for
  the pattern's side and for the counter-examples' side, in the pattern's language ("改线上配置", "先备份再切换"). The
  person is not contradicting themself by mistake: keep both and say when each holds; do not fold one into "unless".
  Empty when there is one side only;
- `overturned`: true only when the counter-examples show the pattern is wrong as stated, not merely bounded;
- `reason`: one line.

Answer for every pattern, by its `id`.
