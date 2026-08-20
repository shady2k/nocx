# nocxify: three delivery modes, and where a command block ends

**Bead:** epic `nocx-mlm7`; brainstorming session `nocx-n0qa`. Supersedes parts of
[`2026-08-03-nocxify-design.md`](2026-08-03-nocxify-design.md) — §8 lists every binding text
this revises. Owner decisions taken 2026-08-05; §1 carries the reasoning, §10 the work
packages this decomposes into.

## 0. What a user can do that they could not before

Type `ssh pi@192.168.0.93` by hand and get command blocks on the far host from its first
prompt, with the `ssh` command itself appearing as an ordinary local block that ends when
the remote session begins — and, from the second connection to that host onward, without a
200-character line on screen.

## 1. Why this document exists

`nocx-pu4.6` shipped the rewrite: a hand-typed simple interactive `ssh` is replaced at
submit with

```
if [ -s '/home/dev/.nocx/run/launcher-478956003' ]; then ssh -t pi@192.168.0.93 "$(cat '/home/dev/.nocx/run/launcher-478956003')"; else ssh pi@192.168.0.93; fi
```

The tty echoes what we send, so that line is on screen for the whole session — the `ssh`
block never finishes, because it ends at OSC 133 D and D arrives only when `ssh` exits.
The owner asked for Warp's presentation instead.

**What Warp actually does**, measured on this machine rather than assumed:
`~/.warp/remote-server/` on the remote host holds three ~215 MB binaries
(`oz-v0.2026.07.15.08.55.stable_01` … `oz-v0.2026.07.29.09.05.stable_02`), a
`server-<id>.sock`, a `server-<id>.pid`, its own `warp.sqlite` and `bundled_resources/`.
`~/.local/state/warp-terminal/oz/warp.log` shows `Handling Initialize`,
`Handling SessionBootstrapped: shell_type="bash"`, then 245 × `Handling RunCommand`. Warp
neither rewrites the command nor types into the tty: it deploys a server and drives the
remote through it. That is precisely the Tier-B remote helper `docs/architecture.md`
defers and `nocx-if6` phase B owns.

**Orca**, the other neighbour: `src/main/agent-hooks/remote-managed-hook-installers.ts`
installs its hooks over SFTP (`ssh2` `SFTPWrapper`) into the remote `$HOME`, once.

Both neighbours pay with a persistent remote footprint. nocx's visible line is the direct
price of D1 of the previous design — _no persistent remote footprint by default_. The
owner has taken the opposite trade for the script tier, and that is decision N3.

## 2. Decisions

| #      | Decision                                                                                                                                                                                                                                                                                                                                                                                                   |
| ------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **N1** | **Three destination modes, and they are not the old policy enum.** `raw` (nothing added), `script` (the shell tiers we ship — no compiled artifact), `relay` (Tier B, a deployed binary). This **replaces** `ShellIntegrationPolicy = auto \| ask \| off` outright. nocx is greenfield: there is no migration, no compatibility value and no import of an old setting. Three axes, never collapsed (§3.5). |
| **N2** | **The rewritten line is visible, and that is the product.** No `stty -echo`, no renderer-side echo suppression, no `ssh` shell function. ADR-0004 §1 (fail-open) and its rejection of echo suppression stand. The claim is "we do not hide the mechanism", not "every executed byte is on screen": the launcher payload itself is behind `$(cat …)` and later behind `~/.nocx/launch`.                     |
| **N3** | **Script mode wraps and installs automatically, without asking.** Consent is required only to deploy the relay binary. This overrides ADR-0004 §2's 2026-08-04 extension for script delivery, replaces D1 and D2 of the 2026-08-03 design, and amends AD-5 (§8).                                                                                                                                           |
| **N4** | **No remote rc file is ever created or modified, on any supported path.** Script mode publishes a versioned bundle under `~/.nocx/` and activates it from the ssh command line. The rc-gate half of `internal/shellintegration/install_remote.go` is retired, not fixed.                                                                                                                                   |
| **N5** | **An environment transition is proven by an identified readiness passport, never by an unnamed marker.** Entry counts only on `passport → clean tagged A → B` carrying the environment id nocx minted for that attempt.                                                                                                                                                                                    |
| **N6** | **One running block in the UI; two lifecycle records in the model.** The `ssh` block freezes on entry with no exit code, while a **dormant environment-transition record** keeps the ledger entry open until the local D delivers the real status. `entered` is a lifecycle state, never a `CommandStatus` (§5.3).                                                                                         |

