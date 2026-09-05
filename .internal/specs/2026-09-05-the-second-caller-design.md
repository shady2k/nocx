# DRAFT — NOT APPROVED

# The second caller: how an external agent reaches the wave

**Date:** 2026-09-05

**Status:** DRAFT — NOT APPROVED. This is a mechanism design, not an implementation.
It records the decisions needed before a worker edits the socket, dispatcher, or helper
ABI. The pin and grant that an external coordinator will eventually hold are deliberately
left as interfaces and open questions below.

## 1. In one sentence

An enrolled external agent reaches the backend-owned wave through a private, listening
Unix socket; `nocx-server` owns the local endpoint, a remote helper carries the same
request to that endpoint without interpreting it, and both the built-in assistant and the
external caller enter one declaration-backed dispatcher whose only difference is the
source of the grant.

What a user can do that the current tree cannot do is type an agent into a nocx pane and
have that agent start, inspect, message, wait for, and close its own worker through the
same five wave calls that the in-process assistant already has. The end-to-end proof must
watch the typed external process call `wave.spawn`, then call `wave.holdings`, `wave.say`,
`wave.wait`, and `wave.close` against the real wave record, without herdr being installed
or running.

## 2. What this crosses, and what is already decided

This design crosses the backend's control-plane transport, the assistant's declaration
and capability pipeline, the in-memory wave record, and the helper's remote carrier. The
following decisions are binding and are not re-decided here.

**AD-1** in `docs/architecture.md` splits nocx communication into a raw binary data
plane and a JSON-RPC 2.0 control plane. PTY bytes never become JSON, and a new control
surface needs its own decision. This document therefore adds a control-plane endpoint,
not a PTY-byte side channel. **AD-6** keeps the backend blind to terminal stream meaning;
the wave endpoint receives explicit RPC calls and never parses terminal output.
**AD-7** keeps session identity server-authoritative. A caller does not choose a session
by putting an arbitrary session id in a wave method; the authenticated caller context is
bound to the session before dispatch. **AD-8** requires interfaces at module boundaries
and manual wiring at one composition root. The endpoint, authorizer, dispatcher, and
helper carrier are separate seams, composed by `cmd/nocx-server` rather than selected by
mode strings or concrete-to-concrete calls.

The 2026-08-15 workspaces, lineage, and orchestration design §6 already decided **one
dispatcher and two callers**. The in-process assistant receives a grant minted for its
run. An enrolled external CLI reaches the same dispatcher over a private local channel.
They differ only in how the grant was sourced. D13 and D14 of that document make external
authority an enrolled process tree, authenticated by process identity and a non-reusable
(pid, start-time) root, with one human approval and no bearer material in the environment.
This document does not invent a token or weaken that requirement.

ADR-0028 makes the grant a set of effects and resources, never a list of permitted tool
names. The dispatcher narrows a capability from the grant; it does not check a name and
then hand a broad service to the caller. The open question in the 2026-08-24 orchestration
mechanism design §10 is exactly the failure this document must avoid: describing one
shared surface while leaving an ambient dispatcher API. The external path therefore has
an authorizer and a narrow invocation context, not a public `WaveRecord` or a public
`*agenttools.WaveCoordinator` constructor that accepts caller-supplied ids.

ADR-0024 decision 2 makes the authenticated channel the channel that is not the tty.
A wave request may never be inferred from OSC, a terminal title, or any other byte-stream
observation. Its local socket is an authenticated control channel. The existing
lifecycle channel is a `socketpair`, which cannot provide kernel peer credentials; it is
therefore not this channel.

The 2026-09-03 waves authority model §4 A9/A10/A12 and §5 says that five wave calls name
the holder's own resources and do not accept a resource argument; `say` and `interrupt`
use handles minted by the object rather than raw participant ids. Most importantly, A12
says that until participant authenticity has a mechanism, a wave call carries no
authority the session does not already have. The external endpoint must not pretend that
same-UID access is the missing participant pin.

The 2026-08-24 orchestration mechanism design D1, D3, D4, D7, D8, D14 and D15 are also
carried forward. Supervision belongs to the backend, not to a coordinator turn. A
restarted coordinator asks what its session holds. Failure is closed. Mail and wave
facts are delivered by explicit calls and cursor acknowledgements. The backend may wake
a coordinator by typing only where the separately designed driver says the pane is
`free_text`; that wake is not the external RPC carrier.

Finally, `internal/wave.MemoryStore` is explicitly an in-memory record for the lifetime
of the backend. The amendment in the spawn-and-register design §2 removed the proposed
startup terminalizer: under stage-1 D5 the wave worker dies with the backend, so a
restart starts with an empty record. The external socket cannot change this into a
durable wave or promise that a remote helper-held process remains addressable after the
backend that owned its wave has gone away.

## 3. Decisions

