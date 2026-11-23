# ADR-0020: CloudEvents out, signed webhooks in and out

- Status: accepted
- Date: 2026-11-23

## Context

Integrations (Slack, n8n, dashboards, other CIs) need a standard event shape and a way to trust what comes in.

## Decision

- Events leave as **CloudEvents 1.0** (type `dev.barnraise.<type>`, source `barnraise://<machine>/<project>`): live over SSE at `/v1/events`, and to outgoing webhooks declared in `barnraise.yaml`.
- Webhooks are signed with **HMAC-SHA256 over `timestamp.body`** in `X-Barnraise-Signature`, with `X-Barnraise-Timestamp` and `X-Barnraise-Delivery`, and retried with backoff.
- Inbound webhooks are always verified: GitHub with `X-Hub-Signature-256`, GitLab with `X-Gitlab-Token`, the generic endpoint with the same scheme as outgoing and a 5-minute replay window.

## Consequences

- Any system can follow or feed Barnraise without a client library.
- Webhook secrets live in the secrets store, never in the YAML.
