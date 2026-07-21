---
title: nocx — Product Vision
status: ready
created: 2026-07-20
updated: 2026-07-20
---

# nocx — Product Vision

## 1. One-line positioning

Tabby's SSH ergonomics on a Ghostty-grade engine, fully local: comfortable SSH + terminal rendering that handles modern agent TUIs, no cloud, no login, no telemetry.

## 2. Problem / motivation

Developers who run AI-agent TUIs locally (Claude Code, aider) have to choose between engines that render those TUIs flawlessly and tools that are actually comfortable to live in — and no single tool gives both without a cloud dependency.

- **Warp** — great rendering and UX, but forces cloud, login, and telemetry.
- **Tabby** — local, with the ergonomics developers love (built-in secrets vault, strong SSH), but its terminal engine renders modern agent TUIs poorly.

There is no local-first terminal that pairs a Warp/Ghostty-grade engine with Tabby-style comfort.

**Competitive-honesty note.** Rendering alone is not the wedge — several tools already render well.
- **Ghostty / WezTerm / Kitty / iTerm2** render excellently, but none ships an integrated SSH manager + vault + Warpify-style UX + GUI configuration in one app; Ghostty in particular has no vault, no SSH manager, and no GUI config.
- **Tabby** has the vault + SSH manager, but a weak engine.
- **Warp** has the UX, but requires the cloud.

nocx's bet is the *combination*, delivered locally in one customizable app — not any single feature.

## 3. Target users

The author, as a daily driver, plus a few work colleagues. This is a personal / small-team tool, not a public launch.

The profile: a developer who runs AI-agent TUIs locally, wants a local-first / no-cloud tool, and values solid SSH ergonomics (and later a built-in secrets vault).

## 4. Differentiator — the combination

Flawless rendering of modern agent TUIs is **table-stakes**, not the differentiator. It is where Tabby fails and where Ghostty/WezTerm/Kitty/iTerm2 already succeed, so on its own it wins nobody over.

The differentiator is the **combination**, all local and in one customizable app:

**Ghostty-grade rendering + integrated SSH manager + (Phase 2) secrets vault + (Phase 2) Warpify-style UX + GUI configuration — no cloud.**

No competitor covers this whole set locally: Ghostty renders but lacks the vault/SSH-manager/GUI-config; Tabby has vault + SSH but a weak engine; Warp has the UX but needs the cloud.

## 5. MVP scope

### IN v1
- Terminal engine that renders modern agent TUIs flawlessly (true-color, mouse-passthrough, bracketed-paste for TUI fidelity)
- Tabs; duplicate tab; restore tabs on restart
- Copy folder path; new-tab-in-same-cwd
- SSH client (basic — *not* the vault)
- Change font + size; switch color schemes
- Copy-on-mouse-select; right-click paste
- Hotkeys / keybindings
- Clickable links/paths (OSC 8, cmd+click)

### PHASE 2 (deferred)
- Secrets vault
- Warpify-style UX (blocks / completions / input-editor extended into nested shells)
- Splits / panes
- Scrollback + find-in-output (search)

### OUT (non-goals)
- Cloud sync
- Mandatory login
- Telemetry

## 6. Strategic roadmap

Phases as one-liners. The detailed, executable backlog lives in **beads**, not in this doc.

- **Phase 1 — MVP.** Local terminal with agent-TUI-grade rendering, tabs/cwd features, basic SSH client, GUI config. macOS.
- **Phase 2 — Comfort layer.** Secrets vault + Warpify-style UX + splits/panes + scrollback search.
- **Phase 3 — Ask an agent + reach.** Natural-language query from the terminal to any AI model the user chooses — bring-your-own, including a fully local model; expand to Windows / Linux.

## 7. Tech stack

- **xterm.js** — WebGL VT engine (MIT).
- **Wails** — Go + WebView shell (MIT).
- **Custom Go backend** — PTY and SSH now; vault later.

**MIT attribution obligation.** Preserve the copyright notices for xterm.js (© The xterm.js authors, © SourceLair Private Company, © Christopher Jeffrey) and @wterm/dom (Apache 2.0).

**Architectural spine — OSC 7 / 133 shell integration.** The VT + shell-integration layer is one spine, not several features. Nailing it yields the agent-TUI rendering, the cwd-dependent features (copy-folder-path, duplicate-tab-in-cwd), and the foundation for a future local Warpify at once. Warpify's core mechanic is a shell-integration marker in the shell RC plus a bootstrap script that enables blocks/completions/input-editor inside nested shells (SSH/docker/gcloud/poetry) across bash/zsh/fish — this has no cloud dependency, so "no cloud" costs nothing to honor.

## 8. Platform & distribution

macOS first (the author's own machine). Windows and Linux later (Phase 3). Builds are produced via GitHub Actions CI and shared with colleagues directly — no app-store or packaged distribution, and no formal support.

## 9. Success criteria

Personal and honest: **"I built it and it works."** Concretely — I can daily-drive nocx without falling back to Warp or Tabby, and a few colleagues can use it too. No adoption targets, no revenue, no moat to defend.

## 10. Non-goals

- Cloud sync, mandatory login, telemetry — ever, not just in MVP.
- Investor-grade positioning or a public launch.
- Being everything to everyone: nocx serves the author's own workflow first.

## 11. Open questions / assumptions to confirm

- **Vault (Phase 2):** single-machine, no sync (confirmed). Exact crypto/UX is a Phase-2 implementation decision — how secrets are stored, encrypted, and surfaced (e.g. OS keychain vs. app-managed encrypted store).
- **SSH ↔ vault integration:** how the SSH client and the vault connect once the vault lands.
- **"Ask an agent" (Phase 3):** precisely how natural-language queries reach a local / BYO AI from the terminal.
- **Licensing:** confirm any obligations beyond those documented in the README License section (xterm.js MIT, @wterm/dom Apache 2.0).
