# The generation daemon: how a helper outlives its carrier, and how it is found again

## 0. Why this document exists

`.internal/specs/2026-08-31-the-helper-as-execution-host-design.md` promises (D1a) that no
nocx update terminates a session or destroys the ability to reattach to it. **That promise is
currently backed by a process that exits when its SSH lane reaches EOF** — `host.go:154`
returns from `Serve` on EOF, `client.go:188` says "the lane is closed — the session dies", and
`launch.go:211` treats stream end as helper loss. Every word of the execution-host design can
be satisfied by an implementation that keeps doing exactly that, and D1a would be false.

This document is the mechanism. It is a prerequisite of that design's acceptance, not a
consequence of it.

**It owes ten answers**: daemonization and parent-death behaviour; deterministic endpoint
derivation and its short-path encoding; atomic socket establishment and stale-path recovery;
peer authentication and attach authorisation; durable generation-bearing handles; the machine
namespace for mutable generation state; reader ownership and the single-writer rule; the SSH
streamlocal and exec-bridge behaviour; reconnect after carrier loss and after coordinator
replacement; the lifetime and prune lock with its tombstone reconciliation; and shutdown rules
distinguishing bridge EOF, last detach, last session, explicit close and uninstall.

## 1. What this reuses, and what that reuse actually costs

**The mechanism exists; the protocol does not.** `internal/coordinator` solved the front-door
problem for the coordinator in `nocx-ofwij` and `nocx-c4wek`, and the reusable half is real:
directory ownership and symlink refusal (`ErrForeignOwner`), the `sun_path` check
(`ErrPathTooLong`), atomic bind with stale-socket recovery, the `fileLock` whose kernel-released
semantics §7 depends on, and the `PeerCredentials`/`PathOwner` seams with their platform halves.

**An earlier draft called this "almost nothing is new". That was wrong, and the code says so.**
The other half of the package is coordinator-specific and does not carry over: `handshake.go`'s
`Request`/`Response`/`Hello`/`Build`/`Backend` is the coordinator's discovery protocol — its
only response carries a WebSocket address and token — while the daemon's handshake is generation
identity, content hash and session-ABI version. `spawn.go` starts a **local** coordinator with a
resolved environment, where D1 launches over SSH through a stub. And `RuntimeDir` takes
`storage.Paths`, which a remote host does not have at all.

Worse for the "reuse" framing: the mechanism is **unexported methods on `Server`** and a private
`fileLock`. `lock.go`'s own comment already names the situation and the answer — a second copy
"is the cheaper of the two wrongs. **If a third caller appears, the answer is to lift one of
these into a package both import**". The helper is that third caller, and `internal/update` is
the second. So this is an extraction that changes shipped, working code the coordinator depends
on: real work with real regression risk, not a function call.

(The draft also cited "a nil field is a configuration error" as covering everything. It covers
the interface fields; `SelfUID` is a scalar and `NewServer` does not validate it.)

What is genuinely new is five things: surviving the carrier that started you; a _generation_
rather than a _profile_ as the unit; a machine namespace over both the artifact and its lock; a
host identity that does not fall back into a shared home; and the retirement lease.

**Crossings.** AD-2 — no new build target; `cmd/nocx-helper` gains `launch`, `daemon`, `bridge`
and `probe-prunable` modes. AD-8 — the seams above are injected, and the `session` service is
the one D15 of the remote-helper design reserved. ADR-0034 — consent is unchanged; a daemon is
not a consent boundary. The execution-host design's D3 puts the retirement surface **inside the
frozen ABI**, so §7 is frozen too.

## 2. Decisions

### D1 — Two remote processes: a launch stub that reports, and a daemon that survives

**Correcting this document's own first draft: `setsid` does not make `ssh` exit.** It does not
reparent the process, does not complete the remote command, and does not by itself close the
exec channel — a daemon holding the channel's stdout keeps `ssh` waiting. The draft's "after
step 1 … `ssh` exits" was simply wrong, and the mechanism it implied would have left the
launcher unable to distinguish a launched daemon from a hung one.

The readiness pipe lives on the **far** host, so carrying its result over an exec channel while
letting the daemon outlive that channel needs **two remote processes**:

1. **The launch stub** (`nocx-helper launch <generation>`) owns SSH stdout/stderr and creates a
   local pipe.
