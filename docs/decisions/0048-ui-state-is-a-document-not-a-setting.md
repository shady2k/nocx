# ADR-0048 — UI state is a document, not a setting

- **Status:** Accepted.
- **Date:** 2026-08-18
- **Related:** [ADR-0011](0011-persistence-storage-capabilities-and-secret-references.md)
  (three storage capabilities; `DocumentStore` already names "tab restore" as its
  tenant), [ADR-0027](0027-structured-backup-and-restore.md) (what backup carries),
  AD-1 (one WebSocket: binary data plane + JSON-RPC control plane), AD-8 (one owner
  per behaviour, wired at a single composition root),
  `docs/vision.md` §10 (no cloud sync, ever). Beads `nocx-mqie` (the epic),
  `nocx-mqie.1` (window geometry), `nocx-mqie.3` (sidebar width in the wrong store),
  `nocx-1ei`/`nocx-8v51` (the settings registry), `nocx-qmcu` (the sidebar width
  controller), `nocx-l21ib` (session restore — explicitly not this).
- **Extends:** ADR-0011 §1, by naming the module that owns `DocumentStore`'s
  "tab restore" tenant and by drawing the settings/UI-state line in writing.
- **Formerly ADR-0033:** renumbered 0048 on 2026-08-28 (`nocx-yjvg5`) — the number was shared with [ADR-0033 — `auto` is the name for "not yet answered"](0033-auto-is-the-name-for-not-yet-answered.md), which is older and keeps it.

## Context

The app forgets how it was left. `main.go` opens every launch at a hardcoded
1024×768 and nothing anywhere reads or writes window geometry. That is the visible
symptom; the cause is that nothing owns the class of state it belongs to.

Two other things had already been decided ad hoc in that vacuum, in opposite
directions, and both are wrong in the same way.

**Downward, into the renderer.** `SidebarImpl` keeps the panel's collapsed state in
`localStorage` under `nocx.sidebar.collapsed` — a fact about the application held in
a store the application does not own, cannot enumerate, cannot back up, and cannot
repair. The owner settled this on 2026-08-18: **localStorage may not carry facts.**
The same afternoon a `localStorage` implementation of "which tab was in front" was
written and reverted within the hour under that rule.

**Upward, into the settings registry.** The sidebar's width is registered as
`settings.SidebarWidth` (`internal/settings/settings.go`, key `sidebar.width`). Two
symptoms follow from the one mistake: Settings → Interface lists a "Sidebar width"
row reading `206.3828125 px` — a fractional pixel count presented as a value to type
— and the section carries a **Modified 1** badge, because dragging a panel edge was
recorded as a decision.

So the question this ADR settles is not "where do we put a new file". It is: **which
of the app's existing stores owns state the app must remember without being asked**,
and how does anyone tell that state from a setting when both survive a restart.

### The distinction

> **A setting is something a user deliberately chooses.**
> **UI state is what the app must remember without being asked.**

They are not the same class, and the test is the act that writes them:

|                     | Setting                                    | UI state                                             |
| ------------------- | ------------------------------------------ | ---------------------------------------------------- |
| Written by          | a deliberate choice, at a control          | a side effect of using the app — a drag, a resize    |
| Declared?           | yes: type, default, bounds, section, label | no: a struct field with a sane zero                  |
| Appears in Settings | yes, that is the point                     | never                                                |
| "Modified" badge    | yes — a decision differs from the default  | never — a drag is not a decision                     |
| Portable?           | yes: it is what the user chose             | no: it describes **this machine's** window and panes |
| Wrong value costs   | the user's intent is lost                  | a panel is the wrong width until they drag it again  |

`ui.theme` is a setting: the user picked it, it means the same on any machine, and
"differs from the default" is worth a badge. A window's position on a 3440×1440
display is not a choice and means nothing on a laptop.

## Decision

### 1. UI state is a `DocumentStore` document, owned by `internal/uistate`

