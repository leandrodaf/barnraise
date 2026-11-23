# ADR-0017: Items of 2 to 6 hours; tweaks ride along

- Status: accepted
- Date: 2026-11-23

## Context

Every item pays a fixed cost: opening, checks, reviews, integration. Many tiny items made that cost almost everything; grouping sped things up.

## Decision

- The unit is something deliverable and visible: a screen, a flow, a complete endpoint with tests. Target size is **2 to 6 agent hours** (`items.target_size`); the planner merges or splits to reach it.
- Anything smaller is a **tweak** of its area. When an agent takes an item of an area, the area's pending tweaks attach to it and share its checks; 5 pending tweaks become an item.
- Review findings in the same area are fixed in the same round as rework; only findings in other areas become proposals.

## Consequences

- Fixed cost is paid once; Barnraise warns when the fixed cost of an area's items exceeds implementation time.
