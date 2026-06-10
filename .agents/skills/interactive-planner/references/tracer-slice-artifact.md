# Tracer-slice milestone artifact

Authoritative template for `M-*` milestones in workshop-mode planner output. Compact-mode
milestones may apply this as guidance rather than as a hard contract — see [SKILL.md](../SKILL.md)
for mode rules.

A milestone is a **tracer slice**: a forward, end-to-end proof of one piece of the spec, observable
to a named actor today, with its own guardrails and out-of-scope fence. "Slice" and "milestone" are
the same artifact (`M-*`); this file defines what counts as a valid one.

Read this with:

- [slice-actors.md](slice-actors.md) — the closed actor list and the evidence bar per actor.
- [slice-examples-end-user.md](slice-examples-end-user.md) — good vs. bad end-user milestones.
- [slice-examples-stakeholder.md](slice-examples-stakeholder.md) — good vs. bad stakeholder milestones.
- [slice-examples-developer.md](slice-examples-developer.md) — good vs. bad developer milestones.
- [slice-gotchas.md](slice-gotchas.md) — recurring anti-patterns and how the validator catches them.

## Why this shape

The goal is shorter feedback loops and incrementally-shippable proofs. A milestone that does not
stand on its own — that only "sets up the next milestone" — is plumbing without payoff and breaks
the tracer property. The richer shape exists so:

- planners cannot hide ambiguity behind a vague DoD;
- reviewers (including the downstream plan-adherence reviewer) can pin implementation
  against named actor evidence, declared guardrails, and explicit out-of-scope items;
- "wired up later" or "next-slice implementer" framings are visible and rejectable before any code
  is written.

## Required fields

Every workshop-mode `M-*` milestone must include these fields. Compact-mode milestones should aim
for the same shape but may collapse some fields when they would be obvious noise (still, the actor
and DoD are never optional).

### Actor

One **primary actor** from the closed list in [slice-actors.md](slice-actors.md). Secondary
actors are allowed but must be named explicitly — never implied. There is no "next-slice
implementer" actor; if the only person who benefits is "the agent doing M-(N+1)", the milestone is
not a tracer.

```markdown
- Primary actor: developer
- Secondary actors: end user (downstream observation only)
```

### Journey

The observable thing the actor can now do because this milestone landed. One sentence. Names a
concrete behaviour, not a process step.

Good:

> An end user opening Settings → Models sees provider display names ("OpenAI", "Anthropic") match
> in the search box, not just internal IDs.

Bad:

> Wires the search matcher through the presenter.

The bad version is end-to-end layers in disguise. It describes plumbing, not a journey.

### End-to-end layers

The layers this milestone wires, named as a consequence of the journey rather than as the framing.
This is the "tracer" property made explicit: the slice cuts through every layer the journey needs
to traverse today.

```markdown
- End-to-end layers: SettingsSearchPresenter → ModelProviderRegistry → SettingsSearchScreen
```

If the layer list reads like "introduces an interface, impl deferred", the slice is not a tracer
yet — see [slice-gotchas.md](slice-gotchas.md).

### Definition of done

Observable, actor-specific, names the concrete behaviour. **"Tests pass" is not a DoD.** The DoD
is what the actor sees, not what the build system reports.

| Actor       | DoD shape                                                              |
|-------------|------------------------------------------------------------------------|
| End user    | A UI state or affordance the user can reach and observe.               |
| Stakeholder | A demoable flow plus a pinning test for its key behaviour.             |
| Developer   | A test or dev-loop signal that exercises the seam the slice introduced.|

### Testing touchpoints

Required test types derived from the actor. The planner does not invent test types; it reads them
off [slice-actors.md](slice-actors.md):

- End user → E2E/compose-driver/UI test exercising the user-visible behaviour.
- Stakeholder → screen recording or screenshot artifact attached to the milestone, **and** a
  pinning test for the demoable behaviour.
