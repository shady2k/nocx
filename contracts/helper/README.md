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

## What landed with `nocx-50w7p.8`, and why `Version` moved to 5

The **forward plane**, which is the other direction a channel can be born in.
The proxied-channel plane let the coordinator ASK for a stream and hold it; a
forward is the far side LISTENING, and the connections that arrive on it are
nobody's request here — so they need an announcement, and the listener needs an
identity and an end of its own.

Three shapes and two events:

- **`direct-tcpip` joins `channelKind`, with a `target`.** It is the outbound
  half of every forward: -L dials the far side's network, and a SOCKS CONNECT
  names a host and port that only the far side can resolve. The target is a
  `channelTarget` — TYPED, two fields, never a `"host:port"` string — and the
  schema requires it exactly when the kind does (`ssh.open.params`'s if/then,
  with a test that proves the condition fires).
- **`forward` and `unforward`**, plus `forwardId`. A listener is not a channel:
  it is a stream FACTORY, it is created by a different op, and it is ended by a
  different one. Folding it into `open` would have made "accept on the far side"
  a mode of "open one stream", and the two would then have to agree about who
  announces what.
- **`forwarded-tcpip` and `forward-closed`.** The first says a connection
  arrived and names the channel its bytes are keyed by; the second says the
  listener is over, with a cause that is empty when the coordinator asked and
  the helper's sentence when it broke. Both ride the data plane's ordering
  guarantee (a `TypeNotify` takes the same writer mutex as the frames), which is
  what makes an announcement sufficient where a response is not available.

Three decisions worth naming rather than leaving to a reader:

- **A forwarded connection is an ORDINARY channel.** It is registered in the
  same table, read through the same `ChannelStream`, closed by the same `close`
  and ended by the same `channel-closed`. Only its creation differs, and its
  lifetime is tied to the listener's: cancelling a forward ends the streams it
  produced, because a listener and the streams on it are one resource.
- **The accept loop starts after the response, like the reader pump.** An
  accepted connection is announced under the forward's id, and the coordinator
  learns that id from the response — so a loop started in the handler could
  announce a connection nobody can address. `ResponseObserver` is the same
  happens-before edge `open` uses, one op over.
- **A refused forward is a refusal, not a dial failure.** The server's
  `AllowTcpForwarding` and its `PermitListen` are indistinguishable on the wire;
  the sentence carries both possibilities rather than inventing a diagnosis,
  which is exactly what the -R strategy has always said.

The schemas are frozen from here like every sibling's: the two new ops degrade
(an older helper answers `unknown_op`), and the `target` FIELD does not — which
is what makes this a version bump rather than an addition, since a helper
speaking 4 would build its params schema from the same struct, accept a field it
does not know, and answer a direct-tcpip open as a subsystem one.

## What landed with `nocx-50w7p.4`, and why `Version` moved to 6

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

## What landed with `nocx-50w7p.9`, and why `Version` moved to 7

**Every probe is a named op, and a lease is what they run on.** Seven ops join
the `ssh` service — `lease`, `unlease`, `uname`, `home`, `sample-ports`,
`completion`, `command-names` — and with them the last shell commands the
coordinator used to compose for a host: `uname -s -m` for the platform probe,
the port-discovery ladder, the completion probe, the PATH enumeration. What a
caller supplies now is a lease, a member of a closed set and typed arguments; no
command, script or argv crosses, and the text of each one lives in
`internal/remoteprobe`, which both ends link.

Three decisions in these shapes are worth naming rather than leaving to a reader:

- **A lease is not a channel.** A probe asked with no reference held would
  acquire and release inside the one request, closing the connection for the
  next probe to redial it — a second authentication per sample, on hosts where
  that is how an account gets banned. So `lease` takes a pooled reference and
  answers its id, every probe op runs on the connection that reference keeps
  alive, and `unlease` drops it. It is the `ssh` service's own `open` for a
  caller that wants no stream.
- **One op per probe, and never a field on an existing one.** Each has its own
  parameters and its own result, and a new FIELD on a frozen op is a break
  (`additionalProperties: false`) while a new op is one an older helper answers
  `unknown_op` to — which is exactly the reading a coordinator needs.
- **One result shape for all five probes** (`probeExecResult`: captured streams,
  remote exit status, `truncated`). Each caller classifies those facts its own
  way — discovery reads exit 127 as "tool absent" and lsof's exit 1 as a valid
  empty sample, completion rejects an unframed answer, deploy refuses a nonzero
  status outright — and none of those are decisions the helper may take for the
  caller. `truncated` is what keeps a PREFIX from being read as a complete
  table, which for port discovery is the difference between "no listeners" and
  "I could not see them".

