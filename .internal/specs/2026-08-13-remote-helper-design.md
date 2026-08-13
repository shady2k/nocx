# The remote helper: one binary, several services, git first

- **Date:** 2026-08-13
- **Beads:** `nocx-6zcr` (this session), `nocx-1gsp` (the deliverable it re-answers),
  `nocx-if6` (the epic that owns the relay), `nocx-fihs` (the local Git panel, closed scope)
- **Status:** design, awaiting approval

## 1. In one sentence

A second build target of this Go codebase — one small static binary, launched on a remote
host over one SSH exec channel it owns for its lifetime, hosting a **closed set of named
operations grouped into services**; the Git panel is its first service, the file tree and
port discovery are its next two, and PTY ownership is designed for and deliberately not
built.

What a user can do that they cannot today: **open an SSH tab, see that host's repository in
the Git panel, stage a change an agent made there, and commit it — with the repository's own
hooks running on the machine that owns the repository.**

## 2. What this design crosses, and what those documents already decided

AGENTS.md requires a brief that crosses a boundary to name the `AD`s and ADRs it touches and
what they already decided, **before** it says what to build.

| Boundary                           | What it already decides                                                                                                                                                                                           | What this design does about it                                                                                                                                                                                                   |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-1**, one WebSocket            | Binary data plane + JSON-RPC control plane, a version byte and a reserved `metadata` msg-type allocated up front                                                                                                  | Unchanged. The helper protocol is a **second, private wire** (backend ↔ helper) and is never exposed to the renderer. The renderer keeps talking to the backend exactly as it does for a local repository                        |
| **AD-2**, one core, many targets   | "One Go codebase, multiple build targets", with a remote helper named explicitly                                                                                                                                  | This **is** that target: `cmd/nocx-helper`. It links the same `internal/git/local` and the same parsers, so there is no second implementation of anything                                                                        |
| **AD-6**, byte-blindness           | The backend never parses the byte stream                                                                                                                                                                          | Unchanged and untouched: the helper never sees the PTY stream. The reserved `session` service would **own** a PTY, which is not sniffing one                                                                                     |
| **AD-8**, one owner                | One owner per behaviour; interface-first + DI at one composition root                                                                                                                                             | Every helper-backed surface is a **third implementation of an interface that already has two**, selected at the composition root. `internal/discovery/provider.go:14` already reserves exactly this seam for exactly this binary |
| **AD-9**, ring + offset acks       | The bounded per-session output ring with offset acks **is** lease/replay semantics                                                                                                                                | The frame header carries `seq`/`ack` from day one so the later `session` service can use them without a wire break. Nothing reads them now                                                                                       |
| **ADR-0020**, the lane             | Auxiliary work runs on its own pooled lease, never the tab's                                                                                                                                                      | The helper holds its own pooled reference, like `DiscoveryConn` and `TunnelConn` do                                                                                                                                              |
| **ADR-0024**, authenticated lane   | The lifecycle rides a transport that is not the tty; an `IntegrationDomain` is "one authenticated shell **or helper** instance"; Tier B is "the **lighter** of the two remote binaries the architecture reserves" | The helper is that lighter binary. It does **not** reuse the `-R` transport — see D4 in full                                                                                                                                     |
| **git spec D3**                    | Local only; the remote case waits for the relay, and `DiscoveryConn.Exec` was rejected by name                                                                                                                    | **Amended.** The rejection of `DiscoveryConn.Exec` stands (§3, D0); what changes is that the remote case no longer waits for the _relay_                                                                                         |
| **git spec D16**                   | The seam is named operations, never `Run(argv, stdin, out) → exit`                                                                                                                                                | **Upheld and hardened into a rule with no exception** — see D3 in full, and the measured price orca paid for keeping one exception                                                                                               |
| **git spec D8 / D9**               | Paths and messages ride stdin, never argv; results are bounded on the machine doing the work                                                                                                                      | Both become satisfiable for the first time: the helper has real stdin and applies the bounds where the work happens                                                                                                              |
| **git spec D18**                   | A mutation is never cancelled                                                                                                                                                                                     | Carried across the wire unchanged: `cancel` is refused for mutation operations, not merely unimplemented                                                                                                                         |
| **Footprint consent** (2026-08-10) | Consent is per machine; `DesiredMode` is `raw`/`script`/`relay`; an explicit choice is the consent; the footprint screen lists and uninstalls                                                                     | The helper is the artifact of the `relay` tier. It is **not** smuggled in as "small enough to be zero-install"                                                                                                                   |
| **architecture.md:203**            | "Tier-B remote helper — cross-compiled Go binary augmenting the remote shell, **feeding** the reserved `metadata` msg-type"                                                                                       | **Restated, deliberately.** Ours augments no shell and is not a one-way feed; it is a request/response operation host. Same weight class, different mandate, and the entry is rewritten rather than stretched                    |

