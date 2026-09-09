# Level 2 — the server on the host

## 0. Status

Companion to `2026-08-31-level-1-the-helper-owns-the-host-design.md`, written after the same
architecture turn. Level 1 is assumed throughout: the helper owns the PTY, mints a
generation-qualified `hostSessionId`, serves an inventory, and holds no human-authored name.
This document owns everything durable and human that lives on the host.

It replaces the "document 2" the earlier execution-host draft reserved (its D15), and the
question that draft deferred — "one workspace from two machines" — is answered here rather
than postponed again.

Backlog: `nocx-lmb6v` → `nocx-6ojko` → `nocx-afgkj` → `nocx-0h9ib`, all labelled
`remote-host`. Level 2 does not start until level 1's `nocx-wrugm` is done, and
`nocx-lmb6v` (migrations) is a prerequisite of the first of them rather than a follow-up.

## 1. What a user can do that they could not before

**Tabs live on the host. You open one from the desktop, close the lid, open your laptop, and
the tab is there with the name you gave it and the blocks that ran in it — and so is the
program still running inside it.**

The end-to-end check is `nocx-afgkj`'s acceptance criterion.

## 2. Why this needs a server and not a bigger helper

The promise above is the whole reason, and it was reached by elimination during the turn.

The owner's test case: `herdr --remote user@host`, open some tabs, and **they stay there until
you close them yourself** — the process inside may exit, the tab does not disappear.

Two cheaper answers were tried and both fail it:

- **Derive everything from the OS.** `/proc` gives cwd and argv for free, which covers "what
  is running here" but cannot produce a name a person typed.
- **Put the name on the helper's session record.** Recommended by codex, and it fails on a
  bigger point than the name: **a nocx tab that outlives its process carries its BLOCKS.**
  Reconnect from the laptop, scroll up, and the command history of that tab must be there.
  An opaque label on a live session record cannot carry `entries` and `artifacts`, and when
  the process exits the record is gone anyway.

So the durable remote object is the pane and tab plus their ledger rows. That needs a store,
a schema and a writer — which is a server.

**And this is not new semantics invented for the remote case.** `internal/content/sqlite.go`
already says it: `entries.pane_id` is "the ANCHOR: durable, frontend-minted, and what makes
restore possible", `entries.session_id` is "PROVENANCE … null once that pipe is gone", and in
as many words, "the session is a fact ABOUT a block, not its home". Locally a tab already
outlives its session. Level 2 is that same rule on a host — which is the owner's original
requirement (one shape everywhere), not an escalation of it.

## 3. Shape

```
your machine                          the host
┌──────────────┐                     ┌─────────────────────────────┐
│ UI           │                     │ nocx-server                 │
│  ↕ WebSocket │                     │  content.db (its own)       │
│ nocx-server  │ ── carrier ───────► │  workspaces, tabs, panes    │
│  vault       │   (ssh or other)    │  blocks, artifacts, names   │
│  content.db  │                     │           ↕                 │
│  helper      │                     │  helper (generations)       │
└──────────────┘                     │   PTYs, git, fs, completion │
                                     └─────────────────────────────┘
   a browser ────── carrier ─────────────────► (same server)
```

**Roles are per connection, never per installation.** The server you sit at holds the vault,
initiates, and is where consent is granted. The server you connect to holds sessions, layout
and its own ledger, and initiates nothing. Your desktop is the second role in somebody else's
connection. This answers the owner's unease about asymmetry: it is not that one install is
lesser, it is that a connection has two ends.

**A responder must be constrained, not merely well-behaved.** Codex's correction, taken:
"initiates nothing" is a promise made inside a process that perfectly well could. A responder
connection receives an explicit capability set that excludes vault reads and outbound
credential use, so the property is enforced rather than asserted.

**The UI still speaks to exactly one backend.** The desktop app to its local server; a browser
to the server on that host. Federation happens server-to-server, below the UI. This is why
`nocx-if6`'s "multi-backend frontend seam" was closed rather than rewritten.

