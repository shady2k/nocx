# Design review: capture records on the helper wire

Method note: every statement about the present tree is based on the cited file
and line. Paragraphs introduced as what I “would” do are recommendations. Where
I derive a consequence that the code does not state in words, I call it an
inference explicitly.

## 1. Is option 4 the right call?

**Yes, as the architectural endpoint. No, as a claim that a second carrier by
itself fixes this stage.**

Capture bodies are bulk data, not control messages. This repository has already
made the equivalent decision for file uploads: bulk bytes travel on another
connection specifically because sharing an interactive stream creates
head-of-line pressure (`docs/architecture.md:105-111`,
`docs/decisions/0036-an-http-upload-route-beside-the-websocket.md:118-157`). A
capture is smaller than a multi-gigabyte upload, but unlike an occasional upload
it is produced after every command. The measured 1.24 MiB empty-scrollback case
is already enough to prove that it is not naturally a small control message.

Option 4 fits the binding ADs:

- AD-6 puts the one emulator and therefore the authoritative record producer in
  the helper (`docs/architecture.md:170-174`). Moving the bytes does not move
  ownership.
- AD-4 requires one pooled SSH connection per host/account, not one SSH
  _channel_. It expressly multiplexes channels on that connection
  (`docs/architecture.md:130-134`). A second pty-less channel therefore obeys
  AD-4; opening a second SSH connection would not.
- AD-8 argues for a bulk-carrier interface selected at the composition root,
  with local-socket and SSH-channel implementations, rather than mode branches
  inside capture code (`docs/architecture.md:188-193`).
- AD-10 now says emulator ingest and the ledger are lossless
  (`docs/architecture.md:203-213`). A delivery path that drops a ledger body
  after logging cannot satisfy that.

There is reusable machinery, but the tree does **not** already have a separate
bulk carrier. A lane is an SSH channel, yet its bytes are wrapped in
`TypeChannelData` and tunnelled through the same helper connection
(`internal/helper/client/lane.go:7-15`,
`internal/helper/proto/frame.go:58-72`). Every such frame, notification,
request, response, keystroke and session frame ultimately takes the same host or
client writer lock (`internal/helper/host/host.go:360-378`,
`internal/helper/client/channels.go:289-310`). Reusing `ChannelID` on the
current connection is option 4 in name only.

I infer from the existing endpoint and lane abstractions that the practical
shape can be a second helper connection locally and a
second pty-less SSH channel remotely, still over the existing pooled SSH
connection. A second helper `Client`/`Host` pair can reuse the framing,
handshake, generation identity and channel lifecycle while getting its own read
pump and writer. It must be bound to the control connection/session by a
one-shot capability; same-UID trust or knowledge of a session id is not such a
binding.

The strongest argument against option 4 is good: the measured correctness bug
can be repaired more cheaply by compacting at the producer and inverting the
operation, so the record is an already-supported chunked response. That avoids
new cross-carrier lifecycle rules. It is sufficient as a **bounded interim for
one stage/release** if all of the following are true: the producer enforces the
retained-record bound before sending; the maximum encoded transfer is stated;
interactive latency under that maximum is measured and accepted; and failures
remain retryable rather than dropped. Option 1 alone is not sufficient, and
option 2 adds machinery to the wrong plane.

That argument loses as the long-term design because this repository already
treats bulk-vs-interactive separation as an architectural concern, capture is a
recurring producer, and reliable retry/outbox state is required whichever
direction initiates the transfer. Once that state exists, keeping the body on
the interactive carrier buys little.

One transport-level inference/qualification: two SSH channels still share one
SSH transport and one TCP
stream. Separate channel windows remove the current helper-frame/write-lock
coupling, but they do not abolish TCP head-of-line blocking or guarantee fair
scheduling. The bulk writer still needs bounded chunks and a measured/yielding
fairness policy. “A second SSH channel removes head-of-line blocking” would be
an overclaim.

## 2. What option 4 does not fix

The proposed second carrier fixes none of the following unless they are changed
separately.

**The 256 KiB policy cap survives.** It is one shared product number
(`internal/outputcap/outputcap.go:1-21`, `internal/content/policy.go:33-40`). The
coordinator applies it only after the full JSON record has crossed the helper
wire (`internal/app/helper_capture.go:255-267`). Its only reduction is deleting
`Departed` rows while preserving both grids
(`internal/app/helper_capture.go:339-392`). Consequently an 80x24 record spends
240,672 of 262,144 bytes on the two screens and leaves about 21 KiB for the
unique scrolled-away rows. The policy is therefore cutting the very information
this feature was introduced to retain.

