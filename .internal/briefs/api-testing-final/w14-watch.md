## Ground rules — read before anything

1. `pwd` first. Every path you create or edit is under this worktree. The spec and plan
   quote repo-relative paths; resolve them against YOUR root.
2. **Do not commit, push or branch.** The coordinator integrates. Leave work uncommitted.
3. **Do not touch beads / `bd`.** The coordinator owns the tracker.
4. **No repo-wide gates.** Another worker is mid-write in neighbouring files, so
   `go build ./...` or a full suite shows you THEIR half-finished work and you will
   escalate on a phantom. Verify only what your section names.
5. **No formatting runs** beyond the commands your section names.
6. **Do not edit files another worker owns** — listed in your section. Escalate instead.
7. Read `AGENTS.md` first. Binding, especially: a test asserts what a user can do; every
   external call has a test where it fails, paired with one where it succeeds; invariants
   are stated with BOTH ends; and `deadcode` can tell you a symbol is dead but never that a
   feature is wired.
8. TDD: the failing test first, run it, see it fail, then implement.
9. Numbers, not adjectives, in your report. Every suppression with its reason. Every
   problem you saw and deliberately left.

## The gates, in full — this list is complete, nothing else is expected of you

The previous wave was sent back because the brief omitted the linter. It is here now:

```bash
gofumpt -w <your packages>
go vet ./internal/<yours>/...          # type-checks _test.go; `go build` does not
golangci-lint run ./internal/<yours>/...
go test ./internal/<yours>/ -race -count=1
```

All four clean before you print your sentinel. `golangci-lint` runs `gosec` and `govet`
with `shadow`: a shadowed `err` in non-test code may be a real defect, so read both
declarations before renaming anything, and say in your report whether any was real. A
suppression is `//nolint:<linter> // <reason>` with the reason written out. **Never weaken
a test to satisfy a linter** — if a check and an assertion genuinely conflict, stop and say
so.

---

# Task: the panel notices the folder changed, instead of asking you to press a button

**Task id for your sentinel: `watch-5d21`**

**You own:** `frontend/src/api/**`, `frontend/src/styles/components/api-workbench.css`.
**Another worker owns, do not touch:** `e2e/**`, everything under `internal/` and `contracts/`.

Bead: `nocx-19rcp`. Owner feedback: _"зачем там обновить?"_ and then _"перенеси её наверх,
как в других панелях"_.

## Why the button is a symptom

A collection is a folder on disk that changes underneath us — a `git pull`, a neighbouring
editor, a colleague's branch. The panel answers that with a refresh button whose tooltip is
"Re-read the open folders".

**The product already answers this question, and differently.** `files.watch` exists, the
Files panel uses it, and its contract carries a `mode` field that exists — in its own words
— "so the UI can say why refresh may lag". So two panels answer "how does this surface learn
the folder changed" two different ways, which is the shape AGENTS.md names.

## Three parts, and the third is the one that is easy to skip

**1. Watch.** Watch the open collection roots through `files.watch`. Read that contract
first: the call **REPLACES** the watch set rather than adding to it, so closing a collection
must not leak a watch, and a newly-added watch that fails must not take the healthy ones
down. A change on disk re-lists the affected collection **with no user action**.

**2. Move the button.** It goes into the pane's header, mirroring `files-view.tsx:481`. Note
that the sidebar's `actions` slot (`sidebar.tsx:68`, "per-view header actions") is for
sidebar VIEWS — the workbench is a pane and has its own header, so mirror the placement, do
not reach for that slot.

**3. Take the degrade badge from Files, do not write a second one.** `files-view.tsx` has
it, with this reasoning beside it: _"polling on a LOCAL binding with a reason is a real
degrade — the persistent badge beside Refresh, hover carries the reason, cleared the instant
watching recovers."_ That is `mode` arriving in the product, and it is what stops a stale
tree looking current. If the badge is not already a kit component, **make it one** and have
Files use it too — one behaviour, one owner. If that turns out to be a bigger change than it
looks, stop and say so rather than copying the markup.

## Acceptance criteria

- A change on disk re-lists the affected collection with **nothing pressed**. Assert the new
  state is on screen.
- Closing a collection removes its watch; the assertion is on the set sent to `files.watch`,
  not on a count.
- A watch that fails to establish leaves the other collections still watched.
- A binding whose `mode` says refresh may lag shows the badge, with the reason reachable,
  and the badge clears when watching recovers.
- The refresh button is in the pane header.
- **The button survives only if watching genuinely cannot cover a case.** If you find such a
  case, name it in your report. If you do not, the button is still there for the person who
  wants to force a re-read — say which of those two you concluded and why.

## Verify

```bash
cd frontend
npm run typecheck
npm run lint
npm run test -- api
```

## When done

`REPORT-watch-5d21.md`: whether the badge became a kit component, what you concluded about
the button, exact commands and results.

Then print exactly, on its own line:

    WORKER_DONE::watch-5d21
