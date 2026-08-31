# The helper as execution host: a remote program is never interrupted by a nocx update

## 0. What a user can do that they could not before

Start a three-hour build on a remote host, let nocx update itself, and come back to a
tab that is still driving the same running process — no dialog, no relaunch, no orphan.

The check that watches it: on a remote host, start a marker-per-second long command in an
SSH tab; update the coordinator to a build with a different helper version; confirm the
marker file's timestamps span the update with no gap, the tab still writes to and reads
from that same process, and a NEW tab to the same host comes up on the new helper
generation. Then close the old tab and confirm the old generation's install directory is
reclaimed.

## 1. In one sentence

The helper stops being a remote git accessory and becomes the **execution host** — it
spawns processes, owns their PTYs and applies OS policy, on any machine, reached over any
carrier — while the coordinator keeps every product decision; and because a helper
generation is already an immutable content-addressed directory, generations coexist, so
updating nocx never kills work.

## 2. What this crosses, and what those documents already decided

**AD-1** (`architecture.md:104`) — one WebSocket, raw binary data plane, JSON-RPC control
plane, PTY bytes never inside JSON-RPC. It does **not** license an extra process hop: it
governs framing, not copies, wakeups or head-of-line boundaries. §6 answers the hop
separately.

**AD-5** (`architecture.md:135`) — "Tier B ... **augments** (never replaces) the remote
shell". A helper that spawns the shell replaces the substrate. `nocx-k6p18` already
declares this amendment and requires it to land in `docs/architecture.md` rather than be
routed around; this design carries it (§3 D2).

