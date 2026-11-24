#!/usr/bin/env python3
"""Draws the README diagrams that are not hand-made: the owner's decisions,
accounts on every machine, the security layers, the parts of a repository and
the inside of the binary. Same look as topology.svg and item-flow.svg.

    python3 scripts/diagrams/build.py      # writes docs/img/*.svg
"""
from html import escape
from pathlib import Path

OUT = Path(__file__).resolve().parents[2] / "docs" / "img"
FONT = "-apple-system, 'Segoe UI', Helvetica, Arial, sans-serif"
MONO = "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
INK, MUTED, LINE = "#1f2328", "#57606a", "#d0d7de"
PAL = {
    "blue": ("#ddf4ff", "#0969da"),
    "green": ("#dafbe1", "#1a7f37"),
    "purple": ("#fbefff", "#8250df"),
    "orange": ("#fff1e5", "#bc4c00"),
    "gray": ("#f6f8fa", "#6e7781"),
    "red": ("#ffebe9", "#cf222e"),
    "plain": ("#f6f8fa", LINE),
}
ARROWS = {INK, MUTED} | {s for _, s in PAL.values()}


class SVG:
    def __init__(self, w, h, title, desc):
        self.w, self.h, self.title, self.desc, self.parts = w, h, title, desc, []

    def text(self, x, y, s, size=12.5, color=MUTED, weight=400, anchor="middle", mono=False):
        fam = f' font-family="{MONO}"' if mono else ""
        self.parts.append(f'<text x="{x}" y="{y}" font-size="{size}" fill="{color}" font-weight="{weight}" text-anchor="{anchor}"{fam}>{escape(s)}</text>')

    def heading(self, title, sub):
        self.text(40, 52, title, 22, INK, 600, "start")
        self.text(40, 80, sub, 14, MUTED, 400, "start")

    def box(self, x, y, w, h, title="", lines=(), pal="gray", align="middle", mono_lines=False, rx=8):
        fill, stroke = PAL[pal]
        self.parts.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}" stroke="{stroke}" stroke-width="1.2"/>')
        n = (1 if title else 0) + len(lines)
        step = 18
        cy = y + h / 2 - (n - 1) * step / 2 + 5
        tx = x + w / 2 if align == "middle" else x + 16
        if title:
            self.text(tx, cy, title, 14, INK, 600, align)
            cy += step + 1
        for ln in lines:
            self.text(tx, cy, ln, 12.5, MUTED, 400, align, mono_lines)
            cy += step

    def group(self, x, y, w, h, label, note=""):
        self.parts.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="10" fill="#f6f8fa" stroke="{LINE}" stroke-width="1.2"/>')
        self.text(x + 20, y + 28, label, 15, INK, 600, "start")
        if note:
            self.text(x + w - 20, y + 28, note, 12.5, MUTED, 400, "end")

    def arrow(self, pts, color=MUTED, dashed=False, label="", lx=None, ly=None, lcolor=None, both=False):
        d = "M " + " L ".join(f"{x} {y}" for x, y in pts)
        mid = f'#a{color[1:]}'
        dash = ' stroke-dasharray="6 5"' if dashed else ""
        start = f' marker-start="url({mid})"' if both else ""
        self.parts.append(f'<path d="{d}" fill="none" stroke="{color}" stroke-width="1.6"{dash} marker-end="url({mid})"{start}/>')
        if label:
            if lx is None:
                (x1, y1), (x2, y2) = pts[0], pts[-1]
                lx, ly = (x1 + x2) / 2, (y1 + y2) / 2 - 8
            for i, ln in enumerate(label.split("\n")):
                self.text(lx, ly + i * 16, ln, 12, lcolor or color)

    def note(self, x, y, w, h, lines, bold=""):
        self.parts.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="8" fill="#f6f8fa" stroke="{LINE}" stroke-width="1.2"/>')
        cy = y + 26
        for i, ln in enumerate(lines):
            if i == 0 and bold:
                self.parts.append(f'<text x="{x + 22}" y="{cy}" font-size="13" fill="{INK}" text-anchor="start"><tspan font-weight="600">{escape(bold)}</tspan> {escape(ln)}</text>')
            else:
                self.text(x + 22, cy, ln, 13, MUTED, 400, "start")
            cy += 20

    def save(self, name):
        markers = "".join(
            f'<marker id="a{c[1:]}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" fill="{c}"/></marker>'
            for c in sorted(ARROWS))
        body = "\n".join(self.parts)
        svg = (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {self.w} {self.h}" width="{self.w}" height="{self.h}" role="img" aria-labelledby="t d" font-family="{FONT}">\n'
               f'<title id="t">{escape(self.title)}</title><desc id="d">{escape(self.desc)}</desc>\n<defs>{markers}</defs>\n'
               f'<rect x="0.5" y="0.5" width="{self.w - 1}" height="{self.h - 1}" rx="12" fill="#ffffff" stroke="{LINE}"/>\n{body}\n</svg>\n')
        (OUT / name).write_text(svg)


