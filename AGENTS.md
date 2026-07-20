# AGENTS.md — Working rules for AI agents on `nocx`

`nocx` is a local-first, Warp-style terminal (Go backend + ghostty-web frontend + Wails v2
desktop). This file is the operating contract for **any** AI agent (Claude Code, Cursor,
OpenCode, …) contributing to the repo. Read it before writing code.

## Read first (sources of truth)

- [`docs/vision.md`](docs/vision.md) — what we're building, MVP scope, roadmap.
- [`docs/architecture.md`](docs/architecture.md) — the architecture spine: invariants
  (`AD-1`…`AD-10`), module boundaries, the WebSocket protocol. **The ADs are binding.**
- The task backlog lives in **beads** (`bd`), not in prose. Get work with `bd ready`.

## Repository layout

- `docs/` — living source-of-truth docs (`vision.md`, `architecture.md`, `decisions/` ADRs).
- `AGENTS.md` — this file (the agent rules). `CLAUDE.md` only points here.
- `_bmad/`, `.claude/`, `.agents/`, `.opencode/` — vendored BMAD agent tooling.
- Code directories appear as the app grows — follow the module map in `docs/architecture.md`.

## How we work

1. Take a `ready` task from beads (`bd ready`); mark it in-progress.
2. Read the relevant `AD`(s) in `docs/architecture.md` before touching a boundary.
3. **TDD**: red → green → refactor. Write the failing test first.
4. Keep it green: `golangci-lint`, `gofumpt`, and tests all pass (pre-commit runs them).
   No merge without green CI (GitHub Actions).
5. Update the task in beads; record any non-obvious decision as an ADR in `docs/decisions/`.

## Engineering rules (non-negotiable)

- **Interface-first + DI.** Every module lives behind an interface, wired at a single
  composition root. Depend on abstractions, obey SRP, keep modules trivially replaceable.
- **Quality gates from every commit:** strict `golangci-lint`, `gofumpt` formatting,
  pre-commit hooks (lint + format + test), mandatory tests.
- **Observability:** structured logging via Go `log/slog` behind the logging interface —
  no ad-hoc `fmt.Println`.
- **Clean-only:** no backward-compatibility shims (greenfield — break & refactor freely),
  no dead code (delete it), no quick-win hacks. YAGNI — don't build speculative features.
- **Respect the spine.** Don't violate an `AD` to save time; if an `AD` is wrong, change it
  in `docs/architecture.md` deliberately rather than routing around it. E.g.: never wrap PTY
  bytes in JSON-RPC (AD-1 data plane); the backend never sniffs the byte stream (AD-6);
  session-id is server-authoritative (AD-7).

## Stack

- **Backend:** Go — `pty`, `ssh` (via `golang.org/x/crypto/ssh`), `session`, `transport`,
  `config`. One core, multiple build targets.
- **Frontend:** ghostty-web (WASM VT) + TypeScript UI. Terminal render state lives here (AD-6).
- **Desktop shell:** Wails v2 (macOS first).
- **Transport:** one WebSocket — raw **binary** data plane + **JSON-RPC 2.0** control plane (AD-1).

## Current top risk

Before building on the cwd / OSC features (AD-5 / AD-6): **verify that ghostty-web actually
exposes OSC 7 / OSC 133 handlers (plus scrollback / selection / resize APIs)** — this is
undocumented and unverified, yet load-bearing. It is the first beads task (a de-risk spike).
