# Round 3

## Challenge 1

**The existing effects mechanism can carry the drain.** I recost it accordingly:
this is a small addition to an owned fork, not the creation of a fork or a new
libghostty subsystem. The manifest already pins `shady2k/ghostty`, its source,
toolchain, six archives and release route (`MANIFEST.json:4-25,31-121`); the
rebuild/re-pin path is already present (`Makefile:144-219`). One precision: the
manifest's `patch` is a one-line description, not a diff applied by the recipe;
the code lives in the fork commit (`third_party/libghostty-vt/README.md:48-63`).

There is no reentrancy blocker. Effects run synchronously during `vt_write`; only
recursive VT writes are forbidden, while short reads/copies are allowed
(`terminal.h:64-83`). Existing nocx callbacks already read terminal state and
copy borrowed values (`bridge.c:55-79,107-127`), and they execute on the goroutine
already holding `terminal.mu` without relocking (`terminal.go:99-121`). The new
callback must likewise do no I/O, fsync or journal transaction. It copies the
row and returns. Its ordering is parser/mutation order, just like `write_pty`;
nocx can stamp a local ordinal in every callback if cross-effect ordering is
needed. Persistence happens only after `vt_write` returns.

**Minimum viable drain: one callback per row.** Add one option and one callback
type to the fork. The callback receives a borrowed grid reference at the row's
first cell plus a closed reason enum (`scroll`, `resize`, `reset/discard`). It
fires after the primary row has its final contents and before the mutation makes
those contents unavailable. It never fires for alternate-screen rows.

The C bridge reads public row/cell facts during the callback and hands Go one
encoded/copied row, so this is one CGo callback per row, not per cell. Allocation
failure or a bounded staging overflow sets a gap flag; the callback never waits.
A batch callback would require libghostty to retain copies until the batch ends,
and a post-write drain API would require a library-owned queue, limits and
lifetime rules. Both are larger public surfaces than the borrowed per-row
effect. The existing 64 KiB runtime ingest bound (`sessionruntime/contract.go:
495-507`) bounds one write batch; latency still gets measured before the ABI is
pinned.

**Order changes:** yes. The fork patch and archive re-pin move to the first
stage, ahead of journal persistence and wire work. The pipeline is established,
and this effect is the primitive every later stage otherwise has to simulate.

## Challenge 2

I withdraw the claim about **all** resets. It was a product judgment stated as a
correctness invariant.

The narrow mechanical subset is: primary rows not yet emitted to the journal
become unreachable inside one libghostty mutation before nocx can observe them.
Examples are scroll followed by RIS or saved-line erase in one `vt_write`, an
in-call prune, and—if the product wants a pre-reset closing snapshot—active rows
printed and reset before a frame is published. Alternate-screen output is
excluded. A deliberate `clear`/RIS may instead be a semantic discard boundary;
calling that “data loss” before the owner chooses is wrong.

That subset is enumerable by auditing the fork's paths that move or destroy a
primary row, then pinning each with a callback-order test. It is not enumerable
from the current C headers alone, and its product frequency is unmeasured.

This changes the **justification, not the order**. The drain lands anyway. It
lets the owner choose preserve-versus-discard from an explicit reason rather
than having post-write observation silently make the choice. The reset policy
can be decided when journal boundary semantics are specified; it does not gate
the effect patch.

## The agreed position

1. Add a borrowed, synchronous primary-row effect to the existing ghostty fork.
2. Fire it before scroll, resize or reset/discard makes the final row unavailable.
3. Copy/encode in the C bridge; perform no persistence inside the callback.
4. Drain nocx's bounded staging after each write and surface overflow as a gap.
5. Keep HISTORY plus one tracked ref only for reconciliation and recovery.
6. Re-pin and publish the six archives through the existing manifest pipeline.
7. Build the 10,000-logical-row/8-MiB content journal; remove `session_output`.
8. Add protocol 16 journal delivery, then the frontend history-page data frame.
9. Make live scrollback and cards read that one journal.
10. First task: specify and test the fork effect on multi-scroll+RIS, resize, prune and alternate-screen cases, then implement and re-pin it.

## Anything still open

- The owner must choose whether explicit RIS/ED3/`clear` preserves pre-boundary
  rows or intentionally discards them. The callback reason supports either.
- The per-row callback/C-encoder latency and staging bound need measurement; a
  failed budget would justify batching later, not before evidence.
- `internal/emulator/ghostty/doc.go:38-40` still names the old
  `e2e53f861482...` pin while the manifest names `1f225ebb5894...`; the manifest
  is authoritative, but the documentation needs correction during the re-pin.

No architectural question remains open.

WORKER_DONE::sbk3-159e081d4429
