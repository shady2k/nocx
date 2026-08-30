# The remote helper: one binary, several services, git first

- **Date:** 2026-08-13
- **Beads:** `nocx-6zcr` (this session), `nocx-1gsp` (the deliverable it re-answers),
  `nocx-if6` (the epic that owns the relay), `nocx-fihs` (the local Git panel, closed scope)
- **Supersedes:** D3 of [the git-manager design](2026-08-06-git-manager-design.md) (2026-08-06)
  — the deferral of the remote case to the relay, not its rejection of `DiscoveryConn.Exec`,
  which stands. That document is amended in the same commit; this line is not the amendment.
- **Status:** design, approved 2026-08-13; stress-tested (see the results section)

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

| #   | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Rejected alternative, and why                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| D0  | **`DiscoveryConn.Exec` remains rejected for git.** It buffers into a 64 KiB `cappedBuffer` (`ssh_discovery.go:86`) and has no stdin at all, so D8's `commit -F -` and `--pathspec-from-file=-` are unreachable through it                                                                                                                                                                                                                                                                                                                                                                                                                               | Raising the cap and adding stdin. That makes the discovery lane a general-purpose remote runner — the process-shaped seam D16 rejected, wearing the discovery package's name                                                                                                                                                                                                                                                                                                           |
| D1  | **One binary, several services, one process per pooled connection.** Not a binary per feature                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | A `nocx-githelper`. Three surfaces already point at one binary — `discovery/provider.go:14` (ports), `architecture.md:203` (file-tree), and now git. A second helper would be the fourth answer to a question that already has one reserved                                                                                                                                                                                                                                            |
| D2  | **The envelope names a service and an operation**: `{id, service, op, params}`. `git` today; `files`, `ports` and `session` are reserved names with no implementation                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | An operation namespace flattened into method strings (`git.status`). It works — orca does it — but a service field makes per-service concurrency, per-service bounds and per-service capability reporting structural rather than a naming convention                                                                                                                                                                                                                                   |
| D3  | **No operation may accept argv, ever. There is no escape hatch and there will not be one**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | A generic `exec`, even "temporarily" or "read-only". Measured: orca kept exactly one, and it cost a 300-line allowlist validator — see D3 in full                                                                                                                                                                                                                                                                                                                                      |
| D4  | **The transport is one SSH exec channel we launch, framed over its stdin/stdout**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | Reusing the `-R` loopback channel of `internal/lifecycleremote`. It exists because the peer there is a shell we do **not** launch; we launch the helper, so we can hand it a pipe — see D4 in full                                                                                                                                                                                                                                                                                     |
| D5  | **The sentinel separates "our helper started" from "something started", and that is all it claims.** A sentinel line on stdout, written only after the `hello` frame is accepted; the `hello-ok` frame echoes a nonce we minted and reports the helper's own content hash. A compromised remote host is **outside the threat model**, stated in §7                                                                                                                                                                                                                                                                                                      | Claiming the sentinel authenticates the peer. It does not and cannot: whoever owns that machine can run a modified helper that says anything, and the user already trusts it with their repositories and keys. What the sentinel does defend is the ordinary case — a `ForceCommand` wrapper, an rc file that prints, a missing binary, a server that accepted the request and did something else                                                                                      |
| D6  | **A version or content-hash mismatch triggers exactly one automatic reinstall**; only a mismatch after that reinstall is the terminal `helperVersionMismatch`. The distinct exit code stays, and non-retryable applies to the reconnect loop, not to installing the right thing once                                                                                                                                                                                                                                                                                                                                                                    | Treating every mismatch as terminal. With the artifact bundled (D20) and directories keyed per version (D7), the reachable case is not "an incompatible peer" but "the file at our path is not our binary" — an interrupted SFTP upload, which is ordinary. Terminal-on-first-sight leaves the panel dead on that host until the user finds an uninstall button                                                                                                                        |
| D7  | **Install is an immutable directory keyed by `version + goos-goarch + content-hash`**, written to a temporary name and renamed atomically, complete only when it carries an `.install-complete` marker; a directory without the marker is removed, never used                                                                                                                                                                                                                                                                                                                                                                                           | Keying on version and hash alone. One `$HOME` shared across machines of different architectures — NFS, or the same account on an arm64 and an amd64 box — would resolve to one directory name holding the wrong binary. And overwriting one path lets a running helper serve new clients off replaced code. This is orca's `relay-0.1.0+8de1d39fd7c1` layout, plus the platform its own deploy path carries separately                                                                 |
| D8  | **Consent is asked when the user reaches for the feature, not when a connection is made.** `git.open` answers `consentRequired`; the panel offers it; accepting raises that machine to the `relay` tier. `auto` resolves to `relay` only when a surface on that connection has asked for the helper — not merely because a binary exists for the platform                                                                                                                                                                                                                                                                                               | Letting the existing `auto` ladder decide. Its first arm is "a suitable binary exists for that platform", written as forward structure before one did (`2026-08-10` design §3.1); the day we ship a helper that arm becomes true everywhere, and every user is asked, on every new machine, about a feature they never reached for. A machine at an explicit `script` is **not** silently upgraded either — `script` is an answer, not a gap                                           |
| D9  | **The helper applies the bounds and reports the domain outcome** (`complete`/`capped`/`cut`), and only the retained records cross the wire                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | Bounding in the backend's reader. It is the reason this design exists rather than an SSH-exec-per-operation one: counting `Total` exactly requires consuming the whole porcelain stream, and a local reader can only do that by dragging it across the network                                                                                                                                                                                                                         |
| D10 | **Cancellation is an operation on the protocol, not a channel close.** The helper cancels with the process-group escalation `internal/git/local` already has                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | Closing the channel to stop the work. `local.go:132` escalates INT → TERM → KILL **against the group** precisely because `git diff` spawns a textconv filter that holds the pipe open. An SSH channel close makes no such promise about descendants                                                                                                                                                                                                                                    |
| D11 | **Mutations are never cancelled** (git D18), and a `cancel` naming a mutation is **refused**, not ignored                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Silently dropping it. A refusal is a fact the caller can act on; a no-op looks like success                                                                                                                                                                                                                                                                                                                                                                                            |
| D12 | **Transport loss during a mutation is an `indeterminate` outcome, and the panel says so.** It is never auto-retried                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | Retrying, or reporting failure. The commit may already have happened, hooks and all. Reconciliation is the next status read, not a guess                                                                                                                                                                                                                                                                                                                                               |
| D13 | **One goroutine per request, responses multiplexed by id, with per-service and per-request bounds**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | A single request pump. One `git commit` sitting in a slow pre-commit hook would stall port sampling and the file tree — the whole reason for putting several services in one process is that they must not be able to do that to each other                                                                                                                                                                                                                                            |
| D14 | **Frames are bounded, and a response above the frame bound is chunked**                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | One frame per response. orca measured a large git response head-of-line-blocking interactive echo and answered with a 256 KiB threshold and 128 KiB chunks. Our case is milder — the helper has its own SSH channel with its own window, and D9 already bounds diffs — but "milder" is not "bounded"                                                                                                                                                                                   |
| D15 | **PTY ownership is designed for and not built.** Reserved now: `seq`/`ack` in the frame header, an **instance id** in `hello-ok`, the `session` service name, and the rule that a helper's lifetime is not tied to one channel. Not built: daemon, socket, reattach                                                                                                                                                                                                                                                                                                                                                                                     | Building it now, or ignoring it. Ignoring it is what forces the wire break later; building it is `nocx-if6` phase B wearing a git panel's clothes. The instance id is the field the first draft missed: without it a later reattach cannot tell the helper it was talking to from a fresh one, and it costs one string today                                                                                                                                                           |
| D16 | **The zero-install paths stay and remain the fallback.** SFTP for files, the exec ladder for ports, the shell hook for lifecycle                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | Migrating them onto the helper. A host that forbids execution, has no writable home, or runs an architecture we do not build for must keep working. Selection happens at the composition root by capability — one interface, three implementations, never two owners                                                                                                                                                                                                                   |
| D17 | **A helper service may only be the remote half of an interface that already exists locally.** No interface, no service                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | Letting the helper grow capabilities of its own. That is how an operation host becomes a platform, and then a shell                                                                                                                                                                                                                                                                                                                                                                    |
| D18 | **`internal/git` must stop importing `internal/session`.** The `Caller` interface and the binding registry move to their own package                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | Linking it as-is. `internal/session` imports `pty`, `ssh` and `storage`, so the domain types would drag the whole desktop tree into a binary that needs none of it — measured, §7                                                                                                                                                                                                                                                                                                      |
| D19 | **The helper's channel never requests a PTY.** It is an `exec` channel with pipes, and the client must assert that                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | Letting it inherit a pty because "that is how sessions are opened here". A pty applies line discipline: `\n` becomes `\r\n` on the way out and input is echoed back. Every binary frame would be corrupted, and it would present as protocol desync rather than as a terminal setting. Found by the stress test in `cmd/e2e-sshd`, which allocates a pty for **every** command including `exec` (`main.go:544`) — so the fixture must be extended before the acceptance test can exist |
| D20 | **The artifact ships inside the app, compressed, for four targets** — `linux/{amd64,arm64}`, `darwin/{amd64,arm64}` — and is decompressed locally before upload. Nothing is downloaded at runtime. **Amended 2026-08-30** (`.internal/specs/2026-08-30-a-stable-code-identity-and-what-the-release-publishes-design.md`): the matrix is 2x2. The app is built universal and an Intel Mac is a supported host, so excluding `darwin/amd64` meant the git panel refused the platform the bundle was carrying a slice for. The reasoning of this decision — nothing downloaded at runtime, no second delivery channel, decompressed locally — is untouched | Fetching the helper on demand. It buys a second delivery channel with its own signature chain and its own supply-chain surface, to save a few megabytes in a bundle that already verifies itself (ADR-0007). Shipping it with the client that will speak to it also makes version skew structurally impossible. Decompressing locally rather than remotely avoids depending on a `gzip` that may not be there                                                                          |
| D21 | **Integrity is proven by what ran, not by what was written**: the helper reports its own content hash in `hello-ok` and the client compares it with the directory it installed                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Hashing the file on the remote host after upload. It needs `sha256sum` or `shasum` to exist there, and it verifies a file rather than the process — missing exactly the case D7 cares about, a corrupt upload sitting in a correctly named directory                                                                                                                                                                                                                                   |
| D22 | **stdout is the wire; stderr is diagnostics.** The helper writes nothing but frames to stdout, and the client surfaces stderr in the refusal states of §6                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Letting the helper log to stdout. One stray `fmt.Println` becomes garbage in the frame stream; the codec survives it by resyncing, but reports it as a gap — so a debug print would be investigated as a network fault                                                                                                                                                                                                                                                                 |
| D23 | **Polling is coalesced by repository identity, not by tab**: one status read in flight per repository, and the interval backs off when an operation's duration approaches it                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | One poll per tab. Three tabs on one host in one repository is three `git status` processes on someone else's machine every few seconds, and the panel already gates on visibility (git D13). `--no-optional-locks` keeps the index untouched but does not make the process free                                                                                                                                                                                                        |
| D24 | **The remote environment is resolved once at helper start** — bounded by a deadline and an output cap — cached for the process lifetime, and reported as `resolved`/`degraded` exactly as the local path does                                                                                                                                                                                                                                                                                                                                                                                                                                           | Resolving per operation (a shell spawn per poll), or not resolving at all (an `exec` channel is non-interactive and non-login, so `git` may not be on `PATH`). `internal/loginshell` has **no internal imports**, so it links into the helper unchanged — one implementation, two machines. What it still cannot contain is a `direnv` or virtualenv created inside a tab; that limit is git D6's, unchanged and restated                                                              |
| D25 | **Uninstall closes the channel before removing anything**, and pruning never touches the version currently in use                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | Removing the directory under a running helper. With no daemon we need no remote liveness probe — the backend knows which version its own channel is running — but two nocx instances sharing one `$HOME` can each hold a different version, so pruning removes only versions **older** than the one being installed. A stale directory left behind is harmless and visible in the footprint inventory                                                                                  |
| D26 | **Every request carries a correlation id that travels frontend → backend → helper**, and the helper's `slog` output on stderr carries it                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Logging on both sides without one. `nocx-if6` states the rule for exactly this moment: "the moment there are two hops a log line without correlation is useless." The request id already exists in the envelope; this makes it the same value the backend logs                                                                                                                                                                                                                         |

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

