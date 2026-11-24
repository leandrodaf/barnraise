# ADR-0027: Outbound inspection and signed receipts

- Status: accepted
- Date: 2026-11-24
- Builds on: [ADR-0026](0026-egress-key-broker-and-local-ca.md)

## Context

The egress proxy refused hosts off the allow list but let anything through to allowed ones, and kept only a counter of refusals. An agent could still post a secret it read to an allowed host, and nobody could later prove what the proxy had allowed or refused.

The idea of scanning agent egress for secrets and leaving a signed, verifiable record of each decision comes from [pipelab-org/pipelock](https://github.com/pipelab-org/pipelock) (Apache-2.0). Only the idea was taken; no code.

## Decision

- Every request the proxy can see (plain HTTP and the terminated hosts) is scanned with the built-in rules of `internal/adapters/out/secretscan`: URL and query, headers except those carrying the caller's own credential for the host (`Authorization`, cookies, names with token/key/secret/session/auth), and the body up to 32 MiB (gzip decompressed). A hit refuses the request (403) and records a `policy.violation` event with the redacted match. Bodies to the key-based providers are not scanned: the conversation goes there by design. Larger bodies pass unscanned and their receipt says so.
- More hosts can be terminated and scanned with `policy.egress.inspect` (opt-in).
- Every decision (tunnel, host refusal, request allowed or refused and why) is a receipt: sequence, time, decision, host, method, URL path (never the query), rule, SHA-256 of the body seen, the SHA-256 of the previous line, and an ed25519 signature by a key of the daemon (`<data dir>/egress/receipts-key.pem`, 0600; public key `receipts.pub`). Receipts are appended to `receipts.jsonl`.
- `barnraise egress receipts verify` checks the signatures, the chain and the sequence (from receipt 1) using only the files; `barnraise egress receipts list` shows the latest.

## Consequences

- Secrets in outbound requests to the hosts the proxy sees are stopped, and the owner sees why.
- Changing, removing, reordering or forging a receipt is detected; removing lines from the end is not, by the log alone (copy `receipts.jsonl` elsewhere to keep a reference).
- One receipt per request to the providers: the log grows with use and is not rotated by the daemon.
- Exfiltration inside pure tunnels and inside credential headers is not seen; the host allow-list and the kernel rules remain the barrier there.
