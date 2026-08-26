# ADR-0039 — an HTTP upload route beside the WebSocket

- **Status:** accepted (2026-08-21)
- **Amends:** `AD-1` (what may travel beside its two planes) —
  `docs/architecture.md:101`, the amendment at `:111`. See "What this leaves to
  AD-1" below.
- **Reads, does not change:** `AD-9` and `AD-10`
  (`docs/architecture.md:169`, `:176`), `AD-6`,
  [ADR-0026](0026-control-plane-runs-off-the-read-loop.md) (the read-loop
  invariant and the admission bounds),
  [ADR-0001](0001-xterm-js-as-vt-frontend.md) (the framing this route stays out
  of).
- **Taken from** the upload design
  ([`.internal/specs/2026-08-21-upload-to-the-active-tab-design.md`](../../.internal/specs/2026-08-21-upload-to-the-active-tab-design.md)),
  decisions **D3** and **D4**, endpoint **§5.4**.
- **Related:** `nocx-9le.5`, `nocx-9le.5.12`.

## Context

### What AD-1 says, in its own words

AD-1 binds "all frontend↔backend communication" (`docs/architecture.md:103`) and
its rule is "one WebSocket per client, split into two planes; sessions
multiplexed by server-assigned session-id" (`:105`). The two planes are
enumerated exactly:

> **Data plane** (PTY I/O) = raw **binary** frames:
> `1-byte version || 1-byte msg-type || 16-byte session-id || payload`.
> PTY bytes are **never** wrapped in JSON/JSON-RPC — base64 + parse overhead
> would dent the hero rendering perf. (`docs/architecture.md:106`)

> **Control plane** = **JSON-RPC 2.0** over text frames: `open`, `close`,
> `resize`, acks, and connection management as JSON-RPC requests &
> notifications […] (`docs/architecture.md:107`)

It also allocates the growth room: "the version byte AND a reserved `metadata`
msg-type are allocated now, so the Phase-2 Tier-B helper feed can ship without a
wire break" (`:113`), and it names the security invariant "auth token +
bind-to-localhost by default" (`:114`).

What AD-1 does **not** say is that every byte between renderer and backend must
travel over that socket. It says what the socket carries and how, and it forbids
one specific mixture — PTY bytes wrapped in JSON. But its "Binds" clause is
`all frontend↔backend communication`, and read plainly that covers a file
upload. So this is a crossing, and `AGENTS.md` is explicit that a boundary is
changed deliberately and in the open rather than decided inside a commit. This
document is that record.

### What is being built

The upload design (`§1`) puts a file from the user's machine onto the host the
active tab is connected to. The control plane mints the transfer:
`files.upload` takes a `bindingId`, a destination directory, a name and a size,
and answers with a `transferId` plus — when the bytes are coming from the
renderer rather than from the backend's own disk — a single-use ticket and a URL
(`§5.3`). The bytes themselves then arrive on a streamed `POST /upload/{ticket}`
(`§5.4`).

The surface this needs already exists. `WSServer.Start` builds an
`http.ServeMux` (`internal/transport/ws.go:1082`) with exactly one route on it,
`mux.HandleFunc("/session", s.handleSession)` (`:1083`), and serves it on a
loopback listener whose default address is `127.0.0.1:0`
(`internal/transport/ws.go:1086-1088`, `internal/transport/ws_auth.go:186-191`).
Adding a second route to that mux costs one line and no new listener, no new
port and no new process.

## Decision

**File bytes travel on a streamed `POST /upload/{ticket}` served by the same
`http.ServeMux` that serves `/session`. They travel on neither of AD-1's two
planes, and no third msg-type is allocated on the data plane.**

Three things bound that, so the crossing is a single named exception rather than
a licence:

1. **The control plane still owns the transfer.** Mint, cancel, progress and
   completion are `files.upload`, `files.uploadCancel`, `files.uploadProgress`
   and `files.uploadDone` — ordinary JSON-RPC methods and notifications with
   schemas in `contracts/` (`§5.3`). Authorisation of the _destination_ happens
   there, through `Registry.Acquire` re-checking that the binding's session
   belongs to the requesting connection. The HTTP route carries a body and
   nothing else: it names no destination, no session and no binding.
2. **The route is single-purpose and ticket-addressed.** A ticket is minted by a
   `files.upload` call that was already authorised on the socket, names exactly
   one pending transfer, and is claimed once. It is not a general "the backend
   also speaks HTTP" seam.
3. **`frame.go` and `frame.ts` are untouched.** The data plane keeps two
   msg-types, `MsgTypeData = 0x01` and `MsgTypeMetadata = 0x02`
   (`internal/transport/frame.go:79-80`), and `MsgTypeMetadata` stays reserved
   for the Phase-2 Tier-B helper feed AD-1 allocated it for.

### What this leaves to AD-1

