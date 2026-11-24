# API

Barnraise is API first. Everything the CLI, the MCP server, the hooks and the panel do goes through one public API and one event stream, so any other interface, CI job or bot can do the same without touching the core.

| Surface | Where |
| --- | --- |
| RPC service (Connect: gRPC, gRPC-Web and JSON over HTTP) | `POST /barnraise.v1.BarnraiseService/<Method>` |
| OpenAPI document, generated from the proto | `GET /v1/openapi.yaml` |
| Live events (CloudEvents over Server-Sent Events) | `GET /v1/events` |
| Inbound webhooks | `POST /v1/webhooks/github`, `POST /v1/webhooks/gitlab`, `POST /v1/webhooks/inbound/{project}` |
| Event chain check (`admin`) | `GET /v1/events/verify[?from=N]`: the report of `barnraise events verify` |
| Trust of the files that steer agents (`admin`, never a session token) | `GET /v1/trust?project=&item=` (status), `POST /v1/trust?project=&item=` (re-trust, as `barnraise trust`) |
| Health (no token) | `GET /healthz` |
| Web panel | `GET /` |

The contract is [`proto/barnraise/v1/barnraise.proto`](../proto/barnraise/v1/barnraise.proto). `buf breaking` runs in CI, so incompatible changes only land in a new version (`barnraise.v2`).

## Where the daemon listens

- **Unix socket** (always): `~/.local/share/barnraise/barnraise.sock`, mode 0600. It moves with `BARNRAISE_DATA_DIR` or is set directly with `BARNRAISE_SOCKET`; when the path would be longer than a socket allows, it is `$XDG_RUNTIME_DIR/barnraise-<hash>.sock`.
- **TCP**: `127.0.0.1:7420` by default (`listen:` in `daemon.yaml`, or `barnraise daemon --listen`; `off` disables it). Any address that is not loopback requires `--tls-cert` and `--tls-key`. Prefer a reverse proxy or Tailscale in front of it.

The CLI uses the socket unless `--url` or `BARNRAISE_URL` points elsewhere.

## Authentication

Every RPC and `/v1/events` need `Authorization: Bearer <token>`. There are three kinds of token:

| Token | Prefix | Who | Scopes |
| --- | --- | --- | --- |
| Owner token | `bro_` | The owner; created on the first `barnraise daemon` in `~/.config/barnraise/owner.token` (0600). Agents cannot read it: the hook denies `~/.config/barnraise/**` | everything (`*`) |
| Agent session token | `brs_` | One per agent session, in memory, revoked when the session ends and expiring 5 minutes after the session's timeout. Bound to its session: only the procedures a session may call, only its project, and for item writes only its item; anything else is refused and recorded as `security.token.denied` ([ADR-0029](adr/0029-bound-session-tokens-and-chained-event-log.md)) | by role (below) |
| API token | `brt_` | Created with `barnraise token create`; shown once, stored hashed | the scopes you give, optionally for one project, with a lifetime |

Each token is rate limited (50 requests per second, bursts of 100); over the limit the API answers `429` with `Retry-After: 1`. Every write is in the audit log (`barnraise audit`, RPC `ListAudit`).

### Scopes

| Scope | Allows |
| --- | --- |
| `items:read` | Read items, status, queue, metrics, timeline, knowledge (`context`, `find`, `get`, `related`, `why`, `history`, `rules`), proposals; also the event stream |
| `items:write` | Create, edit and close items; revalidation |
| `items:claim` | Take the next item (`NextItem`) |
| `deliveries:write` | Submit a delivery |
| `reviews:write` | Submit a review |
| `checks:report` | Report a check result (`ReportCheck`) |
| `approve` | Approve or reject a plan or delivery; takeover, handback, redo |
| `focus` | Set or clear the focus; set the goal |
| `pause` | Pause and resume every agent |
| `proposals:write` | Propose |
| `proposals:decide` | Approve, refuse or postpone a proposal |
| `messages:write` | Send a message tied to an item |
| `owner:ask` | Ask the owner a question |
| `owner:answer` | Answer a question |
| `tweaks:write` | Add a tweak |
| `accounts:write` | Add or change accounts |
| `machines:write` | Machine operations |
| `usage:report` | Report account usage (the status line) |
| `journal:write` | Write the end-of-step journal |
| `events:read` | Read the event stream |
| `admin` | Projects, tokens, audit |

