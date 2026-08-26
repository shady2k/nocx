---
title: Keep PTY bytes off the Wails v3 binding transport
status: ready
created: 2026-08-26
related: nocx-svxik, nocx-mgbjx
review: BMAD Mary/John/Winston/Amelia/Paige roles applied — reported behavior, terminal JTBD, architecture, regression proof, documentation
---

# Keep PTY bytes off the Wails v3 binding transport

## 1. Problem

On Linux after the Wails v3 migration, OpenCode became slow after a request and commands waiting for interactive input can appear hung. The concrete report is `npx @deepseek-ai/dsh web` during its interactive installation.

The PTY data plane itself still uses the binary WebSocket required by AD-1. A side effect beside it does not: every received output chunk calls `log.debug` in `TerminalContent`, and the logger forwards every line through the generated `main.WailsApp.Log` binding. Wails v3 implements its default binding transport as one HTTP `POST`/`fetch` per call. The call is fire-and-forget, so sustained terminal output creates an unbounded queue of fetches, promises, binding dispatches, and file-log writes in the same WebKitGTK process and browser event loop that must accept keyboard events and send PTY input.

This per-chunk log predates Wails v3. The regression is the transport under it: the v2 native bridge became the v3 HTTP binding transport.

## 2. Decision

Remove the per-output-chunk debug log from `TerminalContent`. Rendering remains the only synchronous consequence of receiving a PTY chunk; attention and block-size bookkeeping retain their existing bounded callbacks.

Keep lifecycle logs (`session opened`, `session exited`, renderer readiness) and opt-in decision tracing. They describe state transitions and do not scale with byte chunks.

No replacement metric is added. Chunk counts are not a product fact, and batching or rate-limiting them would preserve a second side channel beside the data plane for no operational value.

## 3. Rejected alternatives

- **Batch or rate-limit chunk logs.** Still couples the terminal hot path to the desktop-shell binding transport and adds timer/flush lifecycle.
- **Replace Wails' HTTP binding transport with a custom WebSocket transport.** A framework-wide transport fork is larger than the defect and creates a second WebSocket owner beside AD-1.
- **Throttle or drop PTY output.** Forbidden by AD-10; bytes remain lossless, ordered, and backpressured only through the existing per-session credit.
- **Special-case OpenCode or DeepSeek Harness.** Both are ordinary PTY programs; the defect is below them.

## 4. Architectural and security invariants

- AD-1 remains unchanged: PTY bytes travel only as binary frames on the nocx WebSocket.
- AD-6 remains unchanged: the backend does not inspect terminal bytes.
- AD-10 remains unchanged: no output is dropped and no new unbounded queue is introduced.
- No terminal content, command text, or secret-bearing output is copied into logs.

## 5. Acceptance proof

1. A focused frontend regression test drives repeated real `SessionHandle.onData` callbacks through `TerminalContent`, observes every chunk reaching the renderer, and observes no debug/backend-log call per chunk.
2. Existing raw-input tests still prove renderer `onData` reaches the session byte-for-byte while a command owns the grid.
3. The focused frontend test file passes.
4. The actual Wails v3 Linux app is launched and a real PTY command that prints output, reads interactive input, and prints the answer advances without delay. OpenCode/DeepSeek Harness may be exercised when safely available, but verification does not depend on an external package installation.

## 6. Ordered implementation

1. Add the failing hot-path regression assertion.
2. Delete the per-chunk debug side effect without changing the renderer or WebSocket path.
3. Run focused tests, build the Linux app, and exercise the interactive PTY path in the real Wails v3 window.
