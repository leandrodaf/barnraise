# Contributing to Barnraise

Thanks for helping. This page says how the code is organized, what CI expects, and how to write commits and docs.

## Getting started

```sh
git clone https://github.com/leandrodaf/barnraise && cd barnraise
make build      # ./barnraise
make test       # fast tests
make help       # every target
```

You need Go 1.26 and `git`. For the API contract also `buf`, `protoc-gen-go`, `protoc-gen-connect-go` and `protoc-gen-connect-openapi`; for linting `golangci-lint` v2.

## The hexagonal rules

Barnraise is ports and adapters ([ADR-0002](docs/adr/0002-hexagonal-architecture.md)):

| Package | May import |
| --- | --- |
| `internal/domain` | the standard library only |
| `internal/ports` | `internal/domain` |
| `internal/app` | `internal/domain`, `internal/ports` |
| `internal/adapters/in/*` | the application and below |
| `internal/adapters/out/*` | ports and domain; never `internal/app` or inbound adapters |
| `cmd/barnraise` | everything: it is the composition root |

- `depguard` in `.golangci.yml` enforces this; `make lint` fails on a violation.
- Time, randomness and processes enter through ports (`Clock`, `IDGen`, `Exec`), so the domain never sleeps or runs anything.
- Keep ports small and specific. A new provider, tracker, check runner or notifier is a new adapter, wired in `cmd/barnraise`.
- The flow is code: the next step, rework and integration are decided by Barnraise, not by a prompt.

## Tests

| What | Where and how |
| --- | --- |
| Domain | Table tests and property tests with `rapid` for invariants |
| Use cases | `internal/app` with the in-memory fakes and contract suites in `internal/testkit` and a controlled clock |
| Adapters | Real git (a local bare repository as remote), real embedded NATS and SQLite; recorded responses for external APIs in `testdata/`; recorded `stream-json` sessions for providers |
| End to end | `cmd/barnraise/e2e_test.go` with the fake provider; `cmd/barnraise/cluster_test.go` with two machines. Skipped by `-short`; run with `make e2e` |
| Fuzzing | Native Go fuzzing for external input; `make fuzz-smoke` |
| Vocabulary | `internal/archtest` |

Before opening a pull request:

```sh
make lint test-race cover proto-breaking
```

- Tests always run with `-race` in CI.
- `internal/domain` and `internal/app` must stay at **90% coverage or more** (`make cover`); changed lines should be covered.
- A change to the proto must pass `buf lint` and `buf breaking`; regenerate with `make proto` and commit `gen/`.

## The glossary rule

One word per concept, everywhere: CLI, YAML, prompts, panel, docs and messages. Use the terms of the glossary in the [README](README.md#concepts) (item, check, machine, goal, focus, tweak, …) and their fixed translations. `internal/archtest/vocab_test.go` lists the words that are not Barnraise vocabulary and fails when they appear in Go strings and comments, templates, the panel, `README.md` or `docs/`. If a third-party API forces such a word on you, add a narrow, explained entry to its allow list instead of renaming the concept.

- Code, CLI, YAML keys, internal prompts and Barnraise's own docs are in English. What goes to people (items, comments, questions, reports) follows the project's `language:`. `docs/README.pt-BR.md` is the Portuguese version of the README; keep both in step.
- "Issue" only means the thing inside GitHub or GitLab.

## Architecture decisions

Every architecture decision gets a short record in `docs/adr/` (`NNNN-short-title.md`: context, decision, consequences) and a line in `docs/adr/README.md`. A decision that changes gets a new record that supersedes the old one; old records are not rewritten. Changes to Barnraise's own policy, hooks, permissions and arbiter need the owner's approval.

## Commits and pull requests

- Use [Conventional Commits](https://www.conventionalcommits.org/): `feat(arbiter): …`, `fix(hooks): …`, `docs: …`, `test: …`, `refactor: …`, `ci: …`, `chore: …`, and `!` or a `BREAKING CHANGE:` footer for incompatible changes. The release changelog is generated from them.
- Do **not** add `Co-authored-by` trailers or co-authorship lines to commits or pull request descriptions.
- One logical change per pull request, with tests and docs in the same pull request.
- New code is written from scratch: ideas from other projects are welcome, their code is not ([ADR-0018](docs/adr/0018-rewrite-and-migrate-side-by-side.md)).

## License

By contributing you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
