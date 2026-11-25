# Changelog

All notable changes to Barnraise are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [semantic versioning](https://semver.org/). Release notes on GitHub are generated from conventional commits.

## [Unreleased]

### Added

- **tracker:** `tracker.reconcile: true` repairs the tracker every 2 minutes: an item without an issue gets one (or is linked back to the `[I-n]` issue with the `barnraise` label already there, never a duplicate), and an issue that shows a stale state or status is updated. One machine does it at a time (`lease/tracker`); a machine that cannot write gives the duty up for 15 minutes so another one takes it.
- **owner:** *Run the checks* in a conversation with the advisor: Barnraise runs the project's checks (the commands of `barnraise.yaml`) on a clean checkout of main, in the same sandbox as an item's checks, and posts the result into the conversation for the advisor to read (`RunChecksInChat`). The advisor still never runs a command; it asks the owner to press the button instead of saying it cannot confirm.
- **owner:** *New item* opens a conversation with the advisor: it reads the code, answers and asks what is unclear, and *Create the item* writes the item from the conversation, with criteria tied to checks (`CreateItemFromChat`). The form stays behind *Write it by hand*.
- **owner:** *Set goal* and *Set focus* open a conversation with the advisor (threads `CG-…` and `CF-…`): it reads the code and the items not done, pushes back on a goal with no end you can check, and *Review the goal* / *Review the focus* drafts it (`DraftFromChat`) into the form, filled in for the owner to set. Nothing is set without the owner; the forms stay behind *Write it by hand* and in the command palette.
- **panel:** The item's *Links* open its pages outside Barnraise: the issue in the tracker (GitHub, GitLab, Jira; Linear through the link the tracker gives), the pull request, the branch, its changes against the main branch and, once integrated, its commit. The links come from the repository's remote (`ItemLinks`); a remote's credentials never reach them.
- **panel:** The autopilot shows what it decides right now: on the question, on the item waiting for approval, on the proposals while it triages them, and on Home.

### Fixed

- **tracker:** A machine whose token cannot write to the tracker no longer leaves items in limbo silently: its heartbeat carries the error and `barnraise status` says *machine X cannot write to the tracker*, and items left without an issue are listed. Before, an update that failed twice was only a warning in that machine's log, and its issues kept a stale status for good.
- **panel:** Once the advisor answered, a conversation about the goal or the focus shows *Review the goal* / *Review the focus* where the eye is, under the last answer, as the main button; it was a small grey button by *Send* that owners did not find.
- **panel:** *Set goal* and *Set focus* with every project on screen ask which project first; they opened the conversation in whichever project was the default.
- **panel:** The goal form's *What it needs* says the forms the daemon takes (`screens <folder>`, `files <path>`, `lines <file>`) instead of "one per line", which always failed.
- **cli:** `barnraise item edit -c` writes the criteria again; it failed with "proto: invalid type", so analysts could not record a refinement.
- **providers:** Claude Code's current limit messages ("You've hit your 5-hour limit · resets 1am · out of usage credits", "…weekly limit", "…Fable limit") are recognized: the account (or only the model, when the limit is the model's own) waits, and the work goes to another account or to the next model of its class.
- **owner:** The advisor goes down the ladder (fable, opus, sonnet, haiku) over every account with room before it gives up, never takes a limit message for an answer, and the panel disables *Talk it over*, *Decide for me* and *Triage* with the reason and the time it comes back. *Decide for me* on a decision only the owner can take leaves it to the owner with what to do instead of failing.
- **machines:** Sessions, the advisor and checks write their temporary files in `/var/tmp/barnraise-<uid>` (the `TMPDIR` the daemon gives them, on the system disk; `<data dir>/tmp` without `/var/tmp`), never in a small `/tmp`: a full `/tmp` had broken every command of every session on a machine. Each machine removes temporary leftovers untouched for a day (its own and Claude Code's `/tmp/claude-<uid>`), and holds new sessions while less than 1 GB is free there.
- **owner:** The autopilot no longer gets stuck on an advisor that fails: the failure is said once in the conversation with its cause, the decision waits (5, 10, 20 minutes… two hours) while the others go on, and after three failures in a row the machine hands the autopilot (now a duty of its own) to another machine. A failed request to the advisor is no longer kept.

### Added

- **machines:** `barnraise machine share --machine <name>` (and **Work here on** in each machine of the panel) has another machine clone the project's repository and register it, so its agents work on it too.
- **machines:** `barnraise machine share --token-stdin` hands the other machines a token for the repository when their own git credential does not reach it; each keeps it in its keychain for that repository only, and git reads it through `barnraise git-credential`.
- **scheduling:** `scheduling.shared_files` in barnraise.yaml lists the files several items may change at the same time (a central scene or script), so they no longer run one at a time; the merge queue settles the shared changes.
- **tools:** Godot 4.7.2 joins the tools Barnraise installs (`barnraise tools install godot --machine <name>`), at a pinned version checked against the official checksums.
- **owner:** A new question, proposal or approval wakes the autopilot at the next housekeeping instead of after its interval.

## [0.1.1] - 2026-11-24

### Added

- **owner:** Autopilot (`barnraise autopilot on|off [--all]`, the switch at the top of the panel): the advisor answers the questions agents ask, approves or sends back the plans and deliveries that wait, and decides the proposals, in every project, each with its reasons. What only the owner can do (an account, money, a device, an access, a personal choice) is left to the owner with what to do, and not asked again until it changes.

### Fixed

- **cluster:** A build made after a release (`0.1.0-1-gebe2f36`) is newer than the release: a machine running it no longer replaces its own binary with a peer's older one.

## [0.1.0] - 2026-11-24

First version: one Go binary that is the daemon, the CLI, the MCP server and the provider hooks.

### Added

- **cli:** Flag missing plugin programs in the up readiness check (I-1)
- **accounts:** Log one account in on several machines at the same time, from the panel, the desktop app or `barnraise account login <claude|codex|id> --all`: every machine runs the provider's own login and keeps it, and one screen shows each machine's link and code. An account lists the machines where it is logged in, and only those use it.
- **machines:** Each machine says how many agents it runs at once and why (its cores or its memory), and the owner sets the number from the panel or with `barnraise machine agents <n|auto> --machine <name>` (`max_agents` in daemon.yaml). The sidebar sums the cluster up and lists the busiest machines first.
- **panel:** Parts show the question each one waits on, answerable there; Capabilities lead with what needs the owner and the one thing to do; gitleaks installs with one click.
- **panel:** Confirm, edit or question the answer the advisor suggests; let it decide a question or a proposal for you; talk a proposal over; triage every open proposal at once and apply the recommendations you keep.
- **cli:** `barnraise panel` opens the web panel in the browser, already signed in; `--print` writes the address.
- **docs:** The README draws the owner's decisions, accounts on every machine, the parts of a repository, the security layers and the inside of the binary (`scripts/diagrams/build.py`), and the topology and GitHub diagrams say what runs today.
- **New panel and desktop app** ([ADR-0045](docs/adr/0045-panel-and-desktop-app.md)): the panel served by the daemon at `/` is rebuilt in Svelte with shadcn-svelte (`web/`): Home with the decisions waiting for the owner, the cluster with spend by model, machine, account, role and day, a board with the real state of each item, items with their flow, accounts with pace and projection, machines with two-hour charts and the standby switch, a command palette (Ctrl+K) and keyboard shortcuts, English and Brazilian Portuguese, light and dark. A desktop app (`desktop/`, Wails) finds or starts this machine's daemon, or reaches another one, and opens the same panel; the agents never depend on it.
- The overview says, for each item, its flow, what the owner approves now, its board column and the signal to read first; accounts carry their outlook and metrics name the numbers past their limit, so no client decides them. `Overview` adds the projects' usage up.
- The version is shown in the panel, which says when a machine of the cluster runs a newer build and asks to reload when the daemon was updated under the page. Builds compare by release, then by commit time: a development build is no longer replaced by a peer's older commit.
- **Standby** (`barnraise standby on|off [why]`, API `SetStandby`): a machine's own switch. The daemon keeps answering (status, panel, questions, events between machines) and starts no agent, check or merge there, in any project; it gives up the housekeeping lease, refuses checks from other machines and is never chosen for offloaded work. It is kept in `daemon.yaml`, so it holds across restarts and can be set with the daemon stopped: a service that starts at login no longer spends tokens before the owner can stop it. Like `down`, it asks whether to switch the connected machines too. `barnraise status` says STANDBY.
- `barnraise pause --all` / `resume --all` reach every project of the daemon.
- **Overview of the cluster** (`barnraise overview`, API `Overview`): every item on every machine in one answer — what it is doing now (working, running a check, idle, stalled, waiting for the owner, paused…), which machine holds it, its branch, pull request and tracker entry, its worktrees on each machine with size, the processes that run for it (agent, tests, browsers, builds) with processor and memory, and what it cost in tokens, money, agent time and processor time, split by step, model, machine, role, account and day. Each machine reports its own worktrees and processes on a timer; the answer comes from memory and does not grow with the history ([docs/api.md](docs/api.md#overview)).
- **Gemini CLI provider** (`gemini`, family `google`): `barnraise account add gemini` signs in with a Google account, and model choices are written `gemini/<model>`, with the ladder `gemini-2.5-flash`, `gemini-2.5-pro`. Its natural role is cheap review and reading long context. On the machine itself a Gemini session cannot run commands or change files. With `container:` it can.
- **Key-based providers**: `barnraise account add openrouter` keeps an OpenRouter API key in the keychain, and model classes take `openrouter/<vendor>/<model>` (e.g. `openrouter/deepseek/deepseek-chat`) for cheap steps and research when the owner wants. Sessions run the Codex CLI against the endpoint, with the key passed only by environment variable; other endpoints that speak the OpenAI Responses API go under `providers:` in `barnraise.yaml`. API accounts have no 5-hour or weekly window: the spending limit is set on the key, and a 402 or 429 marks the account limited.
- **External plugins**: trackers, checks, notification channels and event hooks can be separate programs, in any language, that speak JSON-RPC 2.0 over their standard input and output ([docs/plugins.md](docs/plugins.md)). They are declared under `plugins:` in `barnraise.yaml` (`tracker: {type: plugin:<name>}`, `runs_on: plugin:<name>`, `type: plugin:<name>` in `notifications`, `events:` for hooks), get only the environment variables and secrets they list, are restarted with backoff when they crash and stopped when a call outlives its timeout. Agents cannot add or change them: every `barnraise.yaml` of the repository and the plugin programs kept in it are denied to agent writes.
- **Embedded Tailscale**: release binaries (`-tags tsnet`, `make build-tsnet`) join the tailnet as their own device with tsnet, named after the machine and tagged `tag:barnraise`, so a machine needs nothing but `barnraise`; NATS and pairing are served on the device, `machine join <code>` finds the inviting machine among the tailnet's Barnraise machines, `machine network` chooses `tailscale`, `tsnet` or `lan` and a Headscale control URL, and `machine network-key` keeps the auth key in the keychain. The installed `tailscale` command still works, and `make build` stays without tsnet.
- **Network plugins**: any VPN or overlay (ZeroTier, Nebula, a Headscale client, plain WireGuard, a company VPN) tells Barnraise where the machines are through a plugin of kind `network` that answers `network.status` with this machine's addresses and the peers' names, addresses, online state and tags ([docs/plugins.md](docs/plugins.md#network)). It is set per machine with `network: plugin` and `network_plugin:` in `daemon.yaml` (`barnraise machine network plugin --command <program>`), runs with the plugin host's handshake, timeouts, environment isolation and secret references, and leaves the daemon on the local network when it fails. `machine join` without `--addr` finds the online `tag:barnraise` peers it reports. Examples: a static file for WireGuard or a LAN, and a ZeroTier sketch.
- **Standard commits and changelog**: every item lands as one Conventional Commits commit and, for features, fixes and performance, one line under `## [Unreleased]` of a Keep a Changelog file, both written from the delivery's `change` and always in English. A description that is not English is refused.
- `worktree.setup` runs once in each new worktree; `worktree.include` is empty by default, so files that are not in git (a `.env`) are no longer copied into agents' worktrees.
- An issue a person wrote in the tracker is adopted: its state and labels follow the item, its title and text are never rewritten.
- `barnraise up --paused` registers a project with every agent paused, so existing work is adopted (accounts, `item edit --branch`) before anything starts; `barnraise resume` lifts it.
- `barnraise up` checks the machine first: the arbiter remote exists and accepts a push from here, a GitHub token is reachable, and each local check's program is on the `PATH`. A machine that is not ready starts the project paused, with one sentence per problem.
- The questions Barnraise asks the owner (a blocked step, rework exhausted, an approval, a conflict, the next goal) come out in the project's `language:` (Brazilian Portuguese and English for now), and each option carries its action, so an answer in any language does what it says.
- The owner's answer reaches the item even without a note, and every step's prompt lists the owner's decisions about the item: on Nobira a second agent asked the same question again because a bare choice was never passed on.
- The policy hook resolves relative paths from the folder the agent is in, like the shell (`../httpapi/*.go` from a subfolder was refused as a secret), and lets agents in a container write scratch files to its own `/tmp`.
- A draft whose refine could not start (no account with capacity) is refined again later. It used to stay marked as refined at its version and wait for a person to edit it.
- `account add --isolate` refuses an account folder the isolated user could not reach (the CLI's own, or one under the owner's home, where the data dir and the worktrees live by default) and points to a container instead.
- The policy hook no longer denies agents their own worktree. Worktrees live under Barnraise's data folder, which the absolute rules close (state, tokens); on Nobira's first run every read of a source file was "credentials and secrets" and every write "changing Barnraise itself". Inside the worktree only the worktree rules apply.
- Agent containers run hardened: no capabilities and `no-new-privileges`, a read-only image with scratch folders in memory, limits on processes and open files, no core dumps. `container.mounts` refuses the engine's socket (root on the machine, the escape ai-jail fixed in 1.16), system folders, a whole home and credential folders (`~/.ssh`, `~/.config`, `~/.aws`…), in `barnraise check` and at run time.
- Prints reach agents 540 px wide instead of 720: the model already scales images to ~1.15 MP, so 720 px saved 8.4% on Nobira's 23,154 prints and 540 px saves 43.5%. `barnraise image cost <folder>` measures a folder, full against reduced.
- Drafts are refined only by the machine that holds the housekeeping lease. Drafts have no claim, and every machine's analyst refined the same draft at once.
- The knowledge index shares the event store's writer lock and reindexes in batches of 200 files. A whole-repository reindex used to hold SQLite's write lock in one transaction, and event writes of the daemon failed with `SQLITE_BUSY` meanwhile.
- A tracker import that fails, in full or for some issues, keeps its window: the issues it could not take come in with the next import. Before, the window moved on and they were never imported unless someone touched them; the failures were not logged either.
- A check that is red on main too no longer passes as a flake: it verified nothing. The item is held for the owner with the reason and an urgent notification, and does not go back to an agent to change code that is not wrong.
- The systemd unit written by `barnraise up --service` keeps the `PATH` of the shell that ran it, so checks find tools in `~/go/bin`, `~/.local/bin` or nvm.

- **Machine readings**: load, available memory, processor temperature and each GPU's use, memory and temperature, reported with every heartbeat; shown by `barnraise cluster status` and the panel, with `barnraise machine history` and the `MachineHistory` call for charts. Live state now travels on a last-value channel (`BR_LIVE`): never stored, never replayed as history.
- **Checks move to idle machines**: a loaded machine hands an item's check to an idle machine of the cluster, which checks the commit out, runs it and cleans up; it falls back to running here when the other machine cannot. `offload: false` on a check opts out.
- Real GitHub: the draft pull request opens with the item's first commit, is closed (and its branch removed) when the squash lands, tracker updates are serialized, and a delivery that changes nothing is sent back instead of landing as an empty commit.

- **Account limits from the sessions themselves**: Claude Code reports the 5-hour and weekly windows after every request, and Barnraise records them, so headless sessions keep each account's real usage and reset times current without the status line.
- **Machine pressure**: no new session starts while the machine has less than 3 GB of memory available or no idle CPU; running sessions are never stopped for it.
- Containers get `/dev/kvm` (Android emulators), and Codex runs inside them without its nested sandbox; `scripts/smoke.sh codex` has Codex write and Claude Code review.

- **Sessions in containers**: `container:` in `barnraise.yaml` runs every agent session inside a container that sees only the item's worktree, the repository's `.git`, the agent's account and the daemon's socket; the machine's GPU is passed in by default (`gpu: auto`). `deploy/agent.Dockerfile` is a starting image.

- **Several parts in one repository**: each part (a backend, a web app, a mobile app) carries its own `barnraise.yaml`, found automatically; `barnraise init` detects the parts and writes the files. Checks run inside the part's folder and only for changes that touch it; a part may choose the flow of its items (`kinds`).
- **Check selection**: `selection.all_when` and `selection.none_when`, check `paths`, `covers` and `order`, fail-fast check steps, skipped checks recorded with the reason, `barnraise check --touched <file>` to preview, and a merge queue that only checks what the batch touches and never repeats checks on a tree already checked.

- **MCP by permission**: sessions load only Barnraise's MCP server and the servers the owner lists under `mcp:` in `barnraise.yaml`, per role; the account's own servers, plugins and personal settings stay out.
- `barnraise webhook secret|forget` for the secrets of incoming webhooks; `scripts/smoke.sh` (`make smoke`) from `init` to one integrated item with the fake provider or a real Claude account.
- A review that cannot run on a different model from the implementation tells the owner what to change instead of waiting in silence.

- **Daemon as the only authority** with an append-only event log in SQLite, the current state derived from events, and `item redo` from any step.
- **Projects** from a short `barnraise.yaml` merged over templates (`mobile-android`, `backend`, `web`, `content`, `blank`, and saved ones); `init` in conversation, `check`, `explain`, `template save`.
- **Goal, focus and capacity**: a goal with a measurable end and an inventory, a temporary focus with a preview of its effect, and the 80/15/5 split between goal, bugs and improvements, plus a weekly self-improvement lane.
- **Items** of 2 to 6 hours with EARS criteria tied to checks, anchors, priorities, `--risky` items with two parallel attempts, drafts refined by the analyst, tweaks that ride with the next item of their area, and proposals with a fixed shape, a daily limit, expiry and duplicate detection.
- **Flows as data**: agent, script, approval, check and merge-queue steps, rework rules with a limit and model escalation, then the owner; plan approval before code; reviews with two verdicts.
- **Branch and draft pull request per item**, commits per step with an item trailer, safety commits, and a merge queue with rebase, a new check, batches and bisection.
- **Providers by capability**: Claude Code and Codex through their CLIs and subscriptions, several accounts per provider with usage and reset times, model routing by class with fallbacks, a status line that records real limits, and a scripted fake provider for tests and demos.
- **Hooks**: `pre-tool` (local policy decisions), `prime`, `mail`, `post-tool`, `stop`; output filtering of noisy commands (`barnraise filter`); screenshots reduced for agents (`barnraise image reduce|crop`).
- **Security layers**: blocking policy for paths and commands, isolated Linux users per account, Claude Code's native sandbox when bubblewrap works, Codex in `workspace-write`, an egress allow-list proxy with `nftables` rules per agent user (`barnraise daemon --egress`, `barnraise egress proxy|rules`), sanitizing and fencing of untrusted text, a secret scan before every push, scoped tokens, an audit log.
- **Several machines**: git as the arbiter (`refs/barnraise/claims`, compare-and-swap, 30 s renewals, 2 min ttl on the local clock), NATS JetStream mirroring, pairing with a 6-digit code over CPace, Tailscale when installed, failover from the last pushed step, and cluster self-update to the newest version found on a peer.
- **API**: ConnectRPC (gRPC, gRPC-Web, JSON with snake_case names) from protobuf, OpenAPI at `/v1/openapi.yaml`, CloudEvents over SSE at `/v1/events`, HMAC-signed outgoing webhooks with retries, verified inbound webhooks (GitHub, GitLab, generic), owner notifications (ntfy, Telegram, Slack, email, webhook, desktop).
- **MCP server** with typed tools filtered by role.
- **Trackers**: local, GitHub Issues and Projects, GitLab, Jira and Linear, with `tracker connect` and `tracker migrate`.
- **Knowledge and the living plan**: short ids, `.barnraise/` scaffold, `context`, `find`, `get`, `related`, `why`, `history`, `rules`, revalidation with a cheap triage and a nightly sweep, and `eval` against integrated items.
- **Web panel** served by the daemon, in English and Portuguese.
- **Packaging**: release archives for linux and darwin (amd64, arm64) with checksums, SBOMs and keyless cosign signatures, `install.sh`, a container image, a systemd user service (`up --service`) and a system unit.

[0.1.1]: https://github.com/leandrodaf/barnraise/releases/tag/v0.1.1
[0.1.0]: https://github.com/leandrodaf/barnraise/releases/tag/v0.1.0
