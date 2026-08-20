# The bundle travels on the channel, not in the command

- **Date:** 2026-08-20
- **Bead:** `nocx-a1615`
- **Revises:** [`ADR-0022`](../../docs/decisions/0022-the-ssh-command-line-is-the-carrier.md)
  (superseded), [`ADR-0024`](../../docs/decisions/0024-authenticated-shell-integration-channel.md)
  (closes one open question, adds one boundary),
  [the delivery-modes design](2026-08-05-nocxify-delivery-modes-design.md) §3.2–3.3
- **Reads:** AD-5, AD-6, AD-8, [`ADR-0015`](../../docs/decisions/0015-ssh-g-as-the-ssh-config-oracle.md),
  [`ADR-0025`](../../docs/decisions/0025-domain-request-carries-the-destination-not-the-options.md),
  [the multiplex spike](../reports/nocx-mlm7-spike-multiplex.md)

## 0. What a user can do that they could not before

**A host reached by typing `ssh` by hand comes up integrated, through a connection nocx
owns — and no session, typed or saved, puts the integration bundle or a session secret
into the SSH command.**

The end-to-end check: over a fixture sshd, a typed `ssh` and a saved connection both
reach an integrated prompt; the exec request recorded for each is under 1 KiB and
contains neither generation data nor a secret; and with the fixture's `MaxSessions` at 1
both still reach a working prompt, un-integrated, with a named reason and exactly one
authentication.

## 1. Why this document exists

**The managed path delivers the bundle twice.** `shellStartCommand`
(`internal/ssh/ssh_real.go:763`) calls `EnsureInstalledRemote`, which publishes over
SFTP, discards the result, and then unconditionally returns the full self-installing
launcher, which carries the same bundle again inline. The compact carrier
(`install_remote.go:243`) is reachable only when `RemoteLauncher == nil`, which the
composition root never produces.

**The command is a hazard by size.** `StartCommand` returns 91,198 bytes for
`ShellAuto`, 87,518 for bash, 87,633 for zsh, 85,934 for the minimal tier — 75% of our
own `maxFullLauncherLen` of 120 KiB, which exists because Linux caps a single argument at
`MAX_ARG_STRLEN`. That cap has been brushed before: at `nocx-z9s9.18` the measured form
was 112,676 bytes against a 114,688-byte cap, 2 KB of headroom nobody had noticed
spending.

There is a second bound, and it is ours to declare. A consumer of an exec request must
serialize the command as **one field of one record** — that is what an exec request is —
so the command's size is a property other software has to carry whole or not at all. We
declare no upper bound and emit up to 91,198 bytes. A component that publishes an
unbounded value into somebody else's single-record field has not made a size mistake; it
has failed to state a contract. So we state it: **the remote command is at most 1 KiB,
and everything of variable size travels as bounded channel frames.**

**The command text carries two secrets.** The per-epoch capability and the one-shot
recovery fence are substituted into the rcfile text, which travels in the command. A
probe against the real `StartCommand` confirms both appear verbatim. They therefore reach
the far host's process arguments — readable by any process of the same remote user — and
any recorder of the exec request. The code alongside says "never exported, never in the
environment": the threat model considered `/proc/<pid>/environ` and did not consider the
command line.

**And the compact path was never reachable**, which is why none of this was noticed:
`InstalledFactStore.Get` has no non-test caller — `deadcode -tags gtk3 -whylive` answers
"reachable only through reflection" — while the comment above it promises that "the
delivery planner chooses the compact installed line only when the fact says installed".
The fact is written and never read.

## 2. The shape of the mistake

This is not a payload-size problem, and compression is not its answer. The bundle already
travels over a channel built for it; the defect is that we then send it again over a
channel that is not. Every option that keeps the payload in the command — compression,
minification, one tier instead of three — buys headroom against a limit we cannot query
and spends it again at the next version.

For secrets the same reasoning is sharper: there is no size at which a bearer token in a
recorded command line becomes acceptable.

## 3. Decisions

**D1. The exec command carries no payload and no secret.** What travels is a short,
stable, auditable launch. It stays _meaningful_ to whoever records it — we do not remove
the exec request to make the command disappear from a record, which would be evasion, and
a recorder has a legitimate interest in what we ran. Short is a consequence; honest and
carriable is the goal.

**D2. The input method does not determine delivery — channel ownership does.** "Typed by
hand" and "saved connection" are ways of expressing intent, not delivery modes. A typed
`ssh` whose destination resolves is an ordinary integrated case.

**D3. Ownership is proven, never assumed.** For a typed `ssh` we wrap the user's own
invocation and make _that_ connection the multiplex master. Ownership counts as proven
only after a successful mux handshake against that specific socket; until then nothing is
published, no secret is delivered, and no remote state is touched.

The adapter is **mux-only, with no fallback**. The spike measured the trap: with
`MaxSessions 1`, `sftp -o ControlMaster=auto` silently opens its own connection and
authenticates again — the spike's table records "no-second-auth promise broken". A second
authentication can be a second password or 2FA prompt: a credential use the user did not
ask for. A refused session request refuses the delivery; it never opens a connection.