## 3. Delivery

### 3.1 raw

Nothing is added to the command. The session is an ordinary terminal: no markers, no cwd,
no DOM editor, native input throughout. This is both a mode a user can select and the
**fail-open destination of every uncertainty** — mode `raw`, a host whose config sets
`RemoteCommand`, a line the parser cannot classify, a failed or unavailable `ssh -G`, a
refused launcher, a read-only remote `$HOME`, an environment depth greater than 0. ADR-0004
§1.

### 3.2 script — first contact

> **Amended 2026-08-20 by `nocx-a1615` (the integration-delivery-carrier design)
> and [ADR-0035](../../docs/decisions/0035-the-channel-we-own-is-the-carrier.md),
> which supersedes ADR-0022.** First contact stops being an argv event. The
> launcher no longer travels in the command, nothing is staged in a local file,
> and there is no separate first-contact line: **publication happens on a channel
> in both paths**, and both paths emit the same bounded loader of at most 1 KiB.
>
> What replaces the paragraphs below. The remote command is a fixed loader that
> carries no payload and no secret; the bundle is published over SFTP on a
> connection nocx owns — the SSH session itself for a saved connection, and for a
> typed `ssh` the user's own invocation made a multiplex master, after a handshake
> has _proven_ that ownership. The publish and the loader run concurrently, so the
> loader tests nothing about the far side before starting; the far side is still
> the owner of "is this installation valid", and says so after the publish has
> settled rather than before it has begun (carrier design §4.1, §6.1). Fail-open
> is unchanged in kind and no longer expressed as a shell `if` over a local file:
> every refusal leaves a working native prompt with a **named** reason.
>
> Three things in the paragraphs below go with the staged file. The 4096-byte tty
> line cap and `ARG_MAX` stop governing anything we emit — the payload is not in
> the line. The consume-once rule and the `rm -f` inside the substitution go with
> the file they protected, and so does the reason `nocx-sxdd` bought them for.
> **That reason has not gone away, and the carrier design does not answer it:** a
> rewritten line recalled from the local shell's own history still names our
> `ControlPath` and still carries the loader as its remote command, with no frame
> sender behind it. Nothing here is a secret and nothing republishes, but the
> replacement for consume-once is owed by whoever builds the typed-`ssh` wrapper
> and is not written down yet.
>
> N2 is untouched: the line is still visible, and there is less of it to hide.

The host has no committed bundle. The launcher travels in argv as `nocx-pu4.6` ships it
today: staged in a local file because the canonical tty line is capped at 4096 bytes, read
by the local shell at execution time, handed to `ssh` through argv (bounded by ARG_MAX).
The line is long and visible (N2). Fail-open lives in the line itself: `[ -s … ]` false runs
exactly what the user typed.

Having started, the launcher **publishes the bundle** (§4) before exec'ing the integrated
shell, and names the committed generation in its passport (§5). Nothing is asked; no rc
file is touched.

The staged payload is consumed exactly once, so that the rewritten line sitting in the local
shell's own history does not bootstrap again when it is recalled (`nocx-sxdd`). The removal
must not become the branch's exit status — `ssh …; rm -f P` reports `rm`'s success and
destroys the 255 that a dropped connection is supposed to deliver. It goes inside the
substitution, where its status is discarded and the payload has already been read:

```
if [ -s '<path>' ]; then ssh -t <flags> <dest> "$(cat '<path>'; rm -f '<path>')"; else <typed line>; fi
```

### 3.3 script — installed

