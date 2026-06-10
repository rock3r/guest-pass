# Verdicts

The reviewer emits **exactly one** verdict per milestone:

- `PASS`
- `BLOCKED`
- `NEEDS_PLAN_AMENDMENT`

There is no `PASS_WITH_NOTES`. There is no `NEEDS_INPUT`. There is no
`WARN`. The verdict set is small on purpose: each one maps to a single
explicit action the orchestrator (or user) takes next.

## PASS

The milestone satisfies the approved plan.

- All milestone acceptance criteria are met.
- Required tests / validation evidence are present and have been run.
- No behavioral drift outside agreed scope.
- Declared guardrails hold.
- Declared out-of-scope items have not been touched.

`PASS` may include non-blocking notes — minor implementation-detail drift,
observations about future cleanup, reminders about declared deferrals. None
of these notes block continuation. If a finding would block continuation,
the verdict is not `PASS`; pick `BLOCKED` or `NEEDS_PLAN_AMENDMENT`
instead.

### What `PASS` does NOT mean

`PASS` does not mean "the code is great" or "the design is right". It means
"the implementation matches the approved plan for this milestone." A bad
plan that was followed faithfully still yields `PASS` — challenge the plan
through the planner, not through this reviewer.

## BLOCKED

The implementation, the review inputs, or both must change before the work
can move forward.

Typical `BLOCKED` triggers:

- a milestone acceptance criterion has no implementation;
- a required test or validation step is missing;
- the diff shows behavioral drift that the user has not approved as a plan
  amendment;
- a declared guardrail is violated and the violation is real (not just
  theoretically possible — see [drift-policy.md](drift-policy.md));
- the input preflight failed (missing diff, baseline, or repo context) —
  the reviewer cannot proceed.

`BLOCKED` always names:

- which AC / T / guardrail / OOS / input is the problem;
- a required action that, if completed, would let the reviewer reach a
  different verdict on rerun.

## NEEDS_PLAN_AMENDMENT

The plan must be clarified or amended before the reviewer can give a
meaningful verdict.

Typical `NEEDS_PLAN_AMENDMENT` triggers:

- the plan is too vague to enforce (no concrete ACs, vague DoD, vague
  guardrails / OOS) — see `slice-gotchas.md` patterns 3 and 4;
- the implementation contradicts a decision or AC in a way that may be
  desirable but needs explicit user approval;
- the worklog records an intentional product choice that is not authorized
  by the plan.

The distinction from `BLOCKED` is critical:

- `BLOCKED` says **fix the implementation** (or supply missing review
  inputs);
- `NEEDS_PLAN_AMENDMENT` says **the user has a decision to make**.

The reviewer must not silently approve behavioral drift just because it
looks like an improvement. If the worklog or diff signals an intentional
product change outside the approved scope, the verdict is
`NEEDS_PLAN_AMENDMENT` and the orchestrator must surface the decision to
the user.

## How to choose between BLOCKED and NEEDS_PLAN_AMENDMENT

The deciding question is: **who has the next decision to make?**

- If the implementer has a clear code-level fix and no signal of
  intentional product choice in the worklog → the implementer decides →
  `BLOCKED`.
- If the user has a product choice to make (keep the new behavior or
  revert it; sharpen a vague AC; relax an over-strict guardrail) → the
  user decides → `NEEDS_PLAN_AMENDMENT`.

The worklog is the primary signal of intent. A worklog that admits a
deferred-but-planned gap ("rollback test not yet added") points to
`BLOCKED`. A worklog that argues for the unauthorized change ("this
seems like a natural extension", "the user clearly wants X") points to
`NEEDS_PLAN_AMENDMENT`. A silent worklog defaults to `BLOCKED` — the
reviewer cannot infer product intent from silence.

| Situation                                                                                    | Verdict                |
|----------------------------------------------------------------------------------------------|------------------------|
| Missing AC implementation; no worklog signal of intentional product change                   | `BLOCKED`              |
| Missing validation evidence; plan is otherwise clear                                         | `BLOCKED`              |
| Missing diff/baseline/repo context                                                           | `BLOCKED`              |
| Diff implements something outside scope, worklog argues for the new behavior                 | `NEEDS_PLAN_AMENDMENT` |
| Plan's AC is "feels smoother" or guardrail is "don't break anything"                         | `NEEDS_PLAN_AMENDMENT` |
| Diff violates a concrete declared guardrail; worklog admits the violation as **unintentional** or silently | `BLOCKED`              |
| Diff violates a concrete declared guardrail; worklog argues the violation **should stay**    | `NEEDS_PLAN_AMENDMENT` |
| Implementation diverges from a `D-*` decision but worklog records no intent                  | `BLOCKED`              |
| Implementation diverges from a `D-*` decision and worklog argues for the divergence          | `NEEDS_PLAN_AMENDMENT` |

### Combo cases

When the milestone shows **both** code-level completeness gaps **and**
intentional product drift, the verdict depends on which is the
**dominant blocker**:

- If the code-level gap stops the milestone from functioning at all
  (missing AC implementation, missing test for the milestone's primary
  behavior) → `BLOCKED`. The user can't make a meaningful amendment
  decision until the milestone is at least functionally complete.
- If the code-level gap is a missing pinning test or doc update that is
  trivially added regardless of how the user decides the drift question
  → `NEEDS_PLAN_AMENDMENT`. The user's product decision shapes the
  remaining work; the implementer should not race ahead and "fix" the
  drift in a way the user may want to keep.

In both combo cases, list every required action — code-level and
plan-amendment — in the `Required actions` section. The verdict label
names the *next* decision; `Required actions` enumerates the work that
follows once that decision is made.
