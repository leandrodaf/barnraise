# ADR-0047: Barnraise rebases for the agent, and one item repairs a check red on main

Status: accepted

## Context

The flow was meant to run with nobody watching, but two situations kept needing a person, or another agent from outside, to unblock it.

**Rebase conflicts.** When the merge queue could not rebase an item on main, it aborted, left the worktree clean and sent the item back with one line ("rebase on main conflicts: scripts/check.sh"). The agent then ran the rebase itself, inside its sandbox, where `.barnraise/` is read only. Main changes `.barnraise/map.md` at every integration, so git stopped halfway ("unable to unlink old '.barnraise/map.md': Read-only file system"), and `git reset --hard` failed the same way. The agent delivered BLOCKED, and the step's commit (`git add -A`) recorded the broken worktree on the branch: in one case it deleted the item's own test and plan and copied 26 files of main over the branch. The owner was then asked "retry or give up?", and a retry hit the same wall. The same commit also picked up the empty read-only files the sandbox mounts over protected paths (`.bashrc`, `.mcp.json`, `.claude/agents`…).

**A check red on main.** A check that fails on main as well says nothing about the item, so the item was held and the owner told to "fix main or the machine, then retry". With the autopilot on, the advisor could only answer "retry". Meanwhile every agent whose check failed fixed main on its own: several items and pull requests carried the same fix, and later conflicted with each other.

## Decision

1. **Barnraise rebases, the agent resolves.** When the item's last rework round is a rebase conflict from the merge queue, Barnraise rebases the branch on main before the session of the step it was sent back to, outside the sandbox (`ports.ConflictRebaser`). Conflicts git resolves from recorded resolutions (rerere) are continued, files under `.barnraise/` take main's side, and the item's memory (`plan.md`, `criteria.json`) keeps the branch's. At any other conflict the rebase stays stopped, and the prompt names the files and tells the agent to edit them only, without running git rebase, reset, checkout, stash or commit.
2. **No commit records an unfinished rebase.** Before any commit in an item's worktree, Barnraise stages the conflicted files that no longer hold conflict markers and continues the rebase. If conflicts are left, or a merge, cherry-pick or revert is in progress, it is aborted and the branch stays as it was. The safety commit during a session skips its turn while a rebase waits for that session.
3. **Sandbox mount points never ride with a commit.** Before a step's commit, untracked, empty, read-only files at the paths the sandbox protects (and its `.claude/.cc-writes` log) are removed from the worktree.
4. **One item repairs a check red on main.** When a check is red on the branch and on main, Barnraise opens one P0 bug item, "Make check NAME pass on main again", with what the check printed, unless one is already open. The held item waits for it as a dependency (the same wait as "blocked, waits for I-4"): its step runs again by itself once the repair is on main. Nobody is asked. The repair item's own check failing on main is a normal failure of its work and goes back to its agent. Only when the repair item cannot be created is the owner told, as before.

## Consequences

- An item sent back for a conflict no longer depends on its agent's sandbox allowing a rebase, and a broken rebase can no longer land on a branch.
- A check red on main costs one item, not one per agent, and the items behind it resume on their own when it lands.
- A repair item with an environment cause (a locale, a user data folder, a tool version) is asked to make the check hermetic rather than change the machine. A cause that truly needs the owner (a missing device, an account) still reaches the owner through that item's own flow.
- Barnraise now writes in the item's worktree before a session (the rebase). It does so only for an item sent back for a conflict, and only when the branch is not already on main.