2. It starts the daemon with `SysProcAttr.Setsid`, passing **only the pipe's write end** through
   `ExtraFiles` and no SSH descriptor at all.
3. The daemon takes the lifetime lock and binds its endpoint (D3) **before** writing anything.
4. The daemon writes one bounded readiness record — endpoint, protocol version, content hash —
   and closes the pipe. It never touches SSH stdio.
5. The stub relays that record to SSH stdout and **exits**, producing a real exit status for the
   remote command, so the channel closes normally and `ssh` returns.
6. The launcher performs an **authenticated core handshake** against the endpoint, and only then
   reports success.

**Step 6 is not ceremony.** Binding a socket and writing "ready" proves the endpoint exists, not
that the protocol engine behind it is healthy. Launch success means _readiness record plus a
successful hello_; anything less is a launch that reports up and answers nothing.

The launcher's outcomes are then four, not three: a readiness record **and** a hello (up); a
readiness record with no hello (bound but not serving — a refusal, and a diagnosable one); a
diagnostic then EOF (it refused and said why); EOF alone (it died — a refusal, never success).

**This replaces a documented behaviour rather than assuming it away.** `ssh_helperconn.go:21`
exposes SSH stdin/stdout/stderr and waits for the remote command's exit status, and `:164`
documents that closing the lane ends the remote process. That contract still holds — for the
stub and for the bridge (D6). It stops applying to the daemon because the daemon never holds an
SSH descriptor.

**And the survival promise is scoped.** Ordinary OpenSSH supports this. A server using
`ForceCommand`, PAM session cleanup, or systemd/cgroup session scoping may kill descendants
regardless of `setsid`. Such a host is detected by the daemon being gone at step 6 and is
reported as unsupported for durable sessions, not silently retried.

### D2 — Five shutdown cases, and the first two are not shutdown

| event                                  | daemon                                                    |
| -------------------------------------- | --------------------------------------------------------- |
| **bridge EOF**                         | **keeps running.** The carrier is not the owner.          |
| **last attachment detaches**           | **keeps running.** Nobody watching is not nobody working. |
| **last session exits**                 | exits, releasing its lock and unlinking its endpoint.     |
| **explicit close of the last session** | same as above — the session ended, by request.            |
| **uninstall**                          | exits on request, after the product has listed what dies. |

**The rule is "owns no sessions", and it needs one bounded grace.** Between the launcher
starting a daemon and asking it to spawn the first session, the daemon owns nothing; without a
grace it would exit into the gap and the launcher would race it forever. So: a daemon that has
**never** owned a session exits after a bounded startup grace; a daemon that has owned one and
now owns none exits immediately.

That grace is a timer, and this design has refused timers elsewhere — deliberately, and the
distinction holds: it bounds _its own startup_, a span it controls entirely, rather than
inferring something about another process's life.

**But the transition must be atomic, and saying "the launcher just retries" was not enough.**
At the deadline exactly one of two things happens: a first-session admission wins and the daemon
stays, or shutdown wins and every subsequent spawn is refused. The listener enters a
non-admitting, draining state **before** it concludes the session count is zero, so no spawn is
ever acknowledged by a daemon that then exits. A caller either gets a session or a clean
refusal; it never gets a promise nobody keeps.

The grace's length is derived from measured launch-to-first-spawn p99, not chosen. A grace
shorter than that livelocks the launcher rather than merely costing it a retry, which is why
"the next attempt succeeds" was too strong as written.

**Uninstall is the only case where an outsider ends a daemon**, and it is authorised: the
execution-host design's D14 lists the live work first and takes consent.

### D3 — The endpoint is derived, collision-resistant, and in the same scope as its artifact

```
~/.nocx/run/<machine>/<generation>.sock      0700 dirs, 0600 socket
~/.nocx/run/<machine>/<generation>.lock      the lifetime lock (§7)
~/.nocx/helper/<machine>/<version>-<goos>-<goarch>-<hash>/
```

**The install tree is namespaced too, and that is the correction that matters.** An earlier
draft namespaced only the lock, which was actively wrong: machine B probes _its own_ lock, finds
it free because it is a different file, and retires the **shared** install directory under
machine A's live daemon. A machine-scoped lock cannot prove global disuse of an artifact that is
not machine-scoped. With both in one scope, every rule in §7 holds without any cross-machine
reasoning. The execution-host design's D5 carries the same correction.

