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

# Task: our own folder browser, so choosing a folder never depends on a native dialog

**Task id for your sentinel: `browser-8f12`**

**You own:** `frontend/src/ui/folder-picker.tsx` (new) and its test, and **one row** in
`frontend/src/ui/README.md`.
**Others own, do not touch:** `frontend/src/api/**`, `frontend/src/sidebar.tsx`,
`frontend/src/main.tsx`, `frontend/src/dialog-client.ts`, `frontend/src/generated/**`,
`contracts/**`, everything under `internal/`. Another worker is editing `ui/README.md` too —
add your row and change nothing else in that file.

## Why this exists

`dialog.openFile` and the `dialog.openDirectory` another worker is adding are **native**
dialogs reached through Wails. The dev-web harness has no Wails at all, and a future
backend served over the network would have none either. So a product whose only way to
choose a folder is native has no way to choose a folder in two of the three configurations
it runs in.

Orca solves the same problem the same way: a native dialog for the desktop case
(`dialog.showOpenDialog` with `openDirectory`), and its own routed tree
(`useFileExplorerTree` over a runtime file client) for everything else. We already have the
routed half — `files.list`, which the Files panel uses and which is bound to a filesystem
provider by a `bindingId` the backend mints.

## What to build

A **kit component**: a dialog that browses directories and returns the chosen absolute
path, or null when cancelled.

```tsx
export interface FolderPickerProps {
  /** Where to start. */
  initialPath: string
  /** One page of one directory. The caller supplies this — the component
   *  knows nothing about JSON-RPC, bindings or which machine it is reading. */
  list: (path: string) => Promise<{ path: string; entries: FolderEntry[] }>
  onResolve: (chosen: string | null) => void
}
export interface FolderEntry {
  name: string
  isDirectory: boolean
}
export function showFolderPicker(props: FolderPickerProps): Promise<string | null>
```

**The component supports every filesystem nocx can reach, and that is deliberate.** It is a
general dialog, not part of collections. The CALLER decides which machine to look at, and
for a collection the caller passes the backend's own filesystem — not because a remote one
is hard, but because of availability: a collection that lives on a remote host stops
opening the moment that host is unreachable, and a collection must open always. That is a
property we are choosing, not a limitation waiting to be lifted.

**The `list` callback is the whole design.** The component does not import a client, does
not know about `bindingId`, and cannot tell a local filesystem from a remote one. That is
what makes it testable without a backend and what keeps the routing decision with the
caller, where it belongs.

Follow `frontend/src/ui/dialog.tsx` for the shell and `frontend/src/name-colour-dialog.tsx`
for the imperative `show…` wrapper — a person who has met those has already learnt this.

## Behaviour

- Only directories are selectable. Files may be shown greyed or hidden — pick one and say
  which in your report — but a file can never be the answer.
- Going up from the current directory, and typing a path directly, both work. **Typing must
  stay possible**: it is the path that survives when a listing fails.
- A directory that cannot be listed shows the reason **in place** and leaves the rest of the
  dialog usable. It does not close, and it does not empty the tree.
- Cancel resolves `null` and changes nothing.
- The component never resorts entries. `files.list`'s contract says ordering is
  backend-owned and deterministic, and re-sorting in the renderer would be a second owner
  of the order.

## Acceptance criteria

- Choosing a directory resolves its absolute path.
- A file cannot be chosen — assert that selecting one does not resolve.
- A failing `list` shows its reason and the dialog stays usable — assert the text is
  rendered, and that a subsequent successful `list` recovers.
- Cancel resolves null.
- Typing a path and confirming resolves that path even if `list` never succeeded.
- The component is tested with a `list` stub only — **no client, no dispatcher, no
  backend**. If your test needs either, the seam is in the wrong place.
- A row in `frontend/src/ui/README.md`.

## Verify

```bash
cd frontend
npm run typecheck
npm run lint
npm run test -- folder-picker
```

## When done

`REPORT-browser-8f12.md`: the files-vs-hidden choice and why, exact commands and results.

Then print exactly, on its own line:

    WORKER_DONE::browser-8f12
