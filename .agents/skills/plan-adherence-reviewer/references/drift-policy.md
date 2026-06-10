# Drift policy

The reviewer distinguishes **minor implementation-detail drift** from
**behavior / scope drift**. The first is allowed under `PASS`; the second
forces `BLOCKED` or `NEEDS_PLAN_AMENDMENT`.

This document is the rulebook for that judgement call.

## What counts as minor detail drift (allowed under PASS)

Detail drift is drift that does **not**:

- change user-observable behavior,
- change any acceptance criterion's truth value,
- touch a declared guardrail,
- touch any declared out-of-scope item,
- alter a `D-*` decision recorded in the plan.

Concrete examples that pass:

- **Helper / API names differ slightly** from the plan because the actual
  codebase shape required it. Example: plan says `applyDisplayNameMatch`,
  implementation uses `matchesDisplayName`. Behavior identical, ACs intact.
- **File placement differs within the same intended layer.** Example: plan
  says "add the pinning test to `PermissionCardTest.kt`", implementation
  puts it in a sibling `PermissionCardCopyTest.kt` to keep test classes
  focused. Behavior identical.
- **Equivalent implementation strategy** that preserves behavior and ACs.
  Example: plan says "use `Map.compute`", implementation uses
  `getOrPut`. Behavior identical.

Always note minor drift in the output as a `[note]`, never as `[fail]`. The
worklog is the canonical place to explain why; the reviewer's job is to
acknowledge it as allowed, not to require justification a second time.

## What counts as behavior / scope drift (never passes silently)

Any of the following force `BLOCKED` or `NEEDS_PLAN_AMENDMENT`:

- **Behavioral drift.** User-visible behavior changed in a way the plan does
  not authorize (added affordance, changed interaction, different default).
- **Acceptance-criteria drift.** An AC is no longer true after the change,
  or an AC was reinterpreted to mean something different.
- **Scope drift / opportunistic refactor.** The diff touches code or
  contracts that the milestone did not ask for, regardless of whether the
  change is "nicer".
- **Down-scoping.** The diff intentionally skips planned behavior with no
  user-approved amendment.
- **Guardrail violation.** A declared guardrail invariant no longer holds.
- **Out-of-scope violation.** A declared OOS item is now touched.

The `D-*` decisions are part of the plan. Diverging from a `D-*` decision
is scope drift, not implementation detail — even if the new approach looks
better.

## How to read declared guardrails

Guardrails are anti-drift contracts. Read them literally:

- A guardrail says "matcher remains allocation-free on the hot path." If
  the diff adds visible allocations on that hot path **and** there is no
  runtime evidence in the work log proving the allocations are elided,
  raise the finding. If there is runtime evidence (benchmark output,
  recorded numbers), the guardrail holds and the reviewer must not flag.
- A guardrail says "no SQLite calls from composables." If a composable
  function now calls `SqliteStore` directly, that's a violation regardless
  of how lightweight the call looks.
- A guardrail says "no visual regressions in adjacent permission card
  rows." If the diff changes adjacent rows or the screenshot evidence
  shows a visible difference, raise the finding.

Do not invent guardrails the plan does not declare. The plan is the
contract; the reviewer enforces it, not its own preferences.

## How to read out-of-scope items

OOS items are the explicit fence. Anything that touches an OOS item in any
way is drift.

When the worklog says "I noticed I could also fix X — out of scope,
deferred", that is **healthy**: the implementer used the OOS list as
intended. The reviewer should record it as a positive note, not flag it.

When the diff actually touches an OOS item, the reviewer flags it even if
the change is small.

## Decision: how to route a drift finding

For each candidate drift finding:

1. Is the drift purely internal (no AC, no guardrail, no OOS, no behavior
   change)? → record as `[note]` under `PASS`.
2. Does the worklog argue for the unauthorized change ("this is nicer",
   "the user clearly wants X", "natural extension")? → the user has a
   product decision → `NEEDS_PLAN_AMENDMENT`.
3. Is the worklog silent on the drift, or does it admit the change was
   unintentional? → the implementer has a clear fix → `BLOCKED`.
4. Both code and plan ambiguities are present? → see the combo rules in
   [verdicts.md](verdicts.md#combo-cases). The dominant-blocker question
   decides the verdict label; both kinds of items are listed under
   `Required actions`.

Step 2 is the load-bearing one. The worklog is the canonical signal of
implementer intent — when it argues for the new behavior, the implementer
doesn't want the code-level fix, and routing to `BLOCKED` would be
asking them to undo something they want to keep. Routing to
`NEEDS_PLAN_AMENDMENT` puts the choice where it belongs: with the user.

## Plan-shape limitations (cross-reference)

When the plan itself is vague enough that the reviewer cannot decide
whether a change is drift, that's a plan-shape problem and the verdict is
`NEEDS_PLAN_AMENDMENT`. See [input-preflight.md](input-preflight.md) for
the partial-enforceability rules.

For the recognizable plan anti-patterns (vague DoD, "don't break
anything" guardrails, "future work" OOS), refer to the planner skill's
`references/slice-gotchas.md` — the reviewer surfaces these by pattern
name when amendments are required.