## 3. Decisions

| #   | Decision                                                                                                                                                                                                                          | Rejected alternative, and why                                                                                                                                                                                                                                                                        |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D0  | **`DiscoveryConn.Exec` remains rejected for git.** It buffers into a 64 KiB `cappedBuffer` (`ssh_discovery.go:86`) and has no stdin at all, so D8's `commit -F -` and `--pathspec-from-file=-` are unreachable through it         | Raising the cap and adding stdin. That makes the discovery lane a general-purpose remote runner — the process-shaped seam D16 rejected, wearing the discovery package's name                                                                                                                         |
| D1  | **One binary, several services, one process per pooled connection.** Not a binary per feature                                                                                                                                     | A `nocx-githelper`. Three surfaces already point at one binary — `discovery/provider.go:14` (ports), `architecture.md:203` (file-tree), and now git. A second helper would be the fourth answer to a question that already has one reserved                                                          |
| D2  | **The envelope names a service and an operation**: `{id, service, op, params}`. `git` today; `files`, `ports` and `session` are reserved names with no implementation                                                             | An operation namespace flattened into method strings (`git.status`). It works — orca does it — but a service field makes per-service concurrency, per-service bounds and per-service capability reporting structural rather than a naming convention                                                 |
| D3  | **No operation may accept argv, ever. There is no escape hatch and there will not be one**                                                                                                                                        | A generic `exec`, even "temporarily" or "read-only". Measured: orca kept exactly one, and it cost a 300-line allowlist validator — see D3 in full                                                                                                                                                    |
| D4  | **The transport is one SSH exec channel we launch, framed over its stdin/stdout**                                                                                                                                                 | Reusing the `-R` loopback channel of `internal/lifecycleremote`. It exists because the peer there is a shell we do **not** launch; we launch the helper, so we can hand it a pipe — see D4 in full                                                                                                   |
| D5  | **The helper proves it is ours: a sentinel line on stdout, written only after the hello frame is accepted.** Anything else on that channel — a MOTD, a shell error, a `ForceCommand` substitute — is a refusal, never data        | Treating a successful exec request as proof the helper is running. `ForceCommand` accepts the request and runs a different program; we would parse its output as git's. The sentinel is the only thing that distinguishes "started" from "something started"                                         |
| D6  | **Version mismatch is its own exit code and its own client-visible state**, non-retryable, no reconnect backoff                                                                                                                   | Treating it as a transient failure. orca's `EXIT_CODE_VERSION_MISMATCH = 42` exists because a mismatched peer retried forever                                                                                                                                                                        |
| D7  | **Install is an immutable directory keyed by `version+content-hash`**, written under a lock, with old versions pruned when no process holds them                                                                                  | Overwriting one path. A running helper would then be serving new clients off replaced code, and one `$HOME` shared between machines would fight itself. This is orca's `relay-0.1.0+8de1d39fd7c1` layout and VS Code's `~/.vscode-server/bin/<commit>`                                               |
| D8  | **A binary on a machine we do not own requires consent**: the `relay` tier of `DesiredMode`, per machine, listed on the footprint screen with a working uninstall                                                                 | Auto-install because it is only a few megabytes. The 2026-08-10 design reversed exactly that reasoning for scripts; size is not the axis                                                                                                                                                             |
| D9  | **The helper applies the bounds and reports the domain outcome** (`complete`/`capped`/`cut`), and only the retained records cross the wire                                                                                        | Bounding in the backend's reader. It is the reason this design exists rather than an SSH-exec-per-operation one: counting `Total` exactly requires consuming the whole porcelain stream, and a local reader can only do that by dragging it across the network                                       |
| D10 | **Cancellation is an operation on the protocol, not a channel close.** The helper cancels with the process-group escalation `internal/git/local` already has                                                                      | Closing the channel to stop the work. `local.go:132` escalates INT → TERM → KILL **against the group** precisely because `git diff` spawns a textconv filter that holds the pipe open. An SSH channel close makes no such promise about descendants                                                  |
| D11 | **Mutations are never cancelled** (git D18), and a `cancel` naming a mutation is **refused**, not ignored                                                                                                                         | Silently dropping it. A refusal is a fact the caller can act on; a no-op looks like success                                                                                                                                                                                                          |
| D12 | **Transport loss during a mutation is an `indeterminate` outcome, and the panel says so.** It is never auto-retried                                                                                                               | Retrying, or reporting failure. The commit may already have happened, hooks and all. Reconciliation is the next status read, not a guess                                                                                                                                                             |
| D13 | **One goroutine per request, responses multiplexed by id, with per-service and per-request bounds**                                                                                                                               | A single request pump. One `git commit` sitting in a slow pre-commit hook would stall port sampling and the file tree — the whole reason for putting several services in one process is that they must not be able to do that to each other                                                          |
| D14 | **Frames are bounded, and a response above the frame bound is chunked**                                                                                                                                                           | One frame per response. orca measured a large git response head-of-line-blocking interactive echo and answered with a 256 KiB threshold and 128 KiB chunks. Our case is milder — the helper has its own SSH channel with its own window, and D9 already bounds diffs — but "milder" is not "bounded" |
| D15 | **PTY ownership is designed for and not built.** Reserved now: `seq`/`ack` in the frame header, the `session` service name, and the rule that a helper's lifetime is not tied to one channel. Not built: daemon, socket, reattach | Building it now, or ignoring it. Ignoring it is what forces the wire break later; building it is `nocx-if6` phase B wearing a git panel's clothes                                                                                                                                                    |
| D16 | **The zero-install paths stay and remain the fallback.** SFTP for files, the exec ladder for ports, the shell hook for lifecycle                                                                                                  | Migrating them onto the helper. A host that forbids execution, has no writable home, or runs an architecture we do not build for must keep working. Selection happens at the composition root by capability — one interface, three implementations, never two owners                                 |
| D17 | **A helper service may only be the remote half of an interface that already exists locally.** No interface, no service                                                                                                            | Letting the helper grow capabilities of its own. That is how an operation host becomes a platform, and then a shell                                                                                                                                                                                  |
| D18 | **`internal/git` must stop importing `internal/session`.** The `Caller` interface and the binding registry move to their own package                                                                                              | Linking it as-is. `internal/session` imports `pty`, `ssh` and `storage`, so the domain types would drag the whole desktop tree into a binary that needs none of it — measured, §7                                                                                                                    |

