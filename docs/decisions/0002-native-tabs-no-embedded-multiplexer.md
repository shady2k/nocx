# ADR-0002 — Native tabs; no embedded multiplexer

- **Status:** Accepted
- **Date:** 2026-07-21
- **Related:** `AD-6`, epic `nocx-8yg` (Terminal UI), `nocx-8yg.1`, `nocx-8yg.3`,
  `nocx-2ho.3`, `nocx-2ho.5`

## Context

A local checkout of [zellij](https://github.com/zellij-org/zellij) (v0.45.0, MIT)
raised two separate questions: can we **borrow code** from it, and — the larger
one — should nocx **compose with** a multiplexer instead of building its own
session layer? Running zellij inside a nocx tab would hand it tabs, splits,
detach/reattach and session persistence, shrinking epic `nocx-8yg` and part of
`nocx-2ho` to nothing.

What the checkout actually contains:

- A Rust workspace. VT parsing via the `vte` 0.11 crate — the same parser
  Alacritty uses.
- **The terminal grid lives server-side**: `zellij-server/src/panes/grid.rs`,
  ~5000 lines implementing `vte::Perform`.
- The client renders by re-emitting ANSI into the host terminal (`crossterm`).
  There is no pixel renderer to lift.
- Plugins are WASM modules executed by `wasmi`; layouts are KDL; session
  persistence lives in `zellij-utils/src/session_serialization.rs` and
  `zellij-server/src/session_layout_metadata.rs`.

The server-side grid is not an implementation quirk — it is forced. A
multiplexer composites panes and re-emits them into a terminal it does not own,
so it must parse and model the byte stream. That is precisely what **AD-6**
forbids on our backend.

## Decision

**nocx owns tabs, sessions, and later splits natively.** No multiplexer is
embedded, vendored, or required at runtime. Zellij contributes design input
only — no code, and no place in the process tree.

## Consequences

- **AD-6 stands.** No VT grid on the Go side; render state stays in the frontend.
- We own the work: `nocx-8yg.1` (tab bar), `nocx-8yg.3` (restore tabs on
  restart), `nocx-2ho.5` (multi-session over one WS), `nocx-2ho.3` (reconnect).
  None of it gets outsourced.
- **Reconnect stays a bounded per-session byte-offset output ring** (`nocx-2ho.3`),
  not zellij's "server holds the grid and repaints on attach". That alternative
  is *closed* to us by AD-6, not merely unchosen — worth recording so it is not
  re-proposed as a shortcut.
- Zellij's session-serialization model — per-pane cwd plus the running command,
  written out and replayed to rebuild the workspace — is a **reference design**
  for `nocx-8yg.3`, and pairs with the OSC 7 cwd events in `nocx-5mn.2`.
- No Rust in the build, no FFI, no second runtime to ship or debug.
- Users can still run zellij or tmux inside a nocx tab, exactly as in any other
  terminal. That is their choice, not a product dependency.

## Revisit when

- **Remote-persistent sessions become a product requirement** — a session that
  survives the client process entirely, reattachable from another machine. That
  is the one thing a multiplexer genuinely buys that native tabs do not, and it
  would be an AD-level change, not a UI decision.
- Splits plus persistence measurably outgrow the cost of embedding an existing
  implementation. Note this is a cost argument, not a capability one: the
  capability is ours to build either way.