def owner():
    s = SVG(1200, 640, "The owner decides, the advisor helps",
            "What waits for the owner (questions, approvals, proposals) reaches the panel, the desktop app or the CLI. The owner answers, or talks it over with the advisor, a read-only session on the strongest model that suggests an answer; the owner confirms it, edits it, asks more, or lets the advisor decide. Proposals can be triaged all at once and are applied only when the owner confirms.")
    s.heading("The owner decides; the advisor helps",
              "Nothing waits on a terminal. Every decision reaches one place, with the context and a suggested answer.")
    s.text(160, 128, "What waits for you", 13, INK, 600)
    s.box(40, 142, 240, 70, "Questions", ["an agent cannot go on without you"], "orange")
    s.box(40, 226, 240, 70, "Approvals", ["the plan, or the delivery"], "purple")
    s.box(40, 310, 240, 70, "Proposals", ["ideas the agents brought"], "blue")
    s.box(400, 190, 280, 140, "You, the owner", ["web panel · desktop app · CLI", "answer right where it shows,", "or talk it over first"], "gray")
    for y in (177, 261, 345):
        s.arrow([(280, y), (400, 260)], MUTED)
    s.box(820, 142, 340, 238, "Advisor", ["the strongest model of the project", "reads the item, the question, the timeline", "and the code, in the item's worktree", "never changes a file, runs nothing", "shows live what it reads and searches", "ends with a suggested answer"], "blue")
    s.arrow([(680, 236), (820, 236)], "#0969da", label="Talk it over")
    s.arrow([(820, 290), (680, 290)], "#0969da", dashed=True, label="suggested answer")
    s.box(400, 410, 180, 82, "Confirm · Edit", ["send the suggested", "answer, or your own"], "green")
    s.box(600, 410, 180, 82, "Ask more", ["the conversation", "goes on"], "gray")
    s.box(800, 410, 180, 82, "Decide for me", ["the advisor answers;", "kept as delegated"], "purple")
    s.box(1000, 410, 160, 82, "Triage", ["every proposal,", "8 per answer"], "orange")
    s.arrow([(540, 330), (490, 410)], MUTED)
    s.arrow([(600, 330), (690, 410)], MUTED)
    s.arrow([(890, 380), (890, 410)], "#8250df")
    s.arrow([(1080, 380), (1080, 410)], "#bc4c00")
    s.note(40, 520, 1120, 88, [
        "The item goes on at once; your words and the reasons of the advisor travel with it to the next agent's prompt.",
        "A triage applies nothing by itself: you see approve, reject or later for each proposal, keep the ones you agree with and apply them."],
        "After the answer:")
    s.save("owner.svg")


