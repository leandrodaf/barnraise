# ADR-0021: A branch and a draft pull request per item from the start

- Status: accepted
- Date: 2026-11-23

## Context

If a machine dies, nothing must be lost, and the review should happen where people already look.

## Decision

When an item is taken, Barnraise creates the branch `br/<item>-<slug>` from main and opens a draft pull request with the goal and the criteria as a checklist. Work is committed and pushed at the end of each step (with a `Barnraise-Item: I-<n>` trailer) and at least every 15 minutes (`worktree.safety_commit`), after a secret scan. The pull request leaves draft when the delivery is `DONE`. The merge queue integrates with a rebase on the current main and a new check, in batches with bisection on failure; history is one commit per item (`squash`) or the step commits.

## Consequences

- Whoever resumes an item continues from the last step, not from zero.
- Checks show up as checks of the pull request and reviews as reviews.
- Without a code host that has pull requests, branches and commits still work and the review stays in the panel.