**AD-6** (`architecture.md:150`) — the backend never derives render state from the byte
stream, with one carve-out (ADR-0024's bootstrap window, a framing we wrote). Nothing here
widens it: the helper forwards bytes and does not read them.

**AD-7** — session id is server-authoritative. "The server" becomes "that execution host";
`nocx-if6` phase A already made identity `(backendId, sessionId)`.

**AD-8** — interface-first, one owner per behaviour. Decisive twice below: it forbids a
second implementation of the replay contract (D7), and it says one owner per _behaviour_,
not one process per conceptual role — which is why this design does **not** add a router
process (D8).

**AD-9 / AD-10** (`architecture.md:85`) — the replay ring, offsets, acks, reset, and
losslessness with throttling at the bound. `internal/transport/ring.go` implements them:
`RingCapacity` 256 KiB, `CreditLimit` 64 KiB, `FairChunk` 8 KiB. D7 moves the ring's
_instance_ and keeps its _implementation_ single.

**ADR-0034** — consent is keyed to the machine, not to a connection or a daemon instance.
Kept. D10 changes only what uninstall does to the grant.

**ADR-0043** — one connection to the encrypted store; the ADR is explicit that this does
not establish cross-process safety. This design does not touch it, because a helper
generation never opens the vault or `content.db` (D2). Coordinator replacement stays
sequential.

**The nocx-server design** (`.internal/specs/2026-08-28-the-nocx-server-design.md`):

- **D1** — two build targets with separate composition roots, because
  `//go:embed all:artifacts` means one artifact serving both roles would embed itself.
  Unchanged, and this design adds no third target.
- **D2** — "the coordinator owns the durable-session contract; the helper preserves it",
  with AGENTS.md's delay-fuse warning attached. **This design amends D2** (§3 D7).
- **D4** — on a version mismatch the launcher may kill the old coordinator and lose its
  sessions, _saying so out loud_. Unchanged **for the coordinator**; D3 below makes it
  false for the helper, which is the point of the whole document.
- **§8 B** — this epic (`nocx-k6p18`), which this design specifies.

**The nocxify delivery-modes design** (`.internal/specs/2026-08-05-nocxify-delivery-modes-design.md`):

- **§3.4** named `relay` as the Tier-B carrier — "a deployed binary ... owned by
  `nocx-if6` phase B; named here only so the seam it lands in is decided now rather than
  forked into later". This design is that seam being filled.
- **§3.5** — three axes: desired mode, observed delivery, relay consent. Kept whole. The
  new fact — _which generation served this session_ — belongs to the **observed-delivery**
  axis, exactly as `profile.go`'s `DeliversScripts` comment predicted.
- **§4** — the bundle publisher's lock discipline: atomic-`mkdir` lock carrying a nonce, a
  bounded wait, and a stale rule that trusts neither a remote PID nor a wall clock; at most
  two generations survive a publish. D5 extends this rather than writing a second one.
- **§5.2** — "declining a deployed binary must not also decline shell scripts — different
  risks", and the inverse. Kept; D9 says what happens to the bundle on a host where the
  helper serves the session.

**The remote-helper design** (`.internal/specs/2026-08-13-remote-helper-design.md`), as
built:

- **D7** — the immutable install: `~/.nocx/helper/<version>-<goos>-<goarch>-<hash>/`,
  written to a temporary name, renamed atomically, complete only with `.install-complete`.
  **This is already generational storage**; D4 below stops deleting it.
- **D15** — `session` is a reserved service name. `host.Register` panics on it
  (`internal/helper/host/host.go:85`) — and _only_ on it: `files` and `ports` are merely
  unregistered names answering `ErrCodeUnknownService`. This design implements `session`.
- **D21** — the helper reports its own content hash in the hello-ok, and the installer
  verifies it. Kept, and load-bearing for D4's generation identity.
- **D25** — prune bounds the footprint to the version in use; uninstall removes the whole
  `~/.nocx/helper` tree. **D4 amends prune. D10 keeps uninstall whole-tree and adds to it.**

## 3. Decisions

### D1 — The promise is the constraint, and it is stated first

**A program running on a remote host is never interrupted by a nocx update.** Not by the
coordinator updating, not by the helper updating.

This is written as decision one because every decision after it is derived from it rather
than balanced against it. The bar is not internal: it is `ssh` + `tmux`, which is the
mental model a person already has, and which no terminal has ever asked them to give up.
A tool that kills a three-hour build because _the local application updated_ does not get
a second three-hour build.

The corollary is equally binding and is the honest half: **a helper-served feature MAY
become unavailable for the life of an old session, and must say so where it is used.** A
git panel that returns in twenty minutes is a nuisance; a lost build is not recoverable.
Trading the first for the second is the whole design.

### D2 — The helper is an execution host: fat infrastructure, thin product

The helper owns, on the machine where the user's work runs:

- process creation, and any policy that depends on being the process's ancestor —
  including the future Landlock/Seatbelt sandbox, which can only be applied by whoever
  spawns;
- the PTY master's lifetime; resize, signal delivery, foreground-group questions, exit
  status;
- execution-domain facts and operations: login-shell resolution, native port enumeration,
  git, filesystem;
- the minimum transport state that detach and reattach require: session identity, byte
  offsets, bounded buffering, flow control, raw forwarding.

The helper does **not** own:

- any interpretation of the byte stream — no VT, no OSC, no block boundaries (AD-6 is
  untouched, and its one carve-out is not widened);
- blocks, the ledger, retention, the vault, the assistant, `content.db`, UI state;
- **policy decisions**. The coordinator decides which sandbox a tab gets; the helper
  applies it. The coordinator decides which mode a destination is in; the helper obeys.

The slogan is **fat infrastructure, thin product**, and it is not a preference — D3 makes
it structural.

**Rejected: a helper that emits command-block boundaries itself.** It would move block
semantics across the boundary, make helper/coordinator lockstep real, and contradict AD-5
in a second, undeclared way. Integration continues to be delivered by the shell (Tier A's
mechanism), with the helper as its _carrier_ — it spawns the shell and can inject
integration at spawn, which is what removes rc-file editing and SFTP delivery without
moving one line of interpretation.

**This amends AD-5** in `docs/architecture.md`: Tier B may _replace the delivery_ of the
shell hooks on a host where the helper spawns the shell. It still may not replace the
hooks' role as the source of prompt and block metadata.

### D3 — A small frozen core, and everything else versions freely

The helper's surface splits in two:

**The session-host core is frozen for the lifetime of the product.** It is small enough
that freezing it is a promise rather than a hope:

`attach`, `detach`, `write`, `data` (raw, offset-carrying), `ack`, `resize`, `signal`,
`exit`, `close`, `sessions` (enumerate what this generation holds).

**Everything else versions freely**: `git`, `files`, `ports`, the shape of a sandbox
policy, capability reporting. A new coordinator that cannot speak an old helper's `git`
answers by making the git panel say why (D9), not by killing the session.

This is the _only_ one of the three available answers to wire compatibility that keeps D1
and honours AGENTS.md's "no backward-compatibility shims". The other two are recorded so
they are not re-proposed: a stable payload-blind router in front of versioned helpers
requires a third process (rejected in D8); and "break the contract, kill incompatible live
sessions" is D1 negated. **Keeping several old protocol clients inside the new coordinator
is a shim under another name and is refused.**

**The frame layer already has room, by prior intent.** `internal/helper/proto/frame.go`
frames `[type:1][seq:4][ack:4][len:4][payload:len]` and says of the header's unused halves:
"seq and ack are written by the sender and ignored by every reader in this deliverable;
they are reserved so a later PTY-owning service can resume without a wire break (D15)."
This is that service. Type byte 8 is unallocated (1–7 and 9 are taken). So the core needs
**one new frame type carrying a raw, non-JSON payload** and the seq/ack fields that are
already in every header — no wire break, exactly as reserved.

`proto.Version` (currently `"1"`) names the install directory and must stay numeric:
`deploy`'s `installDirName` is `^[0-9]+-[^-]+-[^-]+-[0-9a-f]{64}$`.

### D4 — Generations coexist, on the layout that already exists

`~/.nocx/helper/<version>-<goos>-<goarch>-<hash>/` is immutable and content-addressed. A
new helper version installs into a **new** directory and does not touch the old one. That
is generational storage, already built and already shipping.

Exactly one thing prevents coexistence today, and it is one function.
`internal/helper/deploy/prune.go` "removes every sibling install directory ... EXCEPT the
one named `keep`". That line is what would kill the user's job.

**Prune's rule changes from "not current" to "not current and not held":**

> Remove an install directory that is neither `keep` nor held by a live generation.

New sessions on a host go to the current generation; sessions already running stay on
theirs until they end. When a generation's last session ends, its helper exits, its hold is
released, and the next prune reclaims the directory.

Following §4's bound: **at most two generations plus the current one survive a prune.** A
third old generation still holding a session is a state the product reports (D9) rather
than a state that grows `$HOME` silently.

### D5 — Liveness is a fact on the host, never an inference

A generation is _held_ because something on the host says so, not because the coordinator
remembers starting it. The coordinator may have been replaced; the helper may have been
`SIGKILL`ed; the host may have rebooted. None of those fire a "last session ended" event.

Each generation takes a **hold in its own install directory** at start, and the rule for
deciding a hold is stale is the one the bundle publisher already uses (nocxify §4): an
atomic-`mkdir` lock carrying a nonce, a bounded wait, and a staleness rule that trusts
**neither a remote PID nor a wall clock**. AGENTS.md's own instruction applies literally —
find the existing answer and extend it, rather than writing a second one that agrees
everywhere anyone looks.

### D6 — The coordinator prunes; the helper only tells the truth about itself

Pruning stays where installation already is: the coordinator, over the SFTP lease it holds
anyway (`internal/app/helper_git.go` calls `deploy.Prune` today), at **connect** time.

**Rejected: the new helper cleans up the old one.** Three reasons. It would grant a helper
write authority over a _sibling generation's_ directory, which it has never had — a buggy
new generation could delete a live old one. It asks the process with the least context to
decide another's liveness. And it does not cover the case pruning exists for at all: a
generation that was killed never runs its own cleanup.

The helper's only contribution is D5's hold: the honest fact "I am alive".

### D7 — The replay ring moves to the fd owner; the implementation stays single

**This amends D2 of the nocx-server design.**

D2 said the coordinator owns the durable-session contract and the helper preserves it,
citing AGENTS.md's "a second implementation of one concept is a regression with a delay
fuse". The concern is right; the placement is not, and the reason is physical rather than
architectural: **only the process holding the fd can answer "what bytes were produced while
nobody was listening."** If the helper owns the PTY and the coordinator is being replaced,
the coordinator cannot buffer. Either the helper buffers, or the bytes are lost and D1 is
false.

So the ring's **instance** moves to the helper. Its **implementation does not fork**:
`internal/transport/ring.go` is Go and the helper is Go, so both link the same package.
One implementation, two owners of instances — the pattern already in the tree, where
`internal/app/app.go:1039` uses `gitlocal.NewFactory()` directly and
`cmd/nocx-helper/main.go:41` wraps the same factory in `hostsvc`.

Cost, stated: the ring must leave `internal/transport` for a package the helper can link
without dragging the transport in. Ordinary refactor, named here so it is planned rather
than discovered.

D2's letter changes; its rule survives intact.

**AD-10 is unchanged and binds the helper**: with no coordinator attached and the buffer
full, the source throttles. Backpressure is acceptable; loss is not. The open question at
the nocx-server design §10.1 — what the ring does when the recorder cannot keep up — stays
where it is, owned by `nocx-22k1c`.

### D8 — No third process, yet

nelix's decomposition is three processes: a thin control-plane router, versioned
generations, and a single-threaded PTY-fork broker. This design deliberately takes the
_shape_ and not the _count_.

**AD-8 requires one owner per behaviour, not one process per conceptual role.** One
persistent helper can be both the execution host and the stable registry for its own
sessions. Half the router already exists inside the coordinator: peer-UID auth on a
per-uid socket with atomic bind (`internal/coordinator/peer_{darwin,linux}.go`), plus the
launcher's spawn/lock/readiness — the same front door the helper needs.

A separate router becomes justified at exactly one trigger: when something stable must
answer "which generation owns session X" _without loading that generation's semantics_,
across a coordinator restart. Recorded here so the trigger is recognised rather than
rediscovered.

**Two claims about nelix, checked and corrected, so they are not cited as precedent they
do not support.** Its router does hold no master fds and streams nothing
(`router/app.py`) — but it is not payload-blind: `router/server.py` parses
operation-specific bodies. And live multi-generation routing is _intended, not
demonstrated_: `resolve_generation_state`'s own docstring returns a handle only "when
process_state is 'serving' **and the generation matches the active one**", and
`_resolve_and_forward` falls back to `registry.active()`. Its PTY broker comparison does
hold, and is instructive in the opposite direction: the broker creates the PTY, passes the
master over `SCM_RIGHTS` and drops its copy, so the **daemon** owns the fd and the broker
can die with sessions unaffected.

### D9 — What the product says, and what it stays quiet about

**The tab is not marked for running on an older generation.** After an update, most open
remote tabs would sprout a badge meaning "everything is fine" — which is how a person
learns to stop reading badges. AGENTS.md requires a _degrade_ to be visible; running on
the previous generation is not one.

Three states, three answers:

1. **Nothing degraded** — the ordinary case. No tab marker. The fact belongs where a
   person asks what nocx has put on a host: `shell.footprint.status`
   (`internal/transport/ws_shell_footprint.go`), which is already read-only, already never
   connects, and already reports "last seen" rather than "installed now". It gains
   generation rows. The kit component is `FactList` — read-only named facts — whose `note`
   field is documented as "the honest half. A value the product cannot fully vouch for",
   which is exactly the shape of a fact observed at last contact.

2. **A helper-served feature is genuinely unavailable** because that service's wire changed
   between generations. The affected surface explains itself **at the point of use** — the
   git panel says this host is on the previous helper generation and the panel returns when
   the session ends. Not a generic badge whose meaning has to be hunted. This is
   AGENTS.md's soft-degrade rule applied directly, and the defence against the
   `vault.status` failure: a surface that goes on offering what it can no longer deliver.

3. **The session is durable and currently detached** — its helper outlived the channel.
   _This_ deserves a tab marker, because it is new information about the session itself,
   and `nocx-k6p18` already names the consequence: "'the channel is closed' stops meaning
   'no helper is running'". `Badge` with `tone="info"` and `title` (the kit documents
   `title` as "hover detail — the degraded-mode reason").

