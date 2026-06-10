# Project-local skills

This folder holds lightweight, agent-facing playbooks for workflows that should
be consistent across Codex, Claude, and other coding agents.

The set is intentionally small. It borrows useful generic pieces from
`fineco-helper`, `earworm`, and `compose-pi`, while leaving behind their
project-specific harnesses, deployment assumptions, UI frameworks, and CI gates.

## Skills

### `using-git-worktree`

Creates an isolated git worktree or confirms the current checkout is already
isolated. Use before non-trivial implementation when the current branch is the
main/default branch.

### `interactive-planner`

Harness-portable requirements-capture workflow. Use for vague, risky,
architectural, multi-workstream, or scope-drift-prone work before
implementation.

### `cal-industries`

Harness-portable autonomous delivery loop. Use when the user wants a task taken
from scoping through implementation, validation, review, and optionally PR
babysitting.

### `git-github-ops`

Guides commit, push, PR, and GitHub CLI work. It emphasizes diff-grounded
messages, non-interactive commands, and file-based multiline text for `gh`.

### `babysit-pr`

Polls an open PR for CI status, mergeability, bot reviews, and human comments.
It includes the reusable watcher script from `fineco-helper`, with repo-specific
validation instructions kept in the skill text instead of baked into the script.

### `review-workflow-guidance`

Helps pick the right review pass and keeps review output structured.

### `plan-adherence-reviewer`

Read-only review workflow for checking an implementation milestone against an
approved plan/spec. Useful once the architecture plan is approved and broken
into milestones.
