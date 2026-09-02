# Pets in the terminal

**Date:** 2026-09-02
**Session bead:** `nocx-q4qeh` (brainstorming) · **Implementation:** `nocx-q4qeh.1`
**Status:** written after the fact. The owner asked for a live prototype before the
document, so what follows records decisions that are already on the branch
(`71ce253a`, `511ef10e`, `005a8a85`) rather than proposing them. Where the branch and
this document disagree, the branch is the defect.

## 1. What a person can do that they could not before

**A small animal lives in the window, walks on the edges of the commands you have run,
and what it does follows how those commands turned out.**

The end-to-end check that watches them do it is `e2e/pets.spec.ts`: the pet arrives,
comes to rest on something the terminal drew, answers a command that succeeded and one
that failed, settles down for the length of a command that is still running, and leaves
when Settings says so.

## 2. What this crosses, and what those documents already decided

**AD-6 — the frontend owns render state, the backend never sniffs the byte stream.** The
pet is entirely a renderer concern and adds no backend traffic. It reads the outcome of a
command from `BlockRecord.status`, which the lifecycle projection already computed, and
never from bytes.

**ADR-0024 — an attempt is the authority for a command's completion.** The pet is a
consumer of that projection and adds nothing to it. It reacts where every freeze path
already converges (`ScrollbackController._settleFrozen`), so it cannot see a completion
the ledger has not.

**§3.1 / `nocx-iadtt` — the author is minted at submit and never derived.** The pet reads
`BlockRecord.author` to tell your command from the assistant's. It does not infer.

**AD-8 — one owner per behaviour, interface-first.** Four modules, one of which may touch
a document (§4).

**ADR-0036 / ADR-0037 — the HTTP upload and download routes beside the socket.** Not
crossed, and this is worth stating because it looks as though it might be: the pack that
ships is a static frontend asset, served by the same vite/embed path as `appicon-96.png`.
A future user-supplied pack (§8) would need a read path and therefore its own decision.

**`frontend/src/ui/README.md` — the kit grows by variants.** The pet needed one control
the kit did not have (§6).

## 3. Terrain: the landscape is the scrollback

nocx already draws the only landscape a screenmate needs. Every frozen command block is a
rectangle whose TOP edge can be stood on, and each block wears chips — the directory, the
duration, the exit badge — which are painted, raised and about sixty pixels wide. Those
are what the old screenmates actually walked on: a Neko walked title bars and window
edges, not the desktop.

Ground is declared as a list of `{selector, edge}`, plus the window's own bottom edge as a
floor with a reserved id, so every rule has exactly one kind of ground to reason about:

|                           | edge   | why                                                   |
| ------------------------- | ------ | ----------------------------------------------------- |
| `.tabbar`                 | bottom | the one shelf that does not move when you change tabs |
| `.pane.active .cmd-block` | top    | the command you ran                                   |
| `.pane.active .nocx-chip` | top    | the directory, the duration, the exit badge           |

Blocks come from the ACTIVE pane only: a pet standing on a block in a pane you cannot see
would be a pet you cannot see.

Three rules, each bought by watching the mockup fail:

**A ledge needs head clearance.** The block's top edge is the floor, so the body occupies
the space ABOVE it — which belongs to the previous block, or to nothing at all near the
top of the pane. Without this the animal walks off the top of the screen. It also means
the pet's declared size decides which blocks are ground, so changing the size re-derives
the terrain rather than merely rescaling the sprite.

**A ledge needs width.** A sliver is a place to stand, not a place to walk, and a pet that
turns round every frame reads as broken.

**Landing is swept, not sampled.** "Is the pet inside a ledge now" misses every ledge
thinner than one frame of travel, and a tenth of a second of falling covers more than the
gap between two blocks.

**The animal can go UP.** Stepping off an edge and descending from the middle move it
through the terrain beautifully and in one direction only, so over a few minutes every pet
ended on the floor and stayed there — the state that looks most like a sticker. The jump is
AIMED: its speed is computed from the ledge it is jumping to, so a shelf just overhead gets
a hop and one two blocks up gets a leap. A single launch speed is either too weak to leave
the floor or too strong for a chip. Nothing is caught while rising, so the arc peaks over
the target and the animal is caught coming down, which is also what makes it read as a
jump rather than a lift.

**A block that disappears drops the pet.** The pet holds a ledge by IDENTITY, not by
rectangle, precisely so that a block removed under it can be detected: it falls. It is
never quietly moved to another ledge, which would read as a teleport.

