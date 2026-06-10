# Stakeholder milestone examples

Two generic review-workflow milestones so the contrast is concrete. Use
these as a template for shape, not for content.

## Good — demoable flow with a pinning test

```markdown
### M-3 — Review pass surfaces the cleanup prompt with the worktree-aware bundle

- Primary actor: stakeholder
- Secondary actors: end user (sees the prompt), developer (owns the pinning test)
- Journey: a PM/designer can run the review pass after a feature lands and watch the
  cleanup prompt fire with the right bundle metadata (changed files, branch name, base commit)
  visible in the agent's first response.
- End-to-end layers: ReviewWorkflowCoordinator -> CleanupReviewCoordinator -> ReviewBundleWriter
  → ChatPanel.
- Definition of done: the cleanup prompt appears in the chat with the bundle metadata block; the
  stakeholder can walk through the flow end-to-end without touching the CLI; the recording is
  attached to the milestone and reproducible in the running app today.
- Testing touchpoints:
  - Screen recording attached: `docs/recordings/review-pass-bundle.mp4`.
  - Pinning E2E test asserting the cleanup prompt contains the expected bundle metadata for a
    seeded session with one changed file.
- Goals:
  - Make the review pass demoable end-to-end without explaining "look, you would see this if X".
  - Pin the bundle metadata shape so later refactors don't quietly drop fields.
- Non-goals:
  - Improving the cleanup prompt copy.
  - Changing bundle contents beyond the metadata block.
- Guardrails:
  - The bundle metadata block keeps the keys `changed_files`, `branch`, `base_commit` in this
    order (downstream review skills parse them positionally).
  - The review pass remains read-only with respect to the working tree.
- Out-of-scope:
  - Other review-mode parity work (M-4).
  - Deeper review planning tracked separately.
- Depends-on: M-1, M-2.
```

Why this is a tracer:

- The journey names something a stakeholder can observe end-to-end; the recording is the demo
  artifact and the pinning test is the regression contract.
- Both evidence items are present — recording **and** pinning test. Either alone fails the
  evidence bar for stakeholder.
- Guardrails name a real, parseable invariant (key order in the metadata block), not "don't
  regress".
- Out-of-scope names concrete deferrals so the implementer can refuse a tempting "while I'm
  here…" expansion.

## Bad — vague "feels nicer", no artifact

```markdown
### M-3 — Make the review pass feel more polished

- Primary actor: stakeholder
- Journey: the cleanup prompt feels nicer to demo.
- End-to-end layers: ReviewWorkflowCoordinator -> CleanupReviewCoordinator.
- Definition of done: prompt reads better; stakeholder is happier.
- Testing touchpoints: vibes.
- Goals:
  - Polish.
- Non-goals:
  - Out of scope: anything not listed above.
- Guardrails:
  - Don't break anything.
- Out-of-scope:
  - TBD.
- Depends-on:
```

What is wrong:

- The journey is an adjective ("nicer", "more polished") with no concrete behaviour.
- No demoable artifact is attached, so the stakeholder evidence bar is unmet.
- No pinning test, so even if the polish is real today, the next refactor will quietly erode it.
- DoD, guardrails, and out-of-scope are placeholders — the validator rejects them.

If the honest version of this slice is "the prompt copy needs a pass", make it an end-user (or
stakeholder) milestone with a concrete before/after, a recording of the new copy, and a snapshot
test that pins the new wording. Anything less is reviewer bait, not a tracer.
