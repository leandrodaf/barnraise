# External plugins

Everything that changes from one project or tool to the next sits behind a fixed interface. The main implementations are compiled into the binary (GitHub, GitLab, Jira, Linear, ntfy, Telegram, Slack, email, local and remote checks). Anything else is an **external plugin**: a separate program, in any language, that the daemon starts and talks to over **JSON-RPC 2.0 on its standard input and output**. Adding a tool never requires rebuilding Barnraise.

This page is the protocol, version **1**. The keys of `barnraise.yaml` are in [configuration.md](configuration.md#plugins); a complete plugin in 60 lines of Python is [examples/plugins/notify-file.py](examples/plugins/notify-file.py), and a network plugin is [examples/plugins/network-static.py](examples/plugins/network-static.py).

| Kind | Used as | Methods |
| --- | --- | --- |
| `tracker` | `tracker: {type: plugin:<name>}` | `tracker.list`, `tracker.create`, `tracker.update`, `tracker.comment`, `tracker.columns` |
| `check` | a check with `runs_on: plugin:<name>` | `check.run` |
| `notifier` | a `notifications` entry with `type: plugin:<name>` | `notify.send` |
| `hook` | a plugin with `events:` | `hook.event` |
| `network` | `network: plugin` and `network_plugin:` in `daemon.yaml` (per machine, not per project) | `network.status` |

One program may implement several kinds; it says which in its answer to `initialize`.

## Transport

- The daemon runs `command` with `args`, in the project's repository root (a network plugin: in the config dir, `~/.config/barnraise/`), and keeps it running between calls. It starts with the first call, not with the daemon.
- **stdin** carries messages from the daemon to the plugin, **stdout** from the plugin to the daemon. Each message is one JSON object in UTF-8 followed by a newline (`\n`): newline-delimited JSON. A line is at most 16 MiB.
- **stderr** is for logs. Every line is copied into the daemon's log with the plugin's name. Nothing but protocol messages may be written to stdout.

**Why newline-delimited and not `Content-Length` framing (as in LSP).** A JSON encoder never writes a raw newline inside a message (newlines in strings are escaped as `\n`), so one line is one message and the framing costs nothing. Every language reads lines out of the box: `for line in sys.stdin` in Python, `read -r` in a shell, `bufio.Scanner` in Go, `readline` in Node.js. `Content-Length` headers need an exact byte count and a reader that switches between headers and a body, which is the part plugin authors get wrong. The cost of the choice is that a message must not be pretty-printed over several lines.

Every message has `"jsonrpc": "2.0"`. As in JSON-RPC 2.0:

- a **request** has `id`, `method` and `params`, and gets exactly one response with the same `id` and either `result` or `error`;
- a **notification** has `method` and `params` and no `id`, and gets no response.

The daemon's ids are integers. Requests may overlap: the daemon can send a new request before the previous one is answered, and matches responses by `id`, in any order. A plugin may answer them one by one; it just makes callers wait.

Batches (JSON arrays) are not used. In version 1 the daemon serves no methods: a request from the plugin is answered with `-32601`.

## Lifecycle

### initialize

The first request on every start. The plugin must answer within 10 seconds.

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{
  "protocol_version":1,"barnraise_version":"0.1.0","plugin":"youtrack",
  "project":"shop","root":"/home/me/shop","options":{"project":"SHOP"}}}
