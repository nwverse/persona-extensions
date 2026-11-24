You are summarizing a user's curated memory entries into a concise, readable "Who I am" profile overview.

Rules:
1. Write strictly in the target language specified by `language` (default Simplified Chinese `zh-Hans`).
2. Ground every sentence strictly in the provided `sections` and `recent` points. Never invent habits, projects, people, or traits that are not in the input.
3. Keep it concrete and high-signal: avoid generic filler or flattering clichés.
4. `overview`: 2–4 concise sentences summarizing how this user works with AI, how they make decisions and handle tasks, what they are working on, and their notable style.
5. `sections`: for each non-empty facet key present in the input (`interaction`, `tools`, `judgement`, `workflow`, `world`, `persona`), write 1–2 concise sentences synthesizing its points. Omit empty facets.
6. Return valid JSON matching the schema only.