Not a new mechanism. ADR-0011 §1 already assigned this data to the store it named —
`DocumentStore` is for "bounded, human-recoverable configuration as atomic JSON
documents. Settings, profiles, groups, credential _metadata_, **tab restore**." This
ADR names the module that owns that tenant and says what is in it.

```
             internal/uistate  ── the only owner of the answer ──
                      │
                      ▼
        storage.DocumentStore    (atomic temp→fsync→rename, 0600 in 0700)
                      │
                      ▼
          <ConfigDir>/uistate.json
```

- **Document name:** `uistate.json`, beside `settings.json` in `Paths.ConfigDir()`.
  Config, not data: it is human-recoverable and a user may delete it to get a clean
  window back — deleting it is a supported repair, and its only cost is defaults.
- **Versioning:** the shared `storage.Module` protocol, exactly as `settings` uses it
  (ADR-0011 §6: each module owns its own monotonic version, there is no app-wide
  one). `storage.Module{Name: "uistate", Current: 1}`, migrations appended as the
  shape changes.
- **Single writer.** `internal/uistate.Store` is the one owner (AD-8). Nothing else
  reads or writes the file, and no surface keeps a second copy of any field in it.

### 2. It is not the settings registry, and not a namespace inside it

The registry is a **declaration** machine: every key has a spec, and the machinery
built on that — the generated Settings screen, the per-section "Modified" count, the
export surface — follows from the declaration existing. There is no way to register
`sidebar.width` and not get a row on Settings → Interface, because the row is what a
declaration _is_. Adding a "hidden" flag to the registry would be building a second
class inside a store whose whole contract is that its contents are one class.

The reverse containment is equally wrong: UI state must not grow a declaration
system, a revision counter or a `Modified` notion. It is a plain struct with a zero
value that works.

**Consequences that are load-bearing and therefore stated here:**

- **UI state never appears in Settings**, on any page, under any group.
- **UI state never counts toward a "Modified" badge.** Dragging a panel edge marks
  nothing as modified, because nothing was decided.
- **UI state is not exported, not imported, and not backed up** (ADR-0027). Restoring
  someone else's window position — or your own from a machine with different displays
  — is wrong at best and off-screen at worst. It is per-machine by construction, so
  it is also the one class that would never sync even if we ever synced anything
  (`vision.md` §10 says we never will).

### 3. What is in the document

```jsonc
{
  "schemaVersion": 1,
  "window": {
    "width": 1440,
    "height": 900,
    "x": 120,
    "y": 64,
    "maximised": false,
    "fullScreen": false,
    "displays": "3:2560x1440p,1920x1080,1920x1080", // fingerprint — see §6
  },
  "sidebar": {
    "collapsed": false,
    "activeViewId": "ports",
    "width": 206, // WHOLE CSS pixels
  },
  "activeTab": "", // the durable pane id of the tab in front — see §7
}
```

- **`window`** is knowable only to the Go/Wails side and is written only there.
- **`sidebar`** is knowable only to the renderer and reaches the document over the
  control plane (§5). `width` is a **whole number of CSS pixels**: nothing about a
  panel edge is meaningful to seven decimal places, and the fractional value was an
  artefact of storing a `getBoundingClientRect()` result verbatim. Bounds stay where
  they are today (200…640, `frontend/src/sidebar-width.ts`), and the clamp is applied
  on read as well as on write, so a hand-edited file cannot produce an unusable panel.
- **`activeTab`** is which tab is in front. Its value is one durable pane id
  (`Pane.wireId`) and nothing else — not an index, which is invalid the moment the tab
  set differs. `PaneManager` reads it once at boot and writes it on every activation;
  an id naming no pane leaves the window on the first tab, because the tab **set** is
  not restored by this document (`nocx-l21ib`) and a remembered pane may legitimately
  be gone.

**Split justified.** The two halves of this document are written from two different
sides because they are knowable from two different sides; that is not a split store.
There is one document, one schema, one file, one owner — `internal/uistate` — and the
renderer reaches it the same way it reaches every other backend fact: over the
control plane (AD-1). The alternative, letting the renderer keep its half in
`localStorage`, is the arrangement this ADR exists to end.

