# ADR-0006: Providers by capability, never the lowest common denominator

- Status: accepted
- Date: 2026-11-23

## Context

Claude Code, Codex and the other CLIs evolve fast and differ: stream output, hooks that can block, subagents with their own model, skills, sandboxes, session resume, usage limits in the status line. Leveling them down to what they all share wastes the best features; hard-coding one CLI's commands in the core makes every change painful.

## Decision

- Each provider is an adapter (`internal/adapters/out/claude`, `codex`, …) that **declares capabilities**; the core asks for a capability, never a command. Missing capabilities are emulated (a fresh session with a summary of `plan.md` instead of resume, policy enforced by the daemon instead of hooks, and so on).
- Adapters detect the installed CLI version and probe each capability before using it.
- Events are normalized (message, tool use, tokens, limit reached, permission request, completion, error) and the original event travels along, so a new feature can be used before the common format names it.
- Providers run through their CLIs with the owner's subscriptions; an API key is optional. Every session runs with `DISABLE_AUTOUPDATER=1`.

## Consequences

- Claude Code sessions get Barnraise's hooks, status line, MCP server and native sandbox; Codex runs with `--sandbox workspace-write`.
- Contract tests per adapter run against recorded `stream-json` sessions and the installed CLI version.
- A new CLI version can break an adapter; the update is rolled out to one machine first.
