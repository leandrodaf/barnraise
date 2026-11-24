# ADR-0040: Services of their own for each item worktree

- Status: accepted
- Date: 2026-11-24

## Context

Items run in parallel, each in its own git worktree (ADR-0021), but a worktree isolates only files. The services the code talks to are shared: every worktree's `docker compose up` lands in a project named after its directory or after the compose file's `name:`, every test suite connects to the database in `.env`, and only the server port was per item (`PORT`, from `worktree.port_base`). One item's tests then truncate the tables another item's tests are reading, and a check fails for a reason that has nothing to do with the change it checks. Every project with a database or a compose file has this problem, so the answer is native, not a plugin.

The idea comes from [workz](https://github.com/rohansx/workz) by rohansx, which gives each git worktree its own compose project and ports. Only the idea was taken; no code was read or copied (ADR-0018).

## Decision

- Each item worktree has an identity, computed in the domain from the project and the worktree key (`I-42`, or `I-42~b` for the second attempt of a risky item): the short id `i42`, the compose project `<project>-i42`, and a port range. The range is `worktree.port_range` ports (default 1) from `port_base + (n % slots) * port_range`, which with one port is exactly the old `port_base + n % 1000`, and never passes 65535.
- The identity reaches every process of the worktree: `worktree.setup`, the worktree hooks (ADR-0041), every check (through `CheckSpec.Env`, so a check run on another machine has it there too) and the agent's session, as `PORT`, `BARNRAISE_PORTS`, `COMPOSE_PROJECT_NAME`, `BARNRAISE_ITEM` and `BARNRAISE_ITEM_SLUG`. Barnraise's own session variables keep precedence.
- `worktree.isolate.env` adds project variables as Go templates over the identity (`{{.Item}}`, `{{.Port}}`, `{{index .Ports 1}}`, ...), with `env` (reads the worktree's `include` files, already in the agent's hands), `dbURL` (points a database URL at another name) and `replace`. Templates are compiled and tried when the configuration loads. A variable may not be Barnraise's, the identity's, or one that changes every program or its traffic (`PATH`, `HOME`, proxies, `LD_*`, `DYLD_*`).
- When an item ends, its services go, once (a marker in the session folder): `worktree.isolate.teardown`, then `docker compose -p <name> down -v --remove-orphans` when compose was used (`isolate.compose`: `auto` looks for a compose file at the worktree root, `on`, `off`). This runs on integration, on close (the worktree itself stays, as before), when another machine finished the item, when the losing attempt of a risky item is dropped, and after a check run for another machine.
- Docker is not a dependency: it is called only where compose was used, and a missing `docker` is a debug line.

## Consequences

- Items in parallel no longer share test data, ports or containers; an existing `barnraise.yaml` behaves as before, plus `COMPOSE_PROJECT_NAME` (which only makes compose's choice of name explicit and per item) and the cleanup of compose projects that items started.
- `down -v` deletes the item's volumes. That is the point (they hold its test data), and the name, which includes the item, keeps it away from any project a person runs.
- The project still has to make its code read the variables (most loaders already prefer the environment over `.env`); Barnraise does not rewrite files.
