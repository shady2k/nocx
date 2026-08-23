# ADR-0037 — an HTTP download route beside the WebSocket

- **Status:** accepted (2026-08-22)
- **Amends:** `AD-1` (what may travel beside its two planes) —
  `docs/architecture.md:101`, the bulk-file-bytes amendment at `:111`. That
  amendment ends "nothing further may be added to this route without its own
  ADR", and this is that ADR. See "What this leaves to AD-1" below.
- **Extends:** [ADR-0036](0036-an-http-upload-route-beside-the-websocket.md),
  whose decision is scoped to `POST /upload/{ticket}` and whose own words are
  "exactly one documented exception, **in one direction**, for one payload
  class". This is the other direction.
- **Reads, does not change:** `AD-6`, `AD-9`, `AD-10`,
  [ADR-0026](0026-control-plane-runs-off-the-read-loop.md) (the read-loop
  invariant), [ADR-0001](0001-xterm-js-as-vt-frontend.md).
- **Related:** `nocx-9le.8`, `nocx-9le.8.1`.

## Context

`nocx-9le.8` is the reverse of upload: a file comes back from the host the
active tab is on. The control plane mints the transfer — `files.download` takes
a `bindingId` and a path, and answers with a `transferId`, a single-use ticket,
a URL, the file's base name and its size — and the bytes then travel on a
`GET /download/{ticket}`.

ADR-0036 already argued the general shape and the whole of that argument holds
here. What this document exists for is the part that does **not** simply carry
over: the direction. ADR-0036 was careful to bound its own crossing to one
direction and one payload class, and the AD-1 amendment it produced names the
upload route explicitly and forbids adding to it without a new record. So a
second route is a second crossing, however similar it looks, and this is it.

## Decision

**File bytes travelling from the tab's host to the renderer are carried by a
`GET /download/{ticket}` served by the same `http.ServeMux` that serves
`/session` and `/upload/{ticket}`. They travel on neither of AD-1's two planes,
and no msg-type is allocated on the data plane.**

The same three bounds ADR-0036 set apply unchanged, and a fourth is added by
this route existing beside the first:

1. **The control plane still owns the transfer.** `files.download`,
   `files.downloadCancel`, `files.downloadProgress` and `files.downloadDone` are
   ordinary JSON-RPC methods and notifications with schemas in `contracts/`.
   Authorisation happens there, through `Registry.Acquire` re-checking that the
   binding's session belongs to the requesting connection — rule R1 of the
   upload design, unchanged, because reading a file off the wrong host is as
   wrong as writing to it. The HTTP route names no binding, no session and no
   path; it carries a body and nothing else.
2. **The route is single-purpose and ticket-addressed.** A ticket is minted by a
   `files.download` call that was already authorised on the socket, names
   exactly one pending transfer, and is claimed once.
3. **`frame.go` and `frame.ts` are untouched.** `MsgTypeMetadata` stays reserved
   for the Phase-2 Tier-B helper feed.
4. **The two routes share one ticket store and refuse each other's tickets.**
   One store rather than two, because a second would be a second TTL, a second
   bound and a second cancellation fan-out — and the day one of them was
   forgotten by session teardown, a download would go on reading a host whose
   tab had closed. The cost of sharing is that a sink ticket presented at
   `/download/` and a download ticket presented at `/upload/` are _expressible_,
   and the store therefore records each ticket's direction at mint and answers
   the wrong route exactly as it answers an unknown ticket — without claiming
   it, without retiring it, and before the claimed/finished flags are consulted,
   so the answer is no oracle for whether a live ticket happens to be the other
   direction's.

## Rationale

ADR-0036's three reasons apply, and each is **stronger** in this direction.
There is also a fourth that decides it on its own.

### 1. Contention: the collision is now head-on

ADR-0036's strongest argument was that an upload multiplexed onto the data plane
would compete with PTY traffic for one TCP stream and one read loop. It could
still say that an upload runs renderer→backend — the same way keystrokes go, and
the opposite way from bulk PTY output.

