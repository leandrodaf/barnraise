# ADR-0015: English inside, the reader's language outside, one word per concept

- Status: accepted
- Date: 2026-11-23

## Context

Prompts are repeated in every step, so their size matters; people read best in their own language; and the same concept under different names (in the CLI, YAML, panel, docs) causes mistakes for people and agents.

## Decision

- Code, CLI, YAML keys, Barnraise's docs and internal prompts are always **English**. What goes to people (items, comments, commits, changelog, reports, questions) uses the project's `language:` or each person's language.
- Each concept has **one English name and one fixed translation per language** (the glossary in the README). A vocabulary test (`internal/archtest/vocab_test.go`) scans Go strings and comments, templates, the panel and the docs, and fails on words outside the glossary or on panel translations that differ from it.
- "Issue" is only used for the thing inside GitHub or GitLab.

## Consequences

- English prompts cost fewer tokens, every step.
- Adding a language means adding its glossary column and translations.
