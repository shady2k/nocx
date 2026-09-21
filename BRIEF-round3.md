# Round 3: two concessions, two challenges, then we are done

Same rules. Read and think; no code, no build, no tests, no tracker, no branch,
no commit. `pwd` first.

## I concede three of your four

1. **A tracked ref is an anchor, not a scroll-off signal.** Your counterexample
   settles it: a ref reports movement or loss and cannot return the contents it
   lost, and nothing can pin a row created and evicted inside one library call.
2. **HISTORY does not grow monotonically in the sense a journal needs**, so
   count-plus-cursor after `Ingest` is a fast path and a reconciliation check,
   not a producer. My position fell exactly where I said it would.
3. **`ROW_DATA_CELLS` initialises a reusable iterator, not a filled row.** I
   read the doc comment and not the call surface. `CELLS_RAW` is the only true
   single-call row view and upstream tells C-header callers not to use it.

I also confirmed independently that libghostty publishes **no** eviction effect:
`terminal.h:85-102` lists fourteen effects and every one is a VT-sequence event
— write_pty, bell, title, pwd, enquiry, xtversion, size, color scheme, device
attributes, clipboard read/write, desktop notification, progress report, unknown
sequence. Nothing about rows leaving the active area. Your drain is needed.

So the design is agreed except for its cost and its order, which is what this
round is about.

## Challenge 1: you priced the drain as a library fork. The fork already exists.

Read `third_party/libghostty-vt/MANIFEST.json`. The pinned upstream is
**`https://github.com/shady2k/ghostty`** — this project's own fork — at commit
`1f225ebb5894189aa9e12cf3a371f787e574c5f4`, with `baseCommit` equal to it and a
`"patch"` field that is currently the empty string. The manifest also carries the
build command, the toolchain pin (Zig 0.16.0, LLVM 21.1.8), the archive and
header paths, and a GitHub release asset template under the same account. The
Makefile has `vt-archives`, `vt-recipe-audit` and `vt-recipe-pin`, and
`internal/emulator/ghostty/doc.go` describes the whole fetch-and-verify route.

So patching the pinned library is not "fork libghostty and maintain it". It is
filling in a field that already exists, in a fork that is already ours, through
a publish pipeline that already runs.

**And I think the minimum patch is much smaller than a new subsystem.**
`terminal.h:78` says every effect callback is invoked **synchronously during VT
writes**. The library already has that mechanism and fourteen users of it. A
fifteenth effect — fired at the moment a row leaves the primary active area,
handing the embedder that row while it still exists — is idiomatic to the
library's own architecture, not an addition beside it.

Answer three things:

- Does the existing effects mechanism carry the drain, or is there a reason it
  cannot — reentrancy, allocation inside a callback, ordering against
  `write_pty`, the "must not block" rule at `terminal.h:82`?
- **Specify the minimum viable drain**: the smallest public surface that makes
  the journal complete. A callback per row, a callback per batch, or a queue the
  embedder drains after the write — and why that one.
- Does the fork already existing change your staging order?

## Challenge 2: your one product claim, which is the owner's and not ours

You wrote "A normal RIS/reset must not create a data-loss gap in an otherwise
complete local session" and "I do not accept that product behaviour." That is a
product judgement, and it is the last thing between us.

Test it against its own cases:

- An alternate-screen interval contributes no line scrollback at all — your
  point, and I agree. So a TUI exiting is not this case.
- A user typing `clear` deliberately discards. Calling that data loss is
  arguably a false statement in the product, not a protection.
- What is left is a program that prints and then resets inside one `vt_write`.
  Nobody has measured how often that destroys rows a person wanted.

So: is your objection to **all** resets, or only to that last subset? Can the
subset be enumerated? And say plainly whether this changes the **order** of
work or only its justification — if the drain lands anyway, this is moot, and I
would rather have that said than have it quietly decide the staging.

## Deliverable

Short. Write to, and only to:

`/home/dev/.herdr/worktrees/nocx/review-scrollback-live-region/ANSWER-round3.md`

1. **Challenge 1** — the three questions, with evidence.
2. **Challenge 2** — the answer, and its effect on order.
3. **The agreed position**, in under twenty lines: what we build, in what order,
   and what the first task is. This is what goes to the owner.
4. **Anything still open**, and say explicitly if nothing is.

Final line, exactly:

`WORKER_DONE::sbk3-159e081d4429`

Or exactly:

`WORKER_BLOCKED::sbk3-159e081d4429 <one line why>`
