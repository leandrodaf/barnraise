# ADR-0016: A goal with an end, a temporary focus, and split capacity

- Status: accepted
- Date: 2026-11-23

## Context

Agents create improvements all the time; in one case a week went by creating work before anything moved, until the owner asked for focus. A goal without an end ("clone the mockup") cannot be measured.

## Decision

- **Goal**: a long-term result with a measurable end (`done_when`), and optionally an inventory that lists its finite work once (`screens <dir>`, `files <path>`, `lines <file>`). Progress is counted, not felt.
- **Focus**: a temporary priority inside the goal, until a date or until done (`barnraise focus`, with `--preview` of the effect).
- **Capacity split** enforced by the scheduler: goal or focus 80%, bugs 15% (P0 first), approved improvements 5%, and one self-improvement item a week approved by the owner.
- **Proposals** have a fixed shape, a daily limit per agent (3), expire after 14 days, and duplicates become comments. With no authorized item, an agent records its state and ends its turn.

## Consequences

- The queue only holds what brings the goal closer; new ideas wait in a separate place.
- When the goal ends, Barnraise asks for the next one and meanwhile works only on bugs and approved improvements.