Request payload: `{id, service, op, params, corr}` — `corr` is the correlation id of D26.
Response: `{id, result}` or `{id, error:{code, message}}`. `cancel`: `{id}` — refused for
mutation operations (D11).

**Startup sequence.** The backend opens **one `exec` channel with no `pty-req`** (D19)
running the installed helper, and sends one `hello` frame carrying the protocol version and a
freshly minted nonce → on a version match the helper writes the sentinel line to stdout, then
a `hello-ok` frame carrying `{nonce, contentHash, instanceId}`, then attaches the dispatcher;
on mismatch it exits with the version-mismatch code and writes nothing (D5, D6).

The backend waits for the sentinel with a deadline and treats _anything_ else arriving first
as a refusal carrying whatever it saw — which is how a `ForceCommand` substitution, an rc
file that prints and a missing binary become three distinct honest states instead of corrupt
data. It then requires `hello-ok` with the nonce it sent and the content hash it installed
(D21); a mismatch on either is one automatic reinstall and, if it recurs,
`helperVersionMismatch` (D6). `instanceId` is recorded and unused (D15).

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

**What is deliberately not defended, and why.** A **compromised remote host** is outside the
threat model. Whoever controls that machine can replace the helper with something that reports
any nonce, any hash and any status we ask for; no handshake we design can detect it from this
side. That is not a gap this design leaves open — it is the situation the user is already in
the moment they hand that machine their repositories, their shell and their agent-forwarded
keys. The sentinel, the nonce and the content hash all defend the _ordinary_ failures (D5,
D21), and the spec says so rather than letting a later reader mistake them for authentication.

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
- **A no-pty assertion** (D19): the client's channel is opened without `pty-req`, and a frame
  round-trip carrying `0x0A` and `0x0D` bytes arrives byte-identical. This is the test that
  would have caught the fixture defect the stress test found by reading.

