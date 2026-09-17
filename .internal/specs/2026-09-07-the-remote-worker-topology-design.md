# The remote worker topology: one helper socket, many bridges

**Date:** 2026-09-07

**Status:** DRAFT — NOT APPROVED. This document resolves the transport topology that
blocks `nocx-rowqt.11`; it does not implement the proxy or decide the remaining
authority-model work.

## 1. In one sentence

A helper exposes one generation-qualified agent socket, each backend bridge registers a
unique connection lease on that socket, and the helper routes each accepted agent
connection over the lease named by its connection-level bind record while carrying only
bounded opaque envelopes and a helper-generated identity assertion to the backend.

## 2. What this crosses, and what is already decided

This design crosses the helper's generation endpoint, the helper/backend bridge, the
agent-facing JSON-RPC stream, and the backend tool authorizer. It does not move worker
state or authorization into the helper.

The binding decisions are:

- **AD-1 (`docs/architecture.md:101-114`)** splits communication into a raw binary data
  plane and a JSON-RPC control plane. PTY bytes remain raw and are never sent through
  this proxy. Agent worker calls are control-plane JSON-RPC, carried beside the helper's
  existing binary framing rather than folded into the PTY stream.
- **AD-2 (`docs/architecture.md:116-120`)** keeps one Go core and makes the helper an
  execution host, not a second implementation of worker orchestration.
- **AD-7 (`docs/architecture.md:167-173`)** keeps session identity server-authoritative.
  The agent cannot select a session by putting a session id into a worker request.
- **AD-8 (`docs/architecture.md:175-180`)** requires narrow interfaces and composition-root
  wiring. The proxy registry, bridge transport, remote authorizer, and worker dispatcher
  remain separate seams.
- **AD-9 and AD-10 (`docs/architecture.md:182-197`)** require bounded, ordered transport
  state and an explicit response when a helper-owned window loses bytes. The proxy adds
  no unbounded queue and does not change the PTY output window.
- **ADR-0024 decision 2 (`docs/decisions/0024-authenticated-shell-integration-channel.md:237-252`)**
  requires authority-bearing lifecycle traffic to use a channel that is not the tty. The
  worker socket is such a control channel; OSC and terminal output are not its carrier.
- **ADR-0028 (`docs/decisions/0028-eino-runs-the-loop-the-grant-is-ours.md:57-63`)**
  keeps grants and per-call authority in nocx. A helper-provided assertion is input to the
  backend authorizer, never a grant and never a tool-name allow-list.
- **Second-caller design D4, D5, D7, D8, D10, and D12
  (`.internal/specs/2026-09-05-the-second-caller-design.md:88-103`)** already decide one
  caller-agnostic dispatcher, bounded newline-delimited JSON-RPC, fail-closed admission,
  one coordinator slot per session, and a remote helper that transports an authenticated
  identity assertion without dispatching it.

The helper's existing generation endpoint remains private: `endpoint.Dir` derives it
from the account home and `endpoint.Path` names one generation with the protocol version
and the first 16 hex characters of the generation
(`internal/helper/endpoint/endpoint.go:110-145`). The new agent socket is a
**generation-qualified sibling** in that same directory, as the second-caller design
requires (`.internal/specs/2026-09-05-the-second-caller-design.md:196-218`). It is not a
per-bridge socket and does not replace the existing session endpoint.

## 3. What exists today

### 3.1 The helper wire

The helper wire is one binary stream with a 13-byte header:
`type:1 || seq:4 || ack:4 || length:4`, followed by a payload
(`internal/helper/proto/frame.go:1-23`, `:71-76`). A payload is bounded at
`1 << 20` bytes. The decoder refuses to trust an unknown type, zero length, or oversized
length prefix and resynchronizes without allocating the claimed payload
(`internal/helper/proto/frame.go:101-185`).

The normal payloads are JSON `Hello`, `HelloOK`, `Request`, `Response`, and `Error` values
(`internal/helper/proto/envelope.go:5-49`). A request has a numeric id, service, operation,
optional params, and correlation id. A response has the same request id and either a
result or an error. Results over the frame bound use a `ChunkedResult` response followed
by `Chunk` frames, with stream id, total bytes, and chunk count
(`internal/helper/proto/envelope.go:80-96`; `internal/helper/host/host.go:481-539`).

