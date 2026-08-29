# The feed groups what belongs together — implementation plan

**Goal:** A user expands a collapsed row to see the individual occurrences inside it, each
with its own timestamp and unread mark, and narrows a busy feed to one host, one session or
one kind.

**Epic:** `nocx-ctl6q`. **Depends on:** `nocx-p0xhg` (epic 1 — the feed, ingress, the wire,
the bell), which is landed. **Spec:** `.internal/specs/2026-08-22-notification-centre-design.md`
§7 and §9.

## The decisions this epic was blocked on

The epic could not start without them, and they are taken here rather than inside a task,
because they are what the tasks are shaped by.

### D1 — Occurrences are point-in-time. There is no firing/resolved lifecycle.

Alertmanager-style grouping assumes a state with two edges and a duration between them.
nocx has classes that would fit — session disconnected/reconnected, port appeared/
disappeared, agent run started/finished — but **no source that produces a closing edge
today**. `program.notify` is an OSC write, and `session.ended` is a terminal fact with no
successor. Building the state machinery now would be a mechanism whose only caller is its
own tests, which is the `ContentDB.Add` shape AGENTS.md names.

So every occurrence stays an instant, and grouping in this epic is **structural** — by run,
by host, by session, by kind — never by lifecycle.

What a stateful class will need when a source finally produces one, written down so the
next person does not discover it by rebuilding it: a **state key** distinct from both
`CollapseKey` and `DebounceKey`, an **explicit closing edge** from the source (never
inferred from silence, which is a timeout wearing a fact's clothes), and a row that
**mutates in place** rather than opening a second one. None of those is the collapse key,
and the collapse key must not be widened to pretend otherwise. That decision gets its own
ADR at the moment a source exists, not before.

### D2 — A collapsed row retains a bounded tail of its constituents

Epic 1's collapse keeps the row's identity and throws the constituents away: `Add`
overwrites the held `Event`, raises `Count`, and returns the existing id. Expansion needs
what it discarded, and retaining _all_ of it is not available — the flood test admits 10 000
occurrences from one session, and keeping them restores exactly the unbounded growth
collapse exists to prevent (`nocx-8xh4t` is where this was found).

So a row retains its **newest `MaxRunRetained` constituents** (start at 20) and counts the
rest. A constituent carries `{ID, At, Title, ReadAt}` — `At` because an expansion whose rows
share one timestamp is not worth opening, `Title` because a run's titles differ (that is why
collapse keeps the newest one), and `ReadAt` because the criterion says each keeps its own
unread mark.

Both ends, stated: a constituent is retained from the `Add` that created it until either the
tail overflows or the row is evicted. When the tail overflows the row records how many it no
longer holds, and the expansion says so — "20 of 4310" — rather than presenting a truncation
as the whole. Constituent bytes count against `MaxRetainedBytes` like every other byte; a
bound that excludes the thing that grows is not a bound.

The read mark: `MarkAllRead` marks the row **and every constituent it still holds**. A later
join clears the row's mark, as epic 1 decided, and does **not** clear the constituents' —
they were seen, the new one was not, and that difference is the whole reason an expansion
shows individual marks.

### D3 — Narrowing is the renderer's, and the bell keeps counting everything

The snapshot is bounded at 200 rows, so a filter by host, session or kind is a view over
data the renderer already holds. Doing it on the wire would add a second place that decides
membership, which is the thing AD-8 is about, and would buy nothing.

The bell keeps showing the **global** unread count: `feed.unreadCount` is the single source
of truth for the bell and for the dock badge (design §6), and a bell that quietened itself
because you narrowed the list would be lying about what is waiting. The panel therefore
**states the narrowed count separately** — "3 of 12 shown" — which is the epic's criterion
"the unread count reflects the narrowed view or is explicitly stated not to", answered with
the second half deliberately.

### D4 — Which rows are expanded, and the active filter, are ephemeral view state

Both live in renderer signals. Not `localStorage`: ADR-0048 settled that it may not carry
facts. Not a UI-state document either — nothing has asked for an expansion to survive a
restart, and the feed itself does not survive one, so a document remembering which row of a
feed that no longer exists was open would be remembering nothing.

## Global constraints

- **AD-8:** the `Feed` remains the only holder of "what was raised". A filter is a view, and
  a view is not a second owner.
- **AGENTS.md rule 5:** the occurrence shape changes, so `contracts/notify.feed.read.schema.json`
  changes with it — `additionalProperties: false`, explicit `required`, regenerated renderer
  types, and BOTH conformance cases including the one off the real socket.
- **UI kit:** a disclosure on a row is a kit concern. Extend `RecordRow`; do not build a
  second expandable row beside it.
- **Commit format:** `<type>(<scope>): <subject> (<bead-id>)`, prose body.

---

### Task 1: the feed retains a bounded tail of each run

**Files:** modify `internal/notify/feed.go`; test `internal/notify/feed_test.go`.

**Produces:** `type RunMember struct { ID OccurrenceID; At time.Time; Title string; ReadAt *time.Time }`,
`Occurrence.Run []RunMember`, `Occurrence.RunDropped int`, `FeedLimits.MaxRunRetained int`.

**Acceptance Criteria:**