> **Amended 2026-08-20, same source as §3.2.** The installed form is **kept** —
> `~/.nocx/launch` is still the 0700 POSIX `sh` script that reads
> `manifest.json`, refuses an incomplete or protocol-incompatible generation,
> `exec`s a native login shell in that case, and emits **no** passport, with all
> that follows from a missing passport. What goes is its position: the guard no
> longer travels at the head of the command and there is no separate installed
> line. A guard placed first loses a race it cannot win — on a host with nothing
> committed the publish is still in flight when the test runs, so the session
> would degrade to raw _while the publish succeeded_. The guard and the full
> generation verification therefore run inside the far side's own startup, after
> the bootstrap and the publish have settled, immediately before `exec`ing the
> launcher.
>
> The `else` arm's job — covering the file's own absence — is now the loader's,
> which also names the outcome instead of merely surviving it. **"Why not a bare
> `ssh pi@host`" below is unaffected and still binding:** the rc gate,
> `SendEnv`/`SetEnv`, and `Match exec` + `RemoteCommand` are rejected for the
> reasons given, and a byte-for-byte clean line still requires either
> unconditional integration of the whole remote account or the relay. An `ssh` that fails with
> `127` is still a bug in this design and never a user-visible outcome — the
> fallback is written as `if`, never as `exec A || exec B`, which the multiplex
> spike measured dead-exiting with exactly that status.

The bundle is committed on that host. **The guard travels to the far side**, because that is
the only machine whose `~/.nocx` is the one in question — a local `[ -x ~/.nocx/launch ]`
test would ask this machine about that host, and on a developer's box it answers about
nocx's own local staging directory:

```
ssh -t pi@192.168.0.93 'if [ -x "$HOME/.nocx/launch" ]; then exec "$HOME/.nocx/launch" <environment-id>; else exec "${SHELL:-/bin/sh}" -l; fi'
```

`~/.nocx/launch` is a 0700 POSIX `sh` script (§4 fixes the modes): it reads `manifest.json`,
refuses an incomplete or protocol-incompatible generation, and in that case `exec`s a native
login shell and emits **no** passport. The `else` above covers the case the file cannot cover
— its own absence. No passport means no environment transition, which downgrades the session
to raw and invalidates the local installed fact, so the next connection bootstraps again
(§3.2). An `ssh` that fails with `127` is a bug in this design, never a user-visible outcome.

**Why not a bare `ssh pi@host`.** The activation has to reach the far shell somehow. An rc
gate conditional on `NOCX_SHELL_INTEGRATION` is never activated, because OpenSSH does not
carry that variable. An unconditional rc gate integrates every terminal the user opens on
that host, including ones nocx did not start, and a marker-only prompt in a foreign terminal
is an invisible prompt (ADR-0006 requires a static opt-in). `SendEnv`/`SetEnv` depend on the
server's `AcceptEnv` and are already rejected as a carrier. `Match exec` + `RemoteCommand`
in the local ssh config cannot tell `ssh host` from `ssh host somecommand`, and OpenSSH then
refuses with _"Cannot execute command-line and remote command"_. A byte-for-byte clean line
therefore requires either unconditional integration of the whole remote account or the
relay. It is bought in §3.4, not here.

### 3.4 relay

A deployed binary, Warp's shape. Explicit consent per host. Out of scope, owned by
`nocx-if6` phase B; named here only so the seam it lands in is decided now rather than
forked into later.

### 3.5 Three axes, never one enum

| axis                  | values                                                        | owner                                                                  |
| --------------------- | ------------------------------------------------------------- | ---------------------------------------------------------------------- |
| **desired mode**      | `raw` \| `script` \| `relay`                                  | the profile / group / global default, resolved by the existing cascade |
| **observed delivery** | `none` \| `bootstrap-script` \| `installed-script` \| `relay` | the renderer, from what actually happened this session                 |
| **relay consent**     | `unknown` \| `granted` \| `denied`                            | persisted per destination; script mode never consults it               |

`frontend/src/capability.ts` currently declares `Delivery = 'launcher' | 'in-band' | 'relay'`
— a carrier, which is a fourth thing again. It is replaced by the observed-delivery axis
above; `deriveActions` reads the axes, never a collapsed value.

## 4. Publishing the bundle