**`<generation>` and `<machine>` are at least 128 bits each, encoded base32 (26 characters).**
An earlier draft used 12 and 8 hex characters — 48 and 32 bits — one paragraph above a sentence
saying this design refuses rather than "truncating a hash into a collision". It was doing
exactly that. A full-hash check inside the handshake detects an alias far too late: two
colliding generations would already be contending for one socket and one lock. The budget
affords it: `~/.nocx/run/` plus two 26-character names and a suffix is ~75 bytes under an
ordinary home.

**The length is checked, and this is not theoretical.** `sockaddr_un.sun_path` is ~108 bytes on
Linux and ~104 on macOS, and measured on 2026-08-31 `nocx-server` refuses to start under a deep
home with "coordinator: socket path exceeds the platform's unix-socket limit: 161 bytes". The
check refuses visibly; it never shortens an identifier to fit.

**The machine identity is nocx's own, obtained on the host.** `deploy.Probe` already runs on the
far side to resolve `goos`/`goarch` and returns the identity in the same round trip:
`/etc/machine-id` (or `/var/lib/dbus/machine-id`) on Linux, `IOPlatformUUID` on macOS.

**It must not come from `internal/contentkey`, and the reason is precise.** That package's
`machineIDOrMinted` **mints** an identifier and keeps it "beside the salt" in the **config
directory** when the host exposes none. For a content key that is a deliberate, documented
trade. Here it is inverted: under a shared home two machines would read the _same_ minted id —
the collision this namespace exists to prevent. Its macOS half also returns an `ioreg` error
rather than `errNoMachineID`, so it would not even reach its own fallback consistently.

**A host with no identity of its own fails conservative, not closed and not silent.** nocx
cannot prove such a home is unshared, so it uses one namespace and **refuses automatic
retirement**, saying so in the footprint surface. Generations accumulate visibly and removably
rather than one machine silently retiring another's live work. Refusing the helper outright
would cost containers and minimal images a feature they can otherwise have.

**Establishment is `internal/coordinator`'s logic, extracted** (§1): verify the directory is
owned by this user and is not a symlink (`ErrForeignOwner`), create it `0700`, check the path
length (`ErrPathTooLong`), bind atomically, take the lock. A socket that exists but refuses
connection is unlinked and rebound **only while holding the lock**, which is what makes "stale"
a proof rather than a guess.

### D4 — The Unix account is the authorisation boundary, and it is stated as such

Peer credentials over the socket, compared against `SelfUID` — `internal/coordinator`'s
`PeerCredentials` seam, unchanged. A foreign uid is refused. The `0700` directory and `0600`
socket are the real gate, exactly as they are for the coordinator's own door.

**And the honest limit: anyone who can log in as that account can attach to its sessions.** No
capability, no token, no per-workspace secret. **The design deliberately treats same-UID peers
as trusted** — which is a policy, and is stated as one. An earlier draft justified it with "a
peer with that account can already read the PTY by other means"; that is not reliably true,
since ptrace restrictions, process dumpability and a future Landlock/Seatbelt policy can all
prevent it. The boundary is a choice, not a consequence.

**And an opaque `WorkspaceID` can never become an authorisation boundary later.** The
execution-host design's D15 reserves one in the frozen ABI, and a reader might take that
reservation for a security seam. It is not one and **cannot be turned into one by document 2**:
a frozen helper accepts any same-UID peer forever, so an old generation still serving a live
session would keep doing so whatever a later document decides. Either the frozen handshake
reserves a real capability or token **now**, or the boundary stays the Unix account permanently.
This design reserves an identifier only, and therefore states the second.

Structurally, that means: the peer UID is authenticated **before** any `WorkspaceID` is decoded
or consulted, and changing or forging one neither grants nor denies access.

### D5 — Many readers, one writer, and `fresh` is explicit

**Readers are unlimited and stateless.** The window is capacity-reclaimed (execution-host D8),
so an attachment that only reads costs the daemon a connection and nothing else. Each reader
holds its own offset and independently receives reset-to-base.

