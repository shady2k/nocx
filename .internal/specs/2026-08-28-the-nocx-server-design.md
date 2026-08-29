# The nocx server: a coordinator that outlives its window, and a thin binary that owns the far side

- **Date:** 2026-08-28
- **Beads:** `nocx-6l4at` (this brainstorming session)
- **Status:** design, approved by the owner 2026-08-28; adversarially reviewed against
  the code with codex over two rounds (§11)
- **Related:** `.internal/specs/2026-08-13-remote-helper-design.md` (the thin role's
  protocol, already built), `nocx-if6` phase B (the relay this supersedes in approach),
  `nocx-457v` (the remote-helper epic, all children closed)

## 1. In one sentence

The Go backend stops being a part of the Wails process and becomes **`nocx-server`**, a
detached per-user daemon the desktop app finds, spawns and attaches to — so a session and
the work running in it survive the window closing, a WebKit crash and a UI update — while
the already-shipped **`nocx-helper`** grows the `session` service so a remote session
survives a network drop too.

What a user can do that they cannot today: **start a long command, quit nocx entirely,
reopen it, and find that same pane still running, with the output produced while the app
was closed.**

What this deliberately does **not** promise, because we have not designed it: an agent
that keeps taking turns with no client attached. Today an assistant run terminalizes on
disconnect (`internal/transport/ws_agent.go:756`), and that stays true. The honest
sentence is _the command the agent started keeps running; the agent's next step waits for
a client._

## 2. What this design crosses, and what those documents already decided

AGENTS.md requires a brief crossing a boundary to name the `AD`s and ADRs it touches and
what they already decided, before it says what to build.

