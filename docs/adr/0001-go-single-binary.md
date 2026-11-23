# ADR-0001: Go, one binary

- Status: accepted
- Date: 2026-11-23

## Context

Barnraise is glue between processes: AI CLIs, git, test runners, emulators, trackers. It must run the same on a laptop, a server without a screen and a friend's machine, and every machine of a cluster must run the same version. Agents also write part of Barnraise, and they make fewer mistakes in a small, explicit language.

## Decision

Barnraise is written in Go and ships as **one static binary** (`CGO_ENABLED=0`, pure-Go SQLite via `modernc.org/sqlite`, NATS server embedded) built from `./cmd/barnraise`. The same binary is the daemon, the CLI, the MCP server and the provider hooks; the mode is the subcommand. The version is set at build time with `-ldflags "-X main.version=…"`.

## Consequences

- Installing is copying one file; `install.sh`, the container image and the release archives all carry the same binary for linux and darwin, amd64 and arm64.
- Goroutines follow hundreds of agent sessions at no relevant cost; the limit is accounts and machines, not the orchestrator.
- Hooks run on every tool call and must start fast: a Go binary starts in milliseconds.
- Dependencies that pull large trees (for example `tsnet`, see ADR-0007) are weighed against binary size.