The backend client arms its pending-request map before writing a `TypeRequest`, routes
`TypeResponse` and `TypeChunk` back to that request, cancels through `TypeCancel`, and
fails all pending calls on transport loss (`internal/helper/client/client.go:106-172`,
`:208-251`). The handshake proves the nonce, protocol version, and expected helper
content hash; the helper instance id is recorded but currently unused
(`internal/helper/client/launch.go:238-261`).

The host side currently accepts `TypeRequest`, `TypeCancel`, `TypeKeepAlive`,
`TypeSessionData`, and `TypeLifecycleData` after the hello
(`internal/helper/host/host.go:138-188`, `:220-245`). It does not handle a response or a
new request arriving from a helper service. Therefore the current request/response
machinery is reusable as a framing and correlation pattern, but it is not already a
helper-initiated RPC path.

### 3.2 What already flows from helper to backend

The existing session service has a per-connection `Sink` with three outbound operations:
raw session data, raw lifecycle data, and unsolicited notifications
(`internal/helper/session/session.go:97-105`). `Host.SendSessionData` and
`Host.SendLifecycleData` write the two raw data-plane frame types, while
`Host.SendNotification` writes a `TypeNotify` frame
(`internal/helper/host/host.go:296-325`). The host never decodes the session or lifecycle
bytes (`internal/helper/host/host.go:260-293`); that preserves AD-6.

The session service uses `host.WithConnection` to associate a request with the host
connection that carried it (`internal/helper/host/host.go:380-398`). Its `Bind` operation
keeps sinks per connection, removes only that connection's attachments on release, and
explicitly permits several bound connections at once
(`internal/helper/session/service.go:228-261`). These outbound methods are the existing
helper-to-backend delivery seam. They are insufficient for a proxy because notifications
have no request id and no response path.

### 3.3 How a bridge is reached today

A local coordinator dials the helper endpoint directly. A remote coordinator obtains a
pty-less helper connection, starts `nocx-helper bridge <generation>`, and runs the same
client handshake and frame protocol through that process
(`cmd/nocx-helper/main.go:1-21`; `internal/helper/endpoint/bridge.go:23-73`). The bridge
only copies bytes between the exec lane and the helper endpoint; it does not forward a
TCP port or interpret the protocol.

For hosted sessions, `helperRegistry.OpenHosted` constructs a `hostHelper`, establishes
one client connection, then uses it to spawn and attach the session
(`internal/app/helper_git.go:452-483`, `:514-545`). `hostHelper.connectLocked` reuses its
own live client, or creates a fresh helper lane and `client.Dial` when that carrier is
lost (`internal/app/helper_git.go:839-894`). This is per `hostHelper` and per coordinator
session; the tree does not share one bridge client between unrelated coordinators.

## 4. Q1 — the helper-initiated opaque RPC proxy stream

### Decision

Add a transport-only proxy registration to the existing bridge connection and add a
pair of helper frame types for opaque proxy request and response envelopes. Reuse the
existing binary frame header, `MaxFrameBytes` bound, write mutex, request correlation,
chunking rules, and connection-loss handling. Do not reuse the `session` service for the
proxy and do not make the helper decode worker JSON-RPC method params.

The helper daemon will publish one generation-qualified sibling agent socket. A backend
bridge registers a fresh, random `bridgeID` over its existing bridge connection. The
registration response contains the generation-qualified agent socket path and that
bridge lease's id. The coordinator's launch adapter passes the id to the agent through
launch-owned configuration, not through a bearer capability in the environment. The id
is a routing label, not authority.

An agent connection begins with one bounded connection-level bind record naming the
`bridgeID`, then carries newline-delimited JSON-RPC requests. The helper validates only
the bind shape, size, and existence of the registered lease. It associates the agent
connection with the bridge connection that registered that id. The helper then forwards
each complete JSON-RPC line as an opaque proxy request envelope. The backend bridge
returns the exact JSON-RPC response line as an opaque proxy response envelope. The
helper writes that response to the bound agent connection.

