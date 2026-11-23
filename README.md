# Barnraise

Barnraise is a single Go binary that coordinates AI coding agents (Claude Code, Codex and others) to drive a software project from start to finish, spending little and staying safe. It does not replace the AI tools: it uses the best of each one through their own CLIs and subscriptions, and takes care of everything around them: the queue, the flow of each item, checks, reviews, the merge queue, accounts and limits, several machines, and security.

*Barnraise* comes from "barn raising", the American community effort where neighbours build a barn together in a day.

> Status: 0.1.0, unreleased. See [CHANGELOG.md](CHANGELOG.md).
>
> Leia em português: [docs/README.pt-BR.md](docs/README.pt-BR.md).

## Principles

When two of them conflict, the lower number wins.

1. **Safe by construction.** The agent cannot reach what it does not need. The barrier is the operating system and the daemon, not the prompt.
2. **The goal ends.** Every goal has a measurable end. Work that does not bring the end closer waits its turn.
3. **The flow is code.** Barnraise decides the next step, rework and integration. The model decides the content of the work.
4. **Use the best of each tool.** Never level down to what all CLIs share. Native capability first; emulation only when it is missing.
5. **Tokens are money.** Each step gets only the context it needs. Looking at images, reading logs and exploring code go to cheap models.
6. **Fixed shapes between roles.** Deliveries, reviews and proposals have validated fields. Loose text does not move an item.
7. **One word per concept.** The same term in the CLI, the YAML, the panel, the docs and the messages.
8. **Generic core, specific project.** The core does not know what Kotlin, Maestro or a blog is.
9. **Configuring is a conversation.** Nobody needs to edit YAML, `settings.json`, environment variables or systemd by hand.
10. **Everything is extensible.** Providers, trackers, checks, step types and notifications are plugins. The CLI, the MCP server and the panel use the same API and events as any other client.
11. **Measure before changing.** Every self-improvement starts from a number, not a guess.
12. **Migrate without stopping.** Each new part runs next to the old one until it proves better.

## Quick start

Requirements: Linux or macOS, `git`, and at least one AI CLI you are logged in to (`claude` or `codex`). Tailscale and `bwrap` (bubblewrap) are used when present.

```sh
# install (Linux and macOS, amd64 and arm64)
curl -fsSL https://raw.githubusercontent.com/leandrodaf/barnraise/main/install.sh | sh

cd your-repo
barnraise init                 # connect accounts, talk about the project, write barnraise.yaml
barnraise up                   # register the project and start the daemon of this machine
barnraise account add claude   # add (another) Claude Code subscription account to the rotation

barnraise item add "Sign-in screen" \
  -c "WHEN the user taps Sign in with valid credentials, THE SYSTEM SHALL show the home screen | main" \
  --area auth --size 4h

barnraise status               # goal, focus, queue, agents, accounts and machines in one look
```

Useful next steps:

| Command | What it does |
| --- | --- |
| `barnraise goal set "<name>" --done-when "<end>"` | Set the long-term goal with a measurable end |
| `barnraise focus <area> --until done` | Point most of the capacity at one area for a while (`--preview` shows the effect first) |
| `barnraise approve I-7 --plan` | Approve the plan of an item (say what is approved: `--plan` or `--delivery`) |
| `barnraise questions` / `barnraise answer` | Questions agents asked the owner, and their answers |
| `barnraise events -f` | Follow every event live |
| `barnraise chat` | Talk to Barnraise in plain words: focus, pause, approve, status |
| `barnraise pause` / `barnraise resume` | Emergency button for every agent |
| `barnraise explain` | The configuration in effect, defaults filled in |

Every command prints human text by default and JSON with `--json`. Run `barnraise <command> --help` for the flags.

## Concepts

Each concept has one English name and one fixed translation per language. An automated test (`internal/archtest`) fails when the CLI, prompts, panel or docs use a word outside this vocabulary.

