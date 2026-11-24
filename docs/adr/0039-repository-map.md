# ADR-0039: A repository map in the repository, written by Barnraise at integration

- Status: accepted
- Date: 2026-11-24

## Context

A new session starts knowing its item, the rules of its area and the symbols of its anchors, but not the shape of the repository: where things are and what each file is for. It explores to find out, every time. The aoci-code project ([aoci-spec/aoci-code](https://github.com/aoci-spec/aoci-code)) keeps a versioned map of the code for agents. Its license is not stated, so only the idea is taken; nothing of its format or code is used.

## Decision

- `.barnraise/map.md` holds one line per file: `- path — what it is for · main symbols`. The purpose is the file's first heading (Markdown) or its header comment (package comment, module docstring), else its first doc comment; license headers, build tags and tool directives are skipped. The symbols are the top-level ones, exported-looking first, six at most. Barnraise's own `.barnraise/` documents (found with `find`) and the map itself are left out. Past `knowledge.map.max_files` lines the least described files are counted per folder. The map depends only on the files: no dates, a stable order.
- It is generated from the knowledge index. When an item is integrated, after its rebase on main and its changelog line, the map is rendered for the item's worktree: the index gives what it knows, and the files changed since the commit it last indexed (main's and the item's) are read again from the worktree. It is written there and committed on the item's branch, so it lands inside the item's single commit on main, as the changelog does. Agents never write it: `.barnraise/**` is write-denied to them, and reads are allowed, so they can open it. The merge queue does not count the map (nor the changelog) as a change of the product when it decides which checks run again, and a map that cannot be made is logged and never holds the item back.
- The index never reads the map back.
- Each session's context pack gets a part of it, bounded by `knowledge.map.prompt_tokens`: the whole map when it fits, otherwise the lines of the item's anchor files and files touched, their folders, the item's area folder and the words of its title first, filled up with the rest in the map's order, and a line saying where the whole map is. The prompt's conventions point to it.
- `knowledge.map.enabled` (default `true`) turns it off.

## Consequences

- Every project gets a map that stays current with main, at no cost to agents, and sessions start with the repository's shape.
- Integrated items carry one more file in their commit when the map changes; an item that changes nothing the map shows leaves it untouched.
- Projects that do not want the file in their repository set `knowledge.map.enabled: false`.