- A fresh occurrence has `Run` of length 1 — itself — so an expansion never has to special-case
  a run of one, and `Count == len(Run) + RunDropped` holds for every occurrence at all times.
- A join appends a member with its own id, instant and title, and the row's `Count` still counts
  every join including the dropped ones.
- Past `MaxRunRetained` the OLDEST member is dropped and `RunDropped` rises by one; the newest
  members are the retained ones.
- `MarkAllRead` sets `ReadAt` on the row and on every retained member; a subsequent join leaves
  those member marks alone and clears only the row's.
- Member bytes (`Title`) count against `MaxRetainedBytes`, and evicting a row releases every
  byte its members held — assert `f.bytes` returns to its prior value after an add-then-evict
  cycle, which is the drift a byte budget dies of.
- `NewFeed` rejects `MaxRunRetained < 1`.
- The flood test from epic 1 still holds with members present: 10 000 occurrences from one
  session leave the feed inside BOTH budgets and leave a pre-existing unread danger row from
  another session present.

---

### Task 2: the run reaches the renderer

**Files:** modify `contracts/notify.feed.read.schema.json`, `internal/transport/ws_notify_feed.go`,
`internal/transport/ws_notify_feed_test.go`, `internal/transport/ws_contract_test.go`;
regenerate `frontend/src/generated/notify.feed.read.ts`.

**Acceptance Criteria:**

- Each occurrence gains `run` (an array of `{id, at, title, read}`) and `runDropped` (integer,
  minimum 0). Both are `required`; `additionalProperties: false` holds on the new member object.
- `run` is built with `make`, never nil — the schema's `type: array` rejects `null`, which is
  the defect the contracts' first run caught on `vault.status`.
- A member carries no `trust`, no `level` and no `body`: the row owns severity and detail, and
  a member that could disagree with its row would be a second answer to one question.
- `npm run contracts:check` passes; the `_DTOConformsToContract` case covers a row with an empty
  tail, a full one and one with `runDropped > 0`; the `_OverTheWireConformsToContract` case takes
  the real result off the real socket.

---

### Task 3: `RecordRow` learns a disclosure

**Files:** modify `frontend/src/ui/record-row.tsx`, its CSS, `frontend/src/ui/record-row.test.tsx`,
and the `RecordRow` row of `frontend/src/ui/README.md`.

**Acceptance Criteria:**

- An optional controlled disclosure: `expandable`, `expanded`, `onToggle`. It renders a native
  button part with `aria-expanded`, operable by Enter and Space, in the same leading position and
  with the same chevron gesture `TreeRow` and `Section` already use — `data-disclosure` carries
  `expanded | collapsed | leaf` in that same vocabulary, because a third word for one concept is
  how a kit stops being one.
- A row given no disclosure props renders exactly as it does today, `data-disclosure="leaf"`,
  and reserves the disclosure's width so titles still form one column.
- Activating the disclosure does NOT activate the row: expanding is not opening, and a click that
  did both would make expansion impossible to reach with the mouse.
- The children a row discloses are the caller's, passed as a slot; the kit decides the geometry
  and never what goes inside.
- A row in the README table, and a test for each bullet above.

---

### Task 4: the panel expands a run and narrows the feed

**Files:** modify `frontend/src/notify/notifications-panel.tsx`, its CSS and test;
`frontend/src/notify/feed-store.ts` if the filter belongs beside the data it filters.

**Acceptance Criteria:**

- A row whose `count > 1` is expandable; a row of one is a leaf. Expanding lists its retained
  members newest first, each with its own timestamp and its own unread mark.
- A row with `runDropped > 0` states it inside the expansion — "20 of 4310 shown" — and never
  presents the tail as the whole.
- The panel offers narrowing by host, by session and by kind, built from the kit
  (`SegmentedControl` or `Select` — read the kit before choosing, and place, never repaint).
- Narrowing hides every row from another host, and the panel states "N of M shown". The BELL's
  badge does not change when a filter is applied — assert that, because it is D3's whole point.
- Clearing the filter restores every row, and the expansion state of a row that was open before
  the filter is still open after.
- No CSS rule in the panel's stylesheet sets a painting property on a `ui-*` class.

---

### Task 5: the acceptance check

**Files:** create `e2e/notification-centre-grouping.spec.ts`.

**Acceptance Criteria:**

- Drives the UI, never the store. Waits on observable state, never on a duration.
- One session raises several notifications in a run; the feed shows one row with a count; the
  spec expands it and reads the individual occurrences, each with its own time.
- Two hosts have raised; narrowing to one hides the other's rows, the panel states the narrowed
  count, and the bell's badge is unchanged by the narrowing.
- Runs on the headless path so `e2e/run-in-container.sh` can run it.

---

## Task dependency order

```
1 (feed run tail) ──► 2 (wire) ──► 4 (panel) ──► 5 (e2e)
                                    ▲
                      3 (RecordRow disclosure) ┘
```

Task 3 is independent of 1 and 2 and may run beside them; task 4 needs both arms.

## What this plan deliberately does not do

- No firing/resolved lifecycle (D1), and no widening of `CollapseKey` to fake one.
- No persistence of the feed, of the filter, or of which rows are expanded (D4).
- No search over the feed — narrowing is by a named axis, not by free text.
- No per-channel routing settings; that is epic 3 (`nocx-3mniv`).
