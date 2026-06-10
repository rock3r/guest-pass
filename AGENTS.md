# AGENTS.md

Operating rules for agents working in this repo. Keep this file focused on
workflow, safety, and source-of-truth rules; implementation details belong in
approved docs once they exist.

`guest-pass` is intended to become a free, open-source guest-management app for
live streams: a thin Go backend, SQLite persistence, static frontend assets, OBS
browser-source pages, and peer-to-peer WebRTC media. The current architecture
design is tentative and not approved. Treat it as context only until it is copied
or superseded by committed project docs.

## Read first, every session

Before writing or modifying production code, inspect the current repo state and
read the relevant committed docs in full. As the project is still being
bootstrapped, expect the doc set to grow; once present, prefer this order:

| Document | Use it for |
| --- | --- |
| `docs/ARCHITECTURE.md` | Approved architecture, module boundaries, data flow, invariants |
| `docs/CONVENTIONS.md` | Go, frontend, database, logging, and file-placement conventions |
| `docs/TESTING.md` | Test layering, TDD expectations, local and CI commands |
| `docs/DEPLOYMENT.md` | Self-hosting, docker-compose, TURN, config, migrations |
| `.plans/worklog.md` | Gitignored session memory, decisions, open threads, next action |

If a design/spec lives outside the repo or is marked tentative/WIP, use it only
to understand intent. Do not implement it as an approved plan unless the owner
explicitly says it is approved or commits it as the source of truth.

## Non-negotiables

- **TDD red -> green for behavior changes.** Write the failing test first, run
  it, verify it fails for the expected reason, implement the minimum fix, then
  rerun to green. For pure docs/config bootstrap, validate with repo inspection.
- **Do not weaken quality gates.** Do not relax lint, formatting, tests,
  security checks, CI, or suppressions to get green. Fix findings at the source
  or surface the blocker.
- **Scope stays tight.** Keep changes related to the requested task. Flag any
  behavior change the user did not ask for as a regression risk.
- **Docs move with behavior.** When a change touches a surface described by a
  committed doc, update that doc in the same session. Stale docs are a
  regression.
- **No secrets.** Never commit real credentials, tokens, OAuth secrets, TURN
  secrets, private hostnames, deployment-specific IPs, or personal account
  details. Use placeholders in committed examples.

## Expected project shape

Until committed docs say otherwise, assume the likely implementation will use:

- Go for the backend and embedded static assets.
- SQLite for local persistence.
- WebSockets for signaling.
- Browser WebRTC APIs for media; the server must not process media.
- A frontend built into the Go binary.
- `docker-compose` for app + coturn self-hosting.

These are orientation notes, not authorization to implement the tentative design.

## Work isolation

Use a feature branch or git worktree for non-trivial implementation. If you are
on `main` and about to make product-code changes, prefer the
`.agents/skills/using-git-worktree` workflow unless the owner has directed you to
work in the current checkout.

Project-local worktrees should live under `.worktrees/`, which is gitignored.
Session plans and scratch notes should live under `.plans/`, which is also
gitignored.

## Actions requiring explicit user approval

Do not do these unless the user clearly asked in the current task:

- Open, close, merge, or mark ready a pull request.
- Push to a remote branch.
- Commit to a branch other than the one used for the current task.
- Touch real production infrastructure, OAuth apps, email providers, TURN
  servers, DNS, or deployment hosts.
- Add a new dependency to authentication, token, signaling, or media paths.

An approach discussion is not approval.

## Working style

- Start by reading the repo and the smallest relevant docs/files.
- Prefer existing repo patterns once they exist.
- Keep commands simple and explicit about the working directory.
- Use structured APIs/parsers for structured data.
- After about three failed attempts with one approach, step back and choose a
  different approach instead of stacking tentative fixes.
- Before handing off, report what changed and what validation ran.

## Local skills

Check `.agents/skills/` before inventing a workflow. The curated project-local
skills cover:

- `using-git-worktree` — isolated feature work.
- `interactive-planner` — durable scoping/spec artifacts before risky work.
- `cal-industries` — mostly hands-off implementation and review loop.
- `git-github-ops` — commit, push, PR text, GitHub CLI workflow.
- `babysit-pr` — PR/CI/review monitoring.
- `review-workflow-guidance` — choosing a review pass.
- `plan-adherence-reviewer` — read-only implementation-vs-plan review.