The remote state is a **versioned immutable generation**, never a mutated working file:

```
~/.nocx/                       0700
  launch                       0700   stable POSIX sh carrier; reads manifest.json only
  manifest.json                0600   THE activation pointer, published by atomic rename
  integration/v<N>/            0700
    nocx.bash, nocx.zsh, nocx.posix   0600
  tmp/<nonce>/                 0700   staging for an unpublished generation
  lock/                        0700   atomic-mkdir lock, holds a nonce
```

- **One activation pointer, and it is `manifest.json`.** `launch` is a stable carrier
  installed before the first activation and never rewritten as part of publishing a
  generation; changing `launch` itself is a protocol-version bump with its own
  compatibility rule. A generation is active if and only if the committed manifest names
  it. This is what makes torn publication unrepresentable.
- A generation is written under `tmp/<nonce>/` on the same filesystem and published by
  atomic rename. The manifest is renamed **last**, after every file it names exists with
  the recorded hash and mode.
- **Durability scope is stated, not assumed:** files and the containing directory are
  `fsync`ed before the manifest rename, and the manifest's directory after it. The
  convergence assertions cover process death and connection loss; they do not claim
  survival of power loss on a filesystem that reorders across `fsync`.
- Concurrency is guarded by an atomic `mkdir` lock carrying a nonce and a bounded wait,
  with a stale rule that does not trust a remote PID or wall clock. The version check is
  repeated **after** the lock is held.
- A committed generation newer than ours and protocol-compatible is **not** downgraded.
  Equality is not the comparison.
- A matching version string alone never proves an installation: a generation is installed
  only when every manifest file exists with the recorded hash and mode.
- Every write boundary is a resumption point: an attempt interrupted anywhere converges on
  the next connection with no manual cleanup, and no partially written generation is ever
  reachable from the manifest. The publisher takes its filesystem through a seam so that
  **failure at each boundary is injectable in tests** rather than argued about.
- At most two generations and one `tmp/` entry survive a publish; older generations and
  orphaned staging directories are removed under the lock. `$HOME` does not grow with every
  app update.
- Uninstall removes only manifest-owned, unmodified files, never `~/.nocx` recursively;
  anything the user changed is reported as a conflict.
- A read-only `$HOME` publishes nothing and records no installed fact; the session runs
  from argv as in §3.2, or raw.

### 4.0 Two writers, one contract — because one of them is not Go

The first draft said the SFTP carrier and the self-installing launcher "hand the same bundle
descriptor to the same publisher". That is impossible for half of it, and P6 caught it: the
bootstrap launcher **is POSIX `sh` executing on the far host**, a machine our Go binary never
reaches. Nothing Go-side can run there.

So the contract is what is shared, not the code:

- **Declared once, on the Go side**: the manifest schema, the directory layout, the modes,
  the version comparison. Neither writer invents a field, a path or a mode.
- **Two writers**: `Publisher.Publish` over the FS seam (used by SFTP, where nocx owns the
  transport), and a POSIX `sh` publish embedded in the bootstrap launcher (used when the only
  thing we have on that host is a shell).
- **One verifier**: Go's `Verify()`, and the `launch` carrier's own manifest check.
- **Conformance runs both ways, and is the acceptance criterion**: `sh` publishes into a
  disposable `$HOME` and Go `Verify()` reports it active and complete; Go publishes and the
  `sh` carrier accepts it and execs the integrated shell. Either direction failing is a
  broken contract, not a test to relax.

The `sh` writer keeps every refusal (symlink, foreign root, read-only `$HOME`, no-downgrade)
and the `mkdir` lock — atomic in POSIX `sh`, which is why P1 chose that shape. What it cannot
keep is `fsync`: its durability guarantee is process death and connection loss, not power
loss, and the code says so where the Go side promises more. Today neither is wired: `internal/app/app.go` constructs the
launcher and the local stager but no installer, and `internal/ssh/ssh_real.go` reaches the
installer only when the launcher is absent — so this is new wiring, not the reuse of a
working path.

### 4.1 Security contract for a silent install

Because N3 writes without asking, these are requirements, not hardening:

- No path in `~/.nocx` is followed through a symlink — not the root, not a generation, not
  `tmp`, `lock`, `manifest.json` or `launch`.
- An existing `~/.nocx` that is not recognisably ours is never modified, and never has its
  mode changed; the session degrades instead.
- Directories 0700, data 0600, the carrier 0700. Set at creation, never left to umask.
- Only fixed filenames. A manifest entry naming an absolute path, a `..` segment, a symlink
  or an unknown key invalidates the whole manifest.
- The restricted shell, `ForceCommand` and administrative policy are never bypassed by
  invoking `/bin/bash` directly.
- Any publish failure leaves the current session usable — transient-integrated or raw.
- The product **shows** the footprint: destination, generation, path, and an uninstall
  action, even though consent was not asked (§9, P10).

## 5. The readiness passport and the environment boundary

### 5.1 Why an unnamed marker cannot do it

`frontend/src/renderers/xterm.ts` delivers `A/B/C/D` plus an exit code and nothing else, and
`terminal-content.ts` pops the environment stack on **any** D. Three concrete breakages
follow: the POSIX tier's first emission is an orphan `D;0` before its first A, which a
"first marker means we are in" rule reads as `ssh` having finished; the first remote
command's D is read as leaving the ssh environment; and `ssh -t host tmux attach` into an
already-integrated tmux emits markers that announce a transition that never happened.

### 5.2 The wire format

Both are raw PTY data on the existing private OSC 636 (`S` snapshot, `H` hello are taken),
parsed by the renderer's VT parser exactly like OSC 7 and OSC 133. The backend stays
byte-blind (AD-6); nothing is suppressed and no byte is reordered.

```
OSC 636 ; P ; <protocolVersion> ; <environmentId> ; <parentEnvironmentId> ; <scriptVersion> ; <tier> ; <generation> ST
```

- Fields are positional and semicolon-separated. Every value is restricted to
  `[A-Za-z0-9._-]{1,64}`; there is no escaping because no field may contain a separator.
  `parentEnvironmentId` may be `-` at depth 0. `tier` is `enhanced|blocks|minimal`.
  `generation` is `-` when nothing was published.
- The whole sequence is bounded at 512 bytes. Anything longer, malformed, or carrying an
  unknown `protocolVersion` is **ignored**, not guessed at.
- Lifecycle markers are tagged: `OSC 133 ; A ; nocx_env=<environmentId> ST` and likewise for
  B, C and D — the `;key=value` parameter form OSC 133 already allows, so a foreign terminal
  is unaffected. An **untagged** marker drives block boundaries as today but never an
  environment transition or keyboard ownership.
- A duplicate passport for an id already accepted is ignored; a passport whose id is not the
  one we minted for the attempt in flight is ignored and logged.

### 5.3 Who mints the id, and what entry means

The **backend delivery planner mints a fresh `environmentId` per attempt** and returns it in
the RPC result; the renderer registers it as expected _before_ the line reaches the pty; the
launcher echoes it in the passport. A second `ssh` from the same tab therefore gets a
different id — today `ws_shell_launcher.go` passes the stable tab session id, which cannot
distinguish two attempts.

**Entry is counted only on `expected passport → tagged A → B`.** Until then nothing changes:
not the environment, not the labels, not keyboard ownership. A remote D closes a remote
command and never pops the environment; only a **local** D pops it. `entered` lives in the
transition record's lifecycle, never in `CommandStatus` — that enum is reflected in
`contracts/history.query.schema.json` and an unfinished `entered` must never reach persisted
history.

### 5.4 The installed fact

Owned by the backend, persisted across restarts, keyed by the **resolved destination
identity** — the `ssh -G` answer for the exact argv (host, port, user, and the
`-F/-o/-J/-l/-p` the user typed), not by the hostname string. It records the protocol
version and generation last observed. It is written only from a passport the renderer
accepted, which crosses the control plane as a typed observation fact (AD-1 needs the
amendment in §8). It is invalidated when a connection that expected `installed-script`
produces no passport.

## 6. What the user sees