| #       | Decision                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 | Rejected alternative, and why                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| ------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **D1**  | **Use a dedicated listening Unix socket for external wave RPC.** The endpoint is a sibling of the existing coordinator discovery socket under `coordinator.RuntimeDir(paths)`, named `wave.sock` for the local daemon. It is a `LISTEN` socket because peer credentials are stamped by `connect(2)`.                                                                                                                                                                                                                                                                                                                                                                                                                                     | Reusing the lifecycle `socketpair` is impossible for this trust decision: a socketpair has no accepted peer whose credentials can be queried. Reusing the discovery socket would mix newline-delimited hello JSON with JSON-RPC and would turn one socket into two protocols with ambiguous framing. A loopback TCP port is rejected because any account on the host can reach it and the port does not supply the Unix-account boundary.                                                                                                                                    |
| **D2**  | **Follow the existing runtime-directory security answer.** `wave.sock` lives in the build-specific data directory's `run` directory, which is created as mode `0700`, checked for ownership without following a final symlink, and refused if owned by another uid. The socket itself is mode `0600`, is bound through a temporary same-directory name and atomically published, and refuses a symlink or non-socket occupant. The existing daemon lock covers the directory so discovery and wave listeners cannot be owned by competing `nocx-server` processes.                                                                                                                                                                       | A socket in `/tmp`, `XDG_RUNTIME_DIR`, or an agent-provided directory creates a second path authority and can be redirected or split between the desktop and headless builds. `coordinator.Server` already has the needed ownership, path-length, stale-socket, lock, and atomic-publication reasoning, but its discovery request protocol must not be copied as the wave protocol. Extract or extend its filesystem/listener primitives rather than writing a second security vocabulary.                                                                                   |
| **D3**  | **`nocx-server` serves the local endpoint.** The composition root starts the wave listener only after the application and its `MemoryStore`/wave record exist, and closes the listener before tearing down the backend. The endpoint package owns accepting connections and framing; the assistant package owns dispatch; the wave package owns the record.                                                                                                                                                                                                                                                                                                                                                                              | The Wails window must not serve it: the external agent may outlive or never have a window, and AD-3 keeps the shell thin. The generic `internal/coordinator.Server` must not own wave semantics: it currently serves discovery and knows only `Backend` address/token facts. The helper must not serve local wave authority: it owns host sessions and the helper ABI, not policy, grants, or the wave record.                                                                                                                                                               |
| **D4**  | **A remote helper is a transport proxy, not a second wave server.** On a remote host, the agent connects to a generation-qualified Unix socket in the helper's existing private endpoint directory. The helper accepts the agent's JSON-RPC byte stream and carries opaque, bounded request/response envelopes over an explicitly allocated helper bridge stream to the backend. The backend performs peer/enrollment authorization and invokes the same dispatcher as locally.                                                                                                                                                                                                                                                          | Adding wave state and wave executors to `internal/helper/host` would make the helper a second authority and would duplicate the five calls. Calling an operation named `session` is especially wrong: `internal/helper/proto/session_service.go` reserves that service name for host-session lifecycle, and its semantics concern PTYs, windows, exits, and attachment, not coordinator mail or wave membership. A new helper proxy transport may be named and registered as a bridge facility, but it must not be a `wave` implementation that narrows or stores authority. |
| **D5**  | **The agent-facing wire is JSON-RPC 2.0 on both local and remote sockets.** The stream uses bounded newline-delimited JSON-RPC messages, one complete request or response per line, with a write mutex and one response carrying the request id. Requests have a maximum envelope size derived from the declared parameter and result bounds. Notifications are refused for wave methods because a mutation with no response cannot be distinguished from a lost response.                                                                                                                                                                                                                                                               | The existing coordinator discovery socket uses newline-delimited JSON but is not JSON-RPC; copying its `RequestHello`/`Response` shapes would lose standard error codes and method identity. The helper's binary protocol is also not the agent-facing protocol; it remains an opaque authenticated carrier. A custom five-command text protocol is rejected because it would create a second declaration and validation vocabulary.                                                                                                                                         |
| **D6**  | **One caller-agnostic wave dispatcher.** Introduce a narrow assistant seam, implemented in the assistant package and wired by the composition root, which accepts a validated external or in-process invocation containing an already authenticated session context and its grant. It looks up the method in the assembled `agenttools.Registry`, validates the method's parameters using the declaration's schema, resolves resources from the invocation's `RunContext`, requires the grant projection to contain the executable declaration, narrows with the declaration's `Narrow`, and invokes the existing executor. The current `executeWave*` functions become the common executor layer rather than an in-process-only branch. | Exporting five new endpoint handlers that call `wave.MemoryStore` directly would duplicate `executeWave*`, bypass `WaveCoordinator`, and allow the two callers to disagree about sender, session, membership, or result shape. A dispatcher taking `map[string]any` plus a tool-name allowlist is the ambient API ADR-0028 rejects. A type switch on caller kind inside each executor is also rejected; caller variation belongs in the invocation/authorization seam.                                                                                                       |
| **D7**  | **Use an invocation seam, not caller-supplied session authority.** The separate authorizer interface receives the accepted connection's kernel identity assertion and returns either a refusal or an invocation containing the bound session, grant, and run context needed to narrow `WaveCoordinator`. The endpoint never reads a `sessionId` from `wave.holdings`, `wave.wait`, or any other wave params. The in-process path constructs the same invocation from its existing run grant and `RunContext`.                                                                                                                                                                                                                            | Letting the external request carry `sessionId` and then checking it against the uid confuses authentication with authority: any same-UID process could name another session. Letting the endpoint construct `agenttools.WaveCoordinator` from a raw session string bypasses the grant's resource scope. Using the process environment for a bearer token contradicts D13/D14 and the shell-integration design.                                                                                                                                                               |
| **D8**  | **Fail closed while the enrollment/pin authorizer is absent.** `nocx-server` does not publish an admitting wave endpoint unless a non-nil authorizer is composed. The wrapper reports the structured refusal "external orchestration unavailable: enrollment is not wired" and does not `exec` the agent as orchestrated. If a disabled endpoint is retained for diagnostics, every request returns a named domain refusal before parameter validation or dispatch and performs no wave mutation; it never silently treats the uid as an enrolled principal.                                                                                                                                                                             | Starting an unauthenticated endpoint and relying on a future check is an authority stub, not a feature. Treating `SO_PEERCRED`'s same uid as the complete identity would claim D13's process-tree and human approval without its pin. Falling back to ordinary execution after an enrollment failure is the D4 fail-open path the orchestration design explicitly rejected.                                                                                                                                                                                                  |
| **D9**  | **Reuse the five existing wave declaration rows and contracts.** The `agenttools` declaration table remains the only place each tool comes into existence, including effect, resource kinds, resolver, deadline, execution site, narrowing function, params contract, and result contract. The external method registry references the same five per-call documents in `contracts/tools/wave.*.schema.json`; it does not hand-write parallel params or results.                                                                                                                                                                                                                                                                          | A second external-only table would drift on the first changed bound, result key, or effect. A generic JSON decoder followed by executor-local checks recreates the missing-validator defect already prevented by `internal/transport`'s required `paramsValidator`. A schema only for the response is insufficient: the external request must be refused before executor entry when its params are malformed.                                                                                                                                                                |
| **D10** | **One logical coordinator owns one session's wave reader.** The endpoint authorizer acquires a per-session controller slot before admitting an external connection, and the common dispatcher serializes calls for that slot. A second live caller for the same session receives a structured `session already has a wave caller` refusal and cannot fetch, acknowledge, spawn, say, wait, or close. When the first connection is observed closed, its slot is released; a replacement can ask holdings using the session's existing record. The built-in in-process caller uses the same slot while it is active.                                                                                                                       | Allowing two independent readers to share the session-shaped `ReaderID` would make one caller consume the other's mailbox and would race acknowledgement with effects. Copying the wave record per connection would split membership and violate one backend-owned record. Silently letting the second caller replace the first would make a dropped response or stale process able to take over. Shared multi-client coordination can be added only after cursor ownership, replay/idempotency, and revocation are separately decided.                                      |
| **D11** | **A request is not an idempotency guarantee.** The endpoint correlates a response with the JSON-RPC request id only for the lifetime of its connection. If the connection dies after an effect starts and before its response arrives, the caller must not blindly retry a mutating call; it asks `wave.holdings` after re-enrollment and treats the outcome as unknown until the record says otherwise. A future idempotency key, if required, belongs in the wave API design and its contracts.                                                                                                                                                                                                                                        | Treating JSON-RPC ids as globally idempotent is false: ids are caller-chosen and commonly reused after reconnect. Retrying `wave.spawn` automatically could fork a second worker; retrying `wave.say` could duplicate mail; retrying `wave.close` could obscure whether the first close reached the process. Inventing a local response cache without defining its lifetime and relation to the in-memory record would make a partial authority claim.                                                                                                                       |
| **D12** | **Remote helper transport carries identity as an authenticated assertion, not as a bearer.** The helper's Unix listener obtains the remote process identity from its kernel-stamped peer credentials. The helper bridge authenticates the helper generation using its existing handshake, then transports the bounded RPC request and the identity assertion to the backend authorizer. The separate pin task decides how the remote `(pid, start-time)` root is resolved and pinned; this document only requires an interface that can accept local and helper-provided identity assertions.                                                                                                                                            | Having the agent send its own pid, uid, or start time in ordinary JSON is self-attestation and cannot satisfy D13. Putting a capability in `NOCX_*` environment variables would make every descendant carrying the environment a bearer and would contradict the no-bearer rule. Making the backend trust an arbitrary helper payload without authenticating the helper connection would move the hole rather than close it.                                                                                                                                                 |

