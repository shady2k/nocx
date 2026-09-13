# `contracts/helper/` — the FROZEN helper ABI

The schemas one directory up describe the **JSON-RPC control plane** between our backend
and our renderer: both ends ours, both ends this build, and a mismatch lasts until the
next release. `contracts/files/` describes **files on disk**, which any build may have
written.

These describe a third boundary, and it is the one where being wrong is permanent: the
wire between a **coordinator** and a **helper generation** on some host.

|                               | `contracts/*.schema.json`  | `contracts/files/*.schema.json` | `contracts/helper/*.schema.json`         |
| ----------------------------- | -------------------------- | ------------------------------- | ---------------------------------------- |
| Who is at the other end       | our renderer, this build   | a document, any build           | a helper generation, possibly months old |
| Generated renderer types      | yes (`npm run contracts`)  | no                              | **no** — the renderer never speaks this  |
| Can a mismatch be fixed later | yes, next release          | yes, by the version protocol    | **no** — see below                       |
| Validated by                  | `internal/transport` tests | `internal/apicoll`              | `internal/helper/client` tests           |

## Why "no" is the whole point

A helper install is content-addressed and immutable, two generations are resident at
once, and a generation lingers for exactly as long as it holds a session — months, in
the case the whole level exists for. So there is no release at which every peer has been
upgraded, and a shape that was wrong when a generation shipped stays wrong for the life
of the sessions it holds.

Three consequences, and they are decisions rather than observations:

- **A generation whose ABI assumes one unnamed client can never later serve two
  observers correctly.** Hence a subscriber on attach, on data and on write, and an
  independent 64-bit cursor per subscriber — reserved and barely used today.
- **An opaque identifier can never later become authorization.** The trust boundary is
  the Unix account: any nocx under that account may connect, and no session capability is
  reserved. If independent same-UID servers must ever be isolated from one another, the
  capability is owed **before** the next generation ships.
- **A frame type nobody allocated is garbage.** The decoder resyncs past an unknown type
  byte one byte at a time — through a live PTY stream, in the case that matters — so the
  data-plane type byte is allocated now and both ends already recognise and drop it.

## Not generated into TypeScript, and deliberately

`frontend/scripts/gen-contracts.mjs` reads `contracts/` and does not descend here. Nothing
in the renderer speaks to a helper: the renderer's socket is the coordinator's, and what it
knows about a remote session it knows through the control plane one directory up. Putting
these shapes into `frontend/src/generated/` would hand the renderer a type for a wire it
must never reach.

A generated type exists to stop a hand-written one drifting from the wire. Here there is
no second hand-written type to drift: both ends of this socket are the same Go package,
`internal/helper/proto`. The drift these schemas catch is the other one — the shape
changing at all — which is what freezing means.

**Do not move these files up one directory.** `gen-openrpc.mjs` turns every top-level
`*.params.schema.json` into an OpenRPC **method**, and
`internal/transport/TestOpenRPCManifestMatchesRegisteredMethods` then demands a
JSON-RPC registration for it that does not and must not exist.

## `identities.schema.json`

It declares `$defs` and nothing else; every other schema here `$ref`s into it, so one
concept has one declaration (AD-8). The three identities are separate there for the same
reason they are separate Go types: before the correction of 2026-08-31 "the session"
meant the coordinator-owned PTY channel, so the channel's death was the session's death,
and conflating the two is exactly what makes a replacing coordinator delete live work.

## What is deliberately not here

- **The data plane.** AD-1 governs this wire as it governs the WebSocket: raw PTY bytes
  are never wrapped in JSON, JSON-RPC or base64, so the data frame has no JSON shape to
  pin. Its layout is frozen by literal-byte golden vectors in
  `internal/helper/proto/abi_test.go` instead.
- **Runtime validation in production.** Both ends of this socket are ours, exactly as one
  directory up.
- **`close-session`, `signal` and `uninstall`** — D9's remaining verbs, which land with
  `nocx-k6p18.7`. An op ADDED by a later generation degrades gracefully (an older helper
  answers `unknown_op`); an op renamed or reshaped does not, which is why only the ops that
  exist are spelled.

## What landed with `nocx-k6p18.3`

