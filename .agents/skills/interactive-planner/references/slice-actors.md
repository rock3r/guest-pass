# Tracer-slice actors

Closed list of actors a milestone can declare, and the evidence bar required per actor. Read this
together with [tracer-slice-artifact.md](tracer-slice-artifact.md).

Every milestone declares **exactly one primary actor** and may declare secondary actors. The
primary actor decides the evidence bar — secondaries are useful context but do not relax it.

## The closed list

```
end user · stakeholder · developer / contributor
```

That is the entire list. There is no "next-slice implementer" actor, no "the agent" actor, no
"reviewer" actor. The planner-time validator rejects anything outside this set.

### End user

The person using the running app. "Observable" means visible in the UI: state, affordances,
side-effects the user can perceive.

- Evidence bar: an E2E/compose-driver/UI test that exercises the user-visible behaviour
  introduced by this slice. The test asserts on what the user sees or can do, not on internal
  call counts.
- Typical journeys: a new affordance becomes available, a list responds to a new query, a setting
  surfaces a previously hidden option.
- Failure mode to watch for: dressing up a refactor as an end-user milestone. If the user cannot
  tell that anything changed, the actor is wrong — pick developer (with a test that proves the
  refactor doesn't regress) or fold the work into a milestone that does deliver an observable
  journey.

### Stakeholder

A PM, designer, support engineer, or other non-implementer who can walk through a flow and decide
whether it ships. Agent/RPC behaviour reachable from the UI lives here when the value is "look,
the agent now does X".

- Evidence bar: a demoable artifact (screen recording or annotated screenshot) attached to the
  milestone, **and** a pinning test for the behaviour shown in the demo. The recording proves the
  flow exists; the pinning test stops it from quietly regressing.
- Typical journeys: an end-to-end demo of a feature, a stakeholder-visible behaviour change,
  evidence that the agent or backend now performs a recognisable task.
- Failure mode to watch for: stakeholder milestones with a recording but no pinning test. The
  recording is reviewer bait, not regression coverage — the validator rejects it.

### Developer / contributor

A contributor working in the codebase **today** — running the test suite, reading IDE inspections,
watching eval verdicts, exercising a protocol under test. "Observable" means a real signal the
contributor sees on this commit: a new test runs and asserts, a previously silent CI signal
lights up, a fake/impl behind a seam responds when driven.

Critically, this is *not* "a hypothetical implementer in a later milestone". A developer milestone
whose only beneficiary is the agent or contributor doing M-(N+1) is the **next-slice implementer
anti-pattern** in disguise and is rejected — see [slice-gotchas.md](slice-gotchas.md).

- Evidence bar: a test or dev-loop interaction that drives a real interaction across the seam
  being introduced **on this slice**. The test must exercise the seam in both directions — not
  just assert that the type exists. Where the slice introduces a seam, a working fake or impl
  ships with the slice; a seam alone is not evidence.
- Typical journeys: introducing a seam with a working fake/impl behind it and an exercising test
  asserting end-to-end behaviour through it; adding a missing eval case that previously had no
  coverage; enabling a CI signal that was silent.
- Failure mode to watch for: declaring a seam and "wiring it up later", or framing the slice as
  preparation for a future milestone. A seam without an exercising test today is not a tracer —
  see [slice-examples-developer.md](slice-examples-developer.md).

## Pi behaviour and eval results

These are **observation channels**, not actors. Fold them into the matching closed-list actor:

- Pi behaviour reached through the UI by a non-implementer → stakeholder (with demo + pinning
  test).
- Pi behaviour exercised only by a test or eval → developer (with the exercising test as
  evidence).

A milestone whose only outcome is "the agent does X" without either a stakeholder demo or a
developer-actor test is not a tracer.

## Secondary actors

Secondary actors are allowed but explicit. They name additional people who will observe the
journey but whose evidence bar is not the gating one. Common patterns:

- Primary developer, secondary end user — a seam slice that an end user will benefit from in a
  later milestone. Evidence today is the developer test; the user-visible journey is intentionally
  deferred and noted in out-of-scope.
- Primary end user, secondary stakeholder — a UI change that will be demoed to PM, but whose
  ship-gate is the UI test.

When in doubt, pick the actor whose evidence bar is *higher*. That keeps the slice honest.

## How the validator uses this list

- Reject any milestone whose primary actor is not in the closed list.
- Reject any milestone whose evidence bar is mismatched against the declared actor.
- Reject milestones that try to launder plumbing as end-user or stakeholder work by attaching the
  wrong actor label.
- Reject "next-slice implementer" or equivalent escape-hatch framings — see
  [slice-gotchas.md](slice-gotchas.md).
