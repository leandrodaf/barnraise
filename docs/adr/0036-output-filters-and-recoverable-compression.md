# ADR-0036: Output filters, recoverable compression and short answers

- Status: accepted
- Date: 2026-11-24

## Context

Most of what an agent reads in a step is the output of its own shell commands, and most of what it writes is prose nobody reads. `barnraise filter` already summarized eight tools (Gradle, `go test`, jest, vitest, the JS test runners, pytest, Maestro and a generic tail), but the commands agents run most were missing: `git status/diff/log`, linters, `cargo`, `mvn`, `make`, `tsc`, listings, `grep`/`rg`, `docker`, `kubectl`, package installs. A summary also loses text: the old filter printed a log path, and nothing tied what it saved to the token ledger.

Two projects showed the ideas; only the ideas were taken, no code:

- [rtk](https://github.com/rtk-ai/rtk) (Apache-2.0; its optional engine is BUSL-1.1): a CLI proxy that rewrites an agent's shell commands into compact outputs per tool, keeping errors and summaries.
- [caveman](https://github.com/JuliusBrussee/caveman) (Apache-2.0): tell the model to answer tersely to cut output tokens, and keep compressed text recoverable.

## Decision

- **Native filters, for every user.** `internal/adapters/out/outfilter` covers 25 tools. Rule tables keep failure lines with their context, summary lines, and count noise (progress, downloads, passing tests, warnings); structural filters handle what needs parsing (`git status` groups, `git diff` per-file stat plus the first lines of each file with lock and generated files left out, `git log` one line per commit, eslint errors under their file with warnings counted by rule, `find` counts by folder, `grep` matches per file, `kubectl describe` fields and events, logs). Identical lines collapse with a count. Short outputs of git, listings, searches and logs, and any output the summary would not shorten, are printed unchanged. Detection reads the command's words (`git -C x status`, `VAR=1 go build`, `docker compose up`); the `pre-tool` hook wraps a simple command only when the tool is the command itself.
- **Delegation to rtk, opt-in by installation.** `outfilter.delegate` (`auto` by default, `rtk`, `off`) decides whether commands go to rtk when it is on the `PATH`: `auto` only for commands without a native filter, `rtk` for every simple command. Barnraise never installs rtk; without it every mode is `off`. Only simple commands (no shell syntax or quotes) are delegated, as `rtk <words>`; if rtk rejects the command line itself, the command runs natively.
- **Recoverable compression.** Every shortened output is kept in full in the item worktree's `.barnraise-session/outputs/` (the data folder's `outputs/` outside a session), as `<id>.log` plus an `index.jsonl` record (`domain.OutputRecord`). Each file is bounded (first and last 8 MB), the folder by age (`outfilter.keep`, 7 days) and size (`outfilter.keep_size`, 64 MB; the newest output always stays). The summary ends with `barnraise output show <id>`, which prints the whole text, a line range or the matching lines; `output list` and `output stats` complete it. The recovery is a CLI command, not an MCP tool: the agents already have a shell, the handle names the exact command, and each role keeps its six to eight MCP tools.
- **Savings in the ledger.** Each session gets a run id (`BARNRAISE_RUN`); records carry it. When the step ends the daemon sums the session's records into `TokenUsage.FilteredRuns` and `FilterSaved` (the `agent.usage` event), which the metrics computation sums (`FilteredRuns`, `FilterSaved`, `FilterSavedByRole`), the timeline shows per step and the digest shows for the week.
- **Short answers in mechanical steps.** `terse.mode` (`auto` by default, `all`, `off`) adds an "Answer style" section to the step's prompt when the step is mechanical: its model class is `cheap` or `simple`, or it is pinned to the first model of a provider's ladder (the critic's haiku). `terse.steps` names more. Code, commands, errors and the delivery's fields stay exact.

## Consequences

- An agent's context gets the failures and summaries of the commands it runs, and the full text stays one command away; nothing the filter drops is lost while the worktree lives.
- The savings are measured, per step and role, from the agents' own runs rather than estimated.
- A delegated command's kept text is rtk's output, since rtk runs the command; `barnraise filter --raw` reruns it unfiltered.
- `barnraise metrics` does not print the savings yet: its proto response has no field for them, and this change keeps the proto as it is. The event log, the timeline, the digest and `barnraise output stats` carry them.
- More filters means more rules to keep honest: each one has tests with real-looking outputs.
