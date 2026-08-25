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

# Task: the collection panel, the icon, the name, and the separator

**Task id for your sentinel: `panel-2e60`**

**You own:** `frontend/src/api/**`, `frontend/src/ui/icons/**`, `frontend/src/ui/README.md`,
`frontend/src/sidebar.tsx`, `frontend/src/styles/**`, `frontend/src/main.tsx`.
**Another worker owns, do not touch:** `frontend/src/dialog-client.ts`,
`frontend/src/generated/**`, `contracts/**`, everything under `internal/`.

All four are owner feedback from a `make dev-web` run. Beads: `nocx-84shs`, `nocx-zccer`,
`nocx-5b3ab` (and `nocx-39jek` is the other worker's).

## 1. Creating a collection asks for a name AND a place (`nocx-84shs`)

Today `frontend/src/api/new-collection-dialog.tsx` asks for a name and silently decides the
location. The panel then stacks **New collection**, a **Collection folder** text field and an
**Open folder** button one under another as a bare form. The owner's reference is **Bruno**,
not Postman: creating a collection asks where it goes, with a default for people who do not
care.

- Follow the shape of `showWorkspaceCreateDialog` in `frontend/src/name-colour-dialog.tsx`.
  A person who has met that dialog has already learnt this one; a second pattern for "name
  this new thing" is the two-owners defect in miniature.
- The location field is **pre-filled with the default** under the app's collections
  directory, so Create works without touching it.
- A **Browse…** control appears **only when the backend offers the directory picker**. The
  other worker is adding `dialog.openDirectory`; call it through the client it exposes. When
  the method answers `-32601` — which is **always** under `make dev-web`, because that
  harness has no Wails — the control is absent and typing the path is the ordinary way, not
  a broken-looking fallback.
- **Opening an existing folder stops being a bare field in the panel.** Give it the same
  treatment: an action that asks, rather than a form the panel wears.
- The dialog **stays open** when the backend refuses, showing the reason — that behaviour
  already exists in the current dialog, keep it.

## 1b. Do not ask for a collection at all on a first run (`nocx-utrzp`)

The owner's second thought, and it is the better default: a person opening this pane for
the first time should have somewhere to type a request, not a form asking them to make one.

Postman removed its Scratch Pad and that is part of the complaint this whole feature
answers — there is now nowhere to try a request without an account. So we ship the
opposite.

**On the first open of the pane, when nothing is open and we have never seeded before,
create a collection in the default location and open it.** Then the empty state is not
"make one" but a collection ready to take a request.

Four rules, and the first is the one that keeps this from becoming a second concept:

- **It is an ORDINARY collection in every respect.** It can be renamed, committed,
  deleted, and opened from another machine. Nothing anywhere may branch on "is this the
  built-in one" — if you find yourself writing that condition, the design is wrong and you
  should stop and say so.
- **Seeded lazily, on first open of the pane** — never at install and never at app start.
  Somebody who never opens this pane gets no directory written on their disk.
- **Seeded once.** Record that it happened. If the person deletes it, it does not grow
  back: resurrecting something a person deleted is its own bug, and a worse one than an
  empty pane.
- The default location is the same one the create dialog pre-fills. One answer to "where do
  collections live", not two.

A test asserts: first open with nothing seeded leaves exactly one collection open and no
dialog on screen; second open creates nothing further; and a delete followed by a reopen
leaves it deleted.

## 2. The icon and the name (`nocx-zccer`)

`ArrowRightIcon` was picked with no recorded reason and reads as navigation.

- **Add a new icon to the kit**: two horizontal arrows pointing at each other — the
  request out, the response back. Not a single arrow, which is what it is replacing, and
  distinct from `ArrowRightIcon`, `ArrowUpIcon`, `ArrowDownIcon` beside it. It sits next to
  `FolderIcon`, `PlugIcon`, a git branch and a list at 16px, so it must read at that size.
  One file in `frontend/src/ui/icons/`, exported from its `index.ts`, and **a row in
  `frontend/src/ui/README.md`** — the kit grows by addition and the inventory is how the
  next person finds it.
- The surface is titled **API testing** — the pane, its activity-bar entry, and the tab
  strip label that follows from it.

## 3. The activity bar's separator (`nocx-5b3ab`)

A horizontal rule sits under the last view icon. **It is not from this epic** — it predates
the API work — and the owner has explicitly asked for it to go, so removing it is
deliberate rather than scope creep. Say in your report which rule you removed and where it
came from.

The two zones must still read as two: whatever the rule was doing has to be done by
spacing. `sidebar.tsx` already has a spacer that pushes the bottom zone down — check that
is enough before adding anything.

## Acceptance criteria

- Create asks name + location, pre-filled, and Create works without touching the location.
- With the picker capability absent, the dialog has no dead Browse control and typing works.
- With it present, Browse fills the field; cancelling leaves what was typed untouched.
- A refused name or path keeps the dialog open with the reason on screen — assert the text
  is rendered.
- The activity-bar entry and the pane both say **API testing**.
- The new icon renders and has its README row; a test asserts the entry uses it rather than
  `ArrowRightIcon`.
- The separator is gone and the bottom zone is still distinguishable — pin that.

## Verify

```bash
cd frontend
npm run typecheck
npm run lint          # includes the dead-exports ratchet: a new icon nothing imports is a NEW violation
npm run test
```

## When done

`REPORT-panel-2e60.md`: what you changed, which rule you removed and its origin, exact
commands and results.

Then print exactly, on its own line:

    WORKER_DONE::panel-2e60
