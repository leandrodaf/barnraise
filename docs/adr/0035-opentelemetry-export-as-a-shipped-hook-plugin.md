# ADR-0035: OpenTelemetry export as a hook plugin shipped in the binary, OTLP/HTTP JSON without the SDK

- Status: accepted
- Date: 2026-11-24

## Context

Owners who already run an observability stack want Barnraise's work there: how long items and steps take, where rework happens, and what each agent run costs in tokens and money (the `agent.usage` ledger). Only some users want it, so by the owner's rule it is opt-in. [OpenTelemetry](https://opentelemetry.io) (CNCF, Apache-2.0) and its OTLP protocol are the common way in; only the specification is used here, no code. The official Go SDK and exporters would add a large dependency tree to a single binary built with `CGO_ENABLED=0`.

## Decision

- The export is a **hook plugin** (docs/plugins.md, kind `hook`) **shipped inside the barnraise binary**: `command: barnraise`, `args: [plugin, otel]`. It runs as a separate process like any plugin, so a slow or failing collector never touches the daemon, uses the existing restart, environment and secrets rules, and needs nothing else installed.
- `internal/adapters/out/otlp` turns events into spans: one trace per item with ids derived from the project and item (so spans join up across plugin restarts and machines); spans for the item, each step, each ledger entry (`gen_ai.*` token attributes, `barnraise.cost_usd`, duration) and each check result. Step spans need the finished step's name, so `item.step.finished` carries it in `extra.step` (see ADR-0034).
- Spans are sent as **OTLP/HTTP with the JSON encoding**, written with the standard library from the OTLP specification: batched (every `interval`, or at `batch` spans, and on shutdown), up to 4096 kept while the collector is down. The endpoint and headers come from the plugin's `options` and the standard `OTEL_EXPORTER_OTLP_*` variables, credentials only through `secrets`.

## Consequences

- No new Go dependency; the export is off until a project declares the plugin.
- Hooks see only the events of their list; a plugin started in the middle of a step closes that step's span with `barnraise.start_unknown`.
- Only traces are exported; metrics and logs over OTLP are left for later, as is the protobuf encoding.
