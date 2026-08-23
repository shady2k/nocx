# ADR-0024 — The lifecycle leaves the byte stream

- **Status:** Accepted
- **Date:** 2026-08-08
- **Supersedes:** [ADR-0004](0004-input-ownership-and-editor-abstraction.md) §1
  (the marker rule), [ADR-0006](0006-marker-only-prompt-mode.md) §4 (`A → B`
  ownership), and the lifecycle half of `AD-5`.
- **Amends:** `AD-1` (what may cross the control plane), `AD-6` (who owns what).
- **Amended:** 2026-08-20 by
  [ADR-0035](0035-the-channel-we-own-is-the-carrier.md) and the
  integration-delivery-carrier design (D8, §5.4) — one open question closes and
  decision 10's threat model gains the actor it was always about. See
  **Amendment (2026-08-20)** below; decision 2's sentence about substituting the
  capability into the script text is retired there. Amended again the same day by
  the same design (§5.5), together with `AD-6`: decision 1 gains a **second
  carve-out** — the bootstrap window — and decision 4 gains the sentence that
  keeps its first condition from contradicting it.
- **Related:** `nocx-u7uh` (the epic that implements it), `nocx-mu8s` (the defect that
  found it), `AD-8`.

## Context

`nocx` reads the prompt/command lifecycle out of the byte stream as OSC 133
`A/B/C/D`, and hangs six things off it: input ownership, the live-region layout,
block boundaries, the command ledger, the environment stack, and the
`_shellIntegrated` latch that decides whether the block model applies at all.

OSC 133 is an **anonymous broadcast channel**. It is bytes on a tty, and every
process with that tty open can write it — a TUI, a `cat` of a hostile file, a
remote host's MOTD, a container's log, a filename, a stack trace. The stream
carries no writer identity and no Unix API attaches one: there is no "read these
pty bytes and tell me whose they are". We nevertheless treat every syntactically
valid marker as a statement by our own shell. `frontend/src/renderers/xterm.ts:87`
says so in as many words — "an untagged marker keeps driving block boundaries
exactly as before".

### How it surfaced

`omp` (opencode-go 17.1.2), an agent TUI that does not use the alternate buffer,
writes `ESC]133;B BEL` then `ESC]133;A BEL` during the repaint where it starts
working on a message. Captured under a bare pty with no shell in the picture, so
the attribution is certain. In nocx the live region collapses to `height: 0` and
the running program becomes invisible while still receiving keys. omp is not
misbehaving: it marks its own prompt, which is what the standard invites.

### What the neighbours do

- **Warp** emits `133;A`, `133;B` and `133;P;k=r` and **nothing else** — verified
  in the integration scripts embedded in its remote-server binaries. Its own
  comment calls them "standard prompt marker OSCs", used by
  `warp_update_prompt_vars` to route prompt bytes to the right grid. `133;C` and
  `133;D` do not occur anywhere. Its command lifecycle rides a private hook
  protocol instead — the hook vocabulary in the same binaries is `Preexec`,
  `CommandFinished`, `Bootstrapped`, `InputBuffer`, `ExitShell`, with fields
  `next_block_id`, `ps1`, `rprompt`. Both hostile sequences below were run in Warp
  and left its block intact.

  **This is protocol separation, not an out-of-band boundary.** If that private
  protocol is DCS, as its bootstrap suggests, it still travels through the
  terminal byte stream — DCS is an escape-sequence namespace, not a transport.
  What additional validation Warp applies is not established here, and nothing in
  this ADR should be read as a claim that Warp is vulnerable. What it does
  establish is that generic OSC 133 is nobody's lifecycle authority but ours.

- **VS Code** uses its own `OSC 633`, and its command-line report carries a nonce
  from `$VSCODE_NONCE`; an unverified command line is treated as untrusted. Also
  in-band, and also authenticated rather than merely renamed.
- **iTerm2** has had a remote-code-execution advisory (CVE-2026-41253) in which
  sequence handling acted without validating the origin of the sequences. Cited
  as the same design lesson, not as the same defect: the analogy is the missing
  origin check, and our case stands without it.

The common thread: a terminal that hangs behaviour off a stream sequence needs
some answer to "who said this". Nobody's answer is "whoever wrote the bytes",
which is currently ours. This ADR chooses the strongest available answer rather
than the most common one — nocx takes the lifecycle off the stream entirely
instead of authenticating it in place.

### What an attacker gets today

Ranked by severity, each with its path through this repo. None of it needs an
exploit chain; it needs a file with the right bytes in it.

