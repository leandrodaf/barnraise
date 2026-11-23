# Configuration reference: `barnraise.yaml`

`barnraise.yaml` lives at the root of the project's repository. It is short on purpose: it keeps only what differs from its template. Barnraise compiles it in three layers, each one deep-merged over the one before:

1. the built-in defaults, [`templates/base.yaml`](../templates/base.yaml);
2. the template named by `template:` (default `blank`), `templates/<name>/template.yaml`, or a template saved with `barnraise template save <name>` in `~/.config/barnraise/templates/<name>/`;
3. the project's own `barnraise.yaml`.

Mappings merge key by key. Two kinds of value replace instead of merging: a whole flow (`flows.<name>`), so a project that redefines a flow does not inherit stray steps, and a whole model class (`models.classes.<class>`). Lists replace.

You rarely edit this file by hand: `barnraise init` writes it from a conversation, `barnraise tracker connect` and other commands update it, `barnraise check` finds mistakes in plain sentences, and `barnraise explain` prints the compiled configuration with every default filled in.

Formats used below:

- **duration**: a Go duration (`30s`, `2m`, `15m`, `36h`) or days and weeks (`14d`, `2w`, `1.5d`).
- **percent**: an integer from 0 to 100, with or without `%` (`80%`, `80`).
- **secret reference**: `keychain:<key>` reads the system keychain (or the encrypted file store), `env:<NAME>` reads an environment variable. Secrets never go into the file itself.

