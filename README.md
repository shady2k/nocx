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

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Go | 1.26 | [go.dev](https://go.dev/dl/) |
| Node | 22 | [nodejs.org](https://nodejs.org/) |
| Wails CLI | v2 | `go install github.com/wailsapp/wails/v2/cmd/wails@latest` |
| gofumpt | latest | `go install mvdan.cc/gofumpt@latest` |
| golangci-lint | **v1.64.8** | `go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.8` |
| bd (beads) | 1.x | `brew install beads` |

> ⚠️ golangci-lint **must** be v1.64.8 — the config (`.golangci.yml`) uses the v1
> schema, and golangci-lint v2 rejects it. Pinning is enforced in CI.

## Getting started

```bash
git clone <repo-url> && cd nocx

# 1. Install the git hooks (REQUIRED first step)
make hooks

# 2. Set up the issue tracker (skip if you don't use beads)
bd bootstrap

# 3. Install frontend dependencies
cd frontend && npm ci && cd ..

# 4. Run in development mode
wails dev
```

`bd bootstrap`, not `bd init`: the backlog lives in a Dolt database that git does
not carry, and bootstrap is the command that knows where to get it — it clones
from the configured remote, and falls back to the tracked `.beads/issues.jsonl`
only if that is unavailable. A clone without this step has no issue database at
all. `bd init --from-jsonl` exists, but it builds a history that has diverged
from the remote, so keep it for recovery rather than setup.

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

CI runs on release branches (`release/**`), version tags (`v*`), and manual
dispatch (GitHub Actions, macos-latest for Go job, ubuntu-latest for frontend).
Everyday gating on `main` is enforced locally by the pre-commit hook and
`make ci` — they run the identical set of checks.

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

Development is driven by AI coding agents using the BMAD workflow. The agent
tooling is vendored — clone and go.

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