1. **A trusted input surface, summoned by hostile output.** From `RUNNING_RAW`,
   `B, A, B` reaches `owned: true` — `B` leaves `RUNNING_RAW` for untrusted
   `PROMPT_READY`; the next `A` is _trusted_ purely because the state is no longer
   `RUNNING_RAW` (`input-state.ts:100`); the next `B` grants ownership
   (`input-state.ts:105`). `shouldShowEditor` is `owned && !nativeMode`
   (`native-mode.ts:7`), so nocx's own editor appears — while a foreign program is
   the foreground process reading stdin. What the user types there, believing they
   are at their shell, is submitted into the pty that program is reading. The
   editor resolves vault secrets into the line it submits, so this is a
   credential-disclosure path, not merely a spoofed prompt. **ADR-0006's
   marker-only mode amplifies it**: with `PS1` suppressed there is no native prompt
   left to compare against, so the forged surface is the only surface.
2. **Writes into the persistent history store.** A foreign `A` reaches
   `ledger.onMarker('A')` (`terminal-content.ts:1476`), which finalizes the running
   record (`command-ledger.ts:155-158`) and fires `onComplete`
   (`terminal-content.ts:915`) → `history.record` (`history-client.ts:54`). The
   record carries `trusted` (`history-client.ts:26`) — the same flag the sequence
   above launders — and the store's ack drives secret detection and its
   pending-capture offers. Arbitrary tty output can **prematurely finalize an
   app-opened entry and forge its status, timestamps and completion boundary**. It
   cannot choose the command text through this path — that comes from the
   app-owned editor. It chooses the verdict, and the verdict persists.
3. **A command that appears to have succeeded.** A foreign `D;0`
   (`terminal-content.ts:1487`) freezes the running block with an exit code the
   command never returned, and can pop a non-ssh environment
   (`terminal-content.ts:1494`). Everything the program prints afterwards is
   detached from the command that printed it.
4. **Hiding the foreground program.** The reported symptom: `setIdle()` →
   `.live-idle { height: 0 }` (`controller.ts:146`, `style.css:450`). The program
   still runs and still takes keys; the user cannot see what they are typing into.
5. **Forcing integration onto a session that has none.** Any marker latches
   `_shellIntegrated` permanently (`terminal-content.ts:1405`), which gates more
   than presentation — typed-`ssh` rewriting reads it at `terminal-content.ts:1020`.
   Hostile output selects which transformation path nocx applies to the user's
   next command.

### Why the obvious defences fail

- **Gate on the foreground process group** (`TIOCGPGRP`). It answers "who owns tty
  input now", never "who wrote these bytes". The counterexample needs no
  adversary: a program writes the marker, exits, the shell becomes foreground, and
  only then are the queued bytes parsed — the ioctl reports the shell and the
  foreign marker is accepted. A background writer gives the same false accept with
  no race at all. `tmux` collapses every pane to one outer pgid, `ssh` has no local
  pty to ask, and `set +m` puts shell and command in one group.
- **Tag OSC 133 and tolerate untagged markers.** The ambiguity then lives in the
  design permanently: each of the six consumers must remember to check, and the
  untagged path stays alive because the standard says it is valid.
- **Move the lifecycle to a private OSC.** This is worth doing for hygiene — a
  private grammar can reject everything unspecified, and foreign software stops
  colliding with us by accident. But it is a **namespace, not a boundary**. The
  bytes are still on the tty, so the token in them would be the only thing doing
  security work, and any capture of one valid frame replays forever. A private OSC
  as the root of trust would not eliminate the class; it would rename it.

## Decision

### 1. PTY output is render-only

No sequence parsed from the byte stream — standard OSC, private OSC, DCS, title,
terminal mode, or anything else — may grant DOM keyboard ownership, declare prompt
readiness, open or complete an execution attempt, assign an exit status, persist a
history record, enable integration-sensitive command rewriting, or authorize a
re-run.

OSC 133 `A`/`B` keep exactly one job: partitioning prompt bytes from output bytes
for rendering, and interop with other tools. `C` and `D` have no meaning to nocx.

OSC 7 (cwd) is unchanged, and "render-only" is not a promise that it is harmless:
it remains untrusted location metadata under its existing `AD-5` validation and UI
rules, feeding the location chip, duplicate-tab cwd and completion scope. It has no
input-ownership or lifecycle authority, which is all this ADR decides about it.

**One carve-out, and it is a rendezvous, not an authority.** A stream sequence may
_locate_ an already-authenticated lifecycle event in render order — see decision 7
— but may never create, authenticate, complete or assign status to an attempt on
its own. A fence with no authenticated event behind it does nothing at all. Written
down because the alternative reading forbids the only clean solution to render
ordering, and a future reviewer would be right to reject it under decision 1 as
otherwise phrased.

**A second carve-out, added 2026-08-20, and it is narrower than the first.** The
sentence above should now be read as "the first of two". This one is bounded by an
interval rather than by a grammar. Before an integrated remote session has a shell
there is a window in which nocx's own bootstrap program — a loader we wrote, sent
as the remote command, holding stdin and stdout on the PTY and nothing else — is
the only thing on the far end of the stream. In that window, and in no other, the
backend may read a closed set of fixed-prefix, length-framed tokens it defined
itself: the loader has taken the terminal and may be written to, stage-1 is loaded
and may be handed its frame, and exactly one terminal outcome. The window closes on
that outcome, the reader is closed with it, and it never reads that session again.
`AD-6` carries the same wording; the mechanism is the integration-delivery-carrier
design §5.5.

