# ADR-0066 — One emulator, and it is the backend's

- **Status:** Accepted
- **Date:** 2026-09-12
- **Supersedes, in the parts named in "What this supersedes":** `AD-6`, `AD-1`, `AD-9`,
  `AD-10` (`docs/architecture.md`), [ADR-0001](0001-xterm-js-as-vt-frontend.md),
  [ADR-0002](0002-native-tabs-no-embedded-multiplexer.md),
  [ADR-0008](0008-command-blocks-as-a-keyboard-first-ledger.md),
  [ADR-0009](0009-dom-scrollback-with-explicit-cell-geometry.md). No existing record is
  edited; `docs/architecture.md` carries an amendment naming this number, which is how that
  file has always recorded a change to an `AD`.
- **Related, and NOT superseded:**
  [ADR-0024](0024-authenticated-shell-integration-channel.md) decisions 1 and 7 (their
  invariants stand; their PLACEMENT moves — see below),
  [ADR-0064](0064-a-pane-that-is-read-may-be-answered.md) (its permission boundary stands),
  [ADR-0065](0065-the-emulator-a-program-talks-to.md) (WHICH emulator; this record is about
  WHO owns one, and the two are independent).
- **Design:** `.internal/specs/2026-09-12-the-backend-holds-the-session-screen-design.md`,
  sixth revision, §4. **Owner's decision, 2026-09-12:** the long-term model, taken
  deliberately over the smaller incremental option.
- **Beads:** `nocx-g5p8c` (this record), `nocx-kkn89` (the design), epics `nocx-ygxjv` (the
  runtime), `nocx-2v80t` (the blocks), `nocx-zg3k3` (the client).

## Context

`AD-6` gives the VT frontend — xterm.js, by [ADR-0001](0001-xterm-js-as-vt-frontend.md) — the
render state, and says in terms that **the backend does not sniff the byte stream**. Two
carve-outs were added to it rather than around it: the bootstrap window (2026-08-20), which
reads a framing nocx wrote and interprets nothing, and a live grid for an **enrolled** pane
(2026-08-25, [ADR-0041](0041-x-vt-as-the-backend-emulator.md)), which does read what an
application drew and is bounded by an interval and exactly two powers.

The product now needs something neither carve-out reaches: **a terminal whose state is not
the property of whichever window happens to be open.** What made that unavoidable was
measured rather than assumed, and the measurements survived being wrong twice:

- A second machine cannot reach "the same screen" from the byte stream alone. The replay ring
  is 256 KB, about ten screens (`internal/transport/ring.go:12`), and past it `AD-9` sends a
  reset — a cleared screen.
- The helper can keep a process alive while the coordinator loses the output needed to
  rebuild its emulator (`internal/transport/ws_readopt.go:100`, the output-window hole).
- A session running with no window attached produces no frozen output **artifact**, because
  `history.record` originates in the renderer (`frontend/src/scrollback/blocks.ts`). The
  ledger row itself is closed from authenticated facts and does survive
  (`internal/transport/ws_lifecycle.go:342`) — the first draft of this reasoning said
  otherwise and was wrong.
- **There are already two answerers to the program, and one of them is the wrong one.**
  `renderer.onData` sends the renderer's replies to the PTY from every mounted pane, hidden
  ones included, behind no guard of any kind (`frontend/src/terminal-content.ts:4011`), while
  the backend emulator's replies are read and dropped
  (`internal/panegrid/panegrid.go:195`). A program asking its terminal a question is answered
  by the browser, from the browser's coordinates.

[ADR-0002](0002-native-tabs-no-embedded-multiplexer.md) closed server-side terminal state
twice and named its own revisit trigger. That trigger has fired, and `AD-6`'s 2026-08-25
amendment already says so: "a session that survives the client process entirely is now a
product requirement".

## Decision

> **One emulator, on the backend. It owns what the session is; the renderer owns what the
> person sees of it, and what the person does to it arrives as intent, not as bytes.**

It lives in a long-lived **session runtime beside the PTY** — for a remote session, beside the
REMOTE pty, with SSH as an authenticated carrier to it. The desktop process and the
orchestration coordinator may die without taking terminal state with them. The runtime owns
the screen, the terminal's modes, the answers to the program's own questions, the committed
geometry, the order in which input is admitted, and the authenticated lifecycle rendezvous.

## What this supersedes

**`AD-6`, in three separable parts, and the third is the one a reader will miss.**

1. **The rule** that the backend does not sniff the byte stream, and the refusal list
   (`docs/architecture.md:160`) forbidding grid-derived content from being displayed or
   persisted. The backend parses VT for every session, and what it derives IS the user's
   terminal rather than a private second reading.