The helper may inspect the routing bind record because it must select a bridge; it must
not inspect `wave.*` method names or params. The backend endpoint remains the one place
that parses JSON-RPC, validates contracts, resolves the bound session, checks the grant,
and dispatches the declaration. The helper's proxy service owns only lease registration,
connection routing, byte bounds, and closure.

The existing helper frame types cannot carry this path without an extension:
`TypeNotify` has no response correlation, while `TypeRequest` is currently parsed as a
helper service call by `Host.frame` and there is no host-side response/request handler
(`internal/helper/host/host.go:220-245`, `:328-378`). New directional proxy types make
opacity and direction explicit. Their payload should contain a bounded stream id and
bounded opaque bytes; it should not add a second JSON-RPC schema or a second worker
method vocabulary. The existing frame codec and response chunking are reused rather
than copied.

### Rejected alternatives

| Alternative                                                                    | Why it is rejected                                                                                                                                                                                                 |
| ------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Put worker requests in `TypeRequest` and register a `wave` or `worker` service | The helper would enter its service lookup and validation path, creating a second dispatcher and making helper code understand worker semantics. It also leaves the reverse response direction undefined.           |
| Use `TypeNotify` for helper requests and infer an acknowledgement              | Notifications have no request id and no response contract. A lost response would be indistinguishable from a refused call, violating the second-caller design's request/response rule.                             |
| Have the helper parse and dispatch JSON-RPC locally                            | D4 explicitly makes the helper a transport proxy. Local dispatch would create helper-side authority, duplicate declaration validation, and allow a remote helper to outlive or diverge from the backend's grant.   |
| Send the agent's JSON-RPC over the PTY or lifecycle stream                     | AD-1 and ADR-0024 keep worker control traffic off the tty and out of raw PTY bytes. A terminal sequence cannot authenticate a caller.                                                                              |
| Create one proxy socket per bridge                                             | The second-caller design already chooses one generation-qualified agent socket. Per-bridge paths multiply the `sun_path` budget and hide the real issue: the shared socket needs a connection-level route binding. |
| Let the helper reuse a cached backend grant after bridge loss                  | A grant is backend-owned and connection-scoped. Bridge loss must fail outstanding calls and close the route; cached authority would survive the authenticated channel that supplied it.                            |

### Q1 invariants

- **Proxy frame bound:** from the decoder accepting a proxy frame until it is delivered or
  refused, the frame payload is at most the existing `MaxFrameBytes`; the interval closes
  when the request is delivered, refused, or the bridge connection is lost. No claimed
  length causes an allocation before the bound check.
- **Opaque forwarding:** from the helper accepting a complete agent JSON-RPC line until
  the backend bridge receives its response or reports bridge loss, the helper copies
  bytes and routing metadata only; the interval closes on one response/error or loss. It
  never enters worker authorization or execution during that interval.
- **Correlation:** from the backend recording a proxy stream id before sending a proxy
  request until exactly one response/error is returned or bridge loss closes the stream,
  one stream maps to one agent connection and one request. Correlation does not survive a
  reconnect.
- **Bridge loss:** from an agent connection being bound to a bridge lease until that lease's
  bridge connection closes and all in-flight proxy streams reach their loss boundary, all
  new and outstanding calls on that lease fail closed and the agent connection closes.
  Other bridge leases and the helper's host sessions remain alive.

## 5. Q2 — bridge ownership when more than one coordinator connects

### What the shipped code does

More than one coordinator connection is reachable and is already tested. The helper's
accept loop tracks each accepted connection independently, starts a handler goroutine,
and explicitly says that two coordinators may connect at once; it refuses no second
connection (`internal/helper/endpoint/serve.go:11-27`, `:34-110`).

The helper process constructs the session service once and constructs a fresh protocol
`Host` for each accepted connection (`cmd/nocx-helper/main.go:145-176`). The session
service's tests prove that two bound connections can coexist, each receives the data for
its own attachment, and releasing one does not remove the other's attachment
(`internal/helper/session/multi_connection_test.go:22-76`, `:124-153`). The real socket
integration proves that a second coordinator sees a session opened by the first while
both connections remain live (`internal/helper/endpoint/serve_test.go:331-364`).

