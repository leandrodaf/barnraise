# ADR-0031: Landlock confinement and built-in sandbox profiles

- Status: accepted
- Date: 2026-11-24
- Amends: [ADR-0023](0023-security-by-os-layers.md)

## Context

Barnraise relied on the providers' own sandboxes, and on Linux both use bubblewrap, which needs unprivileged user namespaces. Ubuntu 23.10 and later forbid those through AppArmor (`kernel.apparmor_restrict_unprivileged_userns=1`). On such a machine Codex got no work at all (every one of its commands would fail), Claude Code ran with its sandbox off, and the worktree setup and the checks, which run code the agent wrote, always ran unconfined, on every machine.

Landlock is a Linux security module (5.13 and later, network rules from ABI v4 in 6.7) that an unprivileged process applies to itself and passes on to every process it starts. It needs no root and no namespace, so AppArmor's restriction does not stop it, and Ubuntu's kernels enable it.

The idea of ready-made profiles per kind of command, with command deny rules on top of the filesystem and network rules, comes from [fence](https://github.com/fencesandbox/fence) (Apache-2.0). Only the idea was taken; no code.

## Decision

- `internal/adapters/out/landlock` implements a new port, `ports.Confiner` (`Support`, `Wrap`), with [go-landlock](https://github.com/landlock-lsm/go-landlock) (MIT, already an indirect dependency; it brings `libcap/psx`). The daemon never confines itself: `Wrap` returns `barnraise sandbox-exec --spec <json> -- <command…>`, a hidden subcommand that matches the command against the profile's command rules, applies the rules to itself, and `execve`s the command. It fails closed: on any error it exits 126 and never runs the command unconfined. Other systems build a version that reports Landlock unavailable.
- The kernel's ABI version is probed once at start and reported in the daemon log, in `barnraise sandbox status` and as the `landlock` machine label; `barnraise status` alerts when a machine has neither bubblewrap nor Landlock.
- Filesystem rights are those of ABI v8 (not v9's restriction of UNIX socket paths, which would cut agents off from the daemon's socket); BestEffort keeps what the running kernel supports. Network rights are TCP connect only: binding a test server stays allowed and UDP (DNS) is untouched. Landlock only grants, so a denied path (`deny_read`, Barnraise's data, credentials inside the package stores) under a granted folder is carved out: the folder is replaced by its other entries, down to the denied path. The command's own work paths (worktree, `.git`, account) are granted even inside a denied folder.
- Built-in profiles live in the domain (`internal/domain/sandbox.go`): `setup` (default of `worktree.setup`; registries reachable), `build`, `check` (default of checks; the egress proxy and loopback services only), `agent`, `open` (filesystem only) and `none` (unconfined). Each adds command deny rules (wiping the filesystem or a disk and fork bombs everywhere; download-and-run and `sudo` outside setup; publishing from a check), merged with `policy.deny_commands` and `sandbox.deny_commands`. `checks.<name>.sandbox` and `worktree.sandbox` pick a profile; `sandbox.read`, `sandbox.write` and `sandbox.ports` add paths and ports.
- It is used for the worktree setup and every local check (including the runs on main for flaky comparison and checks moved to another machine), and, where bubblewrap cannot start, for the whole agent session: the provider adapters put the wrapper inside `sudo`, so it holds for an isolated account's user too; Codex then runs with its own sandbox in `danger-full-access`, as in a container. Codex is refused only on a machine with neither bubblewrap nor Landlock.
- Compatibility before strictness: without an egress proxy, the `check` and `agent` profiles keep ports 80 and 443 open (Landlock cannot tell a registry from any other host, and a check that downloads its modules used to pass); with `--egress` they reach the internet only through the proxy, whose address they get in `HTTPS_PROXY`. The `setup` profile does not refuse `curl … | sh`, which installers use.

## Consequences

- Machines where bubblewrap cannot start get Codex work again, and Claude sessions there are confined instead of open; setup and checks are confined on every Linux machine with Landlock.
- Landlock filters by port, not host or address: port 443 allowed is 443 anywhere, and on ABI v1 to v3 the network is not restricted. Host filtering remains the egress proxy's and nftables' job.
- A check or setup that writes or reads outside the usual places now fails with `Permission denied` until the path is listed in `sandbox:` or the command gets another profile; the profiles' lists of stores and toolchains will need to grow with real projects.
- The command rules see only the command line Barnraise starts (the check's or setup's own text), not what a script runs inside; inside an agent session the hook applies them to every command.
- Plugins are not confined: they are the owner's programs and talk to the daemon.