2. **The LIFETIME.** The 2026-08-25 amendment does not only grant powers; it binds when a
   grid may exist at all: "A grid exists for a pane that has been **explicitly enrolled for
   observation** … and for no other pane"; it "closes when that pane's record becomes durably
   terminal or the enrolment is withdrawn, at which point the grid is discarded and the pane
   is never read again"; "An unenrolled pane has no backend grid at any point." An emulator
   per session contradicts all three sentences. Saying this separately is not pedantry: the
   fifth revision of the design called the amendment's contents POWERS and left it standing
   "verbatim", which would have produced a record preserving the exact rule it exists to
   break, and `nocx-ygxjv.3` — whose whole subject is separating grid lifetime from
   observation authority — would have contradicted an invariant with nothing behind it.
3. **Enrolment stops being what creates terminal state.** `paneEnroller.Enrol`
   (`internal/app/paneenrol.go:102`) creates the grid and refuses an existing one. After this
   record, enrolment is an OBSERVATION authority over a runtime that already exists.

**`AD-1`**, in the part where the data plane carries raw PTY bytes in both directions. It
carries frames outbound and structured input inbound. The two-plane split — binary data
plane, JSON-RPC control plane on one socket — survives untouched.

**`AD-9`** and the `session.output` contract, including its no-grid reasoning.

**`AD-10`**, which promises bounded per-session credit and lossless ordered byte delivery.
Coalesced visual updates need a restated contract: what stays lossless (ingest into the
emulator, and the ledger), what becomes explicitly lossy (intermediate frames nobody was
shown), and how fairness and credit are expressed over frames. `nocx-ygxjv.4` owes that
statement; until it exists, `AD-10` is superseded in its shape and not yet replaced, and this
record says so rather than leaving the gap unnamed.

**[ADR-0001](0001-xterm-js-as-vt-frontend.md)** where it makes xterm.js the VT frontend, and
its consequences promising frontend-only OSC parsing. **xterm.js is removed, not demoted.**
Keeping it as a painter would mean the browser executing cursor moves and erases only to
rebuild cells the runtime already has, and would keep selection bound to xterm's private
machinery ([ADR-0009](0009-dom-scrollback-with-explicit-cell-geometry.md):122 records that
dependency). Verified: xterm's only input is `write(bytes)` and its buffer is read-only
(`xterm.d.ts:809`, `:1216`), so cells cannot be pushed in and its renderer cannot be addressed
without its parser. The client's own cell renderer is the design's §6.11 and a search with
stated criteria, not a thing we already have.

**[ADR-0002](0002-native-tabs-no-embedded-multiplexer.md)** where it closed server-side
terminal state. What it actually refused — nocx becoming a multiplexer with its own panes,
its own keybindings and its own protocol between them — is untouched. Native tabs stay.

**[ADR-0008](0008-command-blocks-as-a-keyboard-first-ledger.md)**'s consequences promising a
byte-blind backend.

**[ADR-0009](0009-dom-scrollback-with-explicit-cell-geometry.md)** where a block's content is
serialised from the client's buffer. Its substance — the DOM owns the frozen block's
presentation and geometry — stands.

The `ledger.capture` contract's description of a renderer serialiser.

**The wire contracts themselves are not rewritten by this record, and that is deliberate.**
`contracts/session.output.schema.json`, `contracts/ledger.capture.schema.json` and
`history.record` describe what the product does TODAY, accurately. They change in the
cutover that implements this decision (design §9 step 7), in the commit that makes the new
shape real. A contract edited to describe a shape nothing sends is the failure mode
`…_OverTheWireConformsToContract` exists to catch, and the reason `vault.status` shipped a
field it never sent.

## What stands verbatim, and must be read as narrowly as it was written

**The 2026-08-25 amendment's limits on what a grid may DECIDE.** No wave state, no lifecycle
attempt, no execution attempt, no network destination. Those are POWERS, not consequences of
where the parser runs, and nothing above touches them.

This is stated separately and out loud because **"a grid may now exist everywhere" reads as
"a grid may now decide more" unless it is refused explicitly.** It decides no more than it
did. What moved is where the terminal lives, not what may be inferred from it.

**`AD-6`'s bootstrap-window carve-out.** Parsing VT is not permission to interpret bootstrap
readiness tokens, nor to write bootstrap frames outside their interval.

