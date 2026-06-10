# Tracer-slice anti-patterns

Recurring failure modes the planner-time validator and the human reader should watch for during
milestone drafting. Each entry pairs the pattern with what to look for and how to fix it.

The validator's job is mechanical, not interpretive — these patterns are written so the same
checks can run as a subagent (where supported) or inline as a self-review step (everywhere else).

## 1. Next-slice implementer framing

**Pattern.** A milestone's only audience is the agent that will do the next milestone. Phrases
like "wired up later", "impl arrives in M-N", "the seam is exercised in a later milestone",
"sets up M-(N+1)" are tells.

**Why it breaks the tracer property.** The slice does not stand on its own today. It is plumbing
without payoff, dressed up as a milestone.

**Fix.** Either bundle a working fake/impl and an exercising test into the slice (as in
[slice-examples-developer.md](slice-examples-developer.md)), or merge it into the milestone that
finally exercises it. If neither works, take it out of the milestone slate and treat it as
non-tracer maintenance work.

## 2. Plumbing without payoff

**Pattern.** Journey reads like a refactor description: "wires the new presenter", "moves logic
out of the composable", "introduces the X seam". End-to-end layers are a 1-line refactor target,
not a path through the spec.

**Why it breaks the tracer property.** The actor cannot point at anything they can do now that
they could not do before. The slice has no observable outcome — only an internal one.

**Fix.** Rewrite the journey so it names a behaviour the actor can observe today. If no honest
journey exists, the actor label is wrong (often developer mislabelled as end user). Pick the
actor whose evidence bar the slice can actually meet, or fold the work into a slice that delivers
a journey.

## 3. "Tests pass" as DoD

**Pattern.** Definition of done reads "tests pass", "build green", "CI happy", "no regressions".

**Why it breaks the tracer property.** A DoD is what the actor observes, not what the build
system reports. Tests passing is a precondition for shipping anything; it is not a milestone's
goal.

**Fix.** Restate the DoD as a concrete behaviour the actor can observe. For developer actors,
that is usually a specific test exercising a specific seam, not "the suite is green".

## 4. Vague non-goals, guardrails, or out-of-scope

**Pattern.** Non-goals: "anything not listed above". Guardrails: "don't break anything".
Out-of-scope: "future work" or "TBD".

**Why it breaks the tracer property.** These fields are anti-drift contracts. A vague fence is
no fence — implementers cannot decide whether a tempting expansion crosses it, and reviewers
cannot decide whether a regression violates it.

**Fix.** Name concrete items. The validator rejects the slate until each field is non-empty and
specific enough that a reviewer could decide a single change for/against it.

## 5. Silently co-required milestones

**Pattern.** Two milestones' DoDs are individually meaningful but only together describe a real
journey: M-2's DoD relies on behaviour that lands in M-3, or vice versa.

**Why it breaks the tracer property.** Either slice alone cannot ship — the tracer is split in
half. Implementers cannot stop after M-2 and demo anything coherent.

**Fix.** Merge the slices, or rewrite the DoD of the earlier one so it stands on its own (with a
fake/impl behind any seam it introduces, per the developer-actor evidence bar).

## 6. Stakeholder demo with no pinning test

**Pattern.** A stakeholder-actor milestone attaches a recording or screenshot but no pinning
test for the demoed behaviour.

**Why it breaks the tracer property.** The recording proves the flow exists today; without a
pinning test, the next refactor quietly erodes it. The evidence bar for stakeholder is
**demoable artifact + pinning test**, not either-or.

**Fix.** Add the pinning test, or downgrade the milestone's actor (if the gating evidence is
really developer-side) and rewrite the DoD accordingly.

## 7. End-user actor mislabel on a refactor

**Pattern.** Actor is "end user" but the journey is invisible to the user — internal refactor,
presenter swap, plumbing move.

**Why it breaks the tracer property.** Mislabelling launders plumbing as user-visible work. The
slate looks tracer-shaped but isn't.

**Fix.** Pick the honest actor (usually developer with an exercising test) and rewrite the DoD
to match. If no honest tracer exists, the work is non-tracer maintenance.

## 8. Risky/integration work parked entirely at the end

**Pattern.** The first N-1 milestones are low-risk preparation; all integration with the rest of
the system happens in the final slice.

**Why it breaks the tracer property.** The whole point of tracers is to surface integration risk
early. Pushing it to the end defeats the shape.

**Fix.** Re-sequence so at least one integration-risk milestone lands early, even if it is a
narrow vertical slice (one risky surface end-to-end with a small fake on the safe surfaces). It
is fine for later slices to broaden coverage — what is not fine is leaving every risky surface
untested until the last possible moment.

## 9. Cyclic or unresolved depends-on

**Pattern.** Depends-on references an unknown ID, or two milestones depend on each other.

**Why it breaks the tracer property.** The graph cannot be executed in any order without
contradiction.

**Fix.** Either remove the cycle by restructuring the slices, or merge the mutually-dependent
slices into one. The validator runs a topological sort; cycles are mechanically detectable.

## 10. New "alternate actor" smuggled in

**Pattern.** A milestone declares a primary actor not in the closed list: "the agent",
"reviewer", "next implementer", "the runtime", "Pi", "the orchestrator".

**Why it breaks the tracer property.** The closed actor list exists so "observable" has a
concrete meaning. Each new actor introduces an evidence bar the rest of the system cannot pin.

**Fix.** Map the slice onto the closed list. Pi behaviour reachable from the UI by a
non-implementer is stakeholder; Pi behaviour exercised only by a test is developer; everything
else is end user. If nothing maps, the slice is not a tracer.

## Validator behaviour summary

For each pattern above, the validator surfaces a finding with:

- the milestone ID,
- the pattern name (matching the headings here),
- the offending text or absence,
- the suggested fix from this file.

Findings do not automatically block approval, but every finding requires explicit user
acknowledgement before the planner artifact can be marked `approval_status: approved`. Silent
overrides are rejected — see [SKILL.md](../SKILL.md) for the approval gate semantics.