## 4. What this crosses

**ADR-0019 — one authoritative ledger.** Not violated. The ADR forbids a second store BESIDE
the ledger ("authorship is a column, not a second store"); one ledger per server keeps exactly
one authority per event. What it does require, and what this document owes: explicit routing
so every event has one home, federated-query semantics with partial failure, and a prohibition
on the local server keeping fallback copies of a remote ledger's rows.

**ADR-0018 — the ContentDB key.** Unchanged in mechanism, changed in exposure (§8).

**ADR-0034 — consent belongs to the machine.** Extended, deliberately: level 2's footprint is
materially larger than level 1's, so it gets its own grant (§7).

**ADR-0043 — one connection to the encrypted store.** Each server has its own store and its
own single connection. The ADR's open question about two backends against one file still does
not arise.

**ADR-0048 — UI state is a document, not a setting.** Extended: it already makes window state
and active tab per-machine, and this adds "which remote tabs this window has open" to that
same local projection.

**AD-1** — the binary plane rides the carrier unchanged (level 1 §5). End-to-end AD-10 credit
must run helper → host server → local server → UI. It must not be re-wrapped in JSON-RPC at any
hop.

## 5. Decisions

### D1 — Migrations are a prerequisite, not a follow-up

`resetIfSchemaChanged` rebuilds `content.db` on any schema change and loses the rows by design.
That is correct while the database is local and disposable: lose it, reopen your tabs. It stops
being correct the moment the host's database holds tabs, names and blocks that are the ONLY copy
and are shared between your machines — there is nowhere to restore them from, and a schema bump
ships with an ordinary update.

The owner confirmed on 2026-08-31 that the reset is a phase and migrations will exist. So they
land before level 2, and `nocx-6ojko` depends on `nocx-lmb6v`.

```
onDisk == current  → open
onDisk <  current  → one explicit ordered migration per edge, crash-safe,
                     user_version updated only after that edge commits
onDisk >  current  → REFUSE, without modifying a byte
```

An older binary's only legal answer to a newer schema is a visible refusal; it never rolls back,
because migrations are one-way. The current code violates the third rule today — any inequality
enters the rebuild, and the unknown-table gate catches only unfamiliar TABLE NAMES, so a newer
schema that changed COLUMNS inside familiar tables is destructively rebuilt. That is `nocx-7qunp`
and it does not wait on the migration chain: refusing upward is correct with or without it.

### D2 — Wire compatibility is declared separately from the schema

A peer never opens the remote database, so a peer floor must not be encoded in `user_version`.
The handshake declares them apart:

```json
{
  "buildGeneration": 142,
  "controlProtocol": { "current": 7, "minPeer": 6 },
  "dataProtocol": { "current": 2, "minPeer": 2 },
  "storageSchema": { "current": 18, "minMigratable": 1 }
}
```

- Protocol ranges must intersect, or the connection is **visibly refused** with the version
  needed.
- A peer does not satisfy the remote SCHEMA; only the remote binary does.
- An older client never deploys its server over a newer remote generation.
- A newer client may install and activate a newer remote server only after checking it can
  migrate the on-disk schema.
- If the newer remote protocol no longer accepts an older client, the client is refused and
  neither the remote server nor its database is downgraded.
- `buildGeneration` is a signed monotonic release generation — never semantic-version string
  ordering, never a commit timestamp.

### D3 — What is shared and what is per machine

| shared, on the host                | per client / per machine            |
| ---------------------------------- | ----------------------------------- |
| workspace / tab / pane membership  | which remote tabs this window shows |
| open / closed durable state        | the active tab                      |
| tab name, colour, pinned           | window size and position            |
| tab order                          | sidebar and local chrome            |
| split topology and size shares     | local pixel allocation and zoom     |
| blocks, artifacts, cwd, lineage    | scroll position and selection       |
| the pane → `hostSessionId` binding | observer or controller status       |

Name and order are shared because the repo already decided it: `layout.go` takes ordering and
decoration from the backend snapshot, and `ReorderTabs` writes the whole strip permutation.
Making them per-client would reverse a live decision, not add one.