**D4. Secrets reach the shell through an inherited anonymous descriptor, and nothing
else.** Never argv, never the environment, never a named file, never the shell's history,
never a product log. §5 is the mechanism.

**D5. Every remote attempt is bounded, and residue is bounded in aggregate.** §7. The
claim is "nocx bounds its own remote work by explicit numbers", never "the remote host
cannot fall over" — the latter is not provable by any client, and claiming it would be
the unfalsifiable criterion this repo has been bitten by before.

**D6. Remote command discovery stays on**, bounded and shared rather than removed. There
is no measurement that it has harmed a host, and removing a working feature on an
unmeasured risk is descoping. What _is_ a defect is that its present budget bounds
nothing (§8).

**D7. Refusal is a closed set and each member carries a named reason.** **Once stage-1 is
running**, any integration refusal leaves a working native login shell, opens no second
connection, and asks for no second authentication. Refusals of the primary `session`, of
`pty-req`, and of the `exec` request are _session-level_ outcomes: each is named and proven
against a fixture, and for those a surviving prompt is **not** promised — the earlier
blanket wording promised more than §6.4 can deliver. It is also not claimed that a refusal
changes nothing: the stage that receives the secret necessarily decides whether an
integrated or a native shell starts. The interval opens at successful authentication and
closes at one of `integrated`, `conventional(reason)` or `session-failed(reason)` — the
third is not a variant of the second, because a session-level refusal never reaches a
prompt — no later than the integration deadline
(§7). A refusal decided _before_ authentication runs the user's line with no nocx effect of
any kind.

**D8. The capability defends against a descendant that inherited the transport, not
against the session's own participants.** It is scoped to one session and one epoch, so
anything already inside the session gains nothing from it. §5.4 states this precisely, and
states what is deliberately not claimed.

## 4. Delivery

### 4.1 The carrier

The remote command is a **bounded loader**. It is unconditional, capability-free, under
1 KiB, and it is the only remote command we emit.

It does **not** begin with `[ -x "$HOME/.nocx/launch" ]`. That guard, placed first, loses
a race it cannot win: on a host with nothing committed the publish is still in flight when
the test runs (§7 requires them concurrent), so the test fails, stage-1 never starts, and
the session degrades to conventional _while the publish succeeds_. The far side remains
the owner of "is this installation valid" — the guard and the full generation verification
simply run **after** the bootstrap and publish have settled, immediately before
`exec`ing the launcher.

The loader is deliberately small because everything stage-1 must do — read a length-framed
payload with a deadline, validate it, `mktemp`, open two descriptors, unlink, write, and
name a terminal outcome on every failure — does not credibly fit in 1 KiB of portable
shell, and asserting that it does would be the same undescribed transport one level lower.
So **stage-1 is itself a frame**, not part of the command:

1. **The loader is the sole owner of the original termios.** It saves it, installs the
   cleanup traps immediately, enters raw with echo off, and emits `LOADER_READY`. Stage-1
   never runs `stty -g` again — by then the state is the loader's, not the user's — and
   never emits `LOADER_READY`. If a handshake is wanted before the secret, it is a distinct
   `STAGE_READY`, so the backend always knows which one it received.
2. Frame 1 is stage-1, capped at **32 KiB**. The loader writes it to a bounded `0600`
   temp file, checks length and digest, then opens a descriptor, unlinks the name, and
   sources `/dev/fd/N`. A platform without a readable `/dev/fd/N` yields
   `stage-fd-unavailable` and a native login shell — an unverifiable or unreachable stage-1
   is never executed — the same unlink-first discipline as the secret, so cleanup has one
   shape and the file cannot accumulate. It counts against the residue budget. Stage-1
   carries no secret, so a name existing briefly is not a disclosure; uniformity is the
   reason, not confidentiality.
3. The digest is computed with `sha256sum`, else `shasum -a 256`. With neither available
   the outcome is `stage-digest-unavailable` and a native login shell: **unverified stage-1
   is never executed.** The installed launcher's hasher cannot be borrowed here — at first
   contact there is no installed launcher.
4. Frame 2 is the secret (§5.2), and only after §6.1's ordering has been satisfied.

Both frames are channel payload, not exec payload, so D1 holds; the framing, the caps and
the quarantine cover both. Delivering code in-band grants a same-user process on the far
host nothing it does not already have — it can write to that tty, and it can `ptrace` the
shell — which §5.4 names and does not claim to defeat; the digest is what stops a
_corrupted_ frame, not a _hostile owner_. The digest is not a secret: it names public
bytes, and knowing it yields nothing about either bearer.

Addressing arguments — environment id, session id, lane, domain, epoch, port, the
descriptor number, and the stage-1 digest — are names, not secrets, and travel in the
command.

### 4.2 A saved connection

Unchanged in shape, minus the second delivery: publish over SFTP on the connection we
already own. It uses the **same** ordering as a typed session (§6.1) — the publish and the
loader run concurrently and the schedule is identical; a saved connection is not a second
sequence to reason about. Publish failure is surfaced, never fatal.