The bump is taken because of what the seven MEAN rather than because seven ops
were added: a coordinator speaking 6 has no ops to ask these questions with and
would fall back to its own dial — the state the owner's invariant exists to end
— while a helper speaking 6 answers `unknown_op` to every one of them, which a
coordinator reads correctly as "this machine's helper is older than this app"
rather than as a host that refuses probes.

The schemas are frozen from here like every sibling's: a new op degrades, and a
new FIELD on one of these shapes does not.

## What landed with `nocx-50w7p.10`, and why `Version` moved to 8

The **exec lane**: the `lane` op, which is how the git-over-a-remote-helper
bridge rides a connection the HELPER dialed, plus the two shape changes the same
migration needed.

Until this generation the coordinator opened that lane itself — an ssh exec
session running `nocx-helper bridge <generation>` — over `internal/ssh`'s
`HelperConn`, and under the owner's invariant of 2026-09-13 no ssh connection
exists without a helper. So the lane becomes this service's op, and the
coordinator's own dial is deleted with `HelperConn`. The remote helper's own ABI
is untouched: what rides a lane is the frame protocol, and what is on the far end
is the same bridge subcommand the coordinator used to start.

Three decisions in these shapes are worth naming rather than leaving to a reader:

- **A lane names an install and a generation and never a command.** Its params
  carry the machine's install DIRECTORY and the generation, and the helper turns
  those into the one invocation it is allowed to run — `deploy.InstalledBinary` +
  `endpoint.BridgeInvocation`, the same derivations the installer uses, in one
  place. That is D3 at the only op whose payload is a program: `host.Register`
  refuses a free-form argv, and the directory is the narrowest identity that
  still determines a binary, so the only thing a caller can point a lane at is a
  directory holding a helper. A command would have been a caller-chosen
  executable on somebody else's machine.
- **The directory is the fact both paths already hold.** An install is
  content-addressed and the coordinator records the directory it wrote
  (`consent.Install.Path`), and a session re-adopted after a coordinator restart
  carries the installed binary's path in its durable route. Home and platform
  would have had to be recovered from a path — a second derivation of the install
  layout, and the regression AD-8 names.
- **`ssh.channel-closed` grew `exit`, and a lane is why.** A lane's far end is a
  PROCESS, and the coordinator's own classification reads the status: the
  bridge's 43 is "no helper is serving that generation", whose sentence and whose
  recovery differ from every other pre-sentinel ending. Folded into a lost
  transport it becomes a connection error and a retry, and a person is told
  something untrue. The field is a pointer so absence stays a fact: an sftp
  subsystem and a direct-tcpip connection have no exit status at all, and `0` is
  an ordinary way for a process to end.
- **`ssh.probe` grew `fingerprint` and `hostKey`.** The settings surface's
  connection test moved onto this op with the same migration, and the probe's
  answer had to keep two things it used to return in-process: the offered key's
  fingerprint, which the surface STORES as the machine's identity on first
  contact, and the evidence the accept sheet and the mismatch warning are built
  from. Nothing carries a Go value across this socket, so the coordinator
  rebuilds `ssh.ErrUnknownHostKey` / `ssh.ErrHostKeyMismatch` from these fields —
  the same shape a refused channel has carried since generation 4.

The version moved because the shapes moved: a generation speaking 7 accepts only
what it was built with (`additionalProperties: false`), so a probe result or a
closed event carrying these fields is a payload it rejects rather than tolerates.
Two peers that disagree refuse each other at hello in both directions.

The schemas are frozen from here like every sibling's: the new op degrades (an
older helper answers `unknown_op`, which a coordinator reads as "this machine's
helper is older than this app"), and a new FIELD on one of these shapes does not.

## What landed with `nocx-50w7p.18`, and why `Version` moved to 10

One field, on two ops: `agentToolEndpoint` on `session.spawn.params` and
`session.spawn-ssh.params` — the tool endpoint **on the helper's own machine**
that a pane's tool connections belong to, which is the CALLER's own socket.

