# ADR-0028: Egress rules by method, host and path

- Status: accepted
- Date: 2026-11-24
- Builds on: [ADR-0026](0026-egress-key-broker-and-local-ca.md)

## Context

`policy.allow_hosts` allowed whole hosts. GitHub must be allowed for agents to fetch dependencies and push their branch, but a whole-host allow also lets an injected agent push to an attacker's repository, open a gist or file an issue elsewhere with a token it was handed.

The idea of allow rules that name a method and a path, not only a host, comes from [multikernel/sandlock](https://github.com/multikernel/sandlock) (Apache-2.0). Only the idea was taken; no code.

## Decision

- An `allow_hosts` entry is a plain host (as before, also its subdomains) or a rule `METHODS host/path` (`GET github.com/owner/repo/**`, `POST,PUT api.github.com/repos/{project-repo}/**`). In paths `*` is one segment and `**` any number; `{project-repo}` is the project's `owner/repo`. Parsing and matching live in `internal/domain` (`ParseAllowEntry`, `CompileEgress`, `EgressRules.Allow`).
- For a request the proxy sees, the rules naming its host and method decide: one must match the path. A method no rule of that host names falls back to the plain hosts. A host named only by rules allows only what they say.
- A host with rules is terminated by the proxy (ADR-0026), since a tunnel hides method and path. Without TLS termination, rules apply only to plain HTTP and tunnels are checked by host.
- Default `policy.egress.repo_scope: true`: when the project's code host is GitHub or GitLab (from `code_host` or the git remote), writes (`POST`, `PUT`, `PATCH`, `DELETE`) to the host and its API are only allowed toward the project's repository, while `git-upload-pack` (fetching any repository) and every read stay allowed. GraphQL stays allowed: its path does not name a repository. The rules are only added for a code host the allow list already allows; they narrow, never open.
- The machine has one proxy, so the rules of all its projects are merged. The Claude Code sandbox, which only knows hosts, gets the host of each entry.

## Consequences

- Pushing or writing through the REST API to a repository other than the project's is refused, with a `policy.violation` event and a receipt.
- Existing configurations keep working: plain hosts mean what they meant; `repo_scope: false` restores whole-host GitHub/GitLab writes.
- GraphQL mutations, LFS uploads and numeric GitLab project ids are not scoped by path; a project that needs more adds its own rules.
- With several projects on one machine, each project's agents may write to every project repository of that machine.