| Barnraise (en) | pt-BR | What it is | Command or field |
| --- | --- | --- | --- |
| Project | Projeto | A repository Barnraise drives, with its `barnraise.yaml` | `barnraise init` |
| Goal | Objetivo | The long-term result, with a measurable end | `goal:`, `barnraise goal set` |
| Focus | Foco | A temporary priority inside the goal, with a deadline or "until done" | `barnraise focus` |
| Item | Item | One deliverable unit of work, 2 to 6 hours of agent time | `barnraise item` |
| Criterion | Critério | An acceptance condition in EARS, tied to a check | `-c` of `item add` |
| Proposal | Proposta | A suggested item, waiting for approval | `barnraise propose` |
| Tweak | Ajuste rápido | A small fix that rides with the next item of the same area | `barnraise tweak add`, `items.tweaks:` |
| Area | Área | A screen, module or folder; groups tweaks and proposals | `areas:` |
| Flow | Fluxo | The sequence of steps of an item kind | `flows:` |
| Step | Etapa | One part of a flow: plan, implement, check, review, integrate | `steps:` |
| Check | Verificação | An automatic verification: build, tests, screenshots | `checks:` |
| Review | Revisão | An evaluation by an agent or a person, with two verdicts | `barnraise review` |
| Rework | Retrabalho | A return to an earlier step, with a limit | `rework:` |
| Delivery | Entrega | What the agent hands back at the end of a step, with a fixed status | `barnraise deliver` |
| Role | Papel | A function: implementer, reviewer, analyst, designer | `roles:` |
| Agent | Agente | A role running on an account, on a machine | `barnraise agents` |
| Account | Conta | A login at a provider | `barnraise account add` |
| Provider | Provedor | Claude Code, Codex, … | `models:`, `roles.<role>.providers` |
| Machine | Máquina | A computer of the cluster; all are equal, none is central | `barnraise machine invite` |
| Tracker | Rastreador | Where people see items: local, GitHub, GitLab, Jira, Linear | `tracker:` |
| Template | Modelo de projeto | Ready roles, flows and checks for a kind of project | `template:` |
| Owner | Dono | The person who approves and decides | `approves: owner` |
| Merge queue | Fila de integração | Where approved items wait to land on main | `barnraise queue` |
| Anchor | Âncora | What an item assumes: files, symbols, screens, contracts, decisions, dependencies and the base commit | `--files`, `--symbols` of `item add` |
| Revalidation | Revalidação | Checking whether an item still holds after something changed | `barnraise revalidate` |

"Issue" only appears when talking about the thing inside GitHub or GitLab: "item I-42 shows up as issue #1608 on GitHub".

Short ids are the same everywhere (tracker, PR title, branch, commit trailer): `G-3` goal, `I-142` item, `P-77` proposal, `D-12` decision, `A-cards` area, `M-301` memory. Every commit carries `Barnraise-Item: I-142`, so `git log --grep I-142` finds all the code of an item.

## Architecture

Barnraise is one binary with several modes that share the same core. **The daemon is the only authority**: only it writes state, touches main and decides transitions. The CLI, the MCP server, the provider hooks, the web panel and any external system are clients of one public API and follow the same event stream.

```
 owner ─┐                                   ┌─ providers (claude, codex CLIs)
 agents ┼─ CLI · MCP · hooks · panel ─ API ─┤  daemon ├─ trackers (local, GitHub, GitLab, Jira, Linear)
 CI/bots┘   (unix socket or HTTPS)          └─ git arbiter · NATS JetStream · SQLite
```

| Mode | For whom | What it does |
| --- | --- | --- |
| `barnraise daemon` | The system | Schedules, runs flows, keeps state, talks to providers, trackers and other machines |
| `barnraise …` (CLI) | Owner, agents through Bash, hooks | Short commands that ask the daemon; human output or `--json` |
| `barnraise mcp` | Agents and the owner's Claude, on any device | Typed tools with permission by role |
| Web panel | Owner and guests | Served by the daemon at `/`: queue, agents, accounts, goal and focus, what waits for the owner |
| `barnraise hook …` | Provider hooks | `pre-tool` decides locally, without network, from a compiled policy file |

The code is hexagonal (ports and adapters). The dependency rule is enforced by `depguard` in [`.golangci.yml`](.golangci.yml):

```
cmd/barnraise/            composition root: wires adapters to ports, one binary
internal/domain/          pure rules: item, goal, focus, flow, rework, capacity, policy (stdlib only)
internal/app/             use cases: claim, deliver, review, integrate, propose, set focus… (domain + ports)
internal/ports/           small interfaces: Provider, Tracker, CodeHost, Arbiter, EventBus, StateStore, …
internal/adapters/in/     httpapi (Connect), mcp, cli, hooks, panel
internal/adapters/out/    claude, codex, github, gitlab, jira, linear, nats, sqlite, git, tailnet, pairing,
                          keychain, webhook, notify, secretscan, checks, fakeprovider, …
proto/                    the API contract (protobuf), linted and checked for breaking changes with buf
gen/                      generated Go, Connect and OpenAPI code
templates/                base defaults and the mobile-android, backend, web, content and blank templates
docs/adr/                 architecture decision records
```

