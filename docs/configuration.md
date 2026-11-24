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
| `providers` | mapping | `{openrouter: {base_url: https://openrouter.ai/api/v1}}` | Key-based providers; see [providers](#providers) |
| `policy` | mapping | built-in policy | Extra security rules, appended to the built-in ones; see [policy](#policy) |
| `areas` | mapping | none (plus `.barnraise/areas.yaml`) | See [areas](#areas) |
| `merge` | mapping | `{mode: squash, batch: 4}` | See [merge](#merge) |
| `lease` | mapping | `{renew: 30s, ttl: 2m}` | See [lease](#lease) |
| `budget` | mapping | `{triage_week: 2%, context_tokens: 6000, compact_at: 60%}` | See [budget](#budget) |
| `outfilter` | mapping | `{delegate: auto, keep: 7d, keep_size: 64MB}` | Output filter of agents' commands; see [outfilter](#outfilter) |
| `terse` | mapping | `{mode: auto}` | Short answers in mechanical steps; see [terse](#terse) |
| `revalidation` | mapping | `{stale_after: 14d}` | See [revalidation](#revalidation) |
| `notifications` | list | none | See [notifications](#notifications) |
| `plugins` | mapping | none | External plugins; see [plugins](#plugins) |
| `webhooks` | list | none | See [webhooks](#webhooks) |
| `experiments` | list | none | See [experiments](#experiments) |
| `worktree` | mapping | `{include: [.env, .env.local], port_base: 4100, safety_commit: 15m}` | See [worktree](#worktree) |
| `stale_reads` | mapping | `{enabled: true, rebase: false, max_files: 10}` | See [stale_reads](#stale_reads) |
| `lessons` | mapping | `{enabled: true, max: 5}` | See [lessons](#lessons) |

A minimal file, as `barnraise init` writes it:

```yaml
template: mobile-android
language: pt-BR
goal: Personal finance app, Android first, matching docs/prototipo-mobile
approves: owner
tracker:
  type: github
  repo: acme/shop
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
| `type` | `local` | `local`, `github`, `gitlab`, `jira`, `linear`, or `plugin:<name>` for a tracker plugin (see [plugins](#plugins)) |
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
| `runs_on` | `local` | `local`, `github-actions`, `machine:<label>` (a paired machine with that label, e.g. `machine:emulators`), `webhook`, `plugin:<name>` (a check plugin, see [plugins](#plugins); it receives `run` as given) |
| `workflow` | none | Workflow file for `github-actions`; dispatched with inputs `item`, `commit` and `check` |
| `url` | none | Endpoint for `webhook` checks; the result comes back on the signed inbound webhook |
| `scope` | `touched` | `touched` runs only for the areas the item changed; `all` runs everything |
| `timeout` | `30m` | duration |
| `flaky_retries` | `3` | Runs on the branch and on main before calling a failure real. Red on the branch and green on main sends the item back; red on main too holds the item for the owner (main or the machine is broken), without rework |
| `areas` | all | Run only for changes that touch these areas |
| `paths` | none | …or that touch a file matching one of these globs (a contract shared by two parts, for example) |
| `covers` | none | Checks this one makes redundant: when both apply, the covered one is skipped (an integration run that already runs the unit tests) |
| `order` | `0` | Lower runs first, cheap before expensive; the step stops at the first failure |
| `dir` | root | Folder the command runs in (set by itself for the checks of a part's own file) |
| `offload` | `true` | `false` keeps the check on the machine that works on the item, even when that machine is loaded and another is idle (for a check that needs something only this machine has) |

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
| `providers` | any | Allowed providers, in order (`claude`, `codex`, `gemini`, `openrouter` or another key-based provider) |
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

Built-in ladders (cheap to strong): `claude: [haiku, sonnet, opus, fable]`, `codex: [gpt-5.5-mini, gpt-5.5]`, `gemini: [gemini-2.5-flash, gemini-2.5-pro]`. Families: `claude: anthropic`, `codex: openai`, `gemini: google`, `openrouter: mixed`.

```yaml
models:
  classes:
    normal: [claude/sonnet@high, codex/gpt-5.5]
  ladders:
    codex: [gpt-5.5-mini, gpt-5.5]
  families:
    codex: openai
```

### Providers

| Provider | CLI | Login | Family | On the machine itself | Inside a [container](#container) |
| --- | --- | --- | --- | --- | --- |
| `claude` | Claude Code (`claude -p`, stream JSON) | Claude subscription | `anthropic` | Blocking PreToolUse hook, plus the native sandbox when `bwrap` works | Hook; the container is the sandbox |
| `codex` | Codex CLI (`codex exec --json`) | ChatGPT login | `openai` | `--sandbox workspace-write` (bubblewrap on Linux) | Own sandbox off; the container is the boundary |
| `gemini` | Gemini CLI (`gemini --output-format stream-json`) | Google account | `google` | A reader: shell, write and edit tools are excluded and the approval mode stays `default` | `--approval-mode yolo`; the container is the boundary |
| any name, [`type: acp`](#agent-programs-type-acp) | Any agent program that speaks the Agent Client Protocol on stdio | The agent's own | The provider's name | Permission requests, file reads and writes answered by the policy; terminals in bubblewrap when it works | The agent and its terminals in the container |

Accounts are added with `barnraise account add <claude|codex|gemini>`. Each one gets its own config dir: `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, or `GEMINI_CLI_HOME` (the Gemini CLI keeps its login in `$GEMINI_CLI_HOME/.gemini`). For a Gemini account, Barnraise selects the Google login in that folder's settings and starts `gemini` once. You sign in in the browser, or with `--headless` by pasting a code, and type `/quit`.

The Gemini CLI has no hook that Barnraise installs, so Barnraise does not rely on its prompt for policy. Outside a container a Gemini session can read the worktree, search, and call Barnraise's MCP tools, but it cannot run a command or change a file. That suits its natural role, cheap review and reading long context: a large diff, a whole module, a long log. A role that must change code runs on Gemini only with `container:`. Each Gemini session also runs with `--extensions none` and `--allowed-mcp-server-names` limited to Barnraise's server and the role's `mcp:` servers. The session's settings, the trusted MCP servers, the excluded tools and `max_turns`, go in as the CLI's system settings (`GEMINI_CLI_SYSTEM_SETTINGS_PATH`), which take precedence over the account's own. Barnraise's system prompt goes at the start of the prompt, because the CLI can only replace its system prompt, not add to it. Effort and JSON schemas have no Gemini equivalent and are ignored.

A typical use is a cheap critic from another family. A reviewer gets the diff, the criteria and the check results in its prompt, so it needs no shell:

```yaml
roles:
  critic: { class: cheap, providers: [gemini, claude] }
models:
  classes:
    cheap: [gemini/gemini-2.5-flash, claude/haiku]
```

## providers

Providers reached with an **API key** instead of a subscription login: OpenRouter, and any other endpoint that speaks the OpenAI Responses API. Their sessions are as agentic as any other: Barnraise runs the Codex CLI against the endpoint (a custom `model_provider` given to `codex exec` with `-c` overrides), with the same sandbox, hooks, MCP servers and policy as a Codex session. Barnraise has no agent loop of its own over an API.

| Key | Default | Meaning |
| --- | --- | --- |
| `<name>.base_url` | none | The endpoint, e.g. `https://openrouter.ai/api/v1`. `https` is required, except for `localhost` and loopback addresses (a local vLLM or Ollama) |

`openrouter` is built in. A provider's name is lower-case letters and digits, and cannot be `claude`, `codex`, `gemini` or `fake`. Its hosts join the egress proxy's allow list.

```yaml
providers:
  local:
    base_url: http://127.0.0.1:8000/v1   # a vLLM server on this machine
models:
  classes:
    cheap: [openrouter/deepseek/deepseek-chat, claude/haiku]   # cheap steps and research go to OpenRouter first
```

The model is everything after the provider: `openrouter/<vendor>/<model>`, e.g. `openrouter/deepseek/deepseek-chat` or `openrouter/qwen/qwen3-coder@low`. Nothing is sent to a key-based provider until the owner names it in a model class, in a step's `model`, or in a role's `providers`. A provider has no ladder unless you add one under `models.ladders`, so rework stays on the same model; its family is `mixed` for `openrouter` (any vendor) and its own name otherwise.

Accounts: `barnraise account add openrouter` asks for the key on standard input (never as a flag, so it stays out of the shell history and the process list) and keeps it in the system keychain as `account/<id>/api-key`. `--test-model deepseek/deepseek-chat` asks that model one minimal question to check the key. The account is bound to the machine whose keychain holds the key (`--machine` to choose another); `barnraise account relogin <id>` replaces the key. An API account has no 5-hour or weekly window, so Barnraise sends it work until the endpoint refuses: a rate limit (429) or the key running out of credits (402) marks the account limited and it is tried again an hour later. **Set the spending limit on the key itself**, at the provider (OpenRouter keys take a credit limit); that is the ceiling, and `--ceiling` does not apply.

The endpoint must speak the Responses API: current Codex CLIs refuse `wire_api = "chat"`. OpenRouter does. A provider that only offers Chat Completions (DeepSeek's own API at `https://api.deepseek.com`, for example) cannot run sessions this way; reach its models through OpenRouter instead (`openrouter/deepseek/deepseek-chat`).

### Agent programs (`type: acp`)

A provider of `type: acp` is any agent program that speaks the [Agent Client Protocol](https://agentclientprotocol.com) (JSON-RPC 2.0 on its stdin and stdout, version 1). Barnraise is the protocol's client, so a new agent needs no adapter of its own: one entry in `providers:` is enough. Off unless declared.

| Key | Default | Meaning |
| --- | --- | --- |
| `<name>.type` | none | `acp`. Without a type, the entry is a key-based provider (`base_url`) |
| `<name>.command` | none (required) | The agent program, found on the `PATH` or given as an absolute path. Inside a [container](#container), the image must have it on its `PATH` |
| `<name>.args` | `[]` | Its arguments, e.g. `[--acp]` or `[--experimental-acp]` |
| `<name>.env` | `{}` | Variables added to the session's environment. A value may be a secret reference (`keychain:<key>`, `env:<VAR>`), resolved when the session starts. The variables Barnraise sets for every session (`BARNRAISE_*`) cannot be replaced |

```yaml
providers:
  myagent:
    type: acp
    command: my-agent
    args: [--acp]
    env:
      MY_AGENT_API_KEY: keychain:my-agent-key
models:
  classes:
    normal: [myagent/default, claude/sonnet]
  families:
    myagent: openai          # the family of the model it runs, for steps that want a different family
```

The name follows the rules of a key-based provider (lower-case letters and digits; not `claude`, `codex`, `gemini`, `fake` or `acp`); an ACP provider named like a built-in key-based one (`openrouter`) replaces it. The model is what follows the name: `myagent/<model>` is selected in the session when the agent lists that model (`session/set_model` or a `model` configuration option), and `myagent/default` keeps the agent's own choice. Effort, `max_turns` and JSON schemas have no ACP equivalent and are ignored. A session started with a resume id resumes with `session/load` when the agent can.

Accounts: `barnraise account add myagent` puts an account in the rotation (plan `acp`). The agent keeps its own login: log in once by hand, as the user the sessions run as (the account's isolated user, with `--isolate`). A session whose agent answers that it needs a login ends with that message. An ACP account has no 5-hour or weekly window: a refusal that says it hit a limit marks the account limited, like an API account.

How the [policy](#policy) applies to these agents is in [security.md](security.md#14-agents-over-the-agent-client-protocol).

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

The trust checks before each session ([security.md](security.md#16-provenance-of-what-steers-an-agent)) and the hash chain of the event log have no keys: they are always on. `policy:` itself is part of `barnraise.yaml`, so a change to it outside `barnraise reload` / `barnraise trust` refuses sessions until the owner re-trusts it.

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

`project`, `template`, `flows`, `roles` and `plugins` belong in the root file only. When the parts bring checks, the template's own guesses (a single `make test` for everything) are dropped, and the flow steps that pointed at them run every check that applies; checks the root file declares itself are kept. An area declared both in the root and in a part's file is an error.

## merge

| Key | Default | Meaning |
| --- | --- | --- |
| `mode` | `squash` | `squash` (one commit per item) or `steps` (the step commits) |
| `batch` | `4` | Items integrated together; a failing batch is bisected |
| `changelog` | `CHANGELOG.md` | Changelog file Barnraise keeps, in the [Keep a Changelog](https://keepachangelog.com) format; empty turns it off |
| `tag` | `false` | Tag after integrating |
| `use_host_queue` | `false` | Use the code host's merge queue when it has one |
| `rerere` | `true` | Keep git's record of conflict resolutions (`rerere`) on for the repository, so a conflict resolved once is resolved again the same way; `false` removes the settings Barnraise added |
| `structural` | `auto` | `auto` merges supported files with [mergiraf](https://mergiraf.org) when it is on the `PATH`; `off` never does. Without it, git's normal merge is used |

With `squash`, the item's own files (`plan.md`, `criteria.json`) are left out of the commit that lands on main.

### Recorded resolutions and structural merge

Barnraise never commits merge settings to the project. At each start and reload of the project it writes them where git keeps local, untracked settings, shared by the main checkout and every worktree (the item worktrees and the merge queue's checkouts alike):

- `rerere`: `rerere.enabled` and `rerere.autoupdate` are set to `true` in the repository's local config (`.git/config`) when no git config sets them; a value you set yourself, in any scope, is kept. Resolutions live in the shared `.git/rr-cache`, so a conflict an agent resolves while rebasing its item is reapplied by the merge queue, by another item or by a retry. When git reapplies one, the item's timeline gets `merge.conflicts.reapplied` with the files, and the merge queue runs every local check on the result (it never skips a check because the tree looks like one already checked). If the checks fail, the reason tells the agent which files came from a recorded resolution and how to forget it (`git rerere forget <file>` during a rebase on main).
- `structural`: with `mergiraf` on the `PATH`, Barnraise registers it as the merge driver `barnraise-structural` in the local config and writes the patterns it supports (from `mergiraf languages --gitattributes`) in a block marked as managed in `.git/info/attributes`. Patterns your `.gitattributes` already gives a merge behaviour (`merge=…`, `-merge`, `binary`) are left out, so your choice wins. Lines of `.git/info/attributes` outside the block are yours and stay. When mergiraf cannot merge a file structurally it falls back to a line merge and leaves conflict markers, and the conflict is handled as any other; where mergiraf is not available to git (an agent in a container), the driver falls back to git's own line merge. With `off`, or when mergiraf is no longer installed, the block and the driver are removed and git merges as before. Barnraise does not ship or vendor mergiraf (GPL-3.0); it only runs it when you installed it.

### Commits and the changelog

Every item lands on main as one commit in the [Conventional Commits](https://www.conventionalcommits.org) form, **always in English**, whatever `language` the project's people read:

```
fix(api): keep cents as integers in the forecast

Rounding happened before the sum, so a month could be off by one cent.

Barnraise-Item: I-142
```

- The agent that did the work describes it in its delivery (`change`: `type`, `scope`, `subject`, and optionally `body` and `breaking`). Barnraise checks the shape: a known type (`feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `style`, `revert`), a one-word scope, a subject in the imperative, lower case, without a final period, a header of at most 72 characters, and English text. A description that is not English is refused, and the agent writes it again.
- When the agent gives none, the item's kind, area and title are used if they already make a valid English header. An item with a title in another language always needs the agent's description.
- A breaking change puts `!` in the header and a `BREAKING CHANGE:` trailer.
- The commits an agent makes while it works follow the same form; with `squash` they are folded into the item's one commit.

The same description writes the changelog. On integration the item's line goes under `## [Unreleased]`, in the section of its type: `feat` → **Added**, `fix` → **Fixed**, `perf` → **Changed**, `revert` → **Removed**, and any breaking change → **Changed**. Tests, build, CI, docs, refactors and upkeep add no line: a changelog is for the people who use the product. The line lands inside the item's own commit, and agents never edit the file.

```markdown
## [Unreleased]

### Added

- **api:** Add CSV export of the statement (I-140)

### Fixed

- **api:** Keep cents as integers in the forecast (I-142)
```

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

## outfilter

Agents' shell commands with a long output (builds, tests, linters, `git status/diff/log`, listings, searches, `docker`, `kubectl`, package installs) go through `barnraise filter`, which prints the errors, the summaries and the counts, and keeps the full text: the last line of a shortened output is `full output: barnraise output show <id>`. A short output is printed as it is. See [operations.md](operations.md#output-filter) for the tools covered and how to read a kept output.

| Key | Default | Meaning |
| --- | --- | --- |
| `delegate` | `auto` | Use [rtk](https://github.com/rtk-ai/rtk) when it is installed on the machine. `auto`: only for commands Barnraise has no native filter for; `rtk`: for every simple command, the native filters included; `off`: never. Without rtk on the `PATH` every mode behaves as `off` |
| `keep` | `7d` | Kept outputs older than this are removed (duration) |
| `keep_size` | `64MB` | Most space the kept outputs of one item worktree (or of the data folder, outside sessions) may take; the oldest go first, the newest always stays. Sizes take `KB`, `MB` or `GB` |

```yaml
outfilter: { delegate: off, keep: 2d, keep_size: 32MB }
```

## terse

Mechanical steps get a short "Answer style" section in their prompt: no preamble, no recap, no closing remarks, while code, commands, paths, errors and the fields of the delivery or review stay exact. The agent's own words are what it pays output tokens for; a step that follows a recipe does not need them.

| Key | Default | Meaning |
| --- | --- | --- |
| `mode` | `auto` | `auto`: only mechanical steps, those whose model class is `cheap` or `simple`, or that are pinned to the first (cheapest) model of a provider's ladder, like the critic's `model: haiku`; `all`: every agent step; `off`: never |
| `steps` | none | Step names that always get it (unless `mode: off`), e.g. `[quick-look, lint-fix]` |

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
| `plugin:<name>` | sent to the plugin | | sent to the plugin |

```yaml
notifications:
  - type: ntfy
    target: barnraise-owner
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

Every container runs with no Linux capabilities and `no-new-privileges`, a read-only image (`/tmp`, `/var/tmp` and `/run` in memory), at most 4096 processes and 65536 open files, no core dumps, and its own hostname. `mounts` never takes the engine's socket (whoever reaches it is root on the machine), `/`, system folders, a whole home or a credential folder (`~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config`, `~/.docker`, `~/.kube`…): `barnraise check` says so, and the session refuses to start.

Mounted by Barnraise, each at its own path: the item's worktree, the repository's `.git` (a worktree commits there), the agent's account folder, the daemon's socket and the `barnraise` binary (read-only). Not mounted: the owner's home, the daemon's state, other items' worktrees. The session runs as the daemon's uid, with `HOME` inside the session folder. Inside a container the provider's native sandbox is off (bubblewrap does not nest); the container is the boundary, and the blocking hook and the policy still apply.

With `gpu` other than `none`, `/dev/kvm` is passed in too when the machine has it, so an Android emulator can run inside the container. Codex runs there with its own sandbox off (bubblewrap cannot nest in a container, and the container is the boundary); on the machine itself it always runs in `workspace-write`. Gemini runs there with `--approval-mode yolo`; on the machine itself it is a reader (see [Providers](#providers)). A Gemini account that uses the CLI's own login gets only `~/.gemini` mounted.

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

## plugins

External plugins: programs the owner configures, in any language, that the daemon starts and talks to over JSON-RPC on their standard input and output. The protocol is [plugins.md](plugins.md). A plugin is used where a tracker, a check or a notification channel is chosen, by `plugin:<name>`, and receives events when it lists them.

| Key | Default | Meaning |
| --- | --- | --- |
| `command` | none | The program: a name on the `PATH`, an absolute path, `~/…`, or a path relative to the repository root (keep those in `.barnraise/plugins/`) |
| `args` | none | Its arguments |
| `env` | none | **Names** of the daemon's environment variables passed to it; nothing else of the daemon's environment reaches it besides `PATH`, `HOME`, `USER`, `LANG`, `TZ` and `TMPDIR` |
| `secrets` | none | Variable name to secret reference (`keychain:…`, `env:…`), resolved when it starts and passed in its environment; never a value |
| `timeout` | `30s` | duration: how long each call may take; a check uses the check's own `timeout` |
| `events` | none | For a hook plugin: event types it receives; a trailing `*` matches a prefix (`item.step.*`), `*` is every event |
| `options` | none | The plugin's own settings, sent in `initialize` |

```yaml
plugins:
  youtrack:
    command: .barnraise/plugins/youtrack.py
    env: [YOUTRACK_URL]
    secrets: { YOUTRACK_TOKEN: "keychain:youtrack" }
    options: { project: SHOP }
  farm:
    command: device-farm-plugin
    events: ["item.step.*", item.integrated]
tracker:
  type: plugin:youtrack
  columns: { ready: Todo, in_progress: Doing, integrated: Done }
checks:
  devices: { runs_on: "plugin:farm", run: "android-14 pixel-8", timeout: 45m }
notifications:
  - type: plugin:farm
    target: "#mobile"
```

`barnraise check` reports a plugin without `command`, a program that is not found, an `env` entry that is not a variable name, a secret that is a value instead of a reference, and a `plugin:<name>` that names no declared plugin. Plugins belong in the root `barnraise.yaml`, not in a part's file. A plugin program inside the repository is added to the policy's `deny_write`, so agents cannot change it.

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
| `include` | none | Files that are not in git, copied from the repository into each item worktree |
| `port_base` | `4100` | Base of the per-item server port (`PORT` in the agent's environment) |
| `safety_commit` | `15m` | Pending changes are committed and pushed at least this often |
| `setup` | none | Command run once in each new worktree, before any work or check there: install dependencies, generate code (`make install`, `pnpm install --frozen-lockfile`). Also run in the scratch checkouts of checks moved to another machine |
| `setup_timeout` | `30m` | How long `setup` may take |
| `seed` | none | Directories copied from the project root into a worktree before `setup` runs (`web/node_modules`), so the setup only checks what is already there |
| `stores` | none | Directories shared by every worktree of the project: store name to the environment variable that receives its path (`pnpm: npm_config_store_dir`) |
| `strip` | none | Rebuildable directories removed from the worktree of an item that waits for the owner or is blocked (`web/node_modules`, `mobile/android/app/build`) |
| `strip_after` | `1h` | How long an item waits before `strip` empties its worktree |

`include` copies files that are not in git into every worktree. It is empty by default, because such files (`.env`) usually hold what an agent must not have: secrets, or the address of a real database. List a file only when it is safe in an agent's hands.

### Many worktrees, one set of dependencies

Every item has its own worktree, and a fresh checkout has no dependencies: without help each one installs its own copy, and ten items are ten `node_modules`. Three keys keep that small. They are independent; use the ones that fit the machine.

```yaml
worktree:
  setup: pnpm install --frozen-lockfile
  seed: [node_modules]
  stores: { pnpm: npm_config_store_dir }
  strip: [node_modules, android/app/build]
  strip_after: 2h
```

**`seed`** copies directories from the project root (the repository `barnraise up` ran in) into a new worktree, before `setup`. The copy keeps file times, so build tools do not see everything as changed. On a filesystem that shares blocks between copies (btrfs, XFS with reflink, APFS) it takes almost no time and no space until a file changes. On ext4 it is a plain copy: it saves the download and the install time, not the disk. A directory that is missing in the root, or already in the worktree, is skipped; a copy that fails is undone and `setup` installs from scratch. A plain copy also turns files a package manager had linked to its store into files of their own: on ext4 choose `stores` for the disk, and add `seed` only if the install time matters more.

**`stores`** is for the disk on any filesystem. Each store is one directory for the whole project, in the data directory (`stores/<project>/<name>/`), on the same filesystem as the worktrees. Its path reaches `setup`, the checks and the agent in the environment variable you name, and the directory is mounted in the agent's container at the same path. Point the package manager's store or cache at it and every worktree links to the same files instead of holding its own: `pnpm: npm_config_store_dir` (pnpm hard-links from its store), `gradle: GRADLE_USER_HOME`, `go: GOMODCACHE`. Several worktrees use a store at the same time, so name only tools made for that (pnpm's store and Go's module cache are; a plain `node_modules` is not). A store is a cache: deleting it only costs the next install. Names of variables that start with `BARNRAISE_` are refused.

**`strip`** empties the worktrees that only wait. When an item has been `waiting_owner` or `blocked` for `strip_after`, the listed directories leave its worktree; the branch, the commits and every other file stay. When the item moves again, the worktree is seeded and set up again before anything runs there. An item with a session running is never touched. List only what `setup` (or a build) puts back.

Paths in `seed` and `strip` are relative to the repository root and stay inside it; `.git` and Barnraise's own folders are refused.

## stale_reads

When main moves (an item of this machine or of another one lands), Barnraise compares what main changed with what each item in progress on this machine has seen: the files its agent sessions read or changed (taken from the session's tool calls, kept in the local store) and the files its branch changes. When they overlap, the agent gets a message with the files and a short summary of each change (`+12 -3 (func Load)`), between two tool calls or at the start of its next step, and the event `item.stale_read` is written. Searches (`Grep`, `Glob`) do not count as reads, and `plan.md`, `criteria.json` and the changelog are never reported.

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Track the files sessions read and warn when main changes them |
| `rebase` | `false` | Also rebase the item's branch on the new main at once, when that applies without a conflict (pending changes are committed first). A conflict leaves the branch as it was, and the message says so; the merge queue rebases at integration anyway |
| `max_files` | `10` | Most files listed in one warning; the rest are counted |

```yaml
stale_reads: { rebase: true }
```

## lessons

When an item's work is sent back (a failed check, a review asking for rework, the merge queue, or the owner's `barnraise item redo … --why`), Barnraise remembers the reason and the item's commit at that moment. When the step that sent it back passes again (or the item lands), it writes a lesson without any model: the reason, the files the fix changed, the areas and the checks it concerns (event `lesson.learned`). A lesson that says the same as one already known (same step and checks, close wording, same places) is merged into it and counted (`lesson.merged`). Implementer steps of later items get the lessons about the files they are anchored to, touch or read, and their areas, the most relevant first. `barnraise lesson list` shows them and `barnraise lesson delete <id>` removes one (owner only).

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Learn lessons from resolved rework and give them to later items |
| `max` | `5` | Most lessons in one prompt (0 to 20; 0 keeps learning without giving them) |

## Machine configuration (`daemon.yaml`)

The machine's own configuration is not part of any repository. It lives in `~/.config/barnraise/daemon.yaml` (mode 0600) and is written by `barnraise up`, `barnraise machine invite/join/leave` and `barnraise machine network`; people do not edit it by hand, except for `network_plugin.options`. The Tailscale auth key is never in it (see [operations.md](operations.md#machines-on-a-tailnet)).

| Key | Default | Meaning |
| --- | --- | --- |
| `machine` | the short hostname | Machine name (`barnraise daemon --machine`) |
| `projects` | none | Repository roots this daemon drives (`barnraise up` adds the current one) |
| `listen` | `127.0.0.1:7420` | TCP address of the API (`--listen`); `off` disables TCP; non-loopback needs `--tls-cert` and `--tls-key` |
| `nats_host` | `127.0.0.1` | NATS address; with peers it moves to the Tailscale address (or the address the network plugin reports) or `0.0.0.0`. With the embedded Tailscale it stays on loopback and peers come through the tailnet device |
| `nats_port` | `4222` | NATS port (`--nats-port`) |
| `nkey_seed`, `nkey_public` | generated | This machine's NATS key |
| `peers` | none | Paired machines: `name`, `url`, `public_key`, `seed`, `added` |
| `network` | auto | `tailscale` (the installed command), `tsnet` (the embedded Tailscale), `plugin` (the network plugin below), `lan` (no tailnet), or empty: the installed command, then the embedded one when it has an auth key or logged in before, then `lan` (never `plugin`). Set with `barnraise machine network` |
| `tsnet.control_url` | Tailscale's | Coordination server of the embedded Tailscale; a Headscale URL self-hosts it (`barnraise machine network tsnet --control-url <url>`) |
| `tsnet.tags` | `[tag:barnraise]` | Tags the embedded device advertises |
| `network_plugin.command` | none | The [network plugin](plugins.md#network) of `network: plugin`: a name on the `PATH`, an absolute path or `~/…` (`--command`; a relative path is made absolute) |
| `network_plugin.args` | none | Its arguments (`--arg`, repeated) |
| `network_plugin.env` | none | **Names** of environment variables passed to it (`--env`) |
| `network_plugin.secrets` | none | Variable name to secret reference, `keychain:<key>` or `env:<NAME>`, never a value (`--secret NAME=keychain:<key>`) |
| `network_plugin.timeout` | `10s` | How long each `network.status` may take (`--timeout`) |
| `network_plugin.options` | none | The plugin's own settings, sent in `initialize` (edit `daemon.yaml`) |
| `notify` | none | Machine-level notification channels (`type`, `target`, `secret`) |
