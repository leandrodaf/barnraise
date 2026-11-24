# ADR-0033: Recorded conflict resolutions, and a structural merge driver when installed

- Status: accepted
- Date: 2026-11-24
- Builds on: [ADR-0004](0004-git-as-arbiter.md), [ADR-0021](0021-branch-and-draft-pr-per-item.md)

## Context

The merge queue rebases every item on the current main. A conflict aborts the rebase and sends the item back to its agent, which resolves it and comes back to the queue. The same conflict then often comes up again: in a retry after main moved, in bisection, in another item that touches the same lines, or on another machine. Each time an agent spends a session resolving what was already resolved.

git has had the answer for years: `rerere` ("reuse recorded resolution") records how a conflict was resolved and applies it the next time the same conflict appears. It only works when it is on at the moment the conflict happens, and its record lives in the repository's common directory (`rr-cache`), which every worktree of the repository shares.

Many conflicts are not real ones either: two items add a function or an import next to each other. A structural merge driver, which merges the syntax tree instead of lines, resolves those. [mergiraf](https://codeberg.org/mergiraf/mergiraf) is such a driver, used through git's documented merge driver interface (`merge.<name>.driver` plus a `merge=` attribute). We checked its repository on 2026-11-24: license **GPL-3.0-only** (`Cargo.toml` `license = "GPL-3.0-only"`, `LICENSE.txt` is the GNU GPL version 3; the README says "License: GPLv3"), actively maintained (version 0.20.0, last commit on main 2026-11-21), and `mergiraf languages --gitattributes` prints the patterns it supports as `<pattern> merge=mergiraf` lines. It is a separate program run as a child process by git, so its license does not reach Barnraise's code (Apache-2.0, ADR-0024); Barnraise never vendors, ships or downloads it. Only some users will install it.

The idea of turning `rerere` on for automated rebases comes from git itself and its documentation; the idea of a structural driver from mergiraf (GPL-3.0-only, https://mergiraf.org). Only the ideas were taken; no code was copied (ADR-0018).

## Decision

**rerere is native and on by default** (`merge.rerere: true`).

- At each start and reload of a project the daemon sets `rerere.enabled` and `rerere.autoupdate` to `true` in the repository's local config, only when no git config scope sets them, and marks that with `barnraise.rerere = true`. A user's own value wins. `merge.rerere: false` removes only what Barnraise added.
- The local config and `rr-cache` belong to the common git directory, so the main checkout, every item worktree and the merge queue's checkouts record into and reuse the same resolutions. Agents running git in their worktree record theirs as they resolve. `CommitAll` also runs `git rerere` while a merge or rebase is in progress, so a resolution left for Barnraise to commit is recorded.
- The git adapter's rebase continues a stop whose conflicts git resolved entirely from recorded resolutions (staging them itself when `rerere.autoupdate` is off, skipping a commit the resolution leaves empty) and aborts, as before, when any file is left unresolved. It reports the reapplied files through an optional port (`ports.ReusingRebaser`), so the `Git` interface stays as it is.
- When files were reapplied, the merge queue emits `merge.conflicts.reapplied` with the files and runs every local check on the combined tree, bypassing both the "same tree as already checked" shortcut and the selection by touched files. A failure carries a hint naming the reapplied files and `git rerere forget`.

**The structural driver is opt-in by presence** (`merge.structural: auto`, the default, uses mergiraf when it is on the `PATH`; `off` never does).

- The daemon registers the driver as `merge.barnraise-structural` in the local config with mergiraf's documented command line (`merge --git %O %A %B -s %S -x %X -y %Y -p %P -l %L`) and its absolute path, and writes the patterns mergiraf lists in a managed block of `<common dir>/info/attributes`. Nothing is committed to the project. Patterns the project's `.gitattributes` already gives a merge behaviour are left out, because `info/attributes` would override them.
- The driver command checks that the program is there and otherwise runs `git merge-file` with the same labels and marker size, so a merge where mergiraf is absent (an agent in a container, an uninstall since the daemon started) gets the usual conflict markers instead of a failed merge.
- When mergiraf is missing, lists nothing or `off` is set, the block and the driver section are removed and git merges as before.
- Tests use a fake driver program; none needs mergiraf or the network.

## Consequences

- A conflict is resolved once per project, not once per attempt; the checks still decide whether the result lands.
- A wrong recorded resolution is reapplied until forgotten. The timeline event and the failure hint point at it; `git rerere forget <file>` or deleting `.git/rr-cache` clears it.
- The settings also apply to the user's own checkout of the repository, since git keeps them per repository. They are local and documented in [operations](../operations.md#merge-settings-in-the-repository).
- Barnraise depends on git's stderr notices (`Resolved '…' using previous resolution.`, `Staged '…'`) under `LC_ALL=C` to know what was reapplied. If git changes them, a reapplied conflict is treated as an unresolved one and sent back as before: the fallback is the old behaviour, never a silent landing.