## Top level

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `project` | string | the repository folder name, as a slug (max 40 characters) | Project name used by `-p`, the API and the events |
| `template` | string | `blank` | `mobile-android`, `backend`, `web`, `content`, `blank` or a saved template |
| `language` | string | `en` | Language of what goes to people: items, comments, commits, changelog, reports, questions. Code, CLI, YAML keys and internal prompts are always English |
| `goal` | string or mapping | none | See [goal](#goal) |
| `approves` | `owner` or `analyst` | `owner` | Who approves new features |
| `main_branch` | string | `main` | The integration branch; only the daemon moves it |
| `tracker` | mapping | `{type: local}` | See [tracker](#tracker) |
| `code_host` | mapping | derived from the tracker | See [code_host](#code_host) |
| `checks` | mapping | from the template | See [checks](#checks) |
| `roles` | mapping | see [roles](#roles) | Functions agents play |
| `flows` | mapping | from the template | See [flows](#flows) |
| `kinds` | mapping | see [kinds](#kinds) | Item kind to flow name |
| `capacity` | mapping | `{goal: 80%, bugs: 15%, improvements: 5%}` | See [capacity](#capacity) |
| `proposals` | mapping | `{per_agent_per_day: 3, expire: 14d, duplicate: comment}` | See [proposals](#proposals) |
| `items` | mapping | see [items](#items) | Item size and tweaks |
| `self_improvement` | mapping | `{budget: 1/week, approves: owner}` | See [self_improvement](#self_improvement) |
| `models` | mapping | built-in router | See [models](#models) |
| `policy` | mapping | built-in policy | Extra security rules, appended to the built-in ones; see [policy](#policy) |
| `areas` | mapping | none (plus `.barnraise/areas.yaml`) | See [areas](#areas) |
| `merge` | mapping | `{mode: squash, batch: 4}` | See [merge](#merge) |
| `lease` | mapping | `{renew: 30s, ttl: 2m}` | See [lease](#lease) |
| `budget` | mapping | `{triage_week: 2%, context_tokens: 6000, compact_at: 60%}` | See [budget](#budget) |
| `revalidation` | mapping | `{stale_after: 14d}` | See [revalidation](#revalidation) |
| `notifications` | list | none | See [notifications](#notifications) |
| `webhooks` | list | none | See [webhooks](#webhooks) |
| `experiments` | list | none | See [experiments](#experiments) |
| `worktree` | mapping | `{include: [.env, .env.local], port_base: 4100, safety_commit: 15m}` | See [worktree](#worktree) |

A minimal file, as `barnraise init` writes it:

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

## goal

A string is the goal's name. The mapping form:

| Key | Default | Meaning |
| --- | --- | --- |
| `name` | none | The long-term result |
| `done_when` | none | Its measurable end |
| `inventory` | none | How to list its finite work once: `screens <dir>`, `files <path>` or `lines <file>` |
| `budget` | none | percent: the most of the accounts' week this goal may use |

```yaml
goal:
  name: Mobile app matching the mockup
  done_when: every screen in docs/prototipo-mobile approved by the designer role
  inventory: screens docs/prototipo-mobile
```

The same can be set with `barnraise goal set "<name>" --done-when "<end>" --inventory "screens docs/prototipo-mobile"`.

## tracker

Where people see items. The daemon owns the flow state; the tracker is the window people read and comment on, synchronized both ways.

| Key | Default | Meaning |
| --- | --- | --- |
| `type` | `local` | `local`, `github`, `gitlab`, `jira`, `linear` |
| `repo` | none | `owner/name` (GitHub) or the project path (GitLab) |
| `project` | none | GitHub Projects number, Jira key (`KEY` or `email/KEY`), Linear team key |
| `url` | none | Base URL for GitLab or Jira |
| `columns` | none | Barnraise status (`draft`, `ready`, `in_progress`, `waiting_owner`, `blocked`, `queued`, `integrated`, `closed`, `paused`) to tracker column, e.g. `{ready: Todo, integrated: Done}` |
| `trusted` | none | Authors or labels whose issues become items directly; everything else goes through the analyst |
| `token` | see below | secret reference; `barnraise tracker connect <type>` stores it as `keychain:…` |

Without `token`, Barnraise reads `GITHUB_TOKEN` (then `gh auth token`), `GITLAB_TOKEN`, `JIRA_TOKEN` (with `JIRA_EMAIL` when `project` has no email) or `LINEAR_API_KEY`.

## code_host

The git server, separate from the tracker. When `type` is empty it is `github` or `gitlab` when the tracker is, with the tracker's `repo`; otherwise `git` (branches and commits without pull requests; the review stays in the panel).

| Key | Default | Meaning |
| --- | --- | --- |
| `type` | derived | `github`, `gitlab` or `git`; pull requests and workflow dispatch need `github` (or `gitlab` for merge requests) |
| `repo` | the tracker's repo | `owner/name` |
| `url` | none | Base URL (GitLab, Gitea) |
| `remote` | `origin` | git remote used as the arbiter and for pushes |
| `token` | the tracker's token, then `GITHUB_TOKEN` / `GITLAB_TOKEN` | secret reference |

## checks

Each check is a command string or a mapping:

| Key | Default | Meaning |
| --- | --- | --- |
| `run` | none | Command, for `local` and `machine:<label>` checks |
| `runs_on` | `local` | `local`, `github-actions`, `machine:<label>` (a paired machine with that label, e.g. `machine:emulators`), `webhook` |
| `workflow` | none | Workflow file for `github-actions`; dispatched with inputs `item`, `commit` and `check` |
| `url` | none | Endpoint for `webhook` checks; the result comes back on the signed inbound webhook |
| `scope` | `touched` | `touched` runs only for the areas the item changed; `all` runs everything |
| `timeout` | `30m` | duration |
| `flaky_retries` | `3` | Runs on the branch and on main before calling a failure real |
| `areas` | all | Run only for changes that touch these areas |
| `paths` | none | …or that touch a file matching one of these globs (a contract shared by two parts, for example) |
| `covers` | none | Checks this one makes redundant: when both apply, the covered one is skipped (an integration run that already runs the unit tests) |
| `order` | `0` | Lower runs first, cheap before expensive; the step stops at the first failure |
| `dir` | root | Folder the command runs in (set by itself for the checks of a part's own file) |

```yaml
checks:
  unit:
    runs_on: github-actions
    workflow: ci.yml           # dispatched with the item's branch and commit
  maestro:
    runs_on: machine:emulators # a paired machine with emulator slots
    run: maestro test .maestro
  lint:
    runs_on: local
    run: make lint
  security-scan:
    runs_on: webhook
    url: https://scanner.example/run   # any system that answers on the API
```

### Which checks run for a change

A flow step either names one check (`run: checks.unit`) or runs every check that applies (`run: checks`). For each change Barnraise decides, in this order:

1. Files matching `selection.none_when` are left out. A change made only of them runs just the checks with `scope: all`.
2. If a remaining file matches `selection.all_when`, or belongs to no area and to no check's `paths`, **every check runs**: nothing is skipped on unknown ground.
3. Otherwise a check runs when the change touches one of its `areas` or `paths`. A check with neither always runs.
4. A check that another running check `covers` is skipped.

They run by `order`, and the step stops at the first failure. Skipped checks are recorded in the item's timeline with the reason. The merge queue applies the same selection to what the batch touches, and does not run anything again when main did not move since the item's own checks (the tree that lands is the one already checked).

```yaml
selection:
  all_when:  [Makefile, "scripts/**", "**/go.mod", "**/package.json"]   # infrastructure and dependencies
  none_when: ["docs/**", "*.md"]                                         # needs no check
```

`barnraise check --touched backend/api/user.go --touched web/src/App.tsx` shows which checks would run for those files, and why the others are skipped. Each check also receives `BARNRAISE_TOUCHED` (the files, one per line) and `BARNRAISE_AREAS`, so a command can narrow itself further, for example to the tests related to the touched sources.

A `github-actions` check needs the code host to be GitHub; see [`action/check`](../action/check) for the workflow side. `barnraise check` reports a check with no command, no workflow or no url.

## roles

Built-in defaults (from `base.yaml`):

```yaml
roles:
  implementer: { class: auto, count: 2 }
  reviewer:    { class: normal, count: 1, reviewer: true }
  critic:      { class: cheap, count: 1, reviewer: true }
  analyst:     { class: normal, count: 1, reviewer: true }
```

| Key | Default | Meaning |
| --- | --- | --- |
| `prompt` | `.barnraise/roles/<role>.md` | Path of the role core, or inline text; falls back to the template's role file |
| `class` | none | Default model class: `simple`, `normal`, `complex`, `native`, `cheap`, or `auto` (the item's class) |
| `providers` | any | Allowed providers, in order (`claude`, `codex`) |
| `count` | `1` | Agents of this role per machine |
| `tools` | by role | MCP tools the role sees (defaults follow its scopes) |
| `needs` | none | Machine labels it requires, e.g. `[emulators]` |
| `skills` | none | Procedures loaded on demand |
| `reviewer` | `false` | The role returns reviews; `reviewer`, `critic`, `designer`, `analyst` and `editor` are reviewers anyway |

## flows

A flow is an ordered mapping of steps plus its rework rules. Step order is the order in the YAML.

| Step key | Meaning |
| --- | --- |
| `role` | An agent step run by this role |
| `run` | `merge_queue`, `checks.<name>` (or `checks` for all), or any other command (a script step) |
| `approve` | Who approves: `owner` or a role. On an agent step it adds an approval step `<step>-approval` right after it |
| `type` | Explicit type: `agent`, `script`, `approval`, `check`, `merge_queue` (normally inferred) |
| `model` | Fixed model for the step, e.g. `haiku` |
| `model_family` | `different_from(<step>)`: use another model family than that step (Codex reviewing Claude) |
| `class` | Model class for the step |
| `output` | `plan.md` (the plan step) or `failing_test` (the reproduce step of a bug) |
| `scope` | For check steps: `touched` or `all` |
| `require` | For check steps: `failing_test_now_passes` |
| `review` | Force the step to return a review (`true`) or a delivery (`false`) |

`rework` maps `<step>.<outcome>` to the step to return to (`check.fail: implement`, `design.reject: implement`, `analyst.rework: implement`), plus `limit`:

| `rework.limit` key | Default | Meaning |
| --- | --- | --- |
| `same_model` | `3` | Returns with the same model |
| `escalate` | `2` | Further returns one model tier up |
| `then` | `owner` | Who gets the item after that |

```yaml
flows:
  feature-mobile:
    steps:
      plan:       { role: implementer, output: plan.md, approve: analyst }
      implement:  { role: implementer, class: auto }
      quick-look: { role: critic, model: haiku }       # cheap check before the expensive ones
      check:      { run: checks.main, scope: touched }
      design:     { role: designer, model_family: different_from(implement) }
      analyst:    { role: analyst }
      integrate:  { run: merge_queue }
    rework:
      quick-look.fail: implement
      check.fail:      implement     # carries the check log
      design.reject:   implement     # carries the findings
      analyst.rework:  implement
      limit: { same_model: 3, escalate: 2, then: owner }
  bug:
    steps:
      reproduce:  { role: implementer, output: failing_test }
      implement:  { role: implementer }
      check:      { run: checks.main, require: failing_test_now_passes }
      review:     { role: reviewer, model_family: different_from(implement) }
      integrate:  { run: merge_queue }
  blog-post:
    steps:
      draft:  { role: writer }
      review: { role: editor, model_family: different_from(draft) }
      owner:  { approve: owner }
```

## kinds

Item kind to flow. Built-in defaults:

```yaml
kinds:
  feature: feature
  improvement: feature
  bug: bug
  self_improvement: feature
```

The `mobile-android` template maps `feature` and `improvement` to `feature-mobile`. A kind without a mapping uses the flow of the same name.

## capacity

How the scheduler splits work between lanes. The three shares must add up to 100.

| Key | Default | What goes in |
| --- | --- | --- |
| `goal` | `80%` | Items of the goal's inventory and of the focus |
| `bugs` | `15%` | Bugs; P0 bugs always go first |
| `improvements` | `5%` | Approved proposals, by value |

Self-improvement of Barnraise is a separate lane, set in [self_improvement](#self_improvement).

## proposals

| Key | Default | Meaning |
| --- | --- | --- |
| `per_agent_per_day` | `3` | Proposals each agent may make per day |
| `expire` | `14d` | A proposal not approved by then closes itself, with the reason recorded |
| `duplicate` | `comment` | What happens to a proposal similar to an existing one in the same area |

## items

| Key | Default | Meaning |
| --- | --- | --- |
| `target_size` | `2h-6h` | Target size in agent hours; the planner merges or splits to reach it |
| `smaller` | `tweak` | What smaller work becomes |
| `tweaks.group_by` | `area` | How tweaks are grouped |
| `tweaks.become_item_at` | `5` | Pending tweaks of an area that become one item |
| `tweaks.attach_on_claim` | `true` | Attach the area's pending tweaks to the item an agent takes |
| `same_area_findings` | `rework` | Review findings in the item's own area are fixed in the same round |

## self_improvement

| Key | Default | Meaning |
| --- | --- | --- |
| `budget` | `1/week` | `<n>/week` items that improve Barnraise itself |
| `approves` | `owner` | Who approves them |

## models

The router picks provider, model and effort by class, with fallbacks, and escalates along each provider's ladder on rework. A choice is written `provider/model` or `provider/model@effort`.

Built-in classes (each one is replaced whole when you redefine it):

| Class | Candidates, in order |
| --- | --- |
| `simple` | `claude/sonnet@low`, `codex/gpt-5.5-mini` |
| `normal` | `claude/opus@medium`, `claude/sonnet@high`, `codex/gpt-5.5` |
| `complex` | `claude/fable@high`, `claude/opus@high`, `codex/gpt-5.5@high` |
| `native` | `claude/fable@high`, `claude/opus@high` |
| `cheap` | `claude/haiku`, `claude/sonnet@low`, `codex/gpt-5.5-mini` |

Built-in ladders (cheap to strong): `claude: [haiku, sonnet, opus, fable]`, `codex: [gpt-5.5-mini, gpt-5.5]`, `gemini: [gemini-flash, gemini-pro]`. Families: `claude: anthropic`, `codex: openai`, `gemini: google`, `openrouter: mixed`.

```yaml
models:
  classes:
    normal: [claude/sonnet@high, codex/gpt-5.5]
  ladders:
    codex: [gpt-5.5-mini, gpt-5.5]
  families:
    codex: openai
```

## policy

Extra rules appended to the built-in policy that the PreToolUse hook and the daemon apply. Globs use `*`, `?` and `**`. See [security.md](security.md) for the built-in lists.

| Key | Meaning |
| --- | --- |
| `deny_write` | Globs relative to the worktree that agents may never write |
| `lock_existing` | Globs whose existing files cannot change (tests, reference images, check scripts); new files can be added |
| `deny_read` | Absolute or `~` globs agents may never read |
| `deny_commands` | Regular expressions of shell commands to block |
| `allow_hosts` | Extra hosts for the sandbox's network allow list |

```yaml
policy:
  deny_write: ["**/release.keystore", "**/google-services.json", "fastlane/**"]
```

## areas

An area maps to folders, screens, contracts and a standard. Areas can also live in `.barnraise/areas.yaml` (same shape, without the `areas:` key); `barnraise.yaml` wins for an area defined in both.

| Key | Meaning |
| --- | --- |
| `paths` | Globs of the area's files |
| `screens` | Screen names |
| `contracts` | API or event contracts, e.g. `POST /v1/payments` |
| `standard` | `.barnraise/standards/<area>.md` |
| `kinds` | Flow per item kind for the items of this area, e.g. `{feature: feature-mobile}` |

```yaml
areas:
  payments:
    paths: [app/src/main/java/app/payments/**]
    screens: [Payments screen]
    standard: .barnraise/standards/payments.md
```

### Several parts in one repository

A repository often holds several projects side by side: a Go backend, a web app, a mobile app. Each part may carry **its own `barnraise.yaml` in its folder**. Barnraise finds these files by itself (up to three folders deep; dependency folders are never searched) and turns each one into an area: the folder is the area's `paths`, its checks are named `<area>-<check>`, run inside the folder, and only for changes that touch it. `barnraise init` detects the parts and writes these files.

```yaml
# backend/barnraise.yaml
about: REST API of the shop
stack: go
checks:
  lint:        { run: golangci-lint run, order: 1 }
  test:        { run: go test ./..., order: 2 }
  integration: { run: go test -tags=integration ./..., order: 5, covers: [test] }
```

```yaml
# mobile/barnraise.yaml
about: Mobile app, Android first
stack: react-native
kinds: { feature: feature-mobile }      # its items go through emulators and a design review
checks:
  test:    pnpm run test
  maestro: { run: maestro test .maestro, runs_on: "machine:emulators", order: 9 }
```

| Key | Meaning |
| --- | --- |
| `area` | Name of the area (default: the folder's name) |
| `about`, `stack` | One line on what the part is, and its stack. Agents working on an item of this area are told which part they are in, its checks, and which other parts exist |
| `checks` | Same keys as the root's checks; `covers` may use the short names of this file |
| `also_when` | Globs outside the folder that also concern this part: `also_when: [backend/api/openapi.yaml]` in the web app's file runs the web checks when the API contract changes |
| `kinds` | Flow per item kind for the items of this area (the flows themselves are declared in the root) |
| `standard`, `screens`, `contracts` | As in `areas:`; `standard` is relative to the folder |

`project`, `template`, `flows` and `roles` belong in the root file only. When the parts bring checks, the template's own guesses (a single `make test` for everything) are dropped, and the flow steps that pointed at them run every check that applies; checks the root file declares itself are kept. An area declared both in the root and in a part's file is an error.

## merge

| Key | Default | Meaning |
| --- | --- | --- |
| `mode` | `squash` | `squash` (one commit per item) or `steps` (the step commits) |
| `batch` | `4` | Items integrated together; a failing batch is bisected |
| `changelog` | none | Changelog file to update on integration |
| `tag` | `false` | Tag after integrating |
| `use_host_queue` | `false` | Use the code host's merge queue when it has one |

With `squash`, the item's own files (`plan.md`, `criteria.json`) are left out of the commit that lands on main.

## lease

| Key | Default | Meaning |
| --- | --- | --- |
| `renew` | `30s` | How often the holder renews `refs/barnraise/claims/<item>` |
| `ttl` | `2m` | Without renewal for this long (measured on the local clock of the machine that observes the ref), another machine may take the item |

## budget

| Key | Default | Meaning |
| --- | --- | --- |
| `triage_week` | `2%` | Most of the accounts' week spent on triage of the living plan |
| `goal_week` | none | Most of the accounts' week the goal may use |
| `context_tokens` | `6000` | Token budget of an item's context pack (`barnraise context`) |
| `compact_at` | `60%` | Context use at which the agent is asked to update `plan.md` and the context is cleared |

## revalidation

| Key | Default | Meaning |
| --- | --- | --- |
| `stale_after` | `14d` | Items not validated for this long enter the nightly sweep (`barnraise revalidate --sweep`) |

## notifications

Owner notification channels. Each entry has `type`, `target`, `secret` (a secret reference) and `extra`:

| `type` | `target` | `secret` | `extra` |
| --- | --- | --- | --- |
| `webhook` | URL | signing secret (optional) | |
| `ntfy` | topic or full topic URL | access token (optional) | `server` (default `https://ntfy.sh`), `action_url` |
| `telegram` | chat id | bot token | `api` |
| `slack` | incoming webhook URL (or put the URL in `secret`) | | |
| `email` | recipients, comma-separated | SMTP password | `host`, `port` (587), `username`, `from` |
| `desktop` | | | |

```yaml
notifications:
  - type: ntfy
    target: barnraise-leandro
  - type: telegram
    target: "123456789"
    secret: keychain:telegram-bot
```

## container

Runs every agent session of the project inside a container. Agents never wait for a permission prompt (they do not outside a container either); what changes is what they can reach: only what is mounted.

| Key | Default | Meaning |
| --- | --- | --- |
| `image` | none | Image with the AI CLIs and the project's toolchain; [`deploy/agent.Dockerfile`](../deploy/agent.Dockerfile) is a starting point |
| `engine` | `docker` | `docker` or `podman` |
| `network` | the engine's default | `bridge`, `host`, `none` or a named network. Use `host` together with the egress proxy, which listens on the machine |
| `mounts` | none | Extra host paths, mounted at the same path: caches, SDKs (`~/.gradle`, `/opt/android-sdk:ro`) |
| `gpu` | `auto` | `auto` passes whatever the machine has (the NVIDIA runtime of the engine, `/dev/dri` render devices); `all` asks for the NVIDIA GPUs and fails loudly without them; `none` keeps the GPU out |

```yaml
container:
  image: ghcr.io/acme/shop-agent:latest
  mounts: ["~/.cache/go-build", "/opt/android-sdk:ro"]
```

Mounted by Barnraise, each at its own path: the item's worktree, the repository's `.git` (a worktree commits there), the agent's account folder, the daemon's socket and the `barnraise` binary (read-only). Not mounted: the owner's home, the daemon's state, other items' worktrees. The session runs as the daemon's uid, with `HOME` inside the session folder. Inside a container the provider's native sandbox is off (bubblewrap does not nest); the container is the boundary, and the blocking hook and the policy still apply.

A machine with an NVIDIA GPU needs the NVIDIA Container Toolkit for containers to see it; the daemon warns when the GPU is there and the engine's runtime is not.

## mcp

MCP servers the agents may use besides Barnraise's own. A session loads **only** Barnraise's server and the ones listed here for its role: the servers, plugins and personal settings of the account's own configuration are never loaded (`--strict-mcp-config`), so an agent cannot reach the owner's mail, ads or cloud tools, and their tool lists do not cost tokens in every session.

| Key | Default | Meaning |
| --- | --- | --- |
| `command`, `args` | none | A local server started by the session |
| `url`, `type` | none, `http` | A remote server (`http` or `sse`); use this or `command` |
| `env` | none | Environment of a local server; a value may be a secret reference (`keychain:…`, `env:…`), resolved when the session starts |
| `roles` | every role | Roles that get this server |

```yaml
mcp:
  figma:
    command: figma-mcp
    env: { FIGMA_TOKEN: "keychain:mcp/figma" }
    roles: [implementer, designer]
  docs:
    url: https://docs.example/mcp
```

`barnraise check` reports a server without `command` or `url`, or one for a role that does not exist. A secret that cannot be read stops the session from starting.

## webhooks

Outgoing webhooks: every matching event is sent as a CloudEvent, signed with HMAC-SHA256 and retried. See [api.md](api.md#webhooks).

| Key | Default | Meaning |
| --- | --- | --- |
| `url` | none | Where to POST |
| `events` | all | Event types; a trailing `*` matches a prefix (`item.*`) |
| `secret_ref` | none | secret reference of the signing secret |

```yaml
webhooks:
  - url: https://n8n.example/webhook/barnraise
    events: [item.integrated, owner.question.asked, policy.violation.blocked]
    secret_ref: env:N8N_SECRET
```

## experiments

Send a share of the items to a change before the whole fleet gets it.

| Key | Default | Meaning |
| --- | --- | --- |
| `name` | none | Name |
| `share` | `10%` | Share of items |
| `role` | all | Role it applies to |
| `model` | none | Model to try |
| `prompt` | none | Role prompt to try |
| `env` | none | Extra environment for the sessions |

## worktree

| Key | Default | Meaning |
| --- | --- | --- |
| `include` | `[.env, .env.local]` | Files copied from the repository into each item worktree |
| `port_base` | `4100` | Base of the per-item server port (`PORT` in the agent's environment) |
| `safety_commit` | `15m` | Pending changes are committed and pushed at least this often |

## Machine configuration (`daemon.yaml`)

The machine's own configuration is not part of any repository. It lives in `~/.config/barnraise/daemon.yaml` (mode 0600) and is written by `barnraise up` and `barnraise machine invite/join/leave`; people do not edit it by hand.

| Key | Default | Meaning |
| --- | --- | --- |
| `machine` | the short hostname | Machine name (`barnraise daemon --machine`) |
| `projects` | none | Repository roots this daemon drives (`barnraise up` adds the current one) |
| `listen` | `127.0.0.1:7420` | TCP address of the API (`--listen`); `off` disables TCP; non-loopback needs `--tls-cert` and `--tls-key` |
| `nats_host` | `127.0.0.1` | NATS address; with peers it moves to the Tailscale address or `0.0.0.0` |
| `nats_port` | `4222` | NATS port (`--nats-port`) |
| `nkey_seed`, `nkey_public` | generated | This machine's NATS key |
| `peers` | none | Paired machines: `name`, `url`, `public_key`, `seed`, `added` |
| `network` | none | `tailscale` or `lan` |
| `notify` | none | Machine-level notification channels (`type`, `target`, `secret`) |
