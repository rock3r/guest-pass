# Cal Industries Phase 2 integration

Cal Industries' Phase 2 is the "autonomous software factory" flow. The
plan-adherence reviewer plugs in **after each implementation milestone**,
before any cross-milestone work (review tail, push, PR) begins.

## Why the reviewer runs per-milestone

The downstream review steps check code quality, hygiene, and risk. They
do not necessarily check whether the implementation matches the
**approved plan**. Without the plan-adherence reviewer, an autonomous
delivery loop can ship a clean PR that quietly drifts from the approved
spec.

Running the reviewer per-milestone — rather than only at the end —
catches drift early, while it is still cheap to course-correct.

## Required orchestrator behavior

For each milestone `M-n` in the approved plan:

1. **Capture a baseline** before implementation starts.
   - Commit hash of the worktree before the milestone,
   - or a named tag like `cal/M-n-start`,
   - or a patch snapshot if the worktree is dirty.
   - Store the baseline pointer in `.cal-industries/<task>/milestones/M-n/baseline.txt`
     (or equivalent) so the reviewer can recover it.
2. **Implement `M-n`** under the project's normal TDD discipline.
3. **Capture an end marker** when the milestone is done.
   - The HEAD commit, or
   - the diff captured against the milestone-start baseline.
   - Store as `.cal-industries/<task>/milestones/M-n/end.txt` or
     equivalent.
4. **Invoke the plan-adherence reviewer** with:
   - the approved plan / spec path,
   - the milestone ID (`M-n`),
   - the baseline + end markers (or the explicit diff range / patch
     snapshot),
   - the work log path,
   - any validation artifact paths.
5. **Act on the verdict**:
   - `PASS` → continue to `M-(n+1)`, or to step 4 onward (Validate UI,
     then the review tail) when `M-n` is the final milestone. Step 4 is
     part of the post-implementation flow, so the final-milestone PASS
     must not skip it.
   - `BLOCKED` → fix the implementation (or supply missing inputs) and
     re-invoke the reviewer. Do not advance.
   - `NEEDS_PLAN_AMENDMENT` → stop and surface the decision to the user.
     Do not advance until the user either amends the plan (re-run the
     reviewer afterwards) or reverts the unauthorized change (re-run the
     reviewer afterwards). The orchestrator must not silently override.

## Ordering relative to other review steps

The plan-adherence reviewer runs **before** broader code-quality reviews.
Reasoning:

- If the milestone is off-plan, hygiene reviews are wasted effort —
  they'll polish code that may need to be reverted.
- The cheap fix (revert the off-plan change) should happen before the
  expensive reviewers run.

Within a single milestone the order is:

1. Implementation finishes for `M-n`.
2. Plan-adherence reviewer for `M-n`.
3. If `PASS`, optionally run any per-milestone hygiene or risk review
   the project enforces. Otherwise batch broader review at the end of
   all milestones.
4. Move on to `M-(n+1)`.

At the end of all milestones, run the full review tail defined by
`cal-industries`.

## Baseline-tracking quick reference

The recommended portable per-milestone layout is:

```
.cal-industries/<task>/
├── plan.md                 # approved plan/spec (or a copy if the source moves)
├── milestones/
│   ├── M-1/
│   │   ├── baseline.txt    # commit hash or patch-snapshot path at milestone start
│   │   ├── end.txt         # commit hash at milestone end
│   │   ├── diff.patch      # optional explicit diff (if worktree was dirty)
│   │   ├── worklog.md      # implementer's worklog for this milestone
│   │   └── review.md       # plan-adherence reviewer's output, latest run
│   └── M-2/
│       └── ...
└── work-log.md             # task-level decisions and rejected findings
```

The reviewer reads `plan.md`, `milestones/M-n/baseline.txt`,
`milestones/M-n/end.txt` (or `diff.patch`), and `milestones/M-n/worklog.md`.

If the orchestrator uses a different layout, the reviewer accepts any
explicit input paths — the layout above is a sane default, not a
contract.

## Non-Cal Industries orchestrators

The reviewer is portable. Any orchestrator (manual user invocation,
external scripts, other agents) that supplies the inputs in
[input-preflight.md](input-preflight.md) can invoke the reviewer. The
Cal Industries integration is opinionated about *when* to run, not about
*how*.

## What Cal Industries must NOT do

- Do **not** swallow `NEEDS_PLAN_AMENDMENT` and continue. The user
  decides whether to amend the plan.
- Do **not** re-run the reviewer with the same inputs hoping for a
  different verdict.
- Do **not** treat `BLOCKED` as advisory; it is a stop sign until the
  required actions are completed.
- Do **not** skip the per-milestone review on small milestones. Small
  milestones are exactly where opportunistic refactors slip in.
