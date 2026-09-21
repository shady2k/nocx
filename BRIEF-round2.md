# Round 2: argue, then converge

Same rules as the first brief. Read and think; write no code, run no build, no
tests, no tracker, no branch, no commit. `pwd` first — you must be in
`/home/dev/.herdr/worktrees/nocx/review-scrollback-live-region`.

Your first answer was good and the owner has acted on it. This round is an
argument, not a new task. Push back where you think I am wrong; I have marked
where I think you were.

## What the owner decided

**Your fourth option is taken WHOLE, inside the xterm.js removal.** The durable
semantic journal is in scope, `session_output` is to be replaced rather than kept
as a fallback, and cards and live scrollback are to consume one source.

So stop costing whether the journal is worth doing. It is decided. What is open
is how it is built, and what it reads from.

## The headers you could not read are now in this checkout

`build/libghostty-vt/vendor/linux-amd64-gnu/include/ghostty/vt/` — the whole set,
including `point.h`, `render.h`, `grid_ref.h`, `grid_ref_tracked.h`, `screen.h`,
`snapshot.h`, `selection.h`, `search.h`. Gitignored build output, copied in for
you. It is the pinned set: ghostty `e2e53f861482e080bf45054ba49ef471f9849937`,
Zig 0.16.0, ReleaseFast.

Read them. Do not take my readings below on trust — I want them attacked.

## Four points. Answer each with agree / disagree and the evidence.

**1. Your Option 1 costing is wrong, and it matters because it may be the
journal's own read path.**

You costed a 10,000-row history at 20.1–50.4 MiB per session, from 16 or 40 bytes
per cell in Go. But that is the cost of _materializing_ history in Go. libghostty
already holds it and already addresses it: `point.h` has
`GHOSTTY_POINT_TAG_HISTORY` ("scrollback history only, before active area") and
`GHOSTTY_POINT_TAG_SCREEN` ("full screen including scrollback"), with `y` a
uint32 that "may exceed page size for screen/history tags". Our adapter is what
restricts us — `internal/emulator/ghostty/bridge.c:231` hardcodes
`GHOSTTY_POINT_TAG_ACTIVE`.

So: is the active-area-only rule a library limit or one line of ours? Recost
reading history on demand, holding nothing in Go.

**2. You said the scroll-off signal may need a callback or a fork. I think
`grid_ref_tracked.h` already is it.**

A tracked reference "moves with the terminal" and
`ghostty_tracked_grid_ref_to_point` converts it to a point in a requested
coordinate system. That looks like "pin a row, ask later where it is now —
active, or history".

Check it properly, including the failure modes I have not: what happens when the
tracked row is pruned out of history entirely; what `has_value` means after a
reset, a resize/reflow or an alt-screen switch; and what it costs to hold one
tracked ref per row of a screen.

**3. I think you framed reading and recording as competitors when they are
layers, and that this changes the journal's producer.**

Reading libghostty's `HISTORY` answers "what does a person see scrolling up".
The journal answers "history survives the runtime, and cards and scrollback have
one owner". Both are wanted; they are not the same decision.

The consequence I want you to settle: **does the journal's producer need a
scroll-off event at all?** If history grows monotonically except on prune and
reset, then "after each `Ingest`, append the history rows that appeared since the
last read" needs no event — only a stable count or cursor. Does the library give
one? If it does, your sharp point about one `Ingest` scrolling several rows is
answered without a callback. If it does not, say so and my position falls.

**4. What you flagged as unverified is now on the critical path, so resolve it.**

You wrote that whether the frozen session channel supports a host-initiated page
response was not verified, and that if it does not, the recommendation is blocked
on an explicitly versioned ABI decision rather than smuggling fields into
`ScreenFrame`. The owner took the recommendation whole, so this is no longer a
caveat to record — it is the first thing that can stop the work.

Read `internal/helper/proto/` — `frame.go`, `channel_frame.go`, `session_frame.go`,
`screen.go` — and `internal/helper/host/`. Say which it is, with the file and the
mechanism, and if a versioned ABI change is needed, say exactly what shape.

## One more reading to confirm or refute

`render.h` gives DIRTY rows, not EVICTED rows, so it does not answer scroll-off.
But it does look like the answer to per-frame read cost, which today is 5–6 CGo
crossings per cell (`terminal.go`'s `readCell`): `ROW_DATA_CELLS` populates a
pre-allocated row in one call; `ROW_CELLS_DATA_HAS_STYLING` avoids materializing
style for unstyled cells; `GRAPHEMES_UTF8` avoids our codepoint loop;
`ROW_DATA_SELECTION` gives a row-local selected range instead of one call per
cell. Note upstream's own advice that `CELLS_RAW` is for embedders with expensive
call boundaries and that callers with the C header should not use it — its bit
positions are not ABI-protected.

Confirm or refute, and say whether any of it changes the journal's design.

## Deliverable

Not another essay. Write to, and only to:

`/home/dev/.herdr/worktrees/nocx/review-scrollback-live-region/ANSWER-round2.md`

Structure it as:

1. **Point by point** — for each of the four, and the render.h reading: agree or
   disagree, the evidence (file and line), and what changes as a result.
2. **The converged design** — the journal as you would now build it: what
   produces a row, what is stored, what bounds it, what the client asks for, and
   which contract carries it. Concrete enough to be decomposed into tasks.
3. **Where we still disagree** — explicitly. Silence will be read as agreement,
   and agreement I have not earned is worse to me than a stated disagreement.
4. **What is still unverified**, same as last time.

Final line, exactly:

`WORKER_DONE::sbk2-3c7dc0063a01`

Or, if you cannot finish, exactly:

`WORKER_BLOCKED::sbk2-3c7dc0063a01 <one line why>`