```

`options` is the plugin's `options` mapping from `barnraise.yaml`, as JSON. A network plugin gets `"project": ""`, the config dir as `root`, and the `options` of `network_plugin`.

```json
{"jsonrpc":"2.0","id":1,"result":{"name":"youtrack-barnraise","version":"1.2.0","protocol_version":1,"kinds":["tracker","notifier"]}}
```

| Field | Meaning |
| --- | --- |
| `name`, `version` | The plugin's own name and version, for the log |
| `protocol_version` | Must be `1`. Any other value stops the plugin with a sentence that names both versions |
| `kinds` | Non-empty subset of `tracker`, `check`, `notifier`, `hook`, `network`. A call for a kind not listed fails in the daemon without reaching the plugin: "plugin youtrack does not implement check (it declares: tracker, notifier)" |

A plugin that cannot work with its options (a missing URL, a token it rejects) answers `initialize` with an error; its message is what the owner reads.

### shutdown

When the daemon stops or `barnraise.yaml` changes the plugin's definition, the daemon sends `shutdown` (no params), waits up to 2 seconds for the answer, closes stdin and, if the program is still running 2 seconds later, kills its process group. A plugin should answer `shutdown` and exit on end of input.

### Restarts

If the program exits, all its pending calls fail with "plugin <name> stopped: <why>", and it starts again (with a new `initialize`) on the next call. Crashes in a row wait 0.5 s, 1 s, 2 s, … up to 30 s before the next start; a run that lasted a minute resets the wait. Plugins must therefore keep durable state elsewhere (the tracker itself, a file) and treat every start as fresh.

## Timeouts and cancellation

Each call has a deadline: the plugin's `timeout` (default 30 s), or for `check.run` the check's own `timeout` (default 30 min, also sent as `timeout_ms`). When it passes, or when the daemon no longer needs the answer, the daemon sends a notification:

```json
{"jsonrpc":"2.0","method":"$/cancel","params":{"id":7}}
```

The plugin should stop that work and may answer it with error `-32800`. After a deadline the plugin has 2 seconds more; a plugin that has not answered by then is killed (its whole process group) and starts again on the next call. When the daemon cancels for another reason (it is shutting down a step), the plugin keeps running and a late answer is ignored.

## Logging

Besides stderr, a plugin can send structured log lines:

```json
{"jsonrpc":"2.0","method":"$/log","params":{"level":"warn","message":"rate limited; retrying in 10s"}}
```

`level` is `debug`, `info`, `warn` or `error`. Never log secrets.

## Errors

A failed request answers `error` with `code` and `message`. `message` is shown to people (the owner, the item's timeline): write a plain sentence.

| Code | Meaning |
| --- | --- |
| `-32700` | Parse error: the plugin could not read the request |
| `-32600` | Invalid request |
| `-32601` | Method not found |
| `-32602` | Invalid params |
| `-32603` | Internal error |
| `-32800` | Cancelled (after `$/cancel`) |
| `-32001` | Not found: the reference names nothing in the tool |
| `-32002` | Unavailable: the tool is down or refuses the plugin's credentials; try later |

A line on stdout that is not a JSON-RPC 2.0 message is a protocol violation: the daemon logs it, fails the pending calls with "plugin <name> stopped: it broke the protocol: …" and stops the program, which starts again on the next call.

## Environment and secrets

The program gets only:

- `PATH`, `HOME`, `USER`, `LOGNAME`, `LANG`, `LC_ALL`, `TZ`, `TMPDIR` from the daemon;
- the variables whose **names** `env` lists, with the daemon's values;
- each `secrets` entry, resolved from its reference (`keychain:<key>` or `env:<NAME>`) when the program starts;
- `BARNRAISE_PLUGIN` (its name; `network` for the network plugin) and `BARNRAISE_PROJECT` (empty for the network plugin).

Secret values never appear in `barnraise.yaml`, in the protocol or on a command line, and the daemon's own tokens are not passed on. A secret that cannot be read stops the start with a sentence naming it.

## tracker

Where people see items. The daemon owns the flow state; the tracker shows it and brings back what people wrote.

An **item** sent to the plugin:

| Field | Meaning |
| --- | --- |
| `id` | Barnraise id (`I-42`) |
| `ref` | The tracker's reference returned by `tracker.create` (empty before) |
| `kind`, `priority` (`P0`…`P3`), `area` | As in Barnraise |
| `title` | Title with the id, as the built-in trackers show it |
| `body` | Markdown description in the project's language: problem, criteria, links, a footer |
| `status` | Barnraise status: `draft`, `ready`, `in_progress`, `waiting_owner`, `blocked`, `queued`, `integrated`, `closed`, `paused` |
| `column` | The tracker column for that status, from `tracker.columns` in `barnraise.yaml`; empty when not mapped |
| `labels` | kind, priority, area and `status:<status>` |
| `branch`, `pr` | When known |
| `adopted` | A person wrote this entry in the tracker: update its state and labels, never its title or text |
| `closed` | The status is final |

An **entry** returned by the plugin: `ref` (required), `number`, `title`, `body`, `status` (the tracker's column or state), `labels`, `author`, `url`, `updated` (RFC 3339), `closed`. Barnraise sanitizes `title` and `body` (HTML comments, invisible characters) and shows them to agents only as fenced, untrusted data.

| Method | Params | Result |
| --- | --- | --- |
| `tracker.list` | `{"since": "2026-11-23T10:00:00Z"}` (`""` for everything) | `{"items": [entry, …]}`: entries changed since then |
| `tracker.create` | `{"item": item, "lang": "en"}` | entry, with its `ref` |
| `tracker.update` | `{"item": item, "lang": "en"}` | `{}` |
| `tracker.comment` | `{"ref": "YT-12", "text": "Markdown"}` | `{}` |
| `tracker.columns` | `{}` | `{"columns": ["Todo", "Doing", "Done"]}` |

## check

| Method | Params | Result |
| --- | --- | --- |
| `check.run` | see below | see below |

Params: `check` (its name), `run` (the check's `run` string, free for the plugin to interpret), `item_id`, `branch`, `commit`, `dir` (the checkout to verify), `touched` and `areas` (what the change touches), `env` (for example `PORT`), `main` (true when it runs against main to tell a flaky check from a broken one), `timeout_ms`.

Result:

| Field | Meaning |
| --- | --- |
| `passed` | The check passed |
| `summary` | A short text for the agent: what failed, where |
| `log` | Optional full log; the daemon keeps it with the result |
| `artifacts` | Optional paths or URLs (reports, screenshots) |
| `flaky` | The plugin retried and saw it both pass and fail |
| `criteria` | Optional ids of the item's criteria it verified |
| `duration_ms` | Optional; measured by the daemon otherwise |
| `pending` | The check was only started (a device farm, a queue). The result is reported later through the API (`ReportCheck`, scope `checks:report`), with a token the owner gives the plugin in its `secrets` |

An error answer means the check **could not run**: it is recorded as a failed check whose summary starts with "check could not run:" and your message, so answer an error only when nothing was verified, and `passed: false` when the code is wrong.

## notifier

| Method | Params | Result |
| --- | --- | --- |
| `notify.send` | `title`, `body`, `urgent`, `link`, `options` (answers the owner may pick), `kind` (`question`, `alert`, `digest`, …), and the entry's `target` and `extra` | `{}` |

The daemon applies its own quiet policy before sending: what is not urgent goes in the daily digest.

## hook

A plugin with `events:` in `barnraise.yaml` receives each matching event of its project, as stored in the event log:

```json
{"jsonrpc":"2.0","id":9,"method":"hook.event","params":{"event":{
  "id":"01J…","type":"item.step.finished","project":"shop","subject":"I-42",
  "machine":"desk","actor":"agent","time":"2026-11-23T10:00:00Z","data":{…}}}}
