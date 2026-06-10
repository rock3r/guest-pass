---
name: git-github-ops
description: Use for commit, push, pull request, issue, and GitHub CLI workflows; emphasizes diff-grounded text and safe non-interactive commands.
---

# Git + GitHub Ops

Use this skill when the user asks for git or GitHub execution, not merely code
edits.

## Rules

- Use `gh` for GitHub operations.
- Inspect real diffs before writing commit or PR text.
- Keep shell commands simple and non-interactive.
- Put multiline commit, PR, issue, and comment bodies in temp files; pass those
  files to `git` or `gh`.
- Follow `AGENTS.md` for actions requiring explicit approval.

## Commit messages

Summarize only what is visible in the diff.

- Subject line: imperative, specific, <= 72 characters.
- Body: bullets with concrete changed behavior/files/validation.
- No conventional-commit prefixes.
- Do not invent issue IDs, people, emails, or private details.

Example shape:

```text
Add project-local agent workflow docs

- Add shared AGENTS guidance and Claude pointer
- Curate reusable local skills for worktrees, git ops, and reviews
- Ignore local plans and worktree scratch directories
```

Commit with file input:

```bash
git diff --staged
git commit -F /tmp/guest-pass-commit-message.txt
```

## Pull requests

PR titles should name the touched area and outcome:

```text
agents: add shared workflow guidance
```

PR bodies should be written to a temp file and passed with `--body-file`.

```markdown
## Summary
- What changed
- Why it is needed

## Testing
- [x] Command or manual validation

## Risks
- Notable risks, migrations, or follow-up constraints

## Scope
- In scope: ...
- Out of scope: ...
```

Create only after approval:

```bash
gh pr create --title "<title>" --body-file /tmp/guest-pass-pr.md --base main --head <branch>
```

## Comments and issues

Use file-first patterns:

```bash
gh pr comment <number> --body-file /tmp/guest-pass-pr-comment.md
gh issue comment <number> --body-file /tmp/guest-pass-issue-comment.md
```

Do not add a literal `Labels` section to issue bodies; use GitHub labels.