## 4. The channel and its lifecycle

### 4.1 Local endpoint

The local path is `coordinator.RuntimeDir(paths)/wave.sock`, where `paths` is the same
build-specific `storage.Paths` passed to the existing coordinator discovery server.
Thus `nocx-dev` and `nocx` cannot accidentally share a wave endpoint, and a test can
supply an isolated data directory without changing production path rules. The endpoint
is not placed beside the shell integration scripts, in a temporary directory, or in a
path sent by the caller.

`nocx-server` already holds the one daemon lock for the runtime directory. The wave
listener follows the existing `coordinator.Server` start order: ensure the directory,
force `0700`, inspect ownership through `Lstat`-style semantics, take the daemon lock
before inspecting the socket path, reject a final symlink or a non-socket occupant,
bind a pid-qualified temporary name, set the socket to `0600`, and atomically rename it
to `wave.sock`. The exact shared helper should be factored from the existing
`coordinator.Server`; duplicating `prepareDir`, `checkSocketPath`, and atomic bind logic
would create two answers to the same path-security question.

The wave endpoint must not reuse the discovery server's `srv.sock` handler. The existing
server authenticates a peer uid and then reads newline-delimited discovery requests with
an exchange timeout of thirty seconds. A wave `wait` is declared with an eleven-minute
ceiling, and a connection can have several requests in flight. The endpoint therefore
needs its own bounded JSON-RPC stream loop, per-request contexts, and response writer,
while retaining the shared directory and peer-credential primitives.

