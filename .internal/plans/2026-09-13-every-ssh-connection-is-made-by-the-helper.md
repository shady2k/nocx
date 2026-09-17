# Every SSH connection is made by the helper — plan

> Owner invariant, 2026-09-13: there is no SSH connection without a helper; everything, even local, goes through a helper, no exceptions. The coordinator holds no SSH client. This plan (written read-only by an omp worker, reviewed by the coordinator) is the design for the epic that makes it true. Where it offers "the coordinator keeps a dialer" or a two-pool interim, the invariant has already rejected that.

Worktree `/home/dev/.herdr/worktrees/nocx/w-sh1-sshhelper`, branch `w/sh1-sshhelper` (HEAD `277cc8eb`).
Plan only; no repository file changed. All references are this worktree.

## Verdict

Buildable, and not buildable alone. An SSH pane no helper claims is opened by the coordinator today
(`internal/transport/session_open.go:287-307`: helper asked at 289-303, `svc.Open` at 306). Moving that
pane onto the local helper puts an `ssh.Client` in a second process, while the coordinator's five SSH
consumers hold their own (`internal/ssh/pool.go:240` `ConnPool` ← `RealClient.pool`, `ssh_real.go:72`).
The brief's binding "one pool owner" therefore makes this bead's landing group = **the helper becomes the
only dialer for the destinations it hosts, and every consumer rides that connection before the fallback is
deleted** (§3, sequenced in §10). If the coordinator will not take the enlarged landing group, the correct
status is BLOCKED-pending-prerequisite — not a two-pool interim.

## 0 — Already decided; not re-decided here

AD-2 (`docs/architecture.md:117-121`): one Go core, multiple build targets incl. the remote helper. AD-4
(`:130-134`): SSH on `x/crypto/ssh` behind an interface, **a ref-counted `ssh.Client` pool keyed by
host+identity, channels multiplexing over one connection, closing with the last tab** — and it exists to
prevent "a spawn-`ssh` MVP", so an `ssh` subprocess is refused and the helper must link the library. Also
AD-5 (`:136-143`); AD-7 (`:176-182`: `session` owns the channel, references a _pooled_ connection); AD-8
(`:184-189`: variation by interface, never a fork); AD-10 (`:199-209`: one lossy case, on a host a helper
owns). ADR-0057: locally there is no Tier A fallback — a helper that cannot be reached is a refusal naming
what failed, why and what to do. ADR-0066: one emulator beside the PTY, already shipped
(`internal/helper/session/runtime.go:13-30`, `session.go:192-199`), so **owner rule 2 is free here**:
nothing new answers terminal queries. Modes `auto|raw|script|helper`: `internal/profile/profile.go:58-65`;
consent: `internal/app/consent.go:84-130`, and a denied or absent answer writes nothing remotely (`:90-93`).

## 1 — Where the SSH client runs (Q1)

**Decision: the local daemon links `internal/ssh`; the deployable artifact does not. A build tag selects
the composition, and a second, local-only artifact supplies the bytes.**

One binary runs on every machine and the _same bytes_ are both deployed and installed locally
(`deploy/artifacts/source.go:52-61` `//go:embed all:bin` + `DefaultSource`; `helper/local/local.go:14-30`:
"the artifact ships embedded in the app… Install writes the same content-addressed directory").
`deploy/dependency_test.go:33-50` forbids `golang.org/x/crypto/ssh`, `.../ssh/agent`, `.../ssh/knownhosts`,
`github.com/pkg/sftp` and `internal/ssh` in `go list -deps ./cmd/nocx-helper`. Artifact names parse as
exactly `nocx-helper-<goos>-<goarch>.gz` (`artifacts/source.go:81-92`), so a variant needs its own
directory, not a longer name. Build: `Makefile:116-117`, `:176-195`.

1. New tag `nocx_local_ssh`, **absent by default**: `make helpers` keeps building the four deployable
   targets untagged, so what ships to somebody else's host is the SSH-free artifact _by default_ —
   forgetting a flag cannot leak the client remotely.
2. A new target builds **the host platform's** artifact with the tag into `artifacts/bin/local/`, with its
   own `//go:embed all:bin/local` and its own map (`bin/`'s walk at `source.go:68-93` skips directories,
   so the sets cannot collide). The lookup is by `runtime.GOOS/GOARCH`, so only the host platform's local
   artifact must exist — one extra native build, not four.
