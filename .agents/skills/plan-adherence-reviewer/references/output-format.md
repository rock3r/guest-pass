# Output format

The reviewer emits **one** structured Markdown report per run. The shape
below is required: downstream orchestrators (Cal Industries Phase 2,
external scripts) parse it to decide what to do next.

## Template

```markdown
# Plan adherence review — <milestone id>

## Verdict
PASS | BLOCKED | NEEDS_PLAN_AMENDMENT

## Scope reviewed
- Plan/spec: <path or "inline">
- Milestone: <id>
- Baseline / diff range: <commit, range, or snapshot description>
- Worktree: <absolute path>
- Work log: <path or "not provided">
- Validation artifacts: <path(s) or "not provided">

## Summary
<two or three sentences naming the verdict driver — what made this PASS /
BLOCKED / NEEDS_PLAN_AMENDMENT.>

## Completeness
- [pass|fail|note] <finding text referencing AC-* / T-* / non-code deliverable>

## Drift
- [pass|fail|note] <finding text referencing guardrail / OOS-* / D-* / behavior change>

## Correctness
- [pass|fail|note] <finding text with evidence pointer>

## Validation
- [pass|fail|note] <finding text referencing actor evidence bar / TDD trace / run output>

## Documentation / handoff
- [pass|fail|note] <finding text referencing user-guide / deep doc / work-log notes>

## Required actions
1. <imperative, atomic action that resolves a [fail]. Skip this section entirely on PASS.>

## Temporary artifacts
Created:
- <path>
Deleted:
- <path>
Remaining:
- <path or "none">
```

## Rules

### Section presence

- Every section heading above must be present in the output, in that order.
- A section may be empty under `PASS` (e.g., `Required actions` is dropped
  entirely on `PASS`).
- `Required actions` is **mandatory** under `BLOCKED` and
  `NEEDS_PLAN_AMENDMENT`. Without required actions the verdict is not
  actionable and the orchestrator cannot proceed.

### Finding tags

Use only three tags inside the content sections:

- `[pass]` — the check passed; record what was checked.
- `[note]` — non-blocking observation. Allowed under any verdict. Use
  this for minor detail drift, deferred OOS reminders, "this could be
  tightened later" comments.
- `[fail]` — blocking finding. Forces verdict away from `PASS`.

Do not invent extra tags (`[warn]`, `[info]`, `[review]`, etc.). The
verdict already carries severity; tags carry per-finding outcome.

### Pointer discipline

Every finding (including `[pass]`) should reference one of:

- a stable plan ID (`AC-2`, `T-3`, `OOS-1`, `D-1`, `M-1`),
- a diff path,
- a worklog excerpt or quote,
- a validation artifact path.

"The implementation looks fine" is not a finding; it is decoration. Cut
it.

### Required actions style

- Imperative, atomic, one line each.
- Each must map to a concrete `[fail]` finding above.
- Examples:
  - "Implement the failure rollback path in `PermissionRulesPresenter` and
     add a presenter unit test (T-2)."
  - "Amend the plan to authorize click-to-rename on the tooltip, or
     revert the click handler and its test."
  - "Supply the milestone-start baseline commit (or a patch snapshot) so
     the reviewer can determine what changed during M-1."

### Temporary artifacts

Always include the `Temporary artifacts` section, even if empty. If
nothing was created, `Created`, `Deleted`, and `Remaining` all say
`none`. If something was created and successfully deleted, list both. If
cleanup failed, the section is the loudest part of the report.

## Length

Reports should be short. A typical PASS report is well under a page; a
BLOCKED report names the failing AC, the missing test, and the required
action. The reviewer is not a code reviewer — it is a plan-adherence
gate. Anything that doesn't move toward the verdict can be cut.