A peer credential is read immediately after accept and before parsing a request. A peer
whose credentials cannot be read is refused. A foreign uid is refused. Neither fact
alone establishes the external enrollment: uid is the input to the authorizer, not its
answer. The endpoint does not log request payloads, task text, mail bodies, or arbitrary
params; worker-written text is untrusted content and can contain secrets.

### 4.2 Stream framing and request execution

The Unix socket is a byte stream, so the implementation needs framing in addition to the
JSON-RPC envelope. The selected framing is one UTF-8 JSON object per newline, with a
maximum line length checked before allocation or decode. This follows the existing short
runtime-socket convention without pretending that discovery's response shape is the
control API. A line without a complete newline before the bound is a parse/size refusal;
its contents are not logged.

Every request must carry `jsonrpc: "2.0"`, a non-null id, a method, and params that match
the method's schema. The server returns standard JSON-RPC errors for parse, invalid
request, method-not-found, invalid params, and internal failure. Enrollment, session
ownership, unavailable wave record, stale participant, and helper loss are named nocx
domain errors in `error.data.reason`, with a stable error code and no raw secret-bearing
error string. A method is not considered implemented until its params and result are
contracted and its refusal shape is accepted by the contract checker.

The read loop parses and bounds each request, then dispatches it on a request context.
The write side is mutex-protected so two completed requests cannot interleave JSON lines.
A connection close cancels all in-flight calls. A request deadline comes from the
existing declaration row, not from a borrowed discovery timeout: a `wave.wait` may hold
its turn for the declaration's bounded wait, while a socket that is idle with no request
may still be closed under a separate connection policy. A wait is an active request and
must not be killed by an idle-connection timer.

The per-session controller slot sits below the connection and above the common
executor. It serializes all wave calls for the session, including an acknowledgement
followed by a holdings fetch. This preserves the existing `waveAnswer` ordering, where
the acknowledgement names the previous answer and the next fetch advances the same
session reader. It also makes the refusal for a second caller deterministic rather than
depending on which goroutine happens to acquire `MemoryStore.mu` first.

### 4.3 Remote endpoint

The remote agent and the helper are on the remote host; the local `nocx-server` is not.
The remote agent therefore cannot open the local `wave.sock` directly. The helper is the
only component already present on that host with a private Unix endpoint and a
full-duplex authenticated carrier back to the backend.

The helper creates a generation-qualified sibling socket under the same private
`internal/helper/endpoint.Dir(home)` directory and applies the existing `0700` directory
and `0600` socket rules. The name is a transport name, not a credential, and is
qualified sufficiently to avoid crossing helper generations within the socket path
bound. The launcher-versus-wrapper decision may choose how the path reaches the agent,
but neither choice changes the server or authority: the path can be staged as launch
configuration or supplied by the nocx wrapper, and no capability is put in the
environment.

A helper connection that owns a remote wave endpoint gets a transport-only proxy stream.
The proxy takes one complete agent JSON-RPC envelope, bounds it, and sends it over a new
helper frame type or equivalent multiplexed bridge record. The backend sends the exact
JSON-RPC response back over that stream. The helper does not decode wave method params,
look up `WaveCoordinator`, touch `internal/wave`, mint a grant, or decide whether a
worker belongs to a coordinator. It only validates enough framing to prevent a corrupt
helper frame from becoming an unbounded allocation and preserves request correlation.

The existing helper `Host` already separates its binary frame protocol from named host
services, runs blocking requests off the read loop, and carries the request's connection
in context. Those are useful transport patterns. The helper's `session` service remains
owned by `internal/helper/session` for helper-hosted PTY lifecycle. The new bridge must
not put wave calls into that service merely because both concepts mention a session.

The backend must authenticate the helper bridge before accepting the helper-provided
identity assertion. A remote helper that loses its bridge fails all outstanding proxy
requests; it does not answer them from a cached grant and does not keep an external
connection that can later reappear without re-enrollment. Whether a helper can host the
remote socket independently of one backend bridge, and how multiple backend clients
under one account are selected, are open questions in §8 because the current helper
endpoint admits generations and the tree does not yet define this reverse stream.

## 5. One dispatcher, two callers

The current in-process path is already close to the desired shape. `agenttools.Registry`
assembles declarations from the table in `internal/agenttools/registry.go`; `ForGrant`
projects the assembled table onto a grant; the policy middleware validates model
arguments; `narrowWave` builds `*agenttools.WaveCoordinator` from the run's environment
scopes and `RunContext.Session`; and `internal/assistant/execute_wave.go` contains the
five wave executors through the `WaveRecord` seam.

