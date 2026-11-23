# ADR-0012: AI tools through their CLIs, with subscriptions

- Status: accepted
- Date: 2026-11-23

## Context

The owner pays for subscriptions (Claude Max, ChatGPT plans). Calling the APIs per token would cost far more and lose what the CLIs add: hooks, skills, subagents, sandboxes, session management.

## Decision

Barnraise drives Claude Code, Codex and other tools **through their CLIs**, each account with its own config dir (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`). `barnraise account add` creates the dir, runs the provider's login (`claude setup-token` or `codex login --device-auth` with `--headless`), tests it with a minimal prompt and puts the account in the rotation. API keys are optional, per project.

## Consequences

- Accounts, their 5-hour and weekly usage and reset times are first-class (`barnraise account list`); the scheduler and the model router use them.
- Barnraise follows each provider's terms; a change in subscription policy is a tracked risk.
