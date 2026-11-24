# ADR-0041: Build caches copy-on-write, and hooks at the phases of a worktree

- Status: accepted
- Date: 2026-11-24

## Context

A new item worktree is a fresh checkout. `worktree.include` brings files that are not in git and `worktree.setup` installs dependencies, but nothing brings the build: every item's first `cargo build`, `next build` or `gradle build` starts from nothing, minutes of CPU on every machine for every item. Copying `target/` from somewhere would cost as much disk and time as it saves. Projects also need to act at moments of a worktree's life Barnraise alone knows: when it is created, before an agent works in it, after it landed, before it goes.

The idea of both comes from [worktrunk](https://github.com/max-sixty/worktrunk) by max-sixty (Apache-2.0): copy-on-write copies of build caches into new worktrees, and hooks at worktree phases. Only the idea was taken; no code was read or copied (ADR-0018).

## Decision

- `worktree.build_cache` lists directories of build artifacts. A new item worktree receives each one, before `setup`, copy-on-write from the build of the last integrated item or, before there is one, from the main checkout; the second attempt of a risky item from the first attempt. When an item is integrated, its worktree's build caches are kept, also copy-on-write, in `buildcache/<project>/` of the data dir.
- Cloning is a port (`ports.TreeCloner`) with an outbound adapter, `internal/adapters/out/reflink`: the `FICLONE` ioctl file by file on Linux, one `clonefile` for the whole tree on macOS, through `golang.org/x/sys/unix`, already in the module graph (it moves from indirect to direct; no new module). Links stay links, modes are kept, sockets and pipes are skipped.
- Where the filesystem cannot clone (ext4, tmpfs, two filesystems), the directory is skipped and nothing is left half-copied, unless `build_cache_fallback: copy` asks for a full copy. A cache that is silently slow would be worse than none.
- `worktree.hooks` has `post_create` (once, after `setup`, in item worktrees), `pre_session` (before each agent session; a failure holds the session), `post_integrate` (after landing, before the build cache is kept) and `pre_remove` (before removal). They run as `setup` runs, `sh -c` in the worktree with the item's isolation variables (ADR-0040) and `BARNRAISE_HOOK`, with `hooks.timeout`. The order when an item lands is `post_integrate`, `pre_remove`, the isolation teardown, compose down, removal.

## Consequences

- On Btrfs, XFS, bcachefs or APFS a new worktree starts with a warm build at no disk cost; elsewhere nothing changes unless the owner opts into full copies.
- Build tools validate their caches, so one from a slightly different tree is a head start, never a wrong result.
- Hooks run what the owner wrote in `barnraise.yaml`, which agents cannot change, with the same reach as `setup`; they add phases, not power.
- Real reflinks were tested only where the test machine's filesystem has them; the tests accept both a clone and a clean refusal.
