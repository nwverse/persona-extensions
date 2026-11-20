# Model routes

Since 0.2.0-beta.30, enkeep memory can pick its model per call: a fast primary model first, and when it does not
answer, a fallback chosen by how long the call is. Every text step goes this way: the daily extraction and triage, the
weekly consolidation and synthesis, people cards, signals, and the rewriting of what goes to the assistant (exits).
The exits' format does not change. Since 0.2.0-beta.31 the whole chain goes to the hub in one call, which tries the
models in order (model.complete's `models`, persona #10398).

## What a call does

The chain of a call is:

1. **Primary models** (`model_primary`, in order; none by default: set it to a fast, cheap model of your providers,
   a Flash-class one, say).
2. **One fallback by size**: `model_long` (default `claude-sonnet-5-5`) if the call's estimated size is above
   `model_long_above` (default 16000), else `model_short` (default `claude-haiku-5-5`).

The hub tries them in order, each on a provider you set up that offers it, and each for at most `model_timeout`
seconds (default 180, the schema's second try included; the whole call stops at 600). A candidate **fails**, and the
next one is tried, when:

| reason | the hub's code |
| --- | --- |
| `error` | `model_failed` (an upstream error, a gateway refusing the account, a network error) |
| `empty` | `model_failed`: the reply was empty |
| `timeout` | `timeout`: it took longer than `model_timeout` |
| `format` | `invalid_output`: the answer did not match the step's JSON schema after the hub's second try |
| `refused` | `model_refused`, or `model_failed` saying the model declined |
| `paused` | `model_paused`: the hub holds that model's connection for quota or load (nothing is sent); see below |
| `not_offered` | `model_unavailable`: no provider you set up offers the model (nothing is sent) |

The answer says which candidate answered (`candidate`), which model the upstream named (`model`) and the candidates
that failed before it (`attempts`); that is what is counted. You can switch the extension's lists off, or replace them
with your own, on its purpose in Settings › Model configuration: then the answer has no candidate (the purpose's model
answered, noted "candidates off") or names a candidate of your list (noted "the user's own list").

What is not a model's failure stops the call at once, as before: the hub cannot run any model for the extension
(`assistant_offline`, `permission_denied`, a hub without `model.complete`), or the run itself was cancelled.

### A hold is a wait, not a failure (0.2.0-beta.34)

`model_paused` means the upstream said the account is busy or its quota is used up, and the hub holds the whole
connection (address and key) for every extension and purpose: first 5 seconds, doubling while it stays busy, at most 5
minutes; the error says until when (`until`). The route's candidates usually share that connection, so trying the next
one does not help. When a candidate of the chain was held and none answered, the call is neither a failure nor sent
the old way: it waits until the hold lets go (`until`; else the seconds the message names; else 10 seconds; at most 5
minutes a wait) and asks the whole chain again, within 10 minutes of waiting per call, and ends when the run is
cancelled. A call still held after that comes back as held: the step puts its unit back (the day stays open and runs
again when the hold lets go, with no failure counted and no error logged). Only a model not offered, a timeout, a reply
that does not match and the like move on to the next candidate, as above.

`enkeep_rerun_status` counts the holds apart ("Held by the hub ..."; per model, "held N"): they are not in the calls,
the unanswered ones or a model's attempts.

When no candidate answers (`model_candidates_failed`, or one candidate's own error), the caller gets what it got
before routes: a refusal (the batch is split into smaller ones), a pause (the day waits for the model), or the error.
Two cases go to the purpose's own model instead, sent the old way: when **none** of the route's models is offered
(another user's providers name their models differently: the route never leaves a new user without a model), and
when no candidate matched the schema (the old call returns the reply anyway, for the step's own checks of each item,
as before routes; with a single candidate the reply the hub returned with `invalid_output` is used directly).

## Long and short

A call's size is estimated before it is sent, from what it sends and what it asks for:

- **input**: the system prompt, the user turn and the answer's JSON schema (the hub writes the schema into the system
  prompt). ASCII counts a quarter token a byte; every other character counts one token. Chinese runs at about a token
  a character, so the estimate errs on the long side for Chinese text and is close for English.
- **output**: the room the step asks for its answer (`max_tokens`; 4096, the hub's default, when unset). The steps
  size it to their answers: 6000 for daily extraction, consolidation and people cards, 3000 to 4000 for triage,
  signals and exit rewriting.

`estimate = input + output`. Above `model_long_above` the call is long.

Why 16000 (decided as recommended): the room for the answer is 3000 to 6000 tokens on every step, so the line falls on
the input. With 16000, the calls that read a lot at once (a busy chat's daily extraction, the corrections pass of the
signals, both around 10k to 20k tokens of input) go to the larger model, where a small model loses detail; triage,
exit rewriting, weekly synthesis and small daily units stay short. The line is a setting: a lower one sends more to
the larger model, a higher one more to the smaller.

## Hubs without "models"

Hubs before persona #10398 turn a call with `models` down as invalid params (`json: unknown field "models"`): the
route then sends the call the old way, on the model of the extension's purpose in Settings › Model configuration, and
keeps doing so for an hour before it tries `models` again (a hub upgrade restarts the extension anyway). Those calls
are counted as "sent the old way".

## Settings

| setting | default | |
| --- | --- | --- |
| `model_route` | `chain` | `purpose`: back to the behaviour before 0.2.0-beta.30, every call on the purpose's model |
| `model_primary` | `[]` | tried in order; empty: the size's fallback only |
| `model_long` | `claude-sonnet-5-5` | fallback of a long call |
| `model_short` | `claude-haiku-5-5` | fallback of a short call |
| `model_long_above` | `16000` | the line between short and long, in estimated tokens |
| `model_timeout` | `180` | seconds each candidate may take, the schema's second try included (the hub stops the whole call at 600) |

With no model set at all the route is off.

## Where to see it

- **`enkeep_rerun_status`** (the `enkeep` connector) ends with the route and its counts since they started: calls,
  calls a fallback answered and the fallback rate, calls no model answered, calls sent the old way, and per model the
  attempts, answers (as fallback), failures by reason and the average time of an attempt.
- **`model-routes.json`** in the extension's data directory: the same counts and the last 50 calls.
- **`model-calls.jsonl`** beside it: one line per call: time, step, estimate, size, each candidate's attempt with its
  reason, the hub's code and message and seconds (and the upstream's model name when it differs), the candidate that
  answered, whether it was a fallback, a note (candidates off, the user's own list) and why a call went the old way. It never
  holds what was sent or answered. At 2 MiB it moves to `model-calls.jsonl.1`.
- The extension also logs one line per call (`enkeep-memory: model call daily: est 14210 tokens (short): answered by
  claude-haiku-5-5 after fallback; tried <primary model> timeout 180s, claude-haiku-5-5 ok 9s`).
