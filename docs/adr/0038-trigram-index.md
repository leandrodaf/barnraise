# ADR-0038: A trigram index for exact substring and regex search, inside the knowledge database

- Status: accepted
- Date: 2026-11-24

## Context

Agents look for text in code all the time: an error message, a call, a constant. The knowledge index answers ids, symbol names and words (FTS5), but not a substring inside an identifier nor a regular expression, so agents walk the tree with grep. [zoekt](https://github.com/sourcegraph/zoekt) (Apache-2.0) answers this with a trigram index: the files that can contain a pattern are those that have all the three-byte sequences the pattern needs; only those are read. Russ Cox's article "Regular Expression Matching with a Trigram Index" (Google Code Search) describes how to derive the needed trigrams from a regular expression. The zoekt library itself would bring a large tree of dependencies and its own storage; only the idea is taken, written from scratch.

## Decision

- Knowledge migration 2 adds `grep_files` (one row per indexed text file of a project: path, content, its sorted trigrams as a delta-varint blob, and a one-line purpose for the repository map) and `grep_postings` (`trigram → file`, a `WITHOUT ROWID` table keyed by both). The migration blanks the stored hashes so the next reindex reads every file once.
- Every file the index reads is indexed: code, `.barnraise/` documents, and plain text files (Markdown, YAML, JSON, TOML, SQL, shell, HTML, CSS, C and others; lock files and hidden files are left out). It is incremental: a changed file's postings are removed (from its own trigram list) and written again in the same short transaction as its documents; a deleted file is removed with them.
- Trigrams are ASCII case-folded, so one index serves case-sensitive and case-insensitive searches; trigrams across a line break or made of blanks are not indexed, since matching is per line.
- A substring needs all its trigrams. A regular expression is parsed with `regexp/syntax` and simplified; each piece yields an exact set of strings when small (literals, small classes, alternations, `?`, concatenations by cross product) or a condition (AND/OR of trigrams) otherwise; `*`, `.` and big classes put no condition. The condition only removes files that cannot match; the candidates are then checked line by line with Go's `regexp` (RE2, linear time).
- `Index.Grep` is the API. It is reached where agents already search, without a new endpoint or tool: `Search` with kind `grep` (substring) or `regex` returns `path:line` hits of kind `match`, a leading `file:<prefix>` narrows the path; the MCP `find` tool takes `mode: substring | regex`, `ignore_case` and `path`; the CLI has `barnraise find --grep | --regex [--path]`. The ordinary layered search gains a substring layer for one-word queries of three or more characters, fused with the others by RRF, so a word inside a longer identifier finds its file.

## Consequences

- Text search over the main branch costs milliseconds and reads only candidate files.
- The database grows by about the indexed text plus postings (Barnraise's own repository: about 400 files, 30 MB).
- The index reflects the files the knowledge index last read (main after each integration), not uncommitted work in an item's worktree.
