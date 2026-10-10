# ADR-0077 — The lifecycle stream resumes at the coordinator's own cursor

- **Status:** Accepted
- **Date:** 2026-09-30
- **Decided by:** the owner, 2026-09-30 (recorded on `nocx-zg3k3.5.11`): "The old coordinator
  accepted something and wrote it into its database. The coordinator should say from which mark it
  needs events. For old events the helper no longer has, mark the blocks from what the helper
  reports." — and "yes" to the restatement: the lifecycle leg resumes from the coordinator's own
  stored cursor, as the PTY leg does; what it already applied is never replayed; what happened while
  it was away arrives once; a range the helper no longer holds is marked with its loss cause. And,
  the same day, on the escalation of this record's first draft: applying one lifecycle frame and
  storing the cursor are ONE SQLite transaction — no window in which a killed process re-delivers a
  frame — and every write a frame causes is idempotent, keyed by a stable identity of the block, so
  a frame delivered twice is a no-op. And, later that day, on the bound: a frame's hold on the store
  is bounded at 250 ms; a frame that fails — by the bound or by a store error — is applied again by
  the same coordinator up to three more times (pauses of 50, 100 and 200 ms), and only then does the
  leg halt, visibly; the deadlock the frame's context guards against is detected in tests by a
  goroutine-identity check behind a build tag and in production by the bound alone; and a frame that
  arrives while the coordinator is stopping is left for the next one. And, on the retry: a failed
  attempt changes no block state — ADR-0076 decision 2, a store write failing is not something the
  helper reported — so no projection code branches on it, and in-memory block state agrees with the
  store after every frame.
- **Supersedes:**
  - [ADR-0024](0024-authenticated-shell-integration-channel.md), the 2026-09-02 amendment's bullet
    "The re-attachment resumes at the lifecycle window's HEAD, not its base." The re-attachment now
    resumes at the coordinator's own stored cursor; the head survives only as the answer for a
    binding that stored none. The bullet's REASON is kept whole — a replay from the base would
    re-deliver authenticated events into the new kernel — and this record is built on it.
  - The base resume of `nocx-zg3k3.5.11` Round 1 (`8b0586758`), which was never an accepted
    decision and contradicted that bullet.
- **Related, and NOT superseded:** [ADR-0076](0076-the-coordinator-going-away-changes-no-block.md)
  (decision 4 — an end the helper saw while the coordinator was away closes the block on return — is
  what this record delivers); [ADR-0024](0024-authenticated-shell-integration-channel.md) otherwise,
  including adoption itself.
- **Beads:** `nocx-zg3k3.5.11`.

## Context

When the coordinator (`nocx-server`) restarts, the helper keeps the shell and a bounded window of
the lifecycle bytes the shell wrote. The replacement adopts the shell's domain and attaches to the
session again, and it must say where in that lifecycle stream it wants to start.

Two answers had been tried, and each was wrong in one direction:

- **The window's head** (ADR-0024's amendment). Nothing is re-delivered, but every frame the shell
  spoke while no coordinator was attached is skipped — the end of a command that finished while
  nocx was away never reaches the new kernel, and its block stays running. ADR-0076 decision 4 says
  such an end settles the block.
- **The window's base** (Round 1). Nothing is skipped, but every frame the previous coordinator
  already applied is re-delivered with a capability the adopted domain still honours:
  `TestTheAdoptedChannelDoesNotReplayCommandsThatAlreadyRan` fails with the first command authenticated
  a second time.

Neither the head nor the base is a fact about what the previous coordinator did. Only the
coordinator knows that, and the PTY leg already works this way: it resumes from the recording's
length — what this machine stored — and never from anything the helper guesses.

## Decision

1. **Each coordinator keeps its own lifecycle cursor, and the next one resumes there.** The cursor
   is a `proto.StreamOffset` into the helper's lifecycle stream — one past the last frame this
   coordinator applied — and never a wall-clock time. It is kept in the session's binding, the row a
   re-adopt already reads (`content.Session.LifecycleApplied`). The binding is born with it at 0,
   because the leg attaches at the stream's start and its bridge starts only after the binding is
   written.
