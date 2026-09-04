# The local helper — your own machine is a host

**Epic:** `nocx-ie23r`. **Implements:** `D11` of
[`2026-08-31-level-1-the-helper-owns-the-host-design.md`](2026-08-31-level-1-the-helper-owns-the-host-design.md),
whose local half was decided on 2026-08-31 and never built. Owner decisions taken
2026-09-04.

## 0. What this is, in one sentence

The machine you are sitting at becomes an ordinary host in the helper inventory — same
install, same handshake, same session service, same window — differing from a remote host
in exactly one thing: the carrier is a Unix socket rather than an ssh exec lane.

## 1. What a user can do that they could not before

**Start a build in a local pane, quit nocx, open it again, and the build is still running
in that pane with its output.**

That is what the helper already gives a remote session and what a local one has never had,
because the backend spawns local PTYs itself and they die with it. The end-to-end check is
the epic's second criterion, through `cmd/nocx-server`.

## 2. What this crosses, and what those documents already decided

- **`D11` (level 1)** — "The helper runs on every machine, including yours. Locally the
  coordinator connects to its endpoint directly; remotely through the bridge. There is no
  second mechanism, no 'local special case', and no code path that exists only for one of
  them." This document builds that; it decides nothing about it.
- **`D12` (level 1)** — same-UID trust, frozen deliberately. Any nocx under that account may
  connect. Locally this is the whole authorization model, and §4 does not add to it.
- **§5 (level 1)** — the endpoint is a private Unix socket, `0600`, in a `0700` directory,
  never a loopback port, and `internal/helper/endpoint` has a test that walks the source of
  every package on the path and asserts no TCP listener exists. Unchanged here.
- **`D2` and `D8` (the generation-daemon lifecycle design)** — the daemon exits when its last
  session exits; a coordinator on start lists generations, probes each endpoint, reconciles,
  attaches and drains, then installs the current generation if absent. Step 1 already says
  "derive and probe each endpoint" without saying the endpoints are remote. §5 below is that
  order with the local generation in the list, not a second order.
- **`N3` (delivery modes) and ADR-0034** — consent is required to deploy the binary, and it
  is keyed to the machine. §4 states why that axis does not reach the local machine, and
  adds no surface.
- **AD-8** — one owner per behaviour. `internal/pty` does not disappear and must not: the
  helper's `session.LocalSpawner` already spawns through `pty.NewLocal` (`spawn_local.go:13`,
  `:36`). What moves is the CALLER, from the backend's `localPTYFactory` into the daemon.
- **AD-5** — Tier A (the script substrate) is untouched. A host where nothing may be
  installed still gets blocks; that is level 0 and this document does not narrow it.

## 3. Decisions

### L1 — The local machine is an entry in the inventory, not a mode

There is no `if local` branch in the session path. The destination resolves to a helper
generation and a carrier; the carrier is `endpoint.Dial(ctx, dir, generation)` for this
machine and `nocx-helper bridge <generation>` over the ssh exec lane for another. Both hand
`client.Dial` a `HelperConn`, both perform the same hello / sentinel / hello-ok handshake,
and both reach the same `session` service.

The alternative — a local carrier that skips the handshake because "we know who we are" —
was rejected: the handshake is what proves the binary answering is the generation we
installed (`D21`), and locally that is exactly as worth proving. A stale binary in
`~/.nocx` is likelier than a stale one on a server, because your own machine is where
builds land.

### L2 — Installing locally is the same installer, with the filesystem as its transport

The artifact is embedded in the app (`helper/deploy/artifacts`), so locally there is nothing
to upload: the install writes the same content-addressed directory the remote installer
writes, and the generation is the same content hash of the same bytes. One installer, two
transports. A second local-only install path would be a second answer to "which build is
serving", which is the question the content hash exists to answer once.

### L3 — No consent, and no surface that asks for one

The consent axis (`N3`, ADR-0034) exists for a **persistent footprint on somebody else's
machine**; that is the trade the owner took for the script tier, and the thing being
consented to is deploying a binary onto a host you reached over ssh. Locally there is no
deployment: the binary arrives with the app, under the account that is already running it,
and `D12` has already frozen the trust boundary at the Unix account. Asking a person for
permission to run, on their own machine, a part of the program they just started is theatre,
and a consent surface that always says yes teaches people to click through the one that
matters.

### L4 — There is no fallback, and the refusal is a product surface

If the local helper cannot be installed, started or reached, nocx **does not open the pane
by another route**. It says, in the product: what failed, why, and what to do.

The distinction that makes this right, and it is not tidiness: a remote host is an
environment we do not control, so a helper failure there must degrade to the script tier —
that is `AD-5`'s whole purpose. The local machine is not an environment, it is an
**installation**. A daemon that ships with the app and will not start is a broken nocx, and
a second, differently-behaving terminal opened quietly in its place is how that breakage
survives for a month and is then debugged from the wrong end.

`internal/transport`'s `openRefusal` is the carrier for it and needs no new mechanism: it
exists precisely so an answer the open path produced itself keeps its sentence instead of
being flattened into "Internal error". Three sentences, and the third is the one usually
missing:

- **what failed** — the install, the start, or the handshake;
- **why** — the concrete error, not a category: no space on `~/.nocx`, the binary is not
  executable, the endpoint did not answer within the sentinel budget, a different
  generation holds the socket;
- **what to do** — retry, reinstall, free space, or read the daemon's log at the path we
  name. A refusal that cannot name an action is a bug in the refusal, not a fact about the
  failure.

**The refusal is raised at the act, not as a nag.** A probe that fails at coordinator start
is recorded and surfaces when a person tries to open a pane. It does not raise a
notification, because a person who has not asked for a terminal has not yet been harmed and
a startup toast about a daemon is noise they cannot act on.

