#!/usr/bin/env python3
"""A Barnraise network plugin (docs/plugins.md, kind network): the machines
come from a static file. Useful for plain WireGuard, a LAN with fixed
addresses, or any VPN whose addresses you already know.

    # daemon.yaml
    network: plugin
    network_plugin:
      command: /usr/bin/python3
      args: [/home/me/bin/network-static.py]
      options: { file: /home/me/.config/barnraise/network-peers }

Without options.file it reads "network-peers" in the config dir (the
plugin's working directory). The file is read again on every call, so edit
it at any time. JSON:

    {"self": "alpha",
     "machines": [
       {"name": "alpha", "addresses": ["10.8.0.1"], "tags": ["tag:barnraise"]},
       {"name": "beta",  "addresses": ["10.8.0.2"], "tags": ["tag:barnraise"], "probe": 22}
     ]}

or hosts-like lines, "address name [tag ...]" (# starts a comment):

    10.8.0.1  alpha  tag:barnraise
    10.8.0.2  beta   tag:barnraise

"self" (or options.self) names this machine; it defaults to the short host
name. A machine with "probe" is online when that TCP port answers within a
second; the others are always online.
"""
import json
import os
import socket
import sys

opts = {}


def send(msg):
    msg["jsonrpc"] = "2.0"
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def reply(id, result=None, error=None):
    if error is not None:
        send({"id": id, "error": error})
    else:
        send({"id": id, "result": {} if result is None else result})


def load(path):
    with open(path, encoding="utf-8") as f:
        text = f.read()
    if text.lstrip().startswith("{"):
        data = json.loads(text)
        return data.get("self"), data.get("machines") or []
    machines = []
    for line in text.splitlines():
        fields = line.split("#", 1)[0].split()
        if len(fields) >= 2:
            machines.append({"name": fields[1], "addresses": [fields[0]], "tags": fields[2:]})
    return None, machines


def online(m):
    port = m.get("probe")
    if not port:
        return m.get("online", True)
    for addr in m.get("addresses") or []:
        try:
            socket.create_connection((addr, int(port)), timeout=1).close()
            return True
        except OSError:
            pass
    return False


def status():
    self_name, machines = load(opts.get("file") or "network-peers")
    self_name = opts.get("self") or self_name or socket.gethostname().split(".")[0].lower()
    me = next((m for m in machines if m.get("name") == self_name), None)
    if me is None:
        raise LookupError("this machine (%s) is not in the file" % self_name)
    peers = [
        {"name": m["name"], "addresses": m.get("addresses") or [], "online": online(m), "tags": m.get("tags") or []}
        for m in machines if m is not me
    ]
    return {"self": {"name": me["name"], "addresses": me.get("addresses") or [], "tags": me.get("tags") or []}, "peers": peers}


for line in sys.stdin:
    if not line.strip():
        continue
    msg = json.loads(line)
    method, id, params = msg.get("method"), msg.get("id"), msg.get("params") or {}
    if id is None:
        continue  # $/cancel: every call here is quick
    if method == "initialize":
        opts = params.get("options") or {}
        reply(id, {"name": "network-static", "version": "1.0.0", "protocol_version": 1, "kinds": ["network"]})
    elif method == "network.status":
        try:
            reply(id, status())
        except (OSError, ValueError, LookupError) as e:
            reply(id, error={"code": -32002, "message": "network-static: %s" % e})
    elif method == "shutdown":
        reply(id)
        break
    else:
        reply(id, error={"code": -32601, "message": "method not found: " + str(method)})
