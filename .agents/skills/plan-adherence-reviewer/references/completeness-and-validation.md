# Completeness, correctness, and validation checks

This file defines what the reviewer actually looks at when it evaluates a
milestone. It is organized by the four content sections of the output
report:

- Completeness
- Drift
- Correctness
- Validation

Drift specifics live in [drift-policy.md](drift-policy.md); this file
covers the rest.

## Completeness checks

For each acceptance criterion (`AC-*`) attached to the milestone:

- Is there a code path in the diff that implements it?
- Is there an explicit test or validation artifact that demonstrates it?
- If the AC names a user-visible behavior, is the user-visible journey
  reachable in the diff (UI route + presenter wiring + state change)?

For each test requirement (`T-*`) attached to the milestone:

- Does the diff add or modify the named test?
- Is the test exercising the behavior (asserting on outcomes) rather than
  just declaring that types exist?
- Is there evidence — in the worklog or validation artifacts — that the
  test was actually run, and that it passed?

For each non-code deliverable named in the milestone (user-guide update,
doc update, screenshot, recording):

- Is the deliverable present in the diff or in the linked artifact path?

A milestone is **complete** when every `AC-*` and `T-*` is matched and
every named non-code deliverable exists.

If anything is missing, raise a `[fail]` finding under `Completeness` and
prefer verdict `BLOCKED`.

## Correctness checks

Correctness is "does the diff actually do what the AC says, in the real
codebase, in a way that won't regress under normal use?"

The reviewer should look at:

- **Edge cases named in the plan.** If the plan mentions empty inputs,
  failure modes, concurrent access, large data, the diff should handle
  them. If they are explicitly listed in `OOS-*`, that's fine.
- **Meaningful tests.** A test that does not assert on the introduced
  behavior is not real coverage. A pinning test that pins a string the
  diff didn't introduce is not real coverage either.
- **Regression risk on adjacent surfaces.** If the diff touches a shared
  presenter / state / matcher, do the diff's tests confirm the existing
  behavior of other callers? Are the existing tests still green according
  to the worklog or validation artifacts?

Correctness findings can be `[fail]` (block on this finding alone) or
`[note]` (mention but don't block) depending on severity. Use judgment:
- "the diff doesn't handle the empty-input case named in the plan" →
  `[fail]`, verdict `BLOCKED`.
- "the test could be tightened by asserting on the exact list ordering"
  → `[note]` under `PASS`.

## Validation checks

Validation is about evidence, not about the implementation itself.

For each piece of validation evidence the milestone required (per its
**actor**, per the planner's tracer-slice actors):

- **End-user actor** → was an E2E/UI test added, and is there a record
  that it ran and passed? Where compose-driver tests apply, was the
  screenshot evidence captured?
- **Stakeholder actor** → is there a demoable artifact (recording or
  screenshot) linked from the milestone, **and** a pinning test? The
  evidence bar is both — not either-or.
- **Developer actor** → is there an exercising test driving the
  introduced seam? "Type exists" is not exercising. The test must drive
  real interaction across the seam in both directions.

The reviewer also records what could **not** be confirmed:

- "E2E test added, but worklog does not record a green
  run." → `[note]` under `Validation`. The reviewer can choose to run the
  test as a temporary validation harness (see
  [temporary-artifacts.md](temporary-artifacts.md)), but is not required
  to.

If a required validation artifact is missing, raise a `[fail]` finding
and prefer `BLOCKED`.

## TDD red/green evidence

When the project enforces TDD, the reviewer checks the
worklog for the red/green cycle on **new** tests:

- Was each new test run red before the implementation existed?
- Was each new test then run green after the implementation?

A worklog that records the cycle is sufficient. A worklog that's silent
on a new test is a `[note]`, not a `[fail]`, unless the milestone
specifically required TDD evidence.

The reviewer never re-runs tests to "verify TDD" — it cannot rewind time.
What it can do is flag tests that look like they were written *after*
the implementation (pinning a string the diff invented, asserting on
internals only reachable post-implementation) and surface that to the
user.

## Documentation and handoff checks

For each documentation surface the project requires:

- If the milestone changed a user-visible surface, was the user-guide
  updated?
- If the milestone changed an internal contract, was the matching deep
  doc updated?
- If the milestone added a new doc, is it registered in any doc index,
  bundle manifest, or navigation file the project requires?

A documentation gap is a `[fail]` finding when the milestone explicitly
required the update. It is a `[note]` when the requirement is implicit
but the diff is not obviously user-visible / contract-changing.

The work log should record any deviations, rejected findings, and
important decisions. A missing work log is a `[note]`, not a `[fail]`,
unless the plan required one.
