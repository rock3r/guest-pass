---
name: interactive-planner
description: >
  Harness-portable planning and requirements-capture workflow for turning
  vague, risky, architectural, multi-workstream, or scope-drift-prone requests
  into durable artifacts before implementation. Use when the user asks to
  scope, plan, workshop, write a spec, capture acceptance criteria, prepare an
  implementation handoff, or when product intent should not be guessed.
---

# Interactive Planner

Use this skill to turn fuzzy intent into an implementation contract. The goal
is not to ask more questions; it is to investigate first, capture what is known,
separate facts from assumptions, and leave a durable artifact that another
agent or future session can execute.

Do not use this for tiny, obvious fixes unless the user explicitly asks to plan
first.

## Operating Posture

1. **Investigate before asking.** Read the relevant repo docs, referenced
   issues/files, previous plans, and obvious source locations before forming
   questions.
2. **Separate intent from facts.** User preference is product input. Claims
   about code, APIs, platform behavior, docs, or prior decisions must be
   verified or marked as unvalidated.
3. **Keep planning mostly read-only.** During planning, do not modify
   production code or product docs. Planner artifacts may be written to the
   repo's ignored planning area, an issue comment, or a harness-native artifact
   store.
4. **Produce durable artifacts.** Never end planning with requirements captured
   only in chat.
5. **Require explicit approval.** A final plan/spec is not approved for
   implementation until the user explicitly approves it.

## Artifact Store

Prefer `.plans/` when it exists or is allowed by repo policy. If the harness or
project uses another ignored planning directory, issue tracker, document, or
native artifact store, use that instead and say where the artifact lives.

For this repo, use `.plans/` for local, gitignored planning artifacts unless
the user asks for a committed doc.

## Preflight

Start each planning run by capturing:

- task type and risk level;
- user goal and desired outcome;
- available references;
- missing inputs;
- likely output target: compact ledger, formal spec, implementation handoff,
  issue update, or Cal Industries handoff;
- recommended mode: compact or workshop.

If important facts are missing, investigate before interrupting the user. Batch
remaining independent questions in one round.

## Mode Selection

### Compact Mode

Use compact mode for small or moderately clear tasks where the main risk is an
unclear done condition.

Produce a lightweight ledger/spec with:

- mission / problem statement;
- context and evidence;
- assumptions and constraints;
- expected behavior / done condition;
- validation expectation;
- out-of-scope notes.

Stable IDs are optional, but direct implementation handoffs should include the
minimum useful IDs: `AC-*` for acceptance criteria, `T-*` for validation, and
`M-*` for milestones when there is more than one step.

### Workshop Mode

Use workshop mode for ambiguous, architectural, UI-heavy, security-sensitive,
multi-workstream, high-risk, or scope-drift-prone tasks.

Produce richer artifacts with:

- mission / problem statement;
- constraints and non-negotiables;
- evidence log;
- assumption ledger;
- decision ledger;
- open questions;
- acceptance criteria;
- validation plan;
- out-of-scope list;
- milestone/workstream breakdown;
- handoff instructions.

Use stable IDs:

- `A-*` — assumptions;
- `D-*` — decisions;
- `Q-*` — open questions;
- `AC-*` — acceptance criteria;
- `T-*` — test/validation requirements;
- `M-*` — milestones;
- `OOS-*` — out-of-scope items.

## Milestone Shape

Milestones should be tracer slices: one observable forward proof of the plan,
not a layer-only task.

Each `M-*` should include:

- primary actor: end user, stakeholder, or developer;
- journey: what the actor can do or verify after the milestone lands;
- end-to-end layers touched;
- definition of done: actor-observable behavior, not just "tests pass";
- acceptance criteria covered;
- validation items covered;
- guardrails and invariants;
- out-of-scope boundaries;
- dependencies on prior `M-*` IDs.

## Planner-Time Validator

Before requesting final approval for a workshop plan, run a mechanical
self-review or subagent review, depending on harness support.

Check:

- every milestone has a valid primary actor;
- every milestone has actor-appropriate evidence;
- definitions of done are observable and concrete;
- guardrails and out-of-scope boundaries are specific;
- milestone dependencies are acyclic and reference known IDs;
- risky integration work is not all deferred to the end;
- no milestone silently requires another unfinished milestone to be useful.

Surface findings before asking for approval. A user may override a finding, but
record the override and reason in the artifact.

## Minimal Templates

Compact ledger:

```markdown
# <task> planner ledger

## Mission
## Context and evidence
## Assumptions
## Decisions
## Acceptance criteria
## Validation
## Out of scope
## Approval
```

Workshop spec:

```markdown
# <task> plan

## Mission
## Context and evidence
## Constraints
## Assumptions
## Decisions
## Open questions
## Acceptance criteria
## Validation plan
## Out of scope
## Milestones
## Handoff
## Approval
```

## Approval Gate

Do not proceed to implementation from this skill until the user explicitly
approves the plan or clearly asks you to continue with a named plan artifact.