**The fixture needs work before any of that runs.** `cmd/e2e-sshd` allocates a pty for every
command it is asked to run, `exec` included (`main.go:544` — stdin, stdout and stderr all
point at the pty slave). Real `sshd` gives an `exec` channel pipes unless `pty-req` preceded
it, so the fixture is unfaithful in exactly the dimension this design depends on, and a frame
stream over it would be corrupted by line discipline rather than by our code. Extending it to
serve `exec` over pipes is step 0 of the sequence, not a detail of step 8.

## 9. Sequence

Each step is a bead; each ends green.

0. **Two preparatory changes, in either order.** Amend D3 and D16 **in
   `.internal/specs/2026-08-06-git-manager-design.md` itself** — the rejection of
   `DiscoveryConn.Exec` stands, the deferral to the relay does not — so the two documents
   never disagree; and extend `cmd/e2e-sshd` to serve `exec` channels over pipes when no
   `pty-req` preceded them.
1. **D18 next**: move `Caller` and the binding registry out of `internal/git` so the domain
   package stops importing `internal/session`. Nothing else can be linked until this is done.
2. `internal/helper/proto`: frames, envelope, codec, version constant, garbage resync.
3. `cmd/nocx-helper` + `internal/helper/host`: hello, nonce, sentinel, `hello-ok`, dispatcher,
   per-request goroutines, the `unknownService` refusal. No services yet.
