# The client survives a backend that is not there

**Date:** 2026-08-30
**Session bead:** `nocx-36sls`
**Supersedes:** `.internal/specs/2026-08-15-transport-outage-one-condition-design.md` (`nocx-cfiul`)
**Depends on:** `nocx-ubszh` (the `ws.onopen` guard)

## 1. What changed since the approved design

The 2026-08-15 spec was approved and never implemented; its step bead `nocx-71kap`
("Transition to writing-plans") is still open and the document itself never reached
`main` — it lives on `worktree/calm-meadow-f429` (`5024e87a`). Everything it decided is
carried forward here verbatim unless this document says otherwise.

**Then the backend left the process.** `nocx-server` owns the sessions, the vault and the
stores; the window is a launcher and a WebView. That turns "the frontend is up and the
backend is not" from a rare state into an ordinary one, and it invalidates one load-bearing
assumption of the 2026-08-15 design: that the endpoint the renderer was given is the
endpoint it will always have. It is not. `internal/transport/ws.go:1608` listens on
`127.0.0.1:` + `devBindPort()`, and `internal/transport/dev_bind_default.go:25` returns 0 in
every shipped build — the port is ephemeral, and the capability token beside it
(`ws_auth.go`, 32 bytes) is minted per daemon. **A daemon that comes back is on a different
port with a different token.**

So the exponential backoff that exists today — `dispatcher.ts`, `MIN_BACKOFF_MS = 250`,
`MAX_BACKOFF_MS = 5000`, jitter at `_scheduleReconnect` (`:385`) — is retrying the wrong
thing. It is correct as a _schedule_ and unchanged by this design. What is wrong is its
_unit_.

## 2. What this crosses, and what those documents already decided

| Boundary                                                                                                                                                                        | What it already decides                                                                                                                                         | What this design does with it                                                                                                                             |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-1** — one WebSocket, binary data plane + JSON-RPC control plane; and, in its own words, "Security invariant for when web ships: auth token + bind-to-localhost by default" | The transport shape, and that the web target was always coming                                                                                                  | §6's heartbeat is a JSON-RPC control-plane method, never a new plane and never a binary msg-type. §5 is the web half AD-1 anticipated.                    |
| **AD-2** — one Go core, multiple build targets, "desktop backend, web server, remote helper"                                                                                    | The web server is a named target, not a hypothetical                                                                                                            | §5 designs the seam it plugs into.                                                                                                                        |
| **AD-3** — the Wails shell stays thin and swappable; business logic must not migrate into it                                                                                    | The shell may offer a _capability_, never a _policy_                                                                                                            | §3's binding performs one discovery attempt and returns the outcome. The backoff, the single-flight guard and the decision to retry stay in the renderer. |
| **AD-8** — interface-first; "variation is expressed by the interface, never by a fork inside an implementation"                                                                 | No mode strings, no `if (isWeb)`                                                                                                                                | §5 is two implementations of one endpoint-provider interface, chosen once at the composition root.                                                        |
| **AD-9** — reconnect/replay: bounded per-session ring keyed by byte offset, frontend acks, replay or explicit `reset`                                                           | How a session's bytes survive a drop                                                                                                                            | Unchanged and relied upon. §6 exists so that a socket which is _dead but open_ reaches this machinery at all.                                             |
| **`nocx-server` design §4** (2026-08-28)                                                                                                                                        | Two entry points: a `0600` unix socket for lifecycle and discovery, loopback TCP for data; a WebView cannot speak unix sockets, and TCP has no peer credentials | §3 is that split obeyed: the stable address stays with the Go process, the ephemeral one with the renderer, so re-discovery must cross the binding.       |
| **`nocx-server` design §5**                                                                                                                                                     | A fresh client asks the coordinator for live sessions and reattaches                                                                                            | Why §3 may raise a daemon at all: a _found_ daemon still has the sessions.                                                                                |

## 3. The retry unit is the coordinator, not the socket