```

Useful types: `item.step.started` and `item.step.finished` (before and after each step; the item in `item.step.finished` has already moved to its next step, and `data.extra.step` and `data.extra.outcome` name the step that ended and how), `item.updated`, `item.waiting_owner`, `item.blocked`, `item.queued`, `item.closed` (status changes), `item.integrated` (integration), `check.completed`, `owner.question.asked`. The full list is the `Ev…` constants in `internal/domain/event.go`. Answer `{}`.

Hooks observe: they are told what happened and cannot hold or change the flow. Events wait in a queue of 256 per plugin; when a plugin falls that far behind, new events are dropped with a warning in the log.

## network

How machines reach each other when the network is not Tailscale: ZeroTier, Nebula, a Headscale client, plain WireGuard, a company VPN, a LAN with fixed addresses. The overlay already routes its addresses for the whole machine, so the plugin carries no traffic: it only says where this machine and the others are. Barnraise then listens and dials with the system's network, as it does with the installed `tailscale`.

| Method | Params | Result |
| --- | --- | --- |
| `network.status` | `{}` | see below |

```json
{"jsonrpc":"2.0","id":2,"result":{
  "self":{"name":"desk","addresses":["10.147.17.1"],"tags":["tag:barnraise"]},
  "peers":[
    {"name":"laptop","addresses":["10.147.17.2","fd80::2"],"online":true,"tags":["tag:barnraise"]},
    {"name":"printer","addresses":["10.147.17.9"],"online":true,"tags":[]}]}}
