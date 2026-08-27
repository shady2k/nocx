# You decide which notifications reach you — implementation plan

**Goal:** A person opens Settings, turns the OS banner off for program notifications and
leaves the toast on, runs a program that prints OSC 9, and sees a toast and no banner.

**Epic:** `nocx-3mniv`. **Depends on:** `nocx-p0xhg` (closed) and `nocx-jiwq`, whose
remaining work is `.internal/plans/2026-08-23-notification-reach-remainder.md` — the toast
channel and OSC 9 come from there, and without them this epic's criterion cannot be
checked at all.

## What the shape of the codebase decides for us

Two things were found before writing this, and both make the epic smaller than the design
feared.

**The Settings page is generic.** It fetches `Declaration[]` from the backend and renders
whatever is declared (`frontend/src/settings.tsx:205`). So the criterion "the kinds and
channels offered are derived from what the backend actually has, not a hand-maintained list
in the renderer that can drift" is satisfied **structurally** rather than by discipline:
there is no renderer list to drift, because the renderer has never had one.

**The registry already notifies.** `settings.Registry` carries `AddNotifier(func(revision
int, keys []string))`, validates a value before storing it, and can refuse a whole batch
(`ValidateSetting`, `ReplaceNonSecretOverrides` returning a `PendingNotification`). So
"reaches the running router without a restart" needs a subscriber, not a mechanism.

## The decisions

### D1 — The table is a matrix of toggles in the existing registry, not a new document

One `Bool` per `(kind, channel)` pair, registered from the backend's own catalogue at
composition time. Five kinds and two channels is ten toggles today, and the count is bounded
by things the backend declares.

Rejected: a structured routing document. It would need its own storage, its own schema, its
own wire method, its own validation and its own Settings surface — all of which the registry
already has and has already had reviewed. A second configuration mechanism beside the one
the product uses is the "second answer to one question" AD-8 is about.

The consequence for the epic's wire criterion is worth stating rather than hiding: **this
epic adds no new JSON-RPC method**, because the settings methods already carry the matrix.
But it does not get to skip the criterion either. Of the eight settings methods only
`settings.describe` has a schema in `contracts/`; `settings.set` — the method the user's
choice actually travels on — has none. So this epic gives `settings.set` its schema, its
generated renderer type and both conformance cases. That is the criterion honoured on the
method that carries the feature, rather than honoured on a redundant method built to have
somewhere to put it. The remaining six stay with the `nocx-bt3w` sweep.

### D2 — Default-deny survives, and it is what the DEFAULT of each toggle encodes

A `(kind, trust)` pair reaches a sink only where a row says so (ADR-0029 §3). With
user-authored rows the same rule holds mechanically: the table is built from the toggles
that are ON, so a kind whose toggles are all off reaches nothing, and a kind nobody
declared has no toggle to turn on.

The shipped default is exactly today's single row — `program.notify` at `programRequest` to
the banner — so an existing user's behaviour does not change the day this lands.

### D3 — The trust-capability bound is re-checked on every swap, and a refused table never becomes live

`NewRouter` refuses a table whose `TrustHeuristic` row reaches a sink with
`LeavesMachine() == true` (`ErrTrustCapability`, ADR-0029 §3). That check exists because
trust classes are a hard capability bound and it is a **security control**.

With a user-authored table it must run on **every** rebuild, not once at construction. A
table that fails validation is refused **whole**: the previous table stays live, the user is
told, and nothing is partially applied. A partially applied routing table is worse than a
refused one — it silently grants a route the person did not choose.

This epic does not make that check configurable, and no setting may express a
heuristic-to-network row. The catalogue simply does not offer the pair.

### D4 — The router's table becomes swappable, and the interval is named

ADR-0029 §2.3 says routing is resolved once, in the router, before any sink is invoked. That
is about **when** resolution happens relative to delivery, and a swappable table does not
touch it — but only if the swap is atomic with respect to a raise.

So: a raise resolves against exactly one table, the one live when it began; a swap takes
effect for raises that begin after it. No raise ever sees half of two tables. State that as
the interval, in the code, with both ends.

### D5 — The Policy does not move, and `Disposition` keeps its meaning

The design's epic-3 sketch had the policy move from "in front of the router" to a routing
stage, with `Disposition` changing meaning from "dropped" to "which channels fired", and
priced `internal/notify/policy_test.go` — 1144 lines — as rewritten rather than renamed.

