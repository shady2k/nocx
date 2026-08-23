package transport

// The upload surface on the wire: files.upload and files.uploadCancel on
// the control plane, and (from Task 6) the streamed POST that carries the
// bytes. It implements §5.3 and §5.4 of
// .internal/specs/2026-08-21-upload-to-the-active-tab-design.md.
//
// The two rules the file exists to enforce, both structural rather than
// procedural:
//
//	R1 — a file can only be uploaded to the machine the tab is on. A local
//	binding was registered with no sink (internal/capability/files.go, the
//	Uploader assertion beside the endpoint attester), so Handle.Uploader
//	refuses with *filesystem.ErrUploadUnsupported. This file asks the
//	BINDING that question and never re-derives it from the endpoint
//	attestation: the one Uploader call in handleUpload is the whole of R1's
//	enforcement here, it cannot answer anything the binding did not say,
//	and because the sink it returns is the sink the transfer then runs on,
//	the capability and the thing it authorises are one answer.
//
//	R2 — the renderer may name the destination and may never name the
//	source. filesUploadParams has no sourcePath and no source field, and
//	the params decoder runs with DisallowUnknownFields, so the guarantee is
//	the shape of a struct rather than a list of forbidden names somebody
//	has to keep current. A renderer that could spell a backend path could
//	ask the backend to read ~/.ssh/id_ed25519 and send it to a host of its
//	choosing; binding ownership proves which terminal the caller owns and
//	says nothing whatever about the backend's disk.
//
// The ticket store follows storePlan/claimPlan/finishPlan (ws.go:509-575)
// deliberately — crypto/rand, a TTL, an eviction pass, a bounded map, an
// in-progress flag — so there is one idiom for opaque server-side tokens in
// this transport rather than two.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
)

// ── bounds and lifetimes ──────────────────────────────────────────────────

const (
	// defaultUploadTicketTTL bounds an UNCLAIMED sink ticket. The renderer
	// POSTs immediately after files.upload returns, so a minute is two
	// orders of magnitude of slack; what the bound really buys is that a
	// renderer which never POSTs cannot hold a transfer and (once the sftp
	// lease is under it) a pooled SSH reference forever.
	//
	// Expiry is NOT one of the four ticket states of §5.4: the mint-side
	// timer drops the ticket and cancels the transfer it named AT THAT
	// MOMENT, so a late POST finds an unknown ticket. That is what keeps
	// 410 meaning exactly one thing — "this names nothing" — instead of
	// also meaning "cancel what it names".
	defaultUploadTicketTTL = 60 * time.Second

	// maxTransferTickets and maxTransfers bound the two maps. Both are
	// far above the product's one-transfer-at-a-time-per-binding rule (§4)
	// and exist so a client that mints without ever finishing cannot grow
	// the server's memory without limit.
	maxTransferTickets = 64
	maxTransfers       = 128

	// maxRetainedDone bounds the files.uploadDone notifications one
	// session may accumulate while nothing is attached to it. Retention is
	// what stops a terminal outcome being lost across a reconnect, and a
	// bound is what stops it becoming an unbounded queue keyed by a session
	// nobody ever comes back to.
	//
	// 64 is maxTransferTickets, and deliberately the same number: a session
	// can only accumulate a done for a transfer it started, tickets are
	// what starting one costs, and §4 limits the product to one transfer at
	// a time per binding — so this is two orders of magnitude above the
	// behaviour it has to survive and still a hard ceiling. Overflow drops
	// the OLDEST: the outcomes a returning person is still looking at are
	// the recent ones, and a queue that discarded the newest would answer
	// the reconnect with ancient history.
	maxRetainedDone = 64

	// transferDoneRetention is how long a finished transfer stays in the
	// registry after it ends. It is what makes files.uploadCancel
	// idempotent over a transfer that has already finished, and what lets
	// the §5.4 table tell "claimed, finished" apart from "unknown" instead
	// of collapsing both into a bare miss.
	transferDoneRetention = 5 * time.Minute

	// transferHeaderTimeout and defaultTransferStallTimeout are this route's OWN
	// deadlines. The shared http.Server keeps ReadHeaderTimeout: 0
	// deliberately, because /session is a long-lived upgrade (ws.go), so
	// nothing above this handler bounds a request that goes quiet.
	//
	// What each one actually covers, stated rather than implied. Go's server
	// parses the COMPLETE header block before dispatching, so a request whose
	// headers never end does not reach this handler and nothing inside it can
	// bound the wait; that interval belongs to uploadGuardConn below, which
	// sits under the server and applies transferHeaderTimeout to it. The same
	// bound then covers handler entry until the first body read, and the
	// stall deadline — re-armed before EVERY read — is what bounds "valid
	// headers followed by silence", which is the failure the spec names:
	// without it a body that stops holds a transfer, a temp file and a lease
	// open indefinitely.
	transferHeaderTimeout       = 10 * time.Second
	defaultTransferStallTimeout = 30 * time.Second

	// uploadRoutePrefix is the path a claimed body is POSTed to, and
	// uploadTicketHexLen the hex width of the ticket in it: 32 random bytes
	// from crypto/rand, comfortably past D4's 128-bit floor.
	uploadRoutePrefix = "/upload/"
	// downloadRoutePrefix is the path a claimed download is fetched from.
	// Both routes carry their one-shot ticket in the PATH and both are
	// guarded by the same OriginPolicy; see ws_download.go for why the
	// bytes travel over HTTP in this direction too, and why the argument
	// is stronger here than D3's was for the upload.
	downloadRoutePrefix = "/download/"
	uploadTicketHexLen  = 64

	// sourceTicketHexLen is the hex width of a SOURCE ticket, which is a
	// different credential of a different width: sourceTicketBytes = 16,
	// D4's floor exactly (ws_upload_source.go). Naming it separately is
	// the point — validateFilesUploadRaw held every sourceTicket to the
	// SINK ticket's 64, so no ticket the mint could ever produce passed
	// the validator, and the refusal arrived before any handler could have
	// redeemed one.
	sourceTicketHexLen = 2 * sourceTicketBytes

	// uploadUnwindTimeout bounds how long files.close and session teardown
	// wait for a CANCELLED transfer to unwind (D8). It is not a wait for
	// the upload: the transfer's context is cancelled and its body closed
	// first, so what is being waited for is one lane call plus the sink's
	// cleanup. The wait expiring is logged and the close proceeds — and it
	// can proceed, because a running transfer holds no use-guard for the
	// close to drain (Handle.Uploader).
	uploadUnwindTimeout = 5 * time.Second
)

// The terminal states a transfer reports. They are §5.3's files.uploadDone
// outcome enum; the notification that carries them is Task 7's, and this
// file is what puts a transfer into one of them.
const (
	uploadStateWritten   = "written"
	uploadStateSkipped   = "skipped"
	uploadStateCancelled = "cancelled"
	uploadStateFailed    = "failed"
)

// ── wire shapes (contracts/files.upload*.schema.json) ─────────────────────