**The script bundle on a helper-served host.** §5.2's additive rule is kept: allowing the
binary never withholds the scripts. What changes is only the _carrier_ — on a host where
the helper spawns the shell, integration arrives at spawn rather than through a published
bundle, and **which carrier ran is the observed-delivery axis's answer**, precisely as
`DeliversScripts`'s comment anticipated. The bundle is not additionally published for a
session the helper spawned; it remains published for every other session on that host, and
`DesiredMode` semantics are untouched.

### D10 — Uninstall is whole, it says what it will kill, and it revokes consent

Uninstall stays **all-or-nothing** over `~/.nocx/helper`. The directories there are
content-addressed and nobody hand-edits them, so partial removal would be an invented
concept. (This is the helper tree only. The **script bundle** keeps its opposite and
deliberate rule — manifest-owned unmodified files only, a user-modified file reported as a
conflict and left. Two footprints, two rules, as the code already has it.)

Three additions:

1. **The confirmation lists the live work it will end**, gathered on the **same connection
   that will do the removal** — `shell.footprint.uninstall` already connects, unlike
   read-only `status`, so the list is fresh rather than the coordinator's memory going
   stale between the dialog and the act. `Dialog` plus a `FactList` row per running
   session. Order: ask → stop sessions → remove → report what was actually stopped.