| block                 | label              | contains                                                                                                                                   |
| --------------------- | ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `ssh pi@192.168.0.93` | local (`~`)        | the rewritten line's echo, host-key prompt, banner, `password:`, 2FA, MOTD — everything up to the passport. Freezes on entry, no exit code |
| next                  | `pi@raspberrypi:~` | the remote shell's first command cycle                                                                                                     |
| …                     | `pi@raspberrypi:~` | ordinary remote blocks                                                                                                                     |
| `exit`                | `pi@raspberrypi:~` | `Connection … closed.` — closed by the **local** D                                                                                         |

**Deliberate divergence from Warp:** the MOTD stays at the end of the `ssh` block rather
than opening the remote block. Warp puts it in its own block because its server owns the
remote session; the only way to draw that boundary here is OpenSSH's `LocalCommand`, which
needs either the user's `~/.ssh/config` or a flag on a line that will not exist once the
host is installed. The banner is genuinely output of the `ssh` command, so this is honest
rather than merely cheaper. Revisit when the relay lands.

Two presentation defects fixed with this: the `ssh` block currently carries the **remote**
host chip, because `submit()` applies the environment entry before `ledger.open` and
`beginBlock`; and the block runs for the whole session because it ends at D. Note that
`freezeBlock` with `exitCode === null` currently paints a failure — the frozen `entered`
block needs its own visual state, not a null code.

### 6.1 Edge cases, and what each must show

| sequence                                     | required behaviour                                                                                                                                                                                            |
| -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| auth fails / `Ctrl-C` at `password:`         | no passport arrives; the block lives to the local D and gets the real exit status — the fail-open path, unchanged from today                                                                                  |
| banner printed before `password:`            | banner, host-key prompt and 2FA all belong to the local `ssh` block                                                                                                                                           |
| POSIX tier's orphan `D;0` before its first A | untagged, and in any case not the expected passport: closes nothing, pops nothing                                                                                                                             |
| `ssh -t host tmux attach`                    | classified as a remote command, so never rewritten; markers from an integrated tmux carry no expected id and create no transition                                                                             |
| nested `ssh` host2 → host3                   | **`environmentDepth > 0 ⇒ raw`** in this epic. No rewrite is built inside a remote environment, so a local staged path can never be read by a remote shell. Depth > 1 is the relay's problem                  |
| `sudo -i` on the remote                      | a raw child shell; no passport, no transition. Automatic detection is `nocx-eepi`, not this epic                                                                                                              |
| connection lost / timeout                    | the running remote command becomes `interrupted`/`unknown` with reason `transition-lost` — AD-6 means we cannot know it was the network; the `ssh` transition record takes the local D's code (typically 255) |
| `Ctrl-D` with no running remote block        | the local D still restores the parent environment and the editor                                                                                                                                              |

## 7. Assertions

Delivery and policy:

- an uncertain plan ⟹ bytes sent == bytes typed.
- a rewritten submit ⟹ the ledger and the block header hold the typed line, and nocx sends
  the rewritten line **and performs no post-submit history mutation** (what the shell's own
  history does with it is the shell's business — `HISTIGNORE`, an unwritable `HISTFILE` and
  history-off are all legitimate).
- a PTY acceptance test sees exactly one wrapper echo before the first remote output, and
  the renderer registers no suppression or repaint hook on that region.
- mode `raw` ⟹ no rewrite and no remote write.
- the typed `-p`, `-F`, `-o`, `-l`, `-J` reach the `ssh -G` oracle and the installed-fact
  key, or the rewrite is refused.
- a failed or unavailable oracle refuses the rewrite (today it proceeds — §9).
- `environmentDepth > 0` ⟹ no rewrite is built.

Installation:

- exactly one committed manifest names exactly one active generation; older immutable
  generations may exist and are unreachable from it.
- an active manifest ⟹ every file it names exists with the recorded hash and mode.
- the manifest rename happens after all files exist and are fsynced.
- a matching version string alone never proves an installation.
- concurrent installs of the same version ⟹ one active generation, no duplicate work, no
  lost bytes.