**Exactly one attachment holds the write capability.** `attach` requests it; a second request
is **refused**, naming the current holder, not silently displaced. Refusal rather than
displacement is deliberate: displacement is a product decision the coordinator already makes
for its own clients (`session.displaced`), and a second, lower, divergent policy would be two
owners of one behaviour. Document 2 decides whether the capability can be taken across
coordinators.

**`fresh` is a flag on `attach`, never an inference.** A fresh renderer can attach at a
non-zero offset; a renderer can hold an offset after losing its screen. Only the caller knows,
and the repaint request (execution-host D9) hangs off this flag.

### D6 — Two carriers, one bridge, and EOF ends the bridge rather than the daemon

**Local**: the coordinator connects to the endpoint directly.

**Remote**: a per-connection **bridge** — `nocx-helper bridge <gen12>` over the pty-less exec
lane — connects to the endpoint on the far side and copies bytes between it and the lane. The
bridge is stateless and disposable: it holds no session, no window and no lock.

This is where today's EOF behaviour lands — and an earlier draft made it sound like moving one
`return`, which it is not. `Host` today **is** one connection: it owns `in` and `out`, request
cancellation, the service table, framing and inflight work (`host.go:32`), and EOF returns from
that whole object (`host.go:154`).

The daemon needs a **process-scoped registry** — sessions, their windows, the write capability —
**plus N connection-scoped protocol engines**. EOF then releases that connection's reader and,
if it held it, the write capability, while every piece of process state survives untouched. That
is a lifecycle decomposition of `Host`, and it is the largest single piece of work this document
implies.

`direct-streamlocal@openssh.com` is used where the server offers it — one fewer process per
connection — and **it is not implemented in this tree today**: `grep -rn streamlocal --include=*.go`
returns nothing, so it is new carrier work with negotiation and fallback, not reuse. The bridge
is the path that always works, including on servers with the forward disabled. Neither is
primary: the _endpoint_ is primary, and both are ways to reach it.

**Reconnect after carrier loss** is a new connection and a new `attach` at the last acked
offset. The daemon sees a reader go and a reader arrive; it distinguishes neither, because
under a capacity-reclaimed window it does not need to.

**Reconnect after coordinator replacement** is the same sequence, preceded by discovery (D7).
The write capability is released when its connection closes, so a replacing coordinator can
take it without arbitration.

### D7 — Discovery, retirement, and the reconciliation of a tombstone

**Discovery is a listing.** A coordinator lists `~/.nocx/helper/`, and for each entry derives
`<gen12>` and probes its endpoint under its own `<machine8>`. Reachable means live; that plus
`sessions` per generation is the whole inventory, with no index to keep consistent.

**Retirement is the execution-host design's D5**, whose steps are frozen ABI and are restated
here only where this document adds mechanism:

- The lock is `flock(LOCK_EX)` held for the daemon's life on `<gen12>.lock`. `probe-prunable`
  attempts `LOCK_EX|LOCK_NB`: failure means held.
- On success the probe **retains** the lock while the coordinator renames the install
  directory to `<name>.tomb.<nonce>`, then releases. The canonical path is gone before the
  lock is, so nothing can be started from it in the window.
- **A tombstone is safe to delete — and it is safe only because D3 put the artifact and the
  lock in one scope.** With the install tree machine-namespaced, the lock a probe takes governs
  exactly the directory the coordinator renames. Nothing looks under a tombstone name, so no
  daemon can be started from one after the rename, and the rename only happened once that lock
  proved unheld. Under the earlier draft's split scopes this claim was **false**: machine B's
  free lock said nothing about machine A's live daemon in the shared directory.
- A re-install of the identical generation recreates the canonical path beside the tombstone.
  They do not collide, because the tombstone carries a nonce.

### D8 — What a coordinator does on start, in order

1. List generations; derive and probe each endpoint; note which are live.
2. Reconcile its durable pane → (generation, session) map against what the live generations
   report, **before** the content store's startup sweep runs — which is the second blocking
   prerequisite and its own document.
3. Attach and **begin draining** each live session's window (execution-host D16), before the
   vault is open, because reading needs neither the vault nor `content.db`.
4. Open the store; start the recorder; record a `Skip` for whatever flowed while it was closed.
5. Install the current generation if absent; retire unheld ones; reconcile tombstones.

The order is the decision. Draining before the store opens is what keeps human time out of the
read gap; retiring last is what keeps a slow install from delaying a live session's recovery.

## 3. Assertions