**None of the epic's criteria need it, and two things that have landed since make it the
wrong move.** Epic 1 already split membership from delivery: the feed remembers everything,
so a suppressed event is no longer lost and `Disposition` is no longer destructive. And
"which channels fired" is already answerable — `Outcome.Results` carries a per-route result,
which is exactly what the failure row (`nocx-r6pxp`) now reads.

What the policy governs is **when** — the debounce window and focus suppression — and that
is orthogonal to **where**, which is the table's. Rewriting 1144 lines of tests to satisfy
no criterion would be work whose only outcome is risk.

If a real need appears later — "suppress the toast while I am looking at the tab but still
send the banner" — that is per-channel policy, it is a feature nobody has asked for, and it
gets its own bead with its own criterion.

---

### Task 1: the catalogue of what can be routed where

**Files:** create `internal/notify/catalogue.go` + test.

**Acceptance Criteria:**

- One place names every routable `Kind` with a stable id and a human label, and every
  channel (sink) with the same, plus which trust classes each kind can carry.
- The catalogue is what the settings registration and the table builder both read; neither
  restates a kind or a channel as a literal.
- A pair the trust bound forbids (`heuristic` to anything leaving the machine) is not
  offered by the catalogue at all — the impossible choice is absent rather than refused.
- A test asserts the catalogue lists every `Kind` declared in `notify.go`, so a kind added
  later cannot be silently unroutable.

### Task 2: the table is built from settings and swapped into the live router

**Files:** modify `internal/notify/notify.go` (the swap), create
`internal/notify/routingsource.go` + tests, modify `internal/app/app.go`.

**Acceptance Criteria:**

- One `Bool` per `(kind, channel)` is registered from the catalogue, in a Notifications
  settings section, with today's single row as the only default that is on.
- The router accepts a validated table atomically: a raise resolves against exactly one
  table, and a swap takes effect only for raises that begin after it. Test it under `-race`
  with raises in flight during a swap.
- Every rebuild re-runs the trust-capability validation. A table that fails is refused
  whole; the previous table stays live; the failure is visible to the user, not only in a
  log.
- Turning every toggle of a kind off makes that kind reach nothing.
- A settings change reaches the router without a restart, through the registry's notifier.
- `contracts/settings.set.schema.json` exists with `additionalProperties: false` and an
  explicit `required`, the renderer's type is generated from it, and both the
  `_DTOConformsToContract` and the `_OverTheWireConformsToContract` cases exist — the second
  taking the real result off the real socket.

### Task 3: the debounce window is a bounded, live setting

**Files:** modify `internal/app/app.go`, `internal/notify/policy.go` if the window must
become readable at delivery time rather than fixed at construction; tests.

**Acceptance Criteria:**

- A `Number` setting with `Min`/`Max`, defaulting to today's constant, in the same section.
- The stored value governs the live pipeline without a restart, and the interval is named:
  a window already open keeps the length it opened with; the next one uses the new value.
  (The alternative — retiming an open window — is a decision, so make it deliberately and
  say which you chose and why.)
- A value outside the bounds is refused by the registry's own validation, and the test
  asserts the refusal rather than assuming it.

### Task 4: the Settings surface reads as a matrix, not ten unrelated toggles

**Files:** `frontend/src/settings.tsx` or the section renderer, plus tests.

**Acceptance Criteria:**

- The notification routing toggles read as kind × channel, not as ten sentences in a list.
  Build it from the kit; place, never repaint.
- Nothing in the renderer enumerates kinds or channels: whatever the backend declares is
  what appears. Assert it by adding a fake declaration in a test and watching it render.
- The section is searchable by the settings page's existing search, like every other
  setting.

### Task 5: the acceptance check

**Files:** create `e2e/notification-channels.spec.ts`.

**Acceptance Criteria:**

- Drives the UI: opens Settings, turns the banner off for program notifications, leaves the
  toast on, has a program print OSC 9, and sees a toast and no banner.
- Waits on observables, never on durations.
- Runs on the headless path so `e2e/run-in-container.sh` can run it.

---

## Order

```
1 (catalogue) ──► 2 (table from settings) ──► 4 (the matrix surface) ──► 5 (e2e)
                  3 (debounce window)  ─────────────────────────────────┘
```

Task 3 is independent of 2 after the section exists.

## What this plan deliberately does not do

- It does not move the `Policy` or change `Disposition`'s meaning (D5).
- It does not add a JSON-RPC method the settings methods already provide (D1).
- No push targets and no secrets — that is epic B (`nocx-hz94`).
- No per-program grants (ADR-0029 §4.5).
