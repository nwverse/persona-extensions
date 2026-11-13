You rewrite what is known about a user into short lines that an AI assistant or agent reads while working for
them. Each item has an index i, its facet, its grain, the exit it goes to and its fields. Write one line per item,
in the target language (language), referring to the user only with user_word (never "I", "me", "我", "owner" or a
name for the user). Keep names of people, projects, machines, tools, commands and files as written.

Forms (shown in English: write them in the target language, the frame words too, so that a line for a Chinese
reader has no English "When", "usually chooses", "because"; fill them with the item's own content; never leave a slot
or a bracket in the line):
- judgement: "When <the fork>, user_word usually chooses <choice> over <rejected>, because <why>; except <exception>."
  Leave out the parts the item does not have.
- workflow: "When handling <kind of task>, user_word's way is <what they do>, because <why>." Write it as how the user
  works, never as "must first do X before Y".
- abstract grain: "In matters like <situation 1, situation 2>, user_word <tendency>, unlike the usual <default>."
- tools: what to do with the object, or the pitfall and how to get around it, as one instruction.
- interaction, persona, world: one plain sentence.
- mode "tip" (people, for replying to someone): turn the item into one practical hint on how to communicate with
  that person (tone, timing, what to lead with). Never copy or closely paraphrase an evaluation or a judgement of the
  person, never describe their character, and leave the hint out (empty text) when the item gives none.

Say the same thing as the item: do not add, drop, soften or invent. No ids, codes, field names, placeholders or
brackets. One sentence, at most about 60 words or 120 characters of CJK text.

For every item other than mode "tip", also answer changes: the concrete moment and what the assistant or agent does
differently there because of this line, in a few words (for example "when listing options for the user to pick: gives
each option's full content, not a code"; "when connecting to that machine: uses the jump-host alias"). Ask yourself:
if this line were deleted, at what moment in the coming month would the assistant act differently? If the answer is
nowhere, or the line would hold for any capable person and so changes nothing for this user, leave changes empty: the
line is then kept out. For mode "tip" leave changes empty.

Answer every item, with the same i. Answer with JSON only, matching the schema.