### D3 in full: why there is no `exec` operation, at any privilege

D16 already rejected a process-shaped seam. This design goes one step further and forbids
the escape hatch, because we can now price it.

orca's relay exposes about forty named git operations — `git.status`, `git.diff`,
`git.commit`, `git.stage`, and so on — which is the same conclusion D16 reached
independently. It also kept **one** generic operation, `git.exec`. That single exception
required `src/relay/git-exec-validator.ts`: an allowlist of permitted subcommands, a
separate list of `config` write flags (because a caller can pass `--list` **and** `--add` in
one invocation and pass a naive read-only check), and a globally denied set —
`--output`, `--exec-path`, `--work-tree`, `--git-dir`. Its own comment names the class:

> `--file` redirects config reads to an arbitrary file, enabling path traversal (e.g.
> `--file /etc/passwd --list` leaks file contents).

That is the cost of one exception: a validator that must stay ahead of every flag git ever
adds, in a component running on someone else's machine. A closed set of named operations has
no such surface, because there is nothing to validate — the operation either exists or does
not.

The rule therefore has no privileged form, no "read-only exec", and no debug build that
enables one. If a panel needs something git can do, it becomes a named operation with its own
parameters and its own bounds.

### D4 in full: why not the `-R` channel that already exists

`internal/lifecycleremote` already holds an authenticated, framed, zero-install channel to
the remote host, and reusing it is the obvious move under "find the existing answer". It is
the wrong one here, for four reasons, and the first is the whole argument.

