# ADR-0014: Generic core, project specifics in barnraise.yaml

- Status: accepted
- Date: 2026-11-23

## Context

The first project is an Android app with Maestro and a design reviewer; the next could be a backend or a blog. Copying and adapting scripts per project is what the old system required.

## Decision

The core knows nothing about Kotlin, Maestro or blogs. Each project has a short `barnraise.yaml` that keeps only what differs from its **template** (`mobile-android`, `backend`, `web`, `content`, `blank`, or one saved with `barnraise template save`), merged over the built-in defaults. Roles live in `.barnraise/roles/*.md`, standards in `.barnraise/standards/*.md`, the constitution in `.barnraise/constitution.md`. No state in the repository.

## Consequences

- `barnraise init` writes the file from a conversation; `barnraise check` and `barnraise explain` make it understandable.
- Flows, checks and rework are data the daemon executes, not prompt text.