AD-1's "Binds: all frontend↔backend communication" now has exactly one
documented exception, in one direction, for one payload class: bulk file bytes
for a transfer the control plane has already authorised. This document was
written without claiming the authority to widen AD-1's own wording, because that
was the coordinator's call rather than a side effect of writing it. The call was
made (`nocx-9le.5.16`), so AD-1 now carries the line.

There is a house convention for that, and it is worth naming so the next person
follows it rather than reinventing it: AD-1 has been widened twice before, and
both times the amendment was written into `docs/architecture.md` itself, dated
and attributed inline — "amended 2026-08-02, nocx-m64b, nocx-rtg0.13"
(`docs/architecture.md:109`) for ledger facts, and "amended 2026-08-14, ADR-0029,
nocx-uz7f" (`:110`) for presentation requests. ADR-0024 likewise declares
"**Amends:** `AD-1`" in its own header
([ADR-0024](0024-authenticated-shell-integration-channel.md):8). An ADR that
crosses AD-1 and a line in AD-1 that says so are two halves of one change, and
both halves are now in place: the amendment is at `docs/architecture.md:111`,
dated and attributed the same way, and this header declares `Amends: AD-1`.

## Rationale

Three reasons, in descending strength.

### 1. The data plane is one TCP connection already carrying PTY

This is the reason that would survive even if the other two were solved.

There is one WebSocket per client (`docs/architecture.md:105`) and one
`readLoop` goroutine per connection (`internal/transport/ws.go:1857`). That
single loop takes every message off the socket and dispatches by type: text to
`handleControlFrame`, binary to `handleDataFrame`
(`internal/transport/ws.go:1867-1878`). Every keystroke for every tab on that
connection comes through it — the comment at the drop site says so in as many
words: "the readLoop is the one goroutine feeding EVERY session on this
connection" (`internal/transport/ws.go:2119-2123`), written after one dead SSH
channel froze every tab (`nocx-o2le`).

A multi-gigabyte upload multiplexed onto that connection competes with terminal
responsiveness on two counts at once. It competes for the socket, because the
upload's frames interleave with PTY output frames in the same TCP stream, and
for the loop, because the loop must read each one. ADR-0026 §1 states the
read-loop invariant with both ends: "from before `ReadMessage` returns until the
next `ReadMessage` begins, the loop spends only bounded, small work, and it never
blocks on admission". The data-plane half is held the same way — `handleDataFrame`
hands the payload to a non-blocking `EnqueueWrite` rather than to the write
itself, precisely so one stuck channel cannot hold the loop
(`internal/transport/ws.go:2119-2126`). Feeding that loop a stream of large
upload frames is exactly the pressure both halves were written against.

The existing fairness machinery does not help, because it points the other way.
`FairChunk` (8 KiB, `internal/transport/ring.go:31`) bounds how many bytes one
session writes per WebSocket message before releasing the shared `wsConn` write
mutex, so a flooding tab cannot stall an interactive one
(`internal/transport/ws.go:2181-2187`). That is server→client fairness on the
output path. There is no counterpart on the input path, and an upload is an
input.

An HTTP request is a separate TCP connection. The upload's bytes and the
terminal's bytes do not share a stream, a read loop or a write mutex, and the
kernel schedules them against each other the way it schedules any two sockets.
The obvious alternative — multiplex the upload onto the socket we already have —
buys one fewer connection and pays for it with the responsiveness that AD-1's
own data-plane rule exists to protect.

### 2. Flow control on that plane would have to be invented; HTTP already has it

nocx has flow control, and it is the wrong direction.

AD-10 is a "bounded in-flight-byte **credit per session**; when the credit is
exhausted, apply backpressure to the PTY/SSH read (throttle the source — **never
drop, never grow unbounded**)" (`docs/architecture.md:180`). It is implemented
in `ringToConn`, which stops sending once unacked bytes reach `CreditLimit`
(64 KiB, `internal/transport/ring.go:22`) and resumes when a client `ack` frees
room (`internal/transport/ws.go:2181-2184`), and in `outputRing.write`, which
blocks the PTY pump when the ring is full — "this is the AD-10 seam: throttle
the source, never drop, never grow unbounded"
(`internal/transport/ring.go:67-70`). Every part of that governs bytes going
**server→client**, sized for terminal output: a 256 KiB ring
(`internal/transport/ring.go:14`), a 64 KiB credit window, 8 KiB frames.

Client→server there is no credit at all. An inbound data frame is handed to
`sess.EnqueueWrite` (`internal/transport/ws.go:2149`), a non-blocking submission
to a queue 64 frames deep (`internal/session/session.go:86`), and the overflow
policy is written down and is fatal for a file: the frame is **dropped** and the
tab is notified (`internal/transport/ws.go:2149-2153`). That is the right answer
for keystrokes — `writeQueueDepth`'s own comment says a session at that depth
"is not slow, it is stuck", and buffering input the user will never see arrive
is worse than saying so (`internal/session/session.go:80-86`). It is the wrong
answer for a file, where a dropped chunk is a corrupted result.