3. `local.Install` selects the local source; `deploy.Ensure` (`deploy/install.go:158`) is unchanged, so the
   install stays content-addressed and "which build is serving" is still answered once, by the hash (D7).
4. `dependency_test.go` becomes **two assertions**: (a) the untagged `go list -deps ./cmd/nocx-helper`
   still reaches none of the five packages — now naming the _deployed_ artifact; (b) the tagged build
   _does_ reach `x/crypto/ssh` and `internal/ssh`, so a local artifact built without the tag (which would
   refuse every SSH pane) fails a test instead of a user. Both are `go list`, not source greps.

Size, measured here (Go 1.26.7, `CGO_ENABLED=0 -trimpath -ldflags="-s -w"`; hello-world 1,200,290 B vs. the
same main importing `x/crypto/ssh` + `ssh/knownhosts` + `pkg/sftp` 3,952,802 B): **+2,752,512 B (≈2.6 MiB)
— a lower bound**, since `internal/ssh` (~10k lines) sits on top. Re-measure with `make helpers`:
`artifacts/bin/` here holds only `.gitignore`. **The deployed artifact grows by 0 B**; the cost lands in the
app's own embed, on the machine that already links `internal/ssh`. Attack surface: a _client_ on your own
machine, already under the coordinator's account (D12); the remote artifact's surface is unchanged, and (a)
keeps it so.

## 2 — Credentials and host-key decisions (Q2)

**Decision: the helper asks, the coordinator answers — an ssh-agent-shaped callback as reverse-direction
requests on the helper connection. The helper holds no secret at rest, writes no `known_hosts`, and cannot
dial what the coordinator has not authorised.** The wire already permits it: `TypeRequest`/`TypeResponse`
exist (`proto/frame.go:34-41`) and the host serves them (`host/host.go:223-225`), while the _client_ does
not — `client/client.go:240-258` logs `unexpected frame`. So this is client-side serving of an existing
frame type; no new frame type, no new envelope. New service `ssh`, ops from a closed set:

- `sign(credentialRef, challenge)` → signature. A key's bytes never enter the helper — the common case.
- `secret(credentialRef, purpose)` → password/passphrase. Unavoidable: the helper must _present_ it. Same
  account, same machine (D12) — a stated widening, not a leak. A sealed vault rides the existing path: the
  callback returns the vault's error and the coordinator refuses with it (`session_open.go:353-359`,
  `refuseWithCause(-32603, err)`).
- `verifyHostKey(host, algorithm, key)` → `trusted|changed|unknown`, plus `trustHostKey(...)` for
  accept-on-first-use: `knownhosts` stays a forbidden import, the coordinator keeps `~/.ssh/known_hosts` and
  the prompt (`transport/ws_probe.go:308-361`, `hostKeyInfoFromError` `:225-235`), so the evidence the UI
  renders is unchanged.
- `probe(host, identity)` — the settings-surface test (`ssh_real.go:228`, the only pool-bypassing path);
  without it the coordinator keeps a dialer (§3).

**No coordinator connected.** An open is always coordinator-initiated, so dial time never lacks one; a
_re-dial_ after a replacement can. The helper then answers `errNoAuthChannel`, ends the channel and reports the
session terminal with its exit status — never a hang, never a stored fallback, never a retry loop (today the
session simply dies with the coordinator). Consent is not extended either: hosting a channel on your own
machine writes nothing to the far host, so `raw`/`script`/no-consent keep their meaning (`consent.go:88-101`).

## 3 — The pool (Q3): one owner, and it is the helper

`ConnPool` (`ssh/pool.go:240`), owned by `RealClient.pool` (`ssh_real.go:72`), is reached through leases by
every consumer: `FSConn` (files — `app.go:1351` `filesystemProviderFactory(sshClient)`), `DiscoveryConn`
(completions, ports, `uname` probes), `TunnelConn` (forwards, remote lifecycle, API routes),
`HelperInstallConn` (`app.go:1387` uninstaller, `deploy.Ensure`), `HelperConn` (the git-over-helper exec
lane). AD-4 makes the pool's reason for existing the _channels multiplexing over one connection_ — and those
channels are the tabs, i.e. the sessions this bead moves.

**Decision: the helper owns the connection per (host, identity) for every destination it hosts; the
coordinator acquires every channel through the helper; `internal/ssh`'s dialer has no coordinator consumer
when this lands.** The helper's `ssh` service therefore also carries a narrow, **typed and argv-free**
channel service (D3: `host.Field.IsFreeFormStringList` refuses a free-form `[]string`; the spawn contract
refuses a command, `contracts/helper/README.md:96-98`). The coordinator's _code_ for each consumer stays
where it is — what moves is the transport underneath it:

