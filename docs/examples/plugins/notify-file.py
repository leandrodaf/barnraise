#!/usr/bin/env python3
"""A Barnraise plugin (docs/plugins.md): a notifier and a hook that append
every notification and every event to a file, one JSON object per line.

    plugins:
      notify-file:
        command: .barnraise/plugins/notify-file.py
        options: { file: /var/log/barnraise/owner.jsonl }
        events: [item.integrated, "item.step.*"]
    notifications:
      - type: plugin:notify-file
"""
import json
import sys

out_path = None


def send(msg):
    msg["jsonrpc"] = "2.0"
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def reply(id, result=None, error=None):
    if error is not None:
        send({"id": id, "error": error})
    else:
        send({"id": id, "result": {} if result is None else result})


def append(record):
    with open(out_path, "a", encoding="utf-8") as f:
        f.write(json.dumps(record) + "\n")


for line in sys.stdin:
    if not line.strip():
        continue
    msg = json.loads(line)
    method, id, params = msg.get("method"), msg.get("id"), msg.get("params") or {}
    if id is None:
        continue  # a notification ($/cancel): every call here is quick, nothing to cancel
    if method == "initialize":
        out_path = (params.get("options") or {}).get("file")
        if not out_path:
            reply(id, error={"code": -32602, "message": "set options.file to the file to write"})
            continue
        print("writing to", out_path, file=sys.stderr)  # stderr goes to the daemon's log
        reply(id, {"name": "notify-file", "version": "1.0.0", "protocol_version": 1, "kinds": ["notifier", "hook"]})
    elif method == "notify.send":
        append({"notification": params})
        reply(id)
    elif method == "hook.event":
        append({"event": params["event"]})
        reply(id)
    elif method == "shutdown":
        reply(id)
        break
    else:
        reply(id, error={"code": -32601, "message": "method not found: " + str(method)})
