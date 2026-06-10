# Developer milestone examples

Two generic seam-introduction milestones so the contrast is concrete. Use
these as a template for shape, not for content.

## Good — seam + fake/impl + exercising test

```markdown
### M-1 — Introduce ReviewBundleWriter seam with in-memory fake and round-trip test

- Primary actor: developer
- Secondary actors: end user (later milestones surface the bundle in the UI)
- Journey: a contributor can write to and read back from `ReviewBundleWriter` through an
  in-memory fake; the round-trip test asserts the manifest, changed files, and conversation are
  preserved byte-for-byte.
- End-to-end layers: SessionReviewBundleWriter → ReviewBundleWriter (new seam) → InMemoryFake
  + on-disk impl.
- Definition of done: round-trip test green for both the in-memory fake and the on-disk impl;
  failing the round-trip (e.g. by dropping a manifest field) turns the test red.
- Testing touchpoints:
  - JUnit test `ReviewBundleWriterRoundTripTest` exercises both implementations through the same
    seam contract.
  - Deliberate regression check: removing the manifest serialiser branch produces a red test
    (recorded once during the TDD red cycle).
- Goals:
  - Introduce the seam with a working impl behind it today.
  - Lock the manifest shape with the round-trip assertion.
- Non-goals:
  - Wiring the writer into the live review-workflow flow (M-3).
  - Adding new manifest fields beyond the existing ones.
- Guardrails:
  - The on-disk impl writes atomically (temp file + rename), same as today.
  - No new dependencies introduced for the seam.
- Out-of-scope:
  - Replacing the existing direct call sites (M-2 does that).
  - Bundle compression (deferred to #985).
- Depends-on: none.
```

Why this is a tracer:

- The seam ships with a working implementation today, not "we'll wire it up later".
- The exercising test runs the seam in both directions; the deliberate red recorded during TDD
  proves the test actually catches a regression.
- DoD names a concrete dev-loop signal (test green / test red on regression), not "the seam
  exists".
- Out-of-scope is precise enough to defuse the obvious scope creep ("while I'm here, let me also
  replace the call sites").

## Bad — seam declared, "wired up later"

```markdown
### M-1 — Add ReviewBundleWriter interface

- Primary actor: developer
- Journey: introduces the ReviewBundleWriter seam; impl arrives in M-3.
- End-to-end layers: ReviewBundleWriter (new interface).
- Definition of done: interface compiles; downstream milestones can depend on it.
- Testing touchpoints: none yet — the seam is wired up later.
- Goals:
  - Land the type so M-2 can reference it.
- Non-goals:
  - Anything that exercises the seam (deferred to M-3).
- Guardrails:
  - Don't break the build.
- Out-of-scope:
  - Anything not listed above.
- Depends-on: none.
```

What is wrong:

- The journey is "next-slice implementer" framing in a thin disguise — the only person who
  benefits is the agent doing M-3, which the closed actor list explicitly forbids.
- No exercising test is present today; the developer evidence bar is unmet.
- The DoD is "interface compiles", a process step rather than a real dev-loop signal.
- The slice cannot stand on its own: removing M-3 leaves dead code with no behavioural meaning.

The right way out depends on intent:

- If the seam is genuinely needed now, bundle a working fake/impl and an exercising test into
  this milestone, as in the good example above.
- If the seam is only useful when M-3 lands, merge M-1 and M-3 — or admit it is non-tracer
  maintenance work and remove it from the milestone slate.

The planner-time validator flags "wired up later", "impl arrives in M-N", "the seam is exercised
in a later milestone", and equivalent phrases as developer-actor escape hatches and refuses to
pass the slate until the slice has exercising evidence today.
