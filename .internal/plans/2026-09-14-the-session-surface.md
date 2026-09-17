# The session surface — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use beads-superpowers:subagent-driven-development
> (recommended) or beads-superpowers:executing-plans to implement this plan task-by-task. Each Task
> becomes a bead (`br create -t task --parent nocx-6q1uh`). Steps use checkbox (`- [ ]`) syntax.
>
> **Plan shape.** This repository's plans name files, interfaces with exact Go signatures, and
> acceptance criteria written as assertions (AGENTS.md testing rule 4: "write acceptance criteria as
> assertions rather than prose"); they do not pre-write implementation bodies, which the implementer
> derives test-first against the current tree. Every new type and function another task consumes is
> spelled out below, so no task invents a name a neighbour does not know.

**Goal:** an orchestrating agent — a coordinator `claude` in a nocx pane, or nocx's assistant over
workers it spawned — reads a descendant's pane, answers its menus, sends it keys and messages it
through `session.read`, `session.keys`, `session.message`, with nothing written onto a screen it did
not see and nothing written after its authority was revoked.

**Design (binding):** `.internal/specs/2026-09-14-the-session-surface-design.md`, revision 6
(`22377b6b`). Section references below (§n) are to that document. When this plan and the spec
disagree, the spec wins and the plan is corrected.

**Architecture:** each helper session gets one I/O owner goroutine that alone orders output ingest,
replies, client frames, intents, resize and shutdown, with a commit point at the idle writer's head
(§5). Targets are one-shot HMAC tokens the helper mints from retained snapshots (§6). Authority is a
`DescendantPaneAccess` capability bound by each adapter; revocation bumps coordinator generations and
an acknowledged helper access epoch, or waits out a monotonic `commitBy` (§7). The tools replace
`workers.screen`/`workers.answer`; the owed task becomes a queued message (§8–9).

**Tech stack:** Go 1.26 (`go.mod`), `golang.org/x/sys/unix`, `github.com/creack/pty` v1.1.24,
`golang.org/x/crypto/ssh` v0.54.0 (helper local variant only), JSON Schema contracts in `contracts/`.

## Global constraints

- Owner invariants (settled; never propose otherwise): every session's PTY/channel is owned by a
  helper, never by `cmd/nocx-server`; the backend answers terminal queries, xterm never; no SSH
  client in the coordinator; `cmd/nocx-server` stays `CGO_ENABLED=0`; the deployed helper artifact
  links no SSH client (`internal/helper/deploy/dependency_test.go`).
- AD-1, AD-6 (as amended by Task 12), AD-7, AD-8 bind every task. ADRs are never edited; a change is
  a new record (AGENTS.md "Before you fix anything" §4).
- Every JSON-RPC result shape touched gets its schema in `contracts/` in the same commit, with
  `additionalProperties: false` and explicit `required`; Go side has `…_DTOConformsToContract` and
  `…_OverTheWireConformsToContract` (AGENTS.md rule 5).
- For every external call a failure-path test, each paired with its ordinary success (rule 3).
- No test depends on timing: wait on an observable state change (a frame count, a record, a fence),
  never on a duration. Watchdogs assert progress, not elapsed time.
- Workers run only the unit tests of the packages they touch, with the build tags those packages
  need: `-tags gtk3` for anything that reaches cgo on Linux, `-tags nocx_local_ssh` for
  `internal/helper/sshdial`, `internal/helper/sshsvc` and the local helper variant. No `make ci`,
  `make ci-full`, containers or e2e in a worker (AGENTS.md "Git authority"). The coordinator runs
  `make ci-full` once on the merged tree.
- A Go package added lands with the wiring that makes it reachable, or its commit fails the deadcode
  ratchet; check a seam with `deadcode -tags gtk3 -whylive '<pkg>.<Symbol>' ./...`.
- Commit format `<type>(<scope>): <subject> (<bead-id>)`, prose body, ending
  `Co-Authored-By: …`. Stage files by name; never `git add -A`, never bare `git stash`.
- Constants named once and referenced by tests: `maxLiveTokens = 256`, `tokenLifetime = 60 s`,
  `resultRetention = 5 min`, `maxDetachedWriters = 8`, `snapshotRing = 8`, `snapshotMaxAge = 2 s`,
  `commitWindow = 5 s`, `regionNowMax = 16 KiB`, `optionSettle = 2 s`.

---

## Files and responsibilities

| Path                                                                                                                                                                                                                    | Responsibility                                                                                 | Task |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- | ---- |
| `internal/agentdriver/document.go`, `predicate.go`, `agentdriver.go`, `explain.go`                                                                                                                                      | `cursor` anchor for extractors; `inputBox`/`menuZone` regions; `Observation.Menu()` projection | 1    |
| `internal/agentdriver/claude.rule.json`                                                                                                                                                                                 | Claude menu extractors, input box, menu zone                                                   | 1    |
| `internal/agentdriver/manifest_test.go`, `testdata/captures/manifest.json`                                                                                                                                              | menu identity asserted at menu moments                                                         | 1    |
| `internal/pty/pty_local.go`, `internal/pty/master_nonblock_{linux,darwin}.go` (new)                                                                                                                                     | master fd non-blocking before wrap; `RawReadUntilAgain`                                        | 2    |
| `internal/helper/session/owner.go` (new)                                                                                                                                                                                | the session I/O owner: queue, drain, commit point, fences, reply reserve, resize, shutdown     | 2    |
| `internal/helper/session/session.go`, `runtime.go`, `service.go`                                                                                                                                                        | `hostSession` routes pump/write/resize/stop through the owner                                  | 2    |
| `internal/sessionruntime/contract.go`, `runtime.go`                                                                                                                                                                     | replies handed to a non-blocking sink; `Commit` validates+encodes without writing              | 2    |
| `internal/helper/session/spawn_ssh.go`, `internal/helper/sshsvc/shell.go`, `internal/ssh/pool.go`                                                                                                                       | SSH sessions under the owner: no barrier, detached writer, pool taint, cap                     | 3    |
| `internal/sessionruntime/digest.go` (new), `internal/helper/session/snapshots.go`, `tokens.go` (new)                                                                                                                    | structural digest v1, snapshot ring, token mint/verify, one-shot records                       | 4    |
| `internal/helper/proto/intent.go` (new), `internal/helper/session/service.go`, `internal/helper/client/intent.go` (new), `internal/monoclock/` (new)                                                                    | wire ops `session.snapshot/target/intent/intent.status/access.bump`; monotonic clock           | 5    |
| `contracts/helper/session.{snapshot,target,intent,intent-status,access-bump}{.params,}.schema.json` (new)                                                                                                               | helper op contracts                                                                            | 5    |
| `internal/workers/workers.go`, `registrar.go`                                                                                                                                                                           | `Delegation.ControllerEpoch` fixed (`nocx-bm99e`)                                              | 6    |
| `internal/workers/access.go` (new), `store.go`, `internal/app/pane_access.go` (new), `worker_auth.go`                                                                                                                   | `DescendantPaneAccess`, generations, revocation fan-out                                        | 7    |
| `internal/agenttools/registry.go`, `session_access.go` (new), `internal/assistant/dispatch.go`, `blocks.go`, `internal/toolendpoint/catalogue.go`, `contracts/tools/session.read.schema.json`                           | `session.read` from helper snapshots with targets; catalogue = dispatch                        | 8    |
| `internal/assistant/execute_session_keys.go` (new), `contracts/tools/session.keys.schema.json` (new)                                                                                                                    | `session.keys` key/text/option                                                                 | 9    |
| `internal/app/pane_messages.go` (new), `internal/assistant/execute_session_message.go` (new), `contracts/tools/session.message.schema.json` (new)                                                                       | `session.message` queue, delivery, cancel                                                      | 10   |
| `internal/app/workers.go`, `internal/agenttools/registry.go`, `internal/assistant/execute_workers.go`, `contracts/tools/workers.{screen,answer}.schema.json` (deleted), `contracts/agent.approvalRequested.schema.json` | owed task as message; `workers.screen/answer` removed                                          | 11   |
| `docs/decisions/00NN-…md` (new), `docs/decisions/INDEX.md`, `docs/architecture.md`                                                                                                                                      | ADR, AD-6 amendment                                                                            | 12   |
| `internal/helper/session/owner_adversarial_test.go`, `internal/app/pane_access_adversarial_test.go` (new)                                                                                                               | independent adversarial tests                                                                  | 13   |
| `internal/app/session_surface_happypath_test.go` (new), `.claude/skills/nocx-detection-verify/SKILL.md`                                                                                                                 | end-to-end check, live procedure                                                               | 14   |

## Order and parallelism

```
T1 (agentdriver) ─────────────────────────────┐
T2 (owner, local) ──┬─ T3 (SSH) ──────────────┤
                    └─ T4 (digest/tokens) ─ T5 (wire) ─┐
T6 (Delegation epoch) ─ T7 (access) ───────────────────┤
                                  T13a (adversarial tests, from interfaces of T4, T5, T7)
                                                       └─ T8 (session.read) ─ T9 (keys) ─ T10 (message) ─ T11 (removals) ─ T14 (e2e, live)
                                                                               T12 (ADR) beside T9
                                                                               T13b (run adversarial tests) after T10
```

Disjoint file sets allow T1, T2 and T6 at once; T3 and T4 after T2 in parallel (T3 touches
`spawn_ssh.go`, `sshsvc`, `pool.go`; T4 touches `digest.go`, `snapshots.go`, `tokens.go` only);
T7 in parallel with T3–T5.

---

### Task 1: The agent rule extracts a menu, an input box and a menu zone

**Files:**

- Modify: `internal/agentdriver/document.go` (anchor kind `cursor`, `Document.InputBox`,
  `Document.MenuZone`, validation), `predicate.go` (region capture keeps row index),
  `agentdriver.go` (`Observation` fields, `Menu()` projection), `explain.go` (readings),
  `claude.rule.json`, `manifest_test.go`, `testdata/captures/manifest.json`
- Test: `internal/agentdriver/menu_test.go` (new), `manifest_test.go`

**Interfaces:**

- Consumes: `paneview.Frame` (`internal/paneview/paneview.go:67-100`).
- Produces:
  ```go
  // agentdriver.go
  type RowSpan struct{ First, Last int } // inclusive, visible-screen rows; Last < First means none

  type Menu struct {
      Question string
      Options  []string   // as drawn, marker and numbering stripped
      Selected int        // index into Options; -1 when no selection is drawn
      Rows     RowSpan    // question row through last option row
      Body     RowSpan    // rows between question and first option; empty span when none
  }

  type Observation struct {
      State    State
      Extras   []Extra
      InputBox RowSpan // from Document.InputBox; empty when its anchor did not bind
      MenuZone RowSpan // from Document.MenuZone; empty when its anchor did not bind
  }

  const MenuExtra = "menu"
  func (o Observation) Menu() (Menu, bool) // false unless State is permission_choice or
                                           // modal_choice AND question, ≥1 option and the body
                                           // boundary were all found
  // document.go
  // AnchorSpec.Kind gains "cursor": binds at Frame.CursorY when CursorVisible. validate() refuses a
  // "cursor" anchor named by any Pred (the deliberate decision at document.go:372-381 stands); only
  // an Extractor, InputBox or MenuZone may name it.
  type Document struct { /* existing fields */ InputBox *RegionSpec `json:"inputBox,omitempty"`; MenuZone *RegionSpec `json:"menuZone,omitempty"` }
  // Extra rows gain the reserved key "_row" (decimal visible row index) set by region.capture.
  ```
- Claude rule (`claude.rule.json`): extractor `menu` anchored at `cursor`, `up: true`,
  `maxRows: 16`, with named groups `question`, `option`, `selected` (the `❯` marker) — the Go
  projection walks rows: options are consecutive option rows through the cursor row and below it
  (a second extractor `menuBelow`, `maxRows: 8`, anchored at `cursor`), the question is the nearest
  non-blank row above the first option that is not an option, the body is the rows between. `inputBox`
  is the region between `topRule` and `bottomRule`; `menuZone` is `topRule` `toEdge` downward
  (spec §6.3: from the top of the input box to the bottom of the screen).

**Acceptance Criteria:**

- `TestTheManifestHolds` passes with a new optional manifest field `menu {question, options,
selected}` asserted for every `bash-permission`, `write-permission`, `folder-trust`,
  `theme-picker` and `model-menu` entry; each asserted value was read off the capture by the
  implementer and is quoted in the commit body.
- A frame where the cursor row is an option but no question row exists above it yields
  `Menu() == (_, false)` while `State` still reads `permission_choice` or `modal_choice`
  (no `menu` target is possible, spec §6.3), paired with a real capture where `Menu()` is true.
- `validate()` refuses a rule whose `Pred` names a `cursor` anchor, with an error naming the anchor;
  a rule whose extractor names it compiles.
- `Observation.InputBox` and `MenuZone` are non-empty at `2.1.266-turn@…` free-text moments and
  `MenuZone` contains `Menu().Rows` at every menu moment above.
- `ReadMenu` (`internal/agenttyping/agenttyping.go:499`) is not changed in this task (Task 9 retires
  it); `paneobserve` is unchanged.

- [ ] **Step 1:** Write `menu_test.go` with three tests: `TestAMenuIsReadOffTheScreenItWasDrawnOn`
      (replay `2.1.266-permission@49000` via `replay(t, capture, atMs)` from `capture_test.go:98`,
      assert question contains `Do you want to`, options non-empty, `Selected == 0`),
      `TestAMenuWithoutAQuestionIsNoMenu` (hand-built `paneview.Frame`), and
      `TestACursorAnchorIsRefusedInAPredicate`. Run
      `go test ./internal/agentdriver/ -run 'Menu|CursorAnchor'` — expected FAIL (undefined `Menu`).
- [ ] **Step 2:** Add `RowSpan`, `Menu`, `Observation.InputBox/MenuZone`, the `cursor` anchor kind
      and its validation, `_row` in `region.capture`, and `Observation.Menu()`.
- [ ] **Step 3:** Add the Claude extractors, `inputBox`, `menuZone` to `claude.rule.json`. Run the
      three tests — expected PASS.
- [ ] **Step 4:** Extend `manifestEntry` with `Menu *manifestMenu` and `checkManifest` to compare
      `reg.Observe(...).Menu()`; add the values to `manifest.json`. Run
      `go test ./internal/agentdriver/` — expected PASS.
- [ ] **Step 5:** Commit `feat(agentdriver): the rule reads a menu, the input box and the menu zone (<bead>)`.

---

### Task 2: One I/O owner per helper session (local PTY)

**Files:**

- Create: `internal/pty/master_nonblock_linux.go`, `internal/pty/master_nonblock_darwin.go`,
  `internal/helper/session/owner.go`, `internal/helper/session/owner_test.go`
- Modify: `internal/pty/pty_local.go` (open master non-blocking), `internal/sessionruntime/contract.go`,
  `internal/sessionruntime/runtime.go`, `internal/helper/session/session.go`, `runtime.go`,
  `service.go` (`finishSpawn` starts the owner)
- Closes: `nocx-6q1uh.1`

**Interfaces:**

- Consumes: `Process` (`session.go:33-47`), `sessionruntime.Session` (`runtime.go:107`),
  `emulator.Terminal` (`emulator.go:245`).
- Produces:
  ```go
  // internal/pty
  // NewLocal opens the master with O_NONBLOCK set BEFORE os.NewFile on both platforms (on Darwin
  // creack/pty wraps a blocking fd, pty_darwin.go:14-18, so pty_local.go opens /dev/ptmx itself via
  // the build-tagged openMaster). A blocking-wrapped fd is never pollable.
  func openMaster() (master *os.File, slaveName string, err error)            // build-tagged
  func (lp *LocalPty) RawReadUntilAgain(buf []byte, deliver func([]byte)) (eof bool, err error)
      // SyscallConn().Read with a callback that loops read(2) until EAGAIN; never blocks
  func (lp *LocalPty) WaitReadable(ctx context.Context) error
      // SyscallConn().Read returning false until readable; used only by the readiness goroutine
  func (lp *LocalPty) InterruptWrite() error // SetWriteDeadline(time.Unix(1,0))

  // internal/sessionruntime
  type ReplySink interface { Reply(p []byte) error } // never blocks; ErrReplyReserveFull on overflow
  var ErrReplyReserveFull = errors.New("sessionruntime: reply reserve full")
  // Config gains Replies ReplySink; Ingest hands replies to it instead of writeLocked. On
  // ErrReplyReserveFull the session's completeness becomes CompletenessLostIngest and stays so for
  // the incarnation.
  type Commitment struct {
      Encoded []byte
      Fence   Fence        // assigned by the owner, recorded by Commit
  }
  type Fence uint64
  // Commit validates under s.mu (incarnation, completeness, and the caller's check) and encodes;
  // it writes nothing. The owner writes Encoded.
  func (s *Session) Commit(i Intent, check func(Snapshot) error) ([]byte, error)
  // Execute and writeLocked are deleted (Execute had no production caller).

  // internal/helper/session/owner.go
  type ownerItemKind int
  const ( itemReply ownerItemKind = iota; itemClientFrame; itemIntent; itemResize; itemAccessBump )
  type ownerItem struct {
      kind    ownerItemKind
      payload []byte
      intent  *pendingIntent        // itemIntent
      resize  *sessionruntime.Geometry
      done    chan ownerResult      // buffered(1)
  }
  // Task 2 defines the minimal shape; Task 4 adds Token and CommitBy fields.
  type pendingIntent struct { Intent sessionruntime.Intent; Check func(sessionruntime.Snapshot) error }
  type ownerResult struct { State sessionruntime.IntentState; BytesWritten int; FenceAfter sessionruntime.Fence; Err error }
  type sessionOwner struct { /* queue, writer state, fence counters, reserve, closing */ }
  func newSessionOwner(proc Process, rt *sessionruntime.Session, win *window, log *slog.Logger) *sessionOwner
  func (o *sessionOwner) run()                                   // the owner goroutine
  func (o *sessionOwner) submit(it ownerItem) (<-chan ownerResult, error) // errOwnerClosing, errBusy
  func (o *sessionOwner) Reply(p []byte) error                    // implements sessionruntime.ReplySink
  func (o *sessionOwner) inputFence() sessionruntime.Fence         // highest completed write fence
  func (o *sessionOwner) stop(graceful bool, deadline time.Time) (tailLost bool)
  const replyReserveBytes = 64 << 10
  const intentQueueMax = 64
  ```
- `hostSession.pump` is deleted for local processes (the owner reads); `hostSession.write` submits
  `itemClientFrame` after its lease checks and returns the submit error; `hostSession.resize`
  submits `itemResize`; `hostSession.stop` calls `owner.stop` in the order of spec §5.7 (request
  termination, interrupt writer, read to EOF, resolve items, join, close readable side, close runtime
  and screen).
- Frames: `paneview.Frame` gains `InputFence sessionruntime.Fence`, stamped by the owner at read.

**Acceptance Criteria:**

- `TestAProgramThatFloodsAndDoesNotReadKeepsItsPaneAlive` (real PTY, `internal/helper/session`):
  a program writes continuously, never reads stdin, and asks DSR; the frame count keeps increasing
  past a watchdog of N frames (no duration); after the program starts reading, it reads `ESC[1;1R`;
  a program that never reads drives completeness to `lostIngest` and output still arrives.
- `TestOneDrainConsumesEverythingReadable` (`internal/pty`, Linux and Darwin build): 4 KiB written
  to the slave before the call; one `RawReadUntilAgain` delivers all 4 KiB and returns without
  blocking; a second call delivers 0 bytes and returns immediately — both with no sleep.
- `TestTheWrapTimeFdIsNonBlocking`: `fcntl(F_GETFL)` on the master has `O_NONBLOCK`, and
  `SetReadDeadline` returns nil (not `ErrNoDeadline`) on both platforms.
- `TestAnIntentBehindABlockedClientFrameValidatesAtTheHead`: a client frame whose write is blocked
  (slave not reading, buffer full) precedes an intent; output changes the region while it waits;
  when the frame completes the intent is validated and refused `stale_target` with zero bytes of it
  on the PTY (read off the slave).
- `TestAClientFrameAndAnIntentNeverInterleave`: bytes read off the slave are the client frame then
  the intent's bytes, contiguous, over 1000 iterations with a concurrent submitter.
- `TestAResizeRacingACommitMakesTheTargetIncomparable` and `TestResizeWhileAWriteIsBlocked` (the
  resize completes after the write; outstanding targets refuse `incomparable`).
- `TestExitWithUnreadTailIngestsTheTail` and `TestForcedStopWithAProgramThatNeverClosesReportsTailLost`.
- `TestShutdownWithABlockedLocalWriteInterruptsAndJoins`.
- `failed_partial` reports the exact `bytesWritten` from a writer returning `n < len` (fake Process).
- `nocx-6q1uh.1` closes on the first test's commit; existing `internal/helper/session` tests pass.

- [ ] **Step 1:** Write the `internal/pty` tests; run `go test ./internal/pty/ -run 'Drain|NonBlocking'` — FAIL.
- [ ] **Step 2:** Implement `openMaster` (both platforms), `RawReadUntilAgain`, `WaitReadable`, `InterruptWrite`; PASS.
- [ ] **Step 3:** Write `owner_test.go` tests above against a real PTY and a fake `Process`; run
      `go test ./internal/helper/session/ -run 'Owner|Flood|Interleave|Resize|Tail|Shutdown'` — FAIL.
- [ ] **Step 4:** Change `sessionruntime` (ReplySink, Commit, delete Execute/writeLocked; update
      `contract_test` schedules that referenced Execute to Commit) and run `go test ./internal/sessionruntime/` — PASS.
- [ ] **Step 5:** Implement `sessionOwner`, rewire `hostSession` and `finishSpawn`; run
      `go test ./internal/helper/session/` — PASS.
- [ ] **Step 6:** `deadcode -tags gtk3 -whylive 'github.com/shady2k/nocx/internal/helper/session.sessionOwner.run' ./...` shows a path from `main`.
- [ ] **Step 7:** Commit `feat(helper): one I/O owner per session, and the pump never waits on a write (<bead>, nocx-6q1uh.1)`.

---

### Task 3: SSH sessions under the owner — no barrier, a detached writer, a tainted pool

**Files:**

- Modify: `internal/helper/session/spawn_ssh.go`, `internal/helper/session/owner.go` (reader-goroutine
  mode), `internal/helper/sshsvc/shell.go`, `internal/ssh/pool.go`
- Test: `internal/helper/session/ssh_owner_test.go` (new), `internal/ssh/pool_taint_test.go` (new)

**Interfaces:**

- Consumes: `sessionOwner` (Task 2), `channelFeed` (`spawn_ssh.go:588`), `ConnPool`
  (`pool.go:242-475`).
- Produces:
  ```go
  // owner.go
  type readMode int
  const ( readRawLocal readMode = iota; readViaReader )   // SSH uses readViaReader
  func (o *sessionOwner) hasReadBarrier() bool             // false for readViaReader
  // An itemIntent on a session without a barrier resolves refused, cause "no_read_barrier".
  const maxDetachedWriters = 8

  // internal/ssh/pool.go
  func (p *ConnPool) Taint(h *poolHandle)       // entry is never reused by acquire; closed at ref 0
  func (p *ConnPool) CloseTainted(h *poolHandle, reason string) // immediate close (cap exceeded)
  // internal/helper/sshsvc/shell.go
  func (c *ShellChannel) Taint()                // forwards to the pool handle
  var ErrDetachedWriterCap = errors.New("sshsvc: detached_writer_cap")
  ```
- Detach (spec §5.7): channel close (never mux close), join deadline, then detach: the writer's
  completion channel is buffered(1); the in-flight item resolves `delivery_unknown`; the session
  reports `writerDetached`; the first detach calls `Taint`; a detach beyond `maxDetachedWriters`
  (counted per helper process) calls `CloseTainted(…, "detached_writer_cap")` and the sibling
  sessions on that connection report that reason on exit.

**Acceptance Criteria:**

- `TestAnSSHSessionRefusesAnIntentAndStillServesASnapshot` over `cmd/e2e-sshd`-style fixture
  (`ssh_spawn_harness_test.go`): intent refused `no_read_barrier`, zero bytes on the far side; a
  snapshot of the same session succeeds.
- `TestAWriterBlockedOnAZeroWindowIsDetachedAndTheSessionCloses`: the fixture peer never adjusts the
  window; `stop` returns within the join deadline with `writerDetached: true`; a sibling channel on
  the same pooled connection still round-trips a write afterwards.
- `TestATaintedConnectionTakesNoNewChannels`: after `Taint`, a new acquire for the same key dials a
  new connection (dial counter = 2); the tainted one closes when its last sibling releases.
- `TestTheNinthDetachClosesItsConnection`: 8 detached writers tolerated; the 9th closes its tainted
  connection and its sibling session exits with reason `detached_writer_cap`.
- Existing `internal/helper/session` SSH tests and `internal/ssh` tests pass with `-tags nocx_local_ssh`.

- [ ] **Step 1:** Write the four tests; run `go test -tags nocx_local_ssh ./internal/helper/session/ ./internal/ssh/ -run 'SSHSession|ZeroWindow|Tainted|NinthDetach'` — FAIL.
- [ ] **Step 2:** Implement `readViaReader`, `no_read_barrier`, detach, `Taint`/`CloseTainted`, the cap.
- [ ] **Step 3:** Run the same command — PASS; run the packages whole — PASS.
- [ ] **Step 4:** Commit `feat(helper): an SSH session takes no conditional input, and a stuck writer is detached (<bead>)`.

---

### Task 4: Structural digest, snapshot ring, one-shot tokens

**Files:**

- Create: `internal/sessionruntime/digest.go`, `digest_test.go`, `internal/helper/session/snapshots.go`,
  `internal/helper/session/tokens.go`, `tokens_test.go`
- Modify: `internal/helper/session/owner.go` (commit point calls the token check),
  `internal/sessionruntime/runtime.go` (snapshot carries rows with style)

**Interfaces:**

- Consumes: `emulator.Row`, `emulator.Cell`, `emulator.Style`, `emulator.Cursor` (`emulator.go:140-231`),
  `Session.Commit` and `sessionOwner` (Task 2).
- Produces:
  ```go
  // internal/sessionruntime/digest.go
  type TargetKind string
  const ( TargetMenu TargetKind = "menu"; TargetInput = "input"; TargetWorking = "working"; TargetRegion = "region" )
  type RowRange struct{ First, Last int } // inclusive
  type ScreenIdentity struct { At Incarnation; AltScreen bool; BufferInstance uint64; Cols, Rows int }
  const DigestVersion = 1
  // Digest hashes identity, then for each row in r every cell's grapheme, width and style, and the
  // row's wrap flag; includeCursor adds cursor X, Y, Visible.
  func Digest(id ScreenIdentity, rows []emulator.Row, r RowRange, cur emulator.Cursor, includeCursor bool) [32]byte

  // internal/helper/session/snapshots.go
  type SnapshotID uint64
  type retainedSnapshot struct { ID SnapshotID; Taken time.Time; Identity sessionruntime.ScreenIdentity; Rows []emulator.Row; Cursor emulator.Cursor; Frame paneview.Frame; Revision sessionruntime.Revision; InputFence sessionruntime.Fence; Completeness sessionruntime.Completeness; AccessEpoch uint64 }
  const snapshotRing = 8
  const snapshotMaxAge = 2 * time.Second
  func (hs *hostSession) takeSnapshot() (retainedSnapshot, error)
  func (hs *hostSession) retained(id SnapshotID) (retainedSnapshot, bool) // false when evicted or older than snapshotMaxAge

  // internal/helper/session/tokens.go
  type TokenID [16]byte
  type Token struct { ID TokenID; Session proto.HostSessionID; At sessionruntime.Incarnation; Identity sessionruntime.ScreenIdentity; Kind sessionruntime.TargetKind; Rows sessionruntime.RowRange; Digest [32]byte; IncludeCursor bool; AccessEpoch uint64; MintedAt, ExpiresAt time.Time; MAC [32]byte }
  const maxLiveTokens = 256
  const tokenLifetime = 60 * time.Second
  const resultRetention = 5 * time.Minute
  var ( ErrCapacity = errors.New("capacity"); ErrForged = errors.New("forged"); ErrExpired = errors.New("expired"); ErrTokenSpent = errors.New("token_spent"); ErrSnapshotGone = errors.New("snapshot_gone") )
  type tokenBook struct { /* HMAC key per incarnation, slots, records */ }
  func newTokenBook(at sessionruntime.Incarnation, now func() time.Time) *tokenBook
  func (b *tokenBook) Mint(s retainedSnapshot, kind sessionruntime.TargetKind, rows sessionruntime.RowRange) (Token, error) // ErrCapacity
  func (b *tokenBook) Verify(t Token) error                                            // ErrForged, ErrExpired
  type canonicalIntent struct { Kind sessionruntime.IntentKind; Payload []byte; AccessEpoch uint64 }
  // pendingIntent (Task 2) gains: Token Token; Canonical canonicalIntent; CommitBy int64
  // Consume at the commit point: unused -> consumed bound to intent; same token+intent -> recorded or in_progress; different -> ErrTokenSpent.
  func (b *tokenBook) Consume(t TokenID, in canonicalIntent) (recorded *storedResult, inProgress bool, err error)
  func (b *tokenBook) Record(t TokenID, r storedResult)
  func (b *tokenBook) Status(t TokenID) (state string, r *storedResult) // "unknown" | "in_progress" | "recorded"
  type storedResult struct { State string; BytesWritten int; FenceAfter uint64; Cause string } // ≤128 bytes encoded
  ```
- Commit point (owner): `Verify`, `Consume`, identity comparison (`incomparable`), access epoch
  (`access_revoked`), `commitBy` (`commit_deadline`, Task 5 supplies the clock), then
  `Session.Commit` with a check that recomputes `Digest` over the live rows and compares.

**Acceptance Criteria:**

- `Digest` differs for: one grapheme changed, a width changed, a style attribute changed only
  (selection highlight), wrap flag changed, alt-screen toggled, geometry changed, cursor moved when
  `includeCursor` — and is equal for a change outside the row range (a spinner row).
- `Mint` on the 257th live slot returns `ErrCapacity` and no slot is evicted; a slot is released
  only when token expired AND `resultRetention` elapsed after terminal (fake clock); after release
  `Status` returns `unknown`.
- `Verify` refuses a token with one MAC byte flipped (`forged`), rows altered (`forged`), expired
  (`expired`), from another incarnation's key (`forged`).
- Concurrent `Consume` of the same token with the same intent from 16 goroutines: exactly one
  proceeds, the others get `inProgress` or the recorded result; with a different intent,
  `ErrTokenSpent`.
- An intent whose snapshot was evicted is refused `snapshot_gone` at mint.
- The stored result encodes in ≤128 bytes for every cause.

- [ ] **Step 1:** Write `digest_test.go` and `tokens_test.go`; run `go test ./internal/sessionruntime/ ./internal/helper/session/ -run 'Digest|Token|Snapshot'` — FAIL.
- [ ] **Step 2:** Implement `Digest`, the snapshot ring, `tokenBook`, the owner's commit-point checks.
- [ ] **Step 3:** Run — PASS. Commit `feat(helper): targets are minted from a retained snapshot and spent once (<bead>)`.

---

### Task 5: The wire — five helper ops, a shared monotonic clock, contracts

**Files:**

- Create: `internal/monoclock/monoclock.go`, `monoclock_linux.go`, `monoclock_darwin.go`,
  `monoclock_test.go`; `internal/helper/proto/intent.go`; `internal/helper/client/intent.go`,
  `intent_contract_test.go`; `contracts/helper/session.snapshot.params.schema.json`,
  `session.snapshot.schema.json`, `session.target.params.schema.json`, `session.target.schema.json`,
  `session.intent.params.schema.json`, `session.intent.schema.json`,
  `session.intent-status.params.schema.json`, `session.intent-status.schema.json`,
  `session.access-bump.params.schema.json`, `session.access-bump.schema.json`
- Modify: `internal/helper/session/service.go` (`Ops`, `ParamsSchema`, `Call`, `Refusal`,
  `RefusesCancel`), `internal/helper/proto/abi.go` (version bump)

**Interfaces:**

- Consumes: Task 4's `tokenBook`, `takeSnapshot`, owner.
- Produces:
  ```go
  // internal/monoclock
  // Now reads the machine monotonic clock as integer nanoseconds: Linux unix.ClockGettime(CLOCK_MONOTONIC),
  // Darwin unix.ClockGettime(CLOCK_MONOTONIC_RAW) (mach_continuous_time domain). Never time.Now().
  type Nanos int64
  func Now() Nanos

  // internal/helper/proto/intent.go
  const ( OpSnapshot = "session.snapshot"; OpTarget = "session.target"; OpIntent = "session.intent"; OpIntentStatus = "session.intent-status"; OpAccessBump = "session.access-bump" )
  type SnapshotParams struct { Session HostSessionID `json:"session"` }
  type SnapshotResult struct { SnapshotID uint64 `json:"snapshotId"`; Frame ScreenFrame `json:"frame"`; Revision uint64 `json:"revision"`; InputFence uint64 `json:"inputFence"`; Completeness Completeness `json:"completeness"`; AccessEpoch uint64 `json:"accessEpoch"`; ReadBarrier bool `json:"readBarrier"` }
  type TargetParams struct { Session HostSessionID `json:"session"`; SnapshotID uint64 `json:"snapshotId"`; Kind string `json:"kind"`; First int `json:"first"`; Last int `json:"last"`; IncludeCursor bool `json:"includeCursor"` }
  type TargetResult struct { Token string `json:"token"`; TokenID string `json:"tokenId"`; ExpiresAtMs int64 `json:"expiresAtMs"` }
  type IntentParams struct { Session HostSessionID `json:"session"`; Token string `json:"token"`; AccessEpoch uint64 `json:"accessEpoch"`; CommitBy int64 `json:"commitBy"`; Kind string `json:"kind"`; Payload []byte `json:"payload"` }
  type IntentResult struct { State string `json:"state"`; BytesWritten int `json:"bytesWritten"`; FenceAfter uint64 `json:"fenceAfter"`; RetryAfterMs int `json:"retryAfterMs,omitempty"`; Refusal *IntentRefusal `json:"refusal,omitempty"` }
  type IntentRefusal struct { Cause string `json:"cause"`; RegionNow string `json:"regionNow,omitempty"`; RegionTruncated bool `json:"regionTruncated,omitempty"`; RegionOmitted bool `json:"regionOmitted,omitempty"` }
  type IntentStatusParams struct { Session HostSessionID `json:"session"`; TokenID string `json:"tokenId"` }
  type IntentStatusResult struct { State string `json:"state"`; Result *IntentResult `json:"result,omitempty"` }
  type AccessBumpParams struct { Session HostSessionID `json:"session"`; Above uint64 `json:"above"` }
  type AccessBumpResult struct { Epoch uint64 `json:"epoch"` }

  // internal/helper/client/intent.go
  func (c *Client) Snapshot(ctx context.Context, id HostSessionID) (proto.SnapshotResult, error)
  func (c *Client) Target(ctx context.Context, p proto.TargetParams) (proto.TargetResult, error)
  func (c *Client) Intent(ctx context.Context, p proto.IntentParams) (proto.IntentResult, error)
  func (c *Client) IntentStatus(ctx context.Context, id HostSessionID, tokenID string) (proto.IntentStatusResult, error)
  func (c *Client) AccessBump(ctx context.Context, id HostSessionID, above uint64) (proto.AccessBumpResult, error)
  ```
- `session.intent` and `session.access-bump` are in `RefusesCancel` (mutations run to their commit
  point; transport cancellation never implies "not executed"). `session.access-bump` acknowledges
  only after every older uncommitted intent is terminal; it is idempotent on `above`.
- Refusal causes (closed set, schema enum): `stale_target incomparable expired forged token_spent
snapshot_gone completeness_unknown cannot_encode would_submit access_revoked no_read_barrier
commit_deadline capacity busy closing`.

**Acceptance Criteria:**

- `TestMonotonicNowIsSharedAcrossProcesses`: a child process prints `monoclock.Now()`; the parent's
  readings before and after bracket it (Linux and Darwin).
- For each of the five ops: `…DTOConformsToContract` and `…OverTheWireConformsToContract` through a
  real helper host, using `loadHelperSchema` (`internal/helper/client/abi_contract_test.go:37-67`).
- `TestAnIntentPastCommitByIsRefused` (at receipt and at the commit point, fake clock injected into the
  owner) → `commit_deadline`, zero bytes.
- `TestABumpAcknowledgesOnlyAfterOlderIntentsAreTerminal`: an intent queued behind a blocked write;
  `AccessBump` does not return until that intent is `access_revoked`; a repeat bump with the same
  `above` returns the same epoch.
- `TestARetryWhileTheFirstWriteIsBlockedGetsInProgress` then `IntentStatus` returns the recorded
  result with `regionOmitted: true`.
- `TestCancellingTheCallDoesNotCancelTheIntent`: the caller context ends; the helper still records
  a terminal result retrievable by `IntentStatus`.
- An old helper generation answering `ErrCodeUnknownOp` maps to a named client error
  `ErrIntentUnsupported` (paired with `ErrScreenUnsupported`'s existing test).

- [ ] **Step 1:** Write `monoclock_test.go`; implement; PASS.
- [ ] **Step 2:** Write the ten schemas and the contract tests; run
      `go test ./internal/helper/client/ -run 'Snapshot|Target|Intent|AccessBump'` — FAIL.
- [ ] **Step 3:** Implement proto types, service dispatch, client methods; PASS; run
      `go test ./internal/helper/...` (with `-tags nocx_local_ssh` for the helper variant packages) — PASS.
- [ ] **Step 4:** Commit `feat(helper): snapshot, target, intent, status and access bump on the wire (<bead>)`.

---

### Task 6: `Delegation` names the controller's incarnation correctly (`nocx-bm99e`)

**Files:** Modify `internal/workers/workers.go:311-325`, `internal/workers/registrar.go:296-306`;
Test `internal/workers/registrar_test.go`.

**Interfaces:**

- Produces:
  ```go
  type Delegation struct {
      ControllerSession  string
      ControllerIdentity session.Identity // the bound controller session's identity at delegation
      Participant        ParticipantID
      Generation         uint64           // bumped by revocation; Task 7
      CreatedByRunID     string
      Effects            []Effect
      State              DelegationState
  }
  ```
  `Epoch` is deleted (no production reader). `RegisterRequest` gains `CoordinatorIdentity session.Identity`,
  filled by both callers (endpoint: `sess.Identity()` of the admitted session; kernel: the run's session).

**Acceptance Criteria:**

- `TestADelegationRecordsTheControllersIdentityNotTheParticipants`: register with controller
  identity `{I1, 7}` and participant liveness epoch 3; the stored delegation has
  `ControllerIdentity == {I1, 7}`.
- `grep -rn '\.Epoch' internal/workers internal/app` shows no read of a delegation epoch.
- Store round-trip (`internal/content` or wherever `PutDelegation` persists) preserves both fields;
  the schema-change rule of ADR-0055 is followed (greenfield: refuse, no migration).

- [ ] **Step 1:** Test FAIL → implement → `go test ./internal/workers/ ./internal/app/ -run Delegation` PASS.
- [ ] **Step 2:** Commit `fix(workers): a delegation records the controller's identity, not the participant's (nocx-bm99e)`.

---

### Task 7: `DescendantPaneAccess` and revocation that cannot be outrun

**Files:**

- Create: `internal/workers/access.go`, `access_test.go`, `internal/app/pane_access.go`,
  `pane_access_test.go`
- Modify: `internal/workers/store.go` (store-wide mutex operations), `registrar.go` (terminalize,
  close, register call `bumpGeneration`), `internal/app/worker_auth.go` (`retire` fans out),
  `internal/app/app.go` (wiring)

**Interfaces:**

- Consumes: `Store` (`store.go:20-127`), `Delegation` (Task 6), helper client `AccessBump` (Task 5),
  `paneScreen.owner` (`internal/app/panescreen.go:134`) to find a session's helper.
- Produces:
  ```go
  // internal/workers/access.go
  type ChainLink struct { Participant ParticipantID; Generation uint64 }
  type Chain []ChainLink // from the target participant up to the bound controller session
  type Reach struct { Participant Participant; SessionID string; Chain Chain }
  var ErrNotReachable = errors.New("workers: not_reachable")
  // Resolve walks Delegation.ControllerSession upward (a controller that is itself a participant's
  // session continues the walk) until it reaches controller, under the store mutex; every link must
  // be DelegationActive and permit e. Plain shells are never participants, so never reachable.
  func (r *Registrar) Resolve(ctx context.Context, controller string, sessionID string, e Effect) (Reach, error)
  // StillHolds re-checks a chain's generations under the store mutex.
  func (r *Registrar) StillHolds(ctx context.Context, c Chain) bool
  // Revoke bumps the generation of every delegation under root (inclusive subtree) under the store
  // mutex and returns the affected pane sessions. Triggers: participant terminalized or closed;
  // controller admission retired (worker_auth.retire); controller session ended; DelegationState
  // leaving Active. (Today nothing leaves Active — registrar.go:296-306 is the only write.)
  func (r *Registrar) Revoke(ctx context.Context, root ParticipantID, cause string) ([]string, error)
  func (r *Registrar) RevokeController(ctx context.Context, controller string, cause string) ([]string, error)

  // internal/app/pane_access.go
  type DescendantPaneAccess struct { controller string; identity session.Identity; authority AuthorityInterval }
  type AuthorityInterval struct { Kind string /* "endpoint" | "kernel" */; AdmissionEpoch toolendpoint.AdmissionEpoch; RunID string }
  type paneAccessHub struct { /* registrar, helper lookup, per-session bump state */ }
  func (h *paneAccessHub) Bind(controller string, id session.Identity, a AuthorityInterval) *DescendantPaneAccess
  func (a *DescendantPaneAccess) Resolve(ctx context.Context, sessionID string, e workers.Effect) (workers.Reach, error)
  // revoke: Registrar.Revoke, then per affected session AccessBump with deadline; on no ack wait
  // until the latest commitBy this coordinator sent to that session has passed. Returns per session
  // ConfirmedBy "ack" | "deadline". Holds no coordinator lock across a helper call.
  func (h *paneAccessHub) revoke(ctx context.Context, sessions []string) map[string]string
  func (h *paneAccessHub) admitting(sessionID string) bool // false until a pending bump confirmed
  func (h *paneAccessHub) noteCommitBy(sessionID string, commitBy monoclock.Nanos)
  ```
- Binding (spec §7.1): the endpoint adapter binds in `toolAuthorizer.Admit` (`worker_auth.go:367`)
  from the admitted session; the kernel adapter binds for the run's session. Neither is inferred from
  parameters. The bound value is placed on `agenttools.RunContext` as `PaneAccess any` (typed at use).

**Acceptance Criteria:**

- `TestAGrandchildIsReachableAndANeighbourIsNot` (fake store): controller C → worker W1 → W1's
  worker W2; `Resolve(C, W2.session)` succeeds with a 2-link chain; a sibling controller's worker
  and a plain shell session return `ErrNotReachable`.
- `TestARevocationRacingAGrandchildSpawnSerialises`: `Revoke(W1)` and a spawn under W1 run
  concurrently 500 times; every spawn that completed after `Revoke` returned is unreachable from C.
- `TestAnIntentAdmittedBeforeRevocationIsRefusedAtCommit` (fake helper): intent checked, then a test
  hook holds it before `session.intent`; `revoke` runs; the intent reaches the helper with the old
  epoch and is `access_revoked`; `revoke` returned `ConfirmedBy: "ack"` only after that.
- `TestAHelperThatDoesNotAnswerIsWaitedOutByCommitBy`: fake helper never answers the bump;
  `revoke` returns after the latest `commitBy` (fake monotonic clock advanced) with
  `ConfirmedBy: "deadline"`; a later intent is not admitted until a fresh snapshot.
- `TestRetiringAnAdmissionRevokesTheControllersDescendants`: `worker_auth.retire` triggers
  `RevokeController`.
- No coordinator mutex is held during a helper call: a test helper that blocks forever does not
  block `Resolve` for another session (watchdog on a completed `Resolve`).

- [ ] **Step 1:** Write tests; `go test ./internal/workers/ ./internal/app/ -run 'Reachable|Revocation|Revok|CommitBy|Admission'` FAIL.
- [ ] **Step 2:** Implement; PASS. Commit `feat(workers,app): descendants are reachable, and revocation cannot be outrun (<bead>)`.

---

### Task 8: `session.read` from the helper, with targets; the catalogue equals dispatch

**Files:**

- Create: `internal/agenttools/session_access.go`, `internal/app/session_targets.go`,
  `internal/app/session_targets_test.go`
- Modify: `internal/agenttools/registry.go` (`session.read` row: resolver and narrow for
  descendants), `internal/assistant/dispatch.go` (`workerMethodNames` → `orchestrationMethodNames`
  gains `session.read`, `session.keys`, `session.message`), `internal/assistant/blocks.go`
  (a `sessionId` naming a descendant is read via `PaneReader`; the run's own pane keeps the
  renderer path — spec §11 "Kept, deliberately"), `internal/toolendpoint/catalogue.go`, `contracts/tools/session.read.schema.json`

**Interfaces:**

- Consumes: `DescendantPaneAccess.Resolve` (Task 7), helper `Snapshot`/`Target` (Task 5),
  `agentdriver.Registry.Observe` and `Observation.Menu/InputBox/MenuZone` (Task 1).
- Produces:
  ```go
  // internal/app/session_targets.go
  type TargetView struct {
      Token      string
      TokenID    string
      Kind       sessionruntime.TargetKind
      Rows       sessionruntime.RowRange
      Region     string                 // normalised text, §4.5
      Menu       *agentdriver.Menu      // Kind == menu
      ExpiresAt  time.Time
  }
  type PaneRead struct {
      Frame          paneview.Frame
      Classification agentdriver.State   // "none" for a non-agent pane
      Target         *TargetView
      Pending        []MessageView       // Task 10 fills; empty here
      DeliveryLost   *time.Time          // Task 10
      ReadBarrier    bool
  }
  type targetRecord struct { Access *DescendantPaneAccess; View TargetView; Enrolment workers.Liveness; Chain workers.Chain; AccessEpoch uint64; SessionID string }
  type PaneReader interface {
      Read(ctx context.Context, access *DescendantPaneAccess, sessionID string, want *sessionruntime.TargetKind, rows *sessionruntime.RowRange) (PaneRead, error)
      Record(tokenID string) (targetRecord, bool)
  }
  // snapshot → classify that snapshot's frame → choose rows (menu: Menu.Rows incl. selection;
  // input: InputBox with cursor; working: MenuZone; region: whole screen or requested rows) → mint
  // from the same snapshotId; snapshot_gone retries once.
  func newPaneReader(hub *paneAccessHub, helpers paneHelpers, rules *agentdriver.Registry) PaneReader
  // agenttools/session_access.go
  func narrowDescendants(grant content.Grant, _ []ResourceRef, runCtx RunContext) (Capability, error)
  ```
- Catalogue (spec §4.1): `buildCatalogue` takes the same bound capability path the dispatcher uses,
  so `tools.catalogue` lists exactly the methods `Dispatch` accepts for that caller.

**Acceptance Criteria:**

- `TestSessionRead_OverTheWireConformsToContract` via the real endpoint (extend
  `internal/toolendpoint/contract_test.go:155` table) and `…DTOConformsToContract`.
- `TestAReadClassifiesAndMintsFromOneSnapshot`: helper fake changes the screen between `Snapshot`
  and `Target`; the token's digest matches the snapshot the classification read (asserted by
  verifying the token against the retained snapshot), never the later screen.
- `TestAMenuWithoutABodyBoundaryGetsARegionTargetOnly`.
- `TestANonDescendantIsNotReachable` and `TestAPlainShellIsNotReachable` → `not_reachable`, each
  paired with a descendant read that succeeds.
- `TestTheCatalogueOffersExactlyWhatDispatchAccepts`: for the coordinator grant and for the kernel
  grant, `set(catalogue) == set(methods Dispatch does not refuse with ErrUnreachableMethod)`.
- Distinct outcomes each have a test: `completeness_unknown`, `frame_unavailable` (helper down),
  `classification: unknown`.
- `TestTheAssistantsOwnPaneStillReadsARunningBlockRegion`: the existing renderer-path tests in
  `internal/assistant` and `internal/transport/ws_readscreen_test.go` still pass unchanged, and a
  `session.read` naming a descendant never calls `RendererRequester.RequestScreen` (a fake that fails
  the test if called).

- [ ] **Step 1:** Tests FAIL → implement → `go test ./internal/app/ ./internal/assistant/ ./internal/toolendpoint/ ./internal/transport/ ./internal/agenttools/` PASS.
- [ ] **Step 2:** Commit `feat(session): read reads the helper and mints a target from the same snapshot (<bead>)`.

---

### Task 9: `session.keys` — one key, one text atom, or an option

**Files:**

- Create: `internal/app/session_keys.go`, `session_keys_test.go`,
  `internal/assistant/execute_session_keys.go`, `contracts/tools/session.keys.schema.json`
- Modify: `internal/agenttools/registry.go` (row `session.keys`, effect `send-input`,
  `Narrow: narrowDescendants`), `internal/assistant/execute.go` (executor map)

**Interfaces:**

- Consumes: `PaneReader` (Task 8), helper `Intent`/`IntentStatus` (Task 5), `paneAccessHub`
  (`admitting`, `noteCommitBy`, `StillHolds`) (Task 7), `monoclock.Now` (Task 5).
- Produces:
  ```go
  type KeyName string // closed vocabulary, spec §4.2; ParseKey rejects anything else
  func ParseKey(s string) (KeyName, error)
  type KeysRequest struct { SessionID string; TokenID string; Key *KeyName; Text *string; Option *string }
  type KeysResult struct { State string; BytesWritten int; Refusal *proto.IntentRefusal; Steps int }
  type PaneKeys interface { Send(ctx context.Context, access *DescendantPaneAccess, req KeysRequest) (KeysResult, error) }
  func newPaneKeys(reader PaneReader, hub *paneAccessHub, helpers paneHelpers) PaneKeys
  const optionSettle = 2 * time.Second
  const commitWindow = 5 * time.Second
  ```
- Per step: `targetRecord` lookup (a token under another access → `forged`), `hub.admitting`,
  `StillHolds(chain)`, `noteCommitBy(now+commitWindow)`, `session.intent`; a transport error →
  `IntentStatus`, and if unanswerable after `commitBy` → `indeterminate`.
- `option` loop (spec §6.4): step budget `len(options)+2`; after each `Up`/`Down` wait for a
  snapshot with `InputFence ≥ fenceAfter` whose selection moved by exactly one; unmoved, moved
  elsewhere or revisited → refused naming the menu seen.
- `text`: a newline or control byte with bracketed paste off → `would_submit` (decided in the helper
  at encode, surfaced here).
- The kernel adapter's approval: the existing gate per call; the approval covers the call's minted
  steps (spec §7.3). `agenttyping.Typist.Choose` and `ReadMenu` are deleted; `Typist.Submit`/`Type`
  stay until Task 11 confirms no caller.

**Acceptance Criteria:**

- `TestSessionKeys_OverTheWireConformsToContract` and DTO conformance.
- `TestAKeyUnderAChangedMenuWritesNothing` (real helper runtime in-process, mock agent program):
  zero bytes on the PTY, refusal `stale_target` with `regionNow` showing the new menu.
- `TestAnOptionIsChosenOnAMenuThatRepaintsLate`: mock menu delays repaint after each key; the loop
  never overshoots (the final selection equals the option; key count equals distance + 1).
- `TestAnOptionLoopRefusesOscillation`.
- `TestASequenceIsNotAccepted`: `keys: ["Down","Enter"]` is refused at schema validation.
- `TestATextAtomWithANewlineAndNoBracketedPasteIsRefused` paired with the same text under
  bracketed paste → executed.
- `TestALostResponseIsRecoveredByStatus` and `TestAnUnanswerableIntentIsIndeterminateNeverCancelled`.
- `TestASpentTokenIsRefused`: the same token sent twice with different keys → `token_spent`.
- `TestAnSSHPaneRefusesKeys` → `no_read_barrier`.

- [ ] **Step 1:** Tests FAIL → implement → `go test ./internal/app/ ./internal/assistant/ ./internal/agenttools/ ./internal/agenttyping/` PASS.
- [ ] **Step 2:** Commit `feat(session): keys are one step under a target, and an option is chosen step by step (<bead>)`.

---

### Task 10: `session.message` — queue, delivery, idempotency, cancel

**Files:**

- Create: `internal/app/pane_messages.go`, `pane_messages_test.go`,
  `internal/assistant/execute_session_message.go`, `contracts/tools/session.message.schema.json`
- Modify: `internal/agenttools/registry.go` (row), `internal/assistant/execute.go`,
  `internal/app/session_targets.go` (`PaneRead.Pending`, `DeliveryLost`), `internal/app/app.go`

**Interfaces:**

- Consumes: `PaneKeys`, `PaneReader`, `paneAccessHub`, `agentdriver` echo form, `workers.Liveness`,
  `session.Identity`.
- Produces:
  ```go
  type MessagePhase string
  const (
      PhaseQueued MessagePhase = "queued"; PhasePasting = "pasting"; PhaseAwaitingEcho = "awaiting_echo"
      PhaseEntering = "entering"; PhaseAwaitingSubmission = "awaiting_submission"
      PhaseRefused = "refused"; PhaseFailedPartial = "failed_partial"; PhaseDeliveryUnknown = "delivery_unknown"
      PhasePartial = "partial"; PhaseWritten = "written"; PhaseSubmitted = "submitted"
      PhaseCancelled = "cancelled"; PhaseIndeterminate = "indeterminate"
  )
  type MessageKey struct {
      Caller      string           // "endpoint" | "kernel"
      Controller  string           // session id
      Identity    session.Identity
      Authority   AuthorityInterval
      Participant workers.ParticipantID
      Liveness    workers.Liveness // compared with SameIncarnation
      Namespace   string           // "caller" | "nocx"
      ID          string
  }
  func PayloadHashV1(text, when string, targetKind sessionruntime.TargetKind) [32]byte // canonical JSON {"v":1,"text","when","targetKind"}, keys sorted
  type MessageView struct { ID string; Namespace string; Phase MessagePhase; BytesWritten int; BoxContents string }
  type CancelResult struct { Result string /* cancelled | too_late | no_such_message */; Phase MessagePhase }
  type PaneMessages interface {
      Send(ctx context.Context, access *DescendantPaneAccess, sessionID, text, when, id string, tokenID string) (MessageView, error)
      Cancel(ctx context.Context, access *DescendantPaneAccess, sessionID, id string) (CancelResult, error)
      Pending(sessionID string) []MessageView
      DeliveryLost(sessionID string) *time.Time
      EnqueueTask(ctx context.Context, participant workers.Participant, task string) error // namespace nocx, id "task"
  }
  func newPaneMessages(keys PaneKeys, reader PaneReader, hub *paneAccessHub, rules *agentdriver.Registry, startedAt time.Time) PaneMessages
  ```
- Delivery per spec §8.2; lock order per §8.5 (queue mutex → claim → release → store mutex resolve →
  release → helper call with no lock → queue mutex commit if generation matches); cancel linearises
  on the claim (§8.6) with exact response shapes.

**Acceptance Criteria:**

- `TestSessionMessage_OverTheWireConformsToContract` for send and cancel forms; DTO conformance.
- `TestAMessageDuringATurnIsSubmitted` and `TestAFreeMessageIsDeliveredWhenTheAgentIsFree` (mock
  agent program through the real helper runtime), each ending `submitted`.
- `TestTextInTheBoxRefusesThePaste`.
- `TestAnEchoThatNeverAppearsLeavesPartialAndNoEnter`: zero `\r` bytes on the PTY.
- `TestAMenuBetweenPasteAndEnterRefusesTheEnter` with the menu drawn outside the input rows but
  inside the menu zone.
- `TestAReusedIDWithADifferentPayloadIsRefused` and `TestARepeatedIDReturnsTheRecordedPhase`;
  `TestKeysDifferingOnlyInControllerIdentityOrAdmissionEpochOrParticipantIncarnationDoNotCollide`.
- Cancel: before claim → `{cancelled, cancelled}`; retry → same; after claim (test hook between
  claim and `session.intent`) → `{too_late, pasting}` and the paste is written; unknown id →
  `{no_such_message}` with no phase.
- `TestRevocationAfterPasteLeavesPartialAndTakesNoFurtherStep`.
- `TestACallerDisconnectDoesNotCancelADelivery`.
- `TestARestartReportsDeliveryStateLost` (new `PaneMessages` with a later `startedAt`).
- A watchdog at each §8.5 handoff proves no deadlock with a concurrent revocation (1000 iterations).

- [ ] **Step 1:** Tests FAIL → implement → `go test ./internal/app/ ./internal/assistant/ ./internal/agenttools/` PASS.
- [ ] **Step 2:** Commit `feat(session): a message is queued, pasted, echoed and submitted at most once (<bead>)`.

---

### Task 11: The owed task becomes a message; `workers.screen` and `workers.answer` go

**Files:**

- Modify: `internal/app/workers.go` (spawn delivery calls `PaneMessages.EnqueueTask` where it marked
  `owedTasks`; delete `owedTasks`, `workerScreener`, `workerAnswerer`, `withOwedTask`,
  `typeOwedTask`, `awaitMenuLeftScreen`, `awaitSelectionOn`, `awaitMenuSettled`, `menuSelectedOn`,
  `paneAnswerOf`), `internal/app/app.go:2192,2247,2259-2262`, `internal/workers/registrar.go`
  (`Screen`, `Answer`, `WithScreener`, `WithAnswerer`, `Screener`, `Answerer`, `PaneScreen`,
  `PaneAnswer`, `TaskOutcome` deleted), `internal/agenttools/registry.go:943-981`,
  `internal/assistant/dispatch.go`, `execute.go`, `execute_workers.go` (screen/answer executors and
  types), `internal/toolendpoint/contract_test.go:191-197`, `contracts/agent.approvalRequested.schema.json:30-67`,
  `internal/agenttyping/agenttyping.go` (delete what has no caller: check with `deadcode -whylive`)
- Delete: `contracts/tools/workers.screen.schema.json`, `workers.answer.schema.json`, their tests
  (`execute_workers_screen_test.go`, `execute_workers_answer_test.go`, `internal/app/worker_*answer*_test.go`)
  — each deleted test's user-level assertion is re-expressed in Tasks 9–10 first (rule 1); the
  commit body lists deleted test → replacing test.

**Interfaces:**

- Consumes: `PaneMessages.EnqueueTask` (Task 10).

**Acceptance Criteria:**

- `TestASpawnThatMeetsAQuestionDeliversTheTaskOnceTheQuestionIsAnswered`: mock agent shows a trust
  menu at spawn; the coordinator answers with `session.keys option`; the task is `submitted` exactly
  once; a second answer path (the person pressing Enter) also results in exactly one submission.
- `grep -rn 'workers.screen\|workers.answer\|owedTasks\|withOwedTask' --include=*.go --include=*.json --include=*.ts .`
  returns only the ADR and spec history.
- `deadcode` ratchet green (`make deadcode` or the command in `.githooks/pre-commit`).
- `go test ./internal/app/ ./internal/workers/ ./internal/assistant/ ./internal/toolendpoint/ ./internal/agenttyping/` PASS.

- [ ] **Step 1:** Write the spawn-question test (FAIL) → rewire → delete → PASS.
- [ ] **Step 2:** Commit `refactor(workers,session): the owed task is a message, and screen and answer are gone (<bead>)`.

---

### Task 12: The ADR and AD-6

**Files:** Create `docs/decisions/00NN-a-write-into-a-descendants-pane-is-one-step-under-a-helper-minted-target.md`
(next free number from `docs/decisions/INDEX.md`); modify `docs/decisions/INDEX.md` (new row;
ADR-0064 row `Accepted (§1 superseded by ADR-00NN)`; ADR-0029 row `Superseded by ADR-00NN`);
`docs/architecture.md:159-174` (power (1) restated per spec §2, citing ADR-00NN); citations of
ADR-0064 §1 elsewhere (`grep -rn 'ADR-0064' --include=*.go --include=*.md .`) moved to ADR-00NN.

**Acceptance Criteria:**

- The ADR has Context / Decision / Rationale / Consequences, supersedes ADR-0064 §1 and ADR-0029,
  extends ADR-0064 §2 to descendants, restates ADR-0064 §4's surviving prohibitions, and records
  the owner decisions (3), (4), (7), (9) with the date 2026-09-14.
- ADR-0029 and ADR-0064 files are byte-identical to `main` (`git diff origin/main -- docs/decisions/0029* docs/decisions/0064*` empty).
- `npx prettier --check` on the three files passes.

- [ ] **Step 1:** Write; commit `docs(decisions): a write into a descendant's pane is one step under a helper-minted target (<bead>)`.

---

### Task 13: Independent adversarial tests (a different author)

Rule 4: written from the spec by a worker who did not implement Tasks 2–7, against the interfaces
above, **before** Task 8 starts; they may fail until Task 10 lands and are run green in 13b.

**Files:** Create `internal/helper/session/owner_adversarial_test.go`,
`internal/helper/session/tokens_adversarial_test.go`, `internal/app/pane_access_adversarial_test.go`.

**Acceptance Criteria (13a — written, compiled, assertions in the bead):**

- Owner: drain-before-validate against output already readable; a reply storm with a blocked writer;
  resize racing commit; shutdown with tail; interleaving under 16 concurrent submitters; a writer
  returning short writes of every prefix length 1..len-1.
- Tokens: forged MAC, altered rows, altered kind, expired, other-incarnation key, token under another
  capability, concurrent duplicate consume, lost response, evicted snapshot, alt-screen switch,
  resize, style-only selection change, cursor-only move — each paired with an unchanged target that
  succeeds while a spinner runs outside the rows.
- Authority: revocation between check and commit (hook), grandchild intent racing grandparent
  revocation, grandchild spawn racing it, unanswering helper waited out by `commitBy`, neighbour and
  plain shell unreachable, the assistant's access cannot reach a coordinator's descendant and vice
  versa.

**Acceptance Criteria (13b):** all of the above pass on the merged branch after Task 10.

- [ ] 13a: write, `go vet` the packages, commit `test(helper,app): adversarial schedules for the owner, targets and revocation (<bead>)`.
- [ ] 13b: run `go test` for the three packages; any failure goes back to the implementing task's worker.

---

### Task 14: The end-to-end check and the live procedure

**Files:** Create `internal/app/session_surface_happypath_test.go` (production wiring: real
`cmd/nocx-server` coordinator stand as in `internal/app/worker_happypath_test.go`'s `newHappyStand`,
real helper runtime, real MCP bridge `internal/mcpstdio` and endpoint); modify
`.claude/skills/nocx-detection-verify/SKILL.md` and `internal/agentdriver/testdata/captures/scripts/`.

**Acceptance Criteria:**

- `TestACoordinatorReadsAnswersAndMessagesItsWorker` (spec §1): through the bridge, a coordinator
  reads a mock-agent worker with `session.read`, answers its permission menu with
  `session.keys option`, sends `session.message when=now` during a turn and `when=free` after it,
  while a `session.read` on another worker is in flight on the same connection; a key under a menu
  that changed writes zero bytes (read off the worker's PTY); a key after `workers.close` of that
  worker writes zero bytes.
- The skill gains: Claude's menu zone and echo form recorded, an option answered, messages during and
  after a turn, pasted text into a menu measured and the answer written into the spec's §15 as a
  measurement row (the one "to measure live" item of spec §8.2).
- The epic closes only when this test is green on the merged tree and `make ci-full` passes there.

- [ ] **Step 1:** Write the test (FAIL on the branch before Task 11 is merged, PASS after).
- [ ] **Step 2:** Update the skill; run it once outside CI against LM Studio; record the measurement.
- [ ] **Step 3:** Commit `test(app): an orchestrating agent reads, answers and messages its worker end to end (<bead>)`.

---

## Spec coverage

| Spec                                                                         | Task                            |
| ---------------------------------------------------------------------------- | ------------------------------- |
| §4.1 tools, catalogue = dispatch                                             | 8, 9, 10                        |
| §4.2 one step per target, key vocabulary, `would_submit`                     | 9                               |
| §5.1–5.6 owner, commit point, fences, reserve, resize                        | 2                               |
| §5.2 SSH no barrier; §5.7 shutdown, detach, taint, cap                       | 2, 3                            |
| §5.8 liveness                                                                | 2                               |
| §6.1 snapshots; §6.2 digest, token, slots, retention, compact result, status | 4, 5                            |
| §6.3 menu, input box, menu zone                                              | 1, 8                            |
| §6.4 option loop                                                             | 9                               |
| §6.5 wire                                                                    | 5                               |
| §7.1 bound capability; §7.2 generations, bump, `commitBy`, indeterminate     | 6, 7, 9                         |
| §7.3 per-call approval                                                       | 9, 10                           |
| §8 messages, phases, key, lock order, interval, cancel                       | 10                              |
| §9 owed task                                                                 | 11                              |
| §11 removals, ADR, AD-6 (renderer path kept, §11)                            | 8, 11, 12                       |
| §13 tests (independent author, happy path, live)                             | 13, 14                          |
| discovered `nocx-6q1uh.1`, `nocx-bm99e`                                      | 2, 6                            |
| §14 sibling epic                                                             | `nocx-3g262` — out of this plan |
