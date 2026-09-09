# The local helper — your own machine is a host

**Epic:** `nocx-ie23r`. **Implements:** `D11` of
[`2026-08-31-level-1-the-helper-owns-the-host-design.md`](2026-08-31-level-1-the-helper-owns-the-host-design.md),
whose local half was decided on 2026-08-31 and never built. Owner decisions taken
2026-09-04. **Decision record:** [ADR-0057](../../docs/decisions/0057-on-your-own-machine-there-is-no-tier-a-fallback.md).

> **Revision 2, 2026-09-04, after an adversarial review (codex) whose findings were verified
> against the tree.** Revision 1 was wrong in four load-bearing places and each correction is
> marked **[R2]** where it lands. In short: `L5` claimed a rule that is already implemented;
> §4 resurrected a start order level 1 had already superseded; §2 called AD-5 untouched when
> the local half narrows it; and `L1`'s "one list entry more" understated the work by a wide
> margin. A fifth finding — the crash interval between the helper's spawn and the durable
> binding — is the hole this design did not have, and it is now §3.5.

## 0. What this is, in one sentence

The machine you are sitting at becomes an ordinary host in the helper inventory — same
install, same handshake, same session service, same window — differing from a remote host
in exactly one thing: the carrier is a Unix socket rather than an ssh exec lane.

## 1. What a user can do that they could not before

**Start a build in a local pane, quit nocx, open it again, and the build is still running
in that pane with its output.**

That is what the helper already gives a remote session and what a local one has never had,
because the backend spawns local PTYs itself and they die with it. The end-to-end check is
the epic's `nocx-ie23r.5`, through `cmd/nocx-server`.

## 2. What this crosses, and what those documents already decided

- **`D11` (level 1)** — "The helper runs on every machine, including yours. Locally the
  coordinator connects to its endpoint directly; remotely through the bridge. There is no
  second mechanism, no 'local special case', and no code path that exists only for one of
  them." This document builds that; it decides nothing about it.
- **`D12` (level 1)** — same-UID trust, frozen deliberately. Any nocx under that account may
  connect. Locally this is the whole authorization model, and `L3` does not add to it.
  **[R2]** `D12` answers _who may connect once a helper exists_; it says nothing about
  whether installing needs consent. `L3` is therefore a NEW decision, not a reading of `D12`.
- **§5 (level 1)** — the endpoint is a private Unix socket, `0600`, in a `0700` directory,
  never a loopback port, and `internal/helper/endpoint` has a test walking the source of
  every package on the path asserting no TCP listener exists
  (`no_tcp_listener_test.go`). **[R2] and the layout is NOT what `D3` of the
  generation-daemon design requires.** `D3` requires `~/.nocx/run/<machine>/<generation>.sock`
  with both identifiers at least 128 bits; the tree has no `<machine>` component
  (`endpoint.Dir`, `endpoint.go:110-117`; `deploy.installDir`) and names the socket with the
  first 16 hex characters — 64 bits (`endpoint.go:68-76`). The truncation carries an argument
  in the code ("this is a NAME and never a credential", confirmed by the handshake's full
  hash) and may stand. **The missing `<machine>` namespace carries none**, and `D3` says what
  it costs: machine B probes its own lock, finds it free because it is a different file, and
  retires the SHARED install directory under machine A's live daemon. A home directory shared
  over NFS between your laptop and your server is exactly that case, and this epic is what
  makes both ends helper hosts. Filed as its own bead; this design does not fix it and must
  not pretend the layout is settled.
- **`D2` (generation-daemon lifecycle)** — the daemon keeps running through bridge EOF and
  through the last detach, and exits when its last session exits, with one bounded startup
  grace. **[R2] None of that is implemented.** There is no last-session shutdown, no grace and
  no non-admitting drain state anywhere under `internal/helper` or `cmd/nocx-helper`; the
  daemon runs until SIGINT/SIGTERM. Locally that is mostly benign — you want it up — but two
  things follow and are stated rather than assumed: a generation never retires itself, so
  `D2` is a prerequisite of retirement rather than a thing to borrow; and any argument that
  reasons from "the daemon dies with its last session" is reasoning about a design, not about
  this tree.
- **`D8` (generation-daemon lifecycle)** — its start order is **superseded** and §4 uses the
  corrected one. **[R2]** Level 1 §4 records the correction, and records that codex retracted
  its own first answer to reach it: `Open` cannot ask, because asking needs a carrier, the
  carrier may need the vault, and the vault needs the store. **The store opens FIRST and
  judges nothing**; then carriers; then inventory; then each session is reconciled
  (`internal/app/session_reconcile.go:1-12`).
