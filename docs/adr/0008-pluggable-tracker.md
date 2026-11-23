# ADR-0008: Pluggable tracker, local by default

- Status: accepted
- Date: 2026-11-23

## Context

People already see their work somewhere: GitHub Issues and Projects, Jira, Linear, GitLab. Some projects have nothing yet and should start in a minute.

## Decision

- The tracker is a port with adapters for `local` (the default: items live in the daemon and the panel), `github`, `gitlab`, `jira` and `linear`, selected by `tracker.type`.
- **The daemon owns the flow state** (rework, limits, who holds what). The tracker is the window where people read and comment, synchronized both ways, with columns mapped to Barnraise statuses.
- The code host (branches, pull requests, workflow dispatch) is a separate port from the tracker, so a project can keep items in Jira and code on GitHub.
- Tokens go to the keychain with `barnraise tracker connect <type>`; `barnraise tracker migrate <from> <to>` moves items keeping their ids.

## Consequences

- Text from trackers is untrusted: it is sanitized and fenced, and only `tracker.trusted` authors or labels create items directly.
- Each tracker adapter is tested against recorded API responses.
