# nocx

A local-first, Warp-style terminal — no cloud, no login, no telemetry. Built on
[ghostty-web](https://github.com/coder/ghostty-web) (WASM VT engine), Wails (Go + WebView),
and a custom Go backend (PTY, SSH).

> **Status:** early planning. See [`docs/vision.md`](docs/vision.md) for the product vision,
> MVP scope, and roadmap.

## What makes it different

Flawless rendering of modern agent TUIs (Claude Code, aider, …) is table-stakes; the wedge is
the *combination*, all local in one app: Ghostty-grade rendering + an integrated SSH manager +
(later) a secrets vault + (later) Warpify-style UX — with no cloud dependency.

## Repository layout

- `docs/` — living, framework-neutral source-of-truth documents.
  - `vision.md` — product vision, MVP scope, roadmap.
  - `architecture.md` — high-level architecture *(coming next)*.
  - `decisions/` — architecture decision records (append-only).
- `AGENTS.md` — rules for AI agents working in this repo *(coming next)*.
- `_bmad/`, `.claude/`, `.agents/`, `.opencode/` — vendored AI-agent tooling (the BMAD
  workflow), so contributors can develop with agents out of the box.

## Built by AI agents

Development is driven by AI coding agents using the BMAD workflow, with task tracking in
[beads](https://github.com/steveyegge/beads). The living docs in `docs/` are the source of
truth; the executable backlog lives in beads (not in prose).

## Getting started (contributors)

1. **Clone** — the agent tooling is vendored, so your agent shares the same workflow.
2. **Set your BMAD identity** (not committed): edit `_bmad/config.user.toml` (your
   `user_name` / `communication_language`) or re-run the BMAD installer to regenerate your
   local `config.yaml`.
3. **Read** [`docs/vision.md`](docs/vision.md), then `AGENTS.md` for how we work.

## License

TBD. Dependencies are MIT — preserve their copyright notices (ghostty-web © 2025 Coder;
Ghostty © 2024 Mitchell Hashimoto and contributors).
