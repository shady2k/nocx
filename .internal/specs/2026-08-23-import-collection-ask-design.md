# The import ask: one gesture, and a place already answered

Date: 2026-08-23 · Bead: nocx-cx442 (brainstorm) · Branch: `feat/api-testing`

## What a person can do that they cannot today

Drag a Postman export from their file manager onto the import dialog and press
Import. Today they must find the export, copy its absolute path, paste it into
one field, then invent an absolute path for a folder in a second field — for a
document they had just downloaded.

## Why this, and what it is answering

The owner's reference is Postman's import dialog. What is worth taking from it
is not the chrome but the **question count**: Postman asks exactly one thing —
where the document comes from — and answers the rest itself. It offers four ways
to answer that one question (paste, drop, file picker, folder picker) and has no
submit button at all, because naming the source _is_ the answer.

nocx asks two questions, both as absolute paths, and answers nothing. That is
the asymmetry `nocx-6hg2w.14` already named and half-closed: `api.collections.create`
next door takes a NAME and puts the folder where nocx keeps collections, while
the import — the door a person arriving from Postman meets first — asks more.

`nocx-6hg2w.14` put `defaultRoot` on the wire (`api.collections.list`) and made
`proposedDestination` (`frontend/src/api/api-paths.ts:54`) offer
`<defaultRoot>/<stem>`. What it did not do is offer anything **before** a file is
chosen: on open both fields are empty and the placeholders say `/work/acme-api`,
which names an arbitrary path rather than ours.

Two boundary documents this crosses, and what they already decided:

- **AD-8 / "look for the existing answer before you write a second one".** The
  window drop already exists (`nocx-9le.5`, `nocx-hbdw4`, merged into this branch
  from `main`). It is not re-implemented here; it is addressed.
- **design §5.5 / R2 / D9** — what a drop may tell the renderer. This spec adds
  no new class of thing the renderer learns: it uses the branch that already
  exists for a local tab.

## What was checked before this was written

`SourceTicketStore.Dropped` (`internal/transport/ws_upload_source.go:445`) reads
`data-session-id` off the drop target, asks `TabKind(sessionID)`, and for
`session.KindLocal` calls `describeSource(name)` — **it mints no ticket** and the
pick carries `LocalPath`, the absolute path
(`ws_upload_source.go:344`). That is exactly what `api.import.postman(srcPath, dest)`
consumes.

**So the drop needs no new mint site, no new addressing and no new credential.**
An earlier reading of this design claimed it did; that claim was wrong and is
recorded here so it is not re-derived.

Three things it _does_ need are below, each a consequence of reading the code
rather than a preference.

## §1 — One gesture, and the two capabilities it needs

The dialog's BODY is the drop target — the region holding the two fields, not the
`<dialog>` element itself. `ui/dialog.tsx` does not forward arbitrary `data-*`
onto its element (`DialogProps`, `dialog.tsx:51`), and reaching past the kit to
set them there would be the repaint rule in another form. Same gesture for the
person; the kit keeps its boundary.

**The affordance is PERMANENT, and the first version of this spec got that
wrong.** It said the region should appear on `dragover` only, reasoning that a
permanent dashed box in a dialog already carrying two fields and a footer is a
third thing competing for the same 480px column.

That reasoning was about the layout and forgot the person. The owner opened the
finished ask in the real Wails window and said the import had not changed at all
(2026-08-23) — the destination prefill was on screen and everything else looked
identical, because the one genuinely new capability said nothing about itself
until you were already doing it. A gesture nobody can discover is a gesture
nobody performs, and the dashed box costing 60px is a cheaper loss than the
feature costing all of itself.

It is also the half of Postman this spec had failed to copy while claiming
Postman as its reference. Postman's dialog says **"Drop anywhere to import / Or
select files or folders"** at rest, permanently, with an icon — the words are
there before the drag, not during it. This spec took its question COUNT and left
its most visible sentence behind.

A drop does **both** halves of the ask in one gesture: it writes the export's
path into the source field and proposes the destination, exactly as the file
picker's answer does today (`chooseExport`, `frontend/src/api/api-pane.tsx:440`).