So carrying an upload on the data plane means inventing the missing half:
credit in the client→server direction, an ack for it, a chunk sequence, a
resume-from-offset rule, and a way for the sender to stop when the SFTP write
falls behind. That is a second flow-control protocol, spanning both codecs,
existing to serve one feature. Over HTTP the transfer gets an independently
flow-controlled byte stream instead: the sink pulls through an `io.Reader`
(`§5.2`) at whatever rate the far filesystem accepts, net/http stops reading the
socket when nobody is pulling, and the sender stops when that window closes —
one connection's window against another's, with the shared read loop nowhere in
the path.

Be precise about why the browser cannot supply the missing half itself.
`WebSocket.bufferedAmount` looks like backpressure and is not: it reports what
the browser has queued locally, and says nothing about whether the backend read
it, whether the session's 64-frame queue has room, or whether the last SFTP
write landed. A sender pacing on it would still overrun the sink, and the only
place that overrun can be absorbed is the one read loop.

The framing ceiling makes the same point from the other end. `wsReadLimit` is
16 MiB and a frame above it is refused by the protocol layer with close code
1009, which closes the connection (`internal/transport/ws.go:1568-1577`). Any
upload larger than that must be chunked, chunking needs sequencing and
completion inside the payload, and getting the bound wrong does not fail the
upload — it takes down every tab on the socket.

### 3. A new msg-type is a second codec beside two that are pinned to each other

The data plane's codec exists twice, once in Go and once in TypeScript, and the
duplication is deliberately fragile so that nobody changes one alone.
`frame.go`'s package comment says it: "The TS half of this codec lives in
frontend/src/frame.ts; a golden-vector test on each side (frame_test.go /
frame.test.ts) pins the layout so a unilateral change to either codec fails
before reaching a user" (`internal/transport/frame.go:14-16`). `frame.ts` says
the same from its side (`frontend/src/frame.ts:1-4`). The vector is one hex
string — `01010123456789abcdef001122334455667768690a`
(`frontend/src/frame.test.ts:18`) — asserted by `TestGoldenVector`
(`internal/transport/frame_test.go:131`) and by the decode test on the TS side
(`frontend/src/frame.test.ts:69-77`).

Today that codec has one payload meaning. The header is
`1 version || 1 msg-type || 16 session-id`, `FrameHeaderSize = 18`
(`internal/transport/frame.go:82`), and bytes 18 onward are "raw PTY bytes",
full stop (`internal/transport/frame.go:22`). A `0x03` upload msg-type breaks
both halves of that. The payload stops being one thing, so the decoder grows a
branch on both sides. And the 16-byte identity field stops being a session-id:
an upload is addressed by a `bindingId`, which is 16 bytes of `crypto/rand` in
hex (`internal/filesystem/binding.go:370-377`) and so happens to fit — which is
worse, not better, because the field would then mean two different things
depending on a byte that precedes it, and nothing in either codec would say so.

The alternative is real, and it is the cheaper one: an HTTP handler is one
function in `internal/transport`, in Go, tested in Go, with the browser side
being `fetch`. No golden vector moves. Nothing in `frontend/src/frame.ts`
changes. `MsgTypeMetadata` stays available for the thing AD-1 reserved it for.

## Consequences

### There is now a second authorisation mechanism, and it is a bearer credential

State it plainly, because the next person to add an HTTP route inherits it.

`/session` is authorised by a per-launch capability token carried in
`Sec-WebSocket-Protocol` and compared in constant time
(`internal/transport/ws_auth.go:29-39`, `:255-279`), with an `OriginPolicy` as a
second, weaker guard (`internal/transport/ws_auth.go:216-253`). The comment that
put the token in that header is explicit about why: "the browser WebSocket API
cannot set Authorization; a query parameter would leak into URLs, proxy logs and
devtools; and a first-frame handshake would authenticate after the upgrade,
which is too late" (`internal/transport/ws_auth.go:31-35`).

A `POST` cannot present a subprotocol. So the sink ticket is the credential, and
these are its properties:

- **Possession authorises both the destination and the bytes written to it.**
  The ticket names a transfer that a prior, socket-authorised `files.upload`
  already bound to a binding, a directory and a name, so a stolen ticket cannot
  redirect a write. It can win the one-shot claim and put attacker-chosen
  content at that path. That is an integrity violation, not merely a denial of
  service. Be precise about what is new here: it is not the first bearer
  credential — the per-launch capability token is one, and it authorises strictly
  more, since a socket can open a session and type anything into it. What is new
  is a credential that travels in a URL path, and one whose entire purpose is to
  authorise a single filesystem write on a remote host.