### 4.3 A typed `ssh`

The submitted line is classified by the existing plan (`frontend/src/ssh-transition.ts`),
which already refuses shell operators, a remote command, `-T`, `-N`, `-f`, `-W`, `-D`,
`-O`, config queries and unknown grammar. §4.4 adds members.

For an accepted line we keep the user's own `ssh` process and its argv — so the agent,
`ProxyJump`, interactive password and keyboard-interactive/2FA, host-key prompts,
identity selection, port forwards, the user's `-F` and `-o`, and the process's exit status
all keep working — and add our own `ControlMaster` and `ControlPath`. The user's process
is the transport master **and** carries the interactive session; auxiliary channels for
the publish are opened on it after ownership is proven. There is never a second
interactive session.

**Reusing a master the user already runs is rejected**, not deferred. Liveness is
checkable — `ssh -O check` answers — but liveness is not identity: the multiplex socket
does not reject a request naming a different destination, which our own spike measured and
recorded as a security note. Binding a socket to a resolved route is separate work with
its own threat model, bought for a convenience nobody asked for.

### 4.4 Raw, and when

Raw is an outcome with a named cause, never a bucket, and it has two arrival times.

**Decided before anything happens** — the user's line runs with no nocx remote effect:

- the line is not an unambiguous interactive `ssh` transition (existing refusals);
- `ssh -G` reports a configured `RemoteCommand`: OpenSSH refuses to run both it and ours;
- the user expressed their own multiplex policy (`-M`, `-S`, or `-o` for
  `ControlMaster`/`ControlPath`/`ControlPersist`) — we never override it;
- no safe short control socket path can be built. This must fail _before_ trying: the
  spike measured that an over-long `ControlPath` does not degrade to no-multiplexing, it
  **kills the connection**;
- the per-session secret cannot be generated.

**Discovered only after authentication** — the user is already connected, so the session
continues as a plain shell and is never torn down. §6.4 is the full matrix.

## 5. How the secret reaches the shell

### 5.1 Why the obvious topologies fail

**A separate no-PTY channel for the frame** gives a clean pipe with no tty discipline, and
fails on ancestry: the receiver and the interactive shell are different children of sshd,
and a value passes between them only through the carriers D4 forbids.

**A forwarded loopback port** is circular: to authenticate the process that connects, you
need the capability you are trying to deliver. "The first connection wins" is a race a
same-user process wins by polling.

**Inverting the proof** — the shell demonstrating something only our child could have —
needs a root of trust the shell does not have. A challenge proves freshness, not
identity; anything that sees the challenge computes the same answer. A real inversion
needs a crypto primitive no portable shell is guaranteed to have, and shipping a verifier
is a compiled remote component, which is a different tier.

### 5.2 The mechanism

Stage-1 runs on the main PTY channel, so ancestry holds: it _becomes_ the shell.

1. Inherit the loader's saved termios and its cleanup context. Stage-1 does **not** save
   termios again: the loader already changed it, so a second save would record the
   loader's state as if it were the user's, and the restore would leave the terminal raw.
2. Emit `STAGE_READY` if the handshake is used.
3. Read frame 2 (the secret) under the frame protocol below.
4. Validate it against the expected session, domain and epoch.
5. `mktemp`, open a read and a write descriptor, **unlink the name**, then write the
   capability and the fence to the writer and close it.
6. Restore the exact termios and `exec` the launcher. **The read descriptor survives the
   `exec`**; its number travels in argv, which is not a secret.
7. Startup files stay capability-free. After the user's own startup has run — the ordering
   the tiers already promise — the hook reads the descriptor once, closes it, and assigns
   non-exported variables.

**A regular file, not a pipe**, and not by accident: this repo already moved the bash
rcfile from a process substitution to a real file, and `launcher_bash.go` records why. A
pipe reintroduces the short-read race that change bought out.

**Frame protocol.** A length prefix, an encoding that never requires holding binary in a
shell variable, and a deadline on _completing the frame_ rather than on each read. The cap
is enforced while accumulating, not after. EOF before the frame completes is
`bootstrap-interrupted`. A byte after a complete frame never becomes shell input. After a
terminal outcome a frame is never recognised again.

**Filesystem failure ordering**, which is the part that decides whether a secret can be
left behind. The name exists only between `mktemp` and `unlink`, and nothing secret is
written in that window:

1. `mktemp` fails — nothing on disk; restore; native shell.
2. the read descriptor fails to open — remove the name, close, restore.
3. the write descriptor fails to open — close the reader, remove the name, restore.
4. **`unlink` fails — write nothing at all**, close both, attempt removal, restore.
5. partial write or no space — close both; the name is already gone; restore.
6. closing the writer fails — the bootstrap did not succeed.
7. `BOOTSTRAP_ACCEPTED` is emitted only after a successful write _and_ close.

Cleanup is idempotent: it closes both descriptors, removes a name if one still exists, and
restores the saved state, however many times it runs.

### 5.3 Intervals