- an installed newer compatible version is never downgraded.
- a fault injected at **each** publisher boundary (the seam makes this enumerable) ⟹ the
  next attempt converges with no manual cleanup and the previous activation is unchanged.
- read-only `$HOME` ⟹ per tier: bash and zsh run transient-integrated, POSIX runs
  transient-integrated at `minimal`, all three record **no** installed fact.
- every supported publisher path leaves `.bashrc`, `.bash_profile`, `.profile`, `.zshrc` and
  `${ZDOTDIR}/.zshrc` byte-identical, asserted by snapshot.
- uninstall removes only manifest-owned unmodified files; a modified file is a reported
  conflict; `~/.nocx` is never removed recursively.
- a symlinked `~/.nocx` or generation path ⟹ no write, session degrades.
- the SFTP carrier and the self-install carrier publish through the same publisher with the
  same descriptor.

Environment boundary:

- an environment is entered only on `expected passport → tagged A → B`.
- an untagged or unexpected OSC 133 changes neither environment nor keyboard ownership.
- every marker that drives environment lifecycle carries `nocx_env`.
- a remote D closes a remote command and does not pop; a local D pops and never assigns its
  code to a remote command.
- no passport ⟹ the `ssh` block stays running until the local D.
- disconnect ⟹ the active remote command is `interrupted`/`unknown` with reason
  `transition-lost`, and the transition record carries the local D's code.
- the frozen entered block shows no exit code and is not painted as a failure; its dormant
  transition record calls no completion and reaches `history.record` only at the local D,
  exactly once.

## 8. Binding texts this revises

Deliberately, in the texts themselves — not as exceptions in a brief:

- **AD-5** (`docs/architecture.md`) defines Tier A as integration with no remote install.
  Script mode installs by default. **AD-4** still says "SFTP later"; it is now.
- **AD-1** admits only after-the-fact ledger facts across the control plane. §5.4's typed
  observation fact needs it widened, or the installed fact needs another home.
- **ADR-0004 §2**'s 2026-08-04 extension requires consent once per destination and permits
  automatic integration only as an informed opt-in, "never as the default". N3 supersedes
  that **for script delivery**: Enter authorises execution, the script footprint is
  automatic product behaviour, and the relay stays consent-gated. §7 of ADR-0004 (fail-open,
  no echo suppression, no termios inference) is untouched — that is what N2 buys.
- **ADR-0006** describes remote enhanced mode as explicitly negotiated and deferred, and
  requires a static opt-in for marker-only prompts. Ownership now requires the passport plus
  a clean tagged A→B.
- **ADR-0008** gains the dormant transition record and the `entered` presentation state; it
  must also state that `entered` never becomes a persisted `CommandStatus`.
- **ADR-0015** fixes the oracle as `ssh -G <host>` cached per host. With command-line
  `-F/-p/-l/-J/-o` the cache key becomes the resolved identity.
- **`docs/vision.md`** still describes integration as a marker in the shell rc.
- **The 2026-08-03 design**: D1 and D2 are replaced by N3; §4.1/§4.2 and the security
  section §7 contradict a persistent default; assertions 8 ("no bash footprint"), 13 (rc
  `exit`/`exec`/`return`, which contradicts the launcher's own behaviour on a top-level
  `return`) and 15 ("no silent rewrite") are restated against N2/N4.
- **Wire contracts**: `shell.launcherCommand` (or its replacement), `open` and
  `profiles.effective` for the mode enum, plus new results for the observation fact and the
  footprint/uninstall surface. `history.query` is **not** changed — which is why `entered`
  stays out of `CommandStatus`.

## 9. Defects in shipped code, filed separately

Bugs in `nocx-pu4.6` as merged. Each is fixed inside the package that owns its file rather
than by a separate worker:

- `nocx-c5az` — the renderer sends only `destination` to `shell.launcherCommand`, dropping
  the typed `-p`, `-F`, `-o`, `-l`, so the oracle answers about a different configuration
  than the one that will run. Owned by P4 + P7.