def accounts():
    s = SVG(1200, 780, "One account, logged in on every machine at once",
            "From the panel, the desktop app or the CLI the owner starts the login of one account on every machine. Each machine runs the provider's own login in its own folder and shows its link and code; the owner signs in once per machine, in any order. Only the steps travel between machines, never a credential. The account then runs on those machines only, and each machine runs as many agents as its cores and memory allow.")
    s.heading("One account, every machine, one screen",
              "Each machine runs the provider's own login and keeps it. Only the steps of the login travel; a credential never does.")
    s.box(40, 150, 250, 110, "Accounts → Log in", ["panel, desktop app, or", "barnraise account login claude --all"], "gray")
    s.box(390, 150, 270, 110, "The daemon you talk to", ["starts the login here", "and asks the paired machines:", "start · state · code · cancel"], "blue")
    s.arrow([(290, 205), (390, 205)], MUTED, label="claude or codex")
    s.text(525, 288, "to the other machines over NATS, signed with nkeys", 12, "#8250df")
    for name, y in (("Machine A", 150), ("Machine B", 290), ("Machine C", 430)):
        s.box(800, y, 360, 110, name, ["claude auth login · codex login --device-auth", "in accounts/<id> under its own data folder", "prints a link, and a one-time code (codex)"], "orange")
    s.arrow([(660, 205), (800, 205)], "#8250df")
    s.arrow([(660, 230), (730, 230), (730, 345), (800, 345)], "#8250df")
    s.arrow([(730, 345), (730, 485), (800, 485)], "#8250df")
    s.box(40, 470, 620, 110, "One block per machine, side by side", ["open the page · type the code there (codex), or paste back the page's code (claude)", "sign in once for each machine, in any order; a login nobody finishes ends in 20 min", "each one says Waiting for you · Logged in · Failed · Stopped"], "green")
    s.arrow([(800, 525), (660, 525)], "#1a7f37", dashed=True, label="link, code and state, live", lx=730, ly=515)
    s.note(40, 620, 1120, 120, [
        "the account lists the machines where it is logged in; a machine not listed never picks it.",
        "Each machine runs as many agents at once as its cores and memory allow (2 cores and 4 GB per agent, 2 GB for the system)",
        "and says which one bounds it; the owner may set the number per machine (barnraise machine agents 5 --machine B).",
        "Usage windows (5 hours, week) come from the sessions themselves; the least loaded account with room takes the next step."],
        "When a login ends well,")
    s.save("accounts.svg")


def security():
    s = SVG(1200, 690, "Security in layers",
            "Untrusted text, private data and the way out are broken apart in layers: fenced untrusted text, secrets in the keychain, one Linux user per account, a blocking hook before every tool call, a sandbox or container, an allow-list network, role scopes in the daemon, and a secret scan and audit before anything leaves.")
    s.heading("Security in layers: no prompt asks for permission",
              "The barrier is the operating system and the daemon. A model can be talked into anything; these layers cannot.")
    layers = [
        ("Untrusted text is data", "issues, comments, proposals and web pages reach agents fenced as untrusted, never as instructions", "gray"),
        ("Secrets in the system keychain", "never in YAML or in the repository; provider keys can stay out of reach behind the egress broker", "purple"),
        ("One Linux user per account", "barnraise account add --isolate: no way into the owner's home", "orange"),
        ("A blocking hook before every tool call", "compiled policy: no writes to workflows, .barnraise/, .env* or locked tests; no push to main, sudo, curl | sh", "red"),
        ("Sandbox or container", "Landlock on every session and check · bubblewrap · a container per session · a microVM", "blue"),
        ("Network by allow-list", "egress proxy with method and path rules; writes only to the project's repository; signed receipts", "green"),
        ("Scopes in the daemon", "each session gets a short-lived token with its role's scopes: an implementer cannot approve", "purple"),
        ("Before anything leaves", "secret scan (gitleaks and built-in patterns) before every push · chained event log · audit of every owner action", "orange"),
    ]
    y = 112
    for i, (t, d, pal) in enumerate(layers):
        inset = i * 18
        s.box(40 + inset, y, 1120 - 2 * inset, 54, "", [], pal, rx=8)
        s.text(60 + inset, y + 23, f"{i + 1}. {t}", 14, INK, 600, "start")
        s.text(60 + inset, y + 42, d, 12.5, MUTED, 400, "start")
        y += 62
    s.note(40, y + 8, 1120, 46, ["Barnraise capabilities says, per machine, which layers are active, what runs instead and the one command that fixes the rest."], "")
    s.save("security.svg")


