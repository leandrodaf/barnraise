# ADR-0045: One panel for the browser and the desktop app, with no rules in it

- Status: accepted
- Date: 2026-11-24

## Context

The owner wants to follow the cluster from a browser and from a desktop app, and the two must not drift apart or block each other. The first panel was plain JavaScript; it grew, and it began to decide things that belong to the daemon: which column an item is in, what an approval approves (by matching step names), when an account counts as limited, when a metric is out of bounds.

## Decision

- **One panel, built from `web/`** (Svelte 5, TypeScript, Tailwind, shadcn-svelte components) into `internal/adapters/in/panel/dist`, embedded in the daemon and served at `/`. `dist/` is committed so `go build` needs no JavaScript toolchain; `make web` rebuilds it. The page loads one module and no inline code, so the daemon's CSP (`default-src 'self'`) stays as it is; a test checks it, and checks the source for HTML injection and browser dialogs.
- **The client is generated from the proto** (Connect-ES, `web/buf.gen.yaml`), so a field that changes breaks the panel's type check, not the screen.
- **The desktop app is a separate Go module** (`desktop/`, Wails v2): Wails needs cgo and WebKitGTK, and the `barnraise` binary stays pure Go (ADR-0001). The app does what a page cannot: find this machine's daemon, read the owner token, start the daemon as its own process (the systemd user service, or `barnraise daemon` in its own session) or reach another machine over https. Then it opens the daemon's own panel. Closing or updating the app never stops the agents, and the panel always matches the daemon's API.
- **The daemon decides, the panel shows.** For each item the overview carries its flow (the project's steps and where the item is), what the owner approves now (`awaits`, from the same function `Approve` checks with), its board column and one signal to read first. Accounts carry their outlook (pace, reset, when they run out, whether they are limited, from `Account.Available`). Metrics name the numbers past their limit. The overview filters by project and adds the usage up. A test fails when the panel's source matches step names or writes metric limits.
- **Versions:** each binary carries the time of its commit; builds compare as releases when both are releases and by commit time otherwise, and a build that cannot be placed is never replaced (a development build is no longer taken over by a peer's older commit). The daemon says whether a machine of the cluster runs a newer build; the panel shows the version, that notice, and asks to reload when the daemon was updated under an open page.

## Consequences

- The browser and the app are the same page; a screen is written once.
- Every rule the owner sees has one home and one test, in Go.
- Building the desktop app needs Wails (rebuilt with a current `golang.org/x/tools` for Go 1.27) and WebKitGTK 4.1; `make desktop` puts `barnraise` next to it.
