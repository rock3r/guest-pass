---
name: cal-industries
description: >
  Harness-portable autonomous delivery loop for a scoped software task:
  front-load uncertainty, produce or confirm a plan, implement with TDD and
  validation, run review passes, prepare git/GitHub artifacts, and babysit the
  PR when authorized. Use when the user wants a mostly hands-off
  problem-to-PR workflow.
---

# Cal Industries

Use this skill when the user wants an end-to-end software delivery loop, such
as "handle the whole thing", "take this from issue to PR", "I'll be AFK", or
"scope it, implement it, review it, and ship it".

The principle is: **front-load uncertainty, then execute steadily**. Ask
important questions early, then avoid mid-task interruptions unless new facts
make the approved plan unsafe or impossible.

This is a portable flavor. It does not assume a specific harness, UI driver,
language, CI stack, or review bot.

## Project Overrides

If `.agents/cal-industries.md` exists, read it immediately after this skill.
Project-local instructions there override this file where they conflict.

## Terminals

Aim to finish at one of:

- completed local change, validated and ready for user review;
- PR opened and ready for review;
- ready-to-merge PR awaiting explicit approval;
- merged PR, only if the user explicitly authorized merge;
- blocked with a concrete reason and next decision needed.

## Phase 1 — Problem Statement And Scoping

Turn the user's request into an implementation contract.

1. Restate the mission:
   - what appears broken or missing;
   - who is affected;
   - what success looks like;
   - what is unknown.
2. Investigate before questioning:
   - read `AGENTS.md`;
   - read relevant committed docs;
   - inspect referenced issues/files/logs/screenshots;
   - identify likely code paths.
3. Ask one consolidated question batch only when investigation cannot resolve
   important ambiguity.
4. Decide whether a formal plan is needed:
   - small/obvious: concise action plan in chat or work log;
   - medium/large/risky: use `interactive-planner` and write a `.plans/`
     artifact or equivalent durable handoff.
5. Secure approval:
   - scope;
   - out-of-scope boundaries;
   - validation strategy;
   - whether commits, pushes, PR creation, babysitting, or merge are approved.

Do not enter Phase 2 until the plan or direction is approved.

## Phase 2 — Autonomous Software Factory

### 1. Create Isolation

If the repo policy or task risk calls for it, use `using-git-worktree` before
editing tracked files. Otherwise confirm the branch is appropriate.

### 2. Keep A Work Log

For non-trivial tasks, maintain a gitignored work log, preferably under
`.plans/` or `.cal-industries/` if the project ignores it.

Record:

- decisions and why they changed;
- TDD checkpoints;
- validation commands;
- review findings and resolutions;
- plan deviations or user-approved amendments;
- blockers.

### 3. Implement With Normal Discipline

- Follow `AGENTS.md`.
- Read relevant docs before touching code.
- Use TDD for behavior changes.
- Keep changes scoped to the approved plan.
- Update docs that describe changed behavior or contracts.
- Do not weaken lint, tests, security checks, or CI gates.
- Do not silently change behavior outside the requested scope.

### 4. Validate

Run the narrowest meaningful checks first, then broader checks as risk grows.
Use commands the repo actually defines. Examples once the toolchain exists:

```bash
go test ./...
npm test
npm run lint
npm run build
```

If no project checks exist yet, validate by inspection and explain that there
is no runnable test harness.

### 5. Review

Use the right review layer:

- `plan-adherence-reviewer` for implementation-vs-plan drift;
- `review-workflow-guidance` for general code review selection;
- built-in harness review tools or subagents when available;
- self-review when no separate reviewer exists.

Fix confirmed findings. If a finding requires product judgement or plan drift,
stop and ask the user.

### 6. Git And GitHub

Use `git-github-ops` for commits, pushes, PR text, comments, and merge prep.

Approval rules:

- Commit when the user asked for a commit or the repo workflow calls for it.
- Push only when authorized.
- Open a PR only when authorized.
- Merge only when explicitly authorized in the current task.

### 7. Babysit PR

When a PR exists and the user asked for monitoring, use `babysit-pr`.

Batch fixes before pushing. Every push may retrigger CI and review bots, so
collect CI failures and review comments into one local fix cycle when possible.

## Blockers

Stop and ask when:

- the approved plan is wrong or underspecified;
- validation reveals a larger unrelated failure;
- comments require product/design/security judgement;
- credentials or production access are needed;
- the repo has unrelated dirty changes that make safe editing uncertain;
- approval is needed for push, PR creation, or merge.

## Final Report

Report:

- what changed;
- validation run and result;
- review passes run;
- commit/PR status if applicable;
- any residual risks or follow-ups.

