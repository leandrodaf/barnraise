# ADR-0034: Built-in dependencies check against OSV, and ast-grep as a built-in check kind

- Status: accepted
- Date: 2026-11-24

## Context

Agents add and update dependencies freely, and a lockfile diff is hard to review by eye. A vulnerable version that slips in is found much later, if ever. Every project with dependencies wants this caught before integration, so by the owner's rule it is native, not a plugin. [OSV](https://osv.dev) is an open, ecosystem-wide database with a public API and a scanner, [osv-scanner](https://github.com/google/osv-scanner) (Google, Apache-2.0). The idea of checking each change's dependencies against OSV is taken from osv-scanner; none of its code is (ADR-0018): Barnraise speaks the documented OSV API and osv-scanner's JSON output.

Structural lint rules are wanted by some projects only. [ast-grep](https://github.com/ast-grep/ast-grep) (MIT) runs them from a project's `sgconfig.yml`; the idea of running its `scan` as a check is taken, not its code.

## Decision

- **Built-in check kinds.** `runs_on` accepts `builtin:dependencies` and `builtin:ast-grep`. The checks runner runs them in the daemon's process, with the daemon's network and tools, and keeps their report as the check's log. A failure that is not the change's (OSV unreachable, osv-scanner or ast-grep missing or broken, no `sgconfig.yml`) is reported red with `main_red`, so the item is held for the owner instead of sending an agent to fix the machine; nothing passes unverified.
- **The dependencies check is on by default.** Compiling `barnraise.yaml` adds a check named `dependencies` (unless the project has its own of that name, or `dependencies: off`), whose `paths` are the manifest and lockfile globs. It runs with the last check step of each flow, so once per round right before the merge queue, whatever single check that step names; a change that touches no manifest skips it with the reason. Its `paths` do not count as claiming a file for the selection rule "a file in no area runs every check", so a `go.mod` outside every area still runs everything, as before.
- **New findings only.** Each touched lockfile (a manifest that pins nothing is followed to its lockfile) is compared with its state at the merge base, which the check step now passes in `CheckSpec.Base`. Only added or changed packages are looked up, and a vulnerability the same package already had at the base does not count. Severity is the highest of the CVSS v3 base score (computed natively) and the advisory's stated rating across alias records; an unrated advisory counts as `dependencies.unknown` (high). `fail_at` (default high) and `ignore` are in the new `dependencies:` key.
- **osv-scanner when installed, the OSV API otherwise.** With osv-scanner on the `PATH` (`scanner: auto`), it runs on each lockfile now and on a copy of the base version, and the difference is kept. Without it, native parsers read Go, npm (package-lock, pnpm, yarn), PyPI (requirements, Pipfile.lock, poetry, uv), crates.io, RubyGems and Maven (gradle.lockfile, pom.xml) and the check calls `/v1/querybatch` and `/v1/vulns/{id}`. No new Go dependency.
- **The daemon reaches OSV, agents do not.** The agents' egress allow-list is unchanged; the daemon is not behind it.
- **ast-grep is opt-in.** A project declares a `builtin:ast-grep` check; it runs `ast-grep scan --json=stream` on the touched files in the check's folder, with `run` as extra arguments. Matches of `severity: error` fail and are given to the agent as `file:line:column rule [error]: message`; others are listed. `barnraise check` reports a missing binary or `sgconfig.yml`.
- `item.step.finished` now carries `extra.step` and `extra.outcome`, so hooks can tell which step ended (the item in it has already moved on).

## Consequences

- Existing projects get one more check, silent unless a manifest changes; it needs outbound HTTPS to `api.osv.dev` from the daemon, or `dependencies: off` / a mirror (`dependencies.api`) on offline machines.
- An outage of OSV holds items that change dependencies until the owner retries; this is chosen over passing them unverified.
- Version formats are those OSV expects (Go without the `v`, PyPI names normalized); lockfile formats Barnraise does not parse are covered only when osv-scanner is installed. Versions inherited from a Maven parent or BOM are not seen natively.
- The merge queue does not run built-in checks again (it reruns local checks only), as with other non-local checks.