2. **`deploy.Uninstall`'s precondition stops being free.** Its doc requires that no helper
   be running out of a directory being deleted; with durable sessions, satisfying it _is_
   killing the user's work. The precondition is therefore raised into the product as a
   consent moment rather than left as a caller's obligation.

3. **Uninstall revokes consent**: `RelayConsent` returns to `unknown`. Removing the
   footprint is a stated intention, and leaving the grant at `granted` would silently
   reinstall the helper on the next connection — the user watching their removal evaporate
   with nothing saying why, which is the soft-degrade-the-UI-contradicts failure exactly.
   ADR-0034 is untouched: consent stays **keyed to the machine**, satisfying
   `nocx-k6p18`'s "with consent still keyed to the machine"; only the stored answer is
   cleared, so the next connection asks again.

### D11 — One interface, two carriers, and the local carrier is decided by measurement

What is standardised is the **SessionHost interface and its semantics**, not one identical
carrier everywhere. `host.New` already takes an `io.Reader`/`io.Writer` pair, so the frame
protocol is carrier-agnostic by construction.

**Remote**: fd passing is impossible. The carrier is the framed path over the SSH exec lane,
with D3's raw data frame. This is where D1 bites, and it is what this design delivers first.

**Local**: two shapes, and the choice is a measurement rather than an opinion.