- **AD-5** — **[R2] this narrows it, and revision 1 claimed it did not.** AD-5's rule says
  Tier A "remains the substrate wherever no helper is installed" and names local operation.
  `L4` refuses the pane instead. That is a deliberate narrowing for one machine, recorded in
  **ADR-0057** rather than by editing AD-5: old records stay as they were true. Level 0 is
  not deprecated and every host that is not this one keeps Tier A.
- **AD-8** — one owner per behaviour. `internal/pty` does not disappear and must not: the
  helper's `session.LocalSpawner` already spawns through `pty.NewLocal`
  (`internal/helper/session/spawn_local.go:85`). What moves is the CALLER.

## 3. Decisions

### L1 — The local machine is an entry in the inventory, not a mode

The destination resolves to a helper generation and a carrier; the carrier is
`endpoint.Dial(ctx, dir, generation)` for this machine and `nocx-helper bridge <generation>`
over the ssh exec lane for another. Both hand `client.Dial` a `HelperConn`, both perform the
same hello / sentinel / hello-ok handshake, and both reach the same `session` service.

The handshake is performed locally too, where the obvious shortcut is to skip it: the
hello-ok is what proves the binary answering is the generation we installed (`D21`), and a
stale binary under `~/.nocx` is likelier on the machine where builds land than on a server.

**[R2] This is new code, and revision 1's "one list entry more" was false.** Every helper
route in the tree is gated on the destination being remote, by construction and not by
accident:

- `helperRegistry.OpenHosted` returns "not mine" unless `cfg.Kind == session.KindRemote`,
  then probes the host over ssh and resolves consent by host-key fingerprint
  (`internal/app/helper_git.go:455-470`).
- `sessionOpener` calls it only on the remote branch (`internal/transport/session_open.go`).
- Cold-start re-adoption refuses any persisted binding without `Host`, `ProfileID` and
  `HelperCommand`, resolves a saved SSH profile, rechecks remote consent, and adopts the
  result as `KindRemote` (`internal/app/session_readopt.go:129-190`, `:348-363`).

So the work is a local carrier, a local hosted-open route and a local durable binding that
carries a generation instead of a host and a profile. The FRAME stays "an entry in the
inventory" — that is what keeps a second mechanism out — but nothing about it is a
one-line addition.

### L2 — Installing locally is the same installer, with the filesystem as its transport

The artifact is embedded in the app (`helper/deploy/artifacts`), so locally there is nothing
to upload: the install writes the same content-addressed directory the remote installer
writes, and the generation is the same content hash of the same bytes. One installer, two
transports. A second local-only install path would be a second answer to "which build is
serving", which is the question the content hash exists to answer once.

### L3 — No consent, and no surface that asks for one

**[R2] A new decision, not an implication of `D12`.** The level-1 matrix says the helper is
installed "by consent" and describes a person opting a host in; this excepts one machine from
that, deliberately.

The consent axis (`N3`, ADR-0034) exists for a **persistent footprint on somebody else's
machine**. Locally there is no deployment: the binary arrives with the app, under the account
that is already running it, and `D12` has frozen the trust boundary at the Unix account.
Asking a person for permission to run, on their own machine, a part of the program they just
started is theatre, and a consent surface that always says yes teaches people to click
through the one that matters.

### L4 — There is no fallback, and the refusal is a product surface

**Recorded as [ADR-0057](../../docs/decisions/0057-on-your-own-machine-there-is-no-tier-a-fallback.md)**, which
carries the argument and the consequences. In this document, only what it means for the
build: `internal/transport`'s `openRefusal` is the carrier and needs no new mechanism, and a
refusal names three things — **what failed** (install, start, or handshake), **why** (the
concrete error, not a category), and **what to do**.

**[R2] "Three parts" is not testable as three non-empty strings**, which is what revision 1's
assertion would have accepted. The refusal carries a structured reason and a structured
action from closed sets — a `reason` naming the boundary that failed and an `action` naming
what the person can do — and the sentence is rendered from them. The test asserts the fields
over the real socket, and a new failure boundary that has no action in the set fails to
compile rather than shipping a refusal that ends at "why".

**The refusal is raised at the act, not as a nag.** A probe that fails at coordinator start is
recorded and surfaces when a person tries to open a pane. No notification: a person who has
not asked for a terminal has not been harmed, and a startup toast about a daemon is noise
they cannot act on.

### L5 — What is missing is a local inventory, not a reconciliation rule

**[R2] Revision 1 claimed this epic owns "ask before you delete, and unknown is not absent".
It does not, because that rule is already built.** `dropDeadSessions` and `closeOpenEntries`
no longer exist — they survive only in comments. `internal/content/reconcile_sqlite.go`
carries the three verdicts including `VerdictUnknown`, and
`internal/app/session_reconcile.go` states the discipline in its own header: "A FAILURE IS
NEVER A VERDICT", with a `causeFor` whose type cannot return one. That landed with
`nocx-k6p18.5`, after `nocx-wrugm`'s description was written, which is how revision 1 read a
description as a statement of the present.

