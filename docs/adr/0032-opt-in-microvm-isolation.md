# ADR-0032: Opt-in microVM isolation through an external runner

- Status: accepted
- Date: 2026-11-24
- Amends: [ADR-0023](0023-security-by-os-layers.md)

## Context

Landlock ([ADR-0031](0031-landlock-and-sandbox-profiles.md)), bubblewrap and containers all share the machine's kernel: an agent that finds a kernel bug escapes them. A microVM has its own kernel behind a hypervisor, boots in well under a second and needs no daemon. Not every owner wants that cost (an image with the toolchain, KVM, memory per VM), so by the owner's rule it is opt-in, through a tool used when installed.

The idea of a microVM per worktree comes from [smolvm](https://github.com/smol-machines/smolvm) (Apache-2.0), whose CLI is the default runner. Only the idea and the documented command line were used; no code.

## Decision

- `internal/adapters/out/microvm` implements `ports.Confiner` by running an external runner: `smolvm` by default, or any program through `isolation.microvm.command`, a template with the placeholders `{runner}`, `{image}`, `{cpus}`, `{memory}`, `{volumes}`, `{network}`, `{env}`, `{workdir}` and `{command}`. Barnraise neither ships nor downloads the runner.
- `daemon.yaml` turns it on (`isolation.backend: microvm`, off by default) for the kinds of command in `isolation.microvm.for` (`sessions`, `checks`, `setup`; all by default), with the image, size and extra mounts. It is the machine's choice, not the repository's, so no agent can turn it off.
- Support is detected once: the runner on the `PATH`, `/dev/kvm` on Linux, an image when the template uses one. When something is missing the daemon logs it once, `barnraise sandbox status` shows why, and Landlock is used instead: the command is never left unconfined because the VM is missing.
- The VM gets the command's worktree and the repository's `.git` as volumes, no network for checks (`NetLocal`: loopback inside the VM is its own), `--allow-host` for the policy's `allow_hosts` (plus the providers' API hosts for sessions), and the variables by name. The profile's command rules are checked before the VM starts. Claude Code's bubblewrap sandbox and Codex's own sandbox are off inside the VM: the VM is the boundary.

## Consequences

- An owner can put checks (and setup, and sessions) behind a hypervisor with one block in `daemon.yaml`; nothing changes for anyone else.
- Not verified against a real smolvm: the adapter was built from its documentation and tested with a fake runner. `--env` and `--version` are not documented in the pages read, and the daemon's UNIX socket (hooks, MCP) is probably not reachable from inside a VM, so sessions in a VM likely cannot talk to the daemon yet; checks and setup do not need it. The command template is the escape hatch for the runner's real flags.
- The image must carry the project's toolchain (and the AI CLIs for sessions); package caches of the host are not shared unless mounted.
