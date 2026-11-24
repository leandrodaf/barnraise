# Barnraise panel

The owner's panel, served by the daemon at `/` and opened by the desktop app
(`../desktop`). Svelte 5, TypeScript, Tailwind and shadcn-svelte; the API
client is generated from `../proto` (`pnpm gen`). See
[ADR-0045](../docs/adr/0045-panel-and-desktop-app.md).

- `pnpm dev` — the panel on http://localhost:5173, talking to the local
  daemon (`BARNRAISE_URL` to point elsewhere).
- `pnpm check` — type check.
- `pnpm build` — build into `../internal/adapters/in/panel/dist` (committed);
  `pnpm build:desktop` builds the desktop app's launcher page.
- `make web` from the repository root does all of it.

The panel shows what the daemon decides and never decides about items itself
(`internal/adapters/in/panel/panel_test.go` checks it).