Agent roles get: `items:read`, `proposals:write`, `messages:write`, `events:read`, `journal:write`, `tweaks:write`, `items:claim`, plus `deliveries:write` and `owner:ask` for implementers, `reviews:write` for reviewers, and for the analyst also `approve`, `focus`, `pause`, `items:write`, `proposals:decide`.

```sh
# A CI job only reports check results, for one project, for 90 days.
barnraise token create -p demo --name github-ci --scope checks:report --ttl 90d
barnraise token list
barnraise token revoke tok-dn2lw57ghm87
```

## Calling the API with curl

The Connect protocol is plain HTTP: `POST` the request message as JSON to `/<package>.<Service>/<Method>`.

```sh
SOCK=~/.local/share/barnraise/barnraise.sock
TOKEN=$(cat ~/.config/barnraise/owner.token)

# Status of a project, over the unix socket
curl -s --unix-socket "$SOCK" \
  -X POST http://localhost/barnraise.v1.BarnraiseService/Status \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"project":"demo"}'

# The same over TCP
curl -s -X POST http://127.0.0.1:7420/barnraise.v1.BarnraiseService/Status \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -d '{"project":"demo"}'

# List items
curl -s --unix-socket "$SOCK" -X POST http://localhost/barnraise.v1.BarnraiseService/ListItems \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $TOKEN" \
  -d '{"project":"demo"}'

# Report a check result (what the check action does)
curl -s -X POST https://barnraise.example/barnraise.v1.BarnraiseService/ReportCheck \
  -H 'Content-Type: application/json' -H "Authorization: Bearer $CI_TOKEN" \
  -d '{"project":"demo","result":{"item_id":"I-42","check":"unit","passed":true,"summary":"212 tests passed","commit":"3f2c9e1"}}'
```

**Field names** are the proto field names in snake_case (`item_id`, `goal_id`, `done_when`), the same JSON the daemon uses in its events. Responses always use them; requests also accept the lowerCamelCase form (`itemId`). Empty fields and zero times are left out of responses. Unknown request fields are ignored, so a typo silently does nothing: check the OpenAPI document for exact shapes.

**Errors** from a method use the Connect error format: an HTTP status plus `{"code": "permission_denied", "message": "forbidden: token \"ci\" lacks items:read"}` (`invalid_argument`, `not_found`, `failed_precondition`, …). A missing or invalid token is refused before the method runs, with `401` and `{"error": "invalid or expired token"}`; the rate limit answers `429` the same way.

gRPC clients use the same service with HTTP/2; generated clients in Go are in `gen/barnraise/v1/barnraisev1connect`, and any language with a protobuf toolchain can generate its own from the proto.

### Methods