The missing abstraction is not a second executor. It is a caller-neutral dispatch
operation in `internal/assistant`, for example a `WaveInvocation` plus a
`WaveDispatcher` interface. The exact exported spelling is an implementation question,
but its shape is fixed:

1. The caller adapter supplies a context, a bound `agenttools.RunContext`, a resolved
   `content.Grant`, and the method's raw params. The external adapter obtains these only
   from its authorizer; the in-process adapter obtains them from the existing assistant
   run.
2. The dispatcher looks up the method in the same assembled registry. An absent method
   is `-32601`, not an executor-local special case.
3. It validates raw params against the declaration's assembled params schema before
   resource resolution or executor entry. This is the same contract shown to the model,
   including `additionalProperties: false`, explicit required fields, bounds, and the
   empty-object shape for no-argument calls.
4. It resolves resources with the declaration's resolver and the invocation's run
   context. For wave holdings, wait, say, and close, the session resource comes from the
   bound context; the caller cannot add a session field to redirect it.
5. It requires that the grant's effects and resource scopes make the executable tool
   reachable, as `Registry.ForGrant` does. A grant without the local environment must
   not expose `wave.spawn`; a grant without the session resource must not expose the
   other wave calls.
6. It calls the declaration's `Narrow` function, producing the scoped
   `*agenttools.WaveCoordinator`, and then calls the existing executor selected by the
   same declaration name. No executor receives a broad record or a caller identity.
7. It bounds and validates the result against the declaration's result contract before
   writing a JSON-RPC result.

The model middleware and the external endpoint are different adapters to this operation.
The model path still has its framework-specific event stream, approvals, checkpoints,
and tool-call announcements. The external path has a JSON-RPC response and a
connection-owned cancellation context. Neither path gets a second wave method body.
The executor remains responsible for the wave result semantics: `wave.holdings` and
`wave.wait` share `waveAnswer`; `wave.say` derives the wave and sender from the narrowed
coordinator; `wave.close` names the worker in its declared params but uses the
coordinator session from the capability; `wave.spawn` checks the environment capability
before `Register`.

The external authorizer is intentionally not the dispatcher. Its job is to answer
whether this accepted peer, on this connection, has an enrolled process-tree principal
and which session/grant context that principal may use. Its output is an invocation,
not a list of tool names. The separate pin task can therefore replace the authorizer
implementation without changing declarations or executor logic. Until that authorizer
exists, there is no valid external invocation.

This preserves A12 in a concrete way. The external socket contributes a transport and a
caller identity assertion; it does not create authority. The session context has exactly
the resource and effect ceiling the authorizer supplied, and the narrowed capability
contains no participant ids outside the wave record reachable from that session. If the
record has no wave for the session, the call refuses; it does not create one as a side
effect of an external connection.

## 6. The wire and contracts

The agent-facing request is the same JSON-RPC 2.0 vocabulary as the control plane, but
this is a Unix-stream transport rather than a WebSocket text frame. That distinction is
necessary for peer credentials and does not justify a second semantic protocol.

A representative exchange is:

```json
{"jsonrpc":"2.0","id":1,"method":"wave.holdings","params":{}}
{"jsonrpc":"2.0","id":1,"result":{"participants":[],"cursor":0}}
```

The method names remain `wave.spawn`, `wave.say`, `wave.wait`, `wave.holdings`, and
`wave.close`. The method params and result are the root and `$defs.result` in the
corresponding existing files under `contracts/tools/wave.*.schema.json`. For example,
`wave.spawn.schema.json` already requires bounded `command` and `task` strings and
contains the `live` result shape; `wave.holdings.schema.json` already makes params an
explicit empty-required object and declares the participant/mail/cursor result.

The existing files are currently agent-tool contracts rather than entries in the
WebSocket `contracts/openrpc.json` manifest. The implementation slice must make the
external method registry consume them through an explicit contract manifest or shared
contract loader. It must not copy their JSON into five new files merely to satisfy a
filename convention. If the existing checker cannot reference a tool contract's root
and result definition, the checker needs a deliberate reference/alias mechanism before
the endpoint ships; a validator bypass is not an acceptable compatibility shortcut.

The epic's “VALIDATED from the first method” rule means yes: every external method that
can be called in the first release has a declared params schema, a declared result
schema, a registered validator, and a real over-the-socket test. The first release may
contain only one method if that is the selected landing slice, but it may not contain a
catch-all external dispatcher that promises the other four later. Since the intended
surface is the five existing calls, the clean cutover is to register and validate all
five together, reusing the declaration table and contracts.

The external contract test must exercise the server's actual response, not marshal a
fixture assembled by the test. It must also send literal request payloads, including
malformed extra keys, missing required fields, overlong strings, and a valid empty
params object. A refused request must be shown not to enter the executor or mutate the
wave record. A valid request must be shown to reach the same `WaveRecord` seam and
produce a result accepted by the result schema. This is the external analogue of the
existing transport params and over-the-wire discipline in `contracts/README.md`.

## 7. Failure paths and intervals

The wave record remains in memory for the backend lifetime. The endpoint and its
connection state are transient; they cannot turn a failed transport into a durable
participant or a guessed completion. The following table states what is true after each
required failure and what a next start may do.

