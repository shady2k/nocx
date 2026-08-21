# ADR-0035 — the channel we own is the carrier, not the command line

- **Status:** accepted (2026-08-20)
- **Supersedes:** [ADR-0022](0022-the-ssh-command-line-is-the-carrier.md) in
  full. Its measurements stand and its implementation traps are carried forward
  below rather than discarded; what is reversed is the choice they were used to
  make.
- **Amends:** [ADR-0024](0024-authenticated-shell-integration-channel.md) — the
  amendment is in that file, dated the same day.
- **Reads, does not change:** `AD-5`, `AD-6`, `AD-8`,
  [ADR-0015](0015-ssh-g-as-the-ssh-config-oracle.md),
  [ADR-0025](0025-domain-request-carries-the-destination-not-the-options.md),
  [ADR-0004](0004-input-ownership-and-editor-abstraction.md) §1 and §7.
- **Related:** the integration-delivery-carrier design
  ([`.internal/specs/2026-08-20-integration-delivery-carrier-design.md`](../../.internal/specs/2026-08-20-integration-delivery-carrier-design.md)),
  the multiplex spike
  ([`.internal/reports/nocx-mlm7-spike-multiplex.md`](../../.internal/reports/nocx-mlm7-spike-multiplex.md)),
  `nocx-a1615`, `nocx-m8jwn`, `nocx-mlm7`.

## What the binding texts already decided

This ADR crosses several boundaries, so here is what already governs them,
before it says anything about what to build.