| Boundary                                                                                                                                                              | What it already decides                                                                                                                                                                                                                                                                                    | What this design does about it                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| --------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-1** — one WebSocket, binary data plane + JSON-RPC control plane                                                                                                  | PTY bytes are never wrapped in JSON-RPC; a version byte and a reserved `metadata` msg-type exist for forward compatibility                                                                                                                                                                                 | Unchanged. The coordinator serves the same socket it serves today; only the process hosting it moves.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| **AD-2** — one Go codebase, multiple build targets (desktop backend, web server, remote helper); hosts embed or serve it, never reimplement it                        | Multiple build targets is the shape                                                                                                                                                                                                                                                                        | **Followed literally, and this is a correction to an earlier draft of this design.** `nocx-server` and `nocx-helper` are two build targets over shared `internal/` packages. They are not one artifact selected at runtime — see D1.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| **AD-6** — the backend never sniffs the byte stream; the frontend owns scrollback                                                                                     | Render state and scrollback are the renderer's                                                                                                                                                                                                                                                             | Unchanged, and load-bearing for D6: detached output is spilled verbatim as transport buffering, never parsed and never made a second scrollback.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| **AD-7** — session-id is server-authoritative                                                                                                                         | `internal/session/session.go:424` mints one backend-instance id, `:516` stamps every session with it, and the wire result carries it (`internal/transport/ws_session_handlers.go:561`)                                                                                                                     | Not replaced. The durable pane association carries `instanceId`, so a coordinator restart continues to invalidate every old association (D5).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| **AD-8** — interface-first, DI at one composition root; variation is expressed by the interface, never by a mode string or a fork inside an implementation            | No switches selecting behaviour                                                                                                                                                                                                                                                                            | Roles are separate composition roots in separate `cmd/` targets, not a `--role` flag (D1).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| **AD-9** — reconnect/replay ownership: a bounded per-session output ring keyed by monotonic byte offset, frontend acks, replay from the offset or an explicit `reset` | Already implemented: `internal/transport/ring.go:36`, `ws.go:59`, and the renderer already reattaches with its last offset and aggregates `{resumed, lost}` (`frontend/src/ipc.ts:398`)                                                                                                                    | Extended, not duplicated. D6 adds a disk spill under the same owner. D2 is why `nocx-helper`'s reserved `seq`/`ack` must implement _this_ contract rather than invent one.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| **AD-10** — backpressure, never drop, never grow unbounded                                                                                                            | `ring.go:74` states it and `:83` implements it: a full ring blocks the writer until an ack frees space                                                                                                                                                                                                     | This is the reason D6 exists at all — see D6.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| **ADR-0007** — cross-platform auto-update: swap in place, relaunch, health reported by the new UI, rollback journal deleted on health                                 | `internal/update/transaction.go:175,376,386`                                                                                                                                                                                                                                                               | Directly threatened by a surviving daemon. D4 is not deferrable.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| **ADR-0019** — one authoritative ledger, disposable projections                                                                                                       | The ledger is the record of what happened                                                                                                                                                                                                                                                                  | **Corrected after this design was first written.** The ledger _is_ where detached output belongs. Output capture shipped in `nocx-2f0f` (closed 2026-08-19): `internal/transport/ws_ledger_capture.go`, `frontend/src/capture-client.ts`, the `artifacts` / `artifact_chunks` tables, and a byte budget with eviction in `nocx-2f0f.5`. The comment at `ws_history_record.go:187` governs a different path and was misread. The half that is genuinely missing — capture happens in the renderer at freeze time, so a detached session captures nothing — is already owned by epic `nocx-22k1c`, which also already decided the rule: build **on** the shipped path, same store, same provenance, same retention knobs, never a second one beside it. |
| **ADR-0024 / `0049-the-channel-we-own-is-the-carrier.md`** — shell integration delivered over the channel we own                                                      | Integration travels on the channel, not in the exec command                                                                                                                                                                                                                                                | Phase B changes the carrier, not the mechanism: the thin binary spawns the shell itself, so integration no longer needs rc-file editing or SFTP delivery. Markers still come from the shell — AD-6 is untouched.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| **ADR-0032** — the vault raises its own unlock, and the dialog is renderer-owned                                                                                      | `RequestUnlock` broadcasts to connected clients and returns `ErrNoClientConnected` when there are none (`internal/transport/unlock_requester.go:205-227`); the renderer owns and mounts the dialog — consumed at `frontend/src/main.tsx:231`, mounted at `:1501`, defined in `frontend/src/vault.tsx:1026` | A detached coordinator cannot raise its own unlock. It suspends and states the reason (D9). We do **not** invent a daemon-native dialog; that would break this ADR.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| **AD-3** — Wails as the desktop shell, thin and swappable (`architecture.md:122`)                                                                                     | The shell is thin and replaceable                                                                                                                                                                                                                                                                          | Crossed in the AD's own direction: taking the backend out of Wails makes the shell thinner. But D3 gives the shell a new job — brokering native-host requests — and that is the half AD-3 must be read against.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| **AD-5** — two-tier shell integration; Tier B **augments (never replaces)** the remote shell (`architecture.md:139`)                                                  | The helper adds metadata beside the shell                                                                                                                                                                                                                                                                  | **Phase B breaks this and amends it deliberately.** A helper that spawns the shell replaces the substrate rather than augmenting it — which is the point of B, since it is what removes rc-file editing and SFTP delivery. Amended in `docs/architecture.md`, not routed around.                                                                                                                                                                                                                                                                                                                                                                                                                                                                      |
| **ADR-0002** — native tabs, no embedded multiplexer                                                                                                                   | Its own "Revisit when" names _"remote-persistent sessions… a session that survives the client process entirely, reattachable from another machine"_ (`0002-native-tabs-no-embedded-multiplexer.md:71`)                                                                                                     | This design **is** that trigger. It is revisited here, and its conclusion still holds: we are not embedding a multiplexer, we are moving our own backend out of the window.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| **ADR-0003** — distribution without a Developer ID                                                                                                                    | Unsigned bundles, dmg + AppImage, no publisher signature                                                                                                                                                                                                                                                   | Adding a second executable to the macOS bundle is a packaging change: it must be covered by the same ad-hoc signing and by the AppImage build, or the daemon is the one file Gatekeeper meets unblessed.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| **ADR-0026** — the control plane runs off the read loop                                                                                                               | Already binds request contexts, disconnect cancellation, native-dialog admission, shutdown draining and multi-client first-consumer behaviour (`0026-…:227`)                                                                                                                                               | D3's client-host brokerage is governed by it. "Route it to an attached client" is not enough: _which_ client owns a given ask is already answered there, and that answer is reused rather than re-invented.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| **ADR-0048 (ui-state is a document)**                                                                                                                                 | `internal/uistate` is the single owner of persisted pane/UI facts                                                                                                                                                                                                                                          | D5 adds a **server-owned** association beside it, so the split is stated explicitly: the pane is the renderer's durable identity (`panes.create.schema.json:5`), the live-session binding is the coordinator's, and neither owns the other's half.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| **ADR-0034** — consent belongs to the machine, not the connection (`0034-…:42`)                                                                                       | Consent is keyed by machine identity                                                                                                                                                                                                                                                                       | B's daemon-aware liveness and uninstall preserve that key. A daemon instance is not a new consent subject.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| **ADR-0036 / ADR-0037** — HTTP upload and download routes beside the WebSocket                                                                                        | The same listener serves ticketed transfers (`ws.go:1477`, `:1492`)                                                                                                                                                                                                                                        | The TCP listener is therefore not "data and control" only. Daemon lifetime, "an operation is in flight" and any token rotation must count an in-progress transfer.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| **ADR-0043** — one connection to the encrypted store                                                                                                                  | Multi-process safety is **unproven**, and nothing stops two backends reaching the same data directory (`0043-…:140`)                                                                                                                                                                                       | Binds A2 directly: a graceful handover must not run two coordinators against one `content.db`. Either the handover is sequential with a proven handoff of the store, or A2 is blocked on resolving this first.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| **`.internal/specs/2026-08-13-remote-helper-design.md` D15**                                                                                                          | `session` is a reserved service name; PTY ownership, reattachment, replay, a daemon and a socket are "designed for and deliberately not built"; `host.Register` panics on `"session"`                                                                                                                      | Phase B builds exactly that reservation, and lifts the panic.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |

## 3. Decisions

- **D1 — Two build targets, not one artifact with roles.** `cmd/nocx-server` (coordinator)
  and `cmd/nocx-helper` (thin) share `internal/` packages and have separate composition
  roots. **The decisive evidence is a build trap, not taste:**
  `internal/helper/deploy/build.go:82` is `//go:embed all:artifacts`; `Makefile:73` is the
  three-target matrix and `Makefile:104` makes `build-release` depend on `helpers`, so three
  cross-compiled helpers are built _into_ the shipped binary. One
  artifact serving both roles would embed itself. AD-8 forbids the `--role` switch that
  would otherwise be needed, and AD-2 already prescribes multiple targets, so this costs
  no architecture amendment.
  _(An earlier draft argued the coordinator needs cgo and the helper must not. That was
  simply wrong — `internal/app/app.go:832`: ncruces/go-sqlite3, "no cgo". The reasons are
  dependency surface, artifact size, role isolation and AD-8.)_
- **D2 — The coordinator owns the durable-session contract; the helper preserves it.**
  AD-9's ring, offsets, acks, replay and `reset` are implemented in the transport today;
  `internal/helper/proto/frame.go` only _reserves_ `seq`/`ack`. Therefore A precedes B, and
  B implements the remote boundary against A's contract rather than minting a second notion
  of resumability. AGENTS.md names the alternative: "a second implementation of one concept
  is a regression with a delay fuse".
- **D3 — The desktop app becomes a window plus a launcher.** It finds the coordinator,
  spawns it detached when absent, waits for readiness, and hands the renderer the address
  and token. Native-host capabilities that live in `main.go`'s `ServiceStartup` — file
  dialogs, opening URLs, notifications, window focus — become **client-host** capabilities:
  the coordinator routes such a request to an attached client and answers honestly that no
  UI host is attached when there is none.
