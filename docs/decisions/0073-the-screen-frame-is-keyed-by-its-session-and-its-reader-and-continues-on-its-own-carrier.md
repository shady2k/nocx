# ADR-0073 — The screen frame is keyed by its session and its reader, and continues on its own carrier

- **Status:** Accepted
- **Date:** 2026-09-23
- **Related:** `nocx-zg3k3` (the client epic), `nocx-zg3k3.2` (the stage that owns
  the wire as well as the painter), `nocx-zg3k3.2.2` (the task that landed the
  type byte, the layout and the continuation, and where these decisions were
  first taken), `nocx-zg3k3.2.6` (the measurement that forced the frame's
  shape), `nocx-zg3k3.2.7` (this record). The delivery design is
  `.internal/specs/2026-09-12-the-backend-holds-the-session-screen-design.md`.

## Context

Since `nocx-zg3k3.2.2` the data plane carries the screen: per revision the
session runtime publishes a full snapshot to every attached client, and a
client that attaches mid-session receives one snapshot at the current revision
before any later frame. The wire half of that — a frame type, a payload
layout, a continuation scheme — was decided on 2026-09-22 in that task's
tracker comments and in a coordinator brief inside a worker's checkout that no
longer exists. A bead comment is not where the next person looks for why a
wire looks the way it does, and a removed worktree is not anywhere; this
record is where the reasoning lives, and the codec's comments cite it.

The shape was forced by measurement (`nocx-zg3k3.2.6`). The frame's first
schema gave every cell its own style object — 261 bytes per styled cell, so
501 KB at 80x24, 1.25 MB at 120x40 and 2.6 MB at 200x50: past the helper wire
bound from 120x40 up. That measurement's own estimate put the same
information at about 8 KB for 120x40 once the style is shared per run of
adjacent cells — the cause was the shape, not the volume. The
reshaped frame carries each cell as a positional tuple beside its run's
style, and realistic published screens measure 78,286 B at 80x24, 188,495 B
at 120x40 and 377,751 B at 200x50 — one part each on the carrier. The
pathological screen — every cell its own truecolour style, the case runs
cannot compress (lolcat, a gradient TUI) — measures 467,399 B at 80x24,
1,167,319 B at 120x40 and 2,429,970 B at 200x50 on the reshaped schema:
above the per-frame bound from 120x40 up, and what the continuation exists
for.

The bounds the carrier enforces, from the code, which is authoritative: one
helper wire frame's payload is bounded by `MaxFrameBytes` (1 MiB,
`internal/helper/proto/frame.go`); one screen frame's payload by
`MaxScreenDataPayloadBytes`, that bound minus this layout's 48-byte header; an
assembly may declare at most `MaxScreenAssemblyParts` (8) parts; a connection
holds at most `MaxScreenAssemblies` (64) partial assemblies. Measured through
the published path, the realistic frames above fit one part each; the
pathological published screen at 120x40 measures above the per-frame bound
and is delivered as parts within the assembly bound. One correction the code
forced on the written record: the tracker comment cites a 256 KiB bound from
`internal/lifecycle` as one the frame busts, but that constant bounds the
kernel-to-shell lifecycle JSON channel and nothing on the screen carrier's
path reads it. The operative bounds are the ones above.

## Decision

> **The screen frame rides its own type byte and layout — keyed by the host
> session id and the subscriber it is for, minting nothing, carrying no lease
> epoch, the revision in its header — and an oversize frame continues as
> bounded parts on this carrier, reassembled before any JSON is parsed, never
> through `ChunkedResult`.**

## Why this rather than the obvious alternative