**Input ownership.** Opens _before_ the command is sent: the session is `bootstrapping`
and user keystrokes are **refused, not buffered** — a buffered keystroke is a command the
user did not knowingly run, executed later. Paste, IME composition and synthetic input are
refused on the same footing; window resize and other PTY control events are not user bytes
and keep working. A keystroke arriving simultaneously with the terminal outcome is
linearised: either before the close and refused, or after it and delivered exactly once.
The interval closes at exactly one terminal outcome — `BOOTSTRAP_ACCEPTED` or
`BOOTSTRAP_REFUSED(reason)` — and **input is re-enabled on that outcome, never on READY**.

**Loader resources.** A fourth interval, small and easy to forget: the stage-1 temp file,
its descriptor, the traps and the saved termios exist from the moment the loader creates
them until stage-1 has been sourced or the attempt has been refused — and they are closed
**before** the terminal outcome is emitted, so no outcome is ever reported over a
half-released terminal.

**Capability**, which is three intervals, not one, because they close on different events
and conflating them promises more than we can hold:

- _confidentiality_ opens at minting and closes at the **last** of the per-copy events,
  each named rather than summarised as "when no copy remains": the attempt's frame buffer
  closes at the terminal bootstrap outcome; the backend's expected value at invalidation;
  the descriptor at the startup hook's read or at bootstrap cleanup; the remote shell
  variable at observed hook teardown or session exit; lifecycle frame buffers at completed
  send or channel teardown;
- _validity_ opens at minting and is closed **hard** by backend invalidation — bootstrap
  refusal or timeout, domain teardown, epoch rotation, session teardown — after which a
  frame of that epoch is rejected. This is the interval we can guarantee;
- _retention of the remote copy_ closes at observed hook teardown or session exit. If the
  channel is lost we cannot guarantee prompt erasure of a variable in a shell we can no
  longer address, and this document does not claim we can.

**Recovery fence.** Its _confidentiality_ interval opens at minting, exactly like the
capability's — it is in backend memory and in the same frame long before bootstrap
succeeds — and closes per copy on the same event list, including after a _successful_
bootstrap: closing the authority interval does not by itself destroy every copy, and the
two must not be conflated. Its _authority_ interval is the
separate one: it opens once bootstrap has succeeded and the backend has registered it for a
domain generation, and closes at the first of the fence being sent once on channel loss and
acknowledged, teardown with no recovery needed, or a generation replacement. The shell
unsets its copy immediately after sending, so a second send is impossible; the backend
keeps the expected value until acknowledgement, because it is what validates the
acknowledgement.

### 5.4 What the capability defends, and against whom (D8)

The capability is scoped to one session and one epoch: outside them it is dead bytes, and
stale-epoch events are rejected. That single fact settles most of what looks like a trust
question.

**Anything that can observe and write the session's input already owns the session.** Our
own backend, the server the user chose and whose host key they accepted, and any
intermediary they connect through are all in that position — and a session-scoped bearer
grants them nothing they do not already have, because they are inside the session. This is
not a concession weighed and accepted; it is a tautology, and the earlier draft was wrong
to present it as a decision. A component that records the session records a credential
already expired by the time anyone reads the recording.

**The actor the capability exists for is a different one:** a process on the far host,
running as the same user, that inherited the transport but was never handed the token —
ADR-0024's descendant. Against it the token is the whole defence, and it is live and
valuable _during_ the session.

That is precisely why the present arrangement is a defect and why this design is worth
building. Today the token sits in the far host's process arguments, where that descendant
reads it with `ps` while it is still valid. It also sits in the command, which is a
discrete field of a record — indexed, forwarded and retained on a different schedule from
the session stream. The frame and the unlinked descriptor remove both.

What is **not** claimed: resistance to active inspection by a same-user process with
control of the shell — `/proc/<pid>/fd`, `ptrace`, a debugger. Mode bits prove nothing
there, ADR-0024 says so, and this document does not pretend otherwise. Ambient disclosure —
argv, the environment, named filesystem entries, history, product logs — is what we remove,
and removing it is what the descendant case turns on.

## 6. State machines

### 6.1 Ordering: nothing is minted before it can be used

A bearer is never handed over before we have proven it can be exercised. The order is
fixed:

1. mux ownership proven;
2. the publish and the loader start **concurrently**;
3. frame 1 received and verified;
4. the lifecycle transport and its receiver fully ready;
5. **the publish attempt reaches a terminal outcome** — committed, unchanged, failed or
   contended;
6. only after 4 _and_ 5 is the capability and fence pair minted;
7. frame 2 delivered to stage-1;
8. far-side verification of the generation as it now stands;
9. `exec` the launcher, or a named conventional outcome.

Step 5 exists to close a mutation race, **not** to let the publish decide validity. Without
it stage-1 can verify the manifest microseconds before an atomic commit and degrade a
session whose publish then succeeds — the same defect as the early guard, one layer along.
And the converse holds: after a `failed` publish the far side may still accept a generation
installed earlier, so a failed publish is not a refusal.

