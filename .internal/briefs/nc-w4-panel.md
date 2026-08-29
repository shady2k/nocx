# W4 — the panel names things, and the bell counts what you asked to see (nocx-alc4y, nocx-mci6x)

Read `.internal/briefs/_common.md` first. Then design §4 (the rendering half), §6, §7
and §10, in full. This is the largest task in the epic and the two beads are one worker
because they meet in `main.tsx`.

**Your worktree:** stated in the message that pointed you here. `pwd` before anything.

**Files you own — nobody else touches these:**

```
frontend/src/notify/notifications-panel.tsx   + its tests
frontend/src/notify/feed-store.ts             + its tests
frontend/src/notify/notification-activation.test.tsx
frontend/src/main.tsx
frontend/src/styles/components/notifications-panel.css
```

**Files other workers own — escalate, do not edit:** all Go, `contracts/**`,
`frontend/src/ui/**` (the kit), `frontend/src/panes.ts`, `frontend/src/sidebar.tsx`,
`frontend/src/styles/components/sidebar.css`, `frontend/src/settings-domain.ts`.

**What already landed before you started** (wave 1, on your base):

- `notify.catalogue` — a read returning `{kind, label, description}` per kind, with a
  generated type at `frontend/src/generated/notify.catalogue.ts`.
- Six settings `notifications.centre.<kindId>`, all defaulting **on**.
- `RecordRow.kind` now takes a typed `description` and passes it to the badge's title.
- `CollectionRow` sets `data-activatable` and activatable rows get `cursor: pointer`.

## 1. The panel names kinds in words (§4, rendering half)

`notifications-panel.tsx:271` passes the wire enum straight through as the badge's label,
and `:133` labels the session filter option with the raw 64-hex id. A person reads
`bell`, `block.finished`, `transfer.finished`.

The kind badge, its tooltip and the Kind filter all read `notify.catalogue` — the same
word in all three. Hold the result in a **Solid signal**, so a kind that arrived in a push
before the catalogue did re-renders when it lands.

**The fallback has both ends named.** From first render until the **first successful**
read — across any number of failures and reconnects — a kind with no entry renders as its
wire value with dots **and camel-case boundaries** split and the first letter capitalised:
`pane.workFinished` → `Pane work finished`. One function, in one renderer utility, used by
both the badge and the filter so they cannot disagree. Never the raw slug, never blank.

## 2. The panel names sessions (§7)

`sessionNameOf(backendId, sessionId)` arrives as a **prop**, beside the `canActivate` the
panel already takes. In `main.tsx` it is the same lookup that already resolves activation
at `:1107`:

```ts
sessionNameOf={(backendId, sessionId) =>
  tm.findBySession(backendId, sessionId)?.displayTitle ?? null}
```

**`PaneManager` is not modified.** Two earlier drafts of this were withdrawn and the spec
records why: the first added a second owner of the name; the second redefined
`sessionDisplayName`, which has consumers this work has no business touching
(`panes.ts:1109`, `:1193`, `:2170`, `blocks.ts:1617`) — and `sessionWhere` reads its tab
from one lookup and its machine from another, so redefining half of it would let the
approval prompt name a tab from one pane and a machine from another. If you find yourself
editing `panes.ts`, stop and escalate.

Sessions with no name collapse into **one** option, `Unavailable sessions`. Not "Closed":
the name is also missing after a renderer reload, for an event that preceded its pane, and
for every non-local backend. One option is honest about what the panel can distinguish;
one option per unnamed session would be a menu of identical entries.

**No id and no dotted slug is rendered in any kind badge or filter option, in any state.**

**Reactivity needs a real seam.** `AXES` is a module-level constant (`:116`) and must move
inside the component so its labels can read props — but that alone is not enough:
`sessionNameOf` reaches plain `PaneManager` fields, which Solid cannot track. `main.tsx:546`
states this rule for `activeOrigin` and solves it with a signal fed on change. Do the same:
a display-revision signal at the composition root, bumped by the same notification that
repaints the tab strip (`setTabDecoration`'s `onDisplayChange`, `panes.ts:202` — subscribe
to it, do not modify it), read inside `sessionNameOf`. Without it, a tab renamed while the
panel is open keeps its old label until an unrelated refetch.

## 3. The bell counts what you asked to see (§6)