- _Preferred_ — **fd passing**. The helper spawns the PTY and retains a duplicate master fd
  so the PTY survives coordinator death; the coordinator gets a fresh duplicate and remains
  the byte reader, because the ring, recording and the AD-6 boundary are already wired
  there. The real `SCM_RIGHTS` precedent in this repo is `internal/ssh/mux/fd_unix.go`'s
  `sendFD`, in OpenSSH's mux-client shape. (`internal/lifecyclechannel/socketpair_linux.go`
  is **not** fd passing — it is a socketpair inherited through `exec.Cmd.ExtraFiles`.)
- _Fallback_ — the helper is the permanent reader and the coordinator proxies, paying one
  hop per keystroke and per output byte.

**The hazard that decides it:** `SCM_RIGHTS` duplicates one open file description. Two
processes reading it split the bytes nondeterministically. So fd passing requires an
explicit **single-reader lease**, and the helper must learn the reader is gone before
another attaches. If the helper must also drain during arbitrarily long coordinator
downtime — which D1 implies — then a duplicated fd is not sufficient by itself and the
lease must be genuinely transferable.

**Deciding test, written before the work:** if the single-reader lease cannot be shown to
hand over without a byte being read twice or not at all, under coordinator kill at a
moment chosen adversarially, the fallback is taken and the hop is paid. **AD-1 does not
settle this** — it governs raw-versus-JSON framing and says nothing about copies, wakeups
or head-of-line boundaries.

**The local execution host does not have to ship first.** D1's promise is about remote
hosts; locally the coordinator already owns the PTY and already outlives the window.

## 4. What the user sees

The coordinator updates. It connects to host H, where generation vN−1 holds a PTY with a
running process.

1. The coordinator installs vN into its own directory. vN−1 is untouched; the process runs.
2. New sessions on H open on vN. The existing session stays on vN−1.
3. The tab behaves exactly as before. No dialog, no badge — unless the session is detached
   (D9.3) or a helper-served panel is genuinely unavailable, which says so itself (D9.2).
4. The user closes that tab. vN−1's last session ends, its helper exits, its hold is
   released, and the next connect prunes the directory.

Nothing is interrupted. The most a person notices is a panel that explains its own absence.

