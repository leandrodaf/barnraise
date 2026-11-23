# ADR-0022: Living plan: anchors, short ids and layered search

- Status: accepted
- Date: 2026-11-23

## Context

A plan with a thousand items rots with every integration, decision or focus change, and agents waste tokens searching blindly.

## Decision

- Every item keeps **anchors**: files, symbols, areas, screens, contracts, decisions, dependencies and the base commit it was validated against. A change is crossed with all anchors without AI; only the candidates go to a cheap batch triage with fixed verdicts (`still_valid`, `needs_update`, `maybe_done`, `obsolete`, `duplicate`, `new_dependency`, `conflict`). No item starts without being valid against the current main.
- **Short ids** are the same everywhere (`G-3`, `I-142`, `P-77`, `D-12`, `A-cards`, `M-301`), including a commit trailer.
- Knowledge is found in layers, cheapest first: exact ids, paths and symbols; filters; full-text search (FTS5). One call (`barnraise context I-142`) returns the item's context pack in a fixed order, cut to a token budget.

## Consequences

- Revalidation is cheap enough to run on every integration and in a nightly sweep, within a cost cap (`budget.triage_week`).
- Vector search and rerankers stay off unless they beat graph plus text on the project's own history (`barnraise eval`).
