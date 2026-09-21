# Recommendation: materialize scroll-off rows at the authoritative runtime

Take a fourth option: **the session runtime emits a bounded, durable semantic
scroll journal as rows leave the primary screen; clients page that journal and
paint it with the same cell renderer as the active frame.** Do not make
libghostty's in-memory scrollback the product store, and do not reconstruct the
screen from `session_output` on demand.

This option is not one of the three listed in the brief, although the design
already points at it in §6.3: the capture record is an opening snapshot,
ordered changes, rows that left the screen, a closing snapshot, completeness,
and retention. The missing decision is to make that capture record the one
source for both live scrollback and finished-card projection.

What the person sees is precise:

- At the tail, the client paints only the active rectangle from
  `contracts/session.frame.schema.json`.
- Scrolling upward crosses a revision boundary into immutable rows immediately
  preceding that rectangle. New output may continue, but it does not move the
  reader's anchor. Returning to the tail resumes the live frame.
- Historical rows retain cell widths, styles, links, wrap/continuation and the
  geometry epoch in which they were produced. A projection may reflow logical
  lines for the current client; it must not reinterpret VT bytes.
- The alternate screen contributes no invented line scrollback. On exit or
  termination, its interval may retain a closing snapshot/card. Retaining every
  transient TUI repaint would be a separate screen-recording feature.
- Output outside an authenticated command is still in the session journal. It
  therefore remains reachable before the first prompt and after a killed TUI
  without inventing a command merely to own it. Cards reference intervals in
  the same journal; they are not a second capture.

`internal/content/session_output.go` should then be **replaced, not kept as a
fallback**. Its offset, gap and completeness lessons survive, but its stored
entity changes from uninterpreted bytes to emulator-derived rows and geometry
epochs. Once the journal is wired and accepted, remove the `session.output`
method, its raw-byte tables and its contract. Greenfield status makes a dual
write or compatibility reader pure dead code.

There is already proof that the current contract cannot survive unchanged:
`contracts/session.output.schema.json` says the client feeds the returned bytes
to its own VT. ADR-0066 removes that VT. The byte sink may remain temporarily
while the cutover is incomplete, but it has no final product reader.

## Cost of the recommendation

Use two bounds, both explicit: **10,000 logical rows and 8 MiB encoded per
session, whichever is reached first**, retaining the most recent contiguous
tail. This is a proposed product bound, not an existing one. A live terminal
scrollback should not reuse the present head-plus-tail eviction rule: a hole in
the middle of the region a person is scrolling is worse than losing its oldest
end. Finished cards may keep their existing head-plus-tail policy because they
answer a different question.

The numbers are:

- At 132 columns, 10,000 physical rows are 1.32 million cells. With an
  estimated run-encoded plain row of about 160 bytes (132 ASCII bytes plus row
  and one-style-run metadata), 10,000 rows cost about 1.6 MiB. With an
  estimated pathological 16 encoded bytes per independently styled cell, the
  8 MiB byte bound fires at about 3,970 rows. These are format estimates; no
  durable row encoding exists yet.
- Keep history on disk, not in the emulator. A 43-row page at 132 columns is
  5,676 cells. A compact 16-byte-per-cell client representation is about 89
  KiB; the current Go `emulator.Cell` is 40 bytes shallow on a 64-bit layout,
  or about 222 KiB per page before grapheme backing storage. Caching two pages
  is therefore approximately 178–444 KiB per visible session, plus strings.
- Persist in batches no larger than 16 KiB (the current session-output chunk
  size) and flush at an authenticated boundary. The client makes **zero
  history calls per live frame** and one page request per roughly 43 rows of
  upward scrolling. A crash before a batch is durable must become an explicit
  incomplete tail; claiming zero loss would require an fsync policy this review
  did not measure.
- A 43×132 history page is comfortably below the helper's 1 MiB frame bound
  under the estimates above. It must still be page-bounded; a 10,000-row reply
  is not legal. The lifecycle channel's separate 256 KiB bound is not a route
  for this data.

The runtime/capture producer is the only place allowed to create journal rows.
It already owns `emulator.Row`, `Snapshot.Revision`, `Completeness`, and
`ScreenIdentity`. The document service may persist those values without
interpreting them. The client only projects them.

