# ADR-0048: A live view of a running session, opened only while someone watches

Status: accepted

## Context

An item page said "running", but the owner could not see what the agent was doing, nor talk to it. The session's events (what the agent writes, the tools it calls) reached the runtime and were dropped after the bookkeeping (usage, files touched, a "working" state every 15 seconds). A message to the item reached the agent only when its hooks read the inbox, at most every 20 seconds between tool calls, and the panel did not show the agent's answers.

Streaming every session all the time would cost the network between machines and the event log for something read rarely, so the view must cost nothing while nobody looks.

## Decision

1. **An in-memory feed per running session.** The runtime turns each provider event a person reads (text, tool call with a short input summary, tool result, error, limit) into a line cut at 300 characters, and keeps the last 200 in a ring. Lines go to the watchers of that session only, without waiting for a slow one. No line is written to the event log or published on the bus.
2. **Watched only on request.** `GET /v1/items/{id}/live` (server-sent events) opens a watch: the session's agent, role, step, model and machine, the ring, then the new lines; the item's messages come in the same stream. The panel opens it when its Agent tab opens and closes it when the tab closes or the page leaves. It needs `admin`; a session token is refused (it is not in the session allow-list, and the service checks again).
3. **Across the cluster, a stream with a lease.** When the session runs on another machine, the daemon serving the panel opens a stream on that machine (`br-rpc.<machine>.live`, within the peer permissions): the first reply carries the session and the ring, the lines follow on the asker's inbox, the asker renews the stream every 15 seconds and says stop when the watch ends; a stream not renewed for three periods ends on its own. A machine that cannot be reached, or runs an older Barnraise, is named in the panel: "running on <machine>: open the panel of that machine".
4. **Messages reach the running agent at its next tool call.** The owner writes in the Agent tab; the message goes to `item:<id>` through `SendMessage`, as before. The daemon of the machine running the session (it sees the message, replicated or its own) marks the session folder (`.barnraise-session/.mail.flag`), and the post-tool hook reads the inbox at the next tool call instead of waiting for its 20-second pause. The delivery tells the agent the owner is watching and that it answers with its `message` tool to `owner`. Injecting text into the provider's live input was not chosen: the Claude adapter sends the prompt on stdin and closes it, and every provider would need its own way.

## Consequences

- With no watcher, a session costs one append to a ring of 200 short lines per event; nothing crosses the network.
- One open tab is one HTTP stream, plus one NATS stream when the session runs elsewhere.
- An agent that is thinking without calling tools reads the message when it next calls one; the panel says so.
- The lines are summaries: the whole of a tool's output stays where it was (the session's outputs, the filtered command's `barnraise output show`).