def parts():
    s = SVG(1200, 540, "Only what the change touches is checked",
            "A repository with a backend, a web app and a mobile app is one project with three parts, each with its own barnraise.yaml. A change inside one part runs that part's checks only; a file of no part, or one listed in selection.all_when, runs every check; files in selection.none_when need none.")
    s.heading("One repository, several parts: only what the change touches is checked",
              "Each part has its own barnraise.yaml, found by itself: what it is, the flow of each kind and the checks that guard it.")
    s.box(40, 130, 300, 74, "backend/", ["go · lint, test"], "blue", mono_lines=False)
    s.box(40, 222, 300, 74, "web/", ["react · lint, test, e2e"], "blue")
    s.box(40, 314, 300, 74, "mobile/", ["react-native · test, device (emulators)"], "blue")
    s.text(190, 120, "parts, each with its barnraise.yaml", 12.5, MUTED)
    rows = [
        (167, "backend/internal/plan.go", "backend checks only, inside backend/", "green"),
        (259, "go.mod · Makefile · a file of no part", "every check (selection.all_when)", "orange"),
        (351, "docs/** · the backlog", "no check (selection.none_when)", "gray"),
    ]
    for y, change, result, pal in rows:
        s.box(430, y - 30, 320, 60, "", [], "plain")
        s.text(590, y + 5, change, 12.5, INK, 400, "middle", mono=True)
        s.box(840, y - 30, 320, 60, "", [], pal)
        s.text(1000, y + 5, result, 12.5, INK, 400)
        s.arrow([(750, y), (840, y)], MUTED)
    s.text(590, 120, "the change touches", 12.5, MUTED)
    s.text(1000, 120, "what runs", 12.5, MUTED)
    s.note(40, 420, 1120, 88, [
        "a check may cover another (covers), cheap checks run first and the step stops at the first failure,",
        "and the merge queue does not check again a tree that was already checked. New dependencies are looked up in OSV.",
        "barnraise check --touched mobile/src/Home.tsx shows what would run."],
        "No repeated work:")
    s.save("parts.svg")


def inside():
    s = SVG(1200, 640, "Inside the binary",
            "One Go binary, hexagonal. Inbound adapters (CLI, Connect API with gRPC, JSON and OpenAPI, MCP server, provider hooks, web panel and desktop app) call the use cases of the application, which hold the rules of the domain and reach the world through ports implemented by outbound adapters (providers, trackers, git, NATS, SQLite, network, sandbox, tools).")
    s.heading("Inside: one binary, the daemon is the only authority",
              "Only the daemon writes state, touches main and decides transitions. Everything else is a client of the same API.")
    s.text(170, 122, "In: clients of the API", 13, INK, 600)
    ins = [("CLI", "barnraise …, text or --json"), ("Connect API", "gRPC · JSON over HTTP · OpenAPI"),
           ("MCP server", "the agents' tools, by role"), ("Provider hooks", "before every tool call, status line"),
           ("Web panel · desktop app", "the same panel, served by the daemon")]
    y = 136
    for t, d in ins:
        s.box(40, y, 260, 60, t, [d], "gray")
        s.arrow([(300, y + 30), (380, y + 30)], MUTED)
        y += 72
    s.group(380, 112, 440, 368, "barnraise daemon")
    s.box(400, 156, 400, 120, "app: use cases", ["claim, deliver, review, integrate, propose,", "focus, discuss with the advisor, log in…", "the runtime that starts sessions"], "blue")
    s.box(400, 292, 400, 100, "domain: pure rules", ["item, flow, rework, capacity, policy, capabilities", "standard library only"], "purple")
    s.box(400, 408, 195, 56, "SQLite", ["append-only event log"], "plain")
    s.box(605, 408, 195, 56, "ports", ["small interfaces"], "plain")
    s.text(1010, 122, "Out: adapters behind the ports", 13, INK, 600)
    outs = [("Providers", "claude · codex · gemini · ACP · API keys"), ("Trackers", "github · gitlab · jira · linear · plugin"),
            ("git · NATS", "claims, main, events between machines"), ("Network", "tsnet · tailscale · LAN · plugin · egress"),
            ("Machine", "landlock · microVM · tools · logins")]
    y = 136
    for t, d in outs:
        s.box(900, y, 260, 60, t, [d], "orange")
        s.arrow([(820, y + 30), (900, y + 30)], MUTED)
        y += 72
    s.note(40, 512, 1120, 96, [
        "a project's repository holds only configuration (barnraise.yaml and .barnraise/), never state and never secrets.",
        "State is derived from the events, so there is full history and any step can be redone. The dependency rule is",
        "enforced by tests and the linter: domain uses only the standard library, app only domain and ports, outbound adapters never call the app."],
        "Where things live:")
    s.save("inside.svg")


if __name__ == "__main__":
    for f in (owner, accounts, security, parts, inside):
        f()
    print("written:", ", ".join(p.name for p in sorted(OUT.glob("*.svg"))))
