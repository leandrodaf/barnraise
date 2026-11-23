# ADR-0004: Git as the arbiter

- Status: accepted
- Date: 2026-11-23

## Context

A few decisions must be taken by exactly one machine: taking an item, holding a housekeeping lease, integrating into main. A consensus cluster needs three machines; most setups have one or two. Every project already has a git server, and atomic ref updates with an expected old value work as compare-and-swap on GitHub, GitLab and Gitea.

## Decision

- Leases live under `refs/barnraise/`: `refs/barnraise/claims/<item>` for items, `refs/barnraise/lease/housekeeping` for global housekeeping, `refs/barnraise/counters/<name>` for id counters.
- A lease ref points to a parentless commit with the empty tree whose message is JSON `{"holder", "renewed", "nonce"}`; the commit sha is the version used for compare-and-swap.
- With a remote, the swap is `git push --force-with-lease=<ref>:<old>`; without one, `git update-ref <ref> <new> <old>`, so the local disk is the arbiter.
- The holder renews every 30 s (`lease.renew`). Another machine may take over when it has **observed the same version unchanged for 2 min (`lease.ttl`) on its own local clock**. Timestamps written by other machines are never compared, so clock skew cannot steal or keep an item.
- Main only moves by fast-forward with an ancestry check; a machine that loses the race rebases and checks again.

## Consequences

- Never two holders of an item, with one, two or fifty machines and no third machine to vote.
- One write every 30 s per item in progress, far below git hosting limits; events and messages stay on NATS.
- A machine without internet finishes its current step but takes no item and does not integrate until it reaches the arbiter again.
- Agents are blocked by policy from pushing or updating `refs/barnraise`.