- **D4 — The version handshake lands in A1, not A2.** Without it the first auto-update is
  actively unsafe, not merely incomplete: `transaction.go:175` swaps the bundle and keeps
  the rollback journal pending; the surviving old coordinator answers the new window; the
  new renderer reports healthy; `transaction.go:376` verifies only the on-disk bundle
  identity, not the running backend's version; `transaction.go:386` begins finalisation and `:403` deletes the
  journal. A mixed pair certifies the update and disarms rollback. A1 must therefore
  compare build and protocol versions, refuse an incompatible coordinator, replace it
  before health is reported, and tie health to the expected pair. A1 may kill the old
  coordinator and lose its sessions on mismatch — **saying so out loud**. Graceful,
  session-preserving handover is A2.
- **D5 — A durable `paneId -> (instanceId, sessionId)` association, server-owned.** Today
  `contracts/panes.create.schema.json` deliberately carries no live session id and says the
  session dies with the backend; that stops being true. A claim is refused when
  `instanceId` differs, which is how AD-7 keeps holding across a coordinator restart.
- **D6 — Detached output is owned by `nocx-22k1c`, not by this design, and the first draft
  of D6 was wrong.** The problem is real: the ring is 256 KiB (`ring.go:14`) and blocks its
  writer when full (`ring.go:74`, implemented at `:83`), and the acks come from a client, so
  with nobody attached a session freezes after 256 KiB — the daemon would stall the very
  work it exists to protect.

  The first draft answered it with a bounded per-session **spill file**. That is a second
  store for terminal bytes, and `nocx-22k1c` forbids exactly that in as many words: build on
  the shipped capture path — same store, same provenance, same retention knobs — never a
  second one beside it. The draft's justification, that the output capture path does not
  exist, came from misreading `ws_history_record.go:187`; capture shipped in `nocx-2f0f`.

  So this design **depends on** `nocx-22k1c` rather than answering it, and the AD-10 question
  it posed narrows and travels with it: the retention bound is already decided by
  `nocx-2f0f.5`'s budget and eviction, and what remains is what the ring does when the
  recorder is unavailable or slower than the source — AD-10 (`architecture.md:188`) requires
  both losslessness and throttling at the bound, so that case still owes an explicit answer.

- **D7 — The reclaim outcome must be visible, and the renderer does not do this yet.** A
  reset returns `'resumed'` (`frontend/src/ipc.ts:419`), and the `{resumed, lost}` aggregate
  (`:444`) counts sessions the backend no longer holds — it does not report a replay gap.
  The hook exists (`state.resetCallback`, `:417`); the notice does not use it. And "clear +
  resync" owes a definition AD-9 promises and the code does not provide: reset today clears
  the decoder and the renderer (`frontend/src/terminal-content.ts:3018`) while the backend
  returns no state when the requested offset is gone (`ring.go:157`). Replay must not begin
  inside a UTF-8 sequence or a VT escape.

- **D7a — a persistence cursor is not an acknowledgement.** AD-9 (`architecture.md:181`) and
  `ring.go:186` both mean by "ack" that the **frontend** received the bytes. A cursor saying
  the recorder has them frees the memory ring, but it is a different fact with a different
  invariant. Two cursors, two names — conflating them is how one silently acquires the
  other's guarantees. Travels with `nocx-22k1c`.

- **D7b — a session with no client has no size, and that is `nocx-eidfb`'s question.** AD-1
  makes `open` carry `{cols, rows, xpixel, ypixel}` and the channel is created at that size,
  so a detached session has no defined geometry at all. This design did not notice it; that
  epic did, and cites herdr's answer (`src/server/headless.rs:329`): the pane runtime size
  comes from the foreground client, or a minimum when none is connected, while rendering
  stays per client. This design depends on it and does not re-answer it.

- **D8 — One active client per session, and the loser is told.** `ws.go:64` is the single `subscriber` slot and `:75` replaces it silently on attach. A1 makes that explicit rather than silent:
  a second attach takes the session and the displaced client is informed it lost it.
  Read-only observers and real multi-window ownership are later work.