**The field and the picker stay.** `make dev-web` has no Wails runtime, a browser
drop delivers `File` objects with no location, and `api.import.postman` takes a
path — so in the dev stand the drop cannot answer and typing is how it is done.
This follows `CollectionDialog`'s established rule verbatim: a capability that is
absent is not drawn, so it never looks like a fallback from something broken.

**Which makes the capability test two conditions, not one:** a Wails runtime
(`hasWailsWebview()`, `frontend/src/wails-runtime.ts:58`) **and** a live local
session. The second alone is not enough and the difference is not theoretical —
the dev stand and the e2e harness both have real local sessions and no Wails, so
a target gated on the session alone would light up under a drag and then deliver
nothing. A drop surface that highlights and does nothing is worse than no drop
surface, because it has already promised.

**Multiple files are refused with a sentence.** The import makes one collection;
N collections is N destinations, which is a different question and not this one.

**A folder needs no new refusal** — `describeSource` already returns
`errSourceNotRegular`, and `Dropped` reports "none of the N dropped files could
be read".

### The session the drop target names

`Dropped` requires `data-session-id` to name a session the registry says is
**open**. The workbench is handed `localSession: () => localSessionId()`
(`frontend/src/main.tsx:567`), and that signal is **latched**: it holds the id of
the first local session ever seen and is never cleared
(`main.tsx:442`, with the comment saying so). A latched id survives its tab.

So a drop into the dialog would fail with `errDropTabNotOpen` naming a tab the
person had closed and was not thinking about.

**The dialog therefore names a LIVE local session**, and when there is none the
drop target is not drawn at all. The latch is right for the question it was
written for ("is there a local session here", read once to decide whether to draw
a capability); it is wrong for "which open session does this gesture belong to",
which is read at drop time.

This is a symptom, and the spec says so rather than hiding it: the import is not
a session gesture at all — it reads a document on the machine the backend runs on
— and borrowing a terminal tab's identity to express that is an addressing scheme
standing in for the one this gesture actually wants. **A session-free drop target
is filed as its own bead**, because `EmitFilesDropped` addresses by session
deliberately (broadcasting was the defect it replaced) and giving a non-session
target an address is a real design question, not a line of code.

### §1a — The import needs the document, not the machine the person is on

**Corrected 2026-08-23, on the owner's reading, and it invalidates two decisions
above.** The rest of §1 was written from the terminal domain's frame — local tab
versus remote tab, a Wails runtime versus none — and that frame does not apply
here at all.

An import writes to **the backend's** disk: `apicoll.DefaultRoot()` is
`paths.DataDir()` of the process running Go, and `api-client.ts` already calls a
collection folder "backend-LOCAL". Where that process runs is not the renderer's
business, and it is not always the renderer's machine — `make dev-web` is
documented in the Makefile as "forward both ports over SSH", so a backend on
another host is a supported configuration, not an exotic one.

Two consequences, both of which contradict what §1 says above:

**`srcPath` names a file on the BACKEND'S machine.** In the desktop app that is
also the person's machine, which is why the field reads naturally and why the
substitution went unnoticed. Reached over a forwarded port, typing
`/work/acme.postman_collection.json` names a file on the _server_ — almost never
what a person means when they have just downloaded an export to their laptop.
So the path is the NARROW case, correct only when backend and person coincide.

**The document itself is the general case.** The renderer has bytes — from a
browser drop (`dataTransfer.files`, which `terminal-drop.ts` already handles as
its non-Wails half) or from the kit's own `ui/file-input.tsx` — and bytes reach
any backend, wherever it runs.

So the gate is not `hasWailsWebview()` and the drop is not a session gesture.
`apiimport.ImportInto(ctx, fsys, bindings, dest, r)` already takes a READER
rather than a path; `capability.ImportPostman` only opens the file first. The
document route is that seam, and `maxAPICurlLineRunes` (1 MiB, `api.import.curl`
next door) is the precedent for bounding a text parameter the control plane
carries.