The **inventory and spawn shapes** were the half `nocx-k6p18.1` deliberately left open,
because freezing `spawn` and `sessions` without their semantics would have been worse than
leaving them. They are frozen now, with the service that answers them:
`session.spawn.params`, `session.spawn`, `session.sessions.params`, `session.sessions`,
`session.resize.params`, and the `$defs` they share — `workspaceId`, `launchRecord`,
`observation`, `windowSpan`, `sessionExitStatus`, `sessionEntry`.

Three of those `$defs` carry a decision rather than a shape, and each is enforced by the
schema rather than left to a reviewer:

- **`launchRecord` and `observation` are two fields and never one.** What the helper
  recorded when it spawned is the authority; what `/proc` says now is evidence. argv is
  mutable by the process itself, a process can be replaced by `exec`, and macOS has no
  `/proc` at all — so merging them would report a lie with the authority of a launch record.
  `observation` is `null` when nobody could be asked, never `{}`.
- **`sessionEntry` has no name, and `additionalProperties: false` is what keeps it that
  way.** The helper reports derived diagnostics because the OS is their source; a friendly
  alias is a projection owned by the local server. One owner ever.
- **`spawn` takes no command and no argv**, and the schema refuses one rather than merely
  omitting the field — an accepted-but-ignored field is what a later generation decides to
  start reading.

Also here now: `workspaceId`, D15's reservation, in `spawn`, in `sessions` and in every
entry. It is unused by this generation and **required to stay**.

## What changed with `nocx-k6p18.10`, and why it could still change

One field: `observation.unavailable`, required, a closed set of the diagnostic names
`observation` itself carries.

It is a **shape change to a frozen `$def`**, and the freeze is what makes that worth
spelling out rather than quietly doing. Nothing is deployed: no generation of this helper
has shipped, so there is no peer months old holding a session against the old shape, and
this is still inside the window `nocx-k6p18.3` named when it froze `sessions` and `spawn`
("the last moment it could be"). After the first published generation the same change is
not available — `additionalProperties: false` means an older reader REJECTS a payload
carrying a field it does not know, so a field added later is a break rather than an
extension.

The defect it closes is one the freeze itself created. Every optional field in
`observation` is omitted when empty, so a diagnostic the OS could not answer arrived as
nothing at all — the same bytes as an inspector with nothing to add. A reader with nothing
falls back to `launch.cwd`, which was true once and goes stale the moment the user `cd`s,
and it then shows the directory a shell STARTED in as though it were where the shell is.
That is a stale value carrying the authority of a launch record, which is the one thing
splitting `launch` from `observed` exists to prevent — arriving by the back door.

macOS is where it bit, and it is the platform nocx ships on first. There is no `/proc`;
`proc_pidinfo(PROC_PIDVNODEPATHINFO)` is the only route to another process's working
directory and it needs cgo, which the helper's size argument refuses. So the darwin
inspector answers `argv` and the foreground command through `sysctl` — cgo-free, through a
dependency the helper already linked — and reports `["cwd"]` here. `observed` is not null
there any more: the platform has evidence, it just does not have all of it, and those are
different answers.

The rule the field states is per-OBSERVATION and not per-platform: a `/proc/<pid>/cwd` read
that was refused is named the same way a platform that cannot answer at all is. The
reader's problem is identical and the reason is not its business.

## What landed with `nocx-50w7p.2`, and why `Version` moved to 3

The **`ssh` service**, and with it the second direction this wire has always reserved and
never used. `probe` is a forward op — the coordinator asks, the helper dials and answers —
and `secret`, `sign`, `verify-host-key` and `trust-host-key` are **reverse ops**: the
helper asks, over the same connection its request arrived on, and the coordinator answers
through handlers registered at its composition root. `TypeRequest` and `TypeResponse` were
allocated in both directions before either was spoken that way (`proto/frame.go`), and the
version bump is what makes a generation that can answer the reverse half distinguishable
from one that would drop the question: two peers that disagree refuse each other at hello,
in both directions.

The service exists because of the owner's invariant of 2026-09-13 — there is no ssh
connection without a helper, and the coordinator holds no ssh client — and the reverse half
exists because the two things a dial needs are things the helper must not have:

- **the material.** A password crosses (`ssh.secret`), because ssh has no protocol for
  proving one without presenting it; a private key does not (`ssh.sign`), because a
  signature proves possession without handover, and `ssh/knownhosts` and the client stack
  are forbidden imports in the deployed artifact
  (`internal/helper/deploy/dependency_test.go`). The reference is the COORDINATOR's and is
  opaque to the helper, so the helper cannot ask for material the coordinator did not name.
- **the host-key decision.** `ssh.verify-host-key` answers `trusted`/`changed`/`unknown`
  from the coordinator's own `known_hosts`, and `ssh.trust-host-key` writes through the one
  write path this repository has for it. The DECISION travels in `probe.acceptOnTrust`,
  which the coordinator sets; a helper cannot turn "ask the user" into "trust it", and a
  `changed` verdict never reaches the write.

Three decisions in these shapes are worth naming rather than leaving to a reader:

- **The public key rides in the identity, and there is no discovery op.** x/crypto/ssh's
  `Signer` answers `PublicKey()` _before_ it asks for a signature, so the helper must be
  able to say which key it is offering. Putting it in the identity — where the party that
  holds the private half already knows it — keeps `sign` a two-field cryptographic request
  and removes any temptation to make an empty challenge mean "identify yourself".
- **The outcome spellings are `ssh.ProbeOutcome`'s, character for character.** The
  coordinator classifies its own probes with `ssh.ClassifyProbeError`; a second vocabulary
  for one fact is the defect AD-8 names, so the wire spells the same six values and
  `internal/helper/sshsvc` holds the test that keeps the two in step.
- **A failure nobody can classify is a refusal, not an outcome.** `rejected` means the
  server refused the credential; folding an unrecognised dial failure into it would send a
  person to look at the host.

The schemas are frozen from here like every sibling's: a new op degrades (an older helper
answers `unknown_service`), and a new FIELD on one of these shapes does not.

## What landed with `nocx-50w7p.3`, and why `Version` moved to 4

The **proxied-channel plane**, and with it the last thing the owner's invariant
needed before consumers could move: a connection the helper dials has to be one a
caller can put a CHANNEL on, or it is a resource nothing uses.

Two forward ops — `open` and `close` — plus a frame type, `TypeChannelData`. The
frame type is the half that makes the bump unavoidable rather than tidy: the
decoder treats an unknown type byte as garbage and rescans one byte at a time, so
a generation speaking 3 would resync **through** a live sftp stream instead of
dropping one frame.

Four decisions in these shapes are worth naming rather than leaving to a reader:

- **A proxied channel has its own identity and its own layout.** `ChannelID` is
  16 raw bytes and the frame after them is `[channel-id][payload]` — not the
  frozen session layout with a different meaning. Reusing `TypeSessionData` would
  have been the quiet version of wrong: a channel id in the `Session` field
  decodes perfectly, routes to the session service, and is dropped against an
  inventory that has never heard of it. A frame that decodes into the wrong
  router is worse than one that does not decode.
- **The end of a stream is said, never inferred.** A payload exactly the header's
  length is a legitimate write of no bytes, so `ssh.channel-closed` is a
  notification and not a zero-length frame. Ordering is what makes it sufficient:
  a `TypeNotify` rides the same wire as the data frames, so every byte written
  before it has already been written.
- **The helper opens the kind, the caller never names a command.** `kind` is a
  member of a closed set (`sftp` today) and not an argv: D3 refuses a free-form
  string list, and a caller that wants another channel adds a member in a
  generation, with a bump and a test.
- **The refusal codes an open ends in are `probeOutcome`'s own spellings.**
  `unreachable`, `rejected`, `needs-interactive`, `host-key-unknown`,
  `host-key-changed` — one vocabulary for one set of facts, because the
  coordinator already switches on it. The two host-key codes carry
  `hostKeyEvidence` in their details, so the coordinator can rebuild its own
  typed error: nothing carries a Go value across this socket, and an accept sheet
  without the fingerprint is a sheet nobody can answer.

`channel_refused` is the one code of its own, and it is deliberately ONE code for
two refusal points — the session and the subsystem request. A caller cannot act
differently on them (either way this host will not serve that channel) and the
sentence keeps the distinction.

The schemas are frozen from here like every sibling's: a new op degrades (an older
helper answers `unknown_op`, which a coordinator reads as "this machine's helper
is older than this app"), and a new FIELD on one of these shapes does not.

