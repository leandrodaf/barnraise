# ADR-0025: Embedded Tailscale behind a build tag

- Status: accepted
- Date: 2026-11-23
- Amends: [ADR-0007](0007-tailscale-cli-and-cpace-pairing.md)

## Context

ADR-0007 drove the installed `tailscale` command to keep the binary lean. The design asks for more: on a new machine only `barnraise` is installed, and `barnraise machine join 482913` joins the network and the cluster at once, with a self-hosted control server (Headscale) possible. That needs Barnraise itself on the tailnet, which is what `tailscale.com/tsnet` does, at the cost of about 13 MB of binary (33 MB to 46 MB on linux/amd64).

## Decision

- `internal/adapters/out/tailnet` has one `Tailnet` interface (status, listen, dial, close) with two implementations: `Network` (the installed command, as before) and `Embedded` (tsnet), which lives in files built only with the `tsnet` build tag. Without the tag a stub reports `ErrNotBuiltIn`.
- `make build` stays without the tag; the release (`.goreleaser.yaml`), `make build-tsnet` and `make release-cross` carry it. CI builds, vets, lints and runs the embedded test tailnet with the tag as well.
- `network:` in `daemon.yaml` chooses `tailscale`, `tsnet` or `lan`; empty picks the installed command, then the embedded one when it can log in without a person (an auth key, or a state from an earlier login), then `lan`.
- The embedded device is named after the machine, advertises `tag:barnraise`, keeps its state in `<data dir>/tsnet` (0700, with a lock so one process runs it), takes its control URL from `tsnet.control_url` and its auth key from `BARNRAISE_TS_AUTHKEY` or the keychain, never from a file or a command line. Log upload to Tailscale is off.
- Tailnet addresses exist only inside the process that holds the device, so the daemon serves NATS on the device (connections are handed to the server in process, with the same nkey authentication), dials peers through it, and relays the device's pairing port to a `machine invite` running on loopback. A `machine join` while the daemon holds the device uses a short-lived ephemeral device.
- Pairing stays CPace over TCP (ADR-0007). `machine join` without `--addr` tries the online `tag:barnraise` machines of the tailnet before LAN beacons.

## Consequences

- Release binaries need nothing else installed; source builds stay light unless asked.
- Two code paths for the tailnet: the shared interface and the embedded test tailnet (Tailscale's test control server on loopback) keep both honest.
- In `tsnet` mode NATS is reachable only through the tailnet, so a peer paired over the local network is not reachable from it.