**What this retires:** the `hasWailsWebview()` capability gate, and the import
ask's borrowing of a local terminal tab's session id. `nocx-ikte5` was filed as
a deferred addressing question; under this frame it is simply the bug.

**What survives:** in the Wails window the native drop answers with `localPath`,
a path on the machine running Go — which IS the backend there — so that route
stays correct and stays used. Two routes into one method, chosen by what the
gesture can answer with, never by what kind of build this is.

## §2 — The place is proposed, never asked twice

**On open the destination field already holds the backend's `defaultRoot` with a
trailing separator** — `/data/collections/` — so the caret sits where the name
goes. Choosing a source, by picker or by drop, replaces the whole value with
`<defaultRoot>/<stem>`.

**The root alone is not a valid answer, and Import says so by staying disabled.**
`ready()` already refuses a blank because a call that could only be refused is a
round trip spent to learn what the form knew; the prefill introduces a second
value of exactly that kind — `/data/collections` and `/data/collections/` both
name a folder that certainly exists, so both would come back "a folder is already
there" about the collections root rather than about anything the person chose. So
`ready()` treats a destination equal to the root, with or without its separator,
the way it treats a blank.

`nocx-6hg2w.14`'s rule is kept intact and NOT re-litigated: **a destination the
person has edited is never overwritten by a later pick.** The prefill is written
through the surface's own signal rather than through `onDest`, so it does not set
`destTyped` and does not count as an edit.

**The path is shown absolute, as the backend gave it — no `~` abbreviation.** An
abbreviated display would make the field's text unequal to the value sent, which
is a second owner for one path. The field a person sees is the truth.

**The destination's `/work/acme-api` placeholder goes, unconditionally.** A
placeholder is what an empty field says, and a placeholder contradicting the
value beside it is a lie in the UI. The source field keeps its placeholder — that
field is still empty on open.

`defaultRoot` is `""` on a build with no app directory (`ErrNoDefaultLocation`).
That state is unchanged by this spec: nothing is prefilled, nothing is proposed,
and the field is empty with the description alone saying what it wants — an offer
nobody can act on is worse than an empty field (`api-paths.ts` states this rule
for the same three cases).

## §3 — One gesture, one owner

`files.dropped` is addressed by **session**, and `terminal-drop.ts:358` filters on
exactly `if (p.sessionId !== origin()?.sessionId) return`. A dialog carrying a
local tab's session id therefore receives the drop **together with that tab's
pane**, which inserts the path at the prompt (D9).

That is two surfaces owning one input, and whichever wins does so by evaluation
order — the defect AGENTS.md names, with the `ssh`-context precedent.

**The fix reuses the attribute that is already there rather than adding one.**
`data-file-drop-target` is set empty today (`terminal-content.ts:3735`) and Wails
hands Go **every** attribute of the target element. Let it carry a NAME:

- the terminal pane sets `data-file-drop-target="terminal"`
- the import dialog sets `data-file-drop-target="api-import"`
- `Dropped` reads it beside `data-session-id` and puts it in the notification
- each subscriber filters on its own name

Cost: one field in `contracts/files.dropped.schema.json`, one line in `Dropped`,
one condition in `terminal-drop.ts`. Greenfield — both sides set a name, and the
empty value is not kept as a legacy case.

A drop whose target names nothing is refused the way a drop with no session
already is, rather than delivered to everybody.

## §4 — The kit

`frontend/src/ui/` has no drop zone (`file-input.tsx` is a picker). `terminal-drop.ts`
hand-rolls its own dragover/dragleave/drop with a `data-drop-active` dataset. A
second hand-rolled one in the dialog is the two-vocabularies defect the kit rules
exist to stop.

So: **`ui/drop-zone.tsx`**, one CSS file in `styles/components/drop-zone.css`, the
identity class `ui-drop-zone`, state as `data-drop-active`, a test, and a row in
`frontend/src/ui/README.md`.

The terminal pane's visual half moves onto it **as its own bead**, not silently
and not in this change — it is entangled with the pane's lifecycle and the DOM
half of the browser drop.

## §5 — Acceptance criteria, as assertions