### D4 — The name is a ladder of separate fields, not one field

Taken from orca deliberately, because it answers a question we had not asked: what does a tab
show before anyone renames it?

```
1. the name the user typed         (customTitle)
2. a quick-command label
3. an OSC title from the program
4. a generated name
5. the live title
6. "Terminal N"
```

Separate columns, resolved at render, first non-empty wins — and **auto-generation refuses to
run once a user-set name exists**. That is what lets the helper's derived diagnostics (cwd,
argv, foreground process) arrive as lower rungs without ever colliding with a name a person
gave. One field with a heuristic cannot do this: it has to guess whether what it holds was
authored or inferred.

### D5 — Mutation is optimistic, and last-write-wins is not enough

One monotonically increasing `workspace.revision`, incremented transactionally by every shared
layout mutation:

```
tabs.rename(expectedRevision, mutationId, …)
tabs.reorder(expectedRevision, mutationId, …)
tabs.close(expectedRevision, mutationId, …)
panes.move(expectedRevision, mutationId, …)
```

- Matching revision → apply, return the new revision.
- Stale revision → `Conflict` with the current revision and either the affected rows or a
  resnapshot token. Never a silent overwrite.
- `mutationId` is bound to client identity and payload, so a retry after a lost response is
  idempotent.
- The server broadcasts `layout.changed{workspaceId, revision}`; a client that missed a
  revision **resnapshots** rather than applying deltas across a gap.

One coarse revision will produce harmless conflicts between unrelated renames. That is
accepted: it is far easier to prove correct, and orca reached the same coarseness for its
remote workspace sessions (`baseRevision` / stale-revision / conflict phase). Split it into
structural and per-tab revisions only after contention is measured.

**Orca's web client is the counter-example this decision exists against**: it cannot rename at
all — `setTabProps` omits title, the rename push is a no-op — so a rename in the browser stays
in that browser's `localStorage`. That is sidestepping the problem, and it is exactly the
failure `nocx-afgkj`'s acceptance criterion tests for.

### D6 — Three verbs for closing, because one is a lie

| verb                             | scope               | effect                                                                                                              |
| -------------------------------- | ------------------- | ------------------------------------------------------------------------------------------------------------------- |
| **detach from this device**      | local               | ordinary Cmd-W. Removes this client's attachment and local projection. No shared mutation, no signal, no PTY close. |
| **close tab on host**            | shared              | marks the durable tab and its panes closed, for every client. Requires a current workspace revision.                |
| **end processes and close tab…** | shared, destructive | lists the live processes, requires confirmation, then does both.                                                    |

**`close tab on host` REFUSES while any pane holds a live helper session**, unless the user
picks the third action. A tab cannot simultaneously cease to exist and remain the durable
container through which its live process is recoverable. This is the minimum model that
satisfies both "Cmd-W never loses work" and the owner's "the tab stays until I close it".

If another client is attached, closing on the host requires a snapshot-bound confirmation
naming those attachments; a stale token is refused if a new attachment or process appeared
meanwhile. On commit the host broadcasts the close and every client detaches.

### D7 — One controller, many observers, and the resize is ordered against the output

Level 1 froze the mechanics (its D8). The product rule:

- Any number of **observers** may read.
- Exactly one **controller** lease owns input, resize, signals and foreground-process actions.
- Observers never resize the PTY. They render the controller's canonical grid, fitting or
  letterboxing locally.
- **Take Control is explicit.** Normal transfer asks and releases; a forced takeover requires
  confirmation and notifies the previous controller. A second writer is refused, never
  silently promoted.
- Connection loss releases the lease after a bounded departure window.
- Every input and resize frame carries the lease epoch; a delayed frame from the old controller
  is rejected.
- Transfer order: revoke the old epoch → pause input → apply the new controller's geometry →
  publish the new effective size and epoch → accept input.