A download runs backend→renderer, which is the direction bulk PTY output runs.
The competition is not incidental, it is direct: a 400 MB download on that socket
would sit in the same outbound path as the frames a person is reading, and the
existing fairness machinery would be dividing the socket between "the terminal"
and "the file" rather than between two terminals.

### 2. Backpressure: the outbound path is deliberately lossy, and a file may not be

AD-10's credit window and `outputRing` govern exactly this direction, and their
policy is right for terminal output and fatal for a file. The refreshable
outbound queue drops frames when it is full, and the drop trips the stall notice
the renderer treats as a cue to reconnect. A dropped chunk of terminal output is
a redraw; a dropped chunk of a file is a corrupted result.

Making file bytes undroppable on that path means inventing the same missing
half ADR-0036 refused to invent, in the mirror: application-level credit in the
server→client direction that is separate from the PTY credit, an ack for it, a
chunk sequence, and a resume rule. An HTTP response gets all of it from TCP: the
handler's write blocks when the client stops reading, and that backpressure
reaches the source's copy loop as a slow `Write`.

### 3. A second codec: unchanged, and unchanged is enough

The golden-vector argument is direction-agnostic and is repeated here only so
that it is not read as having been forgotten.

### 4. A download has to become a file, and only the browser can do that part

This one has no upload counterpart and would decide the question by itself.

A browser saves an HTTP response to disk itself, streaming it, with a memory
cost that does not grow with the file. Bytes arriving as WebSocket messages
cannot be streamed to disk: a page has to accumulate them and hand the platform
one `Blob`, so a 2 GB download would need 2 GB of renderer memory before any of
it reached the disk. There is no version of that which is not worse, and there
is no API that fixes it — the File System Access API is not available in the
WKWebView the desktop app ships.

## Consequences

### The ticket is a bearer credential, and it authorises a READ

ADR-0036 said of the sink ticket that possession authorises the destination and
the bytes written to it, which is an integrity violation rather than merely a
denial of service. State this one's shape plainly, because it is a different
violation and the mitigations are chosen for it:

- **Possession authorises reading a file off somebody's server.** That is a
  confidentiality violation. A stolen ticket cannot be redirected — it names one
  already-opened file — but it can win the one-shot claim and take the bytes.
- The mitigations are the sink ticket's: 256 bits from `crypto/rand`, one claim,
  a TTL enforced by a mint-side timer, a claim event placed immediately before
  the first byte, never logged, never in an error string, and in the request
  path only because the request never leaves loopback.
- The origin is decided by the same `OriginPolicy`, **before** the ticket is
  read out of the path, so a refusal is not an oracle for a credential that
  reads somebody's file.

### The CORS story differs from the upload's, and the difference is a route

`POST /upload/{ticket}` needs a preflight, because its body carries a
`Content-Type` outside the CORS safelist. A `GET` carrying no request header
outside the safelist is a _simple_ request and a browser never preflights it.

So **there is no `OPTIONS /download/{ticket}` route**, deliberately: a route
answering a request nobody makes is a route nobody exercises, which is the
`nocx-rtg0` shape. The reply still carries the origin headers, because a page
reading this with `fetch` gets nothing at all from a cross-origin reply that
does not name it, and it additionally carries
`Access-Control-Expose-Headers: Content-Disposition, Content-Length` — without
which a cross-origin page receives the bytes and cannot read the file's name.
If a client ever needs a custom request header here, the preflight route has to
arrive with it.

### The response head cannot be revised, so the size is pinned before it is written

This is the asymmetry that governs every failure path on this route, and it is
worth writing down once.

An upload can be undone: its bytes land in a temp file, and a failure before the
promote leaves the destination exactly as it was. A download cannot. The status
line and `Content-Length` are written before the first byte, and neither they
nor the bytes can be recalled.

