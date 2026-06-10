---
name: review-workflow-guidance
description: Guide agents to choose a review pass intentionally and keep findings structured.
---

# Review Workflow Guidance

Use this when a task needs a post-implementation review pass.

## Rules

- Prefer a first-class review tool or subagent when the harness provides one.
- Use a quick review for narrow changes.
- Use a deeper review for security-sensitive, authentication, token, signaling,
  WebRTC, persistence, or deployment changes.
- Keep findings severity-ranked and grounded in file/line references.
- Treat structured `::code-comment` findings as typed review output, not
  decorative markdown.
- Do not paste giant diffs into prompts when the review tool can inspect the
  worktree directly.

## Output posture

Findings first, then questions/assumptions, then a short summary. If no issues
are found, say so and name any residual test gaps.