| Failure                                                 | What is true afterwards                                                                                                                                                                                                                                                                                                                                        | What the next start does                                                                                                                                                                                                                                                                                                 |
| ------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Socket missing before connect**                       | No external request reached the backend. No wave record, participant, grant, or process is created by the failed connect. The launcher reports that external orchestration is unavailable rather than silently claiming success.                                                                                                                               | A later `nocx-server` start publishes the endpoint only if the authorizer is present. The caller reconnects and re-enrolls; it does not infer that a previous launch was orchestrated from the agent name or environment.                                                                                                |
| **Runtime directory foreign, symlinked, or occupied**   | The endpoint refuses to start without following or replacing the path. The existing daemon lock and discovery socket remain governed by their own start transaction; no wave request is admitted.                                                                                                                                                              | Fixing the ownership/path issue is required before restart. The next start repeats the ownership and path checks. It never chmods a foreign directory into compliance and never unlinks a foreign non-socket.                                                                                                            |
| **Backend restarted while a caller holds a connection** | The old connection receives EOF or a transport error. In-flight requests are canceled; no fabricated RPC response is sent. The old in-memory wave record disappears with the backend, and stage-1 participant processes disappear with the backend under D5. A response that was not observed is unknown, not success.                                         | The new backend starts with an empty `MemoryStore` and no external controller slot. The caller must establish a new connection and enrollment. It must not ask the new record to adopt a participant or replay the old wave. The human-facing restart/interruption notice remains the open product question named in §8. |
| **Caller disconnects mid-call**                         | The request context is canceled and the connection slot is released only after the handler has stopped or reached the operation's defined cancellation boundary. A read/wait stops. A mutation follows its declaration: if it has already reached the wave record, its record effect remains; the endpoint does not claim whether the caller saw the response. | A new authorized caller may connect after the old slot is definitively released. For a mutation whose response was lost, the caller first reads holdings and does not blindly retry. Any stronger replay/idempotency answer is not silently invented here.                                                               |
| **Call names a wave whose record is gone**              | The dispatcher refuses before executor mutation with a named `wave unavailable`/`no such wave` error. An empty holdings result is not returned, because empty is a valid answer for a present wave and would conceal record loss.                                                                                                                              | The next start creates no wave implicitly. A new coordinator must start a new wave through the normal record-creation path, after its grant and session context are valid.                                                                                                                                               |
| **Authorizer or pin provider is absent**                | No admitting external endpoint is published, or a disabled diagnostic endpoint refuses before validation and dispatch. Same uid is not upgraded to enrollment. No grant, wave record, or process is created by the refusal.                                                                                                                                    | Once the provider is composed, the server starts the endpoint and the launcher repeats enrollment. Until then the agent runs only as an explicitly ordinary, not-orchestrated process if the launcher policy permits that separate mode; the orchestrated path remains closed.                                           |
| **Helper bridge unavailable on a remote host**          | The remote socket cannot complete the request. Outstanding calls receive a bridge-loss error; the helper does not execute a wave operation locally and does not replay a cached grant. The backend wave record remains whatever facts its own supervision has established.                                                                                     | The caller reconnects through a live, generation-matched helper bridge and re-enrolls. It reads holdings before retrying any mutation. If the backend also restarted, the empty-record rule above wins.                                                                                                                  |
| **Malformed, oversized, or unknown RPC**                | The request is refused with the standard JSON-RPC error before resource resolution or executor entry. The server does not log arbitrary payload bytes and does not close unrelated connections.                                                                                                                                                                | Nothing is recovered from the malformed request. The client sends a corrected request with a new id, subject to the same authorizer and slot.                                                                                                                                                                            |
| **Two callers on one session**                          | The first admitted logical coordinator retains the session slot. The second receives `session already has a wave caller`; it cannot consume mail or mutate the wave. If the first connection closes, the slot is released only after its in-flight operation boundary is settled.                                                                              | The replacement caller re-enrolls, acquires the slot, and asks holdings. The wave record and its session-keyed mailbox are not copied or reset.                                                                                                                                                                          |
| **Valid call with insufficient effect/resource scope**  | `Registry.ForGrant`/the dispatcher omits or refuses the tool before the executor. In particular, no environment scope means no `wave.spawn`, and no session scope means no holdings/wait/say/close. No participant or mailbox mutation occurs.                                                                                                                 | A new grant must be minted by the authority owner and explicitly approved where required. The caller cannot repair scope by adding a field to its RPC params.                                                                                                                                                            |

### 7.1 Invariant intervals

The endpoint's local-security interval opens only after the runtime directory has passed
ownership checks, the socket has been atomically published, and the listener has accepted
a connection. It closes when the listener is closed and the socket is unlinked. A stale
socket file before publication is not an endpoint; a path that accepts connections is a
live endpoint. Both ends matter because a path's existence cannot be treated as liveness.

A caller-authentication interval opens after accepted peer credentials and the separate
authorizer have bound the connection to an enrolled process-tree principal, session,
and grant. It remains valid while that principal's pinned root, lifecycle domain, human
approval, and connection slot remain valid. It closes on explicit revocation, root
termination, domain termination, authorizer refusal, or connection loss after all
in-flight calls have reached their cancellation boundary. A uid check alone never opens
this interval.