It is written down because the alternatives are closed rather than unattractive. A
sub-1-KiB POSIX shell script has no channel but the terminal it was handed — the
socket decision 4's diagnostic channel wants is one a portable shell cannot open,
and requiring one would decide at first contact, before we know anything about the
far side, that hosts without it get nothing. Decision 2's authenticated channel is
established **after** the bootstrap this signal exists to sequence, so it cannot
carry the signal that gates its own establishment. And there is nothing to infer
without reading: only a duration to wait, which is the answer this repo does not
accept.

**What it may not do, which is the whole of its licence.** Nothing on this reader
creates, authenticates, completes, revokes or assigns status to a lifecycle
attempt, mints or validates a capability, or gives the editor the keyboard. Like
the first carve-out it is a rendezvous; what it rendezvouses with is a program of
ours rather than an already-authenticated event. Its one effect on input is to end
the **quarantine** the bootstrap itself opened — and ending a quarantine is a
**return** of the keyboard to the state a plain `ssh` would have left it in, not a
grant. Inside the window there is no shell, no attempt and no domain; the lifecycle
axis of decision 6 is `Native` throughout it and is still `Native` when it closes.
If that ever stops holding — if anything a token reaches can leave that axis
anywhere but `Native` — this carve-out is void, and the design that needed it is
the thing that is wrong.

**Why this is not the private OSC this ADR rejected above.** That rejection was of
a **namespace offered as a boundary**: the bytes stay anonymous, so the token in
them does all the security work and one captured frame replays forever. This
carve-out claims no namespace and asks its prefix to do no security work at all.
Its boundary is the interval. Inside the window there is no shell, no user program
and no user keystroke, so any writer other than our loader is a process that can
already write the session's terminal — and such a process can read the same bytes,
so the tokens tell it nothing it could not have taken. Outside the window there is
no reader, so a captured token replays into nothing. Take the interval away and
this carve-out is the rejected idea again, which is why the interval, not the
vocabulary, is what a reviewer should check.

**The worst forgery, stated rather than buried.** A process that can write that PTY
can forge these tokens. What it gains is an early release of the input quarantine,
an early or a doomed delivery of stage-1 — public bytes with a public digest — or a
bootstrap that fails into a conventional session. What it does not gain is a
capability or a fence it could not already have taken, because the frame that
carries them travels on the same terminal it is already reading. There is one
strict gain and it is named rather than smoothed over: forging the readiness that
says stage-1 verified its frame can make the backend mint and send in a session
whose honest outcome was a refusal, producing a capability that would otherwise
never have existed. The design's §6.1 carries the three rules that shrink that to a
race — each token accepted once and only in order, no frame written after an
observed outcome, and hard invalidation on refusal or timeout — and no framing
closes the race itself, because winning it requires writing the session's terminal,
which is the position decision 10 already declines to defend against.

This is the whole decision. Everything below is how it is made true.

### 2. The lifecycle rides an authenticated channel that is not the tty

The contract is one sentence: **the shell reports its lifecycle over a transport
that is not the terminal, and no event is accepted without demonstrated authority
for the live integration domain.** The transport differs per environment behind one
interface; the contract does not.

Hostile _output_ cannot reach any of these transports — it writes to stdout, and
stdout is the tty. That is what removes the class rather than narrowing it.

**A domain is logical, and is never an alias for a transport.** An
`IntegrationDomain` is one authenticated shell or helper instance, carrying an
epoch and an optional parent; a `TerminalLane` is one input-routing lane with at
most one active domain; one transport may carry several domains. Activation,
suspension, restoration and closure are authenticated transitions, and an attempt
belongs to exactly one domain and cannot cross an activation boundary.

This is not built for the roadmap. nocx **already** has a nested environment stack
— ssh, sudo, su, docker, with passports — so a kernel that identified a domain
with its channel would not defer a future feature, it would silently regress a
current one the moment the passport machinery goes. What is deliberately **not**
built now is multi-lane discovery, routing and UI. The three properties that keep
the relay a third adapter rather than a protocol rewrite are cheap and are
required now: every envelope carries lane, domain and epoch; no API obtains them
from a singleton; the registry and the kernel are keyed by lane and domain even
while each adapter registers a single lane.

**Local shell.** A descriptor handed over at spawn through `exec.Cmd.ExtraFiles`;
the shell is already started by `exec.Command(shell, "-i")` + `pty.StartWithSize`
at `internal/pty/pty_local.go:160`. Descriptor discovery, direction, socket type
and shutdown ownership are open — see below.