**`AD-5`** splits shell integration into two tiers: Tier A integrates a shell
that is already there, with no compiled artifact on the far host; Tier B is a
cross-compiled helper that augments a shell it does not own. This decision stays
wholly inside Tier A — what changes is how a shell bundle travels, never what is
deployed. (The delivery-modes design §8 recorded that `AD-5`'s "zero remote
install" needs amending, because script mode publishes a bundle by default. That
amendment is still outstanding and is not this ADR's to make.)

**`AD-6`** gives the backend the PTY and session lifecycle and the SSH
connections, gives the renderer the VT state, and forbids the backend to sniff
the byte stream. This ADR moves a payload and a secret off the SSH command line
and onto SFTP and auxiliary channels the backend already owns and never parses.
It decides nothing about who reads terminal bytes.

**`AD-8`** requires every module behind an interface wired at one composition
root, and requires variation to be expressed by the interface rather than by a
fork inside an implementation. The multiplex adapter is therefore another
implementation of the transport seam, not a mode flag inside the SSH module.

**ADR-0015** made `ssh -G` the oracle for `~/.ssh/config`: nocx does not parse
that file. Everything this decision must know before rewriting a line — whether
a `RemoteCommand` is configured, whether the user has expressed their own
multiplex policy, what the destination resolves to — is asked of `ssh -G`, not
read out of a config by us.

**ADR-0025** fixed `domain_request` at `host`/`user`/`port` and refused a
pass-through for the user's typed options, because an unbounded user-supplied
string in a composed command line is an injection surface with no principled
middle. This ADR does not widen that: on the typed path there is no composed
line to inject into, because we keep the user's own process and its own argv and
add two options of our own.

**ADR-0024** put the lifecycle on an authenticated channel that is not the tty,
and named the actor the per-epoch capability exists for: a descendant on the far
host that inherited the transport but was never handed the token. It left open
"whether the capability ever touches a named file", and said which answer it
preferred. That is the ADR this one amends.

**ADR-0004** §1 and §7 keep fail-open, and reject renderer-side echo suppression
and termios inference. Untouched: the line we submit stays visible, and it is
still the case that we do not hide the mechanism.

## Context

### ADR-0022 was right on its own terms

ADR-0022 asked whether to deliver integration through the SSH command line or
through a second channel opened by making the user's own connection a multiplex
master. It chose the command line, and on the question it was asked it chose
correctly.

The prize on offer was the length of the line on screen, because that is what
the owner had complained about. Multiplexing bought 158 bytes against 207 — the
same order, and the line does not disappear either way, because the payload
never travelled in it: it was already behind `$(cat …)`. Against a marginal
gain, multiplexing added preconditions the argv path did not have — the user's
config and the server must permit `ControlMaster`, the socket path must be short
and its directory writable, and a window opens between "the master is usable"
and "the file has landed". The argv path had no preconditions and no window. On
that ledger, the decision follows.

### Premise one: integration at first contact was mandatory

ADR-0022 weighed the preconditions as fatal because a session that failed to
integrate was a failure. Every precondition was therefore a way to lose.

That premise is gone. An un-integrated session is now an accepted, **named**
outcome: the user reaches a working native prompt, on the connection they
already authenticated, with a reason nocx can state. Once losing integration is
a legitimate ending rather than a defect, a precondition stops being a hazard
and becomes a branch — one that has to be detected honestly and named, which is
a smaller obligation than never failing.

### Premise two: the command line was free

The second premise was never examined, because nothing had yet asked what the
command line costs. Two costs have since been measured in this repository.

**Size.** The command we emit is up to 92,284 bytes — measured with the
identifier shapes the product actually mints and with the epoch and port at
their type maxima, which is the honest worst case rather than a typical one. A
consumer of an exec request has to serialize the command as one field of one
record — that is what an exec request is — so the size of our command is a
property other software must carry whole or not at all. A component that
publishes an unbounded value into somebody else's single-record field has not
made a size mistake; it has
failed to state a contract. It also sits at 75% of our own `maxFullLauncherLen`,
a ceiling that exists because a single argument is capped by the kernel, and one
this repo has already brushed once with 2 KB of headroom nobody had noticed
spending.

**Secrets.** The per-epoch capability and the one-shot recovery fence were
substituted into the rcfile text, and the rcfile text travelled in the command.
Measured per `ShellKind`: both appear verbatim for bash, zsh and auto. So both
reached the far host's process arguments, where any process of the same remote
user reads them with `ps` while they are still valid, and both reached any
recorder of the exec request — a discrete field of a record, indexed, forwarded
and retained on a schedule of its own. The code alongside them said "never
exported, never in the environment": the threat model had considered
`/proc/<pid>/environ` and had not considered argv. There is no size at which a
bearer token in a recorded command line becomes acceptable, which is why this is
not a payload-size problem and compression is not its answer.

### The same technique, wanted for a different reason

ADR-0022 wrote down that the technique it declined "is not rejected as
unworkable — it is measured, it works", and kept the measurements for whoever
needed them next. That is who we are, and the reason is not the one it was
measured against.

We do not want a shorter line. We want **a channel of our own on a connection
the user has already authenticated** — somewhere to put bytes that must not be
in a command, and a place to hand over a secret that must not be in argv.
Against that prize the 49 bytes are incidental, and the preconditions are
branches we now know how to name.

## Decision

**The command carries no payload and no secret; everything of variable size
travels as bounded frames on a channel nocx owns.** The command becomes a fixed,
auditable loader of at most 1 KiB, and it is the only remote command we emit —
952 bytes measured, for every `ShellKind`, against 92,284 before.

The exec request is **not** removed. A recorder has a legitimate interest in
what we ran, and making the command disappear from a record would be evasion
rather than hygiene. Short is a consequence of carrying nothing; honest and
carriable is the goal.

**And the bound is enforced where the command LEAVES the process, not only
where it is built** (`nocx-e4ir3`, added after the first implementation). The
original 92 KiB command was produced by a builder policing itself against a cap
that lived beside it — `maxFullLauncherLen`, 120 KiB — and the measured command
sat at 75% of it with nobody watching. Declaring 1 KiB in the producer
reproduces that arrangement exactly, one order of magnitude down: a
`RemoteLauncher` is a seam somebody else implements next, and a bound only its
current implementation applies to itself is a convention.

So all three seams that put a remote command on a wire refuse one at or above
the bound, before anything is sent and never by truncating: `session.Start` in
`internal/ssh` (the managed carrier, which then falls open to a plain login
shell with `ReasonCommandTooLong`), `discoveryConn.Exec` (the probes), and
`mux.Master.Session` (the typed path's control socket). The local-argv seam
needs no guard because it is closed by construction — the typed path never
appends a command to the user's `ssh` argv and refuses a configured
`RemoteCommand`.

The number is one number in three packages, because AD-8 forbids the imports
that would make it one symbol: `internal/ssh` must not depend on
`internal/shellintegration`, and `mux` is a leaf. `TestTheBoundOnARemoteCommandIsOneNumber`
in the composition root pins them equal, so raising any one alone goes red.

It is worth saying what the bound is NOT. It is not a mechanical ceiling —
every mechanical ceiling in this path is orders above it. Linux caps a single
`execve` argument at `MAX_ARG_STRLEN` = 131,072 bytes (measured: 131,071 passes,
131,072 is `E2BIG`), which binds on the far side where `sshd` runs
`$SHELL -c "<command>"`, and on the near side too whenever we exec `ssh`
ourselves; macOS has no per-argument cap but bounds `argv`+`envp` together at
`kern.argmax` = 262,144. The SSH transport caps a packet at 256 KiB in both
OpenSSH and `x/crypto/ssh`. Those are what a caller hits when nobody declared a
contract, and hitting one produces somebody else's opaque error — the far
execve's `E2BIG` — rather than our named refusal.

**For a saved connection**, the bundle is published over SFTP on the connection
nocx already holds. That is unchanged in shape; what goes is the second
delivery, which sent the same bundle again inline.

**For a typed `ssh`, we take ownership of the user's own connection by making
their own invocation the multiplex master**, adding our `ControlMaster` and
`ControlPath` to the line they typed and keeping their process, their argv and
their exit status. The agent, `ProxyJump`, an interactive password,
keyboard-interactive and 2FA, the host-key prompt, identity selection, port
forwards and their own `-F` and `-o` all keep working because we did not
reimplement any of them. There is never a second interactive session.

**Ownership is proven, never assumed.** It counts as proven only after a
successful mux handshake against that specific socket. Until then nothing is
published, no secret is delivered and no remote state is touched.

**The adapter is mux-only, with no fallback.** If the master refuses a session
request, the delivery is refused; the adapter never opens a connection of its
own. This is the direct consequence of a spike measurement: with the server's
`MaxSessions` at 1 the mux session request is refused, and the SFTP client —
even told to use the master — quietly opens its own connection and
authenticates again. A
second authentication can be a second password or a second 2FA prompt — a
credential use the user did not ask for, arriving as a side effect of a feature
they did not request. Refusing the delivery is the smaller harm, and it leaves
the user exactly where they were: at a working prompt on their own connection.

**Reusing a master the user already runs is rejected, not deferred**, for the
reason in the next section.

## The traps the spike measured, carried forward

None of these kills the approach and every one of them was found by measuring
the wrong shape first. They are here because this is now the transport, and each
is a way to ship something that looks fine.

**An over-long `ControlPath` does not degrade to no-multiplexing — it kills the
connection.** `ssh` refuses to start: `ControlPath too long`, no connection, no
session, no user's shell. The same is true of a socket directory that is missing
or unwritable: `unix_listener: cannot bind`, and `ssh` exits. This is the one
failure class that is worse than losing integration, and it is preventable only
by construction. So the path is built from a short prefix and `%C` — a
40-character hash, 74 bytes measured in full, bounded regardless of how long the
user's `$HOME` or hostname is — the directory is created before the line is
submitted, and if no safe short path can be built at all the session is decided
**raw before anything happens**, never attempted and recovered from.

**Liveness is not identity, which is why we do not reuse a master the user
runs.** `ssh -O check` answers, so liveness is checkable — and it answers `rc=0`
regardless of the destination named. Measured: with one `ControlPath` shared
between two destinations, the mux master accepted a mismatched session request
and executed it on its own connection; a push aimed at a different port landed
on the master's server, as a second subsystem session, with no new
authentication. The mux protocol does not isolate destinations; the control
socket **is** the trust boundary. `%C` makes a collision impossible by
construction for the sockets we create. Binding a socket we did not create to a
resolved route is separate work with its own threat model, and it would be
bought for a convenience nobody asked for — so it is rejected here rather than
left as a "later".

**An auxiliary process must set `ControlMaster` itself.** `ControlPath` is inert
unless `ControlMaster` is set, so an SFTP push that names only the path silently
opens a second fully authenticated connection. The first harness run did exactly
this and the no-second-authentication proof looked broken. With the option set,
twenty runs recorded twenty connections, twenty authentications and forty
sessions: the push added neither.

**The `ProxyJump` leg does not inherit command-line `-o` options.** The jump
child re-reads the config file, so options passed on our line govern the final
leg and not the jump. The final-leg multiplexing works regardless — the mux
destination is the final host — and the jump leg is governed by the user's own
configuration, which is where it belongs and which we must not override.

**A fallback must be `if/else`, never `exec A || exec B`.** In the
non-interactive shell sshd uses to run a remote command, `exec` of a missing
file terminates the shell with 127 and the `||` branch never runs — a session
that dies silently rather than falling back. Any fallback in the loader is
written as `if`.

## Consequences

**A typed `ssh` now leaves a control socket and a master process behind it**,
and they outlive the typed command. They are not a leak only because they have a
closing event: the ownership interval ends when the last nocx-owned session and
auxiliary channel have finished, or on a bounded idle policy, and the socket is
removed after the master's exit is confirmed under a bounded cleanup. Without
that closing event a socket and a process are a footprint with no end, which is
the shape this repo has paid for before.

**A later `ssh` typed plainly does not ride that master** — a plain invocation
never learns our `ControlPath`, so it opens its own connection and authenticates
as it always did. **A later typed `ssh` through the wrapper does**: it resolves
to the same `%C`-derived path for the same destination and joins the existing
master, which is the point of the mechanism and is also why the master's
lifetime must be a decided interval rather than an accident.

**Losing the socket, losing the master process and losing the underlying
transport are three different events** and are detected separately. The first
two, once integration is live, end integration and return the session to a
native presentation. The third ends the session: there is no prompt left to
keep, and claiming otherwise would promise an outcome we cannot deliver.

**A refusal is now something the product says out loud.** Every path that
declines to integrate — a configured `RemoteCommand`, a user who expressed their
own multiplex policy, a socket path that cannot be built safely, a refused
session, a refused subsystem — leaves a working native prompt with a named
reason, and the pre-authentication classes run the user's line with no nocx
remote effect of any kind.

**Nothing is minted before it can be used.** The capability and the fence are
generated only after the lifecycle receiver is ready and the publish attempt has
reached a terminal outcome, so a bearer is never handed across a boundary before
we have established that it has any use.

**The bundle is published exactly once per session**, over a channel, on both
paths. The managed path used to publish over SFTP, discard the result, and then
send the same bundle again inline; that second delivery is gone, and with it the
compact-carrier branch that was reachable only through a `RemoteLauncher` the
composition root never produced.

**The visible line changes shape but not its status.** It no longer carries a
staged payload and it gains two multiplex options. ADR-0004 §7 is untouched, and
hiding the line at render remains deferred and open (`nocx-4vyb`) — this ADR
neither does it nor rules it out.

**The nested child-domain composer is now the last caller of the full argv
launcher.** `internal/app/childdomain.go` composes a nested typed `ssh` inside
an already-integrated session, and it has no frame sender, so converting it in
the same step would have broken nested `ssh` with nothing to replace it. It is
retired together with the launcher and the argument-length cap in the same epic.
ADR-0025's decision is unaffected either way: its three-field shape is about
what the request may carry, not about how the line is carried.

**ADR-0022's forward-looking note survives its supersession.** It kept the
technique on the record for the relay, on the ground that a binary cannot travel
in a command line at all. That is still true, and the transport is now built
rather than merely measured — which brings the relay's starting point closer,
not further away.

## Revisit when

- **A destination-bound control socket becomes cheap.** If OpenSSH ever binds a
  mux socket to the destination it was created for, reuse of a master the user
  owns stops being a threat-model question and becomes an option worth
  re-costing.
- **The relay lands** (`nocx-if6` phase B). A relay that holds the PTY spawns
  the shell itself, so it hands over an inherited descriptor the way the local
  path does, and the frame delivery this ADR is built on becomes unnecessary for
  that tier rather than supplemented.
- **A supported platform has no readable `/dev/fd/N`.** The loader refuses to
  execute an unverifiable or unreachable stage-1 and falls back to a native
  login shell; if that becomes a common outcome rather than a rare one, the
  answer is a helper binary, not a weaker in-band tier.
- **A recorder appears whose limit is below 1 KiB.** The bound is ours to
  declare and we have declared it; a stricter one would be a deliberate change
  here, not a squeeze applied quietly to the loader.
