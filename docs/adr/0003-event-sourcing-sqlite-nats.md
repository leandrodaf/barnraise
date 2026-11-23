# ADR-0003: Event sourcing in SQLite, mirrored over NATS JetStream

- Status: accepted
- Date: 2026-11-23

## Context

The state of items, flows, accounts and machines must survive crashes, be auditable, allow redoing any step, and exist on every machine of a cluster even when the machines cannot see each other.

## Decision

- The state is an **append-only event log** stored in SQLite (`barnraise.db`, WAL mode). The current state is derived from the events; snapshots in event payloads let any machine rebuild the same state.
- Each machine runs an **embedded NATS server with JetStream**. Local events are published to its stream; a replicator reads each peer's stream from a stored cursor and ingests events it does not have. Ingestion is idempotent by event id, events of the same item stay in order, and live-only events (heartbeats, sessions) are applied but never stored.
- Large artifacts (logs, screenshots, diffs, binaries) go to a content-addressed object store; messages carry only the hash.
- SQLite also holds the query side: tokens, audit, and the full-text knowledge index (FTS5).
- Postgres stays an option for many projects or a team.

## Consequences

- Full history and `barnraise item redo <id> <step>` come for free; the timeline of an item is a query.
- A machine works alone when isolated and reconciles when it reconnects.
- Who holds an item is **not** decided by the event log: that is the git arbiter (ADR-0004), so mirrored events never need conflict resolution.
- Event types are a public contract (CloudEvents, webhooks), versioned like the API.