**Over SSH, zero-install for supported shells, on a seam that already exists.**
`internal/ssh/ssh_tunnel.go:23` already defines `TunnelConn`: `Listen(addr)` asks
the remote sshd for a listening socket (`-R`), each accepted connection arrives as
a forwarded channel over the pooled connection `AD-5` already multiplexes, `Done()`
and `LostErr()` are a declared connection-loss contract, and its doc comment
already states that server refusal — `AllowTcpForwarding` off, or a bind outside
`PermitListen` — is a refusal rather than a dial failure. The remote hook connects
to that loopback port with bash's network redirection
(`exec {fd}<>/dev/tcp/127.0.0.1/<port>`, verified end to end here). The bind
address is the literal `127.0.0.1`, never `localhost`: the same file records that
a hostname bind is resolved by the server and cannot be verified locally.

Refusal is therefore detectable synchronously, before enhanced mode is offered.
It is **not** distinguishable — the ADR does not promise a diagnostic naming
`AllowTcpForwarding`, only a clean fall back to a conventional terminal.

"Supported shells" is the honest scope. bash with network redirection compiled in
is the first implementation; zsh needs `zmodload zsh/net/tcp`; POSIX `sh`, fish,
PowerShell and restricted shells need their own proven adapter or get nothing.
Failure to establish either the listener or the shell's connection leaves the
session conventional.

We request a loopback bind; the remote SSH server is trusted to honour it. That
bind is neither exclusive nor independently verifiable, which is one reason the
capability — not the address — is the authenticator.

**Nothing about the transport confers authority.** Access to it is not membership
in the domain: descriptor inheritance, discovery of the listening address, or the
mere creation of a connection must never let a descendant or another local user on
the remote host publish an event. The observable consequences are fixed here; the
wire representation is not:

- an inherited descriptor without domain authority produces no accepted event;
- an unauthenticated candidate connection can neither mutate nor preempt a live
  domain;
- authentication is established before any domain or sequence state is consulted
  or mutated;
- authority rotates with the epoch, and stale-epoch events are rejected;
- accepted events are replay-safe within their epoch.

Whether that is achieved by a bearer field per event, a MAC, non-inheritable
descriptors where a shell can guarantee them, or a helper that owns the key is an
implementation decision with its own bead. An ADR that fixed the wire format would
be wrong within a month.

The per-epoch capability is at least 256 random bits and is substituted into the
integration script text — `internal/shellintegration/inband.go:113` already
substitutes `@SID@` — never passed as an environment variable. Verified: a child
cannot read a non-exported shell variable, and a value that was never in the
environment is absent from `/proc/<pid>/environ`, which survives `unset`.

> **Amended 2026-08-20 (ADR-0035).** "Never passed as an environment variable"
> stands, and so does everything above about the environment. "Substituted into
> the integration script text" is retired: that text travelled in the SSH command,
> so the capability reached the far host's process arguments and every recorder of
> the exec request — the surface the sentence above does not cover. It now arrives
> as a bounded frame on the session channel and is read once from an unlinked
> descriptor. See the amendment below.

**Why the capability is mandatory rather than belt-and-braces, measured.** A child
of the shell inherits the descriptor: `bash -c 'exec {fd}>/tmp/x; …'` allocates fd
10 and the child still sees `10 -> /tmp/x` in `/proc/self/fd`. Bash's `{var}`
redirection is not close-on-exec. The transport stops everything that can only
write to the terminal; the capability stops a descendant that inherited the
transport.

`NOCX_SESSION_ID` stays exported and keeps its ADR-0006 §1 identity role. It is a
name, not a secret, and it authenticates nothing.

### 3. Establishment is a handshake, and "live" means past it

A listener existing is not a channel being live. The sequence is:

1. nocx establishes the transport (locally, hands over the descriptor; remotely,
   `TunnelConn.Listen`). Refusal ends here, in conventional mode.
2. The shell connects and sends an authenticated `HELLO` carrying epoch,
   capability, protocol version and shell kind.
3. nocx validates it and answers `ACCEPT`.
4. **Only after `ACCEPT`** may the shell suppress its prompt or emit lifecycle
   events.
5. Enhanced mode is entered only after the frontend has the accepted domain.
6. Timeout or any failure leaves the visible native prompt in place.

Without this, a shell can suppress its prompt while the accept loop, the validator
or the publication path is not ready — a shell with no usable prompt, which is the
failure ADR-0006 §5 exists to prevent.

The first authenticated connection claims the epoch. Later candidates cannot
preempt it. Failed authentication attempts are rate-limited, and connection count,
handshake size and handshake time are bounded, because on the remote side any
local user can open a socket to that port.

### 4. There is no in-band fallback tier

If no authoritative channel is established, the session is a conventional terminal:
native input, a visible native prompt, one continuous terminal grid and scrollback,
no command blocks, no command ledger.

**Render-derived "best effort", visual-only or disabled-action block-like grouping
is not a permitted fallback.** A block in nocx claims a command identity, a start
and an end, ownership of the output between them, a status and a duration. A grey
approximation withdraws the semantics while keeping the claim, and a user cannot
tell the two apart — which is the soft degrade this repo has already paid for once
(AGENTS.md, "A soft degrade must be visible in the product"). This does not forbid
ordinary terminal affordances that claim nothing about commands: search matches,
user-placed bookmarks, prompt decoration the shell itself draws.