State is an append-only event log in SQLite; the current state is derived from the events, which gives full history and lets any step be redone. The events are mirrored between machines over embedded NATS JetStream. See [docs/adr](docs/adr) for the reasoning behind each decision.

Per project, the repository holds only configuration: `barnraise.yaml`, `.barnraise/roles/*.md`, `.barnraise/standards/*.md`, `.barnraise/constitution.md` and the rest of the `.barnraise/` scaffold. Never state, never secrets.

## Several machines

Barnraise works with one, two or fifty machines and never needs a third one to vote. Every machine runs the same binary and none is central.

- **Git is the arbiter.** Taking an item creates `refs/barnraise/claims/<item>` with an atomic compare-and-swap (`git push --force-with-lease` on the remote, `git update-ref` with the expected old value when there is no remote). The first machine wins; the other gets the refusal at once. The claim is renewed every 30 s; a claim not renewed for 2 min (measured on each machine's local clock) can be taken by another machine, which resumes from the last committed step. Global housekeeping goes to whoever holds `refs/barnraise/lease/housekeeping`. Main only moves by fast-forward.
- **State everywhere.** Each machine has its own JetStream; events are mirrored between machines whenever they can see each other, and each machine keeps working alone when isolated.
- **Pairing with a 6-digit code.** `barnraise machine invite` shows a code; on the other machine, `barnraise machine join 482913` (add `--addr host:47900` when LAN discovery cannot find the inviter). The two sides run CPace, a password-authenticated key exchange, over TCP with the code as the password, then exchange their NATS nkeys sealed with ChaCha20-Poly1305. A wrong code fails cleanly; the inviter allows three failed attempts and the code expires after 10 minutes.
- **Tailscale recommended.** When the `tailscale` CLI is installed and running, NATS listens on the machine's Tailscale address. `barnraise cluster acl` prints the recommended ACL: only `tag:barnraise` reaches the Barnraise ports. Without Tailscale, the same pairing works on a LAN or any VPN.
- **Leaving is easy.** `barnraise machine leave` gives this machine's items back to the queue first. `barnraise cluster status` shows each machine in green, yellow or red.

## Security

The agents read untrusted text, could reach private data and could send data out. Barnraise breaks all three, in layers. Details in [docs/security.md](docs/security.md).

1. **Secrets out of the agents' reach.** Secrets live in the system keychain (or an encrypted file store on headless machines), never in YAML or in the repository.
2. **One Linux user per agent account** with `barnraise account add --isolate`, without access to the owner's home.
3. **A blocking PreToolUse hook** (`barnraise hook pre-tool`) that works even with `--dangerously-skip-permissions`: no writes to workflows, `.barnraise/`, keystores, `.env*` or existing tests; no `git push` to main, `gh secret`, `curl | sh`, `sudo`; no reads of `~/.ssh`, `~/.config/gh` or provider credentials.
4. **Native sandbox.** Claude Code's sandbox (bubblewrap on Linux) is enabled with `failIfUnavailable` whenever `bwrap` really works on the machine; Codex runs with `--sandbox workspace-write`, never in bypass mode.
5. **Untrusted text is cleaned and fenced.** HTML comments and invisible characters are removed from issues and comments, and the agent sees external text inside explicit fences.
6. **Permission by role in the daemon.** An implementer has no `approve` scope and never sees the `approve` tool. Only the daemon integrates into main.
7. **Secret scan before every push**: a commit whose diff or files hold a secret is blocked and the owner is alerted.
8. **Scoped API tokens, audit log, HMAC-signed webhooks, nkeys between machines.**

## API

Everything the CLI, the MCP server and the panel do goes through one public API. Details in [docs/api.md](docs/api.md).

- **Connect RPC from protobuf** ([proto/barnraise/v1/barnraise.proto](proto/barnraise/v1/barnraise.proto)): the same service answers gRPC, gRPC-Web and plain HTTP with JSON. Call it with `curl`:

  ```sh
  curl -s --unix-socket ~/.local/share/barnraise/barnraise.sock \
    -X POST http://localhost/barnraise.v1.BarnraiseService/Status \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $(cat ~/.config/barnraise/owner.token)" \
    -d '{"project":"demo"}'
  ```

- **OpenAPI** generated from the proto, served at `GET /v1/openapi.yaml`.
- **Live events** as CloudEvents over Server-Sent Events at `GET /v1/events` (filters `project`, `types`, `after_seq`).
- **Outgoing webhooks** signed with HMAC-SHA256 and retried; **inbound webhooks** at `/v1/webhooks/github`, `/v1/webhooks/gitlab` and `/v1/webhooks/inbound/{project}`, all verified.
- **Scoped tokens** per project and action (`items:read`, `checks:report`, `approve`, …), with lifetime and revocation: `barnraise token create -p demo --name ci --scope checks:report --ttl 90d`.
- **Audit log** of who did what, with which token, when: `barnraise audit`.

By default the API listens only on the local unix socket and on `127.0.0.1:7420`. Listening anywhere else requires `--tls-cert` and `--tls-key`.

For GitHub Actions checks, the daemon dispatches the workflow with the inputs `item`, `commit` and `check`, and the workflow reports back with the [check action](action/check).

## Configuration

`barnraise init` writes a short `barnraise.yaml` that keeps only what differs from its template; `barnraise explain` shows everything in effect, and `barnraise check` finds mistakes in plain sentences.

```yaml
template: mobile-android
language: pt-BR
goal: Personal finance app, Android first, matching docs/prototipo-mobile
approves: owner
tracker:
  type: github
  repo: leandrodaf/financas-pluggy
  project: 3
checks:
  main: scripts/verificar.py
```

| Key | Default | Purpose |
| --- | --- | --- |
| `template` | `blank` | Base roles, flows and checks (`mobile-android`, `backend`, `web`, `content`, `blank`, or one saved with `template save`) |
| `language` | `en` | Language of what goes to people (items, comments, questions); code and prompts stay English |
| `goal` | none | A string or `{name, done_when, inventory, budget}` |
| `approves` | `owner` | Who approves new features: `owner` or `analyst` |
| `tracker` | `local` | `local`, `github`, `gitlab`, `jira`, `linear` |
| `checks` | from template | Commands or `{run, runs_on, workflow, url, scope, timeout, flaky_retries, areas}` |
| `roles`, `flows`, `kinds` | from template | Who works, which steps each item kind follows, and the rework rules |
| `capacity` | `goal: 80%, bugs: 15%, improvements: 5%` | How capacity is split between lanes |
| `proposals`, `items`, `self_improvement` | see docs | Limits that keep the queue small and the items the right size |
| `models`, `policy`, `areas`, `merge`, `lease`, `budget` | see docs | Model routing, security rules, areas, merge queue, claim lease, token budgets |
| `notifications`, `webhooks`, `experiments`, `worktree` | none | Owner notifications, outgoing webhooks, gradual rollouts, worktree files and ports |

The full reference with every key and default is [docs/configuration.md](docs/configuration.md). Operations (systemd, container image, headless servers, data locations, backups, upgrades) are in [docs/operations.md](docs/operations.md).

## Development

Requirements: Go 1.26, `git`, and for the contract `buf` with `protoc-gen-go`, `protoc-gen-connect-go` and `protoc-gen-connect-openapi` on `PATH`.

| Target | What it runs |
| --- | --- |
| `make build` | `go build` of `./cmd/barnraise` into `./barnraise`, with the version from `git describe` |
| `make test` | Fast tests: `go test -short ./...` |
| `make test-race` | The full suite with the race detector, including end-to-end and cluster tests |
| `make cover` | Coverage profile; fails when `internal/domain` or `internal/app` is below 90% |
| `make lint` | `golangci-lint run` (includes the depguard architecture rules) |
| `make vuln` | `govulncheck ./...` |
| `make proto` | `buf lint` and `buf generate` (Go, Connect and OpenAPI into `gen/`) |
| `make proto-breaking` | `buf breaking` against `main` |
| `make e2e` | End-to-end and cluster tests only |
| `make fuzz-smoke` | Each fuzz target for a short time (`FUZZTIME`, default 10s) |
| `make docker` | Build the container image |

Tests:

- **Domain and use cases**: table tests and property tests with `rapid` (for example "never two holders of an item", "rework never passes its limit").
- **Adapters**: real git (a local bare repository as remote), real embedded NATS and SQLite, recorded responses for external APIs and recorded `stream-json` sessions for the providers.
- **End to end** (`cmd/barnraise/e2e_test.go`): from `init` to an integrated item on a sample repository, with the scripted fake provider (`barnraise daemon --fake-provider`). Needs `git`.
- **Cluster** (`cmd/barnraise/cluster_test.go`): two machines, failover when one disappears.
- **Fuzzing** of external input: policy decisions, sanitizing of untrusted text, durations.
- **Vocabulary**: `internal/archtest` fails on words outside the glossary.

Contributions: see [CONTRIBUTING.md](CONTRIBUTING.md). Security reports: see [SECURITY.md](SECURITY.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