The shipped socket is shared per generation: `endpoint.Path` derives one path from the
protocol version and generation prefix (`internal/helper/endpoint/endpoint.go:119-145`).
The two coordinator connections therefore do **not** have one socket owner today. They
are two independent bridge lanes, each reaching the same process-scoped helper and the
same generation socket. A helper-hosted agent request cannot be routed by asking which
bridge owns the socket, because neither bridge owns it.

### Decision

The agent socket is owned by the helper generation, not by any bridge. A connection-level
bind record names the bridge lease that should carry that connection's opaque requests.
The helper maintains a registry of active bridge ids, each bound to the exact `Host`
connection on which it was registered. The registry is transport state, not session or
worker state.

A bridge registers its id only after the existing helper handshake succeeds. Registration
is atomic: an unused id creates one lease, a duplicate id is refused, and no registration
replaces an existing lease. The id is freshly minted by the backend bridge owner and is
long enough to be collision-resistant; its value is not a credential and cannot authorize
an agent. The helper socket path remains one generation-qualified sibling path, and its
mode and directory inherit the existing `0700`/`0600` endpoint rules
(`internal/helper/endpoint/endpoint.go:49-63`, `:148-191`).

A second bridge to the same helper may register its own id and receive its own agent
connections. It must be refused if it attempts to register an id already held by the
first bridge or to bind an agent connection to an unknown or released id. There is no
first-connection-wins rule, and no second bridge can steal the first bridge's live agent
connection.

The helper socket can remain published while one bridge disappears because it belongs to
the generation, not the bridge. When a bridge's helper connection reaches EOF or loss,
the helper atomically removes that bridge lease, closes every agent connection bound to
it, fails its outstanding proxy streams, and leaves other leases and session state alone.
This is the same connection-versus-session boundary already enforced by
`endpoint.Serve` and `session.Service.Bind` (`internal/helper/endpoint/serve.go:29-33`;
`internal/helper/session/service.go:228-261`). A later bridge registers a new id; it
does not inherit the old id's live requests or cached authority.

### Rejected alternatives

| Alternative                                                         | Why it is rejected                                                                                                                                                                                                                                               |
| ------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| First connection wins ownership of the generation socket            | The shipped endpoint serves both connections, and both lanes are valid. Choosing by accept order would route a request to the wrong backend and would contradict the observed multi-connection behavior.                                                         |
| Share one bridge client between all coordinators                    | `hostHelper.connectLocked` owns one lane per helper/session, while independent coordinators may each create their own lane. A hidden shared client would change connection-loss and authorization scope and would make one coordinator's closure affect another. |
| Route by the socket pathname alone                                  | The pathname names only the generation. It contains no bridge identity, and adding a bridge suffix would violate the frozen one-agent-socket topology rather than solve per-connection ownership.                                                                |
| Let a second bridge replace the first registration                  | Replacement would strand the first agent connection and let a later connection receive responses for earlier requests. Refuse duplicate ids; allow a second bridge only under a distinct lease.                                                                  |
| Keep the agent socket and agent connections alive after bridge loss | The backend authorizer's binding and grant are no longer reachable. Continuing would turn a dead transport into cached authority and would make response-loss semantics unknowable.                                                                              |

### Q2 invariants

- **Single lease owner:** from successful registration of a bridge id until that bridge's
  connection loss, explicit release, and all lease-owned streams and agent connections
  are closed, exactly one helper `Host` connection is associated with that id. The interval
  closes before another registration may reuse the id.
- **Agent route:** from successful bind of an accepted agent connection until the owning
  lease closes, every complete request from that connection is sent only over that bridge
  connection. The interval closes with an explicit bridge-loss/EOF outcome, not by
  silently retargeting the connection.
- **Socket publication:** from atomic publication of the generation-qualified sibling
  socket until helper shutdown and unlink, one helper generation owns the listener. A
  bridge's departure does not close this interval; helper shutdown does.
- **Second bridge refusal:** from a duplicate or unknown bridge id being presented until
  the bind response is written and the attempted connection is closed, no route is
  created, no request is forwarded, and no existing lease changes.
- **Identity assertion:** from the helper accepting an agent connection and reading its
  kernel peer facts until that connection or its bridge lease closes, the assertion is
  bound to that agent connection and that bridge id. It cannot be copied into another
  connection or used after the lease closes.

