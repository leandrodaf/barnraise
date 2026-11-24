# Architecture decision records

One short record per decision: context, decision, consequences. A decision that changes gets a new record that supersedes the old one.

| ADR | Decision |
| --- | --- |
| [ADR-0001](0001-go-single-binary.md) | Go, one binary |
| [ADR-0002](0002-hexagonal-architecture.md) | Hexagonal architecture, enforced by depguard |
| [ADR-0003](0003-event-sourcing-sqlite-nats.md) | Event sourcing in SQLite, mirrored over NATS JetStream |
| [ADR-0004](0004-git-as-arbiter.md) | Git as the arbiter |
| [ADR-0005](0005-connectrpc-from-protobuf.md) | ConnectRPC from protobuf, with a JSON face |
| [ADR-0006](0006-providers-by-capability.md) | Providers by capability, never the lowest common denominator |
| [ADR-0007](0007-tailscale-cli-and-cpace-pairing.md) | Tailscale through its CLI, pairing by CPace over TCP |
| [ADR-0008](0008-pluggable-tracker.md) | Pluggable tracker, local by default |
| [ADR-0009](0009-item-files.md) | Item files: plan.md in the branch, criteria.json never committed |
| [ADR-0010](0010-fake-provider-for-e2e.md) | A scripted fake provider for end-to-end tests |
| [ADR-0011](0011-name-barnraise.md) | Name: Barnraise |
| [ADR-0012](0012-ai-tools-through-their-clis.md) | AI tools through their CLIs, with subscriptions |
| [ADR-0013](0013-daemon-is-the-only-authority.md) | The daemon is the only authority; every interface is a client |
| [ADR-0014](0014-generic-core-project-yaml.md) | Generic core, project specifics in barnraise.yaml |
| [ADR-0015](0015-language-and-glossary.md) | English inside, the reader's language outside, one word per concept |
| [ADR-0016](0016-goal-focus-capacity.md) | A goal with an end, a temporary focus, and split capacity |
| [ADR-0017](0017-item-size-and-tweaks.md) | Items of 2 to 6 hours; tweaks ride along |
| [ADR-0018](0018-rewrite-and-migrate-side-by-side.md) | New code from scratch, migrated side by side |
| [ADR-0019](0019-machines-all-equal.md) | All machines equal, none central |
| [ADR-0020](0020-cloudevents-and-signed-webhooks.md) | CloudEvents out, signed webhooks in and out |
| [ADR-0021](0021-branch-and-draft-pr-per-item.md) | A branch and a draft pull request per item from the start |
| [ADR-0022](0022-living-plan-anchors-short-ids.md) | Living plan: anchors, short ids and layered search |
| [ADR-0023](0023-security-by-os-layers.md) | Security by OS layers, not by prompt |
| [ADR-0024](0024-license-apache-2.md) | License: Apache-2.0 |
| [ADR-0025](0025-embedded-tailscale-behind-a-build-tag.md) | Embedded Tailscale (tsnet) behind a build tag |
| [ADR-0033](0033-recorded-resolutions-and-structural-merge.md) | Recorded conflict resolutions, and a structural merge driver when installed |
| [ADR-0044](0044-generic-acp-provider.md) | A generic provider for agents that speak the Agent Client Protocol |
| [ADR-0036](0036-output-filters-and-recoverable-compression.md) | Output filters, recoverable compression and short answers |
| [ADR-0042](0042-stale-read-warning.md) | Warn an agent when main changes what it read |
| [ADR-0043](0043-lessons-from-rework.md) | Lessons from rework |
| [ADR-0029](0029-bound-session-tokens-and-chained-event-log.md) | Session tokens bound to their session, and a hash-chained event log |
| [ADR-0030](0030-provenance-of-hooks-and-instruction-files.md) | Provenance of hooks and instruction files |
| [ADR-0026](0026-egress-key-broker-and-local-ca.md) | Egress key broker and a local CA |
| [ADR-0027](0027-egress-inspection-and-signed-receipts.md) | Outbound inspection and signed receipts |
| [ADR-0028](0028-egress-rules-by-method-host-path.md) | Egress rules by method, host and path |