**The `-R` channel exists because its peer is a shell we do not launch.** The lifecycle peer
is the user's interactive shell; nobody can hand it a file descriptor after the fact, so the
only route back is for its hook to dial a loopback port the server opened for us. We
_launch_ the helper. A process we launch can be handed a pipe, which is what an exec
channel's stdin and stdout are. Using a forwarded port to reach a process we started
ourselves is a longer way round with more to refuse.

The other three follow from it. A loopback socket on the remote host is reachable by **any
local user of that machine**, which is exactly why `lifecycleremote` must authenticate every
candidate connection with a per-epoch capability (`adapter.go`, "the port is not the
authenticator; the capability is"); nothing outside our SSH channel can write to our
helper's stdin, so that entire mechanism is unnecessary rather than reimplemented. A
forwarded listener depends on the server's `AllowTcpForwarding` and `PermitListen`, a refusal
class we would inherit for no gain. And ADR-0024's own "Revisit when" warns that the
forwarded-port transport becomes **unnecessary** once the relay lands and is "the right
answer today and the disposable one later" — worth heeding before building a second consumer
of it.

What we do reuse is the shape: length-prefixed frames, a codec that owns the wire contract in
one place, and a decoder that treats an unmappable frame as garbage to resync past rather
than as a fatal error.

### D9 in full: what "bounded on the machine doing the work" buys

The git design bounds status by **records**, not bytes, and the parser keeps counting past
the retention bound so `Total` is exact whenever the stream was consumed to its end
(`git.go`, `Completeness`). That contract is why the panel can say "more than N" honestly
instead of rendering a silent prefix.

A backend-side reader can honour the letter of it — stop at a byte ceiling, report `cut`,
call `Total` a lower bound. What it cannot do is produce an exact `Total` without pulling the
entire porcelain stream across the network, which in a repository with a large untracked tree
is megabytes per poll, every few seconds, to compute one integer.

The helper parses and counts where the repository is. Only the retained records and the
outcome cross the wire. The same argument applies to `log` (max+1) and to `diff` (the byte
bound is applied before the bytes are sent, not after they arrive).

### D15 in full: what "designed for" means, concretely

Three things are reserved now because they are free now and expensive later:

1. **`seq` and `ack` in the frame header.** A resumable transport needs to know what the peer
   received. AD-9 already established this shape for the session output ring; orca's relay
   header carries the same two fields for the same reason. Nothing reads them in this
   deliverable, and a decoder that ignores them is correct.
2. **The `session` service name is reserved and refused.** A helper that receives it answers
   `unknownService` today; when phase B arrives it is a registration, not a protocol change.
3. **The helper's lifetime is not defined as "one channel".** This deliverable ties them
   together — the helper exits on stdin EOF — but nothing in the protocol says a helper may
   not outlive the channel that started it. That single sentence is what lets the relay add a
   socket and a `--connect` bridge later without renegotiating the wire.

Explicitly **not** built: a daemon, a listening socket, reattachment, replay, PTY lease
semantics, or any state that survives the process. When phase B lands, the relay is expected
to be **this same binary with a `session` service**, not a second artifact — otherwise we
ship two files to every machine.

## 4. The wire

```
frame  := [type:1][seq:4][ack:4][len:4][payload:len]
payload := JSON
```

- `type` — `hello`, `request`, `response`, `notify`, `cancel`, `chunk`, `keepalive`.
- `seq`/`ack` — reserved (D15); written as the sender's counters, ignored by this
  deliverable's readers.
- `len` — bounded by a frame ceiling; a response larger than the ceiling is sent as a
  sentinel response plus `chunk` frames reassembled by concatenation (D14).

Request payload: `{id, service, op, params}`. Response: `{id, result}` or `{id, error:{code,
message}}`. `cancel`: `{id}` — refused for mutation operations (D11).

**Startup sequence.** Backend opens one exec channel running the installed helper → helper
reads one `hello` frame carrying the protocol version → on match it writes the sentinel line
to stdout and attaches the dispatcher; on mismatch it exits with the version-mismatch code
and writes nothing (D5, D6). The backend waits for the sentinel with a deadline, and treats
_anything_ else that arrives first as a refusal carrying whatever it saw, which is how a
`ForceCommand` substitution, an rc file that prints, and a missing binary all become distinct,
honest states instead of corrupt data.

## 5. Packages

| Package                  | Runs where | Purpose                                                                                               |
| ------------------------ | ---------- | ----------------------------------------------------------------------------------------------------- |
| `cmd/nocx-helper`        | remote     | The build target. Reads frames on stdin, writes on stdout, registers services                         |
| `internal/helper/proto`  | both       | Frame codec, envelope types, error codes, the version constant. One owner of the wire contract        |
| `internal/helper/host`   | remote     | Service registry, request pump, per-request goroutines and bounds (D13)                               |
| `internal/helper/client` | backend    | Launch, hello, sentinel wait, request/response, cancel, loss reporting                                |
| `internal/helper/deploy` | backend    | Platform probe, artifact selection, upload, versioned install under lock, prune, uninstall            |
| `internal/git/helper`    | backend    | `git.RepoFactory` + `git.Repo` over the client, sending only named operations                         |
| `internal/git/hostsvc`   | remote     | The `git` service: maps operations onto `internal/git/local`, applies D9 bounds, returns domain types |

The helper links `internal/git`, `internal/git/local`, `internal/git/spawn`,
`internal/loginshell` and the codec — and nothing else. `internal/git/local` is already
importable standalone (it imports only `git`, `git/spawn` and `loginshell`); `internal/git`
is not, and D18 fixes that.

## 6. States the panel can show

`remoteUnsupported` is deleted and replaced by facts. On an SSH tab, `git.open` answers one
of:

| State                                                    | Meaning                                                                        |
| -------------------------------------------------------- | ------------------------------------------------------------------------------ |
| `ok`                                                     | A repository was resolved on the remote host; the panel is fully operational   |
| `consentRequired`                                        | No footprint consent for this machine; the panel offers the consent flow       |
| `unsupportedPlatform`                                    | We build no helper for that OS/arch                                            |
| `deployFailed`                                           | Upload or install failed; carries what failed                                  |
| `execForbidden`                                          | The server refused the exec, or answered with something that is not our helper |
| `helperVersionMismatch`                                  | An incompatible helper answered; non-retryable until reinstall                 |
| `notARepository`, `noCwd`, `gitUnavailable`, `gitTooOld` | Unchanged, now answered by the remote side                                     |

Plus the existing `EnvState` `resolved`/`degraded`, now describing the **remote**
environment. A soft degrade is visible in the product, never only in a log.

**And one new mutation outcome.** D12's `indeterminate` is not an open state — it is a third
value beside `ok` and `failed` on `CommitOutcome` and on every mutation result, produced only
when the transport died between the request and its response. The panel must say the operation
**may have happened** and offer a refresh; it must never render it as failure, and the store
must never retry it. The next successful status read is the reconciliation, and there is no
other.

## 7. Security

- **The channel is private by construction.** It is our exec channel over the user's
  authenticated SSH connection; no third party can write into its stdin. This is what removes
  the capability handshake `lifecycleremote` needs (D4).
- **No argv operation** (D3). The helper cannot be asked to run anything but the fixed
  invocations its operations define.
- **The helper grants no authority the user does not already have** — it runs as them, on
  their machine, against repositories they can already write. One honest exception, stated
  rather than buried: `commit` runs that repository's hooks, which is arbitrary code. It is
  arbitrary code that was already there and that the user's own `git commit` would run; the
  panel says which environment it runs in (git D6).
- **The helper writes only inside its versioned install directory** and whatever git writes
  in the repository it was asked about.
- **Uninstall removes it**, and the footprint screen is the inventory. An artifact that
  cannot be removed is a footprint we had no right to leave.

## 8. Testing

**The one acceptance test** — the epic closes when this passes, over the real socket, against
a real SSH host:

> On an SSH tab in a remote repository, with a file whose name contains a space, a quote, a
> leading `-` and a newline: stage exactly that row through the public git WebSocket methods,
> then commit a multi-line message containing quotes and non-ASCII text, with a remote
> `pre-commit` hook that writes a marker file and emits more than one packet of output. Then
> assert: the marker exists **on the remote host**, exactly that one path was staged, the
> exact message is `HEAD`'s, and the returned status is fresh and complete.

One test, and it fails for every defect most likely to ship: the factory never wired; a path
or a message that leaked into a command string; NUL framing corrupted; stdin not closed so git
hangs; hooks running on the wrong machine; stdout and stderr deadlocking each other; a stale
poll overwriting the mutation's status.

Beside it, and not negotiable:

- **A registry test that enumerates the helper's operations** and fails if any operation
  accepts free-form arguments. D3 is only a rule if something checks it.
- **Failure paths for every external call** the client makes: upload refused, exec refused,
  sentinel timeout, sentinel replaced by other output, mismatched version, connection lost
  mid-request, connection lost mid-**mutation** (D12's `indeterminate`).
- **Bounds as intervals with both ends**: a status that is `cut`, a diff at exactly the byte
  bound, a log at `max+1`, each asserting both the retained payload and the reported outcome.
- **Contract schemas** in `contracts/` for the new `git.open` states, with the
  over-the-wire conformance test — the real result off the real socket.
- **A concurrency test** that a request wedged in a slow hook does not delay a second
  service's request (D13).

## 9. Sequence

Each step is a bead; each ends green.

1. **D18 first**: move `Caller` and the binding registry out of `internal/git` so the domain
   package stops importing `internal/session`. Nothing else can be linked until this is done.
2. `internal/helper/proto`: frames, envelope, codec, version constant, garbage resync.
3. `cmd/nocx-helper` + `internal/helper/host`: hello, sentinel, dispatcher, per-request
   goroutines, the `unknownService` refusal. No services yet.
4. `internal/helper/client`: launch over an exec channel, sentinel wait with its deadline and
   its refusal states, request/response, cancel, loss.
5. `internal/git/hostsvc` + `internal/git/helper`: the git service and its client, operation
   by operation, reads before mutations.
6. `internal/helper/deploy`: platform probe, artifact selection, versioned install under lock,
   prune, uninstall; wire into the existing footprint screen and the `relay` consent tier.
7. Composition root and transport: replace the refusal at `ws_git.go:371` with factory
   selection; new `git.open` states in `contracts/` and in the panel.
8. The acceptance test of §8, in the e2e container against a real sshd.

## 10. Deliberately out of scope

- **PTY ownership, reattachment, replay, a daemon or a socket** — reserved by D15, built by
  `nocx-if6` phase B.
- **The `files` and `ports` services.** Named, reserved, not written. Their existing SFTP and
  exec-ladder implementations stay and remain the fallback (D16).
- **Migrating any existing remote surface onto the helper.**
- **Windows remote hosts.**
- **push/pull/fetch, branch checkout, discard, hunk staging, conflicts as a surface** — out of
  scope for the local panel today (`nocx-fihs`), and remote parity means parity with what
  exists.
