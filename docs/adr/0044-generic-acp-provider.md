# ADR-0044: A generic provider for agents that speak the Agent Client Protocol

- Status: accepted
- Date: 2026-11-24
- Amends: [ADR-0012](0012-ai-tools-through-their-clis.md)

## Context

Every AI tool Barnraise drives has its own adapter: a command line, a stream format and a parser written for that CLI (`internal/adapters/out/claude`, `codex`, `gemini`, with `provcommon` underneath). Each one breaks when its CLI changes its output, and each new tool means a new adapter.

The Agent Client Protocol ([agentclientprotocol.com](https://agentclientprotocol.com), started by Zed Industries) is a standard between an editor (the client) and a coding agent (a program on stdio): JSON-RPC 2.0, one message per line. The client opens a session, sends a prompt and receives the agent's messages, tool calls, plans and usage as `session/update` notifications. The agent asks the client to read and write files, to run terminals and for permission before a tool call. A growing number of agents speak it, directly or through a small bridge. Version 1 is the stable version; a version 2 is being discussed.

The idea of a Go client for this protocol came from [coder/acp-go-sdk](https://github.com/coder/acp-go-sdk) (Apache-2.0). We took only the idea. Barnraise does not depend on it and has none of its code: the protocol is written from its specification.

## Decision

- A provider type `acp` (`internal/adapters/out/acp`) runs any agent program that speaks the protocol. It is opt-in: a provider is declared in `barnraise.yaml` as `providers.<name>: {type: acp, command, args, env}`. Nothing changes for the built-in providers or for the key-based ones. One adapter, registered in the composition root as `Deps.ACP`, runs every such provider. The session carries the agent program in `SessionSpec.ACP`.
- Barnraise is the client. A session runs `initialize`, then `session/new` (or `session/load` to resume, when the agent can), then picks the model when the agent offers that model, then sends one `session/prompt` with the system prompt and the prompt as text blocks. Barnraise's MCP server and the role's `mcp:` servers are handed to the agent in `session/new`. `session/cancel` stops the turn. When the turn ends, stdin is closed, and the program gets SIGINT and then SIGKILL after a grace period.
- The protocol's own requests are where the policy applies, with the same `policy.json` the hooks of the other providers read:
  - `session/request_permission` is answered by the policy: an allowed call gets `allow_once` (never `allow_always`, so the next call is checked again), and a refused call gets `reject_once`. Nobody is waited for. A change of the agent's own mode (`switch_mode`) is refused.
  - `fs/read_text_file` and `fs/write_text_file` follow `deny_read`, `deny_write`, `lock_existing` and the worktree boundary. Inside a container they also stop at the mounts. Under an isolated Linux user they run as that user, through `sudo -n`.
  - `terminal/*` commands are checked against `deny_commands` and the shell's reads and writes. They run in a bubblewrap sandbox where the machine can start one: the file system is read-only, the worktree and the repository's `.git` are writable, `/tmp` is private, the `deny_read` folders are hidden, and the commands get a PID namespace of their own. Inside the project's container they run in a container made the same way, and as the isolated user when there is one.
  - Every refusal becomes a `policy.violation` event, through a `permission` provider event. The runtime records it like a block of the hooks. An edit that the agent reports as done with its own tool, without asking, is checked after the fact and reported the same way.
- Streaming updates become the normalized provider events: message, tool use and tool result, usage, init and done. The whole conversation goes to `.barnraise-session/acp-<name>.jsonl` in the worktree, which is excluded from git. Usage reaches the ledger where the agent reports it: the cost in `usage_update` when it is in USD, and the turn's tokens when the agent includes them in the answer to the prompt (an unstable field, read when present).
- Version handling is isolated. The session and the client use normalized types. A `dialect` per protocol version turns them into that version's messages and back (`v1.go`), and `initialize` picks the dialect of the version the agent answers with. Supporting version 2 means adding a dialect. Version 1 is implemented now rather than waiting for version 2.
- An ACP account has no login of Barnraise's: `barnraise account add <name>` adds it to the rotation, and the agent keeps its own login. An agent that answers `auth_required` ends the session with a message to log in by hand once.

## Consequences

- A new agent that speaks ACP needs a few lines of configuration, not an adapter and a parser.
- Policy for an ACP agent rests on what the agent asks of the client. An agent whose own tools write files without asking escapes the check before the fact. It is reported after the fact, and the OS layers (isolated user, container, egress proxy) still hold. The security documentation says so.
- Usage limits are only known when the agent reports them in an error text, so an ACP account has no 5-hour or weekly window to plan with.
- The fields of the protocol that are still unstable (model selection, a turn's usage) are optional. If they change, the feature they serve stops working, but sessions keep running.