// filesUploadParams is the whole of what a renderer may say about an
// upload. Read the field list as the enforcement of R2: there is no
// sourcePath, there is no source discriminator, and the decoder refuses
// anything not named here.
type filesUploadParams struct {
	BindingID string `json:"bindingId"`
	DestDir   string `json:"destDir"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	// SourceTicket names a file on the BACKEND's disk that a human already
	// chose, in the native picker or by dropping it on the window. It is
	// minted backend-side and opaque: the renderer can echo one but cannot
	// author one, which is the difference between it and a path. A request
	// carrying one is a PATH upload and sends no body; one without is a
	// stream upload and gets a url (§5.3). An id nothing minted is refused
	// rather than silently treated as a stream upload, which would change
	// what the caller asked for.
	SourceTicket string `json:"sourceTicket,omitempty"`
	// OnExists is the person's collision decision, absent until they have
	// been asked.
	OnExists string `json:"onExists,omitempty"`
}

// filesUploadCollision is the first of §5.3's three outcomes: the
// destination name is taken and nobody has decided what to do about it.
// Nothing was created and no transfer exists.
type filesUploadCollision struct {
	Collision string `json:"collision"` // "exists"
}

// filesUploadStarted is the second outcome: the transfer exists and needs
// no body from the renderer — either because a source ticket named the
// bytes, or because the decision was Skip and there are no bytes to move.
type filesUploadStarted struct {
	TransferID string `json:"transferId"`
}

// filesUploadStream is the third outcome: the sink is waiting for a body.
// The ticket is the credential that authorises the POST (D4) — possession
// authorises both the destination and the bytes written to it — so it is
// never logged and never appears in an error string.
type filesUploadStream struct {
	TransferID string `json:"transferId"`
	Ticket     string `json:"ticket"`
	URL        string `json:"url"`
}

type filesUploadCancelParams struct {
	TransferID string `json:"transferId"`
}

// filesTransferProgressParams is the files.uploadProgress notification AND
// the files.downloadProgress one (contracts/files.uploadProgress.schema.json,
// contracts/files.downloadProgress.schema.json). One SHAPE for both
// directions because the question is one question — how far has this
// transfer got — and two structs saying {transferId, bytes, total} would be
// two places for the answer to drift; two METHODS because the surfaces that
// draw them differ and a renderer must be able to tell which row moved.
//
// It is an INDICATOR and not a ledger: it is addressed to the binding's session's current subscriber,
// resolved at emit time, and dropped when there is none, so nothing may be
// derived from having seen one. Total is the size declared at mint time,
// carried on every frame so a renderer that missed every earlier one can
// still draw a bar from a single notification — the size the renderer
// declared for an upload, or the size measured on the open handle for a
// download.
type filesTransferProgressParams struct {
	TransferID string `json:"transferId"`
	Bytes      int64  `json:"bytes"`
	Total      int64  `json:"total"`
}

// filesUploadDoneParams is the files.uploadDone notification
// (contracts/files.uploadDone.schema.json) — the transfer's terminal
// account, and the one thing on this surface that may not be lost.
//
// Stranded is a LIST and never a single field, because the sink's promote
// fallback can leave two paths behind at once: the upload temp and the
// backup it took of the destination it was about to replace. Flattening it
// to one name would tell a person about one of the two files on their disk
// and leave the other unmentioned. It is always an array on the wire, empty
// rather than absent or null — the same defect class as vault.status's
// `providers` marshalling as null, which would throw on the renderer's
// first .map.
type filesUploadDoneParams struct {
	TransferID string   `json:"transferId"`
	Outcome    string   `json:"outcome"`
	FinalName  string   `json:"finalName"`
	Error      string   `json:"error,omitempty"`
	Stranded   []string `json:"stranded"`
}

// ── validators ────────────────────────────────────────────────────────────

// maxUploadNameRunes bounds the destination's file name. An OS file name
// component is limited to 255 bytes and every server we can reach agrees,
// so this is the same ceiling maxFileNameRunes already names for the save
// dialog's suggestion.
const maxUploadNameRunes = maxFileNameRunes

// decodeParamsStrict decodes one method's params object and REFUSES any
// field the struct does not declare.
//
// This is R2's enforcement and the reason files.upload does not use
// decodeParams. A tolerant decoder would accept {"sourcePath":"/etc/shadow"}
// and quietly drop it, which reads as "the field is ignored" and is one
// refactor away from being read. With the strict decoder the guarantee is a
// property of the struct: the only way to make a source path expressible is
// to add a field for it, which is a diff somebody has to defend.
func decodeParamsStrict(raw json.RawMessage, out any) string {
	if len(raw) == 0 {
		return "params are required"
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		const unknownField = "json: unknown field "
		if name, ok := strings.CutPrefix(err.Error(), unknownField); ok {
			return "unknown parameter " + name + ": this method names the destination and never the source"
		}
		return "params must be a JSON object"
	}
	return ""
}

// validateUploadName checks that name is exactly ONE path component in
// either provider's syntax. It runs BEFORE the handler, so every rejection
// here is -32602 before anything is stat'd.
//
// Both separators are refused regardless of the host we are running on: the
// destination is the machine the TAB is on, which may be a Windows SFTP
// server while the backend is macOS, and a name the local os package would
// happily treat as one component is two components there.
func validateUploadName(name string) string {
	if name == "" {
		return "name is required"
	}
	if strings.ContainsAny(name, `/\`) {
		return "name must be exactly one path component (no / and no \\)"
	}
	if name == "." || name == ".." {
		return `name must not be "." or ".."`
	}
	if strings.ContainsRune(name, 0) {
		return "name must not contain a NUL byte"
	}
	if utf8.RuneCountInString(name) > maxUploadNameRunes {
		return fmt.Sprintf("name exceeds %d characters", maxUploadNameRunes)
	}
	return ""
}

// validateUploadDecision checks the optional collision decision against the
// closed set the sink accepts. Absent is legitimate and means "nobody has
// been asked yet" — the handler answers collision:"exists" rather than
// choosing on the person's behalf.
func validateUploadDecision(d string) string {
	switch transfer.Decision(d) {
	case "", transfer.Overwrite, transfer.KeepBoth, transfer.Skip:
		return ""
	}
	return `onExists must be one of "overwrite", "keepBoth" or "skip"`
}

func validateFilesUploadRaw(raw json.RawMessage) string {
	var p filesUploadParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	if msg := validateFSPath(p.DestDir, "destDir"); msg != "" {
		return msg
	}
	if msg := validateUploadName(p.Name); msg != "" {
		return msg
	}
	if p.Size < 0 {
		return "size must not be negative"
	}
	if p.SourceTicket != "" && !isLowerHex(p.SourceTicket, sourceTicketHexLen) {
		return "sourceTicket must be the id the backend minted"
	}
	return validateUploadDecision(p.OnExists)
}

func validateFilesUploadCancelRaw(raw json.RawMessage) string {
	var p filesUploadCancelParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.TransferID, 32) {
		return "transferId is required and must be the 32-hex id the backend minted"
	}
	return ""
}

// ── the transfer registry ─────────────────────────────────────────────────

// runningTransfer is one upload the transport started. It is registered per
// session (D8) so that closing the binding, closing the session or stopping
// the server CANCELS it rather than blocking on it.
type runningTransfer struct {
	id        string
	sessionID session.ID
	bindingID string
	// dir is which way this transfer's bytes travel. It is what the shared
	// machinery below branches on, and it is deliberately the ONLY thing
	// that differs at this level: an upload waiting for a request body and
	// a download waiting for a response writer have the same identity, the
	// same session, the same one-shot ticket, the same TTL, the same
	// bounded map and — the part that made sharing them the honest answer
	// rather than the cheap one — the same cancellation fan-out from
	// files.close, session teardown and shutdown. A second registry would
	// be a second place for each of those three to remember to look, and
	// the day one of them forgot, a download would go on reading a host
	// whose tab had closed.
	dir transferDir
	// ticket is the sink ticket that names this transfer's body, empty for
	// a transfer that needs none. It is here so the ticket can be retired
	// the instant the transfer ends, and it is never logged (D4).
	ticket string
	upload transfer.Upload
	// download is the pinned source of a dirDownload transfer: an OPEN
	// handle on the far host plus the size and name measured on it, taken
	// while files.download still held the binding's use-guard. It is nil
	// for an upload.
	//
	// It is pinned at mint rather than opened at fetch for the reason
	// SourceTicketStore already gives about the other direction: a name is
	// not an identity. Between the answer to files.download and the fetch
	// that redeems it, the name can be renamed, replaced, or be a symlink
	// whose target moved — and the size the fetch declares as its
	// Content-Length would then describe different bytes from the ones it
	// sends. An open handle cannot be raced at all.
	download *transfer.Download
	// dest carries the claimed response writer from the GET handler to the
	// goroutine running the source — the mirror of body. Buffered by one,
	// for body's reason: the claim happens once, so nothing can queue
	// behind it.
	dest chan io.Writer

	ctx    context.Context
	cancel context.CancelFunc
	// body carries the claimed request body from the POST handler to the
	// goroutine running the sink. Buffered by one: the claim happens once,
	// so nothing can ever queue behind it.
	body chan io.ReadCloser
	// source is the reader for a PATH upload — the file a source ticket
	// named, opened on the backend's own disk (design D1). It is set
	// before the goroutine starts and is the reason such a transfer waits
	// for nothing: the bytes are already reachable, so there is no ticket,
	// no POST and no body channel. Exactly one of source and ticket is
	// ever set, and runUpload closes whichever reader it ends up with.
	source io.ReadCloser
	done   chan struct{}
	// progressWake is the transfer's progress mailbox: one slot, written
	// without blocking, read by the transfer's progress emitter. It carries
	// no value — the emitter reads the LATEST byte count off rt.bytes — so
	// a burst of chunks collapses into one notification instead of one per
	// chunk. See progress and emitTransferProgress.
	progressWake chan struct{}

	mu      sync.Mutex
	closer  io.Closer // the claimed body, so cancellation can unblock a stalled Read
	bytes   int64
	state   string
	outcome transfer.Outcome
	err     error
	endedAt time.Time
}

// transferDir is which way a transfer's bytes travel.
type transferDir int

const (
	dirUpload   transferDir = iota // renderer → the tab's host
	dirDownload                    // the tab's host → renderer
)

// size is the number of bytes this transfer is framed for: the declared
// upload size, or the size measured on the download's OPEN handle. It is
// what the ticket is minted against and what the progress notification
// carries as its total.
func (rt *runningTransfer) size() int64 {
	if rt.dir == dirDownload {
		if rt.download == nil {
			return 0
		}
		return rt.download.Size
	}
	return rt.upload.Size
}

// stop cancels the transfer AND unblocks its reader.
//
// The second half is not optional and it is this caller's to honour.
// transfer.Sink.Put documents it: an io.Reader has no context, so Put
// checks cancellation between chunks and can never abandon a Read already
// in flight. Cancelling the context alone therefore leaves a stalled body
// holding a temp file and a lease open indefinitely. Closing the body is
// what makes the cancellation arrive.
func (rt *runningTransfer) stop() {
	rt.cancel()
	rt.mu.Lock()
	c := rt.closer
	rt.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// attach hands the claimed request body to the running goroutine and
// records it so stop can unblock it. It reports false when the transfer
// ended between the claim and the hand-off.
func (rt *runningTransfer) attach(body io.ReadCloser) bool {
	rt.mu.Lock()
	rt.closer = body
	rt.mu.Unlock()
	select {
	case rt.body <- body:
		return true
	case <-rt.done:
		return false
	}
}

// progress records the running byte total and wakes the emitter.
//
// This runs on the SINK's copy loop, once per chunk, so it must cost
// nothing and must never block: a 256 KiB chunk on a fast local link comes
// round thousands of times a second, and marshalling a frame per chunk
// would fill the connection's refreshable queue (outbound.DefaultQueueDepth
// = 256), which does not merely drop frames — it trips the stall notice the
// renderer treats as a cue to reconnect. So the byte count is stored and a
// single-slot mailbox is poked without blocking. At most one progress
// notification per transfer is ever outstanding: while the emitter is
// building one, every further chunk overwrites rt.bytes and finds the slot
// already full, and the one notification that eventually goes out carries
// the newest count rather than the oldest.
func (rt *runningTransfer) progress(total int64) {
	rt.mu.Lock()
	rt.bytes = total
	rt.mu.Unlock()
	select {
	case rt.progressWake <- struct{}{}:
	default:
		// A wake is already pending and will read the count just stored.
		// This is the coalescing, and it is why the copy loop never waits.
	}
}

// finish records the terminal state. It is called exactly once, before
// done is closed, so anything that observes done sees a settled transfer.
func (rt *runningTransfer) finish(state string, out transfer.Outcome, err error, now time.Time) {
	rt.mu.Lock()
	rt.state = state
	rt.outcome = out
	rt.err = err
	rt.endedAt = now
	rt.mu.Unlock()
}

// snapshot reads the transfer's settled fields.
func (rt *runningTransfer) snapshot() (state string, out transfer.Outcome, bytes int64, err error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.state, rt.outcome, rt.bytes, rt.err
}

// transferTicket is one minted sink ticket. The three flags are the four
// states of §5.4: absent from the map is "unknown", finished is "claimed
// and over", claimed alone is "claimed and running", and neither is
// "minted, unclaimed".
type transferTicket struct {
	transferID string
	// dir is the direction of the transfer this ticket names, recorded at
	// mint so the route check can happen on the TICKET rather than on the
	// transfer it resolves to. That ordering is the point: checking it
	// after the claimed/finished flags would answer a sink ticket
	// presented at the download route with 409 rather than 410, which
	// tells the caller the ticket exists — an oracle for a credential that
	// reads somebody's file.
	dir       transferDir
	size      int64
	createdAt time.Time
	claimed   bool
	finished  bool
	timer     *time.Timer
}

// transferRegistry holds the transport's running transfers and the tickets
// that name them. The zero value is usable.
type transferRegistry struct {
	mu      sync.Mutex
	running map[string]*runningTransfer
	tickets map[string]*transferTicket
	// retained holds the files.uploadDone notifications a session could
	// not be told about because nothing was attached, in the order they
	// happened, flushed on the next attach and cleared as each one is
	// delivered. It is the half of the files.changed precedent that
	// current-subscriber addressing alone does not give (ws_files.go:939),
	// and the half that loses TERMINAL outcomes: a dropped uploadDone
	// leaves the UI saying "uploading" for the rest of the session.
	//
	// Keyed by session because that is what a reattach names, and because
	// it is the lifetime that bounds the map: the entries of a session
	// that ends are dropped with it (forgetDone), so a key can only exist
	// while something can still attach to it.
	retained map[session.ID][]retainedDone

	// ttl/ttlSet is the unclaimed-ticket TTL. ttlSet exists so that a test
	// can ask for zero — expire as soon as the timer goroutine can run —
	// without that being indistinguishable from "unset".
	ttl    time.Duration
	ttlSet bool

	// stall is the per-read deadline on a claimed body; zero means the
	// default.
	stall time.Duration

	// header is how long a connection may take to finish an upload's header
	// block; zero means the default. Read at Start, when the guarded
	// listener is built.
	header time.Duration

	// unwind is how long teardown waits for a cancelled transfer; zero
	// means the default. A test shortens it in place (ws_upload_test.go),
	// the way filesPollInterval is shortened: an exported option would be
	// a knob production has no reason to turn.
	unwind time.Duration

	// now is the clock, injectable for the eviction tests.
	now func() time.Time
}

func (u *transferRegistry) stallTimeout() time.Duration {
	if u.stall > 0 {
		return u.stall
	}
	return defaultTransferStallTimeout
}

func (u *transferRegistry) headerDeadline() time.Duration {
	if u.header > 0 {
		return u.header
	}
	return transferHeaderTimeout
}

func (u *transferRegistry) unwindTimeout() time.Duration {
	if u.unwind > 0 {
		return u.unwind
	}
	return uploadUnwindTimeout
}

func (u *transferRegistry) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

func (u *transferRegistry) ticketTTL() time.Duration {
	if u.ttlSet {
		return u.ttl
	}
	return defaultUploadTicketTTL
}

// sweepLocked drops transfers that finished long enough ago to be beyond
// any caller's interest, and then the tickets that named them.
//
// The two are swept together on purpose. An UNCLAIMED ticket dies on its
// own mint-side timer, which is the enforceable event §5.4 closes the TTL
// at; a ticket that was claimed and whose transfer then finished has no
// timer left and is deliberately RETAINED, so a second POST is told 410
// ("claimed, finished") rather than 409. Retained forever it would fill the
// bound, so its closing event is its transfer's eviction: a ticket whose
// transfer is gone names nothing and goes.
func (u *transferRegistry) sweepLocked(now time.Time) {
	for id, rt := range u.running {
		rt.mu.Lock()
		ended, over := rt.endedAt, rt.state != ""
		rt.mu.Unlock()
		if over && now.Sub(ended) > transferDoneRetention {
			delete(u.running, id)
		}
	}
	for ticket, e := range u.tickets {
		if _, ok := u.running[e.transferID]; !ok {
			if e.timer != nil {
				e.timer.Stop()
			}
			delete(u.tickets, ticket)
		}
	}
}

// add registers a running transfer. It refuses once the map is full rather
// than growing without limit.
func (u *transferRegistry) add(rt *runningTransfer) error {
	now := u.clock()
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running == nil {
		u.running = make(map[string]*runningTransfer)
	}
	u.sweepLocked(now)
	if len(u.running) >= maxTransfers {
		return errors.New("too many transfers in flight")
	}
	u.running[rt.id] = rt
	return nil
}

// remove drops a transfer that never started — the mint failed after it was
// registered, so nothing will ever settle it.
func (u *transferRegistry) remove(transferID string) {
	u.mu.Lock()
	delete(u.running, transferID)
	u.mu.Unlock()
}

func (u *transferRegistry) get(transferID string) *runningTransfer {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.running[transferID]
}

// mintTicket mints the sink ticket for a transfer and arms its expiry.
//
// The timer is the whole of the TTL rule: when it fires on a ticket nobody
// claimed, the ticket is forgotten AND the transfer it named is cancelled,
// in that order and at that moment. So a POST that arrives afterwards finds
// an unknown ticket and is told 410 — which never has to mean "cancel what
// this names", because expiry already did.
func (u *transferRegistry) mintTicket(rt *runningTransfer) (string, error) {
	var buf [32]byte // 256 bits, comfortably past D4's 128-bit floor
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("upload ticket: %w", err)
	}
	ticket := hex.EncodeToString(buf[:])
	now := u.clock()

	u.mu.Lock()
	if u.tickets == nil {
		u.tickets = make(map[string]*transferTicket)
	}
	u.sweepLocked(now)
	if len(u.tickets) >= maxTransferTickets {
		u.mu.Unlock()
		return "", errors.New("too many upload tickets outstanding")
	}
	u.tickets[ticket] = &transferTicket{transferID: rt.id, dir: rt.dir, size: rt.size(), createdAt: now}
	u.mu.Unlock()

	// Armed outside the lock and recorded back under it, because a zero
	// TTL fires before this line runs: expire takes the same lock, so the
	// entry it deleted is simply not there to record against.
	timer := time.AfterFunc(u.ticketTTL(), func() { u.expire(ticket) })
	u.mu.Lock()
	if e, ok := u.tickets[ticket]; ok {
		e.timer = timer
	} else {
		timer.Stop()
	}
	u.mu.Unlock()
	return ticket, nil
}

// expire runs on the mint-side timer. An unclaimed ticket is forgotten and
// the transfer it named is cancelled; a claimed one is left alone, because
// the claim is the event the TTL closes at (§5.4).
func (u *transferRegistry) expire(ticket string) {
	u.mu.Lock()
	e, ok := u.tickets[ticket]
	if !ok || e.claimed {
		u.mu.Unlock()
		return
	}
	delete(u.tickets, ticket)
	rt := u.running[e.transferID]
	u.mu.Unlock()
	if rt != nil {
		rt.stop()
	}
}

// The answers claim can give. The first four are §5.4's table, one per row;
// the last two are the Content-Length rule, which is checked in the same
// critical section so that "before the body is read" is not a window
// somebody has to keep closed but a property of the lock.
type transferClaim int

const (
	transferClaimUnknown      transferClaim = iota // never minted, or already forgotten → 410
	transferClaimOK                                // minted, unclaimed → the body is read
	transferClaimRunning                           // claimed, transfer still running → 409
	transferClaimFinished                          // claimed, transfer finished → 410
	transferClaimNoLength                          // no Content-Length → 400
	transferClaimSizeMismatch                      // Content-Length disagrees with the declared size → 400
)

// claim resolves a ticket to its transfer and marks it taken.
//
// contentLength is the request's declared length, or -1 when the header was
// absent. A request that fails either length rule does NOT claim: the
// ticket stays unclaimed and its timer keeps running, so a malformed
// request cannot burn somebody's one-shot credential.
//
// The claim is the enforceable event the TTL closes at (§5.4), so a
// successful one also disarms the mint-side timer.
func (u *transferRegistry) claim(ticket string, dir transferDir, contentLength int64) (*runningTransfer, transferClaim) {
	u.mu.Lock()
	defer u.mu.Unlock()
	e, ok := u.tickets[ticket]
	if !ok {
		return nil, transferClaimUnknown
	}
	if e.dir != dir {
		// One map holds both directions' tickets, so a sink ticket
		// presented at /download/ and a download ticket presented at
		// /upload/ are expressible and must not be honoured: the first
		// would hand a caller the bytes of a file it only had permission
		// to overwrite, and the second would let a caller write into a
		// transfer that was only ever authorised to read.
		//
		// On the wrong route a ticket names nothing, and it is answered
		// exactly as an unknown one is — the ticket is NOT claimed and NOT
		// retired, so a misrouted request cannot burn somebody's one-shot
		// credential. It is checked FIRST, before claimed and finished,
		// so the answer is no oracle for whether a live ticket happens to
		// be the other direction's.
		return nil, transferClaimUnknown
	}
	if e.finished {
		return nil, transferClaimFinished
	}
	if e.claimed {
		return nil, transferClaimRunning
	}
	rt := u.running[e.transferID]
	if rt == nil {
		// The ticket outlived the transfer it named, so it names nothing —
		// which is exactly "unknown", not a fifth state.
		delete(u.tickets, ticket)
		return nil, transferClaimUnknown
	}
	// The Content-Length rules are the UPLOAD's, because they are about a
	// body the claimant is sending. A download's fetch carries no body and
	// declares no length; the length in that direction is the server's to
	// state, from the size measured on the pinned handle, and the claimant
	// has nothing to agree or disagree with.
	if rt.dir == dirUpload {
		if contentLength < 0 {
			return nil, transferClaimNoLength
		}
		if contentLength != e.size {
			return nil, transferClaimSizeMismatch
		}
	}
	e.claimed = true
	if e.timer != nil {
		e.timer.Stop()
	}
	return rt, transferClaimOK
}

// retireTicket marks a ticket's transfer over. An unclaimed ticket is
// forgotten outright — nothing may claim a transfer that has ended — and a
// claimed one is kept as "finished" so a second POST is told 410 rather
// than 409.
func (u *transferRegistry) retireTicket(ticket string) {
	if ticket == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	e, ok := u.tickets[ticket]
	if !ok {
		return
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	if !e.claimed {
		delete(u.tickets, ticket)
		return
	}
	e.finished = true
}

// cancel stops one transfer by id and reports whether it existed.
func (u *transferRegistry) cancel(transferID string) bool {
	rt := u.get(transferID)
	if rt == nil {
		return false
	}
	rt.stop()
	return true
}

// pick returns the transfers matching a predicate.
func (u *transferRegistry) pick(match func(*runningTransfer) bool) []*runningTransfer {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []*runningTransfer
	for _, rt := range u.running {
		if match(rt) {
			out = append(out, rt)
		}
	}
	return out
}

// ── retained terminal outcomes ───────────────────────────────────────────

// retainedDone is one terminal outcome waiting for somebody to attach: the
// notification's method name and its already-marshalled params.
//
// It holds the marshalled form rather than a typed struct because the two
// directions send different shapes on different methods, and the retention
// rule is about neither — it is "the outcome of a transfer must not be lost
// because nobody was listening when it ended", which is the same sentence
// for both. Keeping one queue per session, in the order things happened,
// is also what makes a reconnect replay them in that order rather than in
// two independent orders that interleave arbitrarily.
type retainedDone struct {
	method string
	params json.RawMessage
}

// retainDone keeps one files.uploadDone for a session that has nothing
// attached. It reports whether the bound evicted an older entry, so the
// caller can say so out loud — a retention that silently forgets is a
// retention nobody can trust.
func (u *transferRegistry) retainDone(sid session.ID, p retainedDone) (evicted bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.retained == nil {
		u.retained = make(map[session.ID][]retainedDone)
	}
	q := append(u.retained[sid], p)
	if len(q) > maxRetainedDone {
		q = q[len(q)-maxRetainedDone:]
		evicted = true
	}
	u.retained[sid] = q
	return evicted
}

// popDone takes the OLDEST retained outcome for a session, so the flush
// delivers them in the order they happened. It is removed by the take, and
// pushBackDone is what returns it when the delivery failed — "cleared on
// delivery" is a claim only if nothing is cleared before one.
func (u *transferRegistry) popDone(sid session.ID) (retainedDone, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	q := u.retained[sid]
	if len(q) == 0 {
		return retainedDone{}, false
	}
	p := q[0]
	if len(q) == 1 {
		delete(u.retained, sid)
	} else {
		u.retained[sid] = q[1:]
	}
	return p, true
}

// pushBackDone returns an outcome to the FRONT of the session's queue after
// a delivery that failed, keeping the order the transfers finished in.
func (u *transferRegistry) pushBackDone(sid session.ID, p retainedDone) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.retained == nil {
		u.retained = make(map[session.ID][]retainedDone)
	}
	u.retained[sid] = append([]retainedDone{p}, u.retained[sid]...)
}

// forgetDone drops a session's retained outcomes. Called from the session
// teardown path: once the session is gone nothing can ever attach to it, so
// keeping its outcomes is a leak and not a courtesy.
func (u *transferRegistry) forgetDone(sid session.ID) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.retained, sid)
}

// forgetAllDone drops every session's retained outcomes — server shutdown.
func (u *transferRegistry) forgetAllDone() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.retained = nil
}

// ── the D8 teardown path ─────────────────────────────────────────────────

// cancelTransfersFor cancels a set of transfers — uploads and downloads
// alike — and waits, BOUNDED, for them to unwind. It returns whether every one of them ended within the bound.
//
// This is D8, and the bound is only honest because of what a running
// transfer does NOT hold. It holds the binding's sink and no use-guard:
// Handle.Uploader takes one for the hand-off and drops it there, so
// Binding.close drains whatever the synchronous calls are holding and never
// waits on a transfer. The wait below is therefore a courtesy — cancel,
// give the sink up to the unwind bound to notice, then close regardless —
// and "regardless" is a thing this code can actually do rather than a
// comment over an unbounded wait.
//
// A transfer that outlives the bound is not abandoned to write into a
// closed binding, either: closing the provider closes the lease under the
// sink, which unblocks the call in flight and fails every later one.
func (s *WSServer) cancelTransfersFor(transfers []*runningTransfer) bool {
	if len(transfers) == 0 {
		return true
	}
	for _, rt := range transfers {
		rt.stop()
	}
	bound := s.transfers.unwindTimeout()
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	for _, rt := range transfers {
		select {
		case <-rt.done:
		case <-deadline.C:
			s.log.Warn("upload did not unwind within the bound; closing anyway",
				"transfer_id", rt.id, "binding_id", rt.bindingID, "maxWait", bound)
			return false
		}
	}
	return true
}

// cancelBindingTransfers cancels every transfer of one binding — files.close.
func (s *WSServer) cancelBindingTransfers(bid string) {
	s.cancelTransfersFor(s.transfers.pick(func(rt *runningTransfer) bool { return rt.bindingID == bid }))
}

// cancelSessionTransfers cancels every transfer of one session — the terminal
// closing, which closes its bindings (spec §5.1).
// The retained outcomes go with the session, and AFTER the cancels: a
// transfer cancelled here settles and retains its "cancelled" outcome on
// the way out, and keeping that for a session nobody can attach to again
// would be a map entry with no reader.
func (s *WSServer) cancelSessionTransfers(sid session.ID) {
	s.cancelTransfersFor(s.transfers.pick(func(rt *runningTransfer) bool { return rt.sessionID == sid }))
	s.transfers.forgetDone(sid)
}

// cancelAllTransfers cancels every transfer — server shutdown. Without it a
// transfer outlives the server that started it, holding a pooled SSH
// reference nothing will ever release.
func (s *WSServer) cancelAllTransfers() {
	s.cancelTransfersFor(s.transfers.pick(func(*runningTransfer) bool { return true }))
	s.transfers.forgetAllDone()
}

// ── running one transfer ─────────────────────────────────────────────────

// uploadMachine is the transport-owned upload surface a handler reaches:
// the registry and the goroutine that runs a transfer. WSServer implements
// it, and a handler is constructed with the interface, so it can reach
// exactly these operations and nothing else on the server.
type uploadMachine interface {
	// bindingSession is the session a binding this transport issued belongs
	// to; false when the binding is not one of ours. A transfer is bounded
	// by the SESSION, exactly as its binding is (spec §5.1) — never by the
	// WebSocket, which an AD-9 reconnect replaces underneath a running
	// upload.
	bindingSession(bid string) (session.ID, bool)
	// startUpload registers rt, mints its sink ticket when it needs a body,
	// and runs it on sink — the binding's write half, taken from the handle
	// during the synchronous call and detached from its use-guard (D8).
	// rt.ticket is filled in before the goroutine starts.
	startUpload(rt *runningTransfer, needsBody bool, sink transfer.Sink) error
	// uploadFor returns a transfer by id, or nil.
	uploadFor(transferID string) *runningTransfer
	// cancelUpload stops one transfer and reports whether it existed.
	cancelUpload(transferID string) bool
	// uploadURL is the path a claimed body is POSTed to.
	uploadURL(ticket string) string
}

func (s *WSServer) bindingSession(bid string) (session.ID, bool) {
	b := s.filesBindingOf(bid)
	if b == nil {
		return "", false
	}
	return b.sessionID, true
}

func (s *WSServer) uploadFor(transferID string) *runningTransfer { return s.transfers.get(transferID) }

func (s *WSServer) cancelUpload(transferID string) bool { return s.transfers.cancel(transferID) }

func (s *WSServer) uploadURL(ticket string) string { return uploadRoutePrefix + ticket }

// startUpload registers the transfer BEFORE minting its ticket, and that
// order is the fix for a real race rather than a preference: the mint arms
// an expiry timer that cancels "the transfer this ticket names", and with a
// short TTL it can fire before the registration. A timer that ran first
// would find no transfer, forget the ticket and cancel nothing, leaving a
// registered transfer waiting for a body no request could ever carry.
// Registered first, the worst case is a cancel that lands before the
// goroutine starts — which the goroutine then observes as a cancelled
// context, which is correct.
func (s *WSServer) startUpload(rt *runningTransfer, needsBody bool, sink transfer.Sink) error {
	if err := s.registerAndMint(rt, needsBody); err != nil {
		return err
	}
	go s.emitTransferProgress(rt)
	go s.runUpload(rt, sink)
	return nil
}

// registerAndMint is the register-then-mint order both directions need, in
// one place because the race it fixes is one race and not two.
func (s *WSServer) registerAndMint(rt *runningTransfer, needsTicket bool) error {
	if err := s.transfers.add(rt); err != nil {
		return err
	}
	if !needsTicket {
		return nil
	}
	ticket, err := s.transfers.mintTicket(rt)
	if err != nil {
		s.transfers.remove(rt.id)
		return err
	}
	rt.ticket = ticket
	return nil
}

// runUpload is the transfer's own goroutine. It waits for a body when one
// is expected, writes it through the binding's sink, and settles the
// transfer's terminal state before anything can observe it as over.
//
// It holds the sink and nothing else. The handle it came from, and the
// use-guard that handle carried, were both let go when files.upload
// answered (D8) — which is what makes files.close, session teardown and
// shutdown able to cancel, wait a bounded moment and then close for real.
func (s *WSServer) runUpload(rt *runningTransfer, sink transfer.Sink) {
	defer close(rt.done)

	// A path upload already holds its reader; only a stream upload waits.
	body := rt.source
	if body == nil && rt.ticket != "" {
		select {
		case body = <-rt.body:
		case <-rt.ctx.Done():
			rt.finish(uploadStateCancelled, transfer.Outcome{}, rt.ctx.Err(), s.transfers.clock())
			s.transfers.retireTicket(rt.ticket)
			s.settleUpload(rt)
			return
		}
	}

	out, err := sink.Put(rt.ctx, rt.upload, body, rt.progress)
	if body != nil {
		_ = body.Close()
	}
	rt.finish(uploadStateOf(out, err, rt.ctx.Err()), out, err, s.transfers.clock())
	s.transfers.retireTicket(rt.ticket)
	// Before the deferred close(rt.done), on every path: anything that
	// observes a transfer as over — the POST handler, the teardown wait, a
	// test — must find the terminal outcome already delivered or already
	// retained, never in flight behind it.
	s.settleUpload(rt)
	if err != nil {
		// The reason and the stranded paths reach the person through
		// files.uploadDone (Task 7); the log is for the operator, and it
		// carries neither the ticket nor the bytes.
		s.log.Warn("upload did not complete",
			"transfer_id", rt.id, "binding_id", rt.bindingID,
			"stranded", len(out.Stranded), "error", err)
	}
}

// uploadStateOf maps the sink's return onto §5.3's outcome enum. A
// cancelled transfer is not a failure and must not be reported as one: the
// person pressed cancel, or the binding went away underneath them.
func uploadStateOf(out transfer.Outcome, err, ctxErr error) string {
	switch {
	case err == nil && out.State == transfer.StateSkipped:
		return uploadStateSkipped
	case err == nil:
		return uploadStateWritten
	case errors.Is(err, context.Canceled), ctxErr != nil:
		return uploadStateCancelled
	default:
		return uploadStateFailed
	}
}

// ── the two notifications (spec §5.3) ────────────────────────────────────
//
// They are deliberately NOT symmetric, and the asymmetry is the whole of
// this section. Progress is live and lossy — current subscriber, resolved
// at emit time, dropped when there is none. The terminal outcome is
// retained when there is none and flushed on attach, which is what
// files.changed's dirty set does (ws_files.go:939, :976) and what
// current-subscriber addressing alone does not: a lost progress frame costs
// a bar that jumps, and a lost uploadDone leaves the UI saying "uploading"
// for the rest of the session about a transfer that ended ten minutes ago.

// emitTransferProgress is one transfer's progress emitter, in either
// direction: the goroutine that
// turns the mailbox rt.progress pokes into notifications. It exists so that
// the sink's copy loop never marshals, never enqueues and never waits — and
// so that at most ONE progress notification per transfer is outstanding at
// a time, whatever the link speed. It ends with the transfer.
//
// A frame may still be enqueued between the transfer settling and this loop
// observing rt.done, so a progress notification can arrive after
// files.uploadDone. That is admissible by construction rather than by luck:
// §5.5 derives in-flight state from files.upload's result and
// files.uploadDone and never from having seen a progress notification, so a
// late indicator cannot contradict a terminal outcome.
func (s *WSServer) emitTransferProgress(rt *runningTransfer) {
	for {
		select {
		case <-rt.done:
			return
		case <-rt.progressWake:
			s.notifyTransferProgress(rt)
		}
	}
}

// notifyTransferProgress sends one progress frame to whoever is attached to
// the transfer's session NOW.
//
// Everything about it is droppable, and each drop is a decision rather than
// an oversight: no session (it is being torn down), no subscriber (the
// laptop is asleep), or an enqueue the connection refused (its queue is
// full of PTY output, which is the traffic that must win). Nothing is
// retained, nothing is retried and nothing is logged — an indicator that
// announced its own losses would be a ledger with extra steps.
func (s *WSServer) notifyTransferProgress(rt *runningTransfer) {
	rx := s.getRx(rt.sessionID)
	if rx == nil {
		return
	}
	wconn, _ := rx.getSubscriber()
	if wconn == nil {
		return
	}
	_, _, sent, _ := rt.snapshot()
	method := "files.uploadProgress"
	if rt.dir == dirDownload {
		method = "files.downloadProgress"
	}
	// One params SHAPE for both directions, because the question is the
	// same one — how far has this transfer got — and two schemas that said
	// {transferId, bytes, total} in two files would be two places for the
	// answer to drift. The METHOD differs because the surfaces that draw
	// them differ and a renderer must be able to tell which row moved.
	_ = wconn.TryNotify(method, mustMarshal(filesTransferProgressParams{
		TransferID: rt.id,
		Bytes:      sent,
		Total:      rt.size(),
	}))
}

// settleUpload is everything a finished transfer owes the person: the
// terminal account, and — on a written outcome only — the invalidation that
// makes the new row appear without anybody pressing anything.
func (s *WSServer) settleUpload(rt *runningTransfer) {
	state, out, _, err := rt.snapshot()
	p := filesUploadDoneParams{
		TransferID: rt.id,
		Outcome:    state,
		FinalName:  out.FinalName,
		// Always an array, never null and never absent: a nil slice
		// marshals to null, and the renderer's first .map would throw on
		// it. Empty is the honest answer for "nothing was left behind".
		Stranded: out.Stranded,
	}
	if p.Stranded == nil {
		p.Stranded = []string{}
	}
	// The reason is carried only for a failure. A cancelled transfer's err
	// is context.Canceled, which is not a fault and must never be shown to
	// a person as one: they pressed cancel, or the binding went away.
	if state == uploadStateFailed && err != nil {
		p.Error = err.Error()
	}
	// The invalidation goes FIRST and the order is load-bearing: a renderer
	// that re-lists on either notification is never told a transfer is done
	// over a directory it has not yet been told to re-list. Both are
	// non-blocking enqueues, so nothing is delayed by the order, and the
	// two accumulation paths flush in the same order on re-attach
	// (flushFilesChanged, then flushUploadDone).
	if state == uploadStateWritten {
		s.invalidateUploadDest(rt)
	}
	s.deliverTransferDone(rt.sessionID, rt.id, retainedDone{
		method: "files.uploadDone", params: mustMarshal(p),
	})
}

// deliverUploadDone sends the terminal outcome to the session's current
// subscriber, and RETAINS it when there is none or when the send fails —
// exactly emitFilesChanged's two accumulation paths (ws_files.go:947), for
// exactly its reason: the connection that started the transfer is the one
// most likely to be gone by the time it ends.
//
// The one case that is dropped rather than retained is a session that no
// longer exists. Nothing can attach to it again, so a retained outcome for
// it would be a map entry with no possible reader — the same answer
// emitFilesChanged gives when its rx is nil.
func (s *WSServer) deliverTransferDone(sid session.ID, transferID string, done retainedDone) {
	rx := s.getRx(sid)
	if rx == nil {
		s.log.Debug("transfer outcome dropped: the session is gone",
			"transfer_id", transferID, "method", done.method)
		return
	}
	if wconn, _ := rx.getSubscriber(); wconn != nil {
		if err := wconn.TryNotify(done.method, done.params); err == nil {
			return
		}
		// The subscriber's socket is going down; a terminal outcome must
		// not go down with it.
	}
	if s.transfers.retainDone(sid, done) {
		s.log.Warn("retained transfer outcomes overflowed; the oldest was dropped",
			"session_id", sid, "max", maxRetainedDone)
	}
}

// flushUploadDone delivers the outcomes a session accumulated while nothing
// was attached, to the connection that just attached. Oldest first, and
// each one is cleared only once it has been handed to the connection — a
// failure part-way through puts the undelivered one back and leaves the
// rest for the next attach, so "cleared on delivery" stays true rather than
// becoming "cleared on the attempt".
//
// Called from handleAttach after setSubscriber, like the files.changed
// flush, so the notifications resolve to the connection that just arrived.
func (s *WSServer) flushUploadDone(sid session.ID, wconn Responder) {
	for {
		p, ok := s.transfers.popDone(sid)
		if !ok {
			return
		}
		if err := wconn.TryNotify(p.method, p.params); err != nil {
			s.transfers.pushBackDone(sid, p)
			return // the socket is dying; whatever remains stays retained
		}
	}
}

// invalidateUploadDest marks the destination directory dirty after a
// written outcome, so the file the person just sent appears in the panel
// with nobody pressing anything (spec §5.5, "Refresh").
//
// It announces through emitFilesChanged rather than inventing a second
// refresh path, which buys two things beyond one owner for the concept: a
// renderer re-lists through files.list exactly as it does for every other
// change, and an upload that completed while nothing was attached leaves
// the directory in the SAME dirty set the reconnect already flushes.
//
// Only a watched directory is announced — a path nobody is looking at has
// nothing to refresh — and the rev is absent because nothing has been
// re-listed here: making it present would mean listing a directory in order
// to say it should be listed. The poll loop may then announce the same
// directory once more when its digest moves, which costs one extra listing
// and is the safe direction the watcher already takes deliberately (a false
// positive re-lists; a false negative hides the change forever). It is also
// what makes an OVERWRITE visible at all: same name, same size, and a
// digest that need not move.
func (s *WSServer) invalidateUploadDest(rt *runningTransfer) {
	b := s.filesBindingOf(rt.bindingID)
	if b == nil || b.watcher == nil {
		return
	}
	w := b.watcher
	w.mu.Lock()
	_, watched := w.paths[rt.upload.DestDir]
	w.mu.Unlock()
	if !watched {
		return
	}
	s.emitFilesChanged(w, rt.upload.DestDir, "")
}

// ── handlers ──────────────────────────────────────────────────────────────

// uploadHandlers answers files.upload and files.uploadCancel. Like every
// files.* handler it holds the FilesystemBindingOperation and a
// transport-owned seam, never the *WSServer.
type uploadHandlers struct {
	op      capability.FilesystemBindingOperation // nil → filesystem not wired
	machine uploadMachine
	// sources redeems a source ticket. It is the mint's own store, reached
	// through the narrowest interface that can redeem one: this handler can
	// claim a ticket a human already minted and can never mint one, which
	// is R2 stated as what the handler can reach.
	sources sourceClaimer
	r       Responder
}

// sourceClaimer is the redemption half of the source-ticket mint
// (ws_upload_source.go). Claim only — a handler that could Mint would be a
// path the wire reaches, which is the whole of what R2 forbids.
type sourceClaimer interface {
	Claim(ticket string) (SourceFile, bool)
}

// handleUpload mints a transfer and starts it — and does not wait for it.
//
// It follows handleList's shape exactly (ws_files.go:539): the op.Run
// wrapper, Acquire re-checking that THIS connection owns the binding's
// session (D15) and taking the use-guard, and filesErrorCode for the
// mapping. The guard's lifetime is handleList's too, and D8 is why: it ends
// when this call answers, on every path, including the one that leaves a
// transfer running. What that transfer runs on is the binding's sink, taken
// from the handle while the guard was still held.
func (h uploadHandlers) handleUpload(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files not available"})
		return
	}
	var params filesUploadParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	err := h.op.Run(ctx, func(ctx context.Context, svc capability.FilesystemBindingService) error {
		handle, release, err := svc.Acquire(params.BindingID, state)
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(err), Message: err.Error()})
			return nil
		}
		// The guard covers this call and nothing after it (D8): what the
		// transfer runs on is the sink, which outlives the handle, so a
		// running upload is never something a close has to wait for.
		defer release()

		// R1, asked of the binding and of nothing else — and the same
		// call is what the transfer will run on, so the capability and
		// the thing it authorises can never be two different answers.
		sink, sinkErr := handle.Uploader()
		if sinkErr != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(sinkErr), Message: sinkErr.Error()})
			return nil
		}
		decision := transfer.Decision(params.OnExists)
		if decision == "" {
			// The collision question is asked before a byte moves (D5).
			// The answer is advisory — the create is the arbiter — but the
			// person is asked on this answer, and an upload that silently
			// replaced a file is the failure that rule exists for.
			taken, probeErr := uploadDestinationTaken(ctx, handle, path.Join(params.DestDir, params.Name))
			if probeErr != nil {
				_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(probeErr), Message: probeErr.Error()})
				return nil
			}
			if taken {
				_ = h.r.TryResult(req.ID, mustMarshal(filesUploadCollision{Collision: "exists"}))
				return nil
			}
			// Nothing is there. Overwrite is what the sink needs for a
			// name it does not have to resolve; the race in which the
			// destination appears between here and the create is the one
			// §6 names as accepted, and the create is still the arbiter.
			decision = transfer.Overwrite
		}

		sid, ok := h.machine.bindingSession(params.BindingID)
		if !ok {
			// Acquire succeeded, so the binding exists in the registry;
			// the transport's own bookkeeping is what is missing, and a
			// transfer with no session could never be torn down with one.
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown bindingId"})
			return nil
		}

		// The ticket is redeemed HERE and not one line earlier, and the
		// order is the whole of what makes the collision question askable
		// on this source: a claim is one-shot, so claiming before the
		// probe would burn the ticket on the very answer that asks the
		// renderer to call again. Everything that can refuse this request
		// without moving a byte has now refused it.
		var source *SourceFile
		var sourceReader io.ReadCloser
		if params.SourceTicket != "" {
			if h.sources == nil {
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sourceTicket"})
				return nil
			}
			src, ok := h.sources.Claim(params.SourceTicket)
			if !ok {
				// Unknown, already redeemed, or expired — one answer for
				// all three, because distinguishing them would tell a
				// caller which guesses were closer.
				_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sourceTicket"})
				return nil
			}
			// The ticket carries the OPEN handle, so there is nothing to
			// resolve here and no name this handler could resolve it from.
			// Ownership passes with the claim: from this line the reader
			// has exactly one owner and every path below closes it.
			//
			// R1's other half, and the one binding authorisation cannot
			// answer. Acquire has already proved this connection owns the
			// DESTINATION; a ticket minted by a window drop additionally
			// names the tab the file was dropped ON, and pairing it with a
			// binding on any other tab is the wrong pairing R1 says must
			// not be expressible. A picker ticket names no tab and is bound
			// by whichever one redeems it (SourceTicketStore.Mint says why).
			//
			// The ticket is spent either way: the gesture it named has been
			// answered, wrongly, and nothing may retry it.
			if src.Session != "" && src.Session != sid {
				_ = src.File.Close()
				_ = h.r.TryError(req.ID, RPCError{
					Code:    -32602,
					Message: "Invalid params: this file was dropped on another tab",
				})
				return nil
			}
			source = &src
			sourceReader = src.File
		}

		id, err := newTransferID()
		if err != nil {
			closeSource(sourceReader)
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		// The transfer outlives this request by design — files.upload
		// mints and starts, and returns (D8) — so it cannot carry the
		// request's context. Owner: the transfer, bounded by its SESSION
		// exactly as its binding is (spec §5.1), never by the WebSocket,
		// which an AD-9 reconnect replaces underneath a running upload.
		// Closing event: cancelTransfersFor, reached from files.close, from
		// session teardown, from files.uploadCancel, from the ticket's
		// mint-side expiry timer and from server shutdown.
		tctx, cancel := context.WithCancel(context.Background())
		rt := &runningTransfer{
			id:        id,
			sessionID: sid,
			bindingID: params.BindingID,
			upload: transfer.Upload{
				DestDir: params.DestDir,
				Name:    params.Name,
				// The SOURCE's length is a property of the source, so on a
				// path upload the ticket answers it and the renderer's
				// declared size is not consulted at all — R2 read as far as
				// it goes: what the renderer may not name, it may not
				// measure either. The sink enforces this number against the
				// reader, so a file that changed length since the mint
				// fails the transfer rather than silently truncating.
				Size:     params.Size,
				OnExists: decision,
			},
			ctx:    tctx,
			cancel: cancel,
			body:   make(chan io.ReadCloser, 1),
			done:   make(chan struct{}),
			// One slot: the emitter reads the latest byte count off the
			// transfer, so a second pending wake would only ask it to send
			// the same number twice.
			progressWake: make(chan struct{}, 1),
		}
		if source != nil {
			rt.source = sourceReader
			rt.upload.Size = source.Size
		}
		// A body is wanted only when the bytes are the RENDERER's to send:
		// a path upload already holds its reader, and Skip moves nothing at
		// all. Everything else is a stream source, and the sink ticket is
		// what authorises the request that carries the bytes (D4).
		if err := h.machine.startUpload(rt, rt.source == nil && decision != transfer.Skip, sink); err != nil {
			cancel()
			closeSource(rt.source)
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}

		if rt.ticket == "" {
			// Skip needs no bytes: the transfer exists and is already over.
			_ = h.r.TryResult(req.ID, mustMarshal(filesUploadStarted{TransferID: rt.id}))
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(filesUploadStream{
			TransferID: rt.id,
			Ticket:     rt.ticket,
			URL:        h.machine.uploadURL(rt.ticket),
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleUploadCancel cancels one transfer. Idempotent by design (§5.3):
// cancelling a transfer that has already finished, or one that never
// existed, is not an error — the renderer's cancel button races the
// transfer's own completion every time, and losing that race is not a
// failure the person should be shown.
func (h uploadHandlers) handleUploadCancel(ctx context.Context, state *connState, req jsonrpcRequest) {
	var params filesUploadCancelParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	// The direction is checked as well as the ownership. Upload and
	// download ids are the same shape and live in one registry, so naming
	// a download here is expressible; honouring it would stop a transfer
	// on a surface the person was not looking at, and they would watch the
	// wrong row stop.
	if rt := h.machine.uploadFor(params.TransferID); rt != nil && rt.dir == dirUpload && state.has(rt.sessionID) {
		h.machine.cancelUpload(params.TransferID)
	}
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}

// ── helpers ───────────────────────────────────────────────────────────────

// uploadDestinationTaken reports whether something already occupies the
// destination name.
//
// Handle exposes no stat, so the probe is a one-byte Read — the cheapest
// existence question the seam can be asked, and one the caller is already
// authorised for on this binding. The mapping is deliberate in both
// directions: not-found is the only answer that means the name is free, and
// a directory or an unreadably large file sitting there is a collision, not
// an error. Everything else is reported as itself rather than guessed at,
// because "the directory is not readable" and "the name is taken" are
// different things to tell a person.
func uploadDestinationTaken(ctx context.Context, h filesystem.Handle, dest string) (bool, error) {
	_, err := h.Read(ctx, dest, 1)
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, new(*filesystem.ErrNotFound)):
		return false, nil
	case errors.As(err, new(*filesystem.ErrNotRegular)),
		errors.As(err, new(*filesystem.ErrTooLargeSize)):
		return true, nil
	default:
		return false, err
	}
}

// closeSource lets go of a redeemed source on a path that never reached the
// transfer. A claimed ticket is spent whatever happens next, so the reader
// it opened has exactly one owner from that moment and every refusal after
// it has to be one of them.
func closeSource(r io.ReadCloser) {
	if r != nil {
		_ = r.Close()
	}
}

// newTransferID mints a transfer id, in either direction — the same 16-byte crypto/rand shape as
// every other backend-minted id on this surface. Unlike the sink ticket it
// is not a credential: cancelling by it still re-checks that the caller
// owns the transfer's session.
func newTransferID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("upload id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

// ── the route's own header deadline (spec §5.4) ──────────────────────────

// maxUploadRequestLine bounds what the guard remembers of a request line
// while deciding which route it names. It only ever has to recognise a
// prefix, so this is a memory bound and not a protocol limit — a longer line
// is still read, it is simply no longer accumulated.
const maxUploadRequestLine = 512

// uploadGuardListener wraps the transport's listener so every accepted
// connection carries an upload guard.
//
// Why here and not ReadHeaderTimeout on the shared server: that setting is
// deliberately zero (ws.go) and turning it on would bound /session's header
// block as well, which is the one thing this must not do. A ConnState hook
// has the same problem in the other direction — StateActive fires when the
// first byte of a request is read, long before the target is known, so a
// deadline armed there could only be cleared by a handler, and /session's
// handler is not ours to change. The bytes themselves are the only thing
// that can tell the two routes apart before the parse finishes, which is
// what this reads.
type uploadGuardListener struct {
	net.Listener
	timeout func() time.Duration
}

func (l uploadGuardListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newUploadGuardConn(c, l.timeout), nil
}

// uploadGuardPhase is where the guard is in the request in front of it.
type uploadGuardPhase int

const (
	uploadGuardLine    uploadGuardPhase = iota // reading the request line; the target is not known yet
	uploadGuardHeaders                         // it names the upload route; reading its header block
	uploadGuardOff                             // decided; the bytes flow past untouched
)

// uploadGuardConn is one accepted connection, watched for exactly as long as
// it takes to find out what it is asking for.
//
// The interval, both ends named. It opens at Accept, before net/http has
// read a byte. It closes at the end of the request line for every target but
// this route's — so a /session connection is bound until its target is
// known and never afterwards, and the upgrade, the handler and the hijacked
// connection that follow are all outside it. For POST /upload/... it closes
// at the end of the header block, which is the interval §5.4 asks the route
// to bound and the one Go's server would otherwise leave unbounded.
//
// It also counts. headerEnd plus the request's declared Content-Length is
// exactly how many bytes the request is, so a connection that has delivered
// more than that has sent a body longer than its own framing — which §5.4
// requires be cut at the bound and failed, and which nothing above this can
// see: net/http hands the handler exactly Content-Length bytes and leaves
// the excess to be misparsed as the next request.
type uploadGuardConn struct {
	net.Conn
	timeout func() time.Duration

	mu        sync.Mutex
	phase     uploadGuardPhase
	deadline  time.Time // absolute; the end of the interval, re-applied before every read
	line      []byte    // the request line so far, bounded by maxUploadRequestLine
	lineLen   int       // bytes on the current header line, ignoring CR
	read      int64     // bytes this connection has delivered
	headerEnd int64     // offset just past the upload request's header block; -1 when unknown
}

func newUploadGuardConn(c net.Conn, timeout func() time.Duration) *uploadGuardConn {
	g := &uploadGuardConn{Conn: c, timeout: timeout, headerEnd: -1}
	g.arm()
	return g
}

// arm opens the interval, and re-applying it before every read is not
// belt-and-braces: net/http sets the read deadline itself at the top of
// every request — to the zero time, because ReadHeaderTimeout is zero — so
// a deadline set once at Accept is cleared before the first byte is read.
// The value is absolute and computed once, so re-applying it bounds the
// whole header block rather than degrading into "no progress for N", which
// a client dribbling one byte per interval would hold open for ever.
func (c *uploadGuardConn) arm() {
	c.mu.Lock()
	c.deadline = time.Now().Add(c.timeout())
	d := c.deadline
	c.mu.Unlock()
	_ = c.Conn.SetReadDeadline(d)
}

func (c *uploadGuardConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	armed, d := c.phase != uploadGuardOff, c.deadline
	c.mu.Unlock()
	if armed {
		_ = c.Conn.SetReadDeadline(d)
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.scan(p[:n])
	}
	return n, err
}

// scan advances the guard over the bytes just delivered.
//
// It is a request-line matcher and a blank-line detector, and deliberately
// not an HTTP parser: it needs to know which route was named and where the
// header block ended, and nothing else. A line is empty when a newline
// arrives with nothing but an optional CR since the previous one.
func (c *uploadGuardConn) scan(b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	base := c.read
	c.read += int64(len(b))
	if c.phase == uploadGuardOff {
		return
	}
	for i := 0; i < len(b); i++ {
		ch := b[i]
		if ch == '\r' {
			continue
		}
		switch c.phase {
		case uploadGuardLine:
			if ch != '\n' {
				if len(c.line) < maxUploadRequestLine {
					c.line = append(c.line, ch)
				}
				continue
			}
			if !guardedRequestLine(c.line) {
				c.phase = uploadGuardOff
				c.releaseLocked()
				return
			}
			c.phase = uploadGuardHeaders
			c.lineLen = 0
		case uploadGuardHeaders:
			if ch != '\n' {
				c.lineLen++
				continue
			}
			if c.lineLen > 0 {
				c.lineLen = 0
				continue
			}
			c.headerEnd = base + int64(i) + 1
			c.phase = uploadGuardOff
			c.releaseLocked()
			return
		case uploadGuardOff:
			return
		}
	}
}

// restart re-opens the interval for the next request on a reused connection.
// Reached from ConnState at StateIdle, which is the only moment net/http
// tells anyone that one request is over and the next has not begun.
//
// A client that pipelined its next request before going idle has already had
// those bytes counted and not scanned, so the guard will not find that
// request's header terminator and the connection dies at the bound. That is
// the safe direction, and nothing legitimate reaches it: /session upgrades
// and never sends a second request, and the upload route answers
// Connection: close.
func (c *uploadGuardConn) restart() {
	c.mu.Lock()
	c.phase = uploadGuardLine
	c.line = c.line[:0]
	c.lineLen = 0
	c.headerEnd = -1
	c.mu.Unlock()
	c.arm()
}

// releaseLocked ends the interval, clearing the deadline rather than leaving
// one in the future: everything after this point — a WebSocket that lives
// for hours, an upload body that takes an hour — is bounded by its own owner
// and must not inherit this one. Called from scan, which holds the lock.
func (c *uploadGuardConn) releaseLocked() {
	c.deadline = time.Time{}
	_ = c.Conn.SetReadDeadline(time.Time{})
}

func (c *uploadGuardConn) headerBlockEnd() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.headerEnd
}

func (c *uploadGuardConn) bytesRead() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read
}

// uploadGuardKey addresses the guard in a request's context. ConnContext is
// what puts it there: it runs with the net.Conn the listener returned, which
// is the guard itself.
type uploadGuardKey struct{}

// uploadOverran reports whether the connection carrying r delivered more
// bytes than r's own framing accounts for — the §5.4 rule that a body
// exceeding the bound is cut there and fails.
//
// headerEnd plus Content-Length is exactly how long the request is, and the
// guard counts what the socket handed over, so anything past that sum is
// bytes the client sent after its own body. It answers false when the guard
// never scanned this request's header block (nothing was armed, or the bytes
// were pipelined past it): an unknown offset is not evidence of an overrun,
// and a check that guessed would fail honest uploads.
//
// What it cannot see is excess that has not arrived yet. That is not a hole
// worth closing: bytes the sink never read cannot have reached the
// destination, and the route answers Connection: close, so they are
// discarded with the connection rather than framed as another request.
func uploadOverran(r *http.Request) bool {
	g, _ := r.Context().Value(uploadGuardKey{}).(*uploadGuardConn)
	if g == nil || r.ContentLength < 0 {
		return false
	}
	end := g.headerBlockEnd()
	if end < 0 {
		return false
	}
	return g.bytesRead() > end+r.ContentLength
}

// guardedRequestLine reports whether a request line names one of the two
// byte routes, which are the two whose header block this guard bounds.
//
// The method is part of the answer: the mux answers 405 for anything else,
// so a request line naming the right path with the wrong method has no
// header block worth bounding.
//
// The download route is here for the same reason the upload route is, and
// the reason is about the header block rather than about the body. Go's
// server parses the COMPLETE header block before dispatching, and the
// shared http.Server keeps ReadHeaderTimeout at zero because /session is a
// long-lived upgrade — so a client that opens a connection and dribbles
// header bytes for ever holds a connection that nothing above this can
// bound. That is true of a GET exactly as it is of a POST; only the body
// half differs, and the body half of a download is the RESPONSE, which the
// download handler bounds with its own per-write deadline.
func guardedRequestLine(line []byte) bool {
	return bytes.HasPrefix(line, []byte(http.MethodPost+" "+uploadRoutePrefix)) ||
		bytes.HasPrefix(line, []byte(http.MethodGet+" "+downloadRoutePrefix))
}

// ── POST /upload/{ticket} — the data half (spec §5.4) ────────────────────

// uploadBody is the claimed request body, wrapped so that it can be bounded
// and, crucially, UNBLOCKED.
//
// The second part is the contract transfer.Sink.Put puts on its caller and
// this is where it is honoured: an io.Reader has no context, so Put checks
// cancellation between chunks and can never abandon a Read already in
// flight. Cancelling alone would leave a stalled body holding a temp file
// and a lease open indefinitely.
//
// Tripping the connection's read deadline is what unblocks a blocked Read,
// and it is the only thing that safely can: a net.Conn deadline may be set
// from another goroutine at any time, while http.body.Close takes the same
// mutex the blocked Read holds — so closing the underlying body here would
// block on exactly the read it is meant to interrupt. Close therefore trips
// the deadline and refuses every later Read; net/http owns the request body
// and closes it when the handler returns.
type uploadBody struct {
	r       io.ReadCloser
	setRead func(time.Time) error
	stall   time.Duration
	// overran is asked once, at the EOF net/http reports at the declared
	// length, and is what turns "the body ended" into "the body ended and
	// the client kept talking" (§5.4). It is checked HERE rather than after
	// the transfer because the sink promotes the temp the moment the copy
	// succeeds: an overrun found afterwards would be found after the
	// destination had already been replaced.
	overran func() bool

	mu     sync.Mutex
	closed bool
}

// errUploadBodyClosed is what a Read after Close reports, and
// errUploadBodyOverran what a body longer than its own framing reports.
// Neither carries the ticket; both reach the person through
// files.uploadDone.
var (
	errUploadBodyClosed  = errors.New("upload: the request body was closed")
	errUploadBodyOverran = errors.New("upload: the body sent more than its declared length")
)

func (b *uploadBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return 0, errUploadBodyClosed
	}
	// Re-armed before EVERY read, so the bound is "no progress for stall",
	// never "the whole transfer within stall" (D2).
	_ = b.setRead(time.Now().Add(b.stall))
	n, err := b.r.Read(p)
	if errors.Is(err, io.EOF) && b.overran != nil && b.overran() {
		return n, errUploadBodyOverran
	}
	return n, err
}

func (b *uploadBody) Close() error {
	b.mu.Lock()
	already := b.closed
	b.closed = true
	b.mu.Unlock()
	if already {
		return nil
	}
	// A deadline in the past: an in-flight Read returns at once with a
	// timeout, and there is none to race with if it has not started.
	_ = b.setRead(time.Now().Add(-time.Second))
	return nil
}

// ── CORS, scoped to this route and to nothing else on the mux ────────────
//
// The route is cross-origin BY CONSTRUCTION, which is why this exists at
// all. `upload-client.ts` resolves the upload URL against the SOCKET's
// origin rather than the document's, because under `dev-web` the page is
// served by vite on one port and the backend listens on another — so every
// POST here is a cross-origin request in the configuration the product is
// developed and tested in. Both halves knew about the split and neither
// made the server allow it, so the feature was unreachable from a browser
// while every unit test was green: a non-browser caller never asks for
// these headers, and every test on this route was one.
//
// The contract is deliberately narrow, and each line is a decision:
//
//   - The origin is decided by the SAME OriginPolicy that guards /session.
//     Two matchers that must agree is a defect with a delay fuse, so there
//     is one and this route asks it rather than restating it.
//   - The check runs BEFORE the ticket is looked up or claimed, so a
//     refusal is not an oracle for a credential that authorises a write.
//   - The requesting origin is echoed EXACTLY, never `*`. A wildcard would
//     let any page on the machine read the reply, and the reply is the only
//     place a 409 and a 410 are told apart.
//   - `Vary: Origin` on every reply, refusals included: the answer depends
//     on a request header, and a cache that missed that would serve one
//     origin's answer to another.
//   - No `Access-Control-Allow-Credentials`. The ticket is the credential;
//     cookies and HTTP auth have no business at a route whose whole purpose
//     is one filesystem write on somebody's server.
//   - Exactly the request headers the handler needs, which is Content-Type
//     and nothing else. No blanket Authorization, no X-* wildcard.
//   - `Access-Control-Max-Age` is omitted: a cached preflight is an origin
//     decision the server cannot withdraw for as long as the cache holds it,
//     and a preflight per upload costs one round trip on loopback.
//
// And the headers go on EVERY reply this route gives, including 400, 409,
// 410 and 5xx. A browser hands the page nothing at all from a cross-origin
// reply that does not name it — the fetch rejects with "Failed to fetch" —
// which collapses "the ticket is gone" and "the network died" into one
// outcome, the two the renderer was just taught to treat differently
// (nocx-9le.5.18).
const (
	uploadAllowMethods = http.MethodPost
	uploadAllowHeaders = "Content-Type"
)

// allowTransferOrigin applies the part of the CORS reply that belongs on
// every answer, and reports whether the request may proceed. It writes the
// refusal itself and returns false when it may not.
//
// It serves BOTH byte routes. Two origin matchers that must agree is a
// defect with a delay fuse — the comment above says so about /session and
// this route, and it is no less true of this route and the download's — so
// there is one function, it asks the one OriginPolicy, and the route it is
// serving reaches it as a parameter for the log line and for nothing else.
func (s *WSServer) allowTransferOrigin(w http.ResponseWriter, r *http.Request, route string) bool {
	// Unconditionally, and before the decision: Vary describes what the
	// answer depends on, which is as true of the refusal as of the reply.
	w.Header().Set("Vary", "Origin")

	policy := s.origins
	if policy == nil {
		policy = LoopbackOriginPolicy{}
	}
	origin := r.Header.Get("Origin")
	if !policy.Allow(origin, r.Host) {
		// Origin and Host are logged for the same reason /session logs them
		// — a rejection nobody can diagnose is worse than one that quotes
		// what it rejected — but NOT the path, which carries the ticket.
		s.log.Warn("upload rejected",
			"reason", "origin_or_host",
			"origin", origin,
			"host", r.Host,
			"method", r.Method,
			"route", route)
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if origin != "" {
		// Echoed exactly. An absent Origin is a caller that is not a page
		// (LoopbackOriginPolicy allows it deliberately), and it gets no
		// header: there is nothing to echo and `*` is never the answer.
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	return true
}

// handleUploadPreflight answers the OPTIONS a browser sends before a POST it
// cannot safelist — which is every POST here, because the body carries a
// Content-Type the safelist does not cover.
//
// It never reads the ticket out of the path. A preflight precedes every
// upload, so one that claimed, extended or merely reported on a one-shot
// ticket would break the route for the only client that needs it, and would
// answer a question about a credential to a caller that has presented
// nothing but an origin. 204 with no body is the whole reply.
func (s *WSServer) handleUploadPreflight(w http.ResponseWriter, r *http.Request) {
	// Connection: close for the same reason the POST sets it, and one more:
	// it keeps "an upload is always the FIRST request on its connection"
	// true, which is the premise the guard's byte offsets are read against.
	w.Header().Set("Connection", "close")
	if !s.allowTransferOrigin(w, r, uploadRoutePrefix) {
		return
	}
	w.Header().Set("Access-Control-Allow-Methods", uploadAllowMethods)
	w.Header().Set("Access-Control-Allow-Headers", uploadAllowHeaders)
	w.WriteHeader(http.StatusNoContent)
}

// handleUpload carries one file's bytes to a claimed sink ticket.
//
// The ticket is the credential (D4): a POST cannot present a WebSocket
// subprotocol, so possession of the ticket is what authorises both the
// destination and the content written to it. The same OriginPolicy that
// guards /session guards this route, and the ticket appears in no log line
// and in no response body — this handler's messages are deliberately
// generic, because the authoritative account of what happened reaches the
// person as files.uploadDone, addressed by a transfer id that is not a
// credential.
func (s *WSServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	// One request per connection, on every path including the refusals.
	// The ticket is one-shot, so nothing follows a POST here that could
	// want the connection; and a connection that cannot be reused cannot be
	// parked idle and then held open with a second, endless header block,
	// which is the hole the guard's own interval would otherwise leave
	// behind a completed request. It also keeps the guard's byte offsets
	// meaningful: an upload is always the FIRST request on its connection,
	// so headerEnd is known for every one of them.
	w.Header().Set("Connection", "close")

	// Before the ticket is read out of the path, let alone claimed. An
	// origin we refuse must not learn whether a well-formed guess names a
	// live transfer, and must not consume one.
	if !s.allowTransferOrigin(w, r, uploadRoutePrefix) {
		return
	}

	rc := http.NewResponseController(w)
	// The route's own deadlines: the shared server has none for this (see
	// transferHeaderTimeout). Cleared before returning so the connection can
	// be reused — a deadline left in the past would kill keep-alive.
	defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	if err := rc.SetReadDeadline(time.Now().Add(s.transfers.headerDeadline())); err != nil {
		// Sink.Put is explicit: a reader that cannot be unblocked at all
		// must not be handed to it. Without a settable deadline this body
		// is exactly that, so it is refused rather than started.
		s.log.Warn("upload rejected", "reason", "no_read_deadline", "route", uploadRoutePrefix, "error", err)
		http.Error(w, "this connection cannot carry an upload", http.StatusInternalServerError)
		return
	}

	ticket := r.PathValue("ticket")
	if !isLowerHex(ticket, uploadTicketHexLen) {
		// A malformed ticket names nothing, which is the same state as one
		// that was never minted. Answering it differently would tell a
		// caller whether a well-formed guess existed.
		http.Error(w, "gone", http.StatusGone)
		return
	}

	rt, claim := s.transfers.claim(ticket, dirUpload, r.ContentLength)
	switch claim {
	case transferClaimUnknown, transferClaimFinished:
		// §5.4: both are 410, and 410 means only "this names nothing".
		// Expiry is not one of these states — the mint-side timer already
		// cancelled the transfer at expiry — so nothing here cancels
		// anything.
		http.Error(w, "gone", http.StatusGone)
		return
	case transferClaimRunning:
		// The first claimant keeps its transfer, untouched.
		http.Error(w, "this upload already has a body", http.StatusConflict)
		return
	case transferClaimNoLength:
		http.Error(w, "Content-Length is required", http.StatusBadRequest)
		return
	case transferClaimSizeMismatch:
		// Refused BEFORE the body is read, which is also what makes a body
		// longer than the declared size a refusal rather than a partial
		// write: the excess is never accepted onto the wire at all.
		http.Error(w, "Content-Length does not match the declared size", http.StatusBadRequest)
		return
	case transferClaimOK:
	}

	body := &uploadBody{
		r:       r.Body,
		setRead: rc.SetReadDeadline,
		stall:   s.transfers.stallTimeout(),
		overran: func() bool { return uploadOverran(r) },
	}
	if !rt.attach(body) {
		// The transfer ended between the claim and the hand-off.
		http.Error(w, "gone", http.StatusGone)
		return
	}

	// The handler waits for the transfer, because net/http invalidates the
	// request body the moment it returns and the sink is reading that body.
	//
	// A client that goes away CLOSES the body and does not cancel the
	// transfer, and the difference is deliberate. Closing is what unwinds a
	// Read the sink has already blocked in — the half of Put's contract this
	// caller owes. Cancelling would additionally relabel the outcome: a
	// dropped connection is a source that stopped delivering, which is a
	// FAILURE the person should see reported as one, while "cancelled" is
	// reserved for somebody actually asking. The first draft cancelled here
	// and a body ending four bytes short reported itself as cancelled,
	// because a half-close cancels the request context at about the moment
	// the sink sees the short read.
	select {
	case <-rt.done:
	case <-r.Context().Done():
		_ = body.Close()
		<-rt.done
	}

	// One shape gets no reply, and net/http decides that rather than this
	// handler: once a request-body read has timed out, the server cancels
	// the connection and discards whatever is written here. A client that
	// stopped talking is not listening either, and the authoritative
	// account still reaches the person as files.uploadDone.
	state, _, _, _ := rt.snapshot()
	switch state {
	case uploadStateWritten, uploadStateSkipped:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	case uploadStateCancelled:
		http.Error(w, "the transfer was cancelled", http.StatusInternalServerError)
	default:
		http.Error(w, "the transfer failed", http.StatusInternalServerError)
	}
}