### Geometry is a snapshot

Read into a snapshot refreshed by `ResizeObserver`, a `MutationObserver` on the block
container, and the scroller's `scroll` event. Calling `getBoundingClientRect` inside the
animation frame is the obvious shortcut and turns every scroll of the terminal into a
layout-thrashing benchmark: each call forces the style and layout the scroll just
invalidated, sixty times a second, for as long as the pet exists. The pointer position is
converted against the same cached rect for the same reason.

## 4. Modules

| module               | owns                                                     | touches the DOM    |
| -------------------- | -------------------------------------------------------- | ------------------ |
| `pets/terrain.ts`    | rectangles → ledges; the swept landing test              | no                 |
| `pets/pet.ts`        | the state machine; every rule about what the animal does | no                 |
| `pets/pack.ts`       | the sprite pack, its takes, and the alpha trim           | no                 |
| `pets/setting.ts`    | the one owner of "is there a pet, how big, which one"    | no                 |
| `pets/overlay.ts`    | geometry, the clock, and painting                        | **yes, only here** |
| `pets/window-pet.ts` | the one owner of the window's animal                     | mounts the overlay |
| `pets/preview.tsx`   | the settings page's live sample                          | mounts the overlay |

`pet.ts` and `terrain.ts` take time as `dt` and chance as `rng`, so a thousand seconds of
cat runs deterministically in a millisecond and every rule is testable without a browser.

### One animal per WINDOW

The pet is an ornament of the window, not of a pane. A per-pane pet lived inside the
scrollback it was walking on, so it died with the tab and was born again in the next one —
switching tabs killed it. `pets/window-pet.ts` owns the single instance, and a pane asks
for it rather than being handed one: the caller is a PANE, at the moment a command starts,
and threading a window ornament through the pane manager's constructor would put it in a
signature that is about the window's parts. This is the shape `reconnect-setting.ts`
already uses, for the same reason.

The layer therefore spans `#app`, above the panes and below every real surface: a dialog,
a menu or a panel covers it. A cat is not more important than what the user opened.

## 5. Behaviour: three axes, not one enum

```
Locomotion:  idle | walk | run | fall
Mood:        calm | pleased | worried | tired
Activity:    sit | groom | stretch | lie | scratch | meow | sleep | none
```

Folding these together is the obvious move and it is wrong: it multiplies into
`happy-walk`, `sad-walk`, `happy-climb`, and every new mood doubles the machine. Kept
apart, a mood shifts the WEIGHTS of a table and costs one row.

**Choosing.** The animal finishes its current occupation before drawing another from a
weighted menu. Deciding afresh every frame is what makes a pet twitch instead of live.

**Sleep is arrived at, not chosen.** Boredom accumulates while the animal is not walking;
at `sleepAfter` (45 s) it sleeps and the mood becomes `tired`. Only an event wakes it.

**Leaving a ledge.** At the end of a ledge it either turns or steps off (`stepOff`, 0.4),
and the menu also offers descending from the middle. Both are needed: a command block is
the full width of the pane, and at a walking pace an edge is the better part of a minute
away — without the second the animal lives on the ledge it first landed on. It leaves AT
the edge, never a pixel past it: blocks are all the same width, so an overshoot would miss
every block below by the same pixel and land on the floor every time.

**The pointer.** The layer takes no clicks and no focus by design, so moving away from the
cursor is the only way a person touches the pet at all. Measured against the animal's BOX
plus a margin and vertically from its middle — a radius around its position leaves a 96px
cat's own flank outside the threat, and it sits there unbothered. Cornered at a ledge end
it steps off rather than turning back into the cursor.

## 6. Events: what the animal is told

**A command starts** → `attend(author)`. Not an Outcome: nothing has happened yet, so it
is not a verdict. Watching is not a fourth axis either — it NARROWS the menu rather than
colouring the animal, so a worried cat watching a build still fidgets. No leaving the
ledge, no running, and no sleeping: a pet that dozed off during your build would report
the opposite of what is happening.

**A command finishes** → `react(outcome, author)`.

|                      | your command          | the assistant's       |
| -------------------- | --------------------- | --------------------- |
| running              | lies down             | sits up and watches   |
| success              | meows · `pleased`     | stretches · `pleased` |
| failure              | scratches · `worried` | lies down · `worried` |
| `entered`, abandoned | sits · `calm`         | sits · `calm`         |

