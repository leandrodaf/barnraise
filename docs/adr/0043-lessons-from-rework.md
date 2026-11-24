# ADR-0043: Lessons from rework

- Status: accepted
- Date: 2026-11-24

## Context

Rework is the most expensive thing an item does: a failed check, a review asking for changes or a merge-queue failure costs a whole new session. The reason and the fix are written down (the rework record, the commits) but forgotten when the item ends, so the next item in the same files makes the same mistake. Exhausted rework already turns into a proposal for the owner (`analyzeExhaustedRework`), but ordinary rounds teach nothing.

The idea comes from agent-beacon (<https://github.com/Asymptote-Labs/agent-beacon>, MIT): distill what an agent got wrong and was corrected on into short lessons fed to later work. Only the idea was taken; no code.

## Decision

- **A round is recorded when the work is sent back.** The rework record keeps the item's head at that moment. The owner's `redo` with a reason, and the rounds of a budget reset for a stronger model, are kept in `past_rework`, which does not count against the rework budget.
- **A lesson is written when the round is resolved**: when the step that sent the work back passes again, or when the item lands. It is distilled without a model, so it costs nothing and is the same on every machine: the reason (its first lines, without logs), the files the fix changed (from the recorded head to the head now, limited to the item's own files), the item's areas and the checks the reason names. A cheap model step was considered and left out: the deterministic summary is good enough to point at the place and the cause, and a model would add cost and a failure mode to every resolved round.
- **Similar lessons merge.** Same step and checks, reasons that share at least half their words, and the same areas, files or folders: the round joins the existing lesson, which counts how many times it was taught (`lesson.merged`); otherwise a new one is written (`lesson.learned`). The id is derived from the first round (`L-` and eight hex digits), so a replayed round is never learned twice.
- **Lessons are events.** They live in the event log and the state, like items, so every machine has them and the history shows when each was learned, merged or deleted. The store migration this front was given is used for the stale-read notes (ADR-0042), which are local.
- **Later items get the relevant ones, bounded.** Implementer prompts of items whose anchors, touched files or read files, their folders or their areas match get at most `lessons.max` (5) lessons, the most relevant and the most taught first.
- **The owner curates them.** `barnraise lesson list [--file] [--area]` and `barnraise lesson delete <id>` (owner scope, audited), over two plain JSON endpoints next to the Connect API, so the proto does not change.
- On by default (`lessons.enabled`): every project has rework.

## Consequences

- A mistake corrected once reaches the next agent working in the same place before its first attempt.
- Lessons are only as good as the reasons written by checks and reviewers; a vague finding gives a vague lesson, which the owner can delete.
- Lessons accumulate; merging keeps duplicates down, and only the relevant ones ever reach a prompt.