- **D9 — The vault seals when the last client has been gone for a departure window** (amended 2026-08-29 by `nocx-58q7d`; the first draft said "when the last client detaches" and did not define the event, which is what broke eighteen e2e specs — a reload and a reconnect both pass through zero clients, so an instantaneous read of the count seals the vault on a page refresh. `DefaultDetachWindow` is ten seconds and lives in `internal/vault/presence.go` with the reasoning). Today `main.go:639` -> `app.go:2478` ->
  `internal/vault/vault.go:754` seals on app shutdown; with a daemon, quitting the window would otherwise
  leave the root key in a live heap for days — an exposure that did not exist before. The
  cost is named: an SSH session that needs a secret while you are away does not reconnect
  by itself; it suspends, states that it is waiting for unlock (the `ErrNoClientConnected`
  path already exists) and continues when you return.
- **D10 — The keystore stance is decided at start, never discovered in a modal.** Verified
  on macOS this session: `$HOME` **does** move the login keychain, a read under a `$HOME`
  with no keychain fails silently, and a **write** raises "Keychain not found" — a modal
  nobody can dismiss on a headless host. `internal/vault/system/system.go:138`'s `Probe` opens with a **write** of a fresh random
  entry on every backend start — which is the very operation that raises the modal. **So
  "probe, then fall back" cannot be the mechanism: the probe is the failure.** The stance is
  _declared_, not discovered — by the launcher on a desktop start, by a build property on a
  headless one — and the write-probe runs only once the stance says the system keystore is
  in play. One policy in one place; §6's build-tag note is the headless half of it, not a
  second answer.
  _(This also settles `nocx-o4hg`, whose recorded cause — "wails dev re-signs the binary,
  so macOS re-asks" — cannot be right: `keyring_darwin.go` in zalando/go-keyring v0.2.8 execs `/usr/bin/security` (`:29`, used by Get/Set at `:43-48` and `:70-99`), so
  our code signature never enters a keychain ACL. AGENTS.md carries the same wrong claim
  and is corrected in the same commit as the fix.)_
- **D11 — `cmd/devharness` is deleted.** It is 51 lines of `app.New()` + `Start`, which is
  what `nocx-server` is. e2e runs the shipped server, so the suite and production stop
  being two similar things. The keystore declaration moves with it, which is what closes
  `nocx-nhhr`.

## 4. Lifecycle and discovery

**Two entry points.** The daemon listens on a `0600` unix socket (lifecycle and access) and
on loopback TCP (data and control — what already exists). The split is forced: a WebView
cannot speak unix sockets, so the renderer must use TCP; and TCP has no peer credentials,
which the socket does. Both paths derive from `storage.AppDirName`, so `nocx` vs `nocx-dev`
isolation by build tag, and `Isolate` for tests, come free.

**Client startup.** Connect to the socket -> state own build and protocol version ->
receive the daemon's versions, the TCP address and the token. If the socket is absent or
dead: take an exclusive lock beside it (two windows starting together must not raise two
daemons), spawn `nocx-server` detached — `setsid`, stdio to `/dev/null` — and poll the
socket for readiness with a timeout. This is herdr's pattern
(`/home/dev/repos/herdr/src/server/autodetect.rs:181-218` — the detachment contract and the stdio redirection), which needs no installer, no
launchd and no sudo.

**Where the binary lives.** macOS: inside the bundle, `nocx.app/Contents/MacOS/nocx-server`
— installing the app installs the server. Linux: an AppImage unmounts its FUSE directory
when the process exits, so the daemon would lose its own executable at exactly the moment
it must survive; the binary is copied on first run to a **versioned** path
`~/.local/share/nocx/bin/nocx-server-<version>-<hash>`, promoted atomically, and always
spawned from there. A stable mutable name is forbidden — it eventually runs stale code
forever. Content-hash verification and pruning already exist in `internal/helper/deploy`.

**When the daemon exits.** Never while a session is alive or an operation is in flight;
otherwise after a grace period once the last client detaches. Living forever accumulates a
daemon per update; exiting eagerly defeats the point. It does **not** hold an unsealed
vault — D9 seals on last detach.

**launchd is later and optional**, and a `LaunchAgent` only — never a `LaunchDaemon`. A
system daemon has no login keychain, and by D10 a keychain write without one raises a modal.