Your command is addressed to the cat as much as to the shell; the assistant's is work it
merely watched. An agent failing is not the person failing, and a pet that scolded them
for it would be wrong about who did what. `entered` and abandoned are deliberately not
read as failure — neither is a verdict on the command, and a cat that sulked at every
`ssh` would sulk most of the time.

**The verdict routinely arrives AFTER the next command has started.** The freeze waits on
a render fence and the start does not. So a reaction is protected while it plays
(`reacting` marks an answer; an ordinary occupation is interrupted freely), and after
answering, the controller returns the animal to watching whatever is still running.

**News can arrive before the sprites do.** Sprites are fetched asynchronously and the
terminal does not wait for them; mood and attention survive the mint of the animal.

## 7. The art, and why the provenance is in the repository

nocx ships builds from a public repository, so **anything vendored here we redistribute**.
That single fact decides the whole question, and it is not theoretical: vscode-pets —
4000 stars, MIT, the largest project in this genre — had to delete its cats from GitHub at
the artist's request, and its `docs/credits.md` is twelve artists under twelve licences.

The pack is **luizmelo's Pet Cats, CC0**, six colours, with the pack's own `LICENSE.txt`
and a `SOURCE.md` carrying the source URL, the download date and the archive's sha256. CC0
is the only licence in this genre that needs no further argument; the provenance is what
makes that checkable later without archaeology.

A clip is a set of TAKES rather than one drawing, and a take is chosen afresh each time the
clip starts. A cat that washes itself with the identical five frames every time is a loop,
not an animal. A missing sheet costs one take; a clip whose takes all failed costs one
behaviour; only a pack that yields nothing is an error — Cat-3 has no scratch, and losing
the animal over a drawing the artist did not make would be the wrong trade.

The trim is computed once as the UNION over every frame of every clip. Per-frame cropping
re-centres the animal on itself, which flattens the walk's bob and deletes the crouch that
makes the stretch read as a stretch.

## 8. Settings

Declared in Go (`internal/settings/settings.go`), so the page renders itself.

- **`pets.enabled`** (default on). Off means the pack is never fetched: a decoration
  somebody declined should cost them no bytes, and "loaded but hidden" is how a disabled
  feature quietly keeps running.
- **`pets.size`**, 16–96 px. Also decides which blocks are ground (§3).
- **`pets.pack`**, the six colours.

All three are live — read in the `settings.changed` loop beside the theme, because someone
turning the pet off is looking at the pane while they do it.

**The slider is a VARIANT of the number control, not a sixth `ControlKind`**, following
the reasoning `multiline` already states one field above it in the same struct: the value,
its bounds, its unit and its refusals are the number control's in every respect, and a kind
of its own would mean paired cases doing the same thing in `validateValue`, `coerceValue`
and the `settings.set` handler. Only the affordance differs. It commits on release and
reports live while dragging — a drag passes through every value between its ends and none
of them was chosen.

**The preview runs the real overlay** over a mock scrollback. Drawn separately it would be
a second implementation of how the pet looks, agreeing with the terminal until the day one
of them changed.

## 9. Deliberately out

- **A pet per workspace.** Assigning a different animal to each workspace is a good idea
  and deliberately not done yet: it means a settings axis per workspace, for a decoration.
  Live with one per window first and find out whether the pet changing with the workspace
  is wanted. `nocx-q4qeh.3`.
- **Bring your own pet.** A user-supplied pack in an open format, plus importers for
  Shimeji and eSheep. This is where the licence question stops being ours: if the pack
  comes to the person from its author and nocx only reads a folder, nocx is not the
  distributor. It needs a format, an import with a real security envelope (untrusted
  input: magic bytes, no SVG, no `DOCTYPE`, size and frame budgets, copy into the managed
  appdir, execute nothing) and a read path that ADR-0036/0037 do not currently allow.
  `nocx-q4qeh.2`.
- **A second species.** The vocabulary is "which pet" rather than "which colour" so that
  this costs a directory and a row.

## 10. What would make this wrong

- A pet that takes a click, a scroll, or focus. The layer is `pointer-events: none`, and
  the whole interaction budget is "it moves out of your way".
- A pet that changes the layout of the pane. It is an absolutely-positioned overlay and
  paints only through `transform`.
- A pet the terminal waits for. A pack that fails to load must leave the terminal exactly
  as it was.
- `prefers-reduced-motion`. Motion is the entire feature, so there is no reduced variant
  that keeps a moving cat: asked for less motion, we show none.