| consumer                                                                       | today                     | after                                                                                                               |
| ------------------------------------------------------------------------------ | ------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| files/SFTP + the script-mode bundle publish (`ssh_fsconn.go`, `deploy.Ensure`) | `FSConn` over the pool    | `ssh.channel(kind=subsystem "sftp")` → stream; `pkg/sftp` and the publish code stay in the coordinator              |
| forwards (`ssh_tunnel.go`), remote lifecycle (`app.go:3012-3046`)              | `TunnelConn`              | `ssh.channel(kind=direct-tcpip, target)` + `ssh.forward(listenSpec)`; the lifecycle tunnel is the first tenant (§6) |
| discovery/completions                                                          | `DiscoveryConn.Exec(cmd)` | a closed set of **named probes** (`uname -s -m`, `ps`, shell detection)                                             |
| git via helper (`helper_git.go:1038` bridge)                                   | exec lane over the pool   | `ssh.lane(machine, generation)` — both values typed                                                                 |
| `Probe` (`ssh_real.go:228`)                                                    | pool bypass               | `ssh.probe(host, identity)`                                                                                         |

Two consequences. **(1)** Proxied channels need their own byte identity: the ABI keys raw bytes by session
(`proto/frame.go:44-61`), so this is a new frame type (or identity kind) and a `proto.Version` bump — safe
_because the version is checked both ways_ (`host/host.go:205`, `client/launch.go:251`). **(2)** The
exec-shaped consumer is the expensive one: a caller-supplied command cannot cross this wire (D3), so every
probe becomes a named op and the list must be complete before the group closes — an unnamed probe is a
feature that silently stops working.

**Why this does not create two pools for one host**: the order is _consumers first, route last_. The
capability lands; each consumer moves in its own commit, deleting one coordinator dial path each; only when
the last consumer and `Probe` have moved do the route commits land — hosting SSH panes, deleting the
fallback (`session_open.go:305-307`) and with it `capability.OpenService.Open` (`capability/open.go:36-40`,
whose remaining caller is that fallback; the others, `capability/open.go:126` and `capability/session.go:187`,
call the session registry). A partially-migrated tree exists only on this branch, never on `main`, because
the merge carrying it completes it. **Rejected, named**: the coordinator keeping its pool while the helper
dials its own — an extra handshake and credential resolution per host whenever a pane and the Files panel are
open, i.e. the two-owner state AD-4 exists to prevent.

## 4 — The session shape in the helper (Q4)

**Decision: a second `Spawner` produces an ssh-channel `Process` that reports no local evidence — absence,
never a fabricated pid.** `session/session.go:33-46` `Process` (Pid, Shell, ForegroundProcessGroup) plus its
optional companions (`:53-57` `ProcessGroupSignaller`, `:48-51` `LifecycleProcess`) are the seams. A remote
channel has no local pid, pgid or foreground group, and the trap is concrete: `session.go:381-386` calls
`inspector.Observe(s.launch.Pid, fg)`, and **pid 0 is the kernel scheduler** — a fabricated zero would
report the scheduler's facts under this session's authority.

- New build-tagged `sshProcess` (`//go:build nocx_local_ssh`): Read/Write on the channel; `Resize` → the
  channel's window-change; `Done`/`WaitErr` from the session's wait (the remote exit status, AD-7); `Shell()`
  = the far login shell the launcher already detects; `Lifecycle()` from the bootstrap descriptor. It does
  **not** implement the local-evidence companion, and `entry()` must ask that before the inspector, leaving
  `Observed: null` — which the schema already means ("null when nobody could be asked",
  `contracts/helper/README.md:90-93`). The remote launch record carries no pgid (§5).
- Signalling: `Pgid: 0` already means "the session's own group" (`proto/session_service.go:111-133`) → the
  far shell → the channel's signal request; a non-zero remote pgid is refused by name (D3: no policy, and it
  cannot validate a group on a host it cannot see). `close-session` (`service.go:799-818`) closes the channel
  and ends the session exactly as it does locally.
- Inventory, window, exit, attach and re-adoption are unchanged: the session enters at channel creation and
  leaves at close (`session.go:23-26`); a replacing coordinator finds it through `client.Sessions`
  (`client/sessions.go:90`); the runtime rides along untouched (`session.go:192-199`).