We also do not ship a weaker private-OSC lifecycle beside the real one. It would
make "integrated" mean two different things, force every consumer to preserve the
distinction, and put the weaker path on exactly the unusual shells and remote hosts
with the least test coverage — while leaving the user unable to tell which
guarantee they have.

**A diagnostic channel is not a tier, and this decision does not forbid one.**
Written down because the sentence above is otherwise read as forbidding the only
way to tell three failures apart. A channel carrying UNAUTHENTICATED bootstrap
progress — how far the shell got through nocx's own startup, and nothing else —
is permitted, on three conditions that are the whole of what makes it safe: it is
not the terminal, it does not travel through the lifecycle codec (whose rule is
that every accepted envelope is authenticated, decisions 2 and 7), and no fact on
it may create, complete, authenticate or revoke anything. Its worst failure — a
forged or missing fact from a descendant that inherited the descriptor — is a
wrong sentence in a diagnosis, never a state the shell did not reach. Concretely
(`nocx-yww2`, `internal/bootstrapprogress`): the rcfile reports that it began and
that the user's own startup returned, and the pair distinguishes "the user's
startup took the shell" from "the shell never started" and from "our own bootstrap
broke" — three situations that are one indistinguishable ten-second silence when
the handshake bound is the only detector. The second fact is written **before** the
capability is substituted into the rcfile, so the diagnosis widens the window in
which the user's own rc could read the capability by exactly nothing.

**Amended 2026-08-20: the three conditions stand, and they do not reach the
bootstrap window.** Decision 1's second carve-out reads tokens on the terminal,
which is the first condition's exact prohibition, so the two must be told apart
here rather than left to a reader to reconcile. They are different things. This
channel reports the progress of a shell that already exists, to a human reading a
diagnosis, for as long as the session lives, and **nothing waits on it** — which is
why a wrong answer on it can only be a wrong sentence. The bootstrap window is a
two-party handshake with a program of ours, before any shell exists, and the whole
reason it exists is that something waits on it. The first condition is what keeps
this channel safe and it stays exactly as written: a diagnostic on the terminal
would be read while a user shell is running, when every process on that tty can
write it and there is no interval that ever closes. Where the two meet, the
stricter rule wins in both directions — a merely diagnostic fact may not be moved
onto the bootstrap reader to escape "it is not the terminal", and a token on the
bootstrap reader may not outlive the terminal outcome in order to become a
diagnostic.

### 5. The lifecycle is attempt-based, and a start may come from either side

Editor submit synchronously creates an `ExecutionAttempt` — attempt id, app-owned
command text, domain, cwd and host at submit, start time — **before** the bytes
that could cause the shell's own start event are written to the pty. That ordering
already exists (`terminal-content.ts:1138`, `:1170`) and becomes a tested
invariant rather than an accident.

Both origins are legitimate, because an authenticated `Start` is exactly as
attributable as an authenticated `Complete`:

- an authenticated top-level `Start` arriving with an app attempt pending
  **attaches** to it and may not replace its id, command text, cwd, host or secret
  representation;
- an authenticated top-level `Start` arriving with nothing pending creates a
  shell-originated attempt — this is what gives native-mode commands structure;
- a `Start` arriving while an attempt already runs is a nested event or a protocol
  violation, and never silently opens a second top-level attempt.

Attachment is bounded: same domain, exactly one pending attempt, exactly one
attachment, before any prompt-ready or loss event. Where a shell's hooks cannot
distinguish a top-level command from a hook-internal one, the protocol carries an
explicit attempt id rather than relying on ordering.

The command-text rule is a privacy rule, not only an authority rule. For an
attached app attempt the shell's text is ignored outright: the wire line may carry
vault-resolved secrets while the app's text carries references, and the code
already keeps those distinct (`terminal-content.ts:988`). For a shell-originated
attempt, whether its text is persisted at all is a **separate decision** — an
authenticated origin does not make a line containing a literal password safe to
store.

The interval, with its closing events named:

> An attempt is open from submit or authenticated start until an authenticated
> same-domain completion. Channel loss, session exit, tab close, native-mode
> escape or a confirmed environment change may **abandon** it as `unknown`.
> Nothing may mark it successful, and nothing may assign it an exit code it did
> not report.

Absence of a completion is not a timeout — commands legitimately run for hours.
An attempt becomes `unknown` only when an authenticated snapshot says the shell is
back at a prompt with no recoverable completion, or the domain is lost, or the
session ends.

### 6. Ownership is a state you can only be given, and the buffer is a separate axis

`state + trusted + owned` is replaced by two orthogonal axes, so that presentation
can never restore authority:

```
Lifecycle: Native | PromptReady(domain) | Running(attempt) | Desynchronized(domain) | Lost
Buffer:    Normal | Alternate
```