## 6. The remote identity assertion

The local endpoint's current identity input is `toolendpoint.Peer{UID, PID}`. The endpoint
reads the UID and, when available, PID from the accepted Unix connection before parsing
requests (`internal/toolendpoint/endpoint.go:225-280`). `PeerProcess` is deliberately
separate because a bare pid is racy; `internal/coordinator/peer.go:23-29` says it must be
paired with a start time, and `internal/peerpin.Root` defines that non-reusable pair as
`PID` plus `StartTime` (`internal/peerpin/pin.go:11-22`). The authorizer receives this
peer assertion and returns a bound invocation or refusal
(`internal/toolendpoint/authorizer.go:9-28`).

The remote helper must send the backend an assertion with this precise shape, carried as
metadata of the authenticated proxy request and never as agent-authored JSON-RPC params:

```text
RemotePeerAssertion {
    helperGeneration   // the content hash already checked by hello-ok
    helperInstance     // the instance id already returned by hello-ok
    bridgeID            // the routing lease on this helper connection
    remoteUID           // uid stamped by the helper kernel
    remotePID           // pid stamped by the helper kernel
    remoteStartTime     // start time observed by the helper for remotePID
}
```

`helperGeneration` and `helperInstance` bind the assertion to the helper handshake and
prevent a response from one helper connection being treated as another's. `bridgeID`
binds it to the route selected by the agent connection. `remoteUID`, `remotePID`, and
`remoteStartTime` replace the local backend's kernel `Peer` facts with the corresponding
facts from the remote machine; the start time prevents pid reuse. The helper constructs
these fields from its accepted Unix connection and its host-side process identity seam,
not from the agent's request bytes.

The helper does not currently expose that peer-credential seam: `endpoint.Serve` passes
raw `net.Conn` values to its handler (`internal/helper/endpoint/serve.go:34-109`), and
no `SO_PEERCRED`/`LOCAL_PEERCRED` reader exists under `internal/helper`. Adding the
per-platform read is therefore part of the implementation prerequisite, using
`internal/coordinator.SystemPeerCredentials` as the existing pattern.

This assertion is evidence, not authority. It does not prove human approval, does not
mint a grant, does not authorize a worker method, and does not by itself prove that the
agent remains a member of the enrolled process tree. The backend must authenticate the
helper bridge first, then pass the remote assertion to a remote-aware authorizer. The
existing authorizer contract already has the correct division of responsibility:
`Admit(Peer)` produces an invocation, while the endpoint does not decide policy
(`internal/toolendpoint/authorizer.go:17-28`). The remote variant must preserve that
shape without pretending a remote UID is the backend's `SelfUID`.

### Rejected alternatives

| Alternative                                                       | Why it is rejected                                                                                                                                                               |
| ----------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Trust `remotePID` supplied by the agent                           | An agent can write any JSON-RPC field. Caller-supplied identity is not a kernel assertion and is replayable.                                                                     |
| Treat the bridge process's local PID as the agent identity        | The bridge is a separate process and each coordinator has its own exec lane. Its local peer facts identify the bridge, not the remote agent that connected to the helper socket. |
| Carry a bearer token or capability in the assertion               | D12 and ADR-0028 keep authority in the backend grant and authorizer. A bearer would survive outside the process-tree and connection intervals this design needs to close.        |
| Treat `remoteUID == backend SelfUID` as sufficient                | UID namespaces belong to different machines and do not establish process identity or ancestry. At most, the remote UID is helper-local evidence.                                 |
| Let the helper authorize or dispatch after checking the assertion | That would make the execution host a second authority and would duplicate the backend declaration and grant pipeline.                                                            |

## 7. Failure paths and deliberate holes