A session controller slot exists from successful acquisition for one session until the
connection is gone and all calls holding the slot have stopped or settled. A second
connection cannot enter the interval. This is what protects the session-keyed mailbox
cursor from two readers racing; releasing a slot before its last handler stops would
allow a replacement to interleave an old mutation.

A wave invocation's narrowed capability exists from the moment the dispatcher has
validated params, resolved resources, checked the grant projection, and completed
`Declaration.Narrow` until the executor returns or its context cancellation boundary
closes. The dispatcher never hands the capability to the caller. A rejected invocation
has no capability interval at all.

A participant record's interval is not changed by this endpoint. The spawn-and-register
design opens it at the prepared record before any irreversible spawn effect and closes it
when the enrolled incarnation is supervised or the record is terminal. Under the current
in-memory `MemoryStore` and D5, the entire interval is contained in one backend lifetime;
backend restart closes the record's lifetime by removing the record and the worker
process together. The endpoint does not extend that interval.

A JSON-RPC request/response correlation interval opens when the server records the
request id for a live connection and closes when exactly one result/error is enqueued or
the connection is lost. It does not extend across reconnect. This is why a request id is
not an idempotency key and why the response-loss path must be handled as unknown.

## 8. Open questions, stated as holes

1. **External grant minting and revocation are not in the tree.** The existing
   `WaveCoordinator` can narrow a `content.Grant`, but the 2026-08-15 design's
   `AuthorityBinding` and D8 `Delegation` are not implemented as the external grant
   source. The authorizer interface must be selected by the authority-model work: which
   human-approved binding supplies effects and environment/session scopes, how takeover
   suspends input, and who revokes or re-approves a scope-suspended controller. This
   document looked at `internal/agenttools/wave.go`, `internal/content`, and the two
   orchestration designs; it does not claim a grant that those files do not provide.

2. **The pin implementation is separate and unresolved.** `internal/coordinator` has
   interfaces for uid and path ownership, and Linux/macOS helpers already expose
   platform-specific peer credential primitives. The tree does not yet provide the
   process-tree root pin required by D13/D14. The missing seam is an authorizer input
   that can distinguish local accepted peer credentials from a helper-authenticated
   remote assertion and bind either to a non-reusable `(pid, start-time)` root. This
   document intentionally does not design pidfd/start-time mechanics.

3. **The remote reverse stream is not implemented.** `internal/helper/host` and
   `internal/helper/client` currently carry helper requests from coordinator to helper,
   session data, lifecycle data, notifications, and responses. They do not contain a
   helper-initiated opaque RPC proxy stream. The helper endpoint can host a second local
   socket, but the tree does not answer how that socket selects the owning backend bridge
   when more than one coordinator connection exists. The implementation must resolve
   that topology before remote orchestration is advertised.

4. **The launcher-versus-wrapper delivery was decided while this was being written, and
   the answer is the wrapper.** The 2026-09-05 amendment to the orchestration mechanism
   design §7.1 (commit 45aadb30, `nocx-rowqt.1`) settles it: the shell function that
   BRACKETS the agent ships, because `docs/lifecycle-protocol.md` §16 requires the shell
   to send the participant's declaration after the agent returns and before
   `agent_withdraw`, and `exec` removes the process that must send it. This document was
   written to be indifferent to that choice and remains correct under it — the wire path
   and the fail-closed rule are unchanged, and what moves is only the code that supplies
   the endpoint path and requests enrolment, which is now the shell function rather than
   a launcher binary. Two consequences the implementation inherits rather than re-decides:
   the caller reaching the endpoint is the AGENT, a child of the shell that holds the
   per-epoch capability, so the authorizer's identity assertion is about that child and
   not about the enrolling shell; and config staging is the shell function's, per the same
   amendment. No decision about staging is made here.

5. **Replay and idempotency are open.** `wave.spawn`, `wave.say`, and `wave.close` have
   request/response correlation but no external idempotency field in their existing
   schemas. The orchestration design already says acknowledgements travel with effects,
   while the current `waveAnswer` acknowledges the previous mailbox page. The tree does
   not yet specify a cross-connection mutation identity or retention window. The first
   implementation must either prove the no-blind-retry contract is acceptable or add a
   separately designed idempotency contract; it must not infer idempotency from JSON-RPC
   ids.

6. **The single-controller rule needs owner approval.** D10 is the conservative choice
   because the current `WaveCoordinator` embeds the session and `waveAnswer` uses that
   session as the mailbox reader. If the owner instead wants two processes in one
   enrolled tree to share a coordinator, the design must first define per-caller reader
   ids, ordered mutation arbitration, reconnect ownership, and whether a built-in run can
   overlap an external connection. No such semantics are present in `internal/wave`.

7. **Restart presentation is not designed.** The spawn amendment defines the in-memory
   result — the new record is empty and old participants are not adopted — but does not
   decide what the human sees when a backend restart breaks an external coordinator or
   remote helper bridge. The failure must remain visible; this document does not choose
   a notification surface.

