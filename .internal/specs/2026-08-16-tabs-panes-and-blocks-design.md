# Tabs, panes and blocks are backend objects

- **Date:** 2026-08-16
- **Beads:** `nocx-kc2l6` (this session), `nocx-isoph` (the workspace epic), `nocx-l21ib`
  (restore), `nocx-ebl4` / `nocx-fraus` (the foundation slice), `nocx-jv3q` and children
  (the tab-strip epic), `nocx-49d4` (the ledger's workspace owner), `nocx-rtg0.3`
  (`ledger.*` over the wire), `nocx-wyp3p` (the backend calls a connection a tab),
  `nocx-o9jdu` and `nocx-iehws` and `nocx-mgbjx` (filed here)
- **Amends:** `.internal/specs/2026-08-15-workspaces-ux-design.md` — §4.4 and §5.2 are
  withdrawn, §8 question 5 ships (§9 here). **And ADR-0008**, whose "output bytes are not
  retained" was already superseded by ADR-0019 and is recorded as stale rather than
  re-decided (§6.3).
- **Status:** design, first draft. Settled point by point with the owner on 2026-08-16,
  in dialogue, one question at a time.

## 1. In one sentence

**The backend owns the workspace, the tab, the pane and the block; the frontend asks it to
create them.** A pane is the thing that survives — it holds the shell, the directory and
the blocks — and a tab is the strip entry that shows one or more panes together.

**What a user can do that they cannot today:** quit with eight tabs across three
workspaces, reopen, and find them — with their blocks and their output — where they were.
And drag any tab onto any other to watch them side by side, then drag it back out, without
anything being recreated.

## 2. What this crosses, and what those documents already decided

| Boundary                     | What it already decides                                                                                                                                                         | What this design does about it                                                                                                                                                                       |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-7**                     | Session-id is server-authoritative; the backend `session` module is the authoritative registry                                                                                  | **Intact, and the reason is concrete**: the session id is embedded in the remote launcher as `NOCX_SESSION_ID` before the connection exists, so the renderer cannot supply it in time (§7)           |
| **AD-1**                     | One WebSocket: raw binary data plane, JSON-RPC control plane                                                                                                                    | **Reused, and it is what makes the window a viewport** (§10) — the backend is already a server, so a second renderer is a client, not a second application                                           |
| **AD-8**                     | One owner per behaviour                                                                                                                                                         | The rule this design applies most often: to the two edges on a tab (§4.2), to the display group (§4.3), to the id of the default workspace (§7), and to the unlock (`nocx-o9jdu`)                    |
| **AD-9**                     | Reconnect: a renderer re-attaches to live sessions                                                                                                                              | Why frontend-minted ids need an untrusted-input discipline (§7): a create whose answer was lost is retried, and a retry must not produce a second object                                             |
| **ADR-0019**                 | One authoritative ledger. **"Historical reconstruction — a query over the ledger… this is what 'reopen my tabs and blocks' means"**; restore is three promises named separately | **This design is that ADR arriving in the product.** Nothing here re-decides restore; §6 and §8 build what it already specifies, and §6.3 records that ADR-0008's contrary line is the stale one     |
| **ADR-0008**                 | Blocks are a keyboard-first ledger of landmarks; **"output bytes NOT retained by default (secrets)"**; a block anchors on xterm `IMarker`s                                      | The retention half is **superseded by ADR-0019** and recorded as such (§6.3). The anchor half is genuinely obsolete: an `IMarker` lives in the renderer's terminal buffer and no restart survives it |
| **ADR-0020 §5**              | A container never confers authority                                                                                                                                             | Untouched. Nothing here reads authority from a tab, a pane or a workspace; the fence remains a later epic with its four open questions                                                               |
| **workspaces-ux §4.4**       | "`workspaceId` is on the session in the backend while tabs live in the renderer, so the two ends of the invariant are in two processes and no one owns both"                    | **Withdrawn — the problem is deleted, not solved.** Both ends are in the backend now (§4.1)                                                                                                          |
| **workspaces-ux §5.2**       | "The backend must not gain a _tab_ id"                                                                                                                                          | **Narrowed, not overturned** (§4.4): all three of its stated reasons are about **addressing**, and every backend→renderer address stays a `sessionId`. Storage was never what it forbade             |
| **workspaces-ux §6, §8.5**   | A branch is a label derived from cwd, no new object; switchable grouping is "deliberately not in this cut"                                                                      | **§6 stands. §8 question 5 ships** — project, host, worktree and branch become grouping axes over a flat list, and still store nothing (§9)                                                          |
| **`nocx-fraus`**             | Every session carries a non-null `workspaceId`                                                                                                                                  | **Its home moves** (§4.5). The invariant and the single owner of the default survive; the field belongs to the tab, and on the session it becomes derived                                            |
| **D5** of the lineage design | Workers die with the backend; a process is never resurrected                                                                                                                    | **Reused exactly**, and it is the whole reason a pane and its session are two objects (§5)                                                                                                           |

## 3. The model

```
workspace              flat, never nested, user-created
  └─ tab               the strip entry: colour, name, position, pinned, layout, seen-mark
       └─ pane         the durable identity: pipe, cwd, blocks
            └─ block   what ran and what it printed
                 └──▶ session   provenance: which pipe it ran in
```

Four objects, and each exists for a reason the others cannot serve:

- **workspace** — which tabs are one piece of work. Flat; depth comes from lineage.
- **tab** — what occupies one slot in the strip, and what the user decorates.
- **pane** — **the identity that never changes.** It outlives its shell, its tab and the
  application.
- **block** — a command and its output.

And one that is not durable at all:

- **session** — the pipe: a local PTY or an SSH channel. It dies with the backend (D5) and
  a fresh one is opened on the other side of a restart. It is never an anchor for anything
  that must survive.

## 4. The tab

### 4.1 Both ends of the invariant are now in one process

The withdrawn §4.4 named a real defect: an invariant whose two ends live in two processes
has no owner. That is fixed by moving the objects, not by writing a protocol. The backend
owns the workspace, the tab and the pane; the frontend **asks** it to create, move and
destroy them, and renders what it is told.

**A tab exists while it holds at least one pane.** The same shape as the workspace rule,
and it needs no lifecycle code: dragging the last pane out of a tab leaves a row with no
members, and the row is removed in the same transaction that moves the pane.

### 4.2 A tab has two edges, and they must never be one

```
tab ──parent──▶ tab      who spawned whom: provenance, immutable, never set by hand
tab ──group───▶ tab      what is shown together: set by dragging, in both directions
```

Lineage and layout agree almost everywhere and disagree exactly where it matters: an agent
spawns a worker you want as a separate tab; you split two tabs by hand that never spawned
one another. Carrying both on one column is the failure AGENTS.md names — the loser goes on
advertising what it can no longer deliver.

So `parent` stays provenance, and §4.5 of the workspaces design keeps its refusal intact:
dragging between **lineage** groups is still refused, because dragging for display touches
the other edge and changes no kinship.

### 4.3 The display group has no host, and therefore no object

Dragging tab B onto tab A shows them together. A is not a container: it is a real tab with
its own panes, and it is only "first" because it was the drop target. So a pointer edge is
the wrong shape — pull A out and the remaining members point at something that left.

The relation is **symmetric**: A, B and C are shown together, no host, any of them can be
pulled out and the rest do not notice. In storage that is a value shared by the members,
not a row of its own, so nothing can outlive its membership.

> An earlier draft of this section had a `tab_groups` row and the owner rejected it, then
> reversed the rejection for a stated reason: colour and pinning are properties of the
> **strip entry**, and a thing with properties of its own needs a row. What the reversal
> settles is that the strip entry **is** the tab — there is no third word between workspace
> and pane.

### 4.4 The tab is the thing that is minted and destroyed automatically

A pane is never created or moved by the user; it is dragged. Everything else follows:

- drag a pane out of a tab → **a tab is minted for it**, and it appears in the strip;
- drag a pane into another tab → its `tab_id` changes, and a tab left with no panes is
  removed;
- the pane's identity, its blocks, its history and its live pipe are untouched in both
  directions, because only a reference moved.

**This is what a `pane` type could never do.** The owner's objection to panes was exact: a
pane can never become a tab without creating a new object and migrating content into it.
Here the durable object is the pane and the tab is the cheap wrapper, so the round trip is
lossless by construction.

It is also why §5.2's prohibition is narrowed rather than overturned. The backend gains a
tab **row**; it gains no tab **address**. Every backend→renderer address remains a
`sessionId` the renderer resolves, for the three reasons §5.2 gives — a tab holds several
panes, so "the tab that spoke" is still not well defined, and the renderer still knows
where a session is.

### 4.5 What a tab stores, and what it only shows

| Stored on the tab                                                               | Computed from its panes                                    |
| ------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| colour, name, position, pinned, layout direction, `workspace_id`, the seen-mark | the activity indicator, the attention indicator, the label |

The split is AD-8 applied to a decoration. **Attention arrives at a pane** — a command
failed, a worker asked a question — so storing it on the tab too gives one fact two owners,
and they diverge the moment a pane is dragged elsewhere. What is genuinely the tab's own is
**"I have seen this"**, which duplicates nothing.

The label is derived the same way and for the same reason §4.2 of the workspaces design
gives for the default workspace never acquiring a name: a tab created by a **drag** was
never named by anybody, so demanding a name asks for something the user did not give. Its
identity is its panes, so their titles are its label — `nocx · srv-01 · claude`, elided to
the available width. A name the user **does** type is stored and wins.

**And a pane's own title has three sources, in this order** (`nocx-n8n82`):

```
the title the program declared (OSC 0/2)
  || the command running in the foreground
  || the pane's cwd
```

The middle one is missing today, which is why a tab running `herdr` is labelled
`Users/<user>`: nothing sits between a program that declares a title and a directory that
declares nothing. The renderer already knows the answer — `command-ledger.ts` holds the
command text and a `running` status, put there by the shell integration — so this is a
source to connect, not one to build. It is written here because it is the same rule the
paragraph above states for a tab: **the label flows pane → tab**, and a tab can only be
named by its panes if a pane is named by what is in it.

**`workspaceId` moves here from the session** (`nocx-fraus`). The invariant is unchanged —
never null, one owner of the default — and the argument in §5.2 for keeping it in the
backend survives whole, because the backend now owns the whole chain and resolves
pane → tab → workspace itself.

## 5. The pane

The pane is the durable identity, and everything else about it follows from that.

It holds the **cwd**, the **kind** (local or ssh), the **endpoint** where it applies, its
**blocks**, and — while the application is running — one **session**, the live pipe.

**A pane and its session are two objects because D5 says so.** The process dies with the
backend; the pane does not. Merging them would mean either resurrecting a process, which
D5 forbids, or losing the blocks, which is the feature. So:

```
pane id      minted by the frontend, lives forever   ← blocks are found by it after a restart
session id   minted by the backend, new every time   ← the pipe, dies with the backend
```

**Panes do not nest.** A tab shows a flat set of them. Nesting buys asymmetric geometry —
"B on the left, C and D stacked on the right" — and the purpose here is watching several
things at once, which a row or a grid serves. The cost is stated rather than hidden: no
asymmetric layouts, ever, until this decision is revisited deliberately.

**Size is a property of the member, direction a property of the set.** Each pane stores its
own share; the tab stores the direction, which is why the tab needed a row and the display
group did not.

## 6. Blocks

### 6.1 A block belongs to a pane and remembers its session

```
block.pane_id      the anchor. Durable, and what makes restore possible
block.session_id   provenance. Which pipe it ran in; null after that pipe is gone
```

Both edges are needed and neither can do the other's work. Anchoring only on the session
loses every block the moment the backend restarts — which is what `entries.session_id
REFERENCES sessions(id) ON DELETE SET NULL` does today. Anchoring only on the pane loses
the answer to "where did this run", which matters the moment a pane has been through an
inline `ssh`.

The UX argument decides which is the parent, and it is the owner's: **a user works in a
tab, so they expect to see what they did there** — not the output of a session they never
thought about. The session is a fact about a block, not its home.

### 6.2 `command_history` is legacy and is replaced, not extended

Two tables hold "a command and what it printed", in one encrypted file:

- `command_history` — the **live** path today. Command, cwd, host, status, exit code,
  timestamps, and redaction metadata about the **command text**. It stores **no output at
  all**.
- `entries` + `edges` + `executions` + `artifacts` + `artifact_chunks` — the ledger, schema
  v1, ADR-0019/0020. Richer, designed for exactly this, and **test-only**: its own package
  says the `ledger.*` wire methods "will drive this surface; until that cutover its only
  callers are tests".

ADR-0019 §4 already forbids both being written. So this is a cutover, not a new table:
`command_history` dies, and the ledger becomes the live path (`nocx-rtg0.3`). **Creating a
third table named `blocks` would give one fact three owners** — the naming question is
whether `entries` is renamed, and that is cosmetic beside the cutover.

### 6.3 Output is retained, and ADR-0019 already decided it

ADR-0008 says "output bytes NOT retained by default (secrets)". ADR-0019 supersedes it, and
the schema was built to the later decision, not the earlier one: `artifacts` carries
`media_type` including `application/vt`, a `capture_method` of `terminal-cells` /
`raw-output` / `serialized-html`, the `terminal_cols`/`terminal_rows` the capture was taken
at, `stream` and byte offsets for provenance, `truncated` and `gaps` for what retention ate,
`pinned` for eviction exemption, and `artifact_chunks` whose comment reads "append-only;
never one BLOB".

**Nothing here re-decides that.** ADR-0008's line is recorded as stale so the next reader
does not treat it as binding, exactly as the workspaces design had to do for its own §4.2.

## 7. Who issues which id

| Object    | Minted by          | Why                                                                                                                                 |
| --------- | ------------------ | ----------------------------------------------------------------------------------------------------------------------------------- |
| workspace | frontend, UUIDv7   | Already the convention: `workspaces.id` is "client-minted UUIDv7"                                                                   |
| tab       | frontend, UUIDv7   | Same, and the frontend is what asks for it                                                                                          |
| pane      | frontend, UUIDv7   | Same. The id must survive a restart, so it cannot come from a backend instance                                                      |
| block     | frontend, UUIDv7   | Already the convention: `entries.id` is "client-minted UUIDv7: UNTRUSTED idempotency key"                                           |
| session   | **backend** (AD-7) | It is embedded in the remote launcher as `NOCX_SESSION_ID` **before the connection exists** — the renderer cannot supply it in time |

**A frontend-minted id is UNTRUSTED input**, and the word is already in the schema. Three
consequences, none optional:

1. The shape is validated, never believed.
2. An insert on an existing id **fails**; it never overwrites. A repeat of the _same_
   request returns the _same_ object, which is what `entries.client` and `entries.digest`
   are for — the key is bound to who sent it and to what they asked.
3. **Knowing an id never confers the right to use it.** UUIDv7 embeds a timestamp and is
   guessable by construction, so the fence epic must not treat possession of an id as
   evidence of anything.

The retry case is not hypothetical: AD-9 exists because the socket drops. A create whose
answer was lost is retried, and without the key that retry produces a second tab.

## 8. Restore

**What comes back:** the workspaces, their tabs with their decoration and layout, their
panes with their cwd, and the blocks with their output. **What does not:** the process. D5
is unchanged, and ADR-0019 requires the difference to be visible — "live resumption cannot
be synthesized from blocks, and **nothing in the UI may imply it**".

Restore behaviour is decided by the pane's kind, not by a dialog:

- **A local pane** opens its blocks and starts **a fresh shell in the same cwd**. There is
  no question to ask: locally a shell can always be started, and the only failure is a cwd
  that no longer exists, which falls back to home.
- **An ssh pane** opens its blocks and **attempts to reconnect**. A sealed vault raises the
  unlock automatically — the mechanism exists — and `nocx-o9jdu` is the defect that must be
  fixed first, because eight panes reconnecting at once ask eight times today and seven of
  those are already answered by the first.
- **An inline `ssh`** — one entered inside a local pane — is treated as **exited**. The
  pane returns as its local shell, and the blocks that ran on the far host keep saying so
  through §6.1's provenance. This case works **only** because that edge exists; without it
  the restored pane would have to either lie about where those blocks ran or drop them.

**A restored pane may have a hole, and it must say so.** ADR-0019 §7 requires reconstruction
to state its own horizon: retention evicts artifacts, and a block whose output is gone
renders as a block with an honest marker, never as a block that printed nothing.

## 9. Grouping axes

Project, host, worktree and branch are **ways of drawing the flat list**, not objects. This
ships §8 question 5 of the workspaces design and keeps §6 exactly as written: a branch is a
label derived from the pane's cwd, and no repository registry outliving panes is introduced.

The accepted cost is unchanged and still stated: a worktree in a repository with no open
pane is not visible anywhere.

## 10. The window

**A window is a viewport, not a container.** It shows one workspace at a time — that is what
the chip and its switcher already are — and it owns no tabs.

This is not a preference. If a window contained tabs, a tab would sit in a window **and** in
a workspace, and the two would disagree: a workspace spread over two windows, a window
showing two workspaces. One set, two owners, which is the defect this design spends most of
its length avoiding.

Multi-window therefore changes **no object and no table**. It is blocked only by the shell:
Wails v2 has no window-creation call at all. That move is `nocx-mgbjx`, and it is
independent of everything here.

## 11. What this withdraws, and what it obliges

| Document                     | What it said                                             | What now                                                                                          |
| ---------------------------- | -------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| workspaces-ux **§4.4**       | Tabs live in the renderer; the invariant has two owners  | **Withdrawn.** Both ends are in the backend (§4.1)                                                |
| workspaces-ux **§5.2**       | The backend must not gain a tab id                       | **Narrowed** to addressing, which is what its reasons argue (§4.4)                                |
| workspaces-ux **§8 q. 5**    | Switchable grouping is deliberately not in this cut      | **Ships** (§9)                                                                                    |
| **ADR-0008**                 | Output bytes are not retained; blocks anchor on IMarkers | **Both stale.** The first was superseded by ADR-0019; the second cannot survive a restart (§6.3)  |
| **`nocx-fraus`**             | `workspaceId` is a field on the session                  | **Moves to the tab**; on the session it becomes derived (§4.5)                                    |
| **`nocx-49d4`**              | The ledger's session row is the restore key              | It is the **pane**, and this is the rename that makes its name honest                             |
| **`nocx-jv3q`** and children | The vertical strip groups tabs                           | Must be re-read against this model before it is taken — its "tab" is this document's **pane**     |
| **The whole tree**           | "Tab" means the thing holding a shell                    | **Renamed to pane.** `tabs.ts`, the `tab.close` contract, `TabPlacement`, `nocx-wyp3p` and others |

The rename is the largest mechanical consequence and the owner accepted it deliberately:
doing it before the word reaches a database schema and a wire contract is far cheaper than
after, and this design puts it in both.

## 12. Open questions

1. **Whether `entries` is renamed to `blocks`.** Cosmetic beside the cutover, but it should
   be decided once rather than drifting.
2. **What the seen-mark restores to.** A tab that had unseen activity when the app closed —
   is it still unseen on the other side?
3. **How eviction and restore meet in the UI.** ADR-0019 §7 requires the horizon to be
   visible; what it looks like is undesigned.
4. ~~**The order of the cutover and this model.**~~ **Answered 2026-08-17, §14.**
5. **Whether a pane can be dragged between workspaces while a subtree move is in flight.**
   §4.4 of the workspaces design left the atomicity model undesigned and required a partial
   move to fail closed; that requirement is inherited unchanged.

## 13. Acceptance criteria

> A user opens four tabs, drags one onto another so both are visible at once, drags it back
> out and finds it whole — same blocks, same history, same live shell. They quit the
> application and reopen it: the workspaces, the tabs with their colours and names, the
> panes with their directories, and the blocks with their output are all where they were.
> The local panes have fresh shells; the ssh panes have reconnected or say why they have
> not; a pane that had an inline `ssh` is back on its local shell, and the blocks that ran
> on the far host still say where they ran. No restored block is presented as live, and no
> surface describes a workspace, a tab or a pane as safe, isolated or contained.

Out of scope: any authority behaviour, the fence, multi-window, asymmetric pane layouts,
nested panes, and any repository registry outliving a pane.

## 14. The order, and why (answers §12 question 4)

Five epics, and the sequence is forced by which of them share files rather than by which
feels more important.

| #   | Epic                           | What it is                                                           |
| --- | ------------------------------ | -------------------------------------------------------------------- |
| 1   | `nocx-ehkvy`                   | the rename: the tree calls a pane a pane                             |
| 2   | `nocx-rtg0.3` + `nocx-rtg0.19` | the cutover: `ledger.*` on the wire, then `command_history` **dies** |
| 3   | `nocx-isoph`                   | the backend owns workspace → tab → pane                              |
| 4   | `nocx-8m2x6`                   | drag: two panes in one tab, and back out                             |
| 5   | `nocx-l21ib`                   | restore                                                              |

**The rename is first because it is cheapest first.** §11 already says so; what is added
here is that 2 and 3 are exactly the two steps that put the word into a schema and a wire
contract, so anything after them pays a migration to move it.

**The cutover comes before the model**, which is the question §12 asked. Both edit the same
store, so they cannot run together, and the direction is decided by what the alternative
would cost: blocks anchor on `pane_id`, `command_history` has no such column and stores no
output at all, so writing blocks there first and moving them afterwards is precisely the
compatibility shim AGENTS.md forbids in a greenfield tree. Nothing runs on `command_history`
in between — it dies in step 2 and the ledger is live from that commit.

**Drag and restore both come after the model** for the same one-owner reason: both are
operations _on_ the chain, and building either against a renderer-owned tab would give the
invariant two owners again — the defect §4.1 exists to close.

`nocx-rtg0.19` was filed on 2026-08-17 because it did not exist: twenty-one beads under
`nocx-rtg0` and not one of them deleted the table. The decision lived in §6.2 and in a
coordinator's head, which is how AGENTS.md says an intention evaporates between rounds.
Nothing is converted — no migration, on the owner's confirmation the same day — so the rows
in an existing file go with the table, and the commit has to say that in words rather than
leave it inferred from an absent function.

**Only the 1 → 3 edge is recorded in the backlog as a dependency.** 2 → 3 and 3 → 4/5 are
sequencing the coordinator holds, not blocking edges: `bd` edges mean "these touch the same
code", and inflating them is what once made `bd ready` unusable at 68 issues.
