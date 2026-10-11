# ADR-0083 — Prompt-boundary effects require a locally accepted screen revision

- **Status:** Accepted
- **Date:** 2026-10-10
- **Supersedes:** None
- **Related:** [ADR-0081](0081-recovery-and-prompt-boundaries-are-backend-owned-non-authoritative-effects.md) (backend-owned prompt observations), [ADR-0073](0073-the-screen-frame-is-keyed-by-its-session-and-its-reader-and-continues-on-its-own-carrier.md) (screen carrier), [ADR-0072](0072-xterm-is-removed-first-and-the-interval-record-follows-the-cell-model.md) (cell-model cutover), [ADR-0066](0066-one-emulator-and-it-is-the-backends.md) (emulator ownership).
- **Beads:** `nocx-zg3k3.8`.

## Context

OSC 133 B remains an at-most-once, non-authoritative observation (ADR-0081). The cell-painted renderer no longer parses PTY bytes, so it cannot use a renderer write barrier to know that the visible screen includes the output through B. The full screen frame and the effect travel on separate producers. A frame can be dropped as refreshable state, while the effect is ordered after the PTY bytes are enqueued, not after a client acknowledgement. An effect that arrives against an older screen can therefore freeze or attach the wrong surface.

The existing screen-resend operation asks the helper runtime to re-offer its current full snapshot. Its carrier is ordered with ordinary frames, but ordinary `TryEnqueue` may drop a snapshot under outbound backpressure. The prompt effect must not pass until the corresponding snapshot has actually been offered to the same subscriber's outbound queue.

## Decision

`promptBoundary` carries a required `screenRevision`, the revision of the full frame published after the same runtime ingest that consumed OSC 133 B. Other effect kinds omit it. This is a non-authoritative lower bound and correlation value only: it is neither a client delivery acknowledgement, a lifecycle fact, nor permission to apply an effect to another session or incarnation.

The coordinator offers the prompt effect only after the existing `ScreenResender` path has successfully enqueued a frame for that session at `revision >= screenRevision` on the ordered outbound carrier. The frontend also checks its local state: `TerminalContent` handles a prompt boundary only when the current session's `CellModel` has accepted a frame at `revision >= screenRevision`. If the frame was not routed or the model refused it, the client drops the at-most-once observation before startup settlement, screen handback, or renderer dispatch. This is a local acceptance check, not an acknowledgement to the backend.

If outbound capacity is unavailable, one per-session worker retains only the largest target revision and the byte size needed for an offer; it does not retain frame payloads. It waits on the outbound queue and shared-budget capacity signals, then asks the runtime for its current full snapshot again. Runtime and transport queues coalesce to current state. The worker holds no session or lifecycle gate, cannot block another session's ring pump, and is cancelled when its subscriber is replaced or closed. The dependent at-most-once effect is not delivered after cancellation or an unofferable target.

The PTY `StreamOffset` keeps its existing meaning: the exclusive byte coordinate used to order a prompt effect after bytes enqueued on the outbound stream. It does not mean that the client acknowledged those bytes.

## Alternatives rejected

- **Use an arbitrary later frame.** A quiet prompt may never produce one, and its revision does not prove it includes the boundary's output.
- **Use a timer or timeout.** Elapsed time proves neither that the frame exists nor that it was offered; event ordering and observable capacity are the evidence.
- **Add a new JSON-RPC method or client acknowledgement.** The existing internal `ScreenResender` already asks the owning runtime for its current frame; the required ordering can be enforced before the existing effect is queued.
- **Retry on a timer or retain every missed frame.** Timers do not establish freshness, and unbounded frame buffering defeats the screen carrier's bounded coalescing contract. One coalesced target plus capacity-driven resend is sufficient.
- **Use the PTY offset as a client acknowledgement.** The ring pump advances that cursor when bytes are enqueued, not when the renderer confirms receipt.

## Consequences

- A prompt-boundary effect is still at-most-once and non-authoritative under ADR-0081. A missing or cancelled offer may drop the effect; lifecycle and authenticated block completion remain independent authorities.
- A screen frame can still be dropped as ordinary refreshable state. Only a prompt boundary that needs that revision waits for the bounded current-frame offer path.
- The renderer handles the effect only after the session-bound cell model accepts the correlated frame revision. If that revision is unavailable or refused, the client drops the observation rather than applying it to stale screen state; no acknowledgement is sent.