2. **A frame's writes and the cursor past it are one transaction.** `content.ApplyLifecycleFrame`
   runs the kernel's ingest of one frame under a context that carries the frame, and every store
   write the frame's projection makes under it — the entry, its execution, the block's artifact, the
   settle of a block the frame ends — joins one SQLite transaction whose last statement moves the
   cursor. They commit together or not at all: a process killed anywhere inside the frame leaves the
   store exactly as it was before it, and the next coordinator applies the frame once. A write that
   fails inside the frame fails the attempt, and a failed attempt commits nothing, cursor included
   (decision 9 says what follows). A refused frame is scoped like any other: the stream carried it
   and the coordinator dealt with it. A coordinator going away finishes the frame it is applying — its
   transaction commits — and applies nothing after it: that frame is the next coordinator's.
3. **Nothing before the cursor is re-delivered; everything after it is delivered once.** A command's
   end spoken while nobody was attached reaches the returning coordinator exactly once and settles its
   block, however many times the coordinator is replaced around it.
4. **A binding with no cursor resumes at the head.** A coordinator that holds no record of what was
   applied may not offer any of it again; ADR-0024's answer stands exactly where nothing better is
   known.
5. **A cursor the helper no longer holds is a stated loss.** When the helper's window has moved past
   the cursor it answers from its base (`Resume.Reset` with a `Gap`), and the frames between are
   gone. The session's open block — the one those frames could have settled — is sealed as a block
   whose boundary never arrived whole (the store's `gap` truncation, the same statement a lost
   boundary makes) and its entry closes `unknown`. It is never left running on the hope that its end
   was not in the gap. The trigger is the helper's own statement of the loss (ADR-0076 decision 2).
6. **A replay's end waits for the replay to be applied, not merely read.** The per-session end hold
   (`ws_end_hold.go`) lifts when the leg's applied cursor reaches the window's head, or the leg
   stops — not when its bytes were read, since read bytes are not yet facts the kernel has published.

7. **A repeated frame is a no-op by the identity of its block, not only prevented by the cursor.**
   The cursor keeps a frame from being offered twice; identity makes a frame offered twice
   harmless. Every shipped shell that speaks the channel (bash and zsh; the POSIX tier has none,
   by design) mints an attempt id per command at start and names it on the start AND on the
   complete (script version 55), so every frame of a command carries the same stable name for its
   block, whatever coordinator applies it. The kernel resolves a named completion the way it
   resolves a snapshot's `last_completed`: exact id, then this domain's alias. A command the shell
   started is stored under that id; one submitted from nocx's editor is stored under the app's id,
   and its execution records the shell's id beside it (`StartExecution.ShellAttempt`), so a
   coordinator that knows the command only by the shell's id finds its entry by that id
   (`EntryForShellAttempt`, `storedAttempt`) instead of opening a second one. What each frame kind
   does when offered again to a fresh coordinator over the same store: a start finds its entry
   already bound and starts nothing; a complete finds its entry closed and changes nothing, and
   never closes the running next command in its place; the shell's exit settles nothing already
   settled; a snapshot is refused, because a fresh kernel asked for none.
8. **A frame holds its lane's emission turn from its first write to its end.** `Publisher.Ingest`
   takes the lane's turn before the kernel sees the frame. ReplayLane takes the same turn and then
   writes the store; were a frame to hold the store's connection and then wait for the turn, the two
   would wait on each other.

