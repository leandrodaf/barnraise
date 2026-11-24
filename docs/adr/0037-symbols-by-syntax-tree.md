# ADR-0037: Symbols by syntax tree with ast-grep, and a native tokenizer as the fallback

- Status: accepted
- Date: 2026-11-24
- Amends: [ADR-0022](0022-living-plan-anchors-short-ids.md)

## Context

The knowledge index reads Go with `go/ast`, and every other language with one regular expression per line: a function written inside a comment or a string was a symbol, a brace inside a string broke nothing because braces were never counted, and a body ended at the next declaration, so end lines, regions and one-level calls were wrong as soon as code was not laid out the expected way. Locals (a closure inside a function) were listed as if they were methods.

A real parser per language would fix it, but tree-sitter in Go needs cgo, and the binary is built with `CGO_ENABLED=0` for four platforms. [ast-grep](https://github.com/ast-grep/ast-grep) (MIT) is a single binary built on tree-sitter that matches syntax kinds and prints JSON; it is the idea taken here, through its command line only. No code of it is used.

## Decision

- Go stays on `go/ast`.
- When ast-grep is installed (`ast-grep`, or `sg` when `sg --version` says ast-grep: on Linux `sg` is also a shadow-utils command), the index runs `ast-grep scan --inline-rules <rules> --json=stream <files>` once per language and per 50 files, before any write transaction, through the `Exec` port. The rules are built in: one per syntax kind (functions, classes, methods, interfaces, types, enums, traits, impl blocks, exported constants) for TypeScript, TSX, JavaScript, Python, Rust, Java, Kotlin and Swift. Only kinds and ranges come from ast-grep; names, signatures, regions and calls are read from the source Barnraise already holds. A match whose text does not agree with that source sends the file to the native extractor; a language whose run fails with no output is not asked again for the life of the process.
- Otherwise, and for every language ast-grep does not cover (Dart) or could not handle, the native extractor runs: a small tokenizer per language family blanks comments and the contents of strings, characters, template literals (with `${}` code kept), raw strings, Rust lifetimes vs. characters, and JavaScript regular expression literals, keeping offsets and line breaks. Declarations are matched on the blanked lines; bodies are followed by braces (by indentation and open brackets in Python; by line ends in Kotlin, Swift and Dart expression bodies). Declarations inside a function are locals and left out; inside a class-like container they are qualified by it. Exported constants are symbols too.
- `knowledge.symbols: auto | native` per project; `auto` is the default. The file document records which extractor read it (`extractor` in its meta).

## Consequences

- With ast-grep installed, symbols come from a syntax tree; without it, the native extractor is still right on comments, strings and body ends, which the line regexes were not.
- ast-grep's rule kinds follow its tree-sitter grammars; a kind a version does not know makes that language fall back to the native extractor instead of failing the index. The integration is tested with a stand-in program; the real binary is only exercised when it is installed.
- The upgrade that brings this re-reads every file once (knowledge migration 2, see ADR-0038).