### L5 — A replacing coordinator asks before it deletes

This epic repeals the theorem `dropDeadSessions` and `closeOpenEntries` stand on — "a
session lives inside one backend process and cannot outlive it" — because it is what first
makes a LOCAL session outlive the backend. So the rule this document owns (lifted out of
`nocx-wrugm`, see that bead's notes):

**Three answers, never two.** The generation reports the session — **live**. A reachable
generation says it does not exist — **absent**. Nobody could be asked — **unknown**.

**`unknown` may never collapse into `absent`.** A refused connection, a timeout, a sealed
vault and an unreachable host are all `unknown`, and `unknown` deletes nothing. This is
`internal/app/session_reconcile.go`'s existing discipline — "A FAILURE IS NEVER A VERDICT",
and its `causeFor` cannot return a verdict at all, the type says so — applied to the local
generation, which until now could not fail to answer because it was never asked.

The failure-path assertion is the one worth writing first: with the endpoint unreachable at
start, a local pane's session is `unknown`, its ledger rows survive the start, and nothing
is deleted. Assert it by making the endpoint unreachable, not by calling the reconciler.

### L6 — `localPTYFactory` is deleted, not bypassed

`internal/app`'s `localPTYFactory` (`app.go:2472`–`2586`) is the second PTY owner, and after
this it has no callers. It goes. The check that keeps it gone is the same shape as the
endpoint package's no-TCP test: walk the source of the packages on the local open path and
assert `pty.NewLocal` is constructed in exactly one place — `internal/helper/session`.

A grep in a review cannot hold this; the whole point of `D11` is that a local special case
must be impossible to reintroduce by accident, and the only thing that makes it impossible
is a test that fails when someone does.

## 4. The order at start, unchanged from `D8` with one list entry more

1. List generations — **including this machine's** — derive and probe each endpoint.
2. Reconcile the durable pane → (generation, session) map against what live generations
   report, under `L5`, before the content store's startup sweep.
3. Attach and begin draining each live session's window where the destination is reachable
   without vault credentials. **Local is always in that class**: there is no ssh
   authentication to resolve, so a local session is never "pending unlock".
4. Open the store; start the recorder; record a `Skip` for what the window lost.
5. Install the current generation if absent; retire unheld ones; reconcile tombstones.

Step 5 is where a local first run installs, and step 1's probe is what makes a second run
cheap. Starting a daemon that is already serving is not an error and is not treated as one
(`cmd/nocx-helper/main.go`, `alreadyServing` → exit 0): the socket is the only authority
present on both sides of that race.

## 5. Deliberately out

- **Level 2** — durable TABS with their ledger rows, and reaching them from another machine.
  That needs a store, a schema and a writer, which is a server: `nocx-6ojko` onward.
- **Generation coexistence across an update** — two resident generations, retirement,
  tombstones, uninstall naming live work. That stays `nocx-wrugm`; this document owns only
  the reconciliation rule it needs.
- **Any change to the remote path's behaviour.**
- **The orchestration dispatcher's move into the helper.** It rides on this and belongs to
  the wave epic.
- **Windows and the platforms `deploy.ErrUnsupportedPlatform` names.** A machine with no
  helper artifact has no local helper, and what that means for `L4` is an open question
  below rather than a decision taken quietly here.

## 6. Open questions

1. **A platform with no artifact.** `L4` says no fallback; `ErrUnsupportedPlatform` says
   there are platforms we ship no helper for. Today that set is windows, 32-bit and the
   BSDs — none of which the app itself targets — so the two do not yet collide. If the app
   ever targets one, `L4` is re-argued rather than quietly excepted.
2. **What the daemon's log path is, and whether the refusal may name it.** `L4`'s third
   sentence promises an action; if the log is not somewhere a person can reach, the promise
   is empty.
3. **First-run latency.** Step 5 installs on first run: extraction plus a start plus a
   handshake, before the first pane opens. Whether that is felt is a measurement, not an
   assertion, and it belongs to the epic's first child.
4. **`Skip` for a local window.** Step 4 records what the window lost while the coordinator
   was away. Locally the coordinator being away is now the ordinary case rather than the
   exception, so the gap's presentation is exercised far more often than it was designed for.

## 7. Assertions — each one falsifiable

1. **A local pane's program survives its coordinator.** Start `sleep 300` in a local pane,
   stop the backend, start it again: the process's pid is unchanged and the pane reattaches
   to it.
2. **No second PTY owner.** The source-walking test fails if `pty.NewLocal` is constructed
   outside `internal/helper/session`.
3. **A refusal names an action.** With the endpoint unreachable, opening a pane produces an
   `openRefusal` whose message names what failed, why, and what to do — asserted as three
   distinct pieces, not as a substring.
4. **`unknown` deletes nothing.** With the endpoint unreachable at start, a local session's
   ledger rows survive and the pane is not dropped.
5. **The handshake is performed locally.** A binary at the install path whose content hash
   does not match the generation is refused, and the refusal says so.
6. **Starting twice changes nothing.** Two coordinators reaching for the same generation
   produce one serving daemon and no error surface.

## 8. What would falsify this design

- **If first-run latency is felt** — if the install-and-handshake before the first pane is
  perceptible — then step 5's placement is wrong and the install belongs before the window
  is shown, not in the coordinator's start order.
- **If `unknown` turns out to be the common answer locally** rather than the rare one, the
  endpoint is less reliable than a same-machine socket should be, and the cause is a defect
  rather than a case for a fallback.
- **If people routinely hit `L4`'s refusal**, no fallback was the wrong trade and the
  argument in `L4` has to be re-made against evidence rather than restated.