If the lifecycle channel cannot be opened, stage-1 receives a **non-secret refusal** or
exits on its bounded timeout, and **nothing is ever minted**. The earlier draft had the
secret delivered and then discarded when the lifecycle channel turned out to be refused,
which hands a bearer across a boundary before establishing that it has any use.

### 6.2 Master lifetime, and losing it

The ownership interval opens at the successful mux handshake and closes when the last
nocx-owned session and auxiliary channel have finished, or on a bounded idle policy. The
socket is removed after the master's exit is confirmed, under a bounded cleanup. Without
that closing event the socket and the master process are a footprint with no end.

Losing the socket file, the master process dying, and the underlying SSH transport dying
are three distinct events, detected separately:

- **before ownership proof** — the user's line runs as plain SSH; nothing published,
  nothing minted;
- **after proof, before integration is live** — native login shell, named reason;
- **after integration is live** — `channel-lost`: authoritative blocks, history and editor
  ownership stop, the fence is offered once, presentation returns to native.

The third row applies to the socket or the master dying. **Losing the underlying transport
ends the session** — there is no prompt to keep, and claiming otherwise would be an
outcome we cannot deliver.

### 6.3 Concurrent first publish

Local singleflight per resolved destination joins waiters to one attempt. **Singleflight is
per process and is not the boundary**: a saved session and a typed session, or a second
instance of the application, race across processes, and the remote lock is the only
authoritative arbiter — the fixed staging slot and stale-lock breaking must be proven
compatible with it. Because the lock is released between attempts, holding it is not by
itself a guarantee of a single commit: the version check is repeated **under** the lock, so
that at most one commit occurs per content digest.

The case that must not be left open is contention when **nothing has ever been committed**.
The loser cannot simply proceed, because the far side would find no generation and nobody
would move the axis out of `starting`. The loser therefore joins the local attempt within
the deadline, or terminates as `publish-contended`, or uses a verified older generation if
one exists.

### 6.4 Selective refusal

An intermediary or server may permit some channels and not others. The receiver is **not**
an auxiliary channel — it is the main PTY session — so the matrix is by real channel type:

| refused                                     | outcome                                                                                            |
| ------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| the primary `session`                       | no session at all; the user's line reports the server's refusal; nothing published, nothing minted |
| `pty-req` after `session`                   | no interactive shell is possible on it; refuse before any frame; `pty-unavailable`                 |
| `exec` after `pty-req`                      | see below — this row is the one that must be proven, not assumed                                   |
| `subsystem` (SFTP)                          | nothing written; native shell; `publish-unavailable`                                               |
| the lifecycle forward or channel            | nothing minted (§6.1); native shell; `channel-unavailable`                                         |
| any already-open channel, severed mid-frame | frame discarded, descriptors closed, termios restored; native shell; `bootstrap-interrupted`       |

**The `exec`-refused row may not simply promise a native shell.** Whether a `shell` request
can still succeed on the same channel after a refused `exec` request is a property of the
server, not something this document may assert. P4 proves it against a fixture; if it does
not hold, the row becomes an honest session-level failure rather than a silent downgrade.
Until it is proven, "the prompt survives in every row" is not claimed.

## 7. Bounded remote work

Per attempt, statically: no remote sleep loops, and no remote work whose duration is
decided by the remote host's state. The active generation is unchanged until the manifest
is atomically replaced.

|                                             | value                                    | why                                                                    |
| ------------------------------------------- | ---------------------------------------- | ---------------------------------------------------------------------- |
| `B` payload bytes per publish               | 256 KiB                                  | bundle is ~50 KiB; five times headroom                                 |
| stage-1 frame                               | ≤ 32 KiB                                 | measured stage-1 with room to grow, three orders below the old command |
| secret frame                                | ≤ 4 KiB                                  | a small delivery must not inherit a publish-sized ceiling              |
| master cleanup after the last owned session | 5 s                                      | the socket and process have a closing event, not an idle hope          |
| `T` publish wall-clock                      | 10 s                                     | past this the session is better served un-integrated                   |
| `K` lock probes                             | 5, at 50/100/200/400/800 ms — max 1.55 s | replaces ~400 metadata operations per waiter, and leaves `T` intact    |
| receiver READY                              | 3 s                                      |                                                                        |
| frame completion after READY                | 3 s                                      |                                                                        |
| integration deadline                        | 15 s                                     | `starting` can never be permanent                                      |

**The deadline arithmetic must close.** 3 + 3 + 10 exceeds 15, so the publish and the
receiver are **not** sequential: the publish runs on its auxiliary channel while stage-1
quarantines input and waits for its frame, and the deadline is proven against the parallel
schedule, not assumed. Local singleflight waiters perform **no** remote probes.

`T` expiring means: no new remote operation is initiated, the channel is closed, the
attempt fails, the shell still starts. It is not a promise to destroy kernel I/O in an
uninterruptible state.

**`N`, the count of remote operations, is a decision gate, not a blank.** P3 does not
implement against an unnamed ceiling: it measures first, brings the number back, and the
ceiling is written into this document and approved before the code that must respect it is
written. A plan may not close P3 while `N` is still a letter.

