# Operations

Barnraise runs the same on a laptop and on a server without a screen. This page covers installing, running it as a service or in a container, logging in to subscriptions on headless machines, where the data lives, backups and upgrades.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/leandrodaf/barnraise/main/install.sh | sh
```

[`install.sh`](../install.sh) detects the OS (Linux, macOS) and architecture (amd64, arm64), downloads the release archive from GitHub Releases, verifies its SHA-256 against `checksums.txt` and installs `barnraise` into `~/.local/bin` (or `/usr/local/bin` when run as root). Options:

| Variable | Meaning |
| --- | --- |
| `BARNRAISE_VERSION` | A tag such as `v0.1.0` (default: the latest release) |
| `BARNRAISE_INSTALL_DIR` | Install directory |

Release archives, `checksums.txt`, SBOMs and a keyless cosign signature of the checksums are published on each GitHub release. To verify the signature:

```sh
cosign verify-blob \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/leandrodaf/barnraise/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

Or build from source: `make build` (Go 1.27), or `make build-tsnet` for the release build with the embedded Tailscale.

## Running the daemon

| Way | Command |
| --- | --- |
| Background process | `barnraise up` (starts `barnraise daemon` in the background if it is not running; log in `~/.local/share/barnraise/daemon.log`) |
| systemd user service | `barnraise up --service` |
| Foreground | `barnraise daemon` |
| Stop | `barnraise down` (running steps are saved; items continue from their last step). With other machines connected it asks whether to stop Barnraise on them too; `--all` stops them without asking, `--here` stops only this machine. Without a terminal to answer, only this machine stops |
| Start paused | `barnraise up --paused`: register the project with every agent paused, adopt the work that already exists (accounts, `barnraise item edit I-7 --branch old/issue-7`), then `barnraise resume` |
| Standby | `barnraise standby on [why]`: the daemon keeps running (status, panel, questions, events between machines) but starts no agent, check or merge on this machine, in any project, gives up the housekeeping lease and takes no checks or offloaded work from other machines. Running sessions stop as with `pause`. Kept in `daemon.yaml` (`standby: true`), so it holds across restarts and can be set with the daemon stopped, before `barnraise up` or a service that starts at login. Like `down`, it asks whether to switch the connected machines too (`--all`, `--here`). `barnraise standby off` lifts it; `barnraise standby` shows it |
| Pause every project | `barnraise pause --all [why]` / `barnraise resume --all`: `pause` stops one project on every machine, `standby` stops every project on one machine |

`barnraise daemon` flags:

| Flag | Default | Meaning |
| --- | --- | --- |
| `--listen` | `127.0.0.1:7420` (from `daemon.yaml`) | TCP address of the API; `off` disables TCP |
| `--tls-cert`, `--tls-key` | none | Required for any non-loopback `--listen` |
| `--machine` | the short hostname | Machine name |
| `--nats-port` | `4222` | Embedded NATS port; a busy port falls back to a random local one (other machines then cannot reach this one) |
| `--egress` | none (`BARNRAISE_EGRESS`) | Run the allow-list proxy at this address (e.g. `127.0.0.1:7421`) and route agent traffic through it; see [security.md](security.md#5-network-restricted-per-agent-user) |
| `--no-agents` | off | Serve the API but start no agent sessions |
| `--fake-provider` | off | Scripted fake provider, for tests and demos |

### systemd user service

`barnraise up --service` writes `~/.config/systemd/user/barnraise.service` and runs `systemctl --user daemon-reload` and `systemctl --user enable --now barnraise.service`. The unit keeps the `PATH` of the shell that ran it: a user service otherwise starts with systemd's short `PATH`, and checks would not find `~/go/bin`, `~/.local/bin` or nvm. Run it again after installing new tools.

```ini
[Unit]
Description=Barnraise daemon
After=network-online.target

[Service]
ExecStart=/home/you/.local/bin/barnraise daemon
Restart=on-failure
RestartSec=5
Environment=DISABLE_AUTOUPDATER=1
Environment="PATH=/home/you/go/bin:/home/you/.local/bin:/usr/local/bin:/usr/bin:/bin"

[Install]
WantedBy=default.target
```

A user service stops when you log out unless lingering is on: `loginctl enable-linger $USER`. Logs: `journalctl --user -u barnraise -f`.

### systemd system service

For a dedicated server, [`deploy/systemd/barnraise.service`](../deploy/systemd/barnraise.service) runs the daemon as a dedicated `barnraise` user with its state in `/var/lib/barnraise`:

```sh
sudo useradd --system --create-home --home-dir /var/lib/barnraise --shell /usr/sbin/nologin barnraise
sudo install -m 0755 barnraise /usr/local/bin/barnraise
sudo install -m 0644 deploy/systemd/barnraise.service /etc/systemd/system/barnraise.service
sudo systemctl daemon-reload
sudo systemctl enable --now barnraise
```

Run the CLI as that user so it finds the same socket and owner token: `sudo -u barnraise -H barnraise status`, and register projects with `sudo -u barnraise -H sh -c 'cd /srv/repos/app && barnraise up'`. The repositories must be readable and writable by `barnraise`.

### Container image

The [`Dockerfile`](../Dockerfile) builds a static binary (`CGO_ENABLED=0`) and a `debian:stable-slim` runtime with `git` and CA certificates, running as the non-root user `barnraise` (uid 10001). All state goes to the `/data` volume:

| Variable | Value in the image |
| --- | --- |
| `BARNRAISE_DATA_DIR` | `/data/share` |
| `BARNRAISE_CONFIG_DIR` | `/data/config` |

The default command is `barnraise daemon`: the API on the unix socket `/data/share/barnraise.sock` and on `127.0.0.1:7420` inside the container. To reach the API from outside the container it must listen beyond loopback, and that requires TLS:

```sh
make docker                       # builds barnraise:dev

docker run -d --name barnraise \
  -v barnraise-data:/data \
  -v /srv/repos:/repos \
  -v /etc/barnraise/tls:/tls:ro \
  -p 7420:7420 -p 4222:4222 \
  barnraise:dev daemon --listen 0.0.0.0:7420 --tls-cert /tls/tls.crt --tls-key /tls/tls.key

docker exec -w /repos/app barnraise barnraise up        # register a repository
docker exec barnraise barnraise status
docker exec barnraise cat /data/config/owner.token      # the owner token, for API clients
```

Without TLS, keep the default command and talk to the daemon with `docker exec` or by mounting the socket. Port 4222 is NATS, for other machines of the cluster.

The image holds the daemon only. Agent sessions need the provider CLIs (`claude`, `codex`, `gemini`): either extend the image with them or let agents run on paired machines, with the container acting as one more machine of the cluster (state, API, merge queue, remote checks).

### Restricted network for agents

On machines with isolated agent users, start the daemon with the egress proxy and install the kernel rules once per agent user:

```sh
barnraise daemon --egress 127.0.0.1:7421
barnraise egress rules --user barnraise-claude-1                       # print the nftables rules
barnraise egress rules --user barnraise-claude-1 --allow-local-port 5554,5555 --apply   # with the emulator ports
```

## Servers without a browser

Subscriptions are logged in with the flow each provider offers for headless machines:

```sh
barnraise account add claude --headless   # runs `claude setup-token` for a long-lived token
barnraise account add codex --headless    # runs `codex login --device-auth`
barnraise account add gemini --headless   # starts `gemini` with NO_BROWSER=true: open the URL, paste the code, /quit
barnraise account relogin claude-1 --headless
```

Each account gets its own config dir (`CLAUDE_CONFIG_DIR`, `CODEX_HOME` or `GEMINI_CLI_HOME`) in `~/.local/share/barnraise/accounts/<id>`. To reuse a CLI that is already logged in, pass `--config-dir <dir>` (or `--config-dir default` for the CLI's own) and `--skip-login`. Other useful flags: `--isolate` (an isolated Linux user, see [security.md](security.md)), `--ceiling 80` (never pass 80% of the weekly window), `--plan Max`, `--machine <name>`.

Where no headless login exists, log in on a machine with a browser and copy that account's config dir to the server over a secure channel.

## Machines on a tailnet

Machines in different places reach each other over a Tailscale network (a tailnet), or any other VPN or overlay through a network plugin, without opening router ports. `network:` in `daemon.yaml` (`barnraise machine network [auto|tailscale|tsnet|plugin|lan]`) says how:

| Network | What happens |
| --- | --- |
| `tailscale` | The installed `tailscale` command. NATS listens on the machine's Tailscale address |
| `tsnet` | The embedded Tailscale: Barnraise itself is a device of the tailnet, named after the machine and advertising `tag:barnraise`. NATS stays on loopback and peers arrive through the device; the daemon also relays the device's pairing port (47900) to `machine invite` |
| `plugin` | A [network plugin](plugins.md#network) (`network_plugin:`) says where this machine and the others are on another overlay: ZeroTier, Nebula, a Headscale client, plain WireGuard, a company VPN. NATS listens on the address the plugin reports for this machine |
| `lan` | No tailnet: the local network or a direct address, with the same pairing code |
| auto (empty) | `tailscale` when the command is installed, else `tsnet` when it has an auth key or logged in before, else `lan`. Never `plugin`: that one is chosen explicitly |

`BARNRAISE_NO_TAILSCALE=1` turns every tailnet off, the network plugin included. The embedded Tailscale is in the release binaries and in `make build-tsnet`; plain `make build` leaves it out (about 13 MB lighter) and `network: tsnet` then fails with a message saying so.

The first time, the embedded device logs in with an auth key. Create one in the Tailscale admin console (or `headscale preauthkeys create`), tagged `tag:barnraise`, reusable when several machines share it, and give it to Barnraise:

```sh
barnraise machine network-key          # reads the key from standard input into the system keychain
# or, for one run or a service unit:
export BARNRAISE_TS_AUTHKEY=tskey-auth-...
barnraise machine network tsnet        # optional: auto picks it once there is a key
```

The key is read from `BARNRAISE_TS_AUTHKEY` (then `TS_AUTHKEY`), then the keychain entry `tailscale/authkey`; it is never written to `daemon.yaml`, passed on a command line or logged. After the first login the device's own keys in the state directory are enough, and the auth key may expire or be removed (`barnraise machine network-key --forget`). Without any key, the daemon logs a login URL and waits up to a minute.

Pairing over the tailnet:

```sh
# machine A
barnraise machine invite
# machine B, with only barnraise and an auth key
barnraise machine join 482913          # joins the tailnet, finds A among the tag:barnraise machines, joins the cluster
```

`machine join` without `--addr` tries the online `tag:barnraise` machines of the tailnet on port 47900, then invite beacons on the local network. When the daemon already holds the embedded device, `machine invite` listens on loopback and the daemon relays the tailnet's pairing port to it; `machine join` opens a short-lived ephemeral device (`<machine>-pairing`, needs the auth key) or falls back to the local network.

**Headscale.** `barnraise machine network tsnet --control-url https://headscale.example.com` points the embedded device at a self-hosted control server; nothing then goes to Tailscale's servers. The embedded device never uploads logs to Tailscale either way.

**Other networks.** Any overlay that gives the machines IP addresses works through a network plugin: a small program, in any language, that answers `network.status` with this machine's addresses and the peers' names, addresses, online state and tags ([plugins.md](plugins.md#network)). Barnraise carries no traffic through it; the overlay routes it.

```sh
# plain WireGuard or a LAN with fixed addresses: the machines in a file
cp docs/examples/plugins/network-static.py ~/bin/ && chmod +x ~/bin/network-static.py
printf '10.8.0.1 desk tag:barnraise\n10.8.0.2 laptop tag:barnraise\n' > ~/.config/barnraise/network-peers   # read from the config dir by default
barnraise machine network plugin --command ~/bin/network-static.py
barnraise machine network               # this machine's address and the peers the plugin reports
```

The plugin runs in the config dir as the owner, with only the environment variables and secrets `network_plugin` lists (secrets as `keychain:` or `env:` references). Pairing works the same: `machine join` without `--addr` tries the online `tag:barnraise` peers the plugin reports. A plugin that is missing, answers badly or too late leaves the daemon on the local network with a warning in its log; `barnraise machine network` shows why. A ZeroTier sketch is in [plugins.md](plugins.md#zerotier-sketch).

**ACL.** `barnraise cluster acl` prints a policy where only `tag:barnraise` reaches the Barnraise ports (NATS 4222 and pairing 47900) and owns nothing else; paste it into the tailnet's policy (Headscale: its policy file).

## Where the data lives

Nothing of this is in the repository.

| Path | Override | Holds |
| --- | --- | --- |
| `~/.config/barnraise/` | `BARNRAISE_CONFIG_DIR` | `owner.token` (0600), `daemon.yaml` (machine name, projects, NATS key, peers; 0600), `secrets/` (encrypted file store when there is no system keychain), `templates/` (from `template save`) |
| `~/.local/share/barnraise/` | `BARNRAISE_DATA_DIR` (or `$XDG_DATA_HOME/barnraise`) | see below |
| `barnraise.sock` in the data dir | `BARNRAISE_SOCKET` | The API socket (0600); falls back to `$XDG_RUNTIME_DIR/barnraise-<hash>.sock` when the path is too long |

Inside the data dir:

| Entry | Holds |
| --- | --- |
| `barnraise.db` (+ `-wal`, `-shm`) | SQLite: the event log, the derived state, tokens, audit, the knowledge index, and the files each item's agent sessions read or changed on this machine (`file_reads`, for the stale-read warning; dropped when the item ends) |
| `nats/` | JetStream storage: the event stream mirrored with other machines |
| `blobs/` | Object store: logs, screenshots, diffs and binaries by content hash |
| `cursors.json` | How far this machine has read each peer's stream |
| `tsnet/` (0700) | The embedded Tailscale's state: the device's private keys (`tailscaled.state`), `self.json` (its tailnet name and addresses, for the commands while the daemon holds the device) and a lock file. Treat it like `daemon.yaml`: whoever copies it can be this machine on the tailnet. `tsnet-pairing/` is a short-lived ephemeral device, removed after `machine join` |
| `worktrees/<project>/<item>/` | Item worktrees (with `.barnraise-session/` for the agent's settings, and `.barnraise-session/outputs/` for the full text of the outputs `barnraise filter` shortened) |
| `outputs/` | Full text of the outputs `barnraise filter` shortened outside agent sessions |
| `stores/<project>/<name>/` | Stores shared by the project's worktrees (`worktree.stores`) |
| `logs/<project>/` | Full logs of checks |
| `accounts/<id>/` | Provider config dirs of each account |
| `daemon.log`, `daemon.pid` | Log and pid of a background daemon |

Environment variables used by the CLI and the hooks: `BARNRAISE_URL`, `BARNRAISE_SOCKET`, `BARNRAISE_TOKEN`, `BARNRAISE_PROJECT`; inside agent sessions also `BARNRAISE_ITEM`, `BARNRAISE_STEP`, `BARNRAISE_ROLE`, `BARNRAISE_AGENT`, `BARNRAISE_POLICY`, and for the output filter `BARNRAISE_OUTFILTER_DELEGATE`, `BARNRAISE_OUTPUT_KEEP`, `BARNRAISE_OUTPUT_KEEP_BYTES` and `BARNRAISE_RUN` (the session's run id, which ties kept outputs to the step's token usage). `BARNRAISE_NO_TAILSCALE=1` turns Tailscale off (installed or embedded); `BARNRAISE_TS_AUTHKEY` is the embedded Tailscale's auth key; `BARNRAISE_NO_SELF_UPDATE=1` turns off the cluster self-update; `BARNRAISE_EGRESS` is the default of `daemon --egress`.

## Backups

What matters most is already off the machine: every step ends with a commit pushed to the item's branch, and with two or more machines the event log is mirrored on each of them. A backup protects the single-machine case and the machine's own identity.

- **State**: `barnraise.db`. It runs in WAL mode; take a consistent copy with `sqlite3 ~/.local/share/barnraise/barnraise.db ".backup /backup/barnraise.db"`, or stop the daemon (`barnraise down`) and copy the file together with `-wal` and `-shm`.
- **Identity and secrets**: `~/.config/barnraise/` (owner token, `daemon.yaml` with the NATS key and peers, the `secrets/` store). The file store's key and sealed data are both in `secrets/`; a backup that holds both can open it, so keep it encrypted.
- **Optional**: `blobs/` (logs and screenshots) and `accounts/` (provider logins; you can also log in again).
- **Not needed**: `worktrees/` (recreated from the item branches), `stores/` (caches), `nats/` (rebuilt from peers), logs.

**Desktop file indexers stay out.** Worktrees hold hundreds of thousands of files (dependencies, build output). At every start the daemon writes two empty marker files, `.trackerignore` and `.nomedia`, in the data directory: Tracker and LocalSearch (GNOME) skip a directory that holds one of them, with everything below it. This matters when the data directory sits in a place the indexer walks (`BARNRAISE_DATA_DIR` or `XDG_DATA_HOME` under Documents, or an indexer set to walk the whole home). `locate`'s nightly `updatedb` has no per-directory marker; add the data directory to `PRUNEPATHS` in `/etc/updatedb.conf` if it bothers.

**What nothing uses again is removed.** An item's worktree goes when the item is integrated or closed. Once an hour each machine also compares its own directories with what the items say and removes what was left behind:

| Directory | Removed when |
| --- | --- |
| `worktrees/<project>/<item>/` of an integrated or closed item | An hour after the item finished |
| `worktrees/<project>/<item>/` of an item this machine does not know | A day after it was last changed (the item may only be late in the event log) |
| `worktrees/<project>/_refine-<item>/` (the analyst's checkout of main) | The item left draft, or no longer exists |
| `worktrees/<project>/_remote-*`, `_main-*`, `repro/<project>/*` (scratch checkouts) | A day old: their owner removes them when it finishes, so these are leftovers of a crash |
| `.trash-*` | Always (see below) |

A directory with a session running in it is never touched, and neither is a directory whose name is not an item's. With `worktree.strip` (see [configuration.md](configuration.md#many-worktrees-one-set-of-dependencies)), the same pass empties the heavy directories of the items that wait.

**Removing is immediate, deleting is not.** A worktree with its dependencies holds hundreds of thousands of files. To remove one, Barnraise renames it to `.trash-<name>-<id>` beside it, tells git to forget it (the branch is free at once) and deletes the files in the background, so the merge queue never waits for a deletion. If the daemon stops in the middle, the hourly pass deletes what is left.

**Shared stores** (`worktree.stores`) live in `stores/<project>/<name>/`. They are caches: stop the daemon and delete one to start it over.

Restore: stop the daemon, put the files back with the same owner and modes (0700 directories, 0600 files), start it again.

## Upgrades

- **Barnraise itself.** Install the new release over the old binary (`install.sh` or your package) and restart: `systemctl --user restart barnraise`, or `barnraise down && barnraise up`. Running steps are saved and continue from their last step. In a cluster, a daemon that sees a peer on the same platform running a newer version fetches that binary by hash from the peer, verifies it, replaces its own executable and restarts, only while no agent session is running; set `BARNRAISE_NO_SELF_UPDATE=1` to opt out on a machine.
- **The AI CLIs.** Barnraise runs every session and the service with `DISABLE_AUTOUPDATER=1`, so a CLI never updates itself under a running fleet. Upgrade `claude`, `codex` and `gemini` on purpose, on one machine first; the provider adapters detect each CLI's version and probe its capabilities before using them. Keep `DISABLE_AUTOUPDATER=1` in your own shell profile on agent machines too.
- **The API.** The proto is checked with `buf breaking`; incompatible changes only come in a new API version, with deprecation notices first.

## Health

Before it lets agents work, `barnraise up` checks the machine it runs on, and starts the project paused, with one sentence per problem, when the machine is not ready:

- the arbiter remote (`code_host.remote`, `origin` by default) exists, and this machine can push to it (a dry-run push);
- a GitHub token is reachable, for a GitHub tracker or code host (the keychain reference, `GITHUB_TOKEN`, or `gh auth token`);
- the program each local check starts (`make` in `COMPOSE_PROJECT_NAME=x make verify`) is on the `PATH`.

A second machine is where this matters most: give it its own credential for the repository, limited to that repository, before it joins.

- `GET /healthz` (no token): `{"ok": true, "machine": …, "version": …, "projects": […]}`.
- `barnraise status`, `barnraise cluster status`, `barnraise machine list`, `barnraise account list`.
- `barnraise events -f` and the panel for the live stream; `barnraise audit` for who did what.

## Merge settings in the repository

When the daemon loads a project it adjusts the repository's local git settings, never a tracked file ([merge](configuration.md#recorded-resolutions-and-structural-merge)):

- `rerere.enabled` and `rerere.autoupdate` in `.git/config`, unless you set them yourself; `barnraise.rerere = true` marks that Barnraise added them, and `merge.rerere: false` removes them. They also apply to your own checkout of the repository. Recorded resolutions are in `.git/rr-cache`; `git rerere forget <file>` during a conflicted rebase drops a bad one, and deleting the folder drops them all.
- With `mergiraf` on the daemon's `PATH`: the `merge.barnraise-structural` section in `.git/config` (its absolute path, so agents with a different `PATH` use it too) and a managed block in `.git/info/attributes`. Install or remove mergiraf, then restart the daemon or touch `barnraise.yaml` to reload; the log says `structural merge driver in use` with the number of patterns. To stop using it for the project, set `merge.structural: off`. mergiraf is GPL-3.0 software you install yourself (for example `cargo install --locked mergiraf`, or your package manager); Barnraise only runs it. git 2.44 or newer is recommended for it.
- `merge.conflicts.reapplied` in `barnraise events` and the item's timeline says when a conflict was resolved from a recorded resolution; the merge queue then runs every local check on the result.

## ACP agents

A provider of [`type: acp`](configuration.md#agent-programs-type-acp) runs any agent program that speaks the Agent Client Protocol. To add one:

1. Install the agent program on each machine that should run it (or in the project's container image), and log in to it once by hand, as the user the sessions run as.
2. Declare it under `providers:` in `barnraise.yaml` and name it in a model class (`myagent/default`).
3. `barnraise account add myagent` in the repository adds an account to the rotation.

Each session writes its whole conversation with the agent, one JSON line per message in either direction, to `.barnraise-session/acp-<name>.jsonl` in the item's worktree. That file is the first place to look when an agent misbehaves. It is capped at 64 MB and is never committed. The errors that end a session are reported in the session's state (`barnraise status`, the panel):

- `the agent needs a login`: the agent answered `auth_required`. Log in by hand as that user.
- `the agent speaks protocol version N`: Barnraise speaks version 1 of the protocol. Update the agent, or check its flag for the stable protocol.
- `the agent program ended (exit code N) before answering …`: the program crashed or is not an ACP agent. The last lines of its stderr follow.
- a refusal that says it hit a limit marks the account limited. It is tried again later, like any other account.

Barnraise asks for the model of the session when the agent lists it and ignores it otherwise. Policy refusals show up as `policy.violation` events (`barnraise events`, `barnraise audit`). Terminals run in bubblewrap when the probe succeeds on the machine. Where it does not (AppArmor forbids unprivileged namespaces, or macOS), they run after the policy check, as the session's user, the way Claude Code's commands run when its sandbox is off.

## Capacity

A machine runs as many sessions as its CPUs and total memory allow (`barnraise cluster status` shows `busy n/slots`). On top of that, **no new session starts while the machine has less than 3 GB of memory available, or no idle CPU**: the builds, tests and emulators of the items in progress keep the memory instead of being killed by the kernel. Sessions already running are never stopped for it, and work resumes by itself when the pressure passes. The daemon logs the reason once.

Each account's usage of its 5-hour and weekly windows comes from the sessions themselves (Claude Code reports it after every request) and from the status line of interactive sessions. `barnraise account list` shows the numbers and the reset times; an account at its ceiling is skipped until its window resets.

## Main moving under an item

When main moves, every item in progress on a machine is compared with it: the files its sessions read or changed there and the files its branch changes, against what main changed since the item last looked. The agent of an item that overlaps gets a message listing those files with a short summary of each change (`api/wallet.go: +12 -3 (func Load)`), delivered between two tool calls by the hooks or at the start of its next step, and `item.stale_read` goes to the event log (and the webhooks). Nothing is sent when the overlap is empty, and each move of main is reported once per item. With `stale_reads.rebase: true` the branch is also rebased on the new main right away when that applies cleanly; otherwise the merge queue rebases it when it lands, as before. Integrations made by another machine count too: the machine running the item fetches and compares when the `item.integrated` event arrives. See [stale_reads](configuration.md#stale_reads).

## Lessons from rework

Every resolved rework round (a failed check, a review asking for rework, a merge-queue failure, an owner's `barnraise item redo <id> <step> --why "…"`) leaves a short lesson: why the work came back and which files the fix changed, tagged by area and check. Similar lessons are merged and counted instead of piling up. Implementer prompts of later items that touch the same files, folders or areas get the most relevant ones (at most `lessons.max`). Lessons are events, so every machine of the cluster has them.

```sh
barnraise lesson list                       # the most taught first
barnraise lesson list --file api/wallet.go   # about a file or its folder
barnraise lesson list --area wallet --json
barnraise lesson delete L-3fa9c1d2          # owner only; audited
```

The same is reachable over HTTP with a token: `GET /v1/lessons?project=…&file=…&area=…` and `DELETE /v1/lessons/{id}?project=…`. See [lessons](configuration.md#lessons).

## Tokens of prints

Agents read screenshots through the `pre-tool` hook, which hands them a copy 540 px wide (`barnraise image crop` gives a full-resolution region when a detail matters). The model already scales big images down to about 1.15 megapixels, so a narrower image is the only thing that saves: a 1080x2400 phone print costs ~1,530 tokens in full, almost the same at 720 px, and ~860 at 540 px.

`barnraise image cost <folder>` measures a folder of prints, full against reduced. On Nobira's 23,154 prints of Nov 2026 (704 items, median 10 prints per item, mean 31, 90th percentile 88):

| Width | Tokens per print | Saved |
| --- | --- | --- |
| Full resolution | 1,279 | |
| 720 px (the old default) | 1,172 | 8.4% |
| 540 px (default) | 721 | 43.5% |

A mobile item with the mean of 31 prints goes from ~40,000 to ~22,500 tokens of images; one at the 90th percentile, from ~113,000 to ~63,000.

## Output filter

The `pre-tool` hook rewrites an agent's simple shell command (no pipes, redirections or `;`) into `barnraise filter -- <command>` when Barnraise has a native filter for it. The filter runs the command, prints what matters and keeps the rest:

| Commands | What the agent sees |
| --- | --- |
| `go test/build/vet`, Gradle, jest, vitest, `npm/pnpm/yarn test`, pytest, Maestro | Failures with their messages, panics, race reports; passing packages and tests counted |
| `cargo`, `mvn`/`mvnw`, `make`, `tsc`, `golangci-lint`, `eslint` | Errors (deduplicated, with their location), the final summary; compile progress, downloads and warnings counted (eslint: warnings by rule) |
| `npm/pnpm/yarn install`, `npm ci`, `pip install`, `uv sync`, `bundle install` | Errors, what was added, vulnerabilities; deprecation and progress lines counted |
| `git status` | Branch state and files grouped (staged, not staged, untracked, conflicts), long groups capped and untracked folders folded |
| `git diff`, `git show` | Commit header, a `+added -removed` line per file, the first lines of each file's changes; lock and generated files only in the stat |
| `git log` | One line per commit: hash, subject, author, date |
| `git push/pull/fetch/clone` | Everything but the transfer progress |
| `ls`, `tree`, `find`, `fd`, `docker ps/images`, `kubectl get` | The first entries; `find` also counts by folder |
| `grep`, `rg`, `ag`, `git grep` | Matches counted per file, with the first lines of the first files |
| `docker build`, `docker compose build` | The failing step and its error; step lines counted |
| `docker logs`, `kubectl logs`, `docker compose up` | Problem lines (deduplicated) and the last lines |
| `kubectl describe/apply/rollout` | Status fields, the last events; unchanged objects counted |

Repeated lines anywhere are collapsed into one with a count. Git, listings, searches and logs are printed unchanged when they are short (40 lines and 4 KB at most); so is any output whose summary would not be shorter.

**Nothing is lost.** A shortened output ends with `full output: barnraise output show <id> (<n> lines; …)`. The full text is kept in the item worktree's `.barnraise-session/outputs/` (outside a session, in the data folder's `outputs/`), at most 16 MB per output (the first and last 8 MB of a bigger one, with the size of the gap), removed after `outfilter.keep` (7 days) or when the folder passes `outfilter.keep_size` (64 MB), oldest first. To read it:

```sh
barnraise output show o1a2b3c4                 # all of it
barnraise output show o1a2b3c4 --grep 'FAIL|panic'   # matching lines, with their numbers
barnraise output show o1a2b3c4 --lines 120:180 # a range
barnraise output list                          # kept outputs, newest first
barnraise output stats                         # tokens saved, by tool
barnraise output list --dir ~/.local/share/barnraise/worktrees/<project>/<item>/.barnraise-session/outputs
```

`barnraise filter --raw -- <command>` prints the whole output (an agent's way out when the filter hides something it needs).

**Savings in the ledger.** Each kept output records the tokens of the whole output and of what was shown. At the end of a step the daemon adds those of the session to the step's `agent.usage` event (`filtered_runs`, `filter_saved_tokens`); `barnraise events --json` and the CloudEvents carry them, the item's timeline shows them (`… output filter saved 5400 tokens on 2 outputs`), and the digest (`barnraise digest`, the daily notification) has a line with the week's total. `barnraise output stats` gives the same numbers from the kept outputs of one folder, by tool.

**rtk.** When [rtk](https://github.com/rtk-ai/rtk) is installed, `outfilter.delegate: auto` (the default) also wraps simple commands that have no native filter and runs them as `rtk <command>`; `rtk` sends every simple command there, native ones included; `off` keeps rtk out. If rtk refuses a command it does not know (a usage error before anything ran), Barnraise runs the command itself. rtk runs the command itself too, so what Barnraise keeps of a delegated command is rtk's output, not the raw one: use `barnraise filter --raw -- <command>` when the raw text matters. Barnraise never installs rtk.

**Short answers.** Mechanical steps (model class `cheap` or `simple`, or a step pinned to the cheapest model of a ladder, like the critic's haiku) get an "Answer style" section asking for terse prose; see [terse](configuration.md#terse).

## Readings and moving checks between machines

Every machine reports, with each heartbeat, its 1-minute load, the memory it has available, the temperature of its processor and, for each GPU, how busy it is, its memory and its temperature (NVIDIA through `nvidia-smi`, AMD through the kernel's counters). A reading a machine cannot take is simply absent.

- `barnraise cluster status` shows the current readings of every machine; the panel shows them in each machine's box; `machine.updated` events on `GET /v1/events` carry them live.
- `barnraise machine history [machine] [--window 30m]` draws the recent past as small charts; with `--json` (or the `MachineHistory` call of the API) the samples come as numbers, ready to plot.

Readings are live state, not history. They travel on a separate channel that keeps only the latest value of each machine, in memory; they never enter the event log or the database, and a machine that joins later receives one value per machine, not the past. Each daemon keeps a few hours of samples in memory for charts (a few kilobytes per machine), gone on restart.

**A loaded machine hands its checks to an idle one.** When the machine that works on an item has 85% of its CPUs or of its memory in use (or less than 3 GB available), and another machine of the cluster on the same platform is alive, staying, at most half busy and at least 25 points less busy, the item's check runs there: that machine fetches the item's commit, checks it out in a scratch worktree, runs the command and removes the worktree. The result comes back to the item as usual, and the timeline says where the check ran and why. If the other machine cannot do it, the check runs here after all: moving work is an optimization, never a reason for a check to fail. Checks of the merge queue stay on the machine that integrates. `offload: false` on a check keeps it home.