A `PromptReady(domain)` value exists only after an authenticated, sequence-legal
prompt-ready event for a live domain. The editor owns keys because the lifecycle
axis says `PromptReady`, not because a second boolean does.

The lane's active domain is a stack, not a variable. Entering a nested
environment **suspends** the parent rather than destroying it, restoring it takes
an authenticated activation rather than a pop of ambient frontend state, and
events from a suspended or closed domain are rejected against the active lane.
That is the same model decision 2 fixes, seen from the state machine's side: the
lifecycle and the domain stack are one reducer, because splitting them would
force whichever landed first to be rewritten by the other.

Keeping the buffer on its own axis is deliberate. Stashing the previous state
inside an `AlternateBuffer` value would let a program enter the alternate buffer,
have integration revoked underneath it, and restore a dead domain's authority on
the way out. ADR-0004 conflated ownership and buffer presentation; this ADR does
not carry that forward.

The trust-laundering transition (`input-state.ts:100`, `trusted: m.state !==
'RUNNING_RAW'`) is deleted rather than patched: it exists only because trust was
guessed from the previous enum instead of established by the speaker.

### 7. Validation precedes publication; corruption degrades, it does not tear down

Every event is checked for protocol version, live epoch, domain, monotonic
sequence, legal transition, matching attempt and payload bounds before anything
downstream sees it. Invalid events mutate nothing.

**Authentication terminates in the backend.** Raw framing and domain
authentication happen in Go, next to the transports; only schema-checked
published facts cross the control plane; no capability and no raw frame ever
reaches the renderer, which validates legal application transitions and can
construct no authority of its own. The backend already owns `ExtraFiles`,
`TunnelConn`, the capabilities and the candidate connections, so shipping frames
or secrets to the renderer would widen the trusted computing base for nothing and
make a second frontend harder than it needs to be.

This does not weaken `AD-6`. The backend parsing **its own protocol on its own
socket** is not sniffing the byte stream, and `AD-1`'s 2026-08-02 amendment
already permits typed, schema-checked facts crossing the control plane. The
renderer keeps owning VT state; what it loses is the ability to mint authority
from it.

Replay, precisely: the validator rejects duplicate or decreasing sequence numbers
after authentication; sequence state mutates only after authentication; a reconnect
never resets the counter within an epoch; a new epoch means a new capability and a
reset counter.

**A gap or framing corruption does not revoke an otherwise authenticated epoch.**
A descendant that inherited the descriptor can interleave garbage, and a rule that
revoked on any gap would hand every ordinary program a one-write kill switch for
enhanced mode — reported as flaky, not as secure. Instead the domain enters
`Desynchronized`: editor ownership is revoked, input routes raw, the terminal stays
visible, ordinary lifecycle events are **quarantined**, and nocx requests an
authenticated state snapshot.

Bounded resynchronization may scan forward for the next independently
authenticated frame, which is safe because unauthenticated bytes can never be
published. But framing recovery is not state recovery, and this is the part that is
easy to get wrong: an accepted `Complete` whose `Start` was lost would attach to the
wrong thing, and an accepted `PromptReady` whose `Complete` was lost would hand the
editor the keyboard over an open attempt. **Only a snapshot answering nocx's own
refresh request restores authority** — reconciling the open attempt, resolving it
as `unknown` where no completion can be recovered, and never inventing success.

Scanning is bounded in bytes and time, frames are size-bounded, candidates are
rate-limited, and a recovery budget exhausted by repeated corruption revokes the
domain. Availability against a descendant that continuously writes to the
transport is not guaranteed and cannot be; integrity and safe recovery are.

**Render ordering** is the other reason a snapshot matters. The lifecycle channel
and the pty are independent streams, and SSH preserves order within a channel, not
across them: an authenticated completion can arrive before the command's last
output bytes. Freezing the block on the event alone would truncate real output.
So logical completion comes from the authenticated event, and the _visual_ freeze
waits for both that event and a matching unpredictable fence written to the pty
after the output — the carve-out in decision 1. A fence alone does nothing; an
event without a fence completes the attempt logically and defers the output
boundary. Its worst failure is cosmetic, and it must never become a second
authority.

### 8. Loss fails to native visibly; local state is atomic, remote effects are not

In one local transition, nocx revokes ownership, exposes the terminal, marks open
attempts `unknown`, and stops accepting events for the dead domain.

Restoring the user's visible prompt is a protocol action, not a state change, and
it can only be promised when the shell is still reachable. If the SSH connection is
gone, the honest result is a disconnected terminal and no restoration claim. If
only the lifecycle channel failed while the pty lives, restoration must be
acknowledged before the session is treated as a usable conventional terminal —
otherwise the user is left at a suppressed prompt with raw input, which is the
worst of both.

The two losses are different and must not share a code path: **SSH transport loss**
ends the domain, the capability, the listener and the attempt, and a new session
gets a new epoch. **Frontend/backend reconnect** must either resume the existing
domain or report ambiguity and revoke it.