| Failure                                    | What is true afterwards                                                                                                                                      | What the next start does                                                                                  |
| ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------- |
| Agent connects before binding              | No bridge is selected and no worker request is forwarded.                                                                                                    | The agent sends a valid bind record or receives a named refusal; it cannot guess a default bridge.        |
| Unknown or duplicate bridge id             | No lease is created and no existing lease changes.                                                                                                           | The bridge registers a fresh id; the agent reconnects through that id.                                    |
| Bridge dies while its agent socket is live | Its lease, agent connections, and in-flight proxy streams close. Responses not observed by the agent are unknown. Other bridges and helper sessions survive. | A replacement bridge registers a new id and the agent re-enrolls; it does not replay mutations blindly.   |
| Helper dies                                | The generation socket and all bridge leases disappear; helper sessions follow the existing helper shutdown policy.                                           | The coordinator reports helper loss and re-establishes the generation endpoint before a new enrollment.   |
| Backend restarts while helper remains      | The existing bridge lanes close, so all proxy leases close. The helper does not retain a grant or dispatch cached calls.                                     | A new backend creates a new bridge and requires a new authorizer admission.                               |
| Proxy envelope is oversized or incomplete  | The proxy request is refused before an unbounded allocation or dispatch.                                                                                     | The agent sends a corrected request with a new request id.                                                |
| Remote assertion cannot be validated       | The backend authorizer refuses before dispatch. No worker record or grant is created by the refusal.                                                         | The implementation must surface a named remote-identity refusal; it must not downgrade to same-UID trust. |

The following holes are deliberately left open rather than guessed:

1. **Exact remote process-tree proof.** The tree now provides `peerpin.Root{PID,
StartTime}` and local ancestry walking, but it does not provide a remote helper
   assertion verifier or a protocol for proving that a later agent connection remains a
   descendant of the enrolled remote root. This design fixes the assertion's fields and
   trust boundary; the pin/authorizer work must decide whether the helper attests the
   immediate peer, the enrolled root, or a bounded ancestry proof.
2. **Assertion authenticity beyond the existing helper handshake.** `client.Dial` checks
   nonce, version, and content hash, but the current handshake does not define a
   cryptographic signature from a remote host. The threat model and the authorizer work
   must state whether the trusted SSH account plus content-addressed helper is sufficient,
   or whether a host-bound key is required.
3. **The exact connection-level bind wire spelling.** The topology requires one bounded
   bind record before worker JSON-RPC, but the implementation must choose whether that is
   a transport preamble or a reserved JSON-RPC handshake method while preserving the
   second-caller design's method and contract rules.
4. **Bridge registration lifecycle API.** The transport-only service name, registration
   response shape, and close/release operation are not in the current helper ABI. The
   implementation must add them without using the already-owned `session` service name.
5. **Proxy backpressure policy.** This design requires bounded proxy streams and explicit
   bridge loss, but the exact number of concurrent in-flight agent requests and whether a
   saturated bridge refuses new requests or applies per-bridge backpressure belongs to
   the implementation plan.
6. **Remote peer acquisition is not implemented.** The helper endpoint currently receives
   raw connections and has no `PeerCredentials` implementation. The implementation must
   add the Linux and Darwin peer read, pair the observed pid with its start time, and
   define the failure interval when those facts cannot be obtained; it must not fill the
   assertion with agent-supplied values.
7. **Bridge-id handoff to remote staging is not implemented.** The backend bridge mints
   the route id, while the remote shell wrapper owns launch staging; the tree has no
   mechanism that carries the id from the coordinator through the helper to that
   launch-owned configuration. The implementation must define that handoff without
   treating the route id as authority or assuming an environment variable is safe.

## 8. Acceptance assertions for the implementation plan

1. Two independent backend bridge lanes can register distinct ids against one helper
   generation and can serve agents through the one sibling socket without cross-routing.
2. A duplicate id and an unknown id are refused without changing an existing route.
3. A bridge-initiated proxy request reaches the backend tool endpoint as opaque bytes, and
   the helper never dispatches a worker method or constructs a worker grant.
4. The backend receives the helper-generated remote assertion, rejects an agent-authored
   identity substitution, and refuses when the remote pin/authorizer cannot validate it.
5. Closing one bridge closes only its agent connections and in-flight calls; another
   bridge's agent connection and the helper's PTY session remain usable.
6. Oversized and truncated proxy frames are bounded and named, and all response-loss
   paths remain unknown rather than being retried as mutations.
7. The real socket test proves the one-socket, two-bridge topology; a test that only wires
   two in-memory fakes is insufficient.