## 5 — Proto and contracts (Q5)

Every helper schema is `additionalProperties: false`: "an older reader REJECTS a payload carrying a field it
does not know, so a field added later is a break rather than an extension" (`contracts/helper/README.md:114-119`),
while an _op_ added later degrades gracefully — an older helper answers `unknown_op` (`:72-76`). `launchRecord`
strictly requires `pid`, `pgid`, `shell`, `cwd`, `cols`, `rows`, `windowBytes`
(`contracts/helper/identities.schema.json`) and `sessionEntry` requires it by `$ref`. `proto.Version`
(`proto/version.go:12-42`) must be bumped for any frame/service/op/result change; the install directory is
keyed by it (D7), so generations coexist and a live session keeps its binary.

**Decision.** (1) **New op `session.spawn-ssh`, beside `spawn`** — never a `destination` field on
`SpawnParams` (`proto/session_service.go:186-245`), which the freeze forbids. Its own params schema carries
the destination as typed fields: `{host, port, user, identityRef, hostKeyFingerprint, cwd, cols, rows,
lifecycle, idempotencyKey, desiredMode}` — no command string, in the shape `LifecycleLaunch` already takes
(`:247-256`). (2) **`sessionEntry.launch` becomes a discriminated union** with a required `kind` const per
branch: `local` = today's record verbatim (a local session's bytes do not change), `ssh` = `{host, port,
user, identityRef, shell, cwd, cols, rows, windowBytes}` with **no `pid`/`pgid` keys at all** — absence is
the honest encoding. New `$def sshLaunchRecord`; every op returning `sessionEntry` inherits it by `$ref`.
(3) **Versions: one bump per post-freeze change, and the two are distinct.** The concurrent worker's `screen`
op takes `Version` 2 → 3 (§10); this bead's change — the `spawn-ssh` op _and_ the launch-record union in one
generation — takes 3 → 4. Old and new peers cannot talk at all (the version is refused at hello in both
directions, `host/host.go:205`, `client/launch.go:251`) and each generation installs beside the other
(`deploy/install.go:4`, `version.go:36-41`), which is what makes the entry change safe: no reader of the new
shape was built without it.

## 6 — Shell integration and lifecycle launch (Q6)

The remote command is a bounded, payload-free carrier (`shellintegration/launcher.go:92-118`,
`carrier.go:566-611`) that the far side loads and is then fed stage-1 over the session's stdin (`StageFD`,
`carrier.go:592`; the bootstrapping read is AD-6's own carve-out). The lifecycle channel is a **remote
loopback listener on the ssh connection** (`app.go:3012-3046` → `client.TunnelConn`).
`internal/shellintegration` has **no** `crypto/ssh`, `pkg/sftp`, `knownhosts` or `internal/ssh` in
`go list -deps` — verified — so it is legal inside the helper.

**Decision.** The **launcher text is rendered by the helper**, from the same `shellintegration` code, because
it must be the exec command of a channel the helper opens; its inputs (session id, lane, domain, epoch, stage
digest) arrive as typed params, exactly as today through `SpawnParams.Lifecycle`. The **stage-1 frames and
readiness tokens are written/read by the helper** — the process holding the stream. Cost, named: today's
implementation is `internal/ssh/ssh_channel.go`, wrapping a `gossh.Session` directly, so the bootstrap
handover must be lifted into a package the helper may link, behind an interface over "the stream". The
**lifecycle kernel stays in the coordinator** (domain, publication, `registerLane` `app.go:1911-1917`, the
child-domain registry, the adapter): the **tunnel moves to the helper** and accepted connections are piped
back on a plane that already exists for exactly this — the helper moves the raw lifecycle stream and nothing
else (`session.go:48-51`, `proto/frame.go:57`), so the remote adapter is reached over a _local_ carrier and
`remoteLifecycleProvider` (`app.go:3012`) is re-pointed, not rewritten. **The script-mode bundle publish is the §3 sftp row**: `pkg/sftp` and the publish code stay in the
coordinator (a forbidden import in the helper) and reach the host over the helper-provided sftp stream — one
connection, one ordering, the same clean cutover as every other consumer. The carrier is deliberately
unconditional (`carrier.go:20-38`), so the two being separate operations introduces no new race.

## 7 — The coordinator side (Q7)

