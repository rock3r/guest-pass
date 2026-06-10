---
name: babysit-pr
description: Watch an open GitHub pull request for CI, mergeability, bot reviews, and human feedback until it is ready, closed, or blocked.
---

# PR Babysitter

Use this skill when the user asks to monitor an open PR, watch CI, handle review
comments, or keep an eye on failures and feedback.

## Inputs

- No PR argument: infer from the current branch with `--pr auto`.
- PR number: `123`.
- PR URL: `https://github.com/<owner>/<repo>/pull/123`.

Start by reporting the PR being watched.

## Core commands

```bash
python3 .agents/skills/babysit-pr/scripts/gh_pr_watch.py --pr auto --once
python3 .agents/skills/babysit-pr/scripts/gh_pr_watch.py --pr auto --snapshot
python3 .agents/skills/babysit-pr/scripts/gh_pr_watch.py --pr auto --watch
python3 .agents/skills/babysit-pr/scripts/gh_pr_watch.py --pr auto --retry-failed-now
```

Use `--repo OWNER/REPO` when the current checkout does not let `gh` infer the
repository.

## Loop

1. Run `--once` so the script waits until something needs attention.
2. Read the JSON `actions`.
3. Diagnose failures or comments.
4. Fix branch-related issues locally.
5. Run project-appropriate validation.
6. Push only after batching all currently known fixes and after approval when
   required by `AGENTS.md`.
7. Run `--once` again until terminal.

## Stop conditions

The watcher emits action names such as:

- `stop_pr_closed` — PR was merged or closed.
- `stop_ready_to_merge` — CI is green, no blocking reviews, no conflicts.
- `stop_exhausted_retries` — flaky retry budget is exhausted.
- `stop_non_retryable_failure` — a failure needs diagnosis/fix.
- `diagnose_hung_check` — a check appears stuck.
- `diagnose_merge_conflict` — resolve conflicts.
- `diagnose_branch_behind` — update branch from base.
- `wait_codex` — Codex is still reviewing.

## Validation

Use the commands appropriate for the current repo state. Once this project has a
Go/backend and frontend, expect something like:

```bash
go test ./...
npm test
npm run lint
```

Do not treat these placeholders as mandatory before the matching toolchain
exists.

## Heuristics

See `references/heuristics.md` and `references/github-api-notes.md` for CI
classification and API details.

