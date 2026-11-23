# ADR-0005: ConnectRPC from protobuf, with a JSON face

- Status: accepted
- Date: 2026-11-23

## Context

Every interface (CLI, MCP, panel, CI, bots, other people's tools) must use the same contract. Some clients want speed and generated types; others want `curl`.

## Decision

- The API is one protobuf service, `barnraise.v1.BarnraiseService`, in `proto/barnraise/v1/barnraise.proto`, linted and checked with `buf breaking` in CI.
- It is served with **ConnectRPC**: the same handler answers gRPC, gRPC-Web and the Connect protocol (plain HTTP POST with JSON). Go code and the OpenAPI document are generated with `buf generate` (`protoc-gen-go`, `protoc-gen-connect-go`, `protoc-gen-connect-openapi`) and the OpenAPI is served at `/v1/openapi.yaml`.
- Proto field names are snake_case and mirror the JSON the domain uses in events. The Connect handler uses a JSON codec with proto names, so responses are snake_case too; requests also accept lowerCamelCase.
- The local transport is a unix socket (0600); TCP is loopback by default and needs TLS beyond it.

## Consequences

- `curl` works without a client library; typed clients can be generated for Go, TypeScript and Python.
- Incompatible changes need `barnraise.v2`.
- One JSON shape everywhere: API responses, event payloads and webhooks use the same names.