**[ADR-0024](0024-authenticated-shell-integration-channel.md) decisions 1 and 7 — their
invariants.** A sighted marker may only LOCATE an already-authenticated event; it never
authorises one. The rendezvous is OSC 1337 with a matching nonce
(`internal/shellintegration/scripts/nocx.bash:1505`), not ordinary OSC 133, whose `C`/`D`
have no meaning of their own. A program printing a forged `D` must not be able to close a
block or choose a capture endpoint. SSH still orders the two channels independently, so both
arrival orders remain real and a bounded missing-fence policy remains necessary.

**What moves is decision 7's PLACEMENT, and only that.** The rendezvous executes in the
renderer today — `BlockManager.freezeFromAttempt`, `sightFence` and `_pendingFence` behind
its `FENCE_DEFER_MS` timer (`frontend/src/scrollback/blocks.ts:2479`, `:2542`) — because the
renderer owned the VT. It moves beside the emulator. Keeping decision 7 whole would leave two
named owners for one rendezvous, which is the defect `AD-8` exists to prevent.

**[ADR-0064](0064-a-pane-that-is-read-may-be-answered.md)'s permission boundary**, whole.
Owning the emulator grants no reading of somebody else's session and no widening of what a
write may do. In particular ADR-0064's own mechanism survives intact: the frame is RE-READ
immediately before the write, so the identification that authorises it is the one taken
microseconds before rather than the one a caller saw
(`internal/agenttyping/agenttyping.go:371` → `:402`). A single authority does not make a
caller's older reading valid.

**`internal/notify`'s trust classes.** An inference from stream content is still `heuristic`,
and the routing table still refuses it a sink that leaves the machine.

## Why this rather than the obvious alternative

**The incremental option was to keep xterm authoritative and give the backend a synchronised
copy.** It is smaller and it was rejected on 2026-09-12, deliberately, by the owner. It does
not remove the failure it is aimed at: two emulators that must agree have a fidelity problem
forever, and the one that answers the program is still in a browser that may not be running.

**A narrower version was tried inside the design and withdrawn.** Its third revision proposed
declaring the backend authoritative while the frontend still cleared its own buffer and still
answered the program's questions, so that `nocx-6q1uh` could start earlier. That does not
repair the divergence; it exposes it to a new consumer. Its fifth revision withdrew that and
then granted a smaller permission of the same shape — automation on a pane no client displays
— which the sixth revision deleted: the signal it rested on does not exist on the wire, and a
mechanism that exists only to start one epic sooner and is deleted at the cutover is a shim,
which this repository does not build.

**There is no partial cutover of the client.** Live display, cards and interaction move in one
coordinated step. A server diff against frame F plus a surviving `clearViewport`
(`frontend/src/scrollback/controller.ts:722`) means the next diff assumes cells the client has
erased.

## What this costs

- **A client cell renderer that does not exist.** Every web terminal is a parser-and-renderer
  bundle and we want only the second half; hterm has xterm's shape. This is a search with
  criteria (design §6.11), and writing one is the fallback rather than the plan.
- **Input encoding moves to the backend.** A frame of cells and a cursor conveys nothing about
  `DECCKM` or bracketed paste, so a program that enabled either would receive differently
  encoded input while the screen looked correct. What the frontend sends becomes intent.
- **Non-visual effects need their own delivery with a duplicate policy.** A full frame must
  never repeat a clipboard write or a notification.
- **Recovery of the backend emulator becomes a real problem** rather than somebody else's. An
  emulator fed only the surviving suffix after an output-window hole is not authoritative;
  completeness must be establishable, and while it is unknown the write gate refuses and the
  attach says so.
- **`AD-10` is superseded before its replacement is written.** Named above; `nocx-ygxjv.4`.

## What this record does NOT rest on

Recorded so a later reader can see the edges of the evidence.

- **Not on ADR-0065.** That record decides WHICH emulator and is still `Proposed`, gated on
  `nocx-cm1ac` (the binding on macOS arm64). Ownership is independent of the choice: if the
  choice changed, this record would not.
- **Not on the design's §8 measurements, because they do not exist yet** (`nocx-rpzdo`). This
  record decides ownership. It does not claim that a frame protocol costs fewer bytes than raw
  output — the design's §7 shows a positional diff LOSING to raw output on scrolling, and a
  scroll-aware encoder is ours to build. If measurement 1 goes badly, what changes is the
  encoder and possibly the delivery shape, not who owns the emulator.
- **Not on a client renderer having been found.** §6.11 is a search, and this record is
  accepted before it concludes.
- **Not on graphics.** Neither candidate emulator displays sixel. Nothing DISPLAYED is lost by
  this decision, and nothing is promised either.
