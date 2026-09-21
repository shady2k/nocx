# Brief: the live-region scrollback fork

You are a consulting reviewer. **Read and think. Write no code, run no build, run no
tests, touch no tracker, create no branch, commit nothing.** Your whole deliverable is
one Markdown file.

First: `pwd`. You must be in
`/home/dev/.herdr/worktrees/nocx/review-scrollback-live-region`. Everything you read and
write is inside that path. Do not read or write in any other checkout of this repo.

## The question

nocx is removing xterm.js. The backend holds the terminal screen; the client receives
cells and paints them. One question has no answer yet, and it is the same question seen
from two sides:

1. **What does a person see when they scroll the LIVE region upward** — not into a
   finished command's card, but into the terminal's own scrollback?
2. **What happens to `internal/content/session_output.go`** — the durable byte sink —
   once xterm.js is gone and nothing in the client can turn bytes into a screen?

These are one fork because the only thing that can answer (1) from stored data is
something that can do (2).

## What is already decided and may NOT be re-proposed

Read the records rather than trusting this summary, but do not spend the session
re-deriving them.

- `docs/decisions/0066-one-emulator-and-it-is-the-backends.md` — ONE VT emulator in the
  system, in the session runtime beside the PTY. The client paints cells and sends
  intent. A second emulator beside the runtime is refused.
- The client gets **no VT parser**. Keeping xterm.js as a painter was verified impossible
  and is out.
- `cmd/nocx-server` (the coordinator) is built `CGO_ENABLED=0` and **cannot link an
  emulator**. Only the helper links libghostty-vt.
- `internal/emulator/emulator.go` — the port reads the **active area only**. Its own
  words: "scrollback is a different reading with a different retention question, and this
  port neither scrolls a viewport nor reads one."
- `internal/helper/proto/screen.go` — `OpReplay` already feeds a capture's bytes to a
  PTY-less emulator and answers the screen after each mark. It exists for calibration.
- The helper ABI is frozen. Bounds: `internal/helper/proto.MaxFrameBytes` = 1 MiB,
  `internal/lifecycle.MaxFrameBytes` = 256 KiB.
- `internal/transport/ring.go` — the 256 KB replay ring is NOT scrollback; it is a
  reconnect buffer, and it blocks its writer when full.
- Finished commands become **interval records ("cards")**: text, produced by the backend,
  reflowed per client. They are not the subject of this brief except where they bound it.
- The session frame (`contracts/session.frame.schema.json`) carries the **active screen
  only**. You MAY challenge this, but if you do, say so explicitly and pay for it in your
  costing.
- nocx is greenfield: **no backward-compatibility shims, no dead code, no quick wins.**
- AD-8 / "look for the existing answer": one owner per behaviour. A second implementation
  of one concept is a defect, not cleanup for later.

## What to read

Start here; follow what these cite rather than searching broadly.

- `.internal/specs/2026-09-12-the-backend-holds-the-session-screen-design.md` — §1, §5,
  §6.3, §6.7, §6.11, §7.1
- `docs/decisions/0066-one-emulator-and-it-is-the-backends.md`
- `docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md`
- `internal/emulator/emulator.go`, `internal/emulator/doc.go`
- `internal/emulator/ghostty/terminal.go` (the read path), `internal/emulator/ghostty/doc.go`
- `internal/sessionruntime/contract.go` (`Snapshot`, `Completeness`), `digest.go`
  (`ScreenIdentity`)
- `internal/helper/proto/screen.go`, `internal/content/session_output.go`
- `internal/transport/ring.go`
- `contracts/session.frame.schema.json`
- `frontend/src/scrollback/` — read `controller.ts`'s header and `serializer.ts`'s shape;
  do not read all 18k lines.

If you have the vendored header, `build/libghostty-vt/vendor/*/include/ghostty/vt/` holds
`render.h`, `screen.h`, `snapshot.h`, `selection.h`, `search.h`. It is gitignored build
output and may be absent in this checkout; if so, say so rather than guessing at its
contents.

## The three options already on the table

Do not simply rank these. They are given so you do not spend the session rediscovering
them, and so that a fourth option is recognisable as new.

1. **The emulator port learns to read scrollback.** Brings an immediate retention
   question: how many rows, who pays that memory per session, what happens to them on
   resize.
2. **The live region does not scroll at all.** Everything that left the screen is
   reachable only as cards. Requires cards to cover output that was never a command — a
   TUI killed mid-run, output before the first prompt.
3. **Scrolling reads the durable byte sink through the helper's replay path.** The sink
   is already written and already durable, but it is bytes with no geometry, and turning
   bytes into a screen needs an emulator, which only the helper has.

## What we want from you

1. **Name the option you would take, and why.** One recommendation, not a survey.
2. **At least one option we have not framed.** If the honest answer is that there is no
   fourth, say that and say what makes the space closed.
3. For every option you discuss, state all four:
   - the mechanism, in this codebase's own names and files;
   - its cost in **numbers** — bytes or rows per session, calls per frame, what grows
     with what. Say when a number is an estimate and how you got it.
   - **what it does not fix**;
   - which invariants, ADRs or frozen contracts it touches, by number.
4. **Name the assumption every option here rests on, and give one option that rejects
   it.** We would rather find out now that the frame is the wrong question.
5. Call out anything you could not verify. Silence will be read as "nothing to report".

Be concrete and adversarial. Disagreeing with a decision above is useful when you name
the record, say what changed, and cost the reversal. Vague agreement is not.

## Deliverable

Write your answer to, and only to:

`/home/dev/.herdr/worktrees/nocx/review-scrollback-live-region/ANSWER-scrollback-fork.md`

Then print, as your final line, exactly:

`WORKER_DONE::sbk-27cc5563b7d9`

If you cannot finish, print instead exactly:

`WORKER_BLOCKED::sbk-27cc5563b7d9 <one line why>`