The scroll-off signal must be obtained at the mutation that evicts a row, not
by diffing two later active screens: one `Ingest` can scroll several rows, and
an after-the-fact diff has already lost them. If libghostty's render API cannot
expose that event, the pinned adapter/fork needs one callback or drain method.
That is still the one emulator. It is not a second parser or a replay terminal.

This touches:

- **ADR-0066 / AD-6:** upheld. The one runtime emulator remains authoritative;
  its capture producer gains a durable reading of rows leaving the active area.
- **AD-8:** upheld only if cards and live scrollback consume this same journal.
  Keeping `session_output` as an alternative reconstruction source would break
  it.
- **AD-1 and AD-10:** a bounded, versioned page request/response is needed on
  the data path, with credit charged in encoded bytes. It is not JSON-escaped
  PTY output on the control plane.
- **AD-9:** the 256 KiB ring remains reconnect transport, never history. Its
  current raw-byte recorder relationship is retired at the ADR-0066 cutover.
- **ADR-0009:** upheld. Historical cells become a DOM projection with explicit
  geometry; its serializer changes source from xterm's buffer to the journal,
  not behaviour.
- **`contracts/session.frame.schema.json`:** left active-area-only. History gets
  a separate page contract carrying `ScreenIdentity`, geometry epoch,
  completeness and row identities. Expanding every live frame with history
  would make cost grow with retained history.
- **Frozen helper ABI:** do not extend `proto.ScreenFrame` or `OpReplay`. The
  journal must use the client frame/data channel already required by ADR-0066,
  tunnelled opaquely through the coordinator. Whether the existing frozen
  session channel supports a host-initiated page response was not verified;
  if it does not, this recommendation is blocked on an explicitly versioned
  ABI decision rather than on smuggling fields into `ScreenFrame`.

What this does not fix: it does not provide video playback of alternate-screen
TUIs, retain Kitty/sixel graphics, make evicted history recoverable, or make a
runtime crash between durable batches complete. Each absence must be reported
through completeness/gaps rather than shown as an empty row range.

## Option 1: teach the emulator port to read scrollback

**Mechanism.** Add viewport/scrollback reads beside `Terminal.Row` and
`Terminal.Cell`, backed by libghostty's primary-screen history. A client asks
the live runtime for pages. The active `session.frame` remains unchanged.

**Cost.** A 10,000-row history at 132 columns is 1.32 million cells per live
session. At an estimated compact 16 bytes per cell that is 20.1 MiB; at the
40-byte shallow size of Go's current `emulator.Cell` it is 50.4 MiB, before
grapheme strings and allocator overhead. Twenty sessions are therefore roughly
0.4–1.0 GiB if represented at those sizes. Resize/reflow is O(retained cells),
up to 1.32 million cell visits per session at that bound. Delivery can still be
one 43-row page per scroll request and zero extra calls per live frame.

**What it does not fix.** In-memory history dies with the runtime. It does not
make `session_output` geometrical, does not recover across helper replacement,
and does not define what happens to old rows on resize or retention eviction.
It therefore answers the UI half but not the durable-sink half unless it is
also serialized—at which point the recommendation above is the smaller owner.

**Contracts and invariants.** It amends `internal/emulator.Terminal`'s explicit
active-area-only rule and needs a retention decision under ADR-0066/AD-6. It can
preserve AD-8 if the port is the only reader. A separate page contract preserves
the active-only `session.frame`; putting history in that frame would reverse the
contract and make AD-10 cost scale with retained rows.

## Option 2: the live region does not scroll

**Mechanism.** The active rectangle is the whole live surface. Everything that
leaves it is reachable only through interval records projected as DOM cards.
The existing `unstructured` live mode in `controller.ts` would need a real
capture interval for output outside commands, not merely a display state.

**Cost.** Zero retained live rows, zero additional bytes per active session,
and zero calls per frame. Durable cost moves to captures. If the current 256
KiB output cap were applied to one unstructured interval, that interval could
retain at most 256 KiB and would carry an explicit gap beyond it; total history
still grows with the number of intervals until the store's global retention
budget acts.

**What it does not fix.** A person cannot inspect output from a still-running
long command until a record is checkpointed, and a terminal user loses the
ordinary expectation that Page Up reveals recent lines. A killed TUI has only
whatever opening/closing snapshots the capture policy chose; its intermediate
states are not line history.

