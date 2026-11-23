# ADR-0018: New code from scratch, migrated side by side

- Status: accepted
- Date: 2026-11-23

## Context

The current system delivers every day. Bash and Python scripts with long prompts cannot be refactored into a tested, hexagonal product, and copying code from other projects would bring their assumptions and licenses.

## Decision

Barnraise is **new code, written from scratch**, hexagonal and tested. From the current system and the projects studied come only ideas, never code. It enters gradually, starting with the hooks, and each new part runs next to the old one until it proves better; nothing stops working.

## Consequences

- Each phase has an exit criterion; the old part is turned off only after the new one proves itself.
- Changes to Barnraise's own policy, hooks, permissions and arbiter need the owner's approval, even when Barnraise drives its own development.
