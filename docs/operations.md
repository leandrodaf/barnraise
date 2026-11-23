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

Or build from source: `make build` (Go 1.27).

## Running the daemon

| Way | Command |
| --- | --- |
| Background process | `barnraise up` (starts `barnraise daemon` in the background if it is not running; log in `~/.local/share/barnraise/daemon.log`) |
| systemd user service | `barnraise up --service` |
| Foreground | `barnraise daemon` |
| Stop | `barnraise down` (running steps are saved; items continue from their last step) |

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

`barnraise up --service` writes `~/.config/systemd/user/barnraise.service` and runs `systemctl --user daemon-reload` and `systemctl --user enable --now barnraise.service`:

```ini
[Unit]
Description=Barnraise daemon
After=network-online.target

[Service]
ExecStart=/home/you/.local/bin/barnraise daemon
Restart=on-failure
RestartSec=5
Environment=DISABLE_AUTOUPDATER=1

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

The image holds the daemon only. Agent sessions need the provider CLIs (`claude`, `codex`): either extend the image with them or let agents run on paired machines, with the container acting as one more machine of the cluster (state, API, merge queue, remote checks).

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
barnraise account relogin claude-1 --headless
```

Each account gets its own config dir (`CLAUDE_CONFIG_DIR` or `CODEX_HOME`) in `~/.local/share/barnraise/accounts/<id>`. To reuse a CLI that is already logged in, pass `--config-dir <dir>` (or `--config-dir default` for the CLI's own) and `--skip-login`. Other useful flags: `--isolate` (an isolated Linux user, see [security.md](security.md)), `--ceiling 80` (never pass 80% of the weekly window), `--plan Max`, `--machine <name>`.

Where no headless login exists, log in on a machine with a browser and copy that account's config dir to the server over a secure channel.

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
| `barnraise.db` (+ `-wal`, `-shm`) | SQLite: the event log, the derived state, tokens, audit, the knowledge index |
| `nats/` | JetStream storage: the event stream mirrored with other machines |
| `blobs/` | Object store: logs, screenshots, diffs and binaries by content hash |
| `cursors.json` | How far this machine has read each peer's stream |
| `worktrees/<project>/<item>/` | Item worktrees (with `.barnraise-session/` for the agent's settings) |
| `logs/<project>/` | Full logs of checks and filtered commands |
| `accounts/<id>/` | Provider config dirs of each account |
| `daemon.log`, `daemon.pid` | Log and pid of a background daemon |

Environment variables used by the CLI and the hooks: `BARNRAISE_URL`, `BARNRAISE_SOCKET`, `BARNRAISE_TOKEN`, `BARNRAISE_PROJECT`; inside agent sessions also `BARNRAISE_ITEM`, `BARNRAISE_STEP`, `BARNRAISE_ROLE`, `BARNRAISE_AGENT`, `BARNRAISE_POLICY`. `BARNRAISE_NO_TAILSCALE=1` ignores an installed Tailscale; `BARNRAISE_NO_SELF_UPDATE=1` turns off the cluster self-update; `BARNRAISE_EGRESS` is the default of `daemon --egress`.

## Backups

What matters most is already off the machine: every step ends with a commit pushed to the item's branch, and with two or more machines the event log is mirrored on each of them. A backup protects the single-machine case and the machine's own identity.

- **State**: `barnraise.db`. It runs in WAL mode; take a consistent copy with `sqlite3 ~/.local/share/barnraise/barnraise.db ".backup /backup/barnraise.db"`, or stop the daemon (`barnraise down`) and copy the file together with `-wal` and `-shm`.
- **Identity and secrets**: `~/.config/barnraise/` (owner token, `daemon.yaml` with the NATS key and peers, the `secrets/` store). The file store's key and sealed data are both in `secrets/`; a backup that holds both can open it, so keep it encrypted.
- **Optional**: `blobs/` (logs and screenshots) and `accounts/` (provider logins; you can also log in again).
- **Not needed**: `worktrees/` (recreated from the item branches), `nats/` (rebuilt from peers), logs.

Restore: stop the daemon, put the files back with the same owner and modes (0700 directories, 0600 files), start it again.

## Upgrades

- **Barnraise itself.** Install the new release over the old binary (`install.sh` or your package) and restart: `systemctl --user restart barnraise`, or `barnraise down && barnraise up`. Running steps are saved and continue from their last step. In a cluster, a daemon that sees a peer on the same platform running a newer version fetches that binary by hash from the peer, verifies it, replaces its own executable and restarts, only while no agent session is running; set `BARNRAISE_NO_SELF_UPDATE=1` to opt out on a machine.
- **The AI CLIs.** Barnraise runs every session and the service with `DISABLE_AUTOUPDATER=1`, so a CLI never updates itself under a running fleet. Upgrade `claude` and `codex` on purpose, on one machine first; the provider adapters detect each CLI's version and probe its capabilities before using them. Keep `DISABLE_AUTOUPDATER=1` in your own shell profile on agent machines too.
- **The API.** The proto is checked with `buf breaking`; incompatible changes only come in a new API version, with deprecation notices first.

## Health

- `GET /healthz` (no token): `{"ok": true, "machine": …, "version": …, "projects": […]}`.
- `barnraise status`, `barnraise cluster status`, `barnraise machine list`, `barnraise account list`.
- `barnraise events -f` and the panel for the live stream; `barnraise audit` for who did what.

## Capacity

A machine runs as many sessions as its CPUs and total memory allow (`barnraise cluster status` shows `busy n/slots`). On top of that, **no new session starts while the machine has less than 3 GB of memory available, or no idle CPU**: the builds, tests and emulators of the items in progress keep the memory instead of being killed by the kernel. Sessions already running are never stopped for it, and work resumes by itself when the pressure passes. The daemon logs the reason once.

Each account's usage of its 5-hour and weekly windows comes from the sessions themselves (Claude Code reports it after every request) and from the status line of interactive sessions. `barnraise account list` shows the numbers and the reset times; an account at its ceiling is skipped until its window resets.

