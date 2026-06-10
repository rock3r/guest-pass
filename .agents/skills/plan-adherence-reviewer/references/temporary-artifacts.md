# Temporary validation artifacts

The reviewer is **read-only** with a controlled exception for temporary
validation. This file defines that carve-out and how the reviewer must
handle it.

## What's allowed

The reviewer may:

- read files, diffs, tests, logs, docs, issue/PR text;
- run existing tests and validation commands when needed to verify a
  suspected finding (e.g., re-run a benchmark to confirm a guardrail is
  honored);
- create temporary tests, scripts, or harnesses in scratch or
  clearly-isolated locations solely to validate a suspected finding;
- use harness-specific runtime tooling when available and relevant.

## What's not allowed

The reviewer may **not**:

- modify production source files;
- modify existing tests or docs;
- commit any change;
- apply fixes;
- leave temporary validation artifacts behind after the review.

If a finding requires changing production code to verify, the reviewer
**must not** make the change. It surfaces the finding and lets the user
or implementer act.

## Where temporary artifacts go

Prefer paths the project already treats as scratch:

- `.cal-industries/<task name>/reviewer-scratch/`,
  `.plans/<plan name>/scratch/`, or `$TMPDIR` when those paths are
  available and ignored.
- Any explicitly named scratch directory the user or project provided.

Never:

- write inside production source directories (`src/main/...`,
  `src/test/...`, `docs/`, `.agents/`);
- write at the repo root using a name that could be mistaken for
  production output (`temp.kt`, `debug.txt`);
- create new top-level directories that aren't gitignored.

## Naming and labelling

Temporary artifact names should make their purpose obvious:

- prefix with `reviewer-` (e.g., `reviewer-validation-probe.txt`);
- contain the milestone ID when scoped to one (e.g.,
  `reviewer-M-1-validation-output.txt`);
- include a short purpose comment at the top of any code file
  ("Temporary reviewer probe for M-1 validation. Delete after
  review.").

## Cleanup contract

Before the reviewer finalizes its output:

1. Delete every temporary file or directory it created.
2. Note in the `Temporary artifacts` section of the output:
   - what was created,
   - what was deleted,
   - what (if anything) remains and why.

If cleanup fails (e.g., file in use, permission error), the reviewer must:

- **not** silently continue;
- list the remaining artifacts prominently in the output;
- flag the cleanup failure as a `[fail]` under `Validation`;
- prefer the verdict `BLOCKED` until the artifacts are removed, because
  leaving reviewer scratch behind is a regression in its own right.

Cleanup is a hard requirement, not a politeness. The whole reviewer is
worthless if it leaves a trail of half-written probes that other agents
later mistake for real artifacts.
