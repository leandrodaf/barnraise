# ADR-0029: Session tokens bound to their session, and a hash-chained event log

- Status: accepted
- Date: 2026-11-24

## Context

Two things the daemon relies on could be abused by something that runs next to an agent.

1. **Session tokens.** Every agent session gets a token (`BARNRAISE_TOKEN`) so its MCP server, hooks and `barnraise` commands can talk back to the daemon. The token carried the role's scopes and the session's item, but each handler checked only what it was written to check: the item binding was applied to deliveries and reviews, and any procedure that only asked for a scope the role had could be called with any item, and procedures that asked for no scope at all were open. A token copied out of a session (printed by a tool, left in a log, read by a dependency's install script) was worth more than the session it came from, and lived one hour past the session's timeout.
2. **The event log.** The log in `barnraise.db` is the source of truth: items, approvals, deliveries and the owner's answers are all events. Anyone who could write the file could edit history (turn a refusal into an approval, remove a blocked command) and nothing would show it.

The idea of capabilities scoped to one unit of work and of a tamper-evident audit chain comes from [nono](https://github.com/nolabs-ai/nono) by nolabs-ai (Apache-2.0). Only the ideas were taken; the design and the code here are Barnraise's own, written from scratch (ADR-0018).

## Decision

**Session tokens are bound to their session and checked before any handler runs.**

- The token's actor carries its project, item, step, role scopes and a session id; it expires at the session's timeout plus 5 minutes and is revoked the moment the session ends (as before).
- A Connect interceptor in the HTTP adapter (and a wrapper on `GET /v1/events`) calls `app.AuthorizeCall` for every call made with a session token. The rule table in `internal/app/grant.go` is an allow-list: a procedure missing from it is refused to every session token (token, account, machine, policy, focus-clearing, takeover, owner answers and the rest). For a listed procedure the session must hold the procedure's scope, the request's `project` must be the session's, and for procedures that act on an item every `item_id` in the request (at any depth) and the top-level `id`/`target` where it names an item must be the session's item. Reads (status, context, find, related…) stay project-wide because an agent needs its neighbours' context; the analyst, whose job spans the backlog, is free of the item binding for asking, messaging and approving.
- A refused call is recorded as `security.token.denied` (the first five per session) and in the audit trail, and the owner is notified once per session: a session token used outside its session is the signature of a leaked token.
- Owner and API tokens are untouched; the handlers still apply every actor's scopes as before.

**The event log is a hash chain.**

- Store migration 2 adds `prev_hash` and `hash` to `events`. `hash` is SHA-256 over a domain-separated, length-prefixed canonical form of the event (seq, id, project, type, subject, machine, actor, time in UTC nanoseconds, raw data) and the previous event's hash; the first event links to "". `Append` computes the links inside the write transaction, reading the tip in the same transaction, so writers in other processes chain correctly too.
- **Existing rows are chained by the migration** (in seq order, inside the migration's transaction), and the migration records a genesis marker (`events.chain.genesis` in the `kv` table: the last seq and the time). We chose this over leaving old rows unchained so that one walk covers the whole log; the marker states the limit honestly: events up to the genesis seq are vouched for only as they were at migration time.
- `barnraise events verify` walks the chain (through the daemon, or the state file directly with `--offline`) and names the first break: `hash` (a row was edited), `link` (a row was removed or inserted), `missing` (a row was written outside Barnraise). It exits with code 2 on a break.
- A chain alone cannot show that the newest events were cut off, nor resist someone who rewrites every hash after the row they edit. So the daemon writes a **signed anchor**: at start and every hour, once the tail checks out, it signs the head (seq, hash, time, machine) with the machine's NKey from `daemon.yaml` and stores it in `kv`. Verification checks the signature against this machine's public key and that the anchored event still has the anchored hash.
- At start, and before each new anchor, the daemon verifies the last 2000 events and the anchor; a problem is recorded once as `security.chain.broken` in every project and sent to the owner as an urgent notice. The warning is itself appended after the break it reports.

## Consequences

- A leaked session token can, at most, do what that session could do on its own item, for the rest of that session. Adding a new procedure to the API means deciding whether sessions may call it; by default they may not.
- Each event write does one extra `UPDATE` in the same transaction; the cost is negligible next to the fsync.
- The chain is per machine: each daemon chains its own copy of the log (replicated events are chained where they are stored). It detects tampering with the file; it does not stop it. Someone who can read `daemon.yaml` (the machine key) and write `barnraise.db` can still forge a consistent log: protect `~/.config/barnraise/` as the documentation already asks, and keep backups.
- An operator who repairs the database by hand will see `barnraise events verify` report it; that is intended.
