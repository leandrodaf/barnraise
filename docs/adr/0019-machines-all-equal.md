# ADR-0019: All machines equal, none central

- Status: accepted
- Date: 2026-11-23

## Context

A central machine is a single point of failure, and the case of a machine stuck on old code for a week came from local state blocking updates.

## Decision

Every machine runs the same binary and none is central. Each machine pulls its own next item; there is no central scheduler. Each declares what it has (CPUs, memory, emulator slots, GPU, logged-in accounts) and only takes work it can run. Only claims, leases and integration go through the git arbiter (ADR-0004); state is mirrored (ADR-0003). A machine announces when it leaves and gives its items back (`barnraise machine leave`). Machines converge on the newest version found in the cluster: each daemon publishes its binary in its object store, and a daemon that sees a peer on the same platform with a newer version fetches it by hash, verifies it, replaces its executable and restarts, only while no agent session runs (`BARNRAISE_NO_SELF_UPDATE=1` opts out).

## Consequences

- Works with 1, 2 or more machines; if one disappears, another takes its items after the lease ttl and resumes from the last pushed step.
- A session does not migrate in the middle of an answer; the item does, because every step resumes from files and the branch.