| Group | Methods |
| --- | --- |
| Projects | `ListProjects`, `RegisterProject`, `ReloadProject` |
| Items | `CreateItem`, `GetItem`, `ListItems`, `UpdateItem`, `CloseItem`, `NextItem`, `Deliver`, `SubmitReview`, `ReportCheck`, `Timeline`, `Estimate`, `Takeover`, `Handback`, `Redo` |
| Proposals and tweaks | `Propose`, `ListProposals`, `DecideProposal`, `AddTweak` |
| Owner | `AskOwner`, `ListQuestions`, `Answer`, `Approve`, `SetGoal`, `SetFocus`, `ClearFocus`, `Pause`, `Resume` |
| Views | `Status`, `Overview`, `Metrics`, `Digest`, `Queue`, `ProcessQueue`, `ListEvents` |
| Messaging | `SendMessage`, `Inbox`, `Journal`, `ReportViolation` |
| Knowledge | `Context`, `Find`, `Get`, `Related`, `Why`, `History`, `Rules`, `Revalidate`, `Sweep` |
| Accounts and machines | `UpsertAccount`, `ListAccounts`, `ReportUsage`, `ListMachines`, `MachineHistory`, `LeaveMachine` |
| Login of an account on several machines | `StartAccountLogin`, `GetAccountLogin`, `AnswerAccountLogin`, `CancelAccountLogin` (owner and admin tokens only; see [below](#login-on-several-machines)) |
| Tokens and audit | `CreateToken`, `ListTokens`, `RevokeToken`, `ListAudit` |
| Machine capabilities | `Capabilities`, `InstallTools` (owner and admin tokens only; see [below](#capabilities-and-tools)) |

## Overview

`Overview` is the whole cluster in one answer: what every item is doing, on which machine, with which worktrees, processes, pull request and cost. `barnraise overview` prints it (`--json` for the structure).

```sh
# Everything that is not finished, plus what finished in the last 7 days, of every project
curl -s --unix-socket ~/.local/share/barnraise/barnraise.sock \
  -X POST http://localhost/barnraise.v1.BarnraiseService/Overview \
  -H 'Content-Type: application/json' -d '{}'

# One item with everything about it; only what is on one machine; only what waits
... -d '{"project": "shop", "item": "I-42"}'
... -d '{"machine": "studio"}'
... -d '{"activity": ["waiting_owner", "blocked", "stalled"], "brief": true}'
```

| Request field | Meaning |
| --- | --- |
| `project` | One project; empty is every project the token may read |
| `item` | Only this item |
| `activity` | Only these activities or statuses |
| `machine` | Only items held by, running on or with a worktree on this machine |
| `finished` | How far back integrated and closed items are listed (`7d` by default) |
| `all` | Every finished item, whatever its age |
| `limit`, `offset` | Items per project: 500 by default, at most 5000. `items_matched` says how many there are, `truncated` that some were left out |
| `brief` | Totals only: no per-item breakdowns, no process lists |

The answer has three parts.

- **`projects[]`**: pause state, goal and progress, counts of every item by `status` and by `activity`, usage in total and split by model, role, account, machine, agent and day (last 30 days), and `items[]`.
- **`projects[].items[]`**: one entry per item. `activity` says what it is doing now: `working` (an agent session runs), `refining`, `running` (processes run for it and no agent: a check, a build), `idle` (a machine holds it and nothing runs), `stalled` (the machine that holds it is out), `waiting_owner`, `blocked`, `paused`, `parked`, `queued`, `ready`, `draft`, `integrated`, `closed`; `detail` says why in a sentence. Then where it is (`machine`, `claim`), `branch`, `pr` (number, URL and a state derived from the item: `draft`, `ready`, `merged`, `closed`), `tracker_ref`, times (`step_seconds`, `quiet_seconds`, `lead_seconds`), live `sessions[]` with their machine, start and tokens so far, `worktrees[]` on every machine, `procs`, `usage` with `usage_by_step`, `usage_by_model`, `usage_by_machine` and `usage_by_role`, the last result of each check, the last review and the last delivery.
- **`machines[]`**: each machine with its health and live readings, its sessions, the items it holds, every directory it keeps (`kind`: `root`, `item`, `alt`, `refine`, `scratch`, `repro`, `trash`; path, branch, commit, size, whether its setup is done), free disk, the processes that run for items and what was spent there.

**Usage** (`usage*`) counts `input`, `output`, `cache_read`, `cache_write`, their sum `tokens`, `cost_usd`, `seconds` of agent sessions, `sessions`, and what the sessions ran on their machine: `cpu_seconds` and `peak_rss_bytes`. 64-bit numbers are strings in JSON, as protobuf writes them.

**Processes** (`procs`) are everything that runs for a worktree: the agent's CLI, tests, browsers, builds, emulators. A process belongs to the worktree its working directory is in, and so does everything it starts. Each one has a `kind` (`agent`, `test`, `browser`, `build`, `emulator`, `runtime`, `container`, `git`, `shell`, `other`), `cpu_pct` (share of one processor since the previous sample; 200 is two processors busy), `rss_bytes` and a short `command`: the program and its first plain arguments. Options are never sent, because that is where keys travel. The six heaviest are listed in `top`; the totals count all of them. Processes are measured on Linux; a machine that does not measure them answers `procs_supported: false`.

**What it costs.** An answer reads only the memory of the daemon that is asked: no git, no disk, no other machine. Totals are added up as events are applied, so finished items are counted and not walked: with 5,000 finished items an answer takes about a third of a millisecond. What only a machine can see is read there on a timer and sent to the others as live state (the event `machine.worktrees`, never stored): the list of worktrees every 30 seconds while something runs and every 2 minutes otherwise; the processes of the machine in one pass of at most every 15 seconds (about 7 ms with 500 processes), less often when a pass turns out slow; sizes in the background, one worktree at a time, and again only for a worktree that was used since it was measured. A machine that just started is complete in the others' answers after its first report.

## Capabilities and tools

`Capabilities` says what is active on each machine ([operations](operations.md#what-runs-on-each-machine)); `InstallTools` installs or removes the optional tools without root ([operations](operations.md#installing-optional-tools)). Both answer for the machine whose daemon is asked and pass the request on to the named peers (`machines`) or to every connected one (`all`); a peer that cannot be reached comes back with `error` set. They need an owner or admin token; a session token is always refused.

```sh
curl -s --unix-socket ~/.local/share/barnraise/barnraise.sock \
  -X POST http://localhost/barnraise.v1.BarnraiseService/Capabilities \
  -H 'Content-Type: application/json' -d '{"all": true, "project": "shop"}'
... InstallTools -d '{"tools": ["mergiraf", "rtk"], "machines": ["studio"]}'
... InstallTools -d '{"tools": ["rtk"], "all": true, "remove": true}'
```

| Request | Fields |
| --- | --- |
| `CapabilitiesRequest` | `machines` (empty and `all` false: this machine), `all`, `project` (the project whose `barnraise.yaml` switches count; empty: every project of the machine), `refresh` (probe again instead of the report of the last minutes) |
| `InstallToolsRequest` | `tools` (`mergiraf`, `rtk`, `osv-scanner`, `ast-grep`, `smolvm`), `machines`, `all`, `remove` |

`CapabilitiesResponse.machines[]` has `machine`, `platform`, `probed_at`, `counts` (status -> number), `error`, `tools[]` (`id`, `title`, `installed`, `pinned`, `managed`, `available`, `path`) and `items[]`: `id` (`sandbox.landlock`, `merge.structural`, `tokens.rtk`, `plugins.<name>`, `providers.acp.<name>`…), `title`, `group`, `kind` (`native`, `optional`, `plugin`), `status` (`active`, `available`, `degraded`, `missing`, `unsupported`), `reason`, `fallback`, `fix`, `tool` (set when `InstallTools` with that tool is the fix), `version` and `needs_root`. `InstallToolsResponse.results[]` has `machine`, `tool`, `ok`, `version`, `error` and `changed`. `ListMachines` carries each machine's `capability_counts`.


## Login on several machines

`StartAccountLogin` starts the provider's own login of one account on this machine, on the named peers (`machines`) or on every connected one (`all`), all at the same time ([operations](operations.md#one-account-on-several-machines)). Every machine keeps its own login; only the steps travel between machines, never a credential. The calls need an owner or admin token; a session token is always refused.

```sh
... StartAccountLogin  -d '{"provider": "codex", "all": true}'                 # a new account: the answer names it
... GetAccountLogin    -d '{"account": "codex-1", "all": true}'                # ask again while a login is running
... AnswerAccountLogin -d '{"account": "claude-1", "machines": ["studio"], "code": "…"}'
... CancelAccountLogin -d '{"account": "codex-1", "all": true}'
```

| Message | Fields |
| --- | --- |
| `AccountLoginRequest` | `account` (empty on start: a new account of `provider`), `provider` (`claude` or `codex`), `machines` (empty and `all` false: this machine), `all`, `code` (`AnswerAccountLogin` only, for the one machine named) |
| `AccountLoginResponse` | `account`, `logins`: one `AccountLogin` for each machine that runs a login of the account |
| `AccountLogin` | `machine`, `account`, `provider`, `state` (`running`, `done`, `failed`, `cancelled`), `url` (where to sign in), `code` (the one-time code to type there, when the provider gives one), `wants_code` (the login waits for the code the page shows), `lines` (the last lines the login printed), `error`, `started_at`, `ended_at` |

A machine that cannot be reached, or that runs a Barnraise without this, comes back as a login with `state: failed` and `error` set. When a login ends well, the account is recorded as logged in on that machine (`Account.machines`); with any name there, only those machines and `machine` use the account.

## Events

Everything that happens becomes an event in an append-only log. Event types are versioned names, the same in the API, in CloudEvents, in webhooks and on NATS.

| Group | Types |
| --- | --- |
| Item lifecycle | `item.created`, `item.updated`, `item.claimed`, `item.released`, `item.step.started`, `item.step.finished`, `item.waiting_owner`, `item.blocked`, `item.queued`, `item.integrated`, `item.closed`, `item.paused`, `item.resumed`, `item.redo` |
| Living plan | `item.impacted`, `item.revalidated`, `item.updated_by_change`, `item.superseded`, `item.obsoleted`, `item.merged_into` |
| Work | `delivery.submitted`, `review.submitted`, `check.requested`, `check.completed`, `rework.started`, `merge.conflicts.reapplied` |
| Main moving and rework lessons | `item.stale_read`, `lesson.learned`, `lesson.merged`, `lesson.deleted` |
| Trust | `trust.recorded`, `trust.accepted`, `trust.mismatch`, `security.token.denied`, `security.chain.broken` |
| Goal and focus | `goal.set`, `goal.finished`, `focus.changed` |
| Proposals and tweaks | `proposal.created`, `proposal.updated`, `tweak.added`, `tweak.attached` |
| Owner | `owner.question.asked`, `owner.question.answered` |
| Messages | `message.sent`, `message.delivered` |
| Accounts and machines | `account.updated`, `account.limit.reached`, `machine.updated`, `machine.left`, `machine.tool.installed`, `machine.tool.removed`, `machine.tool.failed` |
| Agents | `agent.session`, `agent.usage`, `journal.entry` |
| Security and control | `policy.violation.blocked`, `secret.blocked`, `system.paused`, `system.resumed` |

### CloudEvents

Events leave the daemon as [CloudEvents 1.0](https://cloudevents.io/) in structured JSON mode. The CloudEvents `type` is the Barnraise type with the reverse-DNS prefix `dev.barnraise.`; filters (`types`, webhook `events`) use the plain Barnraise type.

```json
{
  "specversion": "1.0",
  "id": "01JD3…",
  "source": "barnraise://laptop/demo",
  "type": "dev.barnraise.item.integrated",
  "subject": "I-42",
  "time": "2026-11-23T12:57:44Z",
  "datacontenttype": "application/json",
  "data": { "item": { "id": "I-42", "status": "integrated", "…": "…" } },
  "barnraiseproject": "demo",
  "barnraisemachine": "laptop",
  "barnraiseactor": "barnraise"
}
```

`data` carries snapshots of the entities involved (`item`, `proposal`, `goal`, `focus`, `delivery`, `review`, `check`, `rework`, `account`, `machine`, …) plus `cause` (the link to the change that caused it) and `note`.

### Live stream (SSE)

```sh
# live only
curl -N --unix-socket "$SOCK" -H "Authorization: Bearer $TOKEN" \
  "http://localhost/v1/events?project=demo&types=item.*,check.completed"

# replay from a sequence number, then live
curl -N --unix-socket "$SOCK" -H "Authorization: Bearer $TOKEN" \
  "http://localhost/v1/events?project=demo&after_seq=120"
```

| Parameter | Meaning |
| --- | --- |
| `project` | Project; a token bound to one project always gets that project |
| `types` | Comma-separated types or prefixes (`item.*`) |
| `after_seq` | Replay events after this local sequence number first |
| `backlog=1` | Replay the recent backlog first |
| `token` | The bearer token, for `EventSource` clients that cannot set headers |

Each SSE message has `id:` (the local sequence number), `event:` (the Barnraise type) and `data:` (the CloudEvent). Reconnecting with `Last-Event-ID` resumes after that sequence. The stream sends `: ping` every 20 seconds. The scope needed is `events:read` or `items:read`.

The RPC `ListEvents` and `barnraise events [--after N] [--types …] [-f]` read the same log.

## Webhooks

### Outgoing

Declared in `barnraise.yaml` (see [configuration.md](configuration.md#webhooks)). Each matching event is POSTed as a CloudEvent with `Content-Type: application/cloudevents+json` and these headers:

| Header | Value |
| --- | --- |
| `X-Barnraise-Timestamp` | Unix seconds |
| `X-Barnraise-Signature` | `sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>` (when `secret_ref` is set) |
| `X-Barnraise-Delivery` | The CloudEvent id, for deduplication |

The timestamp is part of the signed message, so the replay window cannot be bypassed by changing the header. Failed deliveries are retried up to 6 times with exponential backoff starting at 20 s (capped at 5 min, honouring `Retry-After`); a `4xx` other than `408` and `429` is not retried. `agent.session` and `machine.updated` are only on the live stream, not on webhooks.

Verifying a delivery (any language; shell shown):

```sh
expected="sha256=$(printf '%s.%s' "$TIMESTAMP" "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex | sed 's/^.* //')"
[ "$expected" = "$SIGNATURE" ] && [ $(( $(date +%s) - TIMESTAMP )) -le 300 ]
```

Compare in constant time in real code, and reject timestamps outside your window (Barnraise itself uses 5 minutes inbound).

### Inbound

| Endpoint | Verified with | Does |
| --- | --- | --- |
| `POST /v1/webhooks/github` | `X-Hub-Signature-256` (GitHub's `sha256=` HMAC of the body) | `push` to main revalidates affected items; completed `check_run` / `workflow_run` on a `br/I-<n>-…` branch reports the matching `github-actions` check; `issues` imports from the tracker; an `issue_comment` `/barnraise approve plan I-42` (or `reject`, `delivery`) from a `tracker.trusted` author approves |
| `POST /v1/webhooks/gitlab` | `X-Gitlab-Token` (shared token) | Finished `pipeline` on an item branch reports its CI checks |
| `POST /v1/webhooks/inbound/{project}` | `X-Barnraise-Signature` and `X-Barnraise-Timestamp`, same scheme as outgoing, 5-minute window | Generic: check results from any system |

The project of a GitHub or GitLab webhook is found from the repository in the payload. The generic body:

```json
{"type": "check.result", "item": "I-42", "check": "security-scan", "commit": "3f2c9e1",
 "passed": false, "summary": "2 high findings", "log_url": "https://scanner.example/r/91"}
```

The secrets are read from the secrets store under the keys `webhook/<project>/github`, `webhook/<project>/gitlab` and `webhook/<project>/inbound`. A webhook without a configured secret is refused. Store them with `barnraise webhook secret <github|gitlab|inbound>` (it reads the secret from standard input, or `--generate` creates a random one and prints it once) and remove them with `barnraise webhook forget <source>`. When `webhook/<project>/inbound` exists, `webhook` checks get the callback URL `http://<listen>/v1/webhooks/inbound/<project>` and sign their requests with it.

### GitHub Actions checks

For a check with `runs_on: github-actions`, Barnraise emits `check.requested` and dispatches the workflow (`workflow_dispatch`) on the item's branch with the inputs `item`, `commit` and `check`. The workflow reports back either through the GitHub webhook (`workflow_run`) or explicitly with [`action/check`](../action/check) and a `checks:report` token. Green moves the item on; red sends it back as rework with the summary.

## MCP

`barnraise mcp` is an MCP server on stdio that talks to the local daemon. Agents get it in their session configuration automatically; the owner can add it to any MCP client, for example `claude mcp add barnraise -- barnraise mcp`.

The tools a session sees follow its role (`BARNRAISE_ROLE`; empty is the owner). An implementer simply does not see `approve`.

| Tool | Scope behind it | Who sees it | What it does |
| --- | --- | --- | --- |
| `next_item` | `items:claim` | every agent role, owner | Atomically reserves the next item and returns the step, instructions and context; with no authorized work the agent records its state and ends its turn |
| `deliver` | `deliveries:write` | implementers, analyst, owner | Ends a step with a fixed status (`DONE`, `DONE_WITH_CONCERNS`, `NEEDS_CONTEXT`, `BLOCKED`), criteria marked with evidence, files touched |
| `review` | `reviews:write` | reviewers, analyst, owner | Two verdicts (criteria, code), a decision (`APPROVE`, `REWORK`, `REJECT`) and findings; a review without both verdicts is refused |
| `propose` | `proposals:write` | everyone | A proposal in the fixed shape, with a daily limit per agent |
| `ask_owner` | `owner:ask` | analyst, implementers by exception, owner | Context, decision, 2 to 4 options with one recommended, and what happens if nobody answers |
| `message` | `messages:write` | everyone | A short message to an agent, a role or whoever works on an item |
| `status` | `items:read` | everyone | Goal and progress, focus, counts, queue, questions, agents, accounts, alerts |
| `context` | `items:read` | everyone | The item's context pack, cut to the token budget |
| `find` | `items:read` | everyone | Layered search over items, decisions, standards, learnings and code anchors |
| `approve` | `approve` | analyst, owner | Approve or refuse a plan, a delivery or a proposal, saying which |
| `focus` | `focus` | analyst, owner | Set the focus, with a preview |
| `pause` | `pause` | analyst, owner | Stop every agent of the project, or resume |
| `answer` | owner only | owner | Answer a question an agent asked |
| `create_item` | owner only | owner | Create an item with EARS criteria, area and size |

The same operations exist on the CLI (`barnraise next`, `deliver`, `review`, `propose`, `ask`, `message`, `status`, `context`, `find`, `approve`, `focus`, `pause`, `answer`, `item add`), so agents without MCP use the CLI through Bash.