## 5. Reclaiming a pane

A fresh client asks the coordinator for the live sessions with their pane bindings and a
replay start, attaches, and receives either the tail the spill still holds or an explicit
`reset`. This is not a new mechanism: it is the existing reattach, given a list instead of
the renderer's process memory. The gap it closes is precise —
`frontend/src/ipc.ts:355` is a `Map` in renderer memory and the reconnect pass at `:403`
reattaches only what is in it, so a fresh window today reattaches nothing and the live PTYs
are orphaned.

## 6. Security

- **Discovery socket.** Parent directory `0700`, socket `0600`, peer-UID checked on every
  connection (`SO_PEERCRED` / `getpeereid`). Refuse a symlink or a foreign owner. Bind
  atomically (temp name + rename) so two racing launchers cannot leave a live daemon
  without a socket.
- **The token never leaves the socket.** Not to disk, not to `argv` (visible in `ps` to the
  whole machine), not into the spawned process's environment, not into logs. It follows
  that the token cannot be passed to the daemon as an argument: the daemon mints it
  (`ws_auth.go:199`, per launch, unchanged) and hands it back over the socket.
  Per-client capabilities are deliberately **not** built in A1: the trust boundary here is
  the UID, and any process of the same user can ask the socket anyway, so separate tokens
  without revocation buy nothing. Revocation belongs with the window-ownership model (D8).
- **The spawn environment is cleaned.** A daemon that inherited `NOCX_WS_ADDR` or a
  profile override resolves something other than what the launcher expects; herdr clears
  inherited overrides for this reason. Separately, `NOCX_NO_SYSTEM_KEYSTORE` disables the
  system keystore **by environment variable**; for a process that lives for days that must
  become a build-tag property, not something any process of the user can supply.
- **Origin.** `LoopbackOriginPolicy` (`ws_auth.go:79`) stays for A1 — it is defence in
  depth, not the barrier, since a browser page cannot read the socket and so never gets a
  token. But the window is wider than a per-launch server's, so: the daemon must refuse to
  bind anything but loopback, and `PinnedOriginPolicy` (`ws_auth.go:131`) becomes the right
  default when the web role appears.
- **Vault** — D9.
- **For B, recorded now:** the thin role receives no secrets, and its authority today is
  the SSH channel that launched it. A durable session outlives that channel, so "the
  channel is closed" stops meaning "no helper is running". Consent inventory, stop and
  uninstall must gain a daemon-aware liveness protocol, or the reversibility the current
  helper design promises becomes false.

## 7. Testing

**The A1 acceptance test, watched end to end.** With `nocx-server` running headless and the
client being a page, "the client went away" is literal: start the server, open the page,
start a long command, **close the browser context entirely**, wait, open a fresh page,
reclaim the pane. Three conditions, without which the run is green and proves nothing:

1. the command must produce **more than 256 KiB** while detached, or the ring never filled
   and the spill was never exercised;
2. the command must leave a trace in time (a marker per second), and the markers spanning
   the detached window are counted after the return — that is what proves the process
   **ran**, rather than that a pane rendered;
3. the second page must be genuinely fresh — an empty renderer `sessions` map is the thing
   that is broken today.

**One cheap test per failure path.** No daemon -> the launcher raises one; spawn fails ->
the UI says so and does not hang. Two launchers racing -> exactly one daemon. Version
mismatch -> refusal, replacement, and an explicit statement that live sessions died. Spill
overrun -> `reset` on reclaim and a stated loss. Socket owned by another UID, or a symlink
-> refusal. Keystore unreachable -> the file provider chosen explicitly, zero modals. Vault
sealed while detached and a session needs a secret -> suspension with a named reason, and
resumption after unlock.

**Invariants as intervals.** Not "the pane association is written at open" but: _the
association exists from before the session is announced to a client until either the
session ends or the coordinator's `instanceId` changes._

**The wire is a party to the contract.** The new results — the live-session list, the claim
outcome, the reset notice — get JSON Schemas in `contracts/` with `additionalProperties:
false` and explicit `required`, generated renderer types, and a test that validates the
real payload **off the real socket**.

