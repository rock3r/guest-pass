# Input preflight

The reviewer **never** hallucinates a review. Before evaluating anything, it
verifies it has enough information to operate.

## Required inputs

The reviewer needs **all** of the following before it can produce a meaningful
verdict:

| Input                              | Required | Notes                                                                                          |
|------------------------------------|----------|------------------------------------------------------------------------------------------------|
| Approved plan/spec                 | yes      | Path, file content, or inline text. Stable IDs (`M-*`, `AC-*`, `T-*`, `OOS-*`) preferred.      |
| Milestone ID under review          | yes      | A single `M-*` (workshop mode) or the explicit milestone name from a compact handoff.          |
| Diff range or equivalent           | yes      | One of: baseline commit, diff range, patch snapshot, named milestone tag, or changed-file set. |
| Repo / worktree context            | yes      | Working directory the diff applies to.                                                         |
| User-approved plan amendments      | optional | If any acceptance criteria or decisions changed mid-flight, list them explicitly.              |
| Work log path or text              | optional | Records the implementer's decisions, deviations, and rejected findings.                        |
| Validation artifacts               | optional | Test command outputs, recordings, screenshots, eval verdicts.                                  |

## What counts as "enough"

The reviewer can operate when:

- the plan names what should change, and
- the diff (or equivalent) shows what actually changed.

Without one of these, the reviewer cannot pin implementation against the plan
and must not proceed.

## Verdict mapping for missing inputs

- **Missing diff / baseline / repo context** → `BLOCKED` with reason
  `insufficient-input`. Name the missing input by its category from the table
  above ("diff range, baseline commit, or patch snapshot", not "diff").
- **Plan too vague to enforce** → `NEEDS_PLAN_AMENDMENT`. The inputs are
  present; the plan is the unfit input. See
  [drift-policy.md](drift-policy.md) and the planner's
  `slice-gotchas.md` for the recognizable patterns.
- **Missing optional inputs** (work log, validation artifacts) → not a
  blocker, but the reviewer must note what could not be confirmed in the
  output `Validation` section.

There is no `NEEDS_INPUT` verdict. Missing review inputs always map to
`BLOCKED`. Missing *product intent* maps to `NEEDS_PLAN_AMENDMENT`.

## Confirming inputs at run start

Before producing findings, restate the inputs in the output's `Scope reviewed`
block:

- plan/spec path (or note if inline),
- milestone ID,
- baseline / diff range,
- worktree path,
- whether work log and validation artifacts were available.

This serves two purposes:

1. It forces the reviewer to commit to what it actually read.
2. It gives the user a clear pointer to fix when inputs need to be re-supplied.

## When the plan lacks stable IDs

The reviewer can do a **best-effort** review when the plan does not use
`M-*` / `AC-*` / `T-*` / `OOS-*` IDs:

- Find the closest named milestone or section heading and treat it as the
  milestone under review.
- Treat bulleted requirements as informal acceptance criteria.
- Note the limitation explicitly in the output's `Summary` block.

If the plan is so vague that even this best-effort read yields no enforceable
contract, switch to `NEEDS_PLAN_AMENDMENT`.

## Reading the milestone shape (when present)

When the plan uses the refined `M-*` shape, the reviewer's job is
substantially clearer:

- The **actor** drives which evidence bar to check: end-user → UI/E2E test;
  stakeholder → demoable artifact + pinning test; developer → exercising
  test driving the introduced seam.
- The declared **guardrails** are the anti-drift contract — read them
  literally; any change that breaks a guardrail is drift.
- The **out-of-scope** list is the explicit fence — anything in that list
  that the diff touches is drift.
- The **definition of done** is the completeness target — every part of it
  must be supported by the diff or the validation artifacts.

If the milestone shape is missing fields (no actor, no guardrails, vague
OOS, vague DoD), treat the plan as enforceable only where it is concrete and
record the shape gap as a `NEEDS_PLAN_AMENDMENT` finding for the missing
parts. See [drift-policy.md](drift-policy.md) for how to combine partial
enforceability with drift findings.