```

| Field | Meaning |
| --- | --- |
| `self.name` | This machine's name on the network (required) |
| `self.addresses` | Its addresses on the network, IP literals, at least one. NATS listens on the first IPv4 (else the first address) once the machine has peers, never on every interface |
| `peers[].name`, `addresses` | Every other machine the network shows. Addresses are IP literals, not host names |
| `peers[].online` | It can be reached now. `machine join` without `--addr` tries only online peers |
| `tags` | `tag:barnraise` marks a machine that runs Barnraise, as the Tailscale tag does: `machine join` without `--addr` tries the online `tag:barnraise` peers on port 47900, then the local network. A network without tags can tag every machine it lists, or the ones a description, group or file marks |

An answer without an address for this machine, or with an address that is not an IP, is refused with a sentence naming it. The daemon asks once when it starts and again whenever it needs the addresses (pairing, the NATS address, `barnraise machine network`); answer from the overlay's own state, quickly, without side effects. When the plugin cannot start, answers with an error or not within its `timeout` (default 10 s for a network plugin), the network is unavailable: the daemon logs it and keeps running on the local network, as when the embedded Tailscale cannot log in, and pairing falls back to invite beacons and `--addr`.

It is configured per machine, in `daemon.yaml`, with the same keys and rules as a project's plugin (`command`, `args`, `env`, `secrets` as `keychain:`/`env:` references, `timeout`, `options`); `command` is a name on the `PATH`, an absolute path or `~/…`:

```sh
barnraise machine network plugin --command ~/bin/network-static.py
barnraise machine network plugin --command ~/bin/zerotier-network.py --env ZT_NETWORK --secret ZT_TOKEN=keychain:zerotier
barnraise machine network                # shows the plugin, this machine's address and the peers it reports
```

`--arg`, `--env` and `--secret` replace what is stored; `options` is set by editing `network_plugin.options` in `daemon.yaml` (see [configuration.md](configuration.md#machine-configuration-daemonyaml)). Restart the daemon after a change.

### Static file (WireGuard, a fixed LAN)

[examples/plugins/network-static.py](examples/plugins/network-static.py) reads the machines from a JSON file or hosts-like lines (`10.8.0.2 laptop tag:barnraise`) on every call, and probes a TCP port per machine when the file asks for it. Point it at the file with `options.file` in `daemon.yaml`; a relative file is read in the config dir.

### ZeroTier (sketch)

**An untested sketch**: it was written against the documented output of `zerotier-cli` and the ZeroTier Central API, not run against a live network. This machine's address comes from `zerotier-cli -j listnetworks`, the members and their names from Central (`ZT_TOKEN`, an API token kept in the keychain), and a member is online when `zerotier-cli -j listpeers` shows an active path to it. A member whose description in Central contains `tag:barnraise` carries the tag. `zerotier-cli` needs the service's auth token: for a daemon that does not run as root, copy `/var/lib/zerotier-one/authtoken.secret` to `~/.zeroTierOneAuthToken` (mode 0600).

```python
#!/usr/bin/env python3
# UNTESTED SKETCH of a ZeroTier network plugin for Barnraise.
# daemon.yaml: network: plugin
#              network_plugin: {command: ~/bin/zerotier-network.py, env: [ZT_NETWORK], secrets: {ZT_TOKEN: "keychain:zerotier"}}
import json, os, subprocess, sys, urllib.request

NET, TOKEN = os.environ["ZT_NETWORK"], os.environ["ZT_TOKEN"]

def zt(*args):
    return json.loads(subprocess.run(["zerotier-cli", "-j", *args], check=True, capture_output=True, text=True, timeout=5).stdout)

def status():
    me = zt("info")["address"]
    net = next(n for n in zt("listnetworks") if n["nwid"] == NET)
    active = {p["address"] for p in zt("listpeers") if any(x.get("active") for x in p.get("paths", []))}
    req = urllib.request.Request(f"https://api.zerotier.com/api/v1/network/{NET}/member", headers={"Authorization": "token " + TOKEN})
    members = json.load(urllib.request.urlopen(req, timeout=5))
    def tags(m):
        return ["tag:barnraise"] if "tag:barnraise" in (m.get("description") or "") else []
    mine = next((m for m in members if m["nodeId"] == me), {})
    peers = [{"name": m.get("name") or m["nodeId"], "addresses": m["config"].get("ipAssignments") or [],
              "online": m["nodeId"] in active, "tags": tags(m)}
             for m in members if m["nodeId"] != me and m["config"].get("authorized")]
    return {"self": {"name": mine.get("name") or me, "addresses": [a.split("/")[0] for a in net.get("assignedAddresses", [])], "tags": tags(mine)},
            "peers": peers}

def send(m):
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", **m}) + "\n"); sys.stdout.flush()

for line in sys.stdin:
    msg = json.loads(line)
    id, method = msg.get("id"), msg.get("method")
    if id is None:
        continue
    if method == "initialize":
        send({"id": id, "result": {"name": "zerotier-network", "version": "0.1.0", "protocol_version": 1, "kinds": ["network"]}})
    elif method == "network.status":
        try:
            send({"id": id, "result": status()})
        except Exception as e:  # zerotier-cli missing, network not joined, Central unreachable
            send({"id": id, "error": {"code": -32002, "message": f"zerotier: {e}"}})
    elif method == "shutdown":
        send({"id": id, "result": {}}); break
    else:
        send({"id": id, "error": {"code": -32601, "message": f"method not found: {method}"}})