**Where it runs.** The containerized e2e, with CI as the source of truth. Deleting
devharness (D11) moves `NOCX_E2E_HOME_DIR` and `e2e/preflight.ts` onto `nocx-server`, which
is the moment `nocx-o4hg` / `nocx-nhhr` close: the keystore stance becomes a property of how
the server is started rather than a flag missing on one path.

**Who writes the tests.** Acceptance criteria as assertions in the beads, not prose. For the
daemon lifecycle and discovery security — the two places a defect is expensive — the tests
are written from this spec by someone who did not write the implementation.

## 8. Work order

**A1 is an epic, and it is a DAG, not a bag.** As first written it combined process
extraction, secure discovery, cross-platform installation, session identity, disk
buffering, cold reclamation, ownership transfer, native-host RPC, vault lifecycle,
keystore policy, updater correctness and replacing the test harness — an area, failing
AGENTS.md's "can one person be handed this whole and finish it?". Sequenced, with a front
of about three:

- **A1.0 — the foundation (blocks everything else).** `cmd/nocx-server` as a build target;
  the discovery socket with peer-UID and atomic bind; the launcher's spawn, lock and
  readiness wait; the build/protocol handshake and refusal (D4). Nothing else can be
  written against a coordinator that cannot be started or identified.
- Then a front of three, each depending only on A1.0:
  - **A1.1 — session continuity.** The durable pane association and claim (D5); reclaim
    visibility and resync (D7); one-active-client ownership (D8). Detached output (D6, D7a)
    and detached geometry (D7b) are **not** here — they are `nocx-22k1c` and `nocx-eidfb`.
  - **A1.2 — the client host.** Native-host requests routed under ADR-0026 (D3); the
    vault-on-last-detach policy (D9); the declared keystore stance (D10).
  - **A1.3 — update and installation correctness.** Paired health that cannot certify a
    mixed pair (D4's second half); versioned Linux installation under
    `~/.local/share/nocx/bin/`; the macOS bundle's second executable under ADR-0003.
- **A1.4 — the cutover (depends on .1, .2 and .3).** Production Wails starts using the
  coordinator; e2e moves onto `nocx-server`; `cmd/devharness` is deleted (D11).
  **This is where the epic's acceptance test lives**: quit the whole app, reopen it,
  reclaim the same live pane and see the output produced while it was closed, under the
  three conditions of §7. It therefore **depends on `nocx-22k1c`** (there is nothing to see
  otherwise) **and on `nocx-eidfb`** (a detached session must have a size at all).

- **A2 — graceful update handover.** Drain or hand over sessions across an update instead
  of killing the old coordinator. **Blocked on ADR-0043**: two coordinators must not hold
  one `content.db`.
- **B — the helper's `session` service.** A remote session survives a network drop, reusing
  A's ring/ack/reset contract rather than minting one (D2); the binary spawns the shell
  itself, so shell integration stops needing rc-file editing and SFTP delivery — which is
  the AD-5 amendment; acceptance disconnects long enough to cross the ring bound;
  daemon-aware liveness and stop keeping ADR-0034's machine-keyed consent intact.
- **C — `files` and `ports`**, the two remaining reserved service names.

## 9. Deliberately out of scope

- **An autonomous agent with no client attached.** Not designed, and explicitly not
  promised. `ws_agent.go:756` stands: a run terminalizes on disconnect. If it is ever
  wanted, it is a separate design owing an explicit no-human policy for approvals, egress
  review, vault prompts, budgets and notification delivery.
- **Recording output while detached, and the geometry of a session with no client.** Both
  are real and both are prerequisites of §8's A1.4 — they are out of _this_ design because
  they already have owners: `nocx-22k1c` and `nocx-eidfb`.
- Real multi-window ownership and read-only observers (D8 is the minimum honest model).
- The web role's origin pinning and any non-loopback bind.
- Windows remote hosts.

## 10. Open questions

1. **What the ring does when the recorder is unavailable or slower than the source.** AD-10
   (`architecture.md:188`) requires losslessness _and_ throttling at the bound, so with no
   client attached and the recorder unable to keep up there is no third answer: either the
   source throttles (the detached work stops) or AD-10 is amended to permit one lossy case
   and the loss is stated in the product. The question belongs to `nocx-22k1c`; it is
   recorded here because A1.4's acceptance depends on the answer.
2. **Nothing else is open.** The spill bound this document once proposed is gone with the
   spill: retention is already decided by `nocx-2f0f.5`'s byte budget and eviction path.

## 11. Review

Adversarially reviewed with codex over two rounds against the code, not the prose.

**Conceded by this design:** one runtime-selectable artifact (the `//go:embed` trap, D1);
the cgo claim, which was false; A's success criterion, which was "the daemon lives" and is
now pane reclaim; that "agents keep working" does not follow from A (§1); that the vault
lifetime is a security decision, not process management (D9); that the version handshake
cannot wait for A2 (D4); that the token must not reach disk (§6); that one subscriber per
session needs an explicit ownership model (D8); that the Linux copy sits outside ADR-0007's
transaction (§4).

**Conceded by the review:** the ordering. The reviewer opened with B-first and withdrew it
on the evidence that AD-9's ring, acks and reattach are already implemented in the transport
while the helper only reserves `seq`/`ack` — "B-first buys no durable-session decision that
survives this evidence".

**Found during the review and now load-bearing:** that a full ring blocks its writer
(`ring.go:74`), so a detached session freezes after 256 KiB without D6; and that the ledger
cannot be the sink because the output capture path does not exist
(`ws_history_record.go:187`).

**Round 3 — the written document, reviewed whole.** Corrected: nine file:line citations
that pointed at a neighbouring line or at the wrong construct (`session.go`, `build.go`,
`app.go`, `ring.go`, `transaction.go`, `ipc.ts`, `ws.go`, the vault shutdown chain, the
`herdr` span). Found and fixed: **D6's AD-10 compliance claim was impossible** — AD-10
requires losslessness _and_ throttling at the bound, so a lossy bounded spill cannot
satisfy it, and the design now puts the amendment to the owner as D6-a/D6-b instead of
asserting compliance. **D7's premise was false** — the renderer counts a reset as
`'resumed'`, so the product does not state the loss today. **D7a** separates the spill
cursor from a frontend acknowledgement, which the first draft conflated. Two internal
contradictions removed (the daemon "holds an unsealed vault" against D9; "probe then fall
back" against D10, where the probe _is_ the failing write). Nine missing boundaries added
to §2 — AD-3, AD-5, ADR-0002 (whose own "Revisit when" this design triggers), ADR-0003,
ADR-0026, the ui-state ADR, ADR-0034, ADR-0036/0037 and ADR-0043. A1 was an area and is now
a five-node DAG with a front of three. The spill bound is unset pending measurement rather
than guessed.

**Not adopted:** nothing. Every finding in round 3 was verified against the code and taken.

**A note for the next reader:** `docs/decisions/` has four duplicated numbers — 0006, 0029,
0033 and 0035 — so this document cites those by filename. Repairing the collision is its own
chore.

**Round 4 — the backlog check that should have come first.** Filing the epics turned up two
open epics this design had duplicated, and AGENTS.md's very first "before you fix anything"
check is the one that finds them. `nocx-22k1c` already owns _the backend keeps the output
while no client is attached_ — and had already recorded, on 2026-08-25, both the fact that
output capture shipped in `nocx-2f0f` and the rule that the recording must build on that
path rather than beside it. The spill file in D6's first draft was exactly the second store
that epic forbids, and its justification rested on a misread comment. `nocx-eidfb` owns a
hole this design had not noticed at all: a session with no client has no size. Both are now
dependencies of A1.4 rather than content of this document. Two beads created for the spill
were closed unstarted, with the reason recorded on each.

The lesson is the cheap one AGENTS.md already states: the five checks gate the brief, and a
design document is a brief. Three rounds of adversarial review against the _code_ did not
find what one `bd list --type epic` found in a second — because neither reviewer was looking
at the backlog.