What this epic actually owes is the thing that makes the rule reach a local session: **an
inventory for the local generation that can be asked, and a durable binding that says which
pane a local session belongs to.** Today no local session is ever reconciled because none
outlives the backend, so the local branch of every verdict is unexercised.

The failure-path assertion is unchanged and is the one worth writing first: with the endpoint
unreachable at start, a local session is `unknown`, its ledger rows survive, nothing is
deleted — **and the inventory was actually attempted**, which is the half revision 1 omitted
and which a safe default would satisfy without any local code at all.

**[R2] And the surface for it already exists.** `session_reconcile.go`'s co-change partners,
with support behind them, are `frontend/src/unreconciled-notice.tsx` and
`contracts/ledger.query.schema.json` — an unreconciled session is already something the
product shows a person and already something the wire carries. A local `unknown` extends that
notice; it does not get one of its own. Two surfaces for one state is the defect, whichever
wins.

### L6 — `localPTYFactory` is deleted, and it is more than a PTY constructor

**[R2] The type spans `internal/app/app.go:2296` to `:2673`, not the `:2472-2586` revision 1
cited**, and it is constructed at `:694`. Besides `NewPTY` it owns the shell-replacement
watcher (`:2634`), integration status reporting (`:2660`) and bootstrap-progress plumbing
(`:2673`). Each needs a destination — the daemon, the open path, or deletion — named in
`nocx-ie23r.3` before that bead is taken, because "move the PTY caller" is what revision 1
made it sound like and it is not that.

The check that keeps the second owner gone is a source-walking test over the packages on the
local open path, the same shape as the endpoint package's no-TCP test: `pty.NewLocal` is
constructed in exactly one place, `internal/helper/session`. **[R2] Uniqueness is not
reachability** — that test passes on a tree where local open is broken everywhere — so it is
paired with `nocx-ie23r.5`'s real open through `cmd/nocx-server`, which proves the
constructor is reached through the socket.

### L7 — The pane's claim on a session is durable before the spawn, or the spawn is not ours

**[R2] This decision is new in revision 2 and answers §3.5.**

The helper mints the authoritative session id, so the order is necessarily: the helper
spawns and exposes the session in its inventory; the coordinator adopts it; the coordinator
writes the durable pane → (generation, session) binding
(`internal/transport/session_open.go:283`, `:419`). A backend that dies between the first and
the third leaves the daemon holding a live PTY that no pane claims, and — with `D2`
unimplemented — holding it forever.

The wave record's own rule applies here word for word, and it was bought by the same shape:
the record exists **from before the first irreversible effect**. So a pane's claim is written
before the spawn, carrying an idempotency key the spawn also carries, and reconciliation on
the next start resolves the three cases: a claim with a session that the inventory reports
(adopt), a claim whose session the inventory does not have (close the claim), and a session
the inventory reports with no claim (**close it** — it is an orphan of a spawn nobody
recorded, and adopting it would attach an unknown process to a pane).

`SpawnParams` carries no such key today; adding one is `nocx-ie23r.1`'s, because the carrier
and the protocol are the same bead.

## 4. The order at start — the CORRECTED one

**[R2]** Level 1 §4 supersedes `D8`'s ordering, and this is that order with the local
generation in the list:

1. **The store opens and judges nothing.** `content.Open` performs only the sweep it already
   performs (`closeUnanchoredEntries`), which is this process's own work and not a session
   verdict.
2. List generations — **including this machine's** — derive and probe each endpoint.
3. Carriers come up. **Local is always reachable without vault credentials**: there is no ssh
   authentication to resolve, so a local session is never "pending unlock".
4. An inventory is asked of each live generation; each session is reconciled on the store's
   one connection (ADR-0043 untouched), under the three verdicts and `L7`'s claim rule.
5. Attach and begin draining; record a `Skip` for what the window lost.
6. Install the current generation if absent. **Retirement and tombstones are NOT borrowed
   here** — `D2` is unimplemented, so nothing retires itself yet, and that is `nocx-wrugm`'s.

Starting a daemon that is already serving is not an error and is not treated as one
(`cmd/nocx-helper/main.go`, `alreadyServing` → exit 0): the socket is the only authority
present on both sides of that race.

## 5. Deliberately out

- **Level 2** — durable TABS with their ledger rows, and reaching them from another machine.
  That needs a store, a schema and a writer, which is a server: `nocx-6ojko` onward.
