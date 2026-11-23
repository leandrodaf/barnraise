# ADR-0010: A scripted fake provider for end-to-end tests

- Status: accepted
- Date: 2026-11-23

## Context

End-to-end tests must go from `barnraise init` to an integrated item on every pull request, fast and deterministic, without subscriptions or tokens. Real AI sessions are slow, cost limits and are not reproducible.

## Decision

- `barnraise daemon --fake-provider` replaces the AI providers with a scripted one (`cmd/barnraise/fake.go`, `internal/adapters/out/fakeprovider`) that plays each step like an agent would: writes `plan.md`, changes files, delivers and reviews through the real API.
- `cmd/barnraise/e2e_test.go` runs the whole flow on a sample repository with real git (a local bare repository as remote), real SQLite and real NATS. `cmd/barnraise/cluster_test.go` runs two machines and a failover. Both are skipped with `-short` and need `git`.
- A nightly job runs a tiny smoke with real Claude Code when a `CLAUDE_CODE_OAUTH_TOKEN` secret exists, measuring tokens.

## Consequences

- Flow, arbiter, merge queue, checks and the API are covered end to end on every change.
- Provider adapters are covered separately with recorded sessions; the fake does not prove a real CLI still works, which is what the nightly smoke is for.