- **Resize is published ordered against the output**, as `{effectiveAtOffset, cols, rows}`.
  Without that an observer feeds bytes into xterm at the wrong geometry during a transfer and
  reconstructs a screen that never existed.
- Each observer has independent delivery credit. A stalled observer is reset to the window base;
  it can never throttle the running PTY or the controller.

**Open conflict, and it is the owner's to settle:** `nocx-eidfb` is in progress at 3 of 4
children and currently says "the client that attached last defines the size, and the other
letterboxes" — a silent displacement. That contradicts the explicit lease above. Either that
epic is amended or this decision is, before `nocx-afgkj` starts.

### D8 — Where the block bodies come from when nobody is watching

The hole codex found, and it touches AD-6. Block artifacts are produced by the RENDERER, which
freezes a block and serialises its cells. A durable remote tab with no client attached has no
renderer — so what fills its blocks?

**The raw session recording is the reconstruction source, and it already exists for exactly this
reason.** `internal/content/session_output.go`'s header says so in as many words: it is written
by the backend on its own read path "rather than by the renderer at freeze time … because a
session with no client attached has no renderer to freeze". It is wired and proven
(`-whylive`: `main → App.Run → … → handleOpen → pumpToRing → recordSessionOutput → Append`).

What is owed is the PATH: from authenticated command boundaries plus recorded raw offsets to
durable block artifacts. AD-6 is not weakened — the backend still does not interpret the stream;
it stores byte ranges and the lifecycle protocol supplies the boundaries. Without that path the
remote ledger can hold entries with no scrollback body, which is a block that renders as nothing.

### D9 — Adoption belongs to the ledger, and the helper is untouched by it

When level 2 is installed on a host that already ran level 1, the server inventories the existing
`hostSessionId`s and creates pane/session bindings for the ones the user adopts. No helper-side
rename, no workspace mutation, no ownership transfer — which is another reason the helper never
held a human name (level 1 D3).

### D10 — The browser is the same server, and authentication is the actual work

`cmd/nocx-server` already runs the shipped coordinator headless and already speaks the ordinary
WebSocket to a browser-hosted frontend: the whole Playwright suite stands it up beside vite and
drives it in CI. So the transport and the frontend are exercised against a real browser today.
What is missing is that the server does not serve the built assets itself — and that is the
small half.

**The large half is authenticating a person.** Reaching the helper is same-UID trust, which is
precisely what a browser is not. The server authenticates the user and then, as a trusted
process under that account, reaches the helper. That boundary is the epic (`nocx-0h9ib`), and
public exposure — TLS, a hostname, a reverse proxy — is deliberately out of it.

## 6. Encryption on a host you may not own

Stated plainly rather than implied. The remote `content.db` is encrypted with a key derived on
that host: an OS keystore slot where one exists, otherwise machine-id plus a random salt kept in
the config directory. On a headless VM it is the second branch.

`internal/contentkey`'s own comment defines the threat model, and it is narrow on purpose: "a
copy of the FILE must not be readable as it stands … Not a live attacker, not other processes
running as this user — that boundary is not defensible on a desktop." So on the host, encryption
defeats a detached copy — a backup, a synced folder, a pulled disk — and nothing else. It does
not defeat the same Unix user, a full VM snapshot, or usually a whole-account backup.

**In one sentence: your ledger on that host is protected about as well as `~/.bash_history` on
it.** For a machine you own, that is honest and sufficient. If it ever stops being sufficient,
the stronger option is available and costed: the key arrives from the client at connect time and
never rests on the remote disk, leaving the ledger sealed until its owner connects — at the cost
that a sealed server records nothing, and with little lost, since a reboot ends the sessions
anyway. Not built now.

**And `internal/contentkey` stays where it is.** Moving its per-OS identity readers into the
helper was proposed during the turn and rejected: "OS-specific" does not imply
"execution-host-owned". Key-derivation stability is a storage invariant and must not vary with
which helper generation answers first. If the server needs to be OS-neutral here, it takes a
narrow host-facts interface; the fallback choice, the HKDF inputs and the stability tests stay
in `contentkey`.

