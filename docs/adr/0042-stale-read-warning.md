# ADR-0042: Warn an agent when main changes what it read

- Status: accepted
- Date: 2026-11-24

## Context

Items run in parallel, each in its own worktree branched from main. When another item lands, the agent of an item in progress keeps working from what it read before: an old version of a file another item just changed. Today it finds out only in the merge queue, when the rebase conflicts or the combined checks fail, after it spent its session on a stale picture. The living-plan revalidation (ADR-0022) already messages items whose *anchors* a change touches, but anchors are what the item was planned around, not what the agent actually opened.

The idea comes from jcode (<https://github.com/1jehuang/jcode>, MIT): tell an agent when a file it read changed under it. Only the idea was taken; no code.

## Decision

- **What the agent saw is tracked from the session itself.** Every provider adapter already parses its CLI's stream into `tool_use` events. The runtime takes the files of file tools from them (`Read`, `read_file`, `read_many_files`, edits, Codex `file_change`, `apply_patch`; not searches, which would match everything), relative to the worktree, and notes them once each in the local store (`file_reads`, store migration 3), through an optional `ports.FileReadLog` the SQLite store implements. It is local and never replicated: the sessions and the worktree are on this machine. The notes of an item go when it ends.
- **When main moves, each item in progress on this machine is compared with it.** After a local integration, and when an `item.integrated` event arrives from another machine (then after a fetch), Barnraise takes what main changed since the item last looked (the merge base the first time, then the last main it was compared with) and intersects it with the files the item's sessions read or changed and the files its branch changes, without `plan.md`, `criteria.json` and the changelog.
- **The agent hears it through the channels it already has.** An overlap becomes a message to the item, which the hooks deliver between tool calls and the next step's prompt carries, listing each file with a deterministic summary of its change (lines added and removed, the enclosing functions of the first hunks) and the `git diff` to see it, and the event `item.stale_read` with the files, the commits and whether a rebase happened.
- **The early rebase is opt-in.** With `stale_reads.rebase: true`, pending changes are committed and the branch is rebased on the new main at once; a conflict is aborted and reported, leaving the branch as it was. It is off by default because it moves files under a running agent; the merge queue rebases at integration anyway.
- On by default (`stale_reads.enabled`), since every user runs items in parallel and the cost is a few git calls per integration.

## Consequences

- Agents learn about a change to what they rely on minutes after it lands instead of at the end of the item, and can re-read before building on it.
- Reads made through the shell (`cat`, `sed`) are not seen; the branch's own changes and the file tools cover most of what matters.
- The comparison runs only on the machine holding the item, which has the worktree and the notes; nothing new is replicated.