`Launcher.Launch` (`internal/coordinator/launcher.go:182`) already does exactly what a
recovery needs, and is already idempotent — three outcomes in order: a compatible daemon is
found and used as it is; `ErrNoCoordinator` and one is raised; something incompatible is
running and is replaced, loudly. Its address is not searched for but computed:
`RuntimeDir(paths)` (`server.go:77`) is `<DataDir>/run`, and the socket is `srv.sock` inside
it (`:25`), so `nocx` versus `nocx-dev` separates by build tag for free.

It is called **once**, from `ServiceStartup` (`main.go:211`), and there is no second path to
it — no binding on `WailsApp` re-runs it. That is the hole: when the daemon dies mid-session
the renderer retries a dead port forever, nothing raises a replacement, and the only cure is
quitting the app.

**Decision.** `GetWSPort` (`main.go:539`) and `GetWSToken` (`:561`) — two getters over a
cached `w.ws` with `0` as an implicit sentinel — are replaced by **one binding returning a
result**: either an endpoint (`{host, port, token}`) or a typed failure (§4). It runs
`Launch`, so it may find, raise or replace. It is called at startup and on **every**
connection attempt, and the dispatcher connects to what it returns rather than to what it
remembers.

**A re-discovery may raise a daemon** (owner's decision, 2026-08-30). Sessions survive a
found daemon; a _raised_ one is a new process with no PTYs, and the overlay says so rather
than letting the user infer it from an empty window.

**What stays in the renderer, per AD-3.** The binding performs one attempt and reports. The
schedule (§1's backoff, unchanged), the single-flight guard, and the decision to attempt at
all are the dispatcher's. A shell that decides when to retry is a shell that has grown a
policy.

## 4. The launcher classifies its own failure

Today a failed launch is `go w.fatal("nocx cannot start", err.Error())` (`main.go:258`),
and `fatal` (`:515`) shows a native error dialog carrying the raw wrapped Go string, then
calls `app.Quit()`. Two things are wrong with it, and the second is the owner's ruling:

- **It prints machine text at a person.** This is the same defect as the mono
  `Connection lost — reconnecting…` line this design deletes, only modal.
- **It quits.** A process that has ended cannot retry, cannot recover when the cause is
  fixed, and cannot explain anything further.

**Decision (owner, 2026-08-30): the client boots, attempts, and says honestly what it could
not do and what to do about it.** `fatal()` is no longer reached from the launcher path. The
window opens; the overlay is the surface that states the failure.

This obliges the launcher to return a **typed** failure kind rather than a wrapped string,
because a sentence with a remedy cannot be derived from `fmt.Errorf` text. Four kinds, one
per real failure site in `launchCoordinator` (`main.go:410`):

| Kind                       | Site                                                                                 | What the person is told                                                                                                     |
| -------------------------- | ------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------- |
| `profile-unusable`         | `storage.NewAppPaths()`                                                              | the path, and that it cannot be read or written                                                                             |
| `server-binary-unusable`   | `serverbin.Resolve`                                                                  | macOS: the install is incomplete, reinstall. Linux: the versioned copy under `~/.local/share/nocx/bin` could not be written |
| `incompatible-coordinator` | `Launch` → neither `nil` nor `ErrNoCoordinator`                                      | something else answers the socket; its path, and that it is another or older nocx                                           |
| `not-ready`                | spawn succeeded, readiness never arrived inside `launchTimeout` (45 s, `main.go:43`) | the daemon's log path — `nocx-server` owns its own log now (`main.go:55`)                                                   |

Each kind carries a message and a remedy. Retry stays offered for all of them: a person may
have fixed the cause between attempts, and a button that refuses to try again is a second
way of saying nothing.

**Out of scope, filed as `nocx-4ge6t`: the daemon counting its own crashes.** A daemon that
cannot start cannot explain itself, which is precisely why §4 puts the explanation in the
client. Durable crash accounting on the daemon's side — a start record, an unmatched clean
exit read by the next instance, and a refusing state that comes up and answers `Hello` with
a reason — is a real improvement and a separate deliverable. Nothing here depends on it.
Note for whoever takes it: `srv.lock` cannot supply it. `internal/coordinator/lock.go:11`
states that the kernel releases the flock "however it dies", so a clean exit and a crash are
indistinguishable to it by construction.

## 5. Two endpoint providers, one seam

The web client is a first-class target, not a someday (owner, 2026-08-30; AD-2 names it).
`make dev-web` is its current instance: `scripts/dev-web.sh` builds the server with
`-tags "nocx_login_session nocx_dev_bind"` for a stable port and lifts `WSTOKEN` out of the
coordinator socket, because in a browser `GetWSPort` throws, the token falls back to `""`
and the auth layer refuses the socket — the script's own header says so.

Per AD-8, this is one interface with two implementations, chosen once at the composition
root. Not a mode string, and not a `try/catch` around a Wails call deciding the world it is
in — which is what `main.tsx:178-184` does today.

- **Desktop provider.** The §3 binding: discovery over the unix socket, may raise a daemon,
  returns an endpoint or a typed failure.
- **Web provider.** The page's own origin. It cannot discover and cannot raise anything:
  there is no Wails runtime and no unix socket in a browser. Its failures are the server's
  answers — refused token, nothing listening.

`blocked` (§7) is reachable in both worlds, with different kinds. The state machine does not
know which provider it has.

**Open, filed as `nocx-50x64`, and deliberately not guessed here: where a web client's token comes from.** The
owner's direction (2026-08-30) is **pairing**, surfaced as a new item in Settings. That is
an authentication feature with a security surface of its own and it gets its own design;
this seam is what lets it land without touching the state machine. Until it exists, the web
provider is the dev stand.

## 6. The transport must notice that it is dead

Verified on both sides, 2026-08-30:

- `github.com/gorilla/websocket v1.5.3` (`go.mod:11`), and **no `Ping`, no `Pong`, no
  `SetReadDeadline` anywhere in `internal/transport`.**
- In `dispatcher.ts` the **only** `setTimeout` is the reconnect timer (`:389`). There is no
  RPC timeout of any kind; a pending call is rejected only by `rejectAllPending` on the
  socket's `close` event (`:156`).

On loopback this costs nothing: a dead peer produces an immediate RST, `close` fires, and
the machine moves. Over a real network — WiFi dropped, a laptop asleep, a NAT entry expired
— **the socket stays open and `close` never arrives.** The client sits in `online`, the
overlay never appears, and every call hangs forever without rejecting. That is the exact
condition this design exists for, and today it is undetectable.

**Decision: an application-level heartbeat on the control plane, not a per-call timeout.**

- **Not a per-call timeout**, because some calls are legitimately long — a file upload, a
  streamed agent answer — and a blanket timer fires on a healthy slow call. The transport is
  the thing that can be dead; the timer belongs to the transport.
- **Not a protocol-level ping.** The browser WebSocket API exposes no pong event to
  JavaScript at all, so a frame-level ping is unobservable by the half that must observe it.
  This is why it is a JSON-RPC method under AD-1's control plane, and it gets a schema in
  `contracts/` like every other result shape (AGENTS.md testing rule 5).
- **Shape.** The client pings when the socket has been idle; no answer inside the window and
  it closes the socket itself. Everything downstream then takes the existing `close` path,
  so half-open is indistinguishable from closed for every consumer — one condition, one
  owner. The server takes a symmetric read deadline, without which its half of the session
  lingers and holds an AD-9 ring nobody will ever ack.

## 7. One condition, four states, owned where the socket is

Carried from the 2026-08-15 design and extended by one state. The Dispatcher owns the
socket, the timer and the backoff, so it owns the _name_ of the condition; a separate module
cannot derive `connecting` from `waiting` without polling private state. A thin adapter
turns the snapshot into a Solid signal and holds no policy.

- **`connecting`** — an attempt is in flight. It now spans discovery _and_ the socket: on
  desktop that includes finding or raising a daemon.
- **`online`** — the socket is open and answering (§6 is what makes "answering" mean
  something).
- **`waiting`** — the next attempt is scheduled. Backoff unchanged.
- **`blocked`** _(new)_ — the attempt failed in a way a bare repeat cannot fix. Carries the
  §4 kind, its sentence and its remedy. Retry stays available.

## 8. The overlay

Unchanged from the 2026-08-15 design except where §7 adds a state.

A kit component, because a hand-rolled control inside a surface is what produced the notice
being deleted. `ui/connection-overlay.tsx`, identity class `.ui-connection-overlay`, variance
as `data-state`; `styles/components/connection-overlay.css`; a row in `ui/README.md`; a test.
Composed from kit primitives (`Spinner`, `Button`); nothing repainted. Rendered as a native
`<dialog>` + `showModal()` so it is genuinely top-layer and genuinely inert behind — the
mechanism `ui/dialog.tsx` already uses, and the reason §10 must hold.

Content: the app logo, one sentence, and Retry.

**Motion states what is true.** The logo pulses while an attempt is in flight
(`connecting`); in `waiting` and `blocked` it is still. A logo that pulses through three
minutes of waiting is a progress indicator that is lying.

**Retry appears where it can act** — in `waiting` and in `blocked`, never during
`connecting`, where there is nothing to skip and a button that does nothing is the defect
being removed.

**Timing.**

- **At startup**, visible for a minimum of one second. It is the application's loading
  screen, and a splash that flashes for 80 ms is worse than none.
- **On a drop**, immediately, with no grace delay, and gone as soon as the state is
  `online`. A blip that recovers in 300 ms shows for 300 ms. The startup minimum is
  deliberately _not_ applied here: it would turn a 200 ms blip into a second of blindness.

**Two cheap signals, correct on both platforms**: `visibilitychange` and `navigator.onLine`
trigger an attempt immediately rather than waiting out a timer. A backgrounded browser tab
has its timers throttled, so without these the first thing a returning user sees is a stale
`waiting`.

## 9. The composition root runs exactly once

Carried verbatim from the 2026-08-15 design; the split is what makes §4's "the client boots"
possible. `main.tsx:197` is `await client.connect(port, host, token)` with the _entire_
composition root after it, and `main()` is invoked as `main().catch(err => log.error(...))`
(`:1694`). With no backend the promise rejects, `main()` aborts into a log, and the clients,
sidebar and tabs are never constructed — even after the backend returns.

It is **not** re-entered on reconnect. Re-entry would duplicate every application-level
registration in `main.tsx`, none of which is disposed: two `dispatcher.subscribe` handlers
per backend-driven dialog, one keydown handler per reconnect on five listeners, an observer
refresh per reconnect from `VaultObserver`/`SettingsObserver` whose `start()` disposers
`main.tsx` discards, a leaked `setInterval` per reconnect for the update check, and
duplicate Solid roots. The observers' case is `nocx-bwzg` ("A live session can have two
active reconnect subscribers") multiplied by the reconnect count.

Split by **lifetime**, not by feature:

- **Built once, before any socket is awaited** — theme, platform, the `App` shell,
  `Dispatcher`, every client, the global key handlers, the observers, the update notice, and
  the connection overlay.
- **Built on `online`, torn down on disconnect** — the sidebar (activity bar and panel
  surfaces) and any open backend-driven dialog.
- **Never torn down** — the tab bar, the tab strip, the panes and their xterm instances.

The last boundary is where "unmount everything" stops. The tab strip owns the panes;
destroying it destroys the terminals. Scrollback lives in the renderer (AD-6) and sessions
reattach by id (AD-9), so a terminal that survives a blip keeps every byte the user was
reading, and the opaque overlay covers it regardless.

Tearing down the sidebar is what makes the panels' stale `not connected` states die: they
die with the component, and the remount re-fetches. The cost, accepted deliberately:
expanded directories in Files, filter text in Git and Ports, and scroll positions reset on
every outage. The activity bar's selected view is restored after the remount.

Disposal is a contract, not an assumption: the sidebar handle's `destroy()` must reach every
panel disposer. Clearing DOM is not equivalent. Asserted, not assumed — see §12.

## 10. Backend-driven dialogs close on disconnect

`vault.unlockRequest`, `connections.passwordRequest` and the host-key queue hold pending
requests in renderer state. Their replies are RPCs, so on a dead socket there is nothing to
answer with and no server-side request left to answer. They close.

This also resolves a layering conflict the overlay would otherwise lose: a native modal
`<dialog>` renders in the browser top layer, above every stacking context, and a
normal-layer overlay cannot cover one. Pushing the overlay as topmost in the registry would
instead make the registry lie about who owns Escape and focus. With the dialogs closed the
question does not arise.

## 11. Deleted

`connection-notice.tsx`, `connection-notice.test.tsx`,
`styles/surfaces/connection-notice.css`, its `@import` in `style.css`, and the call site in
`main.tsx`. This is the mono line in the tab bar the owner photographed on 2026-08-30; it is
inline machine text where the product's rule permits none, and the overlay replaces both it
and the eight panel sentences behind it.

Its `gone` branch — the no-pending-reconnect state its own comment records as having no live
path, and defers to the owner as "a lifecycle-contract decision" — is answered by §7:
`blocked` is that state, it is reachable, and it carries a remedy.

## 12. Testing

Per AGENTS.md rule 2 the happy path is watched end to end; per rule 3 the failure paths are
enumerated with both ends of each interval named.

**e2e (`cmd/nocx-server` headless, `e2e/run-in-container.sh`):**

1. Start the app with **no backend**, then start the backend without reloading: the overlay
   is present from first paint and the application becomes usable. This is §9, and it fails
   on `main` today.
2. Kill the backend with a live terminal tab: the overlay appears, and no `ws closed` /
   `not connected` text is reachable anywhere in the document during the outage.
3. **Kill the daemon and let a new one be raised**: the client recovers without a restart,
   reaching a _different_ port and token than it started with. This is §3, and it is the
   case the split created.
4. Restart the same daemon: the overlay leaves, the sidebar answers, and the terminal's
   scrollback is byte-identical to before the drop.
5. Retry acts in `waiting`; it is absent during `connecting`.
6. A launch that fails with each §4 kind: the window opens, the overlay names the kind and
   its remedy, and the process is still alive.
7. Disconnect with each backend-driven dialog open: it closes, and the overlay is the
   interactive surface.

**Unit:**

8. **The disposal contract, as an interval.** From before the first teardown until after the
   Nth remount, the Dispatcher's subscriber counts and the observers' registrations return
   to their baseline. This guards the `nocx-bwzg` shape from returning through §9's sidebar
   teardown.
9. Retry is single-flight: called twice while an attempt is in flight, it opens exactly one
   socket. (`nocx-ubszh` is why: a manual retry racing a scheduled one is a superseded
   socket, and `ws.onopen` has no identity guard while `close` does.)
10. The state machine's transitions, including the ones no live event produced before:
    `waiting → connecting` when the timer fires, and every `→ blocked`.
11. **The half-open socket.** With the peer silent but the socket open, the client reaches
    `waiting` inside the heartbeat window. Paired, per rule 3's "and on a normal machine it
    succeeds": an idle but healthy socket is never closed by the heartbeat.
12. Both endpoint providers satisfy the same interface, including their failure shapes.
13. The heartbeat method's result validates against its `contracts/` schema over the real
    socket, not against a payload the test built.

## 13. Consequences

- One sentence about the transport exists in the product, in one place, owned by the layer
  that owns the socket. No inline machine text anywhere.
- Retry exists once, where it can act, and on desktop it can now actually succeed — it
  re-discovers, and may raise a daemon.
- The application boots without a backend, states why, and recovers when one appears.
- An unstable network is detectable at all, for the first time.
- A short blip costs the sidebar's view state. Accepted (2026-08-15).
- A raised daemon is a new process: the sessions of the dead one are gone, and the overlay
  says so rather than leaving the user to infer it.
- A crash-looping daemon is respawned without a ceiling until the separate daemon-side
  accounting lands. Accepted, with the fact stated.
