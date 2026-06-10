# End-user milestone examples

Two milestones drawn from the same feature — settings search matches provider display names — so
the contrast is concrete. Use these as a template for shape, not for content.

## Good — observable end-user journey

```markdown
### M-2 — Settings search matches provider display names

- Primary actor: end user
- Secondary actors: stakeholder (PM walkthrough)
- Journey: typing "Anthropic" in Settings → Models search matches the Anthropic provider row even
  though its internal id is `anthropic-prod`. Clearing the query restores the full list.
- End-to-end layers: SettingsSearchScreen → SettingsSearchPresenter → ModelProviderRegistry.
- Definition of done: the Anthropic row is visible and highlighted when the user types the
  display name; clearing the query restores the full list; the change is visible in the running
  app, not only under test.
- Testing touchpoints: compose-driver UI test asserting the row appears for the display-name
  query; presenter unit test covering match-on-display-name plus match-on-id (regression).
- Goals:
  - Match on display name in addition to internal id.
  - Keep matcher allocation-free on the hot path.
- Non-goals:
  - Fuzzy/typo-tolerant matching.
  - Reordering search results.
- Guardrails:
  - Search remains under 50ms on the cached provider list.
  - No new RPC traffic per keystroke.
  - Provider sort order unchanged.
- Out-of-scope:
  - Searching nested model attributes (deferred to M-4).
  - Provider login OAuth refactor (tracked separately).
- Depends-on: M-1 (ModelProviderRegistry exposes display names).
```

Why this is a tracer:

- The journey names a concrete user action and a concrete outcome.
- The DoD is something the user can observe in the running app, not "the test passes".
- The end-to-end layers list is a *consequence* of the journey; remove the journey and the layer
  list reads like plumbing.
- Guardrails and out-of-scope are non-empty and specific enough that a reviewer can decide
  whether a later change violates them.

## Bad — vertical sliver dressed as end-user work

```markdown
### M-2 — Wire the new SettingsSearchPresenter

- Primary actor: end user
- Journey: search box is now backed by the new presenter.
- End-to-end layers: SettingsSearchScreen → SettingsSearchPresenter.
- Definition of done: tests pass.
- Testing touchpoints: unit tests on the presenter.
- Goals:
  - Move logic out of the composable.
- Non-goals:
  - Anything not listed above.
- Guardrails:
  - Don't break anything.
- Out-of-scope:
  - Future work.
- Depends-on: M-1.
```

What is wrong:

- The "journey" is a refactor description, not an observable user action — the user cannot tell
  the presenter changed.
- The DoD is "tests pass", which is a process step, not an observable behaviour.
- The actor is mislabelled: nothing here is user-observable; the honest actor is developer, and
  even then the evidence bar (a real exercising test of a new seam) is missing.
- Non-goals, guardrails, and out-of-scope are placeholders — the validator rejects them.

If the only honest version of this slice is "I refactored the presenter and want a test that
proves it doesn't regress", fold it into the next user-observable milestone or do it as non-tracer
maintenance work outside the milestone slate.
