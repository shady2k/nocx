# ADR-0081 — Recovery and prompt boundaries are backend-owned, non-authoritative effects

- **Status:** Accepted
- **Date:** 2026-10-07
- **Supersedes:** [ADR-0024](0024-authenticated-shell-integration-channel.md) decision 8 only where it publishes the raw recovery nonce to the renderer as `recovery.fence` / `recovery.generation` and echoes that value in `lifecycle.recoverAck`. The authenticated shell/backend nonce, the composite acknowledgement, and the lifecycle state rules remain.
- **Related:** [ADR-0066](0066-one-emulator-and-it-is-the-backends.md) (the backend owns the session emulator), [ADR-0024](0024-authenticated-shell-integration-channel.md) decisions 1, 2 and 8, [ADR-0074](0074-a-command-boundary-is-settled-by-an-event-never-by-a-timer.md), [ADR-0080](0080-an-authenticated-event-defers-a-settle-it-does-not-take-it.md).
- **Amends:** `AD-1` (typed terminal observations may cross the control plane), `AD-5` (the backend emulator owns supported OSC observations), and `AD-6` (the renderer does not parse these markers).
- **Beads:** `nocx-zg3k3.17`, `nocx-zg3k3.18`, `nocx-zg3k3.14.3`, `nocx-zg3k3.14.4`, `nocx-zg3k3.8`.

## Context

The 0.6 cell-painted terminal removes xterm.js from the build. Two renderer-side parsers currently keep the cutover from being complete: OSC 133 B marks a conventional shell's prompt boundary for startup settlement and Ask screen handback; the `NOCX_RECOVERY` marker locates the restored prompt after the lifecycle channel is lost while the PTY remains alive.

ADR-0066 puts the one VT emulator in the session runtime. Keeping either parser in the renderer would create a second owner for terminal bytes. Sending raw marker bytes to the renderer would move the parser rather than its ownership. Recovery has a second constraint: the current `lifecycle.changed` message sends the same one-shot shell nonce in both `recovery.fence` and `recovery.generation`, and the renderer echoes it in `lifecycle.recoverAck`. The owner approved moving both observations to the backend-owned path, with no raw nonce in any renderer-facing message.

The existing `EffectFence` is not a general terminal-marker channel. It is the runtime's private rendezvous for `NOCX_FENCE`, which locates an authenticated command completion. `NOCX_RECOVERY` has a different purpose and must not reuse that effect.

## Decision

### The emulator reports two typed observations

The session runtime's emulator recognizes the recovery marker and OSC 133 B in byte order, including when a sequence is split across ingests. It emits two distinct observations: a recovery sighting and a prompt boundary. The recovery sighting is a new internal effect, not `EffectFence`; the prompt boundary is its own effect kind. Neither raw OSC bytes nor marker text crosses the control plane.

Both observations use the existing `session.effect` notification route. The `recovery` kind carries the non-secret recovery `episodeId` so it can be matched to the active episode; its title and body are empty. The `promptBoundary` kind has no marker payload and empty title and body. The event's session generation and effect ID remain delivery identities only; neither is a recovery generation, authenticator, nor lifecycle authority. Exact JSON Schemas, generated renderer types, DTO checks and real-socket tests define the wire shape.

Effects are at-most-once events, not screen state. They are never placed in a replayable frame or the output replay ring. The recovery episode state below is durable and is the reconnect-safe record. If a client needs a current prompt-boundary state after attach, that is actual current snapshot state, not replay of an old B event or inference from pixels.

### The recovery nonce remains private; the episode ID is public

The shell retains the one-shot random nonce supplied through the authenticated bootstrap. The backend retains the expected nonce in private recovery state and matches the emulator's recovery sighting against it. The nonce is never serialized in `lifecycle.changed`, `session.effect`, `lifecycle.recoverAck`, generated renderer types or product logs.

Each live recovery episode has a separate, non-secret `episodeId`, scoped to the session incarnation and independent of the nonce. `lifecycle.changed.recovery` publishes only that ID and the episode state (awaiting a sighting or sighted). A successful backend match changes the durable episode state to sighted and emits a `session.effect` recovery event carrying the same ID. A current-state resync returns the active episode and its latest state, so a dropped at-most-once effect cannot erase the sighting.

The renderer acknowledges only after it has applied the conventional presentation and has the `sighted` state for that exact episode. `lifecycle.recoverAck` carries the session identity and `episodeId`, never the nonce. The effect and lifecycle state locate the restoration; neither creates nor authenticates it. The lifecycle kernel remains the authority.

### Prompt-boundary sightings do not grant authority

OSC 133 B is an untrusted terminal observation. It may settle conventional-shell startup presentation and advance Ask screen handback at the observed boundary. It does not prove shell identity, input safety, prompt ownership, lifecycle readiness, command completion or command status. In an integrated session, only the authenticated lifecycle protocol may publish `prompt_ready`; B never substitutes for it.

A missing prompt-boundary event is not inferred from the visible grid or history. The event may be dropped under the existing at-most-once delivery policy. Any recovery of a current presentation state must be represented as actual current state, not by replaying a marker event.

### Decision-8 lifecycle rules remain unchanged

The authenticated lifecycle channel still owns every lifecycle transition. If the PTY/session dies while recovery is pending, session death wins, the episode is cancelled, and late sightings or acknowledgements are refused. A sighting cannot create, authenticate, complete or assign status to an attempt. An acknowledgement is accepted only for the exact live episode after a matching backend sighting; a duplicate acknowledgement is idempotent, while stale or unknown episode IDs cannot advance state. The only transition remains `Lost → Native`; it cannot revive a `DomainLost` domain, grant ownership, or resume a domain.

## Alternatives rejected

- **Keep a renderer-side OSC parser after removing xterm.js.** That creates a second VT parser and another owner for the same input.
- **Send the raw recovery nonce to the renderer.** That preserves the old wire exposure the owner rejected; the public episode ID is sufficient to match the sighting and acknowledgement.
- **Reuse `EffectFence`.** It conflates command-completion rendezvous with recovery and would give the new signal the wrong internal semantics.
- **Put either marker in a replayable frame.** These are at-most-once observations, not visual state; replaying them could repeat a UI transition or misattribute a stale marker.
- **Treat OSC 133 B as `lifecycle.prompt_ready`.** B is an anonymous terminal write and cannot authenticate shell identity or grant lifecycle authority.

## Consequences

- The `.17` and `.18` implementations add distinct emulator/runtime effects and extend the existing `session.effect` contract; both must have real over-the-wire tests. They are independent tasks with shared wire files, so they are implemented in separate worktrees and integrated one at a time.
- The `.8` xterm cutover remains blocked until both typed signals are available and its user-level no-xterm acceptance passes with `.9`.
- `docs/lifecycle-protocol.md` §12.1 now describes a nonce-free renderer contract. ADR-0024 remains unchanged; its authority, threat model and the other decisions still stand.
