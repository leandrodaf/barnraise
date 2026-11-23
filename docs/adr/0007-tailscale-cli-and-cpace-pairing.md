# ADR-0007: Tailscale through its CLI, pairing by CPace over TCP

- Status: accepted
- Date: 2026-11-23

## Context

Machines in different homes must reach each other without opening router ports, and joining a machine must be one command. The design suggested embedding Tailscale with `tsnet`, but `tsnet` pulls the whole `tailscale.com` module (hundreds of packages) into the build.

## Decision

- `internal/adapters/out/tailnet` drives the installed `tailscale` command (status, addresses, peers) instead of embedding `tsnet`. When Tailscale runs, NATS listens on the machine's Tailscale address; `barnraise cluster acl` prints an ACL where only `tag:barnraise` reaches the Barnraise ports. `BARNRAISE_NO_TAILSCALE=1` ignores it. The `Network` type keeps the shape an embedded implementation would have, so it can be swapped later without touching callers.
- Pairing (`internal/adapters/out/pairing`): `barnraise machine invite` shows a 6-digit code and listens on TCP (`:47900` by default, with UDP beacons for LAN discovery). `barnraise machine join <code>` dials it; both sides run **CPace** (a password-authenticated key exchange) with the code as the password, derive keys with HKDF, and exchange machine information (name, NATS URL, nkey public key) sealed with ChaCha20-Poly1305. A wrong code fails without revealing anything; three failures close the invite; codes expire after 10 minutes.

## Consequences

- The binary stays lean, and machines without Tailscale pair the same way on a LAN or any VPN.
- Tailscale must be installed separately where it is wanted.
- Each machine authorizes its peers' nkeys with narrow NATS permissions (read its streams, call its RPC subjects).