**Contracts and invariants.** It preserves ADR-0066, AD-6 and the active-only
frame. It extends ADR-0009's DOM ownership to universal interval records and
requires design §6.3's capture producer to cover pre-prompt and unstructured
output. AD-8 forbids a separate special-case recorder for those intervals.

## Option 3: replay `session_output` through `OpReplay`

**Mechanism.** Read the byte runs and gaps from
`internal/content/session_output.go`, send retained bytes to the helper's
PTY-less replay emulator, and ask for screens at marks.

**Cost.** The default store cap is 256 KiB split into 16 KiB chunks, so a full
default recording has at most 16 body chunks. JSON base64 expands 262,144 raw
bytes to 349,528 bytes before envelope overhead, which fits the helper's 1 MiB
request frame. A user cap above roughly 768 KiB cannot fit in one request after
base64 and envelope overhead; responses can be chunked, but I found no request
chunking path. Work is O(bytes replayed) for every fresh replay. Paging K times
by replaying from the start is O(K × retained bytes), with one helper call per
page and zero per live frame.

**What it does not fix.** As implemented, it does not produce scrollback at
all. `OpReplay` returns only an active `ScreenFrame`; that shape drops style,
wrap and continuation; and `ReplayParams` has one fixed `{cols, rows}` with no
resize timeline. Marks after 16 KiB chunks cannot recover rows that scrolled
away inside a chunk. The store can also contain a cap/unrecorded/host-window
gap, after which replay cannot be authoritative. Thus the current mechanism
cannot faithfully repaint either ADR-0009 rows or a resized session.

**Contracts and invariants.** Promoting calibration replay to live product
scrollback creates a fresh emulator beside the live runtime on every request,
which challenges ADR-0066's one-emulator decision. Making it work would require
changing the frozen helper ABI and the active-only frame shape, or would require
option 1 inside the replay terminal. The lifecycle 256 KiB channel is unrelated
and must not be used to evade the helper's 1 MiB bound. The stale promise in
`session.output.schema.json` that a client owns a VT must be removed either way.

## The common assumption, and an option that rejects it

Options 1–4 all assume **scrollback is authoritative session state shared by
all clients and reconnects**, not a record of what one particular window
happened to paint.

A rejecting option is a **client-local visual DVR**: retain successive
`session.frame` snapshots by revision in each browser and scroll through those.
It needs no parser, no helper ABI change and no server history.

Its cost closes it. A 132×43 frame has 5,676 cells. At an estimated compact 16
bytes per cell, one snapshot is about 89 KiB; at 60 frames/s that is about 5.2
MiB/s and 1.5 GiB for five minutes per visible session. Even sampling at 1 Hz
costs about 26 MiB for five minutes and misses output between samples. Delta
storage reduces the typical case but grows with changed cells, and the measured
`htop` case already makes cell-level delivery 1.37× raw. A newly attached
client has no history, two clients disagree, and `session_output` remains
unreadable. It touches no ownership ADR only because it gives up the shared,
durable product requirement; it does not fix that requirement.

## Verification gaps and contradictions found

- `build/libghostty-vt/vendor/*/include/ghostty/vt/` is absent in this
  checkout. I could not verify whether `render.h`, `screen.h`, `snapshot.h`,
  `selection.h` or `search.h` exposes scrolled-row events, scrollback paging,
  snapshot persistence, or its memory layout. The recommendation depends on a
  scroll-off event; it does **not** assume one exists.
- I could verify only the repository's measured native baseline of about 45 KiB
  per emulator from `internal/emulator/ghostty/doc.go`. I could not verify
  libghostty's per-history-cell memory, so option 1's 20–50 MiB figures are
  explicit representation estimates, not library measurements.
- `contracts/session.frame.schema.json` says nothing sends it yet. Its schema
  is active-area-only and lacks the `ScreenIdentity`/completeness needed to
  join historical pages safely; those belong in the separate history contract,
  not as guessed fields in every frame.
- `contracts/session.output.schema.json` still describes the removed
  client-emulator architecture. That is a present contradiction, not a future
  risk.
- `internal/helper/proto.ScreenFrame` cannot serve as the renderer row format:
  it carries text and width only. Its frozen shape is materially weaker than
  `emulator.Row` and `session.frame`.
- No persistence/fsync measurement for a semantic journal exists here. The
  16 KiB batching and 8 MiB/10,000-row retention numbers above are the concrete
  policy I recommend measuring, not claims that the current store already
  meets them.