`FeedStore.unreadCount()` becomes a **memo** over `occurrences()` and the hidden set:
count the visible rows whose `read` is false.

**Not** `wireUnread − hiddenUnread`. The store holds occurrences and the wire count in two
independent signals (`feed-store.ts:39`, `:51`); a formula spanning both has an
intermediate state where one is set and the other is not, which is two answers to one fact.

It is exactly derivable: the backend counts **rows**, not constituents — `Snapshot`
increments once per occurrence whose `ReadAt` is nil (`feed.go:385`) — and every
occurrence on the wire carries its `kind` and its `read`.

**The panel's own Host/Session/Kind filter still may not reach the count.** That is the
distinction the whole section rests on: a transient narrowing is not a statement that you
stopped waiting; a settings toggle is.

**The badge before the first read is now a specified state.** The store starts at zero and
reads asynchronously (`feed-store.ts:38`, `:96`), and a failed read keeps the last snapshot
on purpose (`:67`) — so today a never-successful read shows a confident zero forever, which
says "nothing happened" when it means "I could not look". Distinguish **not yet known**
from **zero**: the rail draws no badge for either, as now, and the **panel** says so —
"Could not read notifications", a different sentence from "Nothing to catch up on".

**`dropped` stays global.** It carries `count`, `oldest`, `newest` and no kind, so it
cannot be restricted. Reword the line so it says it describes the whole feed rather than
implying it describes what is on screen. The "N of M shown" line counts visible kinds as
its M.

## 4. Where the hidden set lives (§10)

**One** reactive projection of the settings document, owned by the composition root, read
by the store. **No second `SettingsObserver` handler** — it accepts many
(`settings-observer.ts:25`), and a second one would give a single settings push two
independent refetches and two reconciliations of one fact. Extend the existing
reconciliation at `main.tsx:935`, which already reads the snapshot once and applies known
keys.

**One existing line has to change.** The first settings snapshot is awaited at
`main.tsx:385`, the store is built at `:1047`, the sidebar mounts at `:1131` — so the set
can be seeded before anything renders and there is no flash of hidden kinds. But that
snapshot's revision is dropped with its block-local variable and the observer is started
with a hard-coded baseline of zero (`main.tsx:937`). Carry the initial revision to
`observer.setRevision` instead.

**The interval, both ends:** seeded from the first snapshot before the sidebar mounts;
every later change replaces it **whole** after the revision check. No render observes three
kinds hidden and three not.

**On reconnect the two reads race**, and that is accepted rather than coordinated: the feed
read and the settings read are started by different subscribers. The interval closes at the
first successful settings snapshot of the current connection, at which point the count is
recomputed from the occurrences already held. Say so in a comment; do not build a barrier.

**A filter pick whose kind is hidden is cleared.** `activeOn` (`:190`) treats a vanished
option as no filter without clearing the stored pick — so hiding a kind would silently drop
the filter and un-hiding would silently restore one the user had long forgotten.

## Assertions

- No id, no dotted slug, in any kind badge or filter option, in any state.
- An unknown kind renders `Pane work finished`, never `pane.workFinished`, never blank —
  and keeps doing so until the first successful catalogue read.
- `sessionNameOf` returning null puts the row under the single `Unavailable sessions`
  option; two unnamed sessions produce **one** option, not two.
- A tab renamed while the panel is open relabels its filter option, with no refetch.
- The memo equals the wire count when nothing is hidden; excludes hidden kinds otherwise;
  is **unmoved** by the panel's own filter.
- A batch of settings changes applies as one replacement — no render sees a partial set.
- The panel distinguishes "nothing to catch up on" from "could not read".
- Hiding a kind clears a filter pick that named it.
- `frontend/src/panes.ts` is unchanged. If your diff touches it, you took the withdrawn
  road.

## Verification, scoped

```
cd frontend
./node_modules/.bin/tsc --noEmit -p tsconfig.json
./node_modules/.bin/vitest run src/notify/ src/settings-observer.test.ts
```

Then `src/panes.test.ts` unchanged-and-passing, as evidence you did not move `PaneManager`.
Nothing wider: no `npm test`, no `make ci`, no e2e.

## When you are done

Print exactly this line and nothing else on it:

    NCDONE-3f7a::w4-panel

If you cannot finish, print instead:

    NCBLOCK-3f7a::w4-panel <one line why>
