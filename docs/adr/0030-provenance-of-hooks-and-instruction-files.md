# ADR-0030: Provenance of hooks and instruction files

- Status: accepted
- Date: 2026-11-24

## Context

What an agent does is steered by files: the hooks and settings Barnraise writes for each session (`settings.json` with the PreToolUse hook, `mcp.json`, `policy.json`), the `barnraise` binary those hooks run, the prompts and rules the daemon reads from the project root (`barnraise.yaml`, `.barnraise/**`), and the instruction files the agent CLIs load from the worktree by themselves (`CLAUDE.md`, `AGENTS.md`, `GEMINI.md`, `.mcp.json`, `.claude/**`, `.codex/**`, `.gemini/**`).

Each is a way to steer the next agent without passing through the owner. An implementer can leave a `CLAUDE.md` on its item's branch that the reviewer's session then obeys. A process running as the owner's user can rewrite `.barnraise/constitution.md` or a role prompt. An agent could rewrite its own `policy.json` mid-session; worse, the hook treated a missing policy file as "not a Barnraise session" and allowed everything.

The idea of recording the provenance of what a sandboxed agent is given and refusing to run when it changed comes from [nono](https://github.com/nolabs-ai/nono) by nolabs-ai (Apache-2.0). Only the idea was taken; the design and the code are Barnraise's own, written from scratch (ADR-0018).

## Decision

Before each session starts, `internal/app/provenance.go` checks four things; any mismatch refuses the session, records `trust.mismatch` (once per distinct change) and sends the owner an urgent notice that names the files and the way to re-trust.

1. **The hook binary.** The daemon hashes its own executable when it starts. If the file on disk no longer matches, the hooks of a new session would run a binary the daemon did not start from: refused until the daemon is restarted.
2. **The project root.** The hashes of `barnraise.yaml` and every file under `.barnraise/` are recorded as a `trust.recorded` event: the first time the daemon loads the project (trust on first use), and again whenever the owner takes a sanctioned path: `barnraise up` or `barnraise reload` of the project, or `barnraise trust`. A daemon restart is **not** a sanctioned path: on load it compares with the last record, so files changed while it was down (or by something that killed it and let a service manager restart it) are reported, not adopted.
3. **The worktree's instruction files.** Every instruction file in the session's worktree must be identical to the main branch's version of that path (main is the arbiter, ADR-0004), or accepted by the owner for that item with `barnraise trust I-42` (a `trust.accepted` event with the accepted hashes). A file that exists only in the worktree counts as a change. Removed files are not a problem (removing instructions does not inject any). Heavy and generated folders (`node_modules`, `vendor`, `dist`, `build`, `.git`, `.barnraise-session`…) and hidden folders other than the agent CLIs' own are not searched.
4. **The session files.** `settings.json`, `mcp.json` and `policy.json` are hashed right after the daemon writes them and checked again just before the provider is started. The hash of `policy.json` is also handed to the session as `BARNRAISE_POLICY_SHA256`; the PreToolUse hook refuses every tool call when the policy it reads does not match it, or is missing: in a session Barnraise started, the hook now fails closed. The session files are also added to the built-in `deny_write` list and Claude's settings deny list.

`barnraise trust --status` shows what would refuse a session without changing anything. `barnraise trust` and its API (`POST /v1/trust`) are owner-only: session tokens are refused by the endpoint and the command is in the built-in `deny_commands`.

## Consequences

- An owner who edits `.barnraise/` or `barnraise.yaml` by hand runs `barnraise reload` (needed for most config changes anyway) or `barnraise trust`; until then sessions wait. The notice says so.
- When an item's purpose is to change a `CLAUDE.md` or `.claude/` file, the next session on that item (its review) is refused until the owner looks at the change and accepts it with `barnraise trust I-42`. That is the point: the reviewer must not be steered by the text under review.
- Without git (a project whose worktree is the root) only the binary, the root files and the session files are checked.
- Upgrading the binary in place while the daemon runs refuses new sessions until the restart, which the upgrade instructions already ask for.
- The check reads the main branch's version of each instruction file once per main commit (cached) and walks the worktree once per session start.