1. **Bridge EOF is not shutdown**: kill the SSH connection carrying a bridge; the daemon's PID
   is unchanged, its lock is still held, and its session's process still runs.
2. **Last detach is not shutdown**: close every attachment; the daemon survives and the session
   keeps producing into its window.
3. **Last session exit is shutdown**: end the last session; the daemon exits, the lock releases
   and the endpoint is unlinked — asserted by a `probe-prunable` that then succeeds.
4. **Launch success means served, not bound.** A daemon that binds its endpoint, writes its
   readiness record and then fails to serve is reported as a **failed** launch, because success
   requires the authenticated hello. Asserted with a deterministic fault injected between bind
   and accept — not with repeated racing connects, which is probabilistic and passes by luck.
5. Faults injected at each of readiness-write, accept and hello each produce a distinct,
   diagnosable launch failure, and none of them reports success.
6. **The stub exits and `ssh` returns.** After a launch, the remote command has an exit status
   and the SSH channel is closed, while the daemon's PID is still alive and holds its lock.
7. **A death before readiness is a refusal**: a daemon killed between `setsid` and its readiness
   write produces EOF alone, and the launcher reports a failed launch rather than waiting.
8. **The startup grace is atomic**: with a spawn arriving exactly at the deadline, either the
   session exists and the daemon lives, or the spawn is refused and the daemon exits — never an
   acknowledged spawn on a daemon that then exits. Asserted by driving the race directly, not by
   timing.
9. **A foreign uid is refused** on the endpoint, and a runtime directory owned by another user
   is refused with `ErrForeignOwner` rather than used.
10. **The peer is authenticated before a `WorkspaceID` is consulted**, and forging or changing
    one neither grants nor denies access.
11. **A path over the limit refuses visibly**: a home deep enough to exceed `sun_path` produces
    `ErrPathTooLong`, and no identifier is ever shortened to fit.
12. **Identifiers do not collide by truncation**: endpoint and lock names carry at least 128
    bits, asserted on the encoding rather than on the absence of an observed collision.
13. **Stale recovery needs the lock**: a socket left by a killed daemon is unlinked and rebound;
    the same file with a _live_ lock holder is never unlinked.
14. **Two machines, one home, aligned scopes**: with a live daemon on machine A, machine B's
    discovery, install-repair, retirement and tombstone reconciliation leave A's **install
    directory**, lock, endpoint and sessions intact — the assertion that the earlier
    lock-only namespace would have failed.
15. **A host with no machine identity refuses automatic retirement** and says so, rather than
    namespacing two machines onto one identity. Asserted against a host with no
    `/etc/machine-id`, and asserted specifically to **not** fall back to `contentkey`'s minted
    id.
16. **One writer**: a second `attach` requesting the write capability is refused and names the
    holder; when the holder's connection closes, the next request succeeds.
17. **Readers are unlimited**: many concurrent readers of one session see identical bytes at
    identical offsets, and none blocks another.
18. **EOF releases connection state and nothing else**: with two connections attached, cutting
    one leaves the other's reader, the session, the window and the daemon's registry untouched —
    the decomposition of `Host`, asserted rather than assumed.
19. **`fresh` is not inferred**: `fresh=false` at offset zero requests no repaint, and
    `fresh=true` at a non-zero offset does.
20. **Retirement holds exclusion**: with the probe holding the lock, an attempt to start a
    daemon for that generation fails; after the rename the canonical path does not exist.
21. **A tombstone reconciles unconditionally**: interrupt the removal at each step; the next
    connect completes it, and a re-installed identical generation coexists with the tombstone.
22. **And the paired positive**: over a real SSH carrier, a daemon is launched through the stub,
    reports ready, answers a hello, survives its carrier being cut, is rediscovered by a
    **fresh** coordinator process, hands over the write capability, and is retired after its
    last session ends.

## 4. Deliberately out of scope

- **Any product behaviour.** This document defines who is alive, how they are reached and when
  they stop. What flows over the connection is the execution-host design's.
- **Cross-coordinator admission and workspace-scoped discovery** — document 2.
- **Windows remote hosts.**
- **Content-store restart reconciliation** — named in D8 step 2, and its own prerequisite.
- **A capability or token boundary inside one Unix account** (D4). Stated as absent rather than
  left to be assumed.
