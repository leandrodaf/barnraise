# Security policy

Barnraise runs AI agents against real repositories, so security reports are welcome and handled first.

## Reporting a vulnerability

Please **do not open a public issue**. Report privately through GitHub's [private vulnerability reporting](https://github.com/leandrodaf/barnraise/security/advisories/new) for this repository.

Include what you found, how to reproduce it, the version (`barnraise version`) and the impact you expect. You will get an answer within 7 days. Fixes are released as soon as they are ready, with a GitHub security advisory; reporters are credited unless they prefer otherwise.

## Supported versions

Until 1.0, only the latest release gets security fixes.

## Scope

In scope, among others:

- a way for an agent session to get around the PreToolUse policy, the isolated user, the sandbox or the egress proxy as documented in [docs/security.md](docs/security.md);
- a way for a token or an agent to act beyond its scopes, or for an unauthenticated caller to reach the API;
- forged or replayed webhooks that are accepted;
- a machine of the cluster acting beyond its NATS permissions, or breaking the git arbiter's guarantees;
- secrets leaking into logs, events, commits or the repository.

Out of scope: weaknesses of the AI providers themselves, and attacks that need root or the owner's own user on the machine (the encrypted file store does not protect against those, by design).

## Verifying releases

Release checksums are signed keylessly with cosign from the release workflow. See [docs/operations.md](docs/operations.md#install).
