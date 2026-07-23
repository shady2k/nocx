# nocx

A local-first terminal with GPU-accelerated rendering and a built-in SSH
manager — no cloud, no login, no telemetry.
**Stack:** Go backend (PTY, SSH, session, transport) + xterm.js (WebGL) frontend +
Wails v2 desktop shell, connected over one WebSocket carrying a raw binary data
plane and a JSON-RPC 2.0 control plane.

**Status:** MVP in progress. Local PTY over WebSocket works; SSH client, tabs,
and cwd features are under active development. macOS-first.

## What makes it different

Flawless rendering of modern agent TUIs (Claude Code, aider, …) is table-stakes;
the wedge is the *combination*, all local in one app: Ghostty-grade rendering +
an integrated SSH manager + (later) a secrets vault + (later) shell-integration
blocks, completions, and input-editor in nested shells — with no cloud
dependency.

## Install (macOS)

Released builds are on the [Releases page](https://github.com/shady2k/nocx/releases). Download the `.dmg`, open it, and drag **nocx** into Applications.

There is no Apple Developer ID, so the build is unsigned and macOS quarantines it on download. Clear that once, on first install:

```bash
xattr -dr com.apple.quarantine /Applications/nocx.app
```

Then open nocx normally. This is required only the first time — later in-app updates fetch the build directly and do not re-quarantine it. Confirm the version any time with `nocx --version`.

> No publisher signature and no notarization; the reasoning is in [ADR-0003](docs/decisions/0003-distribution-without-a-developer-id.md). Update integrity is enforced by an ed25519-signed manifest, not by Gatekeeper.

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Go | 1.26 | [go.dev](https://go.dev/dl/) |
| Node | 24 | [nodejs.org](https://nodejs.org/) |
| Wails CLI | v2 | `go install github.com/wailsapp/wails/v2/cmd/wails@latest` |
| gofumpt | latest | `go install mvdan.cc/gofumpt@latest` |
| golangci-lint | **v1.64.8** | `go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8` |
| bd (beads) | **≥ 1.1.0** | `brew install beads` |

> ⚠️ golangci-lint **must** be v1.64.8 — the config (`.golangci.yml`) uses the v1
> schema, and golangci-lint v2 rejects it. Pinning is enforced in CI.

> ⚠️ `bd` must be **≥ 1.1.0**. Older builds (e.g. 1.0.3, which some distros and
> nixpkgs still ship) misread the tracker's dependency schema: `bd stats` errors,
> and — worse — the auto-export strips every dependency edge from
> `.beads/issues.jsonl`, which the pre-commit hook then commits. Check with
> `bd version` before enabling hooks.

**On NixOS / without Homebrew.** `brew` and `npm i -g` don't work here — the
latter writes into the read-only Nix store. Install `go`, `nodejs_24`, `gofumpt`,
and `uv` from nixpkgs, put `~/go/bin` and `~/.local/bin` on your `PATH`, then get
the rest through the language toolchains:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8   # exactly this — nixpkgs ships v2, which rejects .golangci.yml
CGO_ENABLED=0 go install github.com/steveyegge/beads/cmd/bd@latest        # server-mode bd; add gcc only for the embedded cgo build
uv tool install graphifyy && graphify install
```

`bd` is not in upstream nixpkgs, so `go install` is the clean route (or package
it yourself) — either way confirm `bd version` is **≥ 1.1.0**, since a system or
nixpkgs build can lag. The `beads-superpowers` plugin installs via `claude` — see
[Agent tooling](#agent-tooling).

## Getting started

```bash
git clone <repo-url> && cd nocx

# One command: git hooks, issue tracker, and both dependency trees
make init

# Run in development mode
wails dev
```

> `make init` assumes the [Prerequisites](#prerequisites) and [Agent tooling](#agent-tooling)
> are already installed — it sets up the repo (hooks, backlog, dependencies), not
> your machine.

`make init` is safe to re-run, and does four things:

| Step | Why it matters |
| --- | --- |
| `git config core.hooksPath .githooks` | Installs the quality gate and the tracker sync. Without it nothing is enforced and issue state never leaves your machine. |
| `bd bootstrap` | Fetches the issue database. Skipped with a note if `bd` is not installed. |
| `npm ci` (root) | `@playwright/test`, for the e2e suite. |
| `npm ci` (frontend) | The app's own dependencies. |

`bd bootstrap`, not `bd init`: the backlog lives in a Dolt database that git does
not carry, and bootstrap is the command that knows where to get it — it clones
from the configured remote and falls back to the tracked `.beads/issues.jsonl`
only if that is unavailable. A clone without this step has no issue database at
all, and `bd ready` will tell you so. `bd init --from-jsonl` exists, but it
builds a history divergent from the remote, so keep it for recovery, not setup.

The e2e suite additionally needs its browser once: `npx playwright install chromium`.

The pre-commit hook runs on every `git commit` and enforces:
- `gofumpt` — format check (fails if any file needs formatting)
- `golangci-lint` — lint
- `go test -race -count=1 ./...` — tests with race detector
- `prettier --check` — frontend format check
- `eslint` — frontend lint
- `tsc --noEmit` — frontend type check
- `vitest` — frontend tests

It then writes `.beads/issues.jsonl` and stages it, so a commit carries the issue
state it describes. That step runs last, so a failed gate never leaves the
tracker export staged for a commit that does not happen.

The pre-push hook pushes the issue database itself with `bd dolt push`. That is
what a fresh clone reads — the tracked JSONL is only bootstrap's last resort — so
skipping it leaves collaborators on a backlog that looks current and is not. If
`bd` is missing or this clone has no database, both hooks step aside silently; a
genuine sync failure stops the push and says so, and `git push --no-verify`
overrides it.

All four frontend gates FAIL with an actionable message if `node_modules` is absent (run `cd frontend && npm ci`).

Run locally without committing: `make ci` (close mirror of CI — runs the same static analysis and tests, but validates against your existing `node_modules` rather than reinstalling).

## Quality gates

Every commit must pass:

| Gate | Pre-commit | `make ci` | CI (GitHub Actions) |
|------|-----------|-----------|---------------------|
| gofumpt (format) | ✓ | ✓ | ✓ |
| golangci-lint | v1.64.8 | v1.64.8 | v1.64.8 |
| `go test -race` | ✓ | ✓ | ✓ |
| `go build ./...` | — | ✓ | ✓ (macos-latest) |
| `prettier --check` | ✓ | ✓ | ✓ |
| `eslint` | ✓ | ✓ | ✓ |
| `tsc --noEmit` | ✓ | ✓ | ✓ |
| `vitest` | ✓ | ✓ | ✓ |
| `npm run build` | — | ✓ | ✓ |

CI (`ci.yml`) runs on release branches (`release/**`) and manual dispatch, and
is called by `release.yml` on a version tag (`v*`) so a release gates on a green
suite (GitHub Actions, macos-latest for the Go and e2e jobs, ubuntu-latest for
the frontend). Everyday gating on `main` is enforced locally by the pre-commit
hook and `make ci` — they run the identical set of checks.

## Task tracking — beads (bd)

The executable backlog lives in beads, not markdown:

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

See `AGENTS.md` for the full workflow.

## Sources of truth

| File | What it contains |
|------|------------------|
| `AGENTS.md` | Binding engineering rules for all contributors (human and AI) |
| `docs/architecture.md` | Architecture spine with AD-1..AD-10 invariants |
| `docs/vision.md` | Product vision, MVP scope, roadmap |
| `docs/decisions/` | Architecture Decision Records (append-only) |

## Repository layout

```
AGENTS.md               — binding contributor contract
Makefile                — lint, format, test, build, dev, ci, hooks
.githooks/pre-commit    — pre-commit hook (POSIX sh)
.github/workflows/      — CI workflows

docs/                   — living docs (vision, architecture, decisions/)

internal/               — Go backend
  app/                    composition root
  config/                 settings, themes
  log/                    structured logging (slog adapter)
  pty/                    local pseudo-terminals
  session/                session registry and lifecycle
  ssh/                    SSH client (x/crypto/ssh)
  shellintegration/       OSC 7/133 substrate
  transport/              WebSocket server

frontend/               — TypeScript frontend (xterm.js + Wails)
  src/
    renderers/            xterm.js, wterm (switchable)
    tabs.ts               tab manager + WS client
    ipc.ts                WebSocket protocol
    main.ts               app entry
  package.json

_bmad/, .claude/,
.agents/, .opencode/    — vendored agent tooling
```

## Built by AI agents

Development is driven by AI coding agents. The tooling comes in two layers that
install differently:

- **Vendored — clone and go.** The BMAD workflow lives in the repo under
  `_bmad/`, `.claude/`, `.agents/`, `.opencode/`.
- **Per-machine — you install it.** The issue tracker and the Claude Code agent
  tooling below.

### Agent tooling

Install these once per machine — they are **not** vendored, and `make init` does
not install them:

| Tool | Needed for | Install |
|------|------------|---------|
| `bd` (beads) | The backlog — **required** (also in [Prerequisites](#prerequisites)) | `brew install beads` (or `npm i -g @beads/bd`) |
| [`beads-superpowers`](https://github.com/DollarDill/beads-superpowers) plugin | Superpowers skills + the `bd` session hooks — **recommended** | `claude plugin marketplace add DollarDill/beads-superpowers` then `claude plugin install beads-superpowers@beads-superpowers-marketplace` |
| [`graphify`](https://github.com/Graphify-Labs/graphify) | Knowledge-graph code search — **optional** | `uv tool install graphifyy` then `graphify install` |

- **Install `bd` before the plugin.** The plugin's hooks call `bd` on every
  session start, so a missing `bd` makes them fail.
- The `beads-superpowers` plugin bundles the Superpowers skill system with the
  Beads integration. It also targets Codex, OpenCode, Cursor and others — the
  [marketplace README](https://github.com/DollarDill/beads-superpowers) has the
  per-agent variant. (Inside a running session the same two steps are
  `/plugin marketplace add …` and `/plugin install …`.)
- `graphify` builds the code knowledge-graph under `graphify-out/`. **That map is
  committed**, so [`graphify-out/GRAPH_REPORT.md`](graphify-out/GRAPH_REPORT.md)
  is readable with nothing installed. Install the CLI only to rebuild it
  (`/graphify .`) or query it live (`/graphify query "…"`). Without graphify,
  code search falls back to plain grep/glob — it still works, just slower and
  less precise.

### Set your BMAD identity

```bash
# If you have the BMAD CLI installed:
#   bmad setup
# Otherwise, edit _bmad/config.user.toml with your user_name and
# communication_language (not committed).
```

## License

TBD. Dependencies are MIT and Apache 2.0 — preserve their copyright notices:

- `@xterm/xterm` — MIT, copyright:
  - © 2017–2019 The xterm.js authors
  - © 2014–2016 SourceLair Private Company
  - © 2012–2013 Christopher Jeffrey
- `@xterm/addon-*` — MIT, © The xterm.js authors (each addon LICENSE repeats its own line)
- `@wterm/dom` — Apache 2.0 (no explicit copyright holder declared in package metadata or LICENSE)
- `vite` — MIT, © 2019–present VoidZero Inc. and Vite contributors (devDependency, build-time only)
