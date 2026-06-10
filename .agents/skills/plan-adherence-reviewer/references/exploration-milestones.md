# Reviewing exploration / prototype milestones

Exploration milestones are not skipped. They are reviewed against their
**own** acceptance criteria — the exploration contract — not against
production-milestone expectations.

## Recognizing an exploration milestone

A milestone is exploration when one or more of the following are true:

- The plan explicitly labels it as exploration / prototype / spike.
- Its ACs talk about *variants* or *comparison* rather than a single
  shipping behavior.
- It has a downstream production milestone that depends on the user
  picking a winner.
- Its DoD names "comparison note", "screenshots / recording per variant",
  or "user decision gate" instead of "feature shipped".

If the plan does not declare the milestone as exploration, do not assume
it. Treat it as a production milestone unless the ACs make exploration
explicit.

## What the reviewer checks for an exploration milestone

- Did the diff produce the requested variants? Count and identify them.
- Is there captured evidence per variant (screenshot, recording, sketch
  file in an appropriate artifact directory)?
- Is there a comparison note / observation log recording the trade-offs?
- Did the implementer **stop** at the user decision gate, or did
  production code get committed past the exploration?
- Is there any production source / test / config the milestone said
  should not be touched yet?

The actor of an exploration milestone is usually `developer` (the
contributor running the canvas / sandbox) with `stakeholder` as a
secondary (the person picking the winner). Read the evidence bar from
that actor.

## What the reviewer must NOT do for an exploration milestone

- Do **not** apply production-milestone completeness rules. There is no
  E2E test for "the winning variant" yet; that's M-(N+1)'s problem.
- Do **not** pick a winner. The user decision gate is the whole point of
  the milestone.
- Do **not** flag the absence of compose-driver / production-side
  validation as a `[fail]`. Production validation lives in the
  downstream production milestone.

## Verdicts for exploration milestones

- All requested variants prototyped, evidence captured, comparison note
  present, production code untouched → `PASS`.
- Production code committed during exploration → `BLOCKED`, with the
  required action "revert production-touching changes from this
  milestone". This is real scope drift, not minor detail drift.
- Variants partial (e.g., 2 of 3), evidence missing → `BLOCKED`.
- Comparison note missing but variants present → `BLOCKED` with the
  required action "add a comparison note covering the prototyped
  variants".
- Exploration ACs themselves are vague → `NEEDS_PLAN_AMENDMENT`.

## When production code is technically "needed" for exploration

Some explorations require touching production-adjacent code (e.g., adding
a feature flag, exposing a test seam) to be runnable. If the plan
explicitly authorizes such changes for the exploration milestone, the
reviewer respects that authorization. If the plan does not authorize it,
the touch is scope drift even if it makes the exploration tidier.
