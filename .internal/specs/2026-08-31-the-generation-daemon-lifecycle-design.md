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

## 1. What this reuses, and why almost nothing is new

**The front door already exists, and it is `internal/coordinator`.** The coordinator solved
this exact problem for itself in `nocx-ofwij` and `nocx-c4wek`, and the package is written as
if it expected a second caller: `RuntimeDir(storage.Paths)` derives the directory rather than
naming one, `Config` takes `Peers PeerCredentials`, `Owner PathOwner` and `SelfUID` as
injected seams with "a nil field is a configuration error, so no test can accidentally reach
the kernel", and the failures are already named — `ErrForeignOwner` ("the runtime directory is
not owned by this user"), `ErrPathTooLong` ("the socket path exceeds the platform's
unix-socket limit"). It holds a `fileLock` beside the socket.

So this design **generalises that package** rather than writing a second front door. AGENTS.md
is explicit about which of the two to do, and the alternative is the shape it warns about: two
implementations of "is this socket mine", agreeing everywhere anyone looks.

What is genuinely new is four things: surviving the carrier that started you; a _generation_
rather than a _profile_ as the unit; a machine namespace inside a possibly shared home; and
the retirement lease.

**Crossings.** AD-2 (one codebase, multiple targets) — no new target; this is `cmd/nocx-helper`
gaining a `daemon` and a `probe-prunable` mode. AD-8 — the seams above are injected, and the
`session` service the daemon serves is the one D15 of the remote-helper design reserved.
ADR-0034 — consent is unchanged; a daemon is not a consent boundary. The execution-host
design's D3 puts the retirement surface **inside the frozen ABI**, so §7 here is frozen too.

## 2. Decisions

### D1 — The daemon detaches, and its readiness travels back before the carrier closes

`nocx-helper daemon --generation <id>` is launched over the same pty-less exec lane the helper
uses today. It then:

1. `setsid`s into its own session, so no controlling terminal and no process group tie it to
   the lane;
2. keeps **one inherited pipe** — the readiness pipe — and closes every other inherited
   descriptor;
3. takes the lifetime lock (§7) and binds the endpoint (D3) **before** reporting readiness, so
   a caller that reads "ready" can immediately connect;
4. writes one readiness record — the endpoint path, the protocol version and its content hash
   — to the pipe, then closes it;
5. redirects its own stdio to the log and **never reads the lane again**.

The launcher reads the readiness pipe to EOF. Three outcomes and no fourth: a readiness record
(the daemon is up), a diagnostic then EOF (it refused, and said why), or EOF alone (it died —
treated as a refusal, never as success).

**Parent death is not an event.** After step 1 the daemon has no parent worth watching: the
exec channel closes, `ssh` exits, and nothing is signalled. This is the single behavioural
change that makes D1a true, and it is why `Serve`'s EOF-return moves to the bridge (D6)
instead of ending the process.

**Rejected: a wrapper such as `nohup`/`setsid(1)` on the remote command line.** It is not
uniformly available, it gives the launcher no readiness signal, and it would make the exit
status of the launch indistinguishable from the exit status of the daemon. The binary already
runs on the far side; it can detach itself.

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
inferring something about another process's life. A daemon that exits early is not a
correctness failure either; the launcher observes the endpoint gone and starts another.

**Uninstall is the only case where an outsider ends a daemon**, and it is authorised: the
execution-host design's D14 lists the live work first and takes consent.

### D3 — The endpoint is derived, short, and machine-namespaced

```
$HOME/.nocx/run/<machine8>/<gen12>.sock      0700 dirs, 0600 socket
$HOME/.nocx/run/<machine8>/<gen12>.lock      the lifetime lock (§7)
```

- **`<gen12>`** is the first 12 hex characters of the generation's content hash — the same
  hash that names its install directory. It is _derived_, so a coordinator that knows the
  generation knows the path, with nothing to look up (execution-host D10).
- **`<machine8>`** is the machine identity, and it exists because a shared home is real: two
  same-platform machines resolve to **one** install directory, and a host-local lock on one
  cannot bind the other. Mutable state is namespaced; the content-addressed _artifacts_ are
  not, because they are identical by construction.
- **The machine identity is the one this repository already derives**, not a second one:
  `internal/contentkey/identity_linux.go` reads `/etc/machine-id` (falling back to
  `/var/lib/dbus/machine-id`) and `identity_darwin.go` reads `IOPlatformUUID` from `ioreg`. It
  is explicitly not a secret there, and it is not one here. It is hashed and truncated to 8
  hex for the path; the full value plays no part.

