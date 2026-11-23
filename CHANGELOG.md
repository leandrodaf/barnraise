# Changelog

All notable changes to Barnraise are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [semantic versioning](https://semver.org/). Release notes on GitHub are generated from conventional commits.

## [0.1.0] - unreleased

First version: one Go binary that is the daemon, the CLI, the MCP server and the provider hooks.

### Added

- **Daemon as the only authority** with an append-only event log in SQLite, the current state derived from events, and `item redo` from any step.
- **Projects** from a short `barnraise.yaml` merged over templates (`mobile-android`, `backend`, `web`, `content`, `blank`, and saved ones); `init` in conversation, `check`, `explain`, `template save`.
- **Goal, focus and capacity**: a goal with a measurable end and an inventory, a temporary focus with a preview of its effect, and the 80/15/5 split between goal, bugs and improvements, plus a weekly self-improvement lane.
- **Items** of 2 to 6 hours with EARS criteria tied to checks, anchors, priorities, `--risky` items with two parallel attempts, drafts refined by the analyst, tweaks that ride with the next item of their area, and proposals with a fixed shape, a daily limit, expiry and duplicate detection.
- **Flows as data**: agent, script, approval, check and merge-queue steps, rework rules with a limit and model escalation, then the owner; plan approval before code; reviews with two verdicts.
- **Branch and draft pull request per item**, commits per step with an item trailer, safety commits, and a merge queue with rebase, a new check, batches and bisection.
- **Providers by capability**: Claude Code and Codex through their CLIs and subscriptions, several accounts per provider with usage and reset times, model routing by class with fallbacks, a status line that records real limits, and a scripted fake provider for tests and demos.
- **Hooks**: `pre-tool` (local policy decisions), `prime`, `mail`, `post-tool`, `stop`; output filtering of noisy commands (`barnraise filter`); screenshots reduced for agents (`barnraise image reduce|crop`).
- **Security layers**: blocking policy for paths and commands, isolated Linux users per account, Claude Code's native sandbox when bubblewrap works, Codex in `workspace-write`, an egress allow-list proxy with `nftables` rules per agent user (`barnraise daemon --egress`, `barnraise egress proxy|rules`), sanitizing and fencing of untrusted text, a secret scan before every push, scoped tokens, an audit log.
- **Several machines**: git as the arbiter (`refs/barnraise/claims`, compare-and-swap, 30 s renewals, 2 min ttl on the local clock), NATS JetStream mirroring, pairing with a 6-digit code over CPace, Tailscale when installed, failover from the last pushed step, and cluster self-update to the newest version found on a peer.
- **API**: ConnectRPC (gRPC, gRPC-Web, JSON with snake_case names) from protobuf, OpenAPI at `/v1/openapi.yaml`, CloudEvents over SSE at `/v1/events`, HMAC-signed outgoing webhooks with retries, verified inbound webhooks (GitHub, GitLab, generic), owner notifications (ntfy, Telegram, Slack, email, webhook, desktop).
- **MCP server** with typed tools filtered by role.
- **Trackers**: local, GitHub Issues and Projects, GitLab, Jira and Linear, with `tracker connect` and `tracker migrate`.
- **Knowledge and the living plan**: short ids, `.barnraise/` scaffold, `context`, `find`, `get`, `related`, `why`, `history`, `rules`, revalidation with a cheap triage and a nightly sweep, and `eval` against integrated items.
- **Web panel** served by the daemon, in English and Portuguese.
- **Packaging**: release archives for linux and darwin (amd64, arm64) with checksums, SBOMs and keyless cosign signatures, `install.sh`, a container image, a systemd user service (`up --service`) and a system unit.

[0.1.0]: https://github.com/leandrodaf/barnraise/releases/tag/v0.1.0