### 4. Reading: absence is an ordinary state

The document is read once, at composition, before the window is created.

| On disk                               | Result                                                               |
| ------------------------------------- | -------------------------------------------------------------------- |
| absent                                | **defaults.** Not an error, not a log line above `Debug`, never a UI |
| present and valid                     | used, after per-field validation (below)                             |
| unreadable (permissions, I/O)         | defaults + one `slog.Warn`; the next write replaces it               |
| unparseable JSON                      | defaults + one `slog.Warn`; the next write replaces it               |
| valid JSON, a field out of range      | **that field** falls back to its default; the rest is kept           |
| valid JSON, an unknown field          | ignored                                                              |
| `schemaVersion` newer than this build | defaults; the file is left alone, never truncated                    |

The rule behind the table: **a bad byte in this file costs a user their window
size, never their launch.** Validation is per field and never rejects the whole
document, because a single unknown sidebar view id must not also throw away the
window geometry that was fine.

This is the one soft degrade permitted here, and it is permitted because it has no
UI to contradict (AGENTS.md: "a soft degrade must be visible in the product, not only
in a log"). There is no Settings row promising that the sidebar width is remembered;
if the file is unreadable the panel is simply the default width, which is exactly
what the product says it will be.

### 5. Writing: coalesced, trailing, flushed at shutdown

A window drag emits geometry continuously. One `fsync` per event would be absurd, and
the value mid-drag is not interesting — only where the drag stopped is.

- **Coalescing debounce, 500 ms trailing.** A change replaces the pending state and
  restarts the timer; the write happens 500 ms after changes stop. Consecutive changes
  inside the window cost exactly one write.
- **500 ms** is chosen to be longer than the gap between two frames of a drag and
  shorter than the gap between a user releasing the mouse and reaching for ⌘Q.
- **The store is the debouncer**, not each caller. A caller says "this is the state
  now"; when it is written is not the caller's business. Callers are therefore
  idempotent and cheap, and there is exactly one place the policy can be got wrong.
- **Flush on shutdown.** `Close` writes any pending state synchronously before
  returning, so a clean quit inside the debounce window loses nothing.
- **Unclean shutdown loses at most the last debounce window.** That is accepted, and
  it is why this data lives here rather than in a journalled store: the recovery for a
  lost sidebar width is that the user drags it again. Because writes are atomic
  (temp → fsync → rename, and the directory is fsynced after), a crash mid-write
  leaves either the previous document or the new one — never a torn file. There is no
  partial-write recovery path because there is no partial write.

### 6. Geometry: display identity and the missing-display fallback

The failure this rule prevents: geometry saved with a second monitor attached, and the
app restarted without it, restoring the window to coordinates that are now nowhere.
A window a user cannot see and cannot drag is worse than one that opened in the wrong
place.

**What we can actually know.** Wails v2's `runtime.ScreenGetAll` returns
`Screen{IsCurrent, IsPrimary, Width, Height, Size, PhysicalSize}` — **no origin
coordinates**. So we cannot compute the desktop's union rectangle and cannot test
whether a saved point falls inside it. Any rule phrased as "is the window on screen"
is unimplementable on the API we have, and writing one anyway would produce a check
that silently always passed.

**The rule we can implement, therefore, is identity, not containment:**

1. On save, record a **display fingerprint** alongside the geometry: the number of
   attached displays and each display's logical size, in a canonical order, with the
   primary marked. It is a string, deliberately human-readable, so someone reading
   the file can see why their window moved.
2. On start, recompute the fingerprint.
   - **Match** → restore position and size as saved.
   - **Mismatch** (a display added, removed, or resized) → **discard the position**,
     keep the size clamped to the primary display, and let the window centre. The
     saved position is not deleted from the file; it is simply not used this launch,
     so plugging the monitor back in restores the old arrangement.
   - **Fingerprint unavailable** (`ScreenGetAll` fails) → treat as a mismatch. When we
     cannot tell, we open somewhere visible.
3. **Size is always clamped** to the primary display's logical size and to the app's
   `MinWidth`/`MinHeight`, on every path including a matching fingerprint. A saved
   size from a larger display, or a hand-edited absurd one, cannot produce a window
   bigger than the screen it opens on.
4. **Maximised and full-screen restore as states, not as pixels.** A maximised window
   is restored by asking the platform to maximise it, and the underlying normal
   geometry is kept so unmaximising lands where it did before. Storing the maximised
   pixel size and setting it would produce a window that merely looks maximised and
   behaves like a normal one.

The decision procedure is a **pure function** — saved geometry plus the observed
display list in, geometry to apply out — with no Wails, no clock and no I/O, which is
what makes the mismatch and the clamp testable at all. The Wails side does nothing but
read the probe, call the function, and apply the answer.

### 7. The wire

Per AGENTS.md rule 5, both methods have a JSON Schema in `contracts/`, the renderer's
types are generated from it, and the Go side is validated against it — DTO **and**
over the real socket.

- **`uistate.get`** → the renderer's half of the document (sidebar, active tab) plus
  nothing else. **Window geometry is deliberately not on the wire in either
  direction.** The renderer cannot know it, cannot act on it, and giving it a copy
  would create a second potential owner of a fact the Go side already owns (AD-8).
- **`uistate.set`** ← the renderer's half, as a whole value. Not a generic
  `(key, value)` bag: an untyped key-value setter is the shape ADR-0011 removed from
  `internal/config`, and it would make the schema `additionalProperties: true` in
  spirit whatever it said on paper.

The renderer's calls are fire-and-forget with respect to the UI: a failed write leaves
the panel where the user put it and warns; it never reverts the drag. Restoring on
start is a read at bootstrap, in the same phase as the settings snapshot.

There is no `uistate.changed` broadcast. One window observes this state and one window
writes it; a notification would be the app telling itself what it just did.

## Alternatives considered

**`localStorage` for the renderer's half.** Rejected by the owner, 2026-08-18, in one
line: localStorage may not carry facts. Concretely — it is not backed up, not
inspectable by the backend, not repairable by hand in any place a user would look, it
is silently discarded when a webview's storage is cleared, and it is per-webview
rather than per-app. It is legitimate for a **cache** whose loss costs nothing and
which must be readable before any RPC exists — `renderers/theme-bootstrap.ts` is
exactly that, and stays — and for a per-machine hint no other component reads
(`integration/status.ts`). Neither is a fact anybody else needs.

**A hidden section in the settings registry.** Rejected: see §2. The Modified badge and
the generated screen are not decorations on the registry, they are what a declaration
means; suppressing them per key builds a second class inside a single-class store.

**A new store beside `DocumentStore`.** Rejected: `DocumentStore` already does atomic,
human-recoverable JSON with a versioning protocol, and ADR-0011 already assigned this
data to it by name. A second mechanism would be the "two answers to one question" that
ADR-0011 was written to end.

**Storing a tab index rather than a pane id.** Rejected in advance: an index is
meaningless against a different tab set, and the tab set is not restored by this epic
(`nocx-l21ib`). A durable id that matches nothing at start simply yields no restored
tab, which is correct.

**No debounce, write on every change.** Rejected: an `fsync` per mouse-move frame.
**Periodic write instead of debounce:** rejected — it writes when nothing changed and
still misses the last change before a quit, which is the one that matters.

## Consequences

- `settings.SidebarWidth` is **removed** from the registry, not deprecated in place —
  this is greenfield and there are no shims (`nocx-mqie.3`). The Interface section
  loses a row it should never have had, and a drag stops marking it Modified.
- `nocx.sidebar.collapsed` leaves `localStorage`; `SidebarStorage` as an injectable
  browser-storage seam goes with it.
- The next surface that wants to remember something without being asked has one place
  to put it and a table (§Context) to check itself against first. If the answer to
  "did the user choose this?" is yes, it is a setting and this document is the wrong
  file.