9. **A frame's hold is bounded, a failed attempt changes no block state and is tried again, and the
   halt is the last resort.** A frame's transaction may hold the store's one connection for
   `content.LifecycleFrameMaxHold` = **250 ms**, counted from its first write; past it the
   transaction is rolled back where it stands and the connection is free again at once, exactly as a
   transaction whose write failed. Either way the frame is not over: at its next store call — the
   failed write itself, or whatever the frame does next — the SAME frame pauses (50, 100, then
   200 ms), begins a fresh transaction, replays in order the writes it had already made, and carries
   on, up to three more times; the leg's next frame waits, so the order the shell spoke in holds.
   The cursor is the frame's last write, so a commit that fails is replayed the same way.

   A FAILED ATTEMPT CHANGES NO BLOCK STATE. [ADR-0076](0076-the-coordinator-going-away-changes-no-block.md)
   decision 2 holds here as everywhere: a block's state changes only on something the helper
   reports, and a store write failing is not something the helper reported. So no projection code
   branches on a failed write: the retry happens underneath the write, and the write the projection
   made answers it as a write that never failed would. The block stream's own failure branches — an
   open left pending a retry, a close parked or abandoned, a seal refused into an orphan, rows
   requeued — are never reached by a failure the frame recovers from. That is what makes replaying
   the recorded writes correct, and it is the whole of the argument: because the projection never
   saw the failure, the writes it made are exactly the writes a frame that never failed makes, and
   the in-memory block state it built from their answers is exactly what those writes store once
   they commit. Memory and store agree by construction, with nothing re-read. The replay is not
   trusted blindly either: the store changed under the frame while it paused only if a replayed
   write answers differently from its first run — refused where it was accepted, or naming another
   execution or artifact than the one the projection holds — and then the frame fails rather than
   commit a frame nobody decided (`content.ErrFrameReplayDiverged`). Every recorded write keeps its
   first answer (`frameAnswer`): whether Submit minted a row (history may be turned on during the
   pause) and whether it found one already there, StartExecution's execution id, OpenBlockOutput's
   artifact — the refused keep included — and CloseBlockRows' summary. A value the store merely
   stamps afresh on every run (`submitted_at`, the next `ingest_seq`, which another writer may take
   during the pause) is not a decision, no caller inside a frame reads it, and it is not compared.

   THE BOUND DECIDES UP TO THE COMMIT. Disarming the bound as the commit begins can lose to a
   callback that already started; the commit then waits for the callback and begins the transaction
   again rather than commit one the bound rolled back. SQLite's COMMIT itself cannot be interrupted —
   a rollback racing it would only lose, since a transaction ends once — so the commit (measured up
   to 11 ms) is the one part of a hold the bound does not cut short.

   THE CURSOR MUST LAND. The cursor is the frame's last write and its proof of being applied: a
   write that matches no binding — the session's row is gone, or it records no cursor — fails the
   frame (`content.ErrLifecycleCursorMissing`); a cursor already at or past the frame is the
   legitimate no-op. Every binding a lifecycle leg is bound to is born with its cursor at 0.

   A WRITE THE STORE REFUSES IS ITS ANSWER, NOT A FAILURE: a missing entry, a discontinuous append, an
   id already used. Its savepoint is rolled back, the frame goes on, and the projection branches on
   the answer as it always has. A failure is the database's own error, a transaction that ended
   under the write, or a closed store.

   Only when the fourth attempt fails does the frame fail. Every store call it still makes then
   answers `content.ErrLifecycleFrameFailed`, and every block-stream branch that decides on a store
   error returns on that at once — it is no answer about any block. Nothing of the frame is stored,
   what would tell someone else it was (the helper's row confirmations, the history receipt, the
   finished notification) waits for a commit that never comes, and when the frame ends the session's
   block state is read again from the store exactly as a coordinator that went away and came back
   reads it (`lifecyclecommit.After`; the transport's `rebindBlockRowsFromStore`), so whatever
   the frame's projection did in memory before its last attempt failed is gone with the rows it
   wrote. The leg then halts: it applies nothing more, the domain is not marked lost (the shell's
   channel did not fail), and the loss reaches the pane's integration axis as `store-refused` through
   the adapter's loss report — `lost` / channel-lost once the pane had integrated,
   channel-unavailable before — with one error line naming the frame's kind, the session, the
   attempts and the hold. The next coordinator applies the frame once, from the cursor.

   THE BASIS FOR 250 MS, measured on the restart and shell-exit acceptances with the hold timed from
   the frame's first write to the end of its commit: a plain run (119 frames) p50 0.70 ms, p99
   12.7 ms, max 35.8 ms; the loaded bar (the acceptances beside `go test -race -count=5
./internal/transport`), two runs of 232 and 234 frames, p50 0.73 / 0.66 ms, p99 21.9 / 11.4 ms,
   max 52.1 / 17.9 ms, and one frame of 106.5 ms. Almost all of it is store work — store time p99
   9–16 ms, the commit itself up to 11 ms on the encrypted WAL — and the non-store code between a
   frame's writes (the rest of the fact's delivery, the kernel's attempt lookup) is p50 5–40 µs, its
   tail (up to 6 ms loaded) CPU contention under the race-detector load. A reader outside the frame
   waited at most 6.6 ms, a writer at most 52 ms, under load. 100 ms, the first figure, had already
   been crossed once under load; 250 ms leaves the measured worst case more than twice over.

10. **The frame's context contract is enforced in tests, and only the bound enforces it in a shipped
    build.** A store call on the frame's own goroutine without the frame's context would wait for the
    connection the frame holds while the frame waits for the call. Telling that call from another
    goroutine's — which must wait — takes the calling goroutine's identity, which Go deliberately does
    not offer, so the check that reads it from the runtime's stack lives only behind the
    `nocx_framecheck` build tag (`internal/content/framecheck_on.go`): there such a call fails at
    once with `ErrFrameContextMissing`, naming this contract — a write as an error, a read as a
    panic, since a `*sql.Row` cannot carry the store's own error. CI's Go test runs pass the tag
    (`.github/workflows/ci.yml`, `scripts/ci-linux.sh`, `.githooks/containerized-tests.sh`, and the
    Makefile's `test` and `test-ci` passes through `FRAMECHECK_TEST_TAGS`). A shipped build compiles
    the no-op twin (`framecheck_off.go`), and `TestTheFrameCheckIsOnlyInTheTaggedBuild` keeps any
    stack-reading code out of it; there, the 250 ms bound is what ends such a wait — the missed call
    then completes on its own and the frame is tried again.
11. **A frame arriving while the coordinator stops is left for the next one.** From the moment
    `App.Shutdown` begins, a lifecycle frame is not applied at all — neither by the kernel nor by the
    store — and the cursor stays before it (`lifecycleCursor.applyFrame`,
    `lifecyclechannel.ErrFrameLeftForNext`). Stopping closes the sessions a frame's projection
    records against, so what it would store is not what the frame says (a start whose entry is not
    recorded and whose block open fails on the missing entry was measured doing exactly that). A
    frame already in hand when stopping began is left the same way whatever its writes answered: once
    its projection has run, a coordinator that is now stopping abandons it (`content.ErrFrameAbandoned`)
    and nothing of it is committed. Its writes need not fail for it to be wrong — measured on the
    loaded bar, the start's projection found the session closing, recorded no entry, the block's open
    was answered (no such entry) rather than failed, and a frame that merely answered committed its
    cursor, so the next coordinator never saw the command begin. That is a handover: no error line, no halt, nothing reported to the pane; the next coordinator applies
    the frame from the cursor.

12. **What a frame tells anyone outside the process waits for the frame's commit, in one ordered
    queue.** A frame's ingest decides things the store records and things it tells others: the shell
    its ACCEPT, a grant or an enrolment's answer and any other outbound envelope
    (`lifecyclepub.Publisher.Ingest`); the renderer its lifecycle fact, integration axis, recovery
    episode, block notifications and history receipt (`transport`); the helper its completion or
    environment entry (`helper/client.CompletionDownlink`) and the confirmation that its rows are
    stored; the notification feed its finished command. Telling any of them before the frame's
    transaction commits tells them of a frame that may fail every attempt — and then the store
    never holds what they were told, or the next coordinator applies the frame and tells them twice.
    So each of these goes through one queue per frame (`internal/lifecyclecommit`), begun and ended
    by the store's frame, in the order the frame caused it — the ACCEPT before the lifecycle fact it
    must precede — run once the frame commits and dropped when it does not. A completion keeps its
    place in the downlink's queue from its acceptance, so what another source accepted after it still
    reaches the helper after it; a frame that fails takes it out. Outside a frame every effect runs at
    once, so a leg with no store, a replay and the rows plane are unchanged.

    THE SHELL'S HALF. A frame carrying a hello whose ACCEPT is withheld and which then fails every
    attempt leaves the shell unanswered: its domain is established in this coordinator's kernel with
    the accept pending, the shell's hook stays unadmitted (conventional, the safe direction), and the
    leg halts with `store-refused` as decision 9 says. The next coordinator resumes the helper's
    stream at the cursor, which never passed that hello, so the hello is offered again; the domain it
    adopts is Established, and the kernel answers a hello on an established domain with a fresh
    ACCEPT ("reconnect within the epoch"), which that coordinator's committed frame sends. The shell
    receives exactly one ACCEPT — never one from the frame that was not stored.
    `TestAnAcceptWaitsForItsFramesCommitAndTheNextCoordinatorSendsItOnce` measures it;
    `TestAFrameThatIsNeverStoredTellsTheRendererNothing` and
    `TestACompletionFromAFrameIsDeliveredOnlyIfTheFrameIsStored` measure the renderer and the helper.

## Why not the helper's acknowledgement cursor

The helper already keeps a per-subscriber lifecycle `acked` cursor, and it is the obvious thing to
resume from. It cannot serve. The coordinator acknowledges lifecycle bytes as its reader hands them
to the bridge (`internal/helper/client/sessions.go`, `attachedLifecycle.Read`), before any frame is
decoded or applied, so the ack runs ahead of the effect by exactly the frames a coordinator dying
mid-stream loses. And it is keyed by a subscriber id each attachment mints afresh
(`session_readopt.go`, `rand.Read(subscriberRaw[:])`), so the next coordinator has no way to name the
previous one's cursor. A cursor that must survive the process has to live where the process's
effects live — the coordinator's store.

## Consequences

- `TestTheAdoptedChannelDoesNotReplayCommandsThatAlreadyRan` passes unmodified: its binding carries
  no cursor, and decision 4 resumes it at the head.
- **The guarantee is transactional.** A frame's rows and its cursor are one commit; there is no
  interleaving of rows stored and cursor not, whether the process is killed, a statement fails, or
  the coordinator detaches. Measured by `TestAFaultInsideALifecycleFrameLeavesNeitherItsRowsNorTheCursor`
  (a fault injected inside the frame's writes and one between its last row and the cursor, each on
  every attempt), `TestAFrameRolledBackByItsHoldBoundIsAppliedOnceByTheSameCoordinator`,
  `TestAFrameFailedAtTheCursorIsAppliedOnceOnItsNextAttempt` and
  `TestAFrameTheStoreRefusesOnEveryAttemptHaltsTheLegAndIsAppliedOnceByTheNext` (the process
  restarts on the stored cursor and applies the frame once).
- **A failed attempt changes no block state** (decision 9), measured through the real projection:
  `TestAWriteThatFailsOnceLeavesTheBlockAsIfItHadNotAndItSealsNormally` (the block's open fails on
  the frame's first transaction; afterwards memory and store hold the same open block at the same
  row cursor, and it takes its rows and seals on the helper's end) and
  `TestAFrameThatFailsEveryAttemptChangesNoBlockAndTheNextCoordinatorAppliesItOnce` (with the end
  marker before and after the frame: memory, store and ledger exactly as before it; the next
  coordinator applies it once). In the store: `TestAWriteThatFailsOnceAnswersItsFrameAsIfItHadNot`,
  `TestAWriteTheStoreRefusesIsItsAnswerAndTheFrameCommits` and
  `TestAReplayTheStoreAnswersDifferentlyFailsTheFrame`.
- **What this asks of every caller inside a frame:** each store call it makes carries the context the
  frame handed it. The store holds its one connection (`maxOpenConns` is one) from the frame's first
  write to its end, so a call on the frame's goroutine with any other context waits for a connection
  the frame will never release. The transport's lifecycle projection, the block stream and the
  completion downlink's loss report were threaded for this; a new path that writes the store from
  inside a frame must be too, and CI's `nocx_framecheck` runs are what catch it when it does not
  (decision 10).
- Other store users wait while a frame holds the connection — for the length of one frame's writes,
  and never longer than the 250 ms bound plus the frame's own commit or rollback.
  A completion the downlink finally gives up on is reported only after its queue has made room, so
  an Accept waiting for room inside a frame is never waiting on that report's write.
- A frame that fails every attempt ends the leg for this coordinator, and the pane shows it. That is
  the price of never storing a later frame past one the store does not hold; the next coordinator
  resumes before it. The session's block stream is read again from the store at that moment, which
  forgets what it held in memory beside it — rows not yet stored and not confirmed, which the helper
  still holds, and ends waiting for a fence the halted leg will never publish — as a coordinator
  restart does. A frame that fails and then succeeds costs its lane at most 350 ms of pauses,
  during which the lane's later frames wait and the connection is free for everyone else; a write
  outside the frame that lands in a pause sees the store without the frame's rolled-back writes.
- A coordinator's orderly shutdown waits for the frame it is applying, however long that frame's
  effect takes.
- Round 1's seam test that pinned the base resume is replaced by one that pins the stored cursor
  and one that pins the head for a binding without one.