- **It is a bearer credential with no second factor.** There is no origin-bound
  session, no signature over the body, and no proof that the claimant is the
  renderer that asked for it. What bounds the exposure is time and arity: one
  claim, a TTL enforced by a mint-side timer, and a claim event placed
  immediately before the body is read (`§5.4`).
- **It travels in the request path.** Acceptable only because the request never
  leaves loopback. Both tickets are minted from `crypto/rand` at no less than
  128 bits, are never logged and never appear in an error string (`§D4`).

### The route needs CORS, and that is part of what it costs

Not foreseen when this was decided, and written down now because the next
person adding an HTTP route pays it too (`nocx-9le.5.19`).

The renderer resolves the upload URL against the **socket's** origin rather than
the document's, because under `dev-web` the page is served by vite on one port
and the backend listens on another. Every `POST` here is therefore a
cross-origin request in the configuration the product is developed and tested
in, and the browser preflights it — the body's `Content-Type` is not on the CORS
safelist. Until the server answered that, the whole feature was unreachable from
a browser while every unit test on the route was green, because every one of
those callers is a non-browser client and a non-browser client never asks for
these headers.

The contract is scoped to `/upload/{ticket}` and to nothing else on the mux, and
the shape of it is the same defence-in-depth argument as above rather than a
second credential: the origin is decided by the same `OriginPolicy`, and decided
**before** the ticket is read out of the path, so a refusal is not an oracle for
a credential that authorises a write. `OPTIONS` answers `204` without touching
the ticket — a preflight precedes every upload, so one that claimed a one-shot
ticket would break the route for the only client that needs it. The requesting
origin is echoed exactly and never as `*`, `Vary: Origin` is always sent,
`Access-Control-Allow-Credentials` is never sent because the ticket is the
credential, and the allow-list is `POST` plus `Content-Type` and nothing more.

The part that is easy to get wrong: the headers belong on **every** reply,
including `400`, `409`, `410` and `5xx`. A browser hands the page nothing at all
from a cross-origin reply that does not name it — the fetch rejects with
"Failed to fetch" — so an error response without them collapses "the ticket is
gone" into "the network died", which is exactly the distinction the renderer
depends on.

### The `OriginPolicy` does not come for free, and it is weaker here

`authorize` is not mux-level middleware. It is called from inside
`handleSession` (`internal/transport/ws.go:1807`), and that is its only
non-test caller. The upload handler must therefore call the origin check
itself; a route that forgets has no guard at all.

And when it does call it, the guard it gets is weaker than `/session`'s.
`LoopbackOriginPolicy` allows an absent `Origin`, and the reasoning written into
that decision is that "a non-browser caller still has to present the token"
(`internal/transport/ws_auth.go:84-87`). On `/session` that is true. On
`/upload/{ticket}` the ticket _is_ the token, and it is in the URL rather than a
header, so for a caller that sends no `Origin` the check reduces to "is `Host`
loopback" (`internal/transport/ws_auth.go:91-93`). The origin policy is defence
in depth against a browser page here, exactly as it is on `/session`, and it is
not a second credential.

### The route sets its own deadlines

The shared `http.Server` is constructed with `ReadHeaderTimeout: 0`
(`internal/transport/ws.go:1101-1103`), which is deliberate because `/session`
is a long-lived upgrade. A second route on the same server inherits that zero.
The upload handler therefore applies its own header deadline and a per-read
stall deadline on the body (`§5.4`); without them, valid headers followed by
silence hold a transfer and an SFTP lease open indefinitely.

Note the shape of this for anyone adding a third route: server-level timeouts on
this mux are set to suit the upgrade, so every non-upgrade handler owns its own.

### What is unchanged

- **AD-6 survives verbatim.** The backend still never parses the terminal byte
  stream. Upload bytes are a file the user chose, not output the backend
  interpreted.
- **AD-9 and AD-10 are untouched.** The replay ring, the credit window and the
  fairness chunking govern the same bytes they governed before, and the upload
  adds nothing to them.
- **The data-plane codecs are frozen.** No golden vector changes, no msg-type is
  consumed, and `MsgTypeMetadata` remains reserved for the Tier-B helper feed.
- **AD-1's "Prevents" clause is not reopened.** What AD-1 exists to prevent is
  shell-locked IPC that would block a future web version, and a heavyweight
  transport abstraction (`docs/architecture.md:104`). An HTTP `POST` to the same
  loopback server is neither: it works identically in the desktop app, under
  `dev-web`, and against a networked backend, and it is `net/http` from the
  standard library on both sides.
- **AD-1's security invariant holds.** "auth token + bind-to-localhost by
  default" (`docs/architecture.md:114`) — the route is on the same
  loopback-bound listener and is credentialled, with the credential being the
  ticket rather than the launch token.