**Decision: the direct branch is deleted, not narrowed.** `session_open.go:305-307`'s `svc.Open` is
unreachable for a remote session: `OpenSpec.Kind == "ssh"` (`:274`) is what makes `cfg.Kind` a
`session.KindRemote` (`session.go:89-92`; set at `:370` and `:422`), and a `session.KindRemote` destination
no helper claims is served by this machine's helper (`localHelperOpener.OpenHosted`, `helper_local.go:154-157`,
which today claims `session.KindLocal` only). Three checks, because a source walk and a behavioural test each
see half:

1. **Behavioural, at the seam**: an opener that selects every remote destination plus a fake `OpenService`
   whose `Open` fails the test if reached; open a pane over the real handler and assert the counter is zero.
   Harness exists: `transport/ws_helper_open_test.go:83,143,280`, `app/worker_happypath_test.go:673`.
2. **The refusal**: with no local generation installed (`errNoLocalGeneration`, `helper_local.go:41-42`) a
   remote open answers that refusal rather than silently dialing.
3. **A source-walk ratchet**, same shape and reason as `helper/session/one_pty_owner_test.go:1-30` and
   `helper/endpoint/no_tcp_listener_test.go:1-57`: the session-open path must contain no call to
   `capability.OpenService.Open`/`ssh.RealClient.Connect`. Its header must say what it cannot prove —
   uniqueness is not reachability — and point at (1) as its paired half.

`capability.OpenService.Open` then has no production caller and the deadcode ratchet removes it; that
deletion is part of the same landing group.

## 8 — Failure paths (Q8)

| failure                                                                         | answer                                                                                                                                                                                                                                                                               |
| ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| local daemon not installed / not started, or an older generation without the op | named refusal at the act (ADR-0057; `errNoLocalGeneration`), or `unknown_op` → "this machine's helper is older than this app" + reinstall — never a generic open error                                                                                                               |
| ssh dial or auth fails / vault sealed                                           | the helper returns the classified reason; the vault's own error rides the callback (`session_open.go:353-359`)                                                                                                                                                                       |
| host key unknown or changed                                                     | the callback decides; the coordinator writes `known_hosts` on accept, the helper retries **once** (a bound, not a loop)                                                                                                                                                              |
| far side refuses exec                                                           | the existing replacement path (`ssh_real.go:1010-1028`): reason recorded, a plain shell opens, the runtime still answers                                                                                                                                                             |
| helper fails mid-open (spawn ok, attach failed)                                 | the existing rollback (`helper_git.go:530-560`, `hostedSpawn.run`) closes the helper session                                                                                                                                                                                         |
| **coordinator dies between spawn and the durable binding**                      | the L7 claim (`session_open.go:60-70`, `proto.SpawnParams.IdempotencyKey`) — **missing on the remote path today**: `helper_local.go:382` calls `h.remote.OpenHosted(ctx, cfg)` without it, so a repeat forks a second shell. One-line fix, in scope, with a test                     |
| the channel or the helper dies mid-session                                      | the attachment ends; the session goes terminal with a cause through the existing loss seam (`helper_local.go:126-131`), never stuck at `starting`. An ssh EOF is exit status in the inventory, reported on reattach — better than today, where the session dies with the coordinator |
| coordinator restart                                                             | the helper holds channel, runtime and window; the next coordinator asks the local helper's inventory and re-adopts (`app/session_readopt.go`; remote consent re-ask at `:177`). **New case**: the destination is remote while the carrier is local — readopt does not cover it today |
| helper restart                                                                  | out of scope: nothing retires a generation yet (`helper/local/local.go:33-40`)                                                                                                                                                                                                       |

## 9 — Tests (Q9)

Go layer: the repo's in-process `x/crypto/ssh` fixtures (`app/helper_open_password_test.go`,
`ssh/ssh_real_test.go`, `transport/tunnel_test_server_test.go`). e2e: `cmd/e2e-sshd` — real commands on a
real PTY, `tcpip-forward`/`direct-tcpip` (`cmd/e2e-sshd/main.go:1-45`). Five tests: **(1)** the acceptance
(owner rule 2) — a program on the fixture's shell asks DSR (`ESC[6n`) and prints the answer, **with no client
attached** (no WebSocket, no renderer), and the helper's own inventory shows the reply, proving the runtime
answered; a real-pty hex readback, no timing. **(2)** restart — kill the coordinator, start a second, re-adopt
from the local helper's inventory; assert the same session id, the same window, a live write. **(3)** §7's
three checks. **(4)** failure — fixture down; helper present but ssh refused; the named reason each time.
**(5)** discrimination — each new refusal proven by mutating the handling and watching that test fail.