8. **Contract-manifest integration is open.** The five wave files under
   `contracts/tools/` already carry params and `$defs/result`, while the current
   `contracts/openrpc.json` and transport checks discover WebSocket method contracts by
   method-name filenames. The implementation must choose a reference mechanism that
   lets the external endpoint validate the existing documents without duplicating them.
   This was checked against `contracts/README.md`, the wave schemas, and
   `internal/transport/openrpc_contract_test.go`; no existing external manifest answers
   it.

## 9. Assertions

These are acceptance assertions, not descriptions of the proposed implementation.

1. With a real `nocx-server` and a real enrolled external agent in a nocx pane, the agent
   calls `wave.spawn` over the private socket, and the response is the worker that the
   backend wave record has marked live. A fake response assembled beside the socket does
   not satisfy this assertion.
2. The same external agent calls `wave.holdings` with `{}` and receives the worker its
   session owns; it cannot add a session parameter to ask about another session.
3. The external agent calls `wave.say`, `wave.wait`, and `wave.close`; each reaches the
   existing `WaveRecord` seam and returns the existing result shape, not a second result
   vocabulary.
4. The in-process assistant and external endpoint use the same declaration row,
   parameter validator, resource resolver, narrowing function, executor, and result
   contract for each of the five methods. Changing the declaration's bound changes both
   callers' acceptance behavior.
5. A malformed params object, an unknown field, a missing required field, or an oversized
   field is refused before the wave executor runs, with JSON-RPC invalid-params and no
   record mutation.
6. An external grant lacking the local environment never offers or executes
   `wave.spawn`; a grant lacking the bound session never executes holdings, wait, say, or
   close. Adding a raw session or environment field to params cannot change that.
7. The lifecycle `socketpair` cannot be passed to the external endpoint in place of its
   listening Unix socket. A connection accepted by the endpoint has kernel-stamped peer
   identity before request parsing.
8. A runtime directory owned by another uid, a final symlink, a non-socket occupant, and
   an overlong path each refuse startup without replacing the foreign path. A stale
   socket left by a dead daemon is replaced only after the daemon lock is held.
9. With no enrollment/pin authorizer composed, no external wave request reaches an
   executor and the orchestrated wrapper reports a structured unavailable/refused
   result. Same-uid access alone never creates a wave participant.
10. A remote agent connects to the helper-hosted socket and receives the same JSON-RPC
    result bytes as a local agent. The helper proxy never calls a wave executor or stores
    wave membership; the backend's dispatcher does.
11. If the backend restarts while a caller is connected, the old connection ends, an
    in-flight request is not falsely reported successful, and a new backend starts with
    an empty in-memory wave record. No old worker is adopted by a new record.
12. If a caller disconnects mid-call, reads and waits cancel at their declared boundary;
    a mutation already committed remains recorded, while a lost response is treated as
    unknown and is not automatically retried.
13. A call for a wave whose record is gone returns a named unavailable/no-such-wave error,
    not an empty holdings result and not an implicit new wave.
14. Two callers trying to control one session cannot both acquire the controller slot.
    The second is refused without consuming mailbox state or mutating the wave; after
    the first connection settles and closes, a replacement can acquire the slot and read
    holdings.
15. A title, OSC sequence, PTY output byte, helper session data frame, or worker-written
    report cannot open, complete, or alter a wave RPC. Only the authenticated RPC path,
    the existing wave record, and the already-defined process/declaration facts can do
    so.
16. A JSON-RPC request id correlates one response only on its connection. Reconnecting
    and resubmitting the same id does not cause the server to claim the first mutation was
    idempotent.

## 10. What would falsify this design

This design is falsified if the external caller requires a second copy of any declaration,
executor, resource resolver, or narrowing rule to behave correctly. That would mean the
one-dispatcher/two-callers decision was not preserved.

It is falsified if a caller can name an arbitrary session in params, if same-uid peer
credentials alone create a grant, or if an environment bearer in the process environment
is required to reach the socket. Each would violate AD-7, A12, or D13 rather than merely
being an implementation bug.

It is falsified if remote orchestration requires the helper to own wave membership,
policy, participant state, or executor logic. The helper is an execution host and
transport carrier; making it a wave authority creates the second dispatcher this design
exists to prevent.

It is falsified if the endpoint has to parse PTY bytes, terminal titles, OSC markers, or
helper session data to decide whether a wave call is allowed or whether a participant
finished. That would cross AD-6 and ADR-0024's authenticated-channel boundary.

It is falsified if the listener can be reached through a loopback port, a foreign-owned
runtime directory, a symlinked path, or an unbounded socket frame. The private Unix
socket and bounded stream are the boundary, not incidental deployment details.

It is falsified if backend restart leaves a live external participant addressable while
the in-memory record is gone, or if the implementation adopts a process it did not prove
was spawned and enrolled in the current backend lifetime. That would contradict D5, A12,
and the spawn-and-register recovery contract.

It is falsified if two callers sharing one session can independently consume the same
mailbox cursor or race acknowledgement and mutation without a designed reader and
idempotency model. The current record's session-keyed reader makes that a correctness
failure, not an optimization concern.

It is falsified empirically if the real external happy-path test cannot run with herdr
absent, or if a remote helper proxy test shows a helper-side executor invocation. In
those cases the mechanism is not a second caller of the backend wave; it is either a
vendor-specific replacement or an unproved transport stub.