**The separate 1 MiB artifact ceiling survives too.** `MaxArtifactBytes` is
hard-coded at 1 MiB (`internal/content/ledger.go:1155-1165`) and is enforced
transactionally over all chunks (`internal/content/ledger_sqlite.go:1267-1277`).
The cap routine admits that screens cannot be cut
(`internal/app/helper_capture.go:347-351`), and its early return leaves an
oversize record unchanged when `Departed` is empty
(`internal/app/helper_capture.go:352-355`). Thus the measured 200x50 record can
cross a perfect bulk carrier and still be refused by storage. The helper then
logs and drops the failure (`internal/helper/session/capture.go:138-141`), so
the user still gets no artifact.

**The cell encoding survives.** Every cell spells `text`, `width`, `hasText`,
`attrs`, and `underline` even at their zero values
(`internal/helper/proto/capture.go:51-71`). `omitempty` is a worthwhile emergency
reduction, not a format: dense styled screens still scale as one object per
column and eventually cross any fixed frame or artifact ceiling. The durable
format needs row/run encoding (blank runs, shared style/color tables, and
explicit grapheme widths) plus bounded compression. Compression must happen at
the producer, before both the transport and per-command retention decision;
transport-only gzip merely delivers a body the store will still reject.

**The existing 1 MiB frame bound survives if option 4 merely carries the same
reverse request on another connection.** `Host.Ask` refuses the marshalled
request before writing it (`internal/helper/host/reverse.go:62-93`). The bulk
plane needs a stream/chunk envelope, or a pull whose response uses chunking; a
different socket does not change `MaxFrameBytes`.

So the belief in the question is correct but incomplete: compression/compaction
and a source-side retention projection are required regardless, and the store's
1 MiB artifact ceiling is a third independent blocker.

## 3. Failure semantics forced by a second carrier

The control notification must be a **hint**, never the commit. Two carriers have
no cross-carrier ordering, so the implementation must be correct under all four
orders: notification first, body first, either carrier dying first, and a retry
duplicating either half.

I would require these rules:

1. At the authenticated boundary, the runtime seals an immutable record, gives
   it a stable capture id and per-incarnation sequence, and places it in a
   bounded helper outbox **before** advertising it. The identity should include
   the generation-qualified session/incarnation and interval identity; the
   settled fence nonce can remain the idempotency source, but an unfinished
   interval also needs a stable helper-side identity.
2. The control event carries only `{captureId, session/incarnation, revision,
state, encodedLength, digest}`. It may arrive before or after the body. The
   coordinator rendezvouses the two; neither alone makes an artifact available.
3. The bulk transfer is framed by id, offset/sequence, declared length and
   digest. A partial record is never passed to `CaptureOutput`. The coordinator
   acknowledges only after the complete body has validated and the canonical
   record is durably committed. Derived views may fail independently, as they
   do now (`internal/app/helper_capture.go:305-335`), but artifact availability
   cannot precede the canonical commit.
4. The helper retains the outbox entry until that durable acknowledgement.
   Timeout, cancellation, bulk-lane close and coordinator restart stop a
   transfer; they do not delete the record. Reconnection lists pending ids and
   resumes from the last acknowledged offset (or safely restarts from zero).
   Duplicate whole records are harmless through the stable artifact id.
5. Per-session order is explicit. Transfers may be interleaved fairly across
   sessions, but record `n+1` must not make a same-session gap invisible. Either
   availability is published in sequence or a missing `n` is represented as an
   explicit gap.
6. A bounded outbox may not silently evict. If the product accepts eviction,
   the coordinator must receive and persist a named “capture lost before
   commit” fact. Otherwise the implementation needs enough spool to keep the
   lossless-ledger promise. Spooling terminal content to disk on a remote host
   is a new privacy/retention decision and cannot be smuggled in as transport
   plumbing.
7. The control-plane binding used to resolve a record to its ledger entry must
   survive the same failures. Today it is an in-memory, 256-entry map
   (`internal/app/helper_capture.go:53-101`), while the entry itself is durable.
   A delayed or retried bulk record after coordinator restart therefore becomes
   `noEntry`. Persist the fence-to-entry association transactionally with the
   lifecycle projection, or make the durable ledger able to resolve it; do not
   put an unauthenticated entry id in the helper record.

The current sftp/channel implementation answers useful _within-carrier_ parts:
the open response precedes data, early data and an early close are parked across
the caller-registration race (`internal/helper/client/channels.go:95-185`);
queued data is drained before EOF (`internal/helper/client/channels.go:205-238`);
and `channel-closed` follows all data on the same wire
(`internal/helper/proto/ssh_service.go:142-152`,
`internal/helper/sshsvc/channel.go:397-434`). It also distinguishes stream end
from transport loss (`internal/helper/client/lane.go:109-151`).

It does **not** answer cross-carrier ordering, partial-record validation,
resume, durable acknowledgement, or coordinator-restart recovery. Its same-wire
ordering is exactly the property option 4 removes. Copying that lifecycle
without adding a rendezvous would introduce a race rather than reuse an answer.