```

Nebula, Netbird or a company VPN follow the same shape: this machine's address from the overlay's interface or command, the peers from its lighthouse, controller or a static list.

## OpenTelemetry (shipped)

Barnraise ships a hook plugin inside its own binary: `barnraise plugin otel` exports the work as [OpenTelemetry](https://opentelemetry.io) spans to any collector that takes OTLP over HTTP (the OpenTelemetry Collector, Grafana Tempo or Alloy, Jaeger, Honeycomb, Datadog, …). It is opt-in: nothing is exported until a project declares it.

```yaml
plugins:
  otel:
    command: barnraise
    args: [plugin, otel]
    events: ["item.*", agent.usage, check.completed]
    options:
      endpoint: http://localhost:4318        # the collector; /v1/traces is added
      service_name: barnraise
    secrets:
      OTEL_EXPORTER_OTLP_HEADERS: "keychain:otel-headers"   # e.g. "x-honeycomb-team=…" or "Authorization=Bearer%20…"
```

Each item is one trace, with ids derived from the project and the item, so its spans join up across restarts and machines:

| Span | From → to | Attributes |
| --- | --- | --- |
| `item <id>` (root) | creation → `item.integrated` or `item.closed` | `barnraise.item.kind`, `.priority`, `.area`, `.lane`, `.status`, `.rework` (rounds), `barnraise.commit` |
| `step <name>` | the step's start → `item.step.finished` | `barnraise.step`, `barnraise.outcome`, `barnraise.actor`; an error for `fail`, `reject`, `blocked` |
| `agent <role>` | one agent run of the ledger (`agent.usage`), under its step | `gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `barnraise.tokens.cache_read`, `.cache_write`, `barnraise.cost_usd`, `barnraise.duration_ms`, `barnraise.agent`, `.account`, `.role` |
| `check <name>` | one result (`check.completed`), under its step | `barnraise.check.passed`, `.runs_on`, `.flaky`, `.main_red`, `barnraise.commit`; an error when it failed, with its summary |

Every span also has `barnraise.project`, `barnraise.machine`, `barnraise.item.id` and `barnraise.event.id`; the resource has `service.name`, `service.version` and `barnraise.project`. Ledger entries without an item (the analyst's triage) are traces of their own. A step that was already running when the plugin started is closed with `barnraise.start_unknown: true`.

| Option | Default | Meaning |
| --- | --- | --- |
| `endpoint` | `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, else `OTEL_EXPORTER_OTLP_ENDPOINT`, else `http://localhost:4318` | The collector's OTLP/HTTP base URL (or the full `…/v1/traces` URL) |
| `headers` | none | Headers sent with every export; put credentials in the `OTEL_EXPORTER_OTLP_HEADERS` secret (`key=value` pairs, comma separated, values URL-encoded), not here |
| `service_name` | `barnraise` | `service.name` of the resource |
| `interval` | `5s` | How often waiting spans are sent |
| `batch` | `256` | Send at once when this many wait |
| `timeout` | `10s` | Per export request |

The spans are sent as OTLP/HTTP JSON, written with the standard library from the OTLP specification (no OpenTelemetry SDK in the binary). While the collector is unreachable, up to 4096 spans wait and the oldest are dropped, with a warning in the daemon's log; on `shutdown` what is left is sent within the daemon's grace. The `OTEL_EXPORTER_OTLP_*` variables reach the plugin only through its `env` or `secrets`, like any plugin's environment.

## Security

Plugins run as the daemon's user, so they are the owner's: agents cannot add or change them. The network plugin lives in `daemon.yaml` (0600, in the config dir), which no agent can reach. `barnraise.yaml` (the root one and every part's) and `.barnraise/**` are in the built-in `deny_write`, and a plugin program at another path inside the repository is added to `deny_write` when the file is compiled. Keep repository plugins in `.barnraise/plugins/`. See [security.md](security.md#13-plugins-are-the-owners).

## Not in version 1

- **Providers** (AI tools besides Claude Code and Codex) are compiled in. The names `provider.detect`, `provider.open`, `provider.send`, `provider.stop` and the notification `provider.event` are reserved for a later version.
- **Step types** besides agent, script, approval, check and merge queue are not pluggable yet; a `script` step runs any program.
- Hooks cannot block a step or veto an integration.
- Plugins are declared per project; `daemon.yaml` declares only the network plugin.