4. `internal/helper/client`: launch over a pty-less exec channel, sentinel wait with its
   deadline and its refusal states, `hello-ok` verification, request/response, cancel, loss.
5. `internal/git/hostsvc` + `internal/git/helper`: the git service and its client, operation
   by operation, reads before mutations.
6. `internal/helper/deploy`: the three-target build and its bundling, platform probe, artifact
   selection, versioned install under lock with its `.install-complete` marker, prune,
   uninstall; wire into the existing footprint screen, and add the second condition to `auto`'s
   `relay` arm (D8) so shipping the binary does not opt every machine in.
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

## Stress Test Results: the remote helper

Twelve branches (ten mapped, two added by the reflexion pass). Bead `nocx-4v5c`.

### Resolved decisions

- **The amendment procedure.** D3 and D16 are amended **in the git-manager spec itself**, in
  the same commit, and `OpenRemoteUnsupported` is deleted rather than redefined. A "see the
  other document" note is not an amendment; it is a second truth. No ADR: this changes a
  decision inside one feature, and an ADR would be a third place saying the same thing.
- **Artifacts and release cost** → D20, D21. Four targets, bundled compressed, decompressed
  locally, nothing downloaded at runtime, integrity proven by the helper's self-reported hash.
- **What the sentinel proves** → D5, §7. Re-scoped from "authenticates the peer" to "separates
  our helper from something else", with a nonce and an explicit statement that a compromised
  host is outside the threat model. Overclaiming here would have been the defect.