**`N` is deliberately not guessed here.** Application-level
FS operations are not SFTP packets: one write may split, extension negotiation adds
requests, and a tree removal scales with entries already present. P3 forbids unbounded
directory traversal, bounds inspected and removed entries separately, measures the maximum
trace of the happy path and of every failure path, and _then_ fixes `N` as a constant. The
assertion is that the measured maximum equals the recorded constant — which fails the
moment either moves, and is therefore a ratchet rather than a wish.

**Aggregate residue.** Today a failure before commit can leave a staging directory until
some future successful publish sweeps it; repeated failures leave several — each attempt
bounded, the total not. One staging slot per destination, reused or removed before a new
one is created, and a refusal to write when previous residue cannot be cleared.

## 8. Command discovery

The expensive part — enumerating executables on the remote `PATH` — is computed once per
cache key and shared; the genuinely session-local part (builtins, functions, aliases) is
enumerated separately with its own small budget, so we never claim a function from one
session exists in another. The key is the resolved route identity, the remote user, the
shell family, a hash of the effective `PATH`, and the integration generation.

**Invalidation is by event, not by clock.** Time is the wrong axis: the name set changes
when a package is installed or removed, which is rare, while a short expiry would restore
exactly the unbounded per-session enumeration this section exists to remove — ten tabs in
an hour would mean ten full scans instead of one. The signal is the `mtime` of each `PATH`
directory, since adding or removing an entry updates it; that is a handful of `stat` calls
against an enumeration of thousands of files, cheap enough to run every session. Any change
rescans. Replacing a binary in place does not move the directory `mtime`, and does not need
to — the set of names is what we cache. A change to `PATH` itself is already in the key.
The age bound is therefore a backstop against a filesystem that reports `mtime`
unreliably, not the mechanism. The cache is backend-owned **and lives only in memory**: it dies with the application, so a
working day of tabs to one host shares a single scan and a restart simply recomputes. That
is a natural bound and it costs nothing to maintain — there is no cross-restart state to
invalidate, and no file on our disk holding something recomputed in seconds. Discovery
grows no persistent footprint on either side.

The budget must stop the work. Today the 250 ms wait stops only _waiting_ — the pipeline
continues — and the exit cleanup group-kills only when the job is a process-group leader,
otherwise killing the subshell and orphaning the enumeration. The scan runs under a
supervisor owning its own process group; the deadline terminates the group, then kills it,
then reaps; a result is published only if it completed inside the deadline.

Discovery has numbers too, for the same reason the publish does — D6 keeps the feature,
which obliges it to be bounded rather than merely supervised:

|                                                               | value                               |
| ------------------------------------------------------------- | ----------------------------------- |
| scan deadline (whole group)                                   | 5 s                                 |
| session-local enumeration                                     | 250 ms and at most 4,096 names      |
| shared result                                                 | at most 8,192 names, 64 KiB encoded |
| `PATH` directory `stat`s per session (the invalidation probe) | at most 32                          |
| backstop age a cache is still served                          | 1 h                                 |

States are `running`, `ready`, `stale`, `timed-out` and `failed`, each distinguishable in
the surface. Today a missing snapshot always renders as "Command names are still loading",
which is true only while a scan runs. There is no `off` state: D6 keeps discovery on, and
inventing one would smuggle back the decision we rejected. A stale cache is served with a
bound on its age, past which it is no longer claimed as current.

## 9. Startup fidelity

Because we hand sshd a command rather than asking for a shell, it updates the login
records and skips the banner: **MOTD is never printed**, and `~/.hushlogin` is never
consulted to decide it should not be. Verified: no tier and no Go file in this repo
mentions `motd` or `hushlogin`.

The banner is the smallest member of a family, and the rest is already deliberate — each
tier declares an equivalence set in its source, and each declares a deviation:

- **bash** runs interactive **non-login** (`exec bash --rcfile <file> -i`), so
  `/etc/profile`, `~/.bash_profile` and `~/.profile` are not read at all, and
  `/etc/bash.bashrc` is replaced rather than sourced;
- **zsh** runs login, so the `/etc/zshenv`, `/etc/zprofile`, `/etc/zshrc` and `/etc/zlogin`
  phases run natively, but the user's `~/.zshenv`, `~/.zprofile` and `~/.zlogin` are not
  sourced — only `~/.zshrc`;
- **POSIX** runs login and is closest to native.

On a centrally managed fleet the bash deviation is the one that bites: `PATH` additions,
proxy variables, module systems and corporate profile scripts live in `/etc/profile`, and
a shell that never reads it behaves differently from every other shell on that host —
silently, and in a way the user attributes to the host rather than to us.

**Decision.** The set of differences is named in **one** place rather than three comments,
the banner is emulated with `~/.hushlogin` honoured, and the login-shell gap is closed or
declared with its reason. Anything left deviating is stated in the product, not only in a
source comment.

This is caused by passing a command at all, which D1 keeps; it is not caused by the
carrier change and is not fixed by it.

## 10. Binding texts this revises