## 10 — Commits and the parallel split (Q10)

Collision with the concurrent worker (`panegrid`, `paneview` (new), `paneobserve`, `agenttyping`,
`agentcalib`, `agentcapture`, `app/paneenrol.go`, `transport/ws_panegrid.go`, and a **new `screen` op on the
helper session service**):

- **Hard collision**: `helper/session/service.go`, `helper/proto/session_service.go`, `contracts/helper/*`,
  `proto/version.go` — both workers add an op **and both bump `Version`**, a single number. Sequence: the
  `screen` op lands first (smaller, in flight, verified absent: `proto/session_service.go:42-81` lists exactly
  six ops) and takes **2 → 3**; this bead rebases and takes **3 → 4** with its op _and_ its launch-record union
  in one generation (§5). **Same file, different regions**: `app/app.go` (the other worker at
  1417-1506/1836/1871; this bead at ~1374/1524) — serialize, never parallel. **Disjoint**: `Makefile`,
  `helper/deploy/**`, `cmd/nocx-helper/**`, new `helper/session/spawn_ssh*.go`, new `helper/sshdial/**`,
  `internal/ssh/**` (new files only), `helper/local/**`.

Order (each commit passes the pre-commit hook — a new package lands with its wiring): **(1)**
`build(helper): the local daemon links an ssh client and the shipped artifact does not` — tag, Makefile
target, second embed source, both dependency assertions, re-measured size. **(2)** `feat(helper): the ssh
service dials, and the coordinator holds no dialer` — the `ssh` service (conn/channel/forward/named
probes/sign/secret/host key), reverse-request serving in the client, version bump, wiring, **plus** the
consumers moving one per commit until `internal/ssh`'s dialer and `Probe` have no coordinator caller (large;
not splittable by package without failing the deadcode ratchet). **(3)** `feat(helper): a session whose channel
is a remote shell` — `sshProcess`, `spawn-ssh`, the launch-record union, the helper-side launcher and
bootstrap. **(4)** `feat(app): every ssh pane is opened on this machine's helper` — the local opener's remote
arm, the fallback deletion, `capability.OpenService.Open` removed, the claim fix, §7's checks. **(5)**
`test(app): the pane, its answer, and its survival` — §9.

## 11 — What in the tree contradicts this brief (Q11)

Verified true: `session_open.go:289-307`; `app.go:783`; `ssh_real.go:171-190` and `:1023`; `helper_git.go:455`;
`proto/session_service.go:186` (no destination, field by field); `cmd/nocx-helper/main.go:184-191` (only
`LocalSpawner`); `deploy/dependency_test.go:33-50`; `session/session.go:33-46`; `ssh/ssh.go:450`
(`ConnectConfig`, not a wire request); the freeze rules (`contracts/helper/README.md:72-76`, `:114-119`).

Three corrections and one trap. **(1) `registerLane` is not the pane-observation path.** `app.go:1911-1917`
binds a _lifecycle lane_ to its session; the pane grid is fed from the coordinator's own byte path by the
enrolment act (`app/paneenrol.go:102` `grid.Enrol`, feeding `transport/ws.go:3290`). The brief's
"registerLane → paneenrol.go:80,102" conflates two seams; the conclusion is right, the path is not.
**(2)** `paneview` does not exist here and there is no `screen` op yet (`proto/session_service.go:42-81` lists
exactly six) — both are the other worker's. **(3)** The freeze cuts both ways: the brief's `spawn`-destination
idea is _not_ a safe extension (§5); only a new op is. **(4) Trap for the landing**:
`helper/endpoint/no_tcp_listener_test.go:1-57` is a source-walk ratchet over a **fixed package list**
(`thePath`, `:38-42`), asserting every `net.Listen` names `unix`. Outbound `net.Dial` is not covered, so a
dialing helper is legal — but any _new_ package on the coordinator↔helper path must be added to `thePath`, and
the ssh code must never listen: a `net.Listen` for the lifecycle tunnel is exactly the shape that test
catches, which is why §6 gives the listener to the connection and never to a socket.

## The decision the coordinator owes this plan

"One pool owner" and this route cannot both land incrementally: §3's migration is the prerequisite and is
bigger than the pane. Recommended: split into (a) the helper's ssh connection service + consumer migration and
(b) this route, with (b) blocked on (a). A single landing group is the alternative this plan sequences — but
the route must not be enabled on a tree where two processes dial one host.