- Developer → exercising test that drives a real interaction across the seam being introduced.

A seam without an exercising test is not a developer-actor tracer — fold it into a milestone with
payoff or do it as non-tracer maintenance work.

### Goals

What this milestone delivers. Two or three concise bullets. Should be directly satisfiable by the
end-to-end layers and DoD.

### Non-goals

What this milestone deliberately does not deliver. Concrete items, not categories.

Good:

> - Does not change provider sort order.
> - Does not add fuzzy/typo-tolerant matching.

Bad:

> - Out of scope: anything not listed above.

The bad version is a refusal to think; the validator rejects it.

### Guardrails

Invariants that must hold across this milestone. These are anti-drift contracts: the implementer
agrees not to break them, and the reviewer flags any change that does. Typical guardrails:

- performance budgets ("settings search remains under 50ms on the cached list");
- contract stability ("RPC `set_model` payload shape is unchanged");
- no regressions in adjacent surfaces ("Models tab keyboard navigation still works");
- security/permission invariants ("no new write-mode shell rules added by default").

The guardrail list must be non-empty. "Don't break anything" is not a guardrail.

### Out-of-scope

Explicit deferrals named concretely so the implementer can point at them when tempted to expand.
Each item should be specific enough that a reviewer can decide whether a change violates it.

Good:

> - Provider login OAuth refactor (tracked separately).
> - Searching nested model attributes (deferred to M-4).

Bad:

> - Anything else.
> - Future work.

Out-of-scope and non-goals are related but distinct: non-goals are things this milestone could
plausibly cover and chooses not to; out-of-scope is the explicit fence that keeps later
implementation from quietly absorbing nearby work.

### Depends-on

Prior `M-*` milestones that must land first. Lists IDs only. The full graph across the milestone
slate must be acyclic — the planner-time validator checks this mechanically.

```markdown
- Depends-on: M-1, M-2
```

If a milestone silently requires another to land alongside it (not before), the slice is not
self-standing. Either rewrite the DoD so it holds on its own, or merge the two milestones.

## Worked template

```markdown
### M-2 — Settings search matches provider display names

- Primary actor: end user
- Secondary actors: stakeholder (PM walkthrough)
- Journey: typing "Anthropic" in Settings → Models search matches the Anthropic provider row even
  though its internal id is `anthropic-prod`.
- End-to-end layers: SettingsSearchScreen → SettingsSearchPresenter → ModelProviderRegistry.
- Definition of done: the Anthropic row is visible and highlighted when the user types the display
  name; clearing the query restores the full list.
- Testing touchpoints: compose-driver UI test asserting the row appears for the display-name
  query; unit test on the matcher for the new field.
- Goals:
  - Match on display name in addition to internal id.
  - Keep matcher allocation-free on the hot path.
- Non-goals:
  - Fuzzy/typo-tolerant matching.
  - Reordering search results.
- Guardrails:
  - Search remains under 50ms on the cached provider list.
  - No new RPC traffic per keystroke.
- Out-of-scope:
  - Searching nested model attributes (deferred to M-4).
  - Provider login OAuth refactor (tracked separately).
- Depends-on: M-1.
```

## How the validator reads this artifact

The planner-time validator (see [SKILL.md](../SKILL.md)) performs a mechanical pass against this
shape. It is not a free-text reviewer:

- Missing or unknown actor → flag.
- Journey or DoD that is a process step ("hook up", "wire", "tests pass", "land plumbing") → flag.
- Empty/vague non-goals, guardrails, or out-of-scope → flag.
- Depends-on graph cyclic or referencing unknown IDs → flag.
- All risky/integration work parked at the end of the slate → flag for ordering.
- Two milestones whose DoDs are only meaningful together → flag (silently co-required).
- "Next-slice implementer" framing or equivalent escape hatch → flag.

Findings surface to the user before final approval. Overrides are allowed but must be explicit;
silent overrides are rejected.