## What landed with `nocx-50w7p.4`, and why `Version` moved to 5

The **ssh pane**: a session whose process is a shell channel on a connection the
helper dialed, rather than a PTY this machine forked. Two shapes carry it.

**A new op, `session.spawn-ssh`, beside `spawn`** — never a destination field on
`spawn`, which the freeze forbids. Every shape here is `additionalProperties:
false`, so a field added to `spawn` is a payload an older generation REJECTS,
while a new op is one it answers `unknown_op` to; and a destination on `spawn`
would have to be absent on every local spawn, making its absence the common case
and its presence a change in what the op means. Its params carry the destination
as typed fields (via `ssh.schema.json`'s own `$defs/destination`, so "a resolved
ssh destination" has one declaration — the same one `probe` and `open` take), the
caller's host-key statements, the session's geometry, the mode, and the far
host's two tool-surface paths. There is no command and no argv: the remote
command is the launch carrier, built by the helper from `internal/shellintegration`.

**`launch` is a discriminated union**, with `kind` required and the branches under
`local` and `ssh`:

- `local` is the record every session had before, field for field, moved one
  level down. A local session's facts are unchanged.
- `ssh` has **no `pid` and no `pgid` key at all**. That is the shape rather than
  an omission: the process is on another machine, its pid belongs to that
  machine's namespace, and the only value a record insisting on one could carry
  is 0 — the kernel scheduler — which a reader would then ask the OS about.
  Absence is the honest encoding, and `additionalProperties: false` is what makes
  it an absence the decoder enforces.
- `ssh.cwd` is a required key that is **always empty**: the far login shell
  starts in the far account's own directory and this helper has no mechanism to
  move it, so the record reports the resolution it performed — none. The key
  stays because every reader asks the same questions of whichever branch it
  holds, and `spawn-ssh` REFUSES a caller's non-empty `cwd` by name rather than
  accepting one nothing acts on.
- For such a session `observed` is `null` — "nobody could be asked". Evidence
  about pids is evidence about THIS machine's kernel, and the helper's
  OS-evidence seam is never reached for a session with no process here.

Three decisions in these shapes are worth naming rather than leaving to a reader:

- **The refusal codes a `spawn-ssh` ends in are the ssh service's own.** An
  unreachable host, a credential the server refuses, a key nobody recorded, a
  key that CHANGED, a sealed vault and a helper with no coordinator connection
  are the vocabulary `probe` already reports, and a `changed` key carries
  `hostKeyEvidence` in its details for the same reason it does there. A helper
  service that hands one of these on keeps the code: reporting `internal` instead
  would tell every caller the helper broke, while the sheet that should have been
  raised never was.
- **A build without an ssh client answers `no_ssh_client`.** A helper built
  without `nocx_local_ssh` links no client, so `spawn-ssh` there is a fact about
  the BINARY and not about the request: it is distinct from `spawn_failed`
  (nothing about the request would work) and from `unknown_op` (this generation
  is not older — it is built for a host that must not dial).
- **The enhanced lifecycle channel and the tool socket are NOT in this
  generation's ssh pane.** The lifecycle channel is a remote loopback listener on
  the connection and the socket needs the same forward; both are the
  `direct-tcpip`/forward ops of nocx-50w7p.8. A request carrying a lifecycle
  launch is answered with the PANE and no lifecycle window — `adopt-lifecycle`
  answers `null` and the helper logs the refusal by name — because ADR-0004 makes
  an ordinary usable terminal the one thing no failure path may suppress, and
  losing it to nocx's own optional work is the only way to lose it that is nocx's
  fault. The two bearer values are accepted and never rendered anywhere.

The version moved because the entry changed shape: `sessionEntry` is
`additionalProperties: false` on both sides, so a generation speaking 4 REJECTS an
entry carrying `kind`, and one speaking 5 answers a `spawn` with a shape a
4-reader cannot read. Two peers that disagree refuse each other at hello in both
directions, and each generation installs beside the other — which is what makes
the entry change safe: no reader of the new shape was built without it.

The schemas are frozen from here like every sibling's: a new op degrades (an older
helper answers `unknown_op`), and a new FIELD on one of these shapes does not.
