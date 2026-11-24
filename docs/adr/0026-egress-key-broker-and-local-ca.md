# ADR-0026: Egress key broker and a local CA

- Status: accepted
- Date: 2026-11-24
- Builds on: [ADR-0023](0023-security-by-os-layers.md)

## Context

Sessions on key-based providers (OpenRouter and the endpoints under `providers:`) received the real API key in `BARNRAISE_PROVIDER_KEY`. It was never in a file or on a command line, but the agent's own processes run as the same user as the CLI, so a prompt injection could make the agent read its environment and send the key out. The egress proxy already sits between every session and the network, yet only tunneled TLS by host, so it could not help.

The idea of holding the secret in the proxy and handing the client only a sealed token comes from [superfly/tokenizer](https://github.com/superfly/tokenizer) (Apache-2.0). Only the idea was taken; no code.

## Decision

- The daemon owns a local CA in `<data dir>/egress/` (`ca.pem`, `ca-key.pem`, 0600, ECDSA P-256, created on first start). The proxy terminates TLS with leaf certificates of that CA **only** for the hosts it must see: the key-based providers' hosts, hosts with method/path rules ([ADR-0028](0028-egress-rules-by-method-host-path.md)) and `policy.egress.inspect`. Every other `CONNECT` stays a plain tunnel.
- A key broker (`internal/adapters/out/egress`, port `ports.KeyBroker`) hands each session a random placeholder (`barnraise-sealed-` + 48 hex characters) bound to the provider's host, and forgets it when the session ends. On a terminated request to that host the proxy swaps the placeholder in the headers for the real key. A request the proxy sees that carries a live placeholder toward any other host is refused. The real key stays in the daemon's memory and keychain.
- Sessions trust the CA through the usual variables (`SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`, `PIP_CERT`, `CARGO_HTTP_CAINFO`, `DENO_CERT`, `CODEX_CA_CERTIFICATE`), all pointing at a bundle in the session folder made of the system's CA bundle followed by the proxy's CA, so tools that replace their roots lose nothing.
- Inside a terminated connection the TLS server name must equal the `CONNECT` host, and requests always go to the `CONNECT` host whatever their `Host` header says. Upstream connections are verified against the system roots.
- Fallback: without the egress proxy, or when the daemon finds no system CA bundle to extend or cannot create the CA, nothing is terminated and the key reaches the session as before. The daemon logs which case applies.

## Consequences

- A leaked environment no longer leaks a usable provider key on machines with the proxy.
- The daemon holds a CA key that every session trusts; it is local, 0600, and only used for the terminated hosts. Anyone who can read the data dir could already read the keychain-backed state of the daemon.
- A CLI that pins certificates or ignores all the trust variables cannot reach a terminated host; today no supported CLI does.
- HTTP/1.1 is spoken to the client on terminated connections; the upstream side may use HTTP/2.
