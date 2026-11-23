# ADR-0023: Security by OS layers, not by prompt

- Status: accepted
- Date: 2026-11-23

## Context

Agents read untrusted text, could reach private data and could send data out. Prompts and automatic permission classifiers miss part of the injection cases.

## Decision

Security is layered and enforced by the operating system and the daemon: secrets in the keychain and off the agents' machines; one Linux user per agent account (`barnraise account add --isolate`); a PreToolUse hook that decides locally from a compiled policy and works even with `--dangerously-skip-permissions`; Claude Code's native sandbox whenever bubblewrap works and Codex in `workspace-write`; agent traffic forced through an allow-list proxy by `nftables` rules per agent user (`barnraise daemon --egress`, `barnraise egress rules`); sanitized and fenced external text; scopes per role in the daemon; a secret scan before every push; scoped tokens, audit, signed webhooks and per-machine nkeys. See `docs/security.md`.

## Consequences

- Some convenience is lost (agents cannot read the owner's home, push to main or change workflows); those actions go to the owner.
- The sandbox and isolated users may need documented exceptions for emulators.