- **Version mismatch** → D6, D7. One automatic reinstall before any terminal state; atomic
  rename and an `.install-complete` marker, because an interrupted upload is ordinary.
- **Consent timing** → D8. The existing `auto` ladder resolves to `relay` as soon as "a
  suitable binary exists for that platform" — forward structure written before one did. Left
  alone, shipping the helper opts every machine in at connect time. The arm gains a second
  condition, and the ask moves to the moment the user opens the panel.
- **Poll cost** → D23. Coalesced by repository identity rather than by tab, with backoff.
- **Remote environment** → D24. `internal/loginshell` has no internal imports, so it links
  into the helper unchanged; resolution happens once at start, bounded, and is reported.
- **Uninstall and pruning** → D25. No daemon means no remote liveness probe is needed; pruning
  removes only versions older than the one being installed.
- **Enough reserved for phase B?** → D15. No: an **instance id** was missing, and without it a
  later reattach cannot recognise its own peer. One field, added now.
- **Does the acceptance test's infrastructure exist?** Partly, and this was the sharpest find.
  `cmd/e2e-sshd` is a real SSH server running real commands on a real PTY — but it allocates a
  pty for `exec` channels too (`main.go:544`), which real `sshd` does not. A framed binary
  protocol over a pty is corrupted by line discipline (`\n` → `\r\n`, plus echo). Two
  consequences: D19 (the helper's channel never requests a pty, asserted by a test) and step 0
  of the sequence (extend the fixture before anything depends on it).

### Added by the reflexion pass

- **A shared `$HOME` across architectures** → D7. The install directory was keyed by version
  and content hash with no platform, so one account on an arm64 and an amd64 machine — NFS, or
  the same login on both — resolves to one directory name holding the wrong binary. The key
  now carries `goos-goarch`.
- **Observability across two hops** → D26. The design said nothing about logging, while the
  epic that owns the relay states the rule for exactly this moment: "the moment there are two
  hops a log line without correlation is useless." A correlation id rides the envelope.

### Changes made

Eight decisions rewritten or added (D5, D6, D7, D8, D15, and new D19–D26), §4's startup
sequence, §7's threat model, §8's test list plus the fixture gap, and a step 0 in §9.

### Deferred / parking lot

- **Bulk-lane chunking thresholds.** D14 says responses are chunked; the numbers are not
  chosen. orca uses 256 KiB threshold / 128 KiB chunks over a channel shared with a pty; ours
  is a dedicated channel with D9 bounds in front of it, so the numbers should be picked
  against a measurement rather than copied.
- **`darwin/amd64`.** Answers `unsupportedPlatform` until someone asks for it.
- **Whether the relay later merges into this binary.** Stated as intent (D15), not designed.

### Confidence

**Medium-high.** The seam, the transport choice and the operation set are well grounded — two
independent designs (ours and orca's) converged on named operations, and the one place orca
deviated is the one place it paid. Residual concern sits in **deployment**, not in the
protocol: three build targets, a two-stage release build, install locking, pruning and a
consent tier are the majority of the surface area and the least exercised by anything already
in this repository. If this slips, that is where it will.
