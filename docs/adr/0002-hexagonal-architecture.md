# ADR-0002: Hexagonal architecture, enforced by depguard

- Status: accepted
- Date: 2026-11-23

## Context

Barnraise orchestrates production code of other projects, so its rules must be testable without git, network, real AI sessions or sleeping. Providers, trackers and code hosts change often; the rules of items, flows and rework should not change with them.

## Decision

The code follows ports and adapters:

| Layer | Package | May depend on |
| --- | --- | --- |
| Domain | `internal/domain` | the standard library only |
| Ports | `internal/ports` | domain |
| Application | `internal/app` | domain and ports |
| Inbound adapters | `internal/adapters/in/*` (httpapi, mcp, cli, hooks, panel) | application |
| Outbound adapters | `internal/adapters/out/*` (claude, codex, github, nats, sqlite, git, …) | ports, never the application or inbound adapters |
| Composition root | `cmd/barnraise` | everything |

The rule is checked in CI by `depguard` rules in `.golangci.yml`; a domain that imports NATS, HTTP or git fails the pipeline. Time, randomness and processes enter through ports (`Clock`, `IDGen`, `Exec`).

## Consequences

- The domain is deterministic and covered by table and property tests (`rapid`); CI requires 90% coverage of `internal/domain` and `internal/app`.
- Every port has an in-memory fake and a contract suite shared by fakes and real adapters.
- Adding a provider or tracker is adding an adapter; the core does not change.
- Some convenience is lost: an adapter cannot reach into the application for a shortcut.