## 5. Assertions

Written as assertions rather than prose, per AGENTS.md testing rule 4 — and the ones that
matter are written from the spec by someone other than the implementer.

**Continuity (D1, D4)**

1. A process spawned by generation vN−1 is still running, and still readable and writable
   from its tab, after the coordinator has been replaced by one whose helper version is vN.
2. A new session opened to the same host after that replacement is served by vN.
3. Installing vN removes no file inside vN−1's directory.
4. Prune with vN−1 held removes nothing; prune after its hold is released removes it.
5. A generation whose helper was killed without cleanup is pruned on a later connect —
   staleness decided without reading a remote PID or comparing a wall clock.
6. With three generations installed and one old one held, prune leaves current + held and
   removes the rest.

**The frozen core (D3)**

7. Every core operation is exercised by a coordinator built at a _different_ helper
   version against a running helper, and each succeeds.
8. A non-core service whose wire changed answers a version refusal that names the service —
   and the session it is refused on continues to read and write.
9. A raw data frame round-trips a payload containing bytes that are not valid UTF-8 and
   bytes that resemble a frame header, and the decoder's gap callback reports nothing.

**Ring and backpressure (D7)**

10. The ring package is linked by both binaries and has exactly one implementation of
    offsets, acks and reset — asserted structurally, not by inspection.
11. With no coordinator attached and the ring full, the source throttles and no byte is
    lost (AD-10).
12. A reattach after a coordinator replacement either replays exactly from the last acked
    offset or states its gap — never silently skips.

**Failure paths (AGENTS.md rule 3 — for every external call, a test where it fails)**

13. Install of vN fails at each write boundary in turn; vN−1 keeps serving throughout, and
    the next connect converges with no manual cleanup.
14. The hold cannot be taken because the directory is read-only: the product refuses
    visibly and names the reason; no generation is deleted.
15. The carrier drops mid-session: the process survives, and the tab reports detached.
16. **And the paired positive**, per AGENTS.md: on an ordinary host with an ordinary home,
    a generation is installed, held, served and pruned end to end.

**Product surfaces (D9, D10)**

17. After an update with an old generation still serving, no tab acquires a badge and
    `shell.footprint.status` lists both generations without connecting.
18. A detached durable session's tab carries the info badge, and it clears on reattach.
19. Uninstall's confirmation lists every session it will end, gathered on the connection
    that performs the removal; declining ends nothing and removes nothing.
20. After uninstall, `RelayConsent` reads `unknown`, the next connection asks, and
    declining leaves the host with no helper installed.

## 6. Deliberately out of scope

- **The local execution host as a separate process.** D11 decides its shape and its
  deciding test; shipping it is separate work, and D1 does not depend on it.
- **A router process.** D8 records the single trigger that would justify one.
- **Autonomous work with no coordinator attached.** The nocx-server design already excludes
  it and `ws_agent.go:756` stands: a run terminalizes on disconnect. A helper keeps the
  _process_ alive; it does not advance the agent.
- **Sandbox implementation.** D2 places Landlock/Seatbelt helper-side. Note as fact: no
  sandbox package exists in this tree — `nocx-y46q` is closed but `grep -l
'landlock|seatbelt|sandbox-exec' --include=*.go` is empty and ADR-0035 is now about
  AppImage. Greenfield, and it lands helper-side from the start rather than migrating.
- **The `files` and `ports` services.** Section C of the nocx-server design.
- **Windows remote hosts.**
- **Graceful coordinator handover (A2).** Coordinator replacement stays sequential: the
  old one closes the encrypted store and exits before the new one opens it. This design
  _avoids_ ADR-0043 rather than solving multi-process encrypted SQLite.

## 7. Open questions

1. **How the coordinator re-discovers a local helper it did not spawn** (D11, local case
   only). The pattern exists — the coordinator's own peer-UID socket with atomic bind — but
   the ownership-transfer protocol is unwritten. Not blocking: the remote case ships first.
2. **What the ring does when the recorder is unavailable or slower than the source.** Owned
   by `nocx-22k1c`, restated at the nocx-server design §10.1, unchanged by this document.
   It becomes reachable in a second place once the helper holds a ring, which is worth
   saying out loud, not worth re-deciding here.