- **Generation coexistence, retirement and tombstones, and `D2`'s daemon lifecycle** —
  `nocx-wrugm`. **[R2]** Revision 1 declared these out and then used step 5 of the old order,
  which required them. §4 no longer does.
- **The `<machine>` namespace and the socket-name width** — its own bead. This design names
  the divergence and inherits it; it does not resolve it.
- **Any change to the remote path's behaviour.**
- **The orchestration dispatcher's move into the helper.**
- **Platforms `deploy.ErrUnsupportedPlatform` names**, per ADR-0057's last consequence.

## 6. Open questions

1. **What the daemon's log path is, and whether the refusal may name it.** `L4`'s third part
   promises an action; if the log is not somewhere a person can reach, the promise is empty.
2. **First-run latency.** Step 6 installs on first run: extraction, a start and a handshake
   before the first pane. Whether that is felt is a measurement, and it belongs to
   `nocx-ie23r.1`.
3. **`Skip` for a local window.** Locally the coordinator being away becomes the ordinary
   case rather than the exception, so the gap's presentation is exercised far more often than
   it was designed for.
4. **[R2] Partial failure of the install sequence.** `deploy.Ensure` has a dozen boundaries —
   artifact selection, `Lstat`, removing an incomplete tree, `Mkdir`, temp create, write,
   sync, close, rename, directory sync, marker, verification read. Which leave a tree that
   the next attempt may reuse, and which leave one it must remove first? The invariant is
   owed as an interval with both ends and is not written yet; `nocx-ie23r.1` owes it.
5. **[R2] A detached child that starts while the caller is cancelled.** `endpoint.Ensure`
   returns on context cancellation without terminating the process it started. Is that
   daemon under a startup grace, and how does the retry tell it from a stale socket?
6. **[R2] Bind succeeds, then startup fails.** The listener is established before the
   instance id is minted (`cmd/nocx-helper/main.go:105-141`), so an exit there leaves a
   socket with nothing behind it. Which side repairs it, and how does a prober tell it from a
   daemon that is merely slow?

## 7. Assertions — each one falsifiable, and each one able to fail

**[R2] Every assertion below was strengthened. Revision 1's set could pass on a broken
product**, which the review demonstrated case by case; the rewrites are why each now cannot.

1. **A local pane's program survives its coordinator, WITH its output.** Start a program that
   prints a numbered line, then keeps printing while the backend is gone. Stop the backend,
   start it again. The pid is unchanged, the pane reattaches, and the lines printed before and
   during the absence each appear exactly once in that pane, with an explicit gap only where
   capacity was exceeded. (`sleep 300` emits nothing and would pass with replay, `Skip` and
   the recorder all broken.)
2. **`pty.NewLocal` has one constructor site AND local open reaches it.** The source-walking
   test proves uniqueness; `nocx-ie23r.5`'s open through `cmd/nocx-server` proves the site is
   reached through the socket. Neither alone is the assertion.
3. **A refusal carries a structured reason and a structured action**, both from closed sets,
   asserted on the real JSON-RPC error off the real socket, and the rendered sentence is
   built from them. Three arbitrary non-empty strings do not satisfy it.
4. **`unknown` deletes nothing, and the inventory was attempted.** With the endpoint
   unreachable at start: the local endpoint was dialled, the original pid still runs, the
   ledger rows survive, **no replacement shell was spawned**, and the pane presents its
   unreconciled state. A safe default that never asks fails this.
5. **The local handshake actually runs.** Put a responder on the expected generation's socket
   that answers with a wrong content hash; the client rejects that response and says so. (A
   mismatched binary at the install path hashes to a different generation and would be
   refused before any handshake — it proves nothing about this.)
6. **Starting twice leaves one daemon and two working clients.** One daemon pid; both clients
   complete the handshake; both see the same inventory; neither loses the other's attachment
   or write lease. "Changes nothing" is not an assertion.
7. **[R2] A spawn nobody recorded does not survive.** Kill the backend between the helper's
   spawn and the durable binding; on restart the pane is either reunited with that session
   through its pre-spawn claim, or the session is closed — never left live and unclaimed.

## 8. What would falsify this design

- **If first-run latency is felt**, step 6's placement is wrong and the install belongs before
  the window is shown.
- **If `unknown` is the common local answer** rather than the rare one, a same-machine socket
  is less reliable than it should be, and the cause is a defect rather than a case for a
  fallback.
- **If people routinely hit `L4`'s refusal**, ADR-0057 was the wrong trade and has to be
  re-argued against evidence rather than restated.
- **[R2] If `L7`'s claim cannot be written before the spawn** — because the helper's protocol
  cannot carry an idempotency key without a change nobody will accept — then the orphan
  interval is real and permanent, and this design needs a reaper with a policy for closing
  sessions it cannot attribute.
