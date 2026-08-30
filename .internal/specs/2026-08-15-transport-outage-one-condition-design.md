# A transport outage is one condition, not eight panel errors

**Date:** 2026-08-15
**Session bead:** `nocx-cfiul`
**Supersedes the UI half of:** `nocx-gbhwh`
**Depends on (owner's branch):** `nocx-ictcq`

## The report

The owner, 2026-08-15, with the backend down: a raw line `Connection lost —
reconnecting…` sits at the left of the tab bar and reads as debug output, and
every sidebar panel independently invents its own sentence for the same fact.

| Surface  | What it renders                                                           | Where                      |
| -------- | ------------------------------------------------------------------------- | -------------------------- |
| Files    | bare text `ws closed`, then `not connected` + Retry                       | `files/files-store.ts`     |
| Ports    | danger `Badge` "not connected" **and** a "Reading ports…" spinner at once | `ports.tsx:547`            |
| Git      | `StatusCard` "Could not reach git" / `not connected` + Retry              | `git/git-panel.tsx:498`    |
| Settings | "Failed to load settings." + Retry                                        | `settings.tsx:1005`        |
| Terminal | `Terminal failed to start:\n\nnot connected`                              | `terminal-content.ts:2066` |

Every one of those Retry buttons is a promise the surface cannot keep: a panel
does not own the socket, so retrying a call over a dead transport fails the same
way it just failed.

## What is already right, and must not be rebuilt

**Exponential backoff exists.** `dispatcher.ts` — `MIN_BACKOFF_MS = 250`,
`MAX_BACKOFF_MS = 5000`, jitter at `_scheduleReconnect` (`:368`), retries
unbounded. The initial hypothesis for this work was "there should probably be a
global state with exponential backoff"; there is one. Nothing in this design
changes the policy.

**A single lifecycle subscriber exists.** `connection-notice.tsx` is the one
consumer of `dispatcher.onDisconnect`/`onConnect`, built by `nocx-gbhwh`. This
design replaces what it renders, and moves — not duplicates — the derivation it
performs.

## Why the panels are wrong, precisely

`dispatcher.call()` rejects with `new Error('not connected')` (`:204`) and
`rejectAllPending('ws closed')` (`:148`). Those are plain `Error`s: nothing in
their type distinguishes "the backend is unreachable" from "this feature
failed". A panel that receives one has no way to tell which it is, so each
panel guesses, and each guesses differently. That is the defect the surfaces
inherit; it is not eight independent bugs.

## Two defects found while designing this

**D1 — a failed first connect leaves a shell that never recovers.**
`main.tsx:119` is `await client.connect(port, host, token)`, and the _entire_
composition root is after that await. `main()` is invoked as
`main().catch(err => log.error(...))` (`:952`). So when the backend is absent at
startup: the promise rejects, `main()` aborts, the failure goes to a log, and
the clients, sidebar, and tabs are never constructed — **even after the backend
returns**. The socket may well reconnect underneath; nothing resumes `main()`.
A soft degrade visible only in a log is the shape AGENTS.md names explicitly.

**D2 — `ws.onopen` has no identity guard.** The close listener has one
(`if (this.ws !== ws) return`, `:145`); `onopen` (`:135`) does not, so a
superseded socket still calls `fireConnect()`. Unreachable in practice today
because nothing supersedes a socket mid-attempt. `retryNow()` below makes it
reachable. **Filed separately; this design depends on it.**

## Design

### 1. The Dispatcher publishes its own state

The Dispatcher owns the socket, the timer and the backoff. It must also own the
_name_ of the current condition, because it is the only thing that knows one:

- `connecting` — an attempt is in flight
- `online` — the socket is open
- `waiting` — the socket is dead and the next attempt is scheduled

This is not a preference. `onConnect`/`onDisconnect` are the only events the
Dispatcher emits; there is no "attempt started" and no "attempt failed", and
when the timer fires at `:372` and a new attempt begins, nothing is announced.
A separate module _cannot_ derive `connecting` from `waiting` without polling
private state or guessing. An earlier draft of this design proposed exactly
that separate module; it was a second owner of connection policy wearing a
derivation's clothes.

So: the Dispatcher exposes a read-only snapshot plus a change event. A thin
adapter turns it into a Solid signal. The adapter holds no policy.

### 2. `Dispatcher.retryNow()`

Backs the one Retry button in the product — the only place where Retry can act,
because this is where the socket lives. Contract:

- cancels the scheduled timer;
- resets backoff to `MIN_BACKOFF_MS`;
- **single-flight**: a no-op when an attempt is already in flight;
- never leaves two live sockets, and a superseded socket's `open`/`error`/
  `close` may not move current state (this is why D2 is a dependency).

### 3. The composition root runs exactly once

**It is not re-entered on reconnect.** An earlier draft proposed re-entering it
on each transition to `online`; that would duplicate nearly every application-
level registration in `main.tsx`, none of which is disposed:

| Registration                                          | Line                    | Effect of a second entry       |
| ----------------------------------------------------- | ----------------------- | ------------------------------ |
| `dispatcher.subscribe('vault.unlockRequest')`         | 139                     | two dialogs per request        |
| `dispatcher.subscribe('connections.passwordRequest')` | 154                     | two dialogs per request        |
| `document.addEventListener('keydown', …)`             | 200, 201, 615, 823, 835 | one keypress handled N times   |
| `new VaultObserver(...).start()`                      | 126                     | N refreshes per reconnect      |
| `new SettingsObserver(...).start()`                   | 268, 498                | N snapshots per reconnect      |
| `setInterval` (update check)                          | 868                     | one leaked timer per reconnect |
| `render(...)`                                         | 83, 885                 | duplicate Solid roots          |

The observers' case is `nocx-bwzg` ("A live session can have two active
reconnect subscribers") multiplied by the reconnect count. `start()` returns a
disposer that `main.tsx` discards, and `stop()` is never called.

Instead, `main()` is split by _lifetime_, not by feature:

- **Built once, before the socket is awaited** — theme, platform, the `App`
  shell, `Dispatcher`, every client, the global key handlers, the observers,
  the update notice, and the connection overlay. This is what fixes **D1**: the
  application exists whether or not the first connect succeeds.
- **Built on `online`, torn down on disconnect** — the sidebar (activity bar and
  panel surfaces) and any open backend-driven dialog.
- **Never torn down** — the tab bar, the tab strip, the panes and their xterm
  instances.

The last boundary is deliberate and is where "unmount everything" stops. The
tab strip owns the panes; destroying it destroys the terminals. Scrollback lives
in the renderer (AD-6) and sessions reattach by id, so a terminal that survives
a blip keeps every byte the user was reading. The opaque overlay covers it
regardless, so nothing is gained by destroying it and a session's worth of
output is lost.

Tearing down the sidebar is what makes the panels' stale `not connected` states
die: they die with the component, and the remount re-fetches. The cost, accepted
deliberately: expanded directories in Files, filter text in Git and Ports, and
scroll positions reset on every outage. The activity bar's selected view is
restored after the remount — a person who was on Git comes back to Git.

Disposal is a contract, not an assumption: the sidebar handle's `destroy()` is
what runs, and it must reach every panel disposer (`sidebar.tsx:535`,
`files/files-view.tsx:153`, `files/files-store.ts:1198`). Clearing DOM is not
equivalent. This is asserted, not assumed — see Testing.

### 4. Backend-driven dialogs close on disconnect

`vault.unlockRequest` (`main.tsx:139`), `connections.passwordRequest` (`:154`)
and the host-key queue (`:163`) hold pending requests in renderer state. Their
replies are RPCs, so on a dead socket there is nothing to answer with and no
server-side request left to answer. They close.

This also resolves a layering conflict the overlay would otherwise lose. A
native modal `<dialog>` renders in the browser top layer, above every stacking
context — stated in `ui/dialog.tsx:2` and acknowledged in
`ui/overlay/stack.ts:8`. A normal-layer overlay cannot cover one. Pushing the
overlay as topmost in the registry would instead make the registry lie about who
owns Escape and focus. With the dialogs closed, the question does not arise.

### 5. The overlay

A kit component, because a hand-rolled control inside a surface is what produced
the notice being replaced.

- `ui/connection-overlay.tsx`, identity class `.ui-connection-overlay`
  (the kit's vocabulary is `ui-*`), variance as `data-state`.
- `styles/components/connection-overlay.css`, a row in `ui/README.md`, a test.
- Composed from kit primitives: `Spinner` and `Button`. Nothing is repainted.
- Rendered as a native `<dialog>` + `showModal()` so it is genuinely top-layer
  and genuinely inert behind — the same mechanism `ui/dialog.tsx` already uses,
  and the reason §4 must hold.
- Content: the app logo, a spinner, one sentence, and Retry.
- **Retry appears only in `waiting`.** While an attempt is in flight there is
  nothing to skip, and a button that does nothing is the defect being removed.

**Timing.**

- **At startup** the overlay is visible for a minimum of one second — it is the
  application's loading screen, and a splash that flashes for 80 ms is worse
  than none.
- **On a drop** it appears immediately, with no grace delay, and leaves as soon
  as the state is `online`. A blip that recovers in 300 ms shows for 300 ms.

### 6. Deleted

`connection-notice.tsx`, `connection-notice.test.tsx`,
`styles/surfaces/connection-notice.css`, its `@import` in `style.css:75`, and
the call site at `main.tsx:118`.

## Explicitly out of scope

- **Marking a tab whose session was lost.** Today a failed reattach calls
  `state.exitCallback?.(sid)` (`ipc.ts:290`), which reaches
  `host.requestClose()` (`terminal-content.ts:1902`) and destroys the tab. That
  is `nocx-ictcq`, owned by the repo owner on another branch. Consequence,
  recorded so it is a decision and not an accident: deleting the notice removes
  the `{resumed, lost}` sentence, which is currently the only place the product
  says work was lost. Between this landing and `nocx-ictcq` landing, a session
  lost on reconnect is reported nowhere. The owner accepted this trade with the
  fact in hand.
- **Stopping an in-flight block's timer**, and marking an interrupted ask. That
  is the other half of `nocx-gbhwh` and stays with it. The overlay hides a
  spinning block during the outage; it does not make it honest afterwards.

## Testing

Per AGENTS.md rule 2, the happy path is watched end to end; per rule 3, the
failure paths are enumerated.

**e2e (`cmd/devharness`, headless):**

1. Start the app with **no backend**, then start the backend without reloading
   the page: the overlay is present from first paint and the application becomes
   usable. This is D1, and it fails on `main` today.
2. Kill the backend with a live terminal tab: the overlay appears, and no
   `ws closed` / `not connected` text is reachable anywhere in the document
   during the outage.
3. Restart it: the overlay leaves, the sidebar answers, and the terminal's
   scrollback is byte-identical to before the drop.
4. Retry during a scheduled wait acts; during an in-flight attempt it is absent.
5. Disconnect with each backend-driven dialog open: it closes, and the overlay
   is the interactive surface.

**Unit:**

6. **The disposal contract.** After N drop/reconnect cycles, the Dispatcher's
   subscriber counts and the observers' registrations return to their baseline.
   This is the assertion that guards the `nocx-bwzg` shape from returning
   through the sidebar teardown, and it is stated as an interval — from before
   the first teardown until after the Nth remount — not as a moment.
7. `retryNow()` is single-flight: called twice while an attempt is in flight, it
   opens exactly one socket.
8. The state machine's transitions, including the one no live event produced
   before: `waiting → connecting` when the timer fires.

## Consequences

- One sentence about the transport exists in the product, in one place, owned by
  the layer that owns the socket.
- Retry exists once, where it can act.
- The application boots without a backend and recovers when one appears.
- A short blip costs the sidebar's view state. Accepted.
- Between this and `nocx-ictcq`, a session lost on reconnect is reported
  nowhere. Accepted, with the fact stated.
