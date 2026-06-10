---
name: using-git-worktree
description: Use when starting feature work that should be isolated from the main checkout; creates or verifies an isolated git worktree with safety checks.
---

# Using Git Worktrees

Use this skill before non-trivial implementation when the current checkout is on
the default branch and the user has not asked to work directly there.

## Pre-flight

Check whether isolation already exists:

```bash
git rev-parse --git-common-dir
git rev-parse --git-dir
git rev-parse --abbrev-ref HEAD
```

If `git-common-dir` differs from `git-dir`, you are already inside a worktree.
If the current branch is a non-default feature branch, treat that as enough
isolation unless the user asked for a separate worktree.

## Directory

Use `.worktrees/<slug>` from the repo root. Verify it is ignored:

```bash
git check-ignore -q .worktrees
```

If it is not ignored, add `.worktrees/` to `.gitignore` before creating the
worktree.

## Create

Choose a short descriptive branch name, normally `wt/<slug>`:

```bash
git fetch origin
git worktree add .worktrees/<slug> -b wt/<slug> origin/main
```

If the default branch is not `main`, use the repository default instead.

After creation, run commands from the worktree path explicitly. In app or tool
sessions where `cd` does not persist, use absolute paths.

## Setup and baseline

Run project setup only when the relevant files exist:

```bash
go mod download       # if go.mod exists
npm install           # if package.json exists
go test ./...         # if Go packages exist
npm test              # if package.json defines tests
```

If the baseline fails before your changes, report the failure and ask whether to
investigate or continue.

