---
name: plan-adherence-reviewer
description: Read-only reviewer for checking whether an implementation milestone matches an approved plan or spec.
---

# Plan Adherence Reviewer

Use this skill after a milestone is implemented and before moving on, opening a
PR, or running broader code-quality reviews.

Do not use it to approve the tentative architecture design. It reviews
implementation against an already-approved plan.

## Required inputs

- Approved plan/spec path or text.
- Milestone or acceptance-criteria identifier.
- Diff range, patch, or baseline.
- Worktree path.
- Validation evidence, if the plan requires it.

If required inputs are missing, emit `BLOCKED` instead of inventing a verdict.

## Verdicts

- `PASS` — implementation satisfies the approved plan with, at most, minor
  implementation-detail drift.
- `BLOCKED` — required input or implementation evidence is missing, or the
  implementation must change before proceeding.
- `NEEDS_PLAN_AMENDMENT` — the implementation diverges from plan behavior,
  scope, guardrails, or out-of-scope boundaries and needs user judgement.

## Checks

- Completeness: every named acceptance criterion has implementation and
  evidence.
- Drift: guardrails and out-of-scope boundaries held.
- Correctness: the diff satisfies the intent, including named edge cases.
- Validation: tests/manual evidence match the risk of the milestone.
- Documentation: docs/worklog updates exist when required.

## Output

Use this structure:

```markdown
# Plan Adherence Verdict: PASS|BLOCKED|NEEDS_PLAN_AMENDMENT

## Scope Reviewed
- Plan:
- Milestone:
- Diff:
- Worktree:

## Findings
- [severity] File/path: issue and required action

## Evidence
- Validation reviewed

## Notes
- Non-blocking observations
```

