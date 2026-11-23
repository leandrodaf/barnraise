# ADR-0013: The daemon is the only authority; every interface is a client

- Status: accepted
- Date: 2026-11-23

## Context

Today's orders are typed into terminals and rules live in long prompts and scripts. Interfaces multiply: CLI, MCP on a phone, a web panel, CI, bots.

## Decision

One binary, several modes. **The daemon is the only authority**: only it writes state, touches main and decides transitions. The CLI, the MCP server, the hooks, the panel and any external system only ask, through the public API (unix socket or HTTPS), and follow the event stream. The flow (next step, rework, integration) is Barnraise code; the model decides the content of the work.

## Consequences

- No `tmux send-keys`; messages to agents go through the daemon (hooks inject the inbox).
- Permission by role is enforced in one place; an agent cannot go around it through Bash.
- The panel is only a client: anyone can build another interface on the same API and events.