- `nocx-qwhp` — `internal/transport/ws_shell_launcher.go` refuses the rewrite only when
  `ssh -G` succeeds _and_ reports a `RemoteCommand`; a failed oracle still rewrites, which
  inverts fail-open. Owned by P7.
- `nocx-sxdd` — `internal/shellintegration/stage.go` removes a stale staged launcher only on
  the next `Stage`, so a rerun from native shell history re-triggers a bootstrap. Fixed in
  the wrapper (consume-once) by P4; `stage.go` stays a safety net for abandoned files.

## 10. Work packages

| ID      | Owns                                                                                                                                                               | Depends on     | Done when                                                                                                                                                                                       |
| ------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **P1**  | new files in `internal/shellintegration/` (publisher, manifest, fs seam) + tests                                                                                   | —              | the §7 installation assertions hold against an injectable filesystem                                                                                                                            |
| **P2**  | `scripts/nocx.{bash,zsh,posix}`, `scripts.go`, `scripts_version_test.go`, `renderers/xterm.ts`, new protocol module + tests                                        | —              | passport and tagged markers parse per §5.2; untagged/malformed/stale change nothing; the three real shells emit them                                                                            |
| **P3**  | `internal/profile/profile.go` + resolver, `frontend/src/capability.ts`, `connections.tsx`, `contracts/open`, `contracts/profiles.effective` + generated TS + tests | —              | the three axes of §3.5 exist, default is `script`, `raw` refuses everything, relay needs consent. No migration (greenfield)                                                                     |
| **P4**  | `frontend/src/ssh-transition.ts` + tests                                                                                                                           | —              | a typed plan preserves every accepted option; operators, remote commands and unknown grammar refuse; the wrapper consumes the staged file exactly once (`nocx-x99j`, `nocx-2wtc` renderer half) |
| **P5**  | `frontend/src/command-ledger.ts`, `scrollback/blocks.ts` + tests                                                                                                   | —              | one running block in the UI, one dormant transition record, `entered` painted as neither success nor failure, exactly one completion at the local D                                             |
| **P6**  | `launcher*.go` + tests                                                                                                                                             | P1, P2         | full launcher publishes then emits the passport; the compact carrier fails open to a native shell with no passport; read-only `$HOME` leaves no installed fact                                  |
| **P7**  | `ws_shell_launcher.go`, `ssh_resolver.go`, installed-fact store, `app.go`, contracts + tests                                                                       | P3, P4, P6     | fresh env id per attempt, oracle sees the real argv, failed oracle refuses (`nocx-4psh`), installed fact keyed by resolved identity and invalidated on a missing passport                       |
| **P8**  | `install_remote.go`, `ssh.go`, `ssh_real.go` + tests                                                                                                               | P1, P6         | the SFTP carrier publishes through P1's publisher; rc files byte-identical; the installer is reachable from `main()`                                                                            |
| **P9**  | `terminal-content.ts`, `input-state.ts`, `environment-commands.ts` + tests                                                                                         | P2, P5, P7     | every row of §6.1                                                                                                                                                                               |
| **P10** | footprint status + uninstall: transport handler, settings/connection UI, contracts + tests                                                                         | P1, P3, P7, P8 | the user can see destination, generation and path, and uninstall safely                                                                                                                         |
| **P11** | `e2e/`, fixture sshd glue                                                                                                                                          | P8, P9, P10    | the epic's acceptance criterion, plus the auth-failure and exit variants                                                                                                                        |

**Wave 1 is P1–P5**, file-disjoint by construction. P3 may make only the minimal policy
adaptation inside `terminal-content.ts`; after that the file belongs to P9 alone. P2 owns
`scripts.go` and every shell script; P6 consumes a bundle descriptor and never edits them.
P7 and P9 are sequential — both converge on the submit path and the RPC result shape.

## 11. Out of scope

The relay binary (`nocx-if6` phase B) beyond naming its seam; Warp's separate MOTD block;
an `ssh` shell function; renderer-side echo suppression; automatic detection of `sudo -i` or
a container (`nocx-eepi`); nested bootstrap at depth > 0; and any change to how the local
shell is _delivered_ at spawn — its wire protocol does change (§5.2), its delivery does not.