It replaces a value the daemon read once from its own start environment
(`NOCX_TOOL_SOCKET`), and the defect that value produced is the reason this is a
wire change rather than a local one. A helper endpoint socket is keyed by the
GENERATION, and its directory is derived from the account's home and nothing else
(`internal/helper/endpoint.Dir`) — one daemon per generation per account, serving
**several coordinators at once** (D12). A daemon that takes its tool target from
the environment of whichever coordinator happened to start it therefore has a fact
about that coordinator, and every later caller inherits it: a pane opened by
coordinator B had its far agent's tool connections forwarded to coordinator A's
endpoint, and a local pane B started carried A's `NOCX_TOOL_SOCKET` into its
shell. Both are the same mistake at two hops — the pane's tool connections belong
to the coordinator that OPENED THE PANE, and only that coordinator knows the
value. So it travels on the spawn request, empty meaning "I run no endpoint",
which is the state `cmd/nocx-server` already has when it publishes no tool
surface.

Three things about the field are worth naming rather than leaving to a reader:

- **It is a second field beside `agentToolSocketPath`, not a rename of it.** They
  name the same concept on two different machines: that one is a path on the FAR
  host, this one is an endpoint on the machine the helper runs on. Folding them
  would have made one field mean two things depending on which op it arrived on,
  which is exactly the kind of shape a later generation reads wrongly.
- **A far socket path with no endpoint is refused by name, before anything is
  dialed.** The refusal exists already (`sshsvc`'s `errNoToolSocket`, raised in
  `validatePaneSpec` before the connection is acquired); what changed is that the
  emptiness is now the request's rather than the daemon's, so a caller that runs
  no endpoint costs a far host nothing.
- **A pane whose coordinator has gone is refused, never re-routed.** The target is
  the one the request named, so an endpoint that no longer answers ends that
  connection with the path in the helper's own account of it — there is no second
  endpoint the daemon could fall back to, because there is no longer a daemon-held
  one to fall back TO.

`nocx-50w7p.11` took the number to 9 (the route and the prompt) in the same
window this change was written in, so this one is 10 rather than 9; nothing else
about it differs. The bump is the freeze's own rule and not tidiness: both
shapes carry `additionalProperties: false`, so a helper speaking 9 builds its
params schema from the same struct this one does and REJECTS a payload carrying
the field, answering `bad_params` — a sentence about a request that is
well-formed. Two peers that disagree refuse each other at hello in both
directions, each generation installs beside the other (D7), and a session still
holding the old binary keeps it.

The schemas are frozen from here like every sibling's: a new op degrades, and a
new FIELD on one of these shapes does not.

## What landed with `nocx-50w7p.19`, and why `Version` moved to 11

`nocx-50w7p.18` took the number to 10 (the pane-scoped tool endpoint) in the same
window, so this one is 11; nothing else about it differs.

The **key queue**: `ssh.identity` no longer carries one `credential` plus one
`publicKey` for key auth. It carries an ordered `keys` list — each entry a public
half beside the reference the coordinator signs through — and the password arm is
unchanged in meaning, so `credential` now belongs to password alone.

Two ordinary setups were refusals before this, and both are the same shape
underneath: the coordinator could nominate exactly ONE key per dial.

- **A profile that names no credential.** "Connect to this host with my keys" is
  what most saved profiles mean, and OpenSSH answers it by offering what it finds:
  the agent's keys, and the identity files its configuration lists — ssh's own
  default `~/.ssh/id_*` list when the configuration lists none, honouring
  `IdentityFile` and `IdentitiesOnly`. The coordinator's own dial path always did
  this; through the helper the same profile was refused, because there was one
  slot and nothing to put in it.
- **An agent holding several keys.** Only one of them is the key a given host
  accepts, and it is frequently not the one the agent lists first. Offering the
  first was a refusal that reads as "the host rejected your credential".

**A queue is not a second attempt at authentication.** ssh's `publickey` method IS
a query per key: the client declares a public half, the server answers whether it
would accept a signature with it, and the client either signs or moves on. That is
what `gossh.PublicKeys(signers...)` implements, so the whole queue travels inside
ONE method and one connection's auth — which is why this is not the thing
`MaxAuthTries` exists to bound. What crosses is a public half per key; every
private half stays in the coordinator, and `sign` is still exactly a challenge and
a signature.

The version moved because the SHAPE moved. `additionalProperties: false` means a
10-helper REJECTS an identity carrying `keys`: a key-auth dial would not degrade
into a single-key one, it would not dial at all, and both setups above would
arrive as protocol failures rather than as the working connections they are meant
to be. An 11-helper reading a password identity behaves exactly as one speaking 10
did — the break is confined to the kind that changed. Two peers that disagree
refuse each other at hello in both directions.