**The e2e suite cannot watch the native drop, and this spec says so rather than
claiming a check it does not have.** `e2e/drop-gesture.ts` performs a BROWSER
drop — it builds a `DataTransfer` and dispatches `DragEvent`s
(`drop-gesture.ts:35`) — because the harness runs `cmd/devharness` plus vite and
has no Wails. The native drop is not a DOM event at all: Wails hands Go the
absolute paths directly, and `SourceTicketStore.Dropped` is not reachable over
JSON-RPC by design (R2 — the wire may never mint a source). So no Playwright
gesture can produce one.

What covers the happy path instead is **three checks meeting at the contract**,
which is what rule 5 says the contract is for:

1. Go: `Dropped` on a local tab with `target: "api-import"` emits `files.dropped`
   carrying the absolute path and that target.
2. `..._OverTheWireConformsToContract` for `files.dropped` — the real
   notification off the real socket, against the schema.
3. Frontend: that notification fills both fields, and pressing Import calls
   `api.import.postman` with them.

The gap is named and filed rather than left implied: **no automated check watches
a human perform the native gesture**, and `nocx-9le.5.23` ("the local-tab drop has
no end-to-end check, and it is the half that broke twice") is the precedent for
taking that seriously. A bead carries it.

What the e2e CAN assert, and does:

- `e2e`: with no Wails runtime the ask draws **no** drop target, and typing the
  export's path plus pressing Import still puts the collection in the tree — the
  dev-stand path, which is also every contributor's path.
- `e2e`: the ask opens with `<defaultRoot>/` already in the destination and Import
  disabled until it grows a last segment.

And §3's assertion — the terminal not acting on the ask's drop — is a frontend
test driving `files.dropped` directly, because that is the only place the
notification can be produced outside the Wails window.

- Frontend: opening the dialog with `defaultRoot: "/data/collections"` renders
  `/data/collections/` in `#api-import-postman-dest`, and Import is DISABLED
  until that value grows a last segment.
- Frontend: opening it with `defaultRoot: ""` renders an empty destination and no
  placeholder at all on that field.
- Frontend: a destination the test types is not overwritten when a source is then
  chosen (the `nocx-6hg2w.14` rule, re-asserted because this change moves the
  code that honours it).
- Frontend: with no Wails runtime, OR with no live local session, the dialog
  renders no drop target (`data-file-drop-target` absent), and the source field
  and picker still work. Both conditions asserted separately — they fail
  independently and a test covering one would pass while the other was wrong.
- Frontend: a multi-file drop leaves both fields unchanged and renders a refusal
  in the kit's validation slot.
- Go: `Dropped` with a target naming `api-import` emits `files.dropped` carrying
  that name; with a target naming nothing it refuses and mints nothing.
- Go: `..._OverTheWireConformsToContract` for `files.dropped` — the real
  notification off the real socket, against the schema with the new field
  `required` and `additionalProperties: false`.
- `npm run contracts:check` green; the generated `files.dropped.ts` regenerated
  and committed.
- Kit: `ui-drop-zone` has its README row; `npm run lint` (raw-controls,
  two-owners, row-grammar, menu-icons) green.

## Deliberately out

- **Paste and URL as sources** — `nocx-2cm98`, phase-2, under the OpenAPI epic
  `nocx-ttrlr`. This spec does not pre-empt that ask's shape; it leaves the
  source field as the one place a third source would join.
- **A session-free drop target** — its own bead (see §1). Addressing a
  non-session target is a real question and `EmitFilesDropped`'s session
  addressing was bought by a defect.
- **Folding the terminal pane's drop affordance onto `ui/drop-zone`** — its own
  bead (see §4).
- **Deriving the folder name from `info.name`.** The importer already reads it
  (`internal/apiimport/postman.go:263`) and writes it into the manifest, so the
  document does name itself. It is not used for the FOLDER because
  `validateCollectionName` refuses rather than sanitises (`/`, a leading dot, a
  length bound) and `info.name` is free text — deriving a folder from it needs a
  naming rule this change does not need to invent. The file's stem is what the
  person recognises from their downloads folder.
- **Importing several collections at once.**