Two things follow. First, the size is measured by an `fstat` on the **open
handle** the backend holds for the transfer, not on the path — `files.download`
opens the file while it still holds the binding's use-guard and keeps that
handle for the transfer's life — so nothing that happens to the _name_ between
the answer and the fetch can make the declared length describe different bytes.
Second, a transfer that fails part-way is _visible_ rather than silent: the body
ends short of the `Content-Length` it declared, which every HTTP client treats as
the broken transfer it is. The authoritative account still reaches the person as
`files.downloadDone`, which carries how many bytes actually left.

The one outcome a download can cleanly undo is a failure **before** the first
byte, so the head is committed as late as possible — at the first byte that is
actually going out — which leaves a status to tell the truth with when nothing
was sent.

### The route sets its own deadline, and it is a WRITE deadline

ADR-0036 noted that the shared `http.Server` has `ReadHeaderTimeout: 0` because
`/session` is a long-lived upgrade, so every non-upgrade handler owns its own
bounds. This route inherits that and needs the mirror: a per-**write** stall
deadline, re-armed before every chunk, because an `http.ResponseWriter` blocks
for as long as the client refuses to read. Tripping it is also what unblocks a
write already in flight when the transfer is cancelled — the caller's half of
`transfer.Source.Get`'s bargain.

The header block is bounded by the same guarded listener the upload route uses,
widened to recognise this route's request line. That is not about the body: it
is because Go's server parses the complete header block before dispatching, so a
client dribbling headers for ever holds a connection nothing above the listener
can bound, and that is as true of a `GET` as of a `POST`.

### `R2` has no counterpart here, and the asymmetry is deliberate

The next reader will notice that `files.download` takes a **path** while
`files.upload` refuses one, and deserves the answer rather than having to derive
it.

`R2` — "the renderer may name the destination, it may never name the source" —
exists because a source path on the **backend's own disk** is scoped by nothing:
a renderer that could spell one could have the backend read `~/.ssh/id_ed25519`
and send it to a host of the renderer's choosing. Binding ownership proves which
terminal the caller owns and proves nothing whatever about the backend's
filesystem.

A download's path is on the **remote** host, inside a binding the caller already
owns and can already enumerate with `files.list` and read with `files.read` on
exactly the same authority. Naming it is the same authority in a different
bound, not new authority, so no ticket is minted for it — and one would be a
check that cannot fail, which the next person reads as a check. What the request
still cannot do is reach a binding it does not own, which is `R1` and is
`Registry.Acquire`'s to enforce.

What _is_ new is the bound: `files.read` is capped, buffered whole in memory and
decoded as text; a download is uncapped, streamed and never decoded. So the path
goes through the same validation `files.upload` applies to its destination —
absolute, clean, bounded, then the provider's own syntax — and the provider
refuses anything that is not a regular file, because a directory has no byte
stream and a fifo blocks.

### What this leaves to AD-1

AD-1's bulk-file-bytes amendment (`docs/architecture.md:111`) is worded around
the upload: it names `POST /upload/{ticket}`, says "the bytes of a file being
uploaded", and closes with "nothing further may be added to this route without
its own ADR". This document is that ADR, and the line now needs widening to name
both directions and both routes.

That widening is deliberately **not** done here, following the precedent
ADR-0036 set in these words: "This document was written without claiming the
authority to widen AD-1's own wording, because that was the coordinator's call
rather than a side effect of writing it." The same holds. The half of the change
that lives in `docs/architecture.md` is the coordinator's, and until it lands
AD-1 and this ADR disagree about how many routes there are — which is a thing to
fix in the open rather than quietly.

### What is unchanged

- **AD-6 survives verbatim.** The backend still never parses the terminal byte
  stream; it reads a file a person asked for.
- **AD-9 and AD-10 are untouched.** No download byte enters the replay ring or
  the credit window.
- **The data-plane codecs are frozen.** No golden vector changes, no msg-type is
  consumed, `MsgTypeMetadata` remains reserved.
- **AD-1's security invariant holds.** The route is on the same loopback-bound
  listener and is credentialled, with the credential being the ticket rather
  than the launch token.