**ADR-0022 is superseded.** It chose the command line and declined multiplexing, and on
its own terms it was right: the only prize was the length of the visible line, and
multiplexing bought 158 bytes against 207. Two premises have gone. Integration at first
contact is no longer mandatory — an un-integrated session is an accepted, named outcome —
and the command line has acquired costs the ADR did not weigh: an unbounded value in
somebody else's single-record field, and two secrets inside it. The ADR itself recorded
that the technique "is not rejected as unworkable — it is measured, it works". We want it
for a different reason than it was measured against: not a shorter line, but a channel of
our own on a connection the user already authenticated. The superseding ADR says this in
these terms and carries the spike's implementation traps forward.

**ADR-0024**: its open question "whether the capability ever touches a named file" is
closed in the direction it preferred — no named file, and installed scripts stay
capability-free. Its threat model gains D8/§5.4 explicitly: the exec-request recorder is
now out of scope for the bearer, the session-input recorder is in the trusted transport
boundary, and same-user inspection remains named and undefeated.

**The delivery-modes design §3.2–3.3** keeps its installed form and loses first contact as
an argv event: publication happens on a channel in both paths.

**One new observable consequence:** a typed `ssh` acquires a control socket and a master
process that outlives the typed command until §6.2's teardown. A later _plain_ `ssh` does
not ride it — that invocation never learns our `ControlPath` — but a later typed `ssh`
through the wrapper resolves to the same path and does. Socket, master process and that
second case all belong in the ADR.

## 11. Assertions

None of these may be satisfied by waiting on a duration: where a deadline is asserted it is
driven by an injected clock and an observable state change, never by wall-clock sleep.

**Carrier and payload**

1. For every `ShellKind` the emitted command matches an explicit allowlist grammar and is
   under 1 KiB — grammar, not only length, so an encoded payload cannot satisfy it.
2. A fixture recorder that stores the exec request as one field and refuses a field over
   its limit accepts every command we emit at a 1 KiB limit, and refuses today's.
3. The loader runs unconditionally: with `~/.nocx/launch` absent, stage-1 still starts and
   still reaches a terminal outcome. (The defect this replaces: a first-contact publish
   that succeeds while the session degrades anyway.)
4. Stage-1 is delivered as a frame of at most 32 KiB; an over-cap frame and a
   digest mismatch are each refused. Paired cases per hasher: with `sha256sum` present it
   succeeds, with only `shasum` present it succeeds, and with neither the outcome is
   `stage-digest-unavailable` and no stage-1 is executed.
5. `shellStartCommand` emits the same carrier whether the preceding publish succeeded,
   failed, or was not attempted.
6. With the far side's generation absent, corrupt, protocol-incompatible or hash-mismatched
   — one case each — the session reaches a _usable_ native prompt and no hook is sourced.

**Secrets**

7. A taint canary in the capability and in the fence appears in none of: the emitted
   command; the far host's argv; the environment; any directory entry under any remote root
   we write to, including the temp root; product logs; the shell's history — asserted per
   surface, not in aggregate.
8. No filesystem directory entry naming secret-bearing storage ever exists: the `unlink`
   precedes the first write, and a failed `unlink` results in no write at all.
9. A frame naming a different session, domain or epoch is refused, and a replayed frame is
   refused.
10. Bootstrap refusal, timeout, epoch rotation and session teardown each close the
    capability's **validity** interval: after each, a frame of that epoch is rejected. This
    asserts invalidation, not erasure of every copy.
11. The fence's confidentiality interval closes per copy on refusal, on timeout, **and
    after a successful bootstrap**; its authority interval closes on its own events —
    sent-and-acknowledged, teardown, generation replacement — one case each. Closing
    authority is asserted not to close confidentiality by itself.
12. Nothing is minted before the lifecycle receiver is ready: with the lifecycle channel
    refused, no capability and no fence are ever generated.

**Input ownership**

13. Between `bootstrapping` and the terminal outcome, keystrokes, paste, IME composition
    and synthetic input are refused and never delivered later; resize and other control
    events still work.
14. Input is enabled on the terminal outcome, not on READY.
15. Input arriving simultaneously with the terminal outcome is linearised: refused, or
    delivered exactly once — never both, never neither.
16. A partial frame, an over-long frame, and a byte trailing a complete frame each produce
    their named outcome, and the trailing byte never becomes shell input.
17. On every **catchable** exit path the restored termios byte-equals the saved termios and
    cleanup run twice behaves as cleanup run once. The paths are enumerated and each is
    injected: success; validation failure; each filesystem failure of §5.2; each catchable
    signal; and the loader's own — a partial frame 1, a temp-file failure, an absent
    hasher, a digest mismatch, and a signal arriving before control reaches stage-1.
    Uncatchable termination is out of scope: closing the SSH session closes the interval.

**Ownership and refusal**

18. On the typed path, no publish, no minting and no remote mutation occurs before the mux
    handshake proves ownership of that specific socket. A saved connection proves ownership
    by holding the SSH session itself and has no mux handshake to wait for; the assertion is
    scoped accordingly rather than asserting a step that path never takes.
19. The adapter never opens a network connection: with the socket refused, exactly one
    authentication is recorded and no second TCP connection appears.