## 7. Consent, and why it is not inherited

ADR-0034 keys consent to machine identity by host-key fingerprint, and it was written about the
HELPER's footprint. A full server is materially different: it creates an encrypted database on
that host, it may expose a web endpoint, and it holds user-authored state that is the only copy.

So the stored grant distinguishes **helper execution** from **full host service**, and granting
the first never implies the second. "Deploy the server" is itself the explicit consent act.

The reverse edge matters as much: helper uninstall is all-or-nothing for HELPER GENERATIONS and
**must never silently define deletion of a level-2 `content.db`**. Removing the server is its own
act, and it names the durable state it will delete before it runs.

## 8. Assertions

1. **Tabs are on the host**: with the desktop closed entirely, a second machine connects and sees
   the tabs, their user-given names and their blocks.
2. **A rename propagates**: renamed on machine B, machine A shows the new name without a
   reconnect.
3. **A rename does not lose a race silently**: two clients renaming from the same revision — one
   applies, the other receives `Conflict` with the current revision, and no write is lost without
   the loser being told.
4. **A missed revision resnapshots**: a client that misses a broadcast does not apply a delta
   over the gap.
5. **The name ladder holds**: a user-set name is never overwritten by a generated one; clearing
   it falls back down the ladder rather than to empty.
6. **Cmd-W does not mutate shared state**: detaching on machine B leaves machine A's tab and the
   host's row untouched.
7. **Closing on the host refuses a live process** unless the destructive verb is chosen, and that
   verb names every process first.
8. **One controller**: a second write-capable attachment is refused; a stale-epoch input frame is
   rejected; a takeover notifies the previous controller.
9. **Resize is ordered against output**: during a controller transfer no observer renders bytes
   at a geometry they were not produced under.
10. **A stalled observer is reset, not obeyed**: the PTY's rate is unaffected.
11. **Directional schema refusal**: an older binary opening a newer database refuses and modifies
    nothing; a newer binary migrates an older one and the rows survive; a migration killed midway
    leaves the old file intact.
12. **Wire floor is separate**: an older client whose control protocol is below the host's
    `minPeer` is refused with the version it needs, and neither the remote server nor its database
    is downgraded.
13. **Consent is two entries**: granting the helper does not grant the server; removing the helper
    leaves `content.db`; removing the server names what it deletes.
14. **A block has a body without a renderer**: a command that ran with no client attached has
    readable output in its block afterwards.
15. **Adoption is ledger-side**: installing the server on a host already running level 1 adopts
    existing sessions into panes without the helper being renamed or restarted.
16. **The browser is the same session**: bytes typed in the browser appear in the desktop app
    attached to the same host, and closing the browser ends nothing.
17. **And the paired positive**: on an ordinary host, deploy the server, open three tabs, name
    them, close the laptop, open the desktop — three tabs, three names, three block histories, and
    whatever was running is still running.

## 9. Deliberately out of scope

- **Public deployment** of the web endpoint: TLS termination, a hostname, a reverse proxy. This is
  reachable-on-your-own-network, and says so in the product.
- **Chaining** — reaching a third host through the host's own connections. Either a free jump host
  or your credentials on somebody else's machine; not decided here, and until it is, a responder's
  capability set excludes outbound credential use (§3).
- **Recall across servers.** Federated query is named as an obligation (§4) but its partial-failure
  UX is its own work.
- **The retention age** for anything, here or in level 1.
- **The word `relay`** in code — 411 occurrences including type names; its own chore, done
  since as `nocx-0xq2s`.

## 10. Open questions

1. **`nocx-eidfb` versus D7.** The live epic silently displaces the writer; this document requires
   an explicit lease. Settle before `nocx-afgkj` starts.
2. **Does the local server keep any projection of a remote ledger**, and if so how is ADR-0019's
   "one home per event" enforced against it?
3. **What the tab strip shows for a host that is unreachable right now** — the tabs are known from
   the last snapshot, the sessions are not. Neither "running" nor "gone" is true.