The current capture delivery is specifically unsuitable as the new semantics:
it starts one goroutine per record, applies a five-second timeout, and explicitly
drops any failed ask (`internal/helper/session/capture.go:9-18,118-141`). A log
line is not a recoverable history gap, and the stored card cannot distinguish
that drop from a command that produced nothing.

## 4. Is the record the right shape?

**No. The transport exposed the size first, but the present record is not the
interval record the accepted design describes.**

The design says the record contains an opening snapshot, **ordered changes and
rows that left the screen**, and a closing snapshot
(`.internal/specs/2026-09-12-the-backend-holds-the-session-screen-design.md:331-336`).
The implementation has opening, departed rows and closing, but no ordered
changes (`internal/sessionruntime/capture.go:91-108`,
`internal/helper/proto/capture.go:111-137`). It therefore cannot represent
transient content overwritten in place, progress displays, or alternate-screen
activity that returned before the boundary. That is an inference from the
missing mutation log: two endpoints plus departed rows are not a record of the
path between them.

The two full grids are also currently waste, not merely verbose. The derived VT
and text views explicitly ignore `Opening` and concatenate only `Departed` and
all of `Closing` (`internal/captureview/captureview.go:63-101`). That means the
largest fixed part of every body is never consulted. It also means the closing
view is the whole terminal grid, including rows that predate the interval,
rather than a projection demonstrably attributable to this command. The
pre-interval inclusion is an inference from taking every closing row while
ignoring the opening rows; there is no attribution/subtraction step in the
cited function.

I would make the canonical object an **interval journal with checkpoints**:

- a header containing capture id, session incarnation, interval sequence,
  revision, state/fence, geometry changes, completeness and retention facts;
- a reference/digest to the opening checkpoint, with a full opening snapshot
  only for the first interval or after a reset/recovery boundary;
- ordered sparse row/cell runs for mutations and departures, including buffer
  switches, encoded with style/color dictionaries and blank runs;
- a compact closing checkpoint or digest plus final cursor/buffer state, with a
  periodic full checkpoint if independent replay needs one.

The previous interval's closing checkpoint is already assigned as the next
interval's opening (`internal/sessionruntime/capture.go:194-199`), so copying it
again into every record is structurally redundant. A checkpoint reference also
makes that continuity testable.

If the product does **not** want replay of the interval and wants only the final
card body, choose the simpler honest record instead: materialise exactly the
styled logical rows attributed to the interval at the helper and store those,
plus completeness/retention metadata. Do not keep two grids and call them an
interval record. The current hybrid pays snapshot cost without supplying replay
semantics or a correct card projection.

## 5. What I would do first

I would split the work.

Land the five acceptance fixes separately **only if they are independently
correct and useful**; I did not inspect enough context to verify that premise.
Do not accept or merge the capture stage as a working feature. The 200x50 case
is a deterministic no-artifact path, not a performance follow-up, and the
record/view mismatch above is more fundamental than the carrier.

Make the capture stage depend on a dedicated delivery stage, but define that
stage more narrowly than “add a socket”: it must include the stable capture id,
bounded retryable outbox, durable entry binding, source-side compact
representation/cap, integrity framing, and a committed acknowledgement. The
first implementation may use option 3 on the current carrier to prove the
record and recovery state machine; moving that same bulk protocol onto the
second connection is then a carrier change rather than a second rewrite of the
domain semantics.

Holding unrelated fixes hostage to this is unnecessary. Calling the capture
stage done before this is dishonest.

## 6. Additional objections

- **Artifact availability is a revision fact.** AD-9 says cards, live frame and
  artifact availability belong to one snapshot revision
  (`docs/architecture.md:195-201`). A control event saying “closed” while the
  body is pending on another lane needs an explicit `pending` availability
  state; otherwise attach can again produce a card/body torn read.
- **The current cap marks the wrong fact.** When screens alone exceed the
  256 KiB policy, `TruncCap` says the output was summarised even though the
  stored body may itself exceed the policy (`internal/app/helper_capture.go:339-392`).
  Retention must be applied to the logical interval content before encoding,
  and metadata overhead must have its own stated bound.
- **Compression needs a decompression bound and version.** A length and digest
  must cover the canonical uncompressed or compressed bytes unambiguously, and
  the receiver must refuse a declared/expanded size above its cap before
  allocating it.
- **The bulk plane needs fairness despite being separate.** On remote hosts it
  still shares one SSH/TCP transport with interactive channels. Measure echo
  latency while several maximum-size captures drain; choose chunk and window
  numbers from that result, as D14's own parking lot requires
  (`.internal/specs/2026-08-13-remote-helper-design.md:391-396`).
- **There must be an observable failure state.** “Warn and return” currently
  leaves the restored card looking like output never existed. Every terminal
  failure—policy suppression, cap summary, producer hole, transfer loss,
  corruption and store refusal—needs a distinct persisted availability reason.

My bottom line: choose option 4, but do not mistake topology for correctness.
First fix the record and the delivery state machine; then give that bounded,
retryable bulk protocol its own carrier.