20. Against a fixture with `MaxSessions 1` the session reaches a working prompt with a
    named refusal and one authentication.
21. Each pre-authentication refusal class runs the user's line with **zero nocx auxiliary,
    session, subsystem or publish operations** — plain `ssh`'s own operations are not
    counted.
22. Every option the user gave is preserved with its semantics, and the process's exit
    status is preserved exactly. (Not literal argv equality: the wrapper adds its own
    multiplex options by design.)
23. Each row of §6.4 produces its named reason; and the `exec`-refused row is decided by a
    fixture test rather than assumed — whichever way it resolves, the row states the proven
    outcome.
24. Each loss interval of §6.2 produces its outcome, and the three loss events are
    distinguished from one another.
25. The master's ownership interval closes: after the last owned session ends, the master
    exits and the socket is removed within 5 s of injected time.
26. A session moves out of `starting` to a terminal state within the integration deadline,
    on every path.

**Bounded work**

27. Fault injection at every FS-seam boundary, including a failing cleanup: the manifest is
    byte-identical to before the attempt, and owned residue is at most one staging slot.
28. A second attempt against un-clearable residue creates no second slot.
29. 100 concurrent local calls for one destination produce one remote publish; across
    processes, at most one commit occurs per content digest and no torn state is
    observable.
30. Two assertions, because equality alone would be satisfied by a constant set to any bad
    number: the measured maximum FS-seam calls and bytes written on each path is at or below
    the ceiling recorded in §7, **and** the constant in the source equals that measured
    maximum. The ceiling is a number in §7 before P3 implements against it (§7's gate).
31. Lock probes number at most `K` and total at most 1.55 s of injected time; a waiter
    joined by singleflight performs none.
32. The publish and the receiver are scheduled concurrently, and the deadline holds against
    that schedule — asserted on the schedule, not on a sum and not on a stopwatch.
33. Contention with nothing ever committed produces a named outcome, never a session left
    in `starting`.

**Discovery**

34. The scan's deadline terminates the whole process group, including a member that
    ignores `TERM` and requires `KILL`, and every member is reaped: after the deadline no
    enumeration process of that group survives and none is left a zombie.
35. A timeout publishes no partial snapshot; a cache hit starts no second scan; a cache
    older than its bound is not served as current.
36. Each of `running`, `ready`, `stale`, `timed-out`, `failed` maps to a distinct, asserted
    user-visible string — distinct enum values do not count.

**Startup fidelity**

37. Against a fixture with `/etc/motd`, an integrated session of each tier prints it
    exactly once, and nothing when `~/.hushlogin` exists.
38. For each tier, a variable exported only by `/etc/profile` is observable in the
    integrated shell, **or** that tier and that reason appear in a declared-deviation
    allowlist checked by the same test — so a new deviation fails until it is declared.
39. Every declared deviation is readable through the real wire result the product consumes
    — asserted at that seam, not by the existence of a component that could render it.

## 12. Work packages

The epic lands as **one change**: no partial release, and the pull request is opened after
every package is implemented. So no package may leave the product in a state that needs
explaining — in particular there is no interval in which the command is already short while
a secret is still inside it.

- **P1** Carrier selection on the managed path: one command from `LaunchOptions`, publish
  result no longer consulted; retires the full argv launcher from this path.
- **P2** The loader and stage-1: the bounded loader and its digest check, the input
  quarantine, the frame protocol, validation, the unlinked descriptor and its failure
  ordering, the trap/termios interval, and the capability-free reads in all three tiers.
  Includes the failure paths the mechanism creates: an absent or unreadable `/dev/fd/N`, a
  failed source, closing the stage descriptor, and a signal arriving between a successful
  digest check and the start of sourcing.
- **P3** Publisher budgets: measure and fix `N`, single staging slot, `K` backoff,
  singleflight, cross-process lock proof, byte ceilings, cleanup-on-failure.
- **P4** The typed-`ssh` wrapper: master creation, ownership proof, mux-only adapter, the
  new refusal classes, the master's closing event, and the fixture that decides the
  `exec`-refused row of §6.4.
- **P5** §6.1 ordering (lifecycle readiness and publish-settled before minting), §6.2 loss
  intervals and §6.4 refusal matrix, including teardown without a hung master, and the
  fence's own backend-copy events — its expected value lives until recovery acknowledgement
  or teardown, not necessarily until the capability's events.
- **P6** Discovery: process-group supervisor and real deadline, shared cache and key,
  honest states.
- **P7** The superseding ADR; ADR-0024 amended with D8; the delivery-modes design amended.
- **P8** The end-to-end check of §0.
- **P9** Startup fidelity (§9).
- **P10** (discovered here) Wire or delete `InstalledFactStore.Get`.

## 13. Out of scope

The relay tier, and the compiled remote helper §5.4 names as the only answer to a stricter
threat model. Compression or minification of any payload — this design removes the reason
for it on both paths, and reintroducing it restores a dependency on an unknown limit.
Hiding the exec request from a recorder by any means. Reuse of a master the user owns.