**The length is checked, not assumed.** `sockaddr_un.sun_path` is ~108 bytes on Linux and ~104
on macOS, and this is not theoretical: measured 2026-08-31, `nocx-server` refuses to start
under a deep home with "coordinator: socket path exceeds the platform's unix-socket limit: 161
bytes". The derived path above is ~30 bytes under an ordinary home, but the check is
`ErrPathTooLong` — the coordinator's own — and it **refuses visibly** rather than truncating a
hash into a collision.

**Establishment is the coordinator's, unchanged**: verify the directory is owned by this user
and is not a symlink (`ErrForeignOwner`), create it `0700`, bind, and take the lock. A stale
socket — a path that exists but refuses connection — is unlinked and rebound **only while
holding the lock**, which is what makes "stale" a proof rather than a guess: the lock is free
exactly when no daemon owns it.

### D4 — The Unix account is the authorisation boundary, and it is stated as such

Peer credentials over the socket, compared against `SelfUID` — `internal/coordinator`'s
`PeerCredentials` seam, unchanged. A foreign uid is refused. The `0700` directory and `0600`
socket are the real gate, exactly as they are for the coordinator's own door.

**And the honest limit: anyone who can log in as that account can attach to its sessions.** No
capability, no token, no per-workspace secret. This is tmux's boundary and it is the right one
here, because a peer with that account can already read the PTY by other means. It is written
down because the execution-host design's D15 reserves an opaque `WorkspaceID`, and a reader
might take that reservation for an authorisation boundary. It is not one; whether it becomes
one is document 2's question.

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

This is where today's EOF behaviour lands. `Serve` returning on EOF is correct **for the
bridge**: the lane closed, so the bridge exits. The daemon behind it is untouched. That single
relocation is the difference between a helper that dies with its carrier and one that does not.

`direct-streamlocal@openssh.com` is used where the server offers it — one fewer process per
connection — and the bridge is the fallback that always works, including on servers with the
forward disabled. Neither is the primary: the _endpoint_ is primary and both are ways to reach
it.

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
- **A tombstone is always safe to delete.** Nothing looks under a tombstone name, so no daemon
  can have been started from one after the rename; and the rename itself only happened once
  the lock proved unheld. Reconciliation is therefore unconditional: any tombstone found on a
  later connect is removed, and an interrupted removal simply finds it again.
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
4. **The startup grace is bounded and self-correcting**: a daemon started and never given a
   session exits; the launcher's next attempt starts another and succeeds.
5. **Readiness precedes reachability**: for a thousand launches, a connect attempted the
   instant the readiness record is read never fails.
6. **A death before readiness is a refusal**: a daemon killed between `setsid` and its
   readiness write produces EOF alone, and the launcher reports a failed launch rather than
   waiting.
7. **A foreign uid is refused** on the endpoint, and a runtime directory owned by another user
   is refused with `ErrForeignOwner` rather than used.
8. **A path over the limit refuses visibly**: a home deep enough to exceed `sun_path` produces
   `ErrPathTooLong` and no truncated path is ever bound.
9. **Stale recovery needs the lock**: a socket file left by a killed daemon is unlinked and
   rebound; the same file with a _live_ lock holder is never unlinked.
10. **Two machines, one home**: with a live daemon on machine A, machine B's discovery,
    install-repair, retirement and tombstone reconciliation all leave A's lock, endpoint and
    sessions intact — because their mutable state is under different `<machine8>`.
11. **One writer**: a second `attach` requesting the write capability is refused and names the
    holder; when the holder's connection closes, the next request succeeds.
12. **Readers are unlimited**: many concurrent readers of one session see identical bytes at
    identical offsets, and none blocks another.
13. **`fresh` is not inferred**: `fresh=false` at offset zero requests no repaint, and
    `fresh=true` at a non-zero offset does.
14. **Retirement holds exclusion**: with the probe holding the lock, an attempt to start a
    daemon for that generation fails; after the rename the canonical path does not exist.
15. **A tombstone reconciles unconditionally**: interrupt the removal at each step; the next
    connect completes it, and a re-installed identical generation coexists with the tombstone.
16. **And the paired positive**: over a real SSH carrier, a daemon is launched, reports ready,
    survives its carrier being cut, is rediscovered by a **fresh** coordinator process, hands
    over the write capability, and is retired after its last session ends.

## 4. Deliberately out of scope

- **Any product behaviour.** This document defines who is alive, how they are reached and when
  they stop. What flows over the connection is the execution-host design's.
- **Cross-coordinator admission and workspace-scoped discovery** — document 2.
- **Windows remote hosts.**
- **Content-store restart reconciliation** — named in D8 step 2, and its own prerequisite.
- **A capability or token boundary inside one Unix account** (D4). Stated as absent rather than
  left to be assumed.