This decision fixes the property and leaves the mechanism open on purpose. The
mechanism chosen — a one-shot recovery fence pre-provisioned during the
authenticated bootstrap, matched by the renderer without inspecting the grid, and
acknowledged only once the conventional presentation is also applied — is recorded
in [`docs/lifecycle-protocol.md`](../lifecycle-protocol.md) §12.1, which is where
mechanisms live. Two alternatives were rejected there and are worth naming here,
because both would have contradicted decisions this ADR makes: the renderer
**observing** a repainted native prompt in the grid is untrusted inference and
reintroduces the render-state → control-plane edge decision 1 exists to sever; and
an acknowledgement that the renderer merely **applied** the conventional
presentation acknowledges the wrong thing, leaving reachable the suppressed prompt
taking raw input that this decision names as the worst of both.

### 9. Marker-only prompt requires a live authoritative channel

Prompt suppression is forbidden unless a domain is live in the sense of decision 3
— past `ACCEPT`, not merely connected. Suppressing the user's only native cue while
accepting readiness from an anonymous source is the phishing primitive in the
threat model above, and the two must never be separable.

A `Desynchronized` domain is not live. If corruption happens while the shell sits
at a suppressed prompt, waiting for "the next prompt" produces an invisible prompt
taking raw input; the refresh protocol therefore has an immediate response path,
and the shell restores a visible prompt until resynchronization succeeds.

### 10. The security boundary is stated, not implied

nocx defends against hostile bytes on the terminal from any source — files, logs,
MOTDs, remote output, container output, filenames, and ordinary TUIs that emit
prompt marks in good faith. That is the whole of the reported class.

nocx does **not** claim to defend against: a compromised shell or shell plugin; a
process that can inspect the shell's memory or the integration bootstrap as the
same user; a remote environment the user explicitly integrated, which is trusted to
report its own lifecycle honestly; or a compromised backend, renderer or validator.

Availability is bounded too, and stating it is part of being honest: a descendant
that can write to the lifecycle transport may force a safe transition to native
mode, and a descendant that can write to the diagnostic channel decision 4 permits
may spoil a diagnosis. Neither can produce a validated event without the epoch's
authenticator, and the diagnostic channel has no authenticator to produce one
with — which is why nothing may be built on it that a wrong answer would break.

That second list is irreducible in kind. We can authenticate **who spoke**; we can
never prove a compromised speaker told the truth.

## Amendment (2026-08-20) — how the capability travels, and whom it is for

Made by [ADR-0035](0035-the-channel-we-own-is-the-carrier.md) and the
integration-delivery-carrier design (D8 and §5.4). Two things change: an open
question closes, and decision 10 gains an actor it named only in passing.

### The capability touches no name, and no longer touches the command

The question this ADR left open — whether the capability ever touches a named
file — is closed the way it said it preferred. It touches none, and installed
scripts stay capability-free.

The mechanism, in one sentence: the bootstrap stage `mktemp`s, opens a read and a
write descriptor, **unlinks the name before writing anything**, writes the
capability and the recovery fence to the writer, closes it, and `exec`s the
launcher with the read descriptor surviving the `exec` — its number travels in
argv, which is a name and not a secret. After the user's own startup has run, the
startup hook reads that descriptor once, closes it, and assigns non-exported
variables. If the `unlink` fails, nothing is written at all; the name exists only
between `mktemp` and `unlink`, and nothing secret is written in that window.

What this replaces is worse than the open question assumed. The capability was
substituted into the integration script text, and that text travelled in the SSH
command — so the capability reached the far host's process arguments, where any
process of the same remote user reads it, and reached any recorder of the exec
request. Measured verbatim for bash, zsh and auto, together with the one-shot
recovery fence. The code beside it said "never exported, never in the
environment": the threat model had considered `/proc/<pid>/environ` and had not
considered argv.

### Decision 10 gains the actor, and loses a pretence

Three statements, in the order that decides how to read them.

**Anything that can observe and write the session's input already owns the
session.** Our own backend, the server the user chose and whose host key they
accepted, and any intermediary they connect through are all in that position. A
session-scoped bearer grants them nothing they do not already have, because they
are inside the session. **This is a tautology, not a concession weighed and
accepted** — it is written down in that form so that no later reader takes it for
a trade-off somebody made on the user's behalf. A component that records the
session records a credential already expired by the time anyone reads the
recording.

**The actor the capability exists for is a different one:** a process on the far
host, running as the same user, that inherited the transport but was never handed
the token — the descendant this ADR named when it made the capability mandatory
rather than belt-and-braces. Against that actor the token is the whole defence,
and unlike the participants above it is an actor for whom the token is live and
valuable **during** the session. That is exactly why the previous arrangement was
a defect rather than an untidiness: the descendant read the token with `ps` while
it was still valid, and it also sat in the command, which is a discrete field of a
record — indexed, forwarded, and retained on a schedule of its own, unlike the
session stream.

**What is removed is ambient disclosure, and only that:** argv, the environment,
named filesystem entries, the shell's history, product logs — each asserted per
surface rather than in aggregate. Decision 10's exclusion list is therefore
adjusted at three points:

- **the exec-request recorder is out of scope for the bearer**, because no bearer
  is in an exec request any more;
- **the session-input recorder is inside the trusted transport boundary** by the
  first statement above — not defended against, and never was;
- **same-user active inspection remains named and undefeated.** `/proc/<pid>/fd`,
  `ptrace` and a debugger attached to the shell all defeat an unlinked descriptor,
  exactly as they defeated a `0600` file. This ADR already said mode bits prove
  nothing there and the amendment does not pretend otherwise. The answer to that
  threat model is still a compiled remote component, and is still out of scope.

The distinction the last point turns on is worth stating plainly, because it is
the whole value of the change: ambient disclosure needs no privilege and no
intent — a `ps` while the session runs, a log somebody greps a week later. Active
inspection needs a process already able to attach to the shell it is inspecting,
which is the compromised-shell case this ADR excluded from the start.

## What this deliberately leaves open

- ~~**Whether the capability ever touches a named file.**~~ **Closed
  2026-08-20 (ADR-0035), in the direction this ADR preferred:** the per-epoch
  capability never enters a filesystem object under a name, and installed scripts
  stay capability-free. The condition attached to the preference is also
  discharged, and in the harder direction — decision 10 does now name same-user
  inspection as excluded, rather than resting on mode bits it never had a right
  to rest on. The amendment above is the whole answer.
- **The local descriptor's mechanics.** Discovery without exporting a secret,
  socket type (`SOCK_SEQPACKET` would preserve message boundaries where supported),
  direction, behaviour across a shell that `exec`s another shell, and shutdown
  ownership.
- **The wire format**, per decision 2: bearer field versus MAC, framing, and the
  descriptor number — POSIX `sh` guarantees only single-digit descriptors in
  redirections, and 3–9 collide with what user scripts use.
- **A forwarded unix socket** (`streamlocal-forward@openssh.com`) instead of a
  loopback TCP port, which would remove other-local-user reachability entirely.
  Whether the shells can write to one without a helper is the open half.
- **Persistence policy for shell-originated command text** (decision 5).
- **How common the no-transport environments really are.** POSIX `sh` remotes,
  `AllowTcpForwarding no`, `docker exec`. A research bead against real hosts, not
  a guess from an armchair: if it is common, Tier B moves up the roadmap.
- **Whether our scripts still emit OSC 133 `C`/`D`** for third-party interop now
  that we no longer consume them.

## Consequences

Deleted, not deprecated — the repo is greenfield and clean-only:

- the untagged-marker lifecycle path in `parseOsc133` (`xterm.ts:87`) and every
  consumer that reads `CommandMarker` kinds for authority;
- `_shellIntegrated` latching on any OSC 133 (`terminal-content.ts:1405`);
- the `trusted` boolean and its laundering rule (`input-state.ts:100`), and
  `trusted` as a field crossing to `history.record` (`history-client.ts:26`) —
  what persists becomes the attempt's domain-authenticated status;
- `ledger.onMarker` as an entry point for anonymous kinds (`command-ledger.ts:150`).

Added, in the backend: the channel, its handshake, its framing, capability
generation and substitution in `internal/shellintegration` and `internal/pty`; the
remote path built on the existing `TunnelConn` (`internal/ssh/ssh_tunnel.go:23`)
rather than a new one; the domain registry and the validator that terminates
authentication there. In the renderer: the published fact's type, the two-axis
state machine, and the projections that consume it.

The published fact gets a `contracts/` schema like any other result shape. The
channel's own framing does not: `contracts/README` scopes that directory to
JSON-RPC results, and the lifecycle channel is neither JSON-RPC nor necessarily
JSON. Widening it would be a deliberate change to that README, not a side effect
of this ADR.

This gives lifecycle authority **one publication boundary**. It does not shrink the
trusted computing base to one function: channel establishment, framing, the
capability and domain validator, the shell adapters and the attempt state machine
are all still trusted. What changes is that no ordinary renderer consumer can
manufacture authority out of terminal bytes.

## Revisit when

- The Tier-B remote helper is built (`docs/architecture.md:203`), at which point
  the environments with no transport today come back under this same contract,
  without it changing. Tier B is the lighter of the two remote binaries the
  architecture reserves: it augments a shell it does not own.
- **The relay lands** (`nocx-if6` phase B — a remote process that _holds the PTY_
  so a session survives a network drop). Then the remote case stops being a case at
  all: the relay spawns the shell, so it hands over an inherited descriptor exactly
  as the local path does, and the forwarded-port transport becomes unnecessary
  rather than supplemented. Worth knowing before anyone spends much on the
  forwarded-port path — it is the right answer today and the disposable one later.
- Reattachment after a relay reconnect is designed, which needs its own
  authenticated epoch rather than resumption of an old one.
- A supported shell cannot write to a non-tty transport from its prompt hooks —
  then the answer is a helper binary, not an in-band tier.