**A second meaning for `TypeSessionData`** loses three ways. AD-1 and AD-6:
that plane's payload is raw PTY bytes, and feeding JSON screen frames into the
byte carrier corrupts the stream every downstream consumer reads. Routing:
the session service decodes `SessionFrame`'s fixed header and routes by its
inventory, so a screen frame would decode cleanly into the wrong router — the
exact hazard `channel_frame.go` names for `TypeChannelData`, the template this
byte follows. Resync: the decoder treats an unknown type byte as garbage and
scans forward one byte at a time, so a generation that did not know a screen
stream would resync THROUGH it rather than drop one frame. That last fact is
also why the byte is allocated before any producer ships and announced in
`version.go` — the same move AD-1 made for its reserved metadata msg-type.

**A helper-minted screen id** loses to the argument `session_frame.go`'s own
header already carries: it rejects a second version byte because the protocol
has one and "a second would be a second owner of the same question". A screen
is the screen OF a session, the wire already names that session, and a
helper-minted screen id is a second name for a named thing — two names for one
identity eventually disagree, which is the defect shape AD-8 exists for.
Nothing is minted. The subscriber id stays, and is sharper here than for
bytes: what each reader is owed genuinely differs — a mid-session attacher is
owed one baseline snapshot at the current revision, an established reader the
stream — so a frame has to say which reader it is for.

**Carrying a lease epoch** loses because a screen frame only ever flows
helper-to-coordinator, and `SessionFrame`'s own header says the epoch is zero
on exactly those frames, where there is nothing to authorize. The field is
left out rather than carried as a permanent zero — a field that is always zero
is a field that lies about being read — and a reader of the two layouts side
by side should see the difference is deliberate.

**The revision only inside the JSON payload** loses because the receiver must
order, drop and reassemble without parsing JSON. The header's revision is
what lets it: a part that continues a partial at another revision drops that
partial whole and is refused by name; a part that continues nothing is
refused by name; a part declaring more than the assembly bound is refused
before a byte is buffered. Half of revision N and half of N+1 is a screen
that never existed, and seeing that never requires parsing a part. The whole
delivery design is revision-relative.

**`ChunkedResult` plus `TypeChunk`** — the house's first answer to "this
payload does not fit one wire frame", instructed, checked, and refused — loses
because the mechanism is bound to the request/response envelope:
`ChunkedResult` is the sentinel a RESPONSE carries when the real payload
follows as `TypeChunk` frames, and `Chunk`'s stream id names the sentinel that
preceded the chunk. A published screen frame has no Response to carry a
sentinel. This is the case the find-the-existing-answer rule reserves — the
existing answer genuinely does not fit — so the type owns its continuation the
way `channel_frame.go` owns its own layout: part index and part count in the
payload header, `SplitScreenDataFrame` on the sender, `ScreenAssembler` at the
receiver, reassembly bounded per connection (eight parts per assembly,
sixty-four assemblies per connection), a superseded partial dropped whole, and
every drop named and routed into the loss report the delivery contract
already carries (`Consumer.Coalesced`) — visible in the product, never a
silent oversize discard.

## What stands

- **The invariant, with both ends:** from the moment the runtime publishes a
  frame to the moment a consumer holds it, that consumer either holds the
  whole frame at that revision or has been TOLD it lost it. The assembler's
  named errors are the telling; a screen silently dropped for its size is the
  outcome this design refuses.
- **The strictness** — parts contiguous, in order, one revision at a time —
  rests on the wire being ordered (one connection, frames in send order) plus
  the sender's obligation to send a frame's parts together or not at all,
  owned by the drain that publishes them.
- **The carrier's ends as landed:** `internal/helper/session`'s drain splits
  and sends; `internal/helper/host` writes the wire under its writer mutex;
  `internal/helper/client` reassembles per connection and hands whole
  documents to the attachment; `internal/transport` publishes the whole
  document on the data plane's reserved metadata msg-type — never
  `MsgTypeData`, never JSON-RPC.
- **What this record does NOT decide:** the `session.frame` document's own
  shape — the cell model and `contracts/session.frame.schema.json`, reshaped
  under `nocx-zg3k3.2.6` — or the renderer's consumption of it. This record
  is the carrier, not the cargo.
