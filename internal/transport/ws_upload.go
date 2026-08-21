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
//	Uploader assertion beside the endpoint attester), so Handle.Upload
//	refuses with *filesystem.ErrUploadUnsupported. This file asks the
//	BINDING that question and never re-derives it from the endpoint
//	attestation: uploadSupported below is the whole of R1's enforcement
//	here, and it cannot answer anything the binding did not say.
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
	// renderer which never POSTs cannot hold a transfer, a use-guard and
	// (once the sftp lease is under it) a pooled SSH reference forever.
	//
	// Expiry is NOT one of the four ticket states of §5.4: the mint-side
	// timer drops the ticket and cancels the transfer it named AT THAT
	// MOMENT, so a late POST finds an unknown ticket. That is what keeps
	// 410 meaning exactly one thing — "this names nothing" — instead of
	// also meaning "cancel what it names".
	defaultUploadTicketTTL = 60 * time.Second

	// maxUploadTickets and maxUploadTransfers bound the two maps. Both are
	// far above the product's one-transfer-at-a-time-per-binding rule (§4)
	// and exist so a client that mints without ever finishing cannot grow
	// the server's memory without limit.
	maxUploadTickets   = 64
	maxUploadTransfers = 128

	// uploadDoneRetention is how long a finished transfer stays in the
	// registry after it ends. It is what makes files.uploadCancel
	// idempotent over a transfer that has already finished, and what lets
	// the §5.4 table tell "claimed, finished" apart from "unknown" instead
	// of collapsing both into a bare miss.
	uploadDoneRetention = 5 * time.Minute

	// uploadRoutePrefix is the path a claimed body is POSTed to, and
	// uploadTicketHexLen the hex width of the ticket in it: 32 random bytes
	// from crypto/rand, comfortably past D4's 128-bit floor.
	uploadRoutePrefix  = "/upload/"
	uploadTicketHexLen = 64

	// uploadUnwindTimeout bounds how long files.close and session teardown
	// wait for a CANCELLED transfer to unwind (D8). It is not a wait for
	// the upload: the transfer's context is cancelled and its body closed
	// first, so what is being waited for is one lane call plus the sink's
	// cleanup. The wait expiring is logged and the close proceeds.
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
	// author one, which is the difference between it and a path. Nothing
	// mints one yet (that is Task 8), so a request carrying one is refused
	// rather than silently treated as a stream upload.
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
	if p.SourceTicket != "" && !isLowerHex(p.SourceTicket, uploadTicketHexLen) {
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
	// ticket is the sink ticket that names this transfer's body, empty for
	// a transfer that needs none. It is here so the ticket can be retired
	// the instant the transfer ends, and it is never logged (D4).
	ticket string
	upload transfer.Upload

	ctx    context.Context
	cancel context.CancelFunc
	// body carries the claimed request body from the POST handler to the
	// goroutine running the sink. Buffered by one: the claim happens once,
	// so nothing can ever queue behind it.
	body chan io.ReadCloser
	done chan struct{}

	mu      sync.Mutex
	closer  io.Closer // the claimed body, so cancellation can unblock a stalled Read
	bytes   int64
	state   string
	outcome transfer.Outcome
	err     error
	endedAt time.Time
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

// progress records the running byte total. Task 7 turns this into the
// files.uploadProgress notification; here it is what makes the transfer's
// advance observable at all.
func (rt *runningTransfer) progress(total int64) {
	rt.mu.Lock()
	rt.bytes = total
	rt.mu.Unlock()
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

// uploadTicket is one minted sink ticket. The three flags are the four
// states of §5.4: absent from the map is "unknown", finished is "claimed
// and over", claimed alone is "claimed and running", and neither is
// "minted, unclaimed".
type uploadTicket struct {
	transferID string
	size       int64
	createdAt  time.Time
	claimed    bool
	finished   bool
	timer      *time.Timer
}

// uploadRegistry holds the transport's running transfers and the tickets
// that name them. The zero value is usable.
type uploadRegistry struct {
	mu        sync.Mutex
	transfers map[string]*runningTransfer
	tickets   map[string]*uploadTicket

	// ttl/ttlSet is the unclaimed-ticket TTL. ttlSet exists so that a test
	// can ask for zero — expire as soon as the timer goroutine can run —
	// without that being indistinguishable from "unset".
	ttl    time.Duration
	ttlSet bool

	// now is the clock, injectable for the eviction tests.
	now func() time.Time
}

func (u *uploadRegistry) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

func (u *uploadRegistry) ticketTTL() time.Duration {
	if u.ttlSet {
		return u.ttl
	}
	return defaultUploadTicketTTL
}

// WithUploadTicketTTL bounds how long an unclaimed sink ticket lives. Zero
// is legitimate and means "expire as soon as the mint-side timer can run" —
// the tests use it to reach the expiry path through the SAME code
// production takes, rather than by sleeping.
func WithUploadTicketTTL(d time.Duration) WSServerOption {
	return func(s *WSServer) {
		s.uploads.ttl = d
		s.uploads.ttlSet = true
	}
}

// evictLocked drops transfers that finished long enough ago to be beyond
// any caller's interest. Tickets are not swept here: each carries its own
// mint-side timer, which is the enforceable event §5.4 closes the TTL at.
func (u *uploadRegistry) evictLocked(now time.Time) {
	for id, rt := range u.transfers {
		rt.mu.Lock()
		ended, over := rt.endedAt, rt.state != ""
		rt.mu.Unlock()
		if over && now.Sub(ended) > uploadDoneRetention {
			delete(u.transfers, id)
		}
	}
}

// add registers a running transfer. It refuses once the map is full rather
// than growing without limit.
func (u *uploadRegistry) add(rt *runningTransfer) error {
	now := u.clock()
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.transfers == nil {
		u.transfers = make(map[string]*runningTransfer)
	}
	u.evictLocked(now)
	if len(u.transfers) >= maxUploadTransfers {
		return errors.New("too many transfers in flight")
	}
	u.transfers[rt.id] = rt
	return nil
}

func (u *uploadRegistry) get(transferID string) *runningTransfer {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.transfers[transferID]
}

// mintTicket mints the sink ticket for a transfer and arms its expiry.
//
// The timer is the whole of the TTL rule: when it fires on a ticket nobody
// claimed, the ticket is forgotten AND the transfer it named is cancelled,
// in that order and at that moment. So a POST that arrives afterwards finds
// an unknown ticket and is told 410 — which never has to mean "cancel what
// this names", because expiry already did.
func (u *uploadRegistry) mintTicket(rt *runningTransfer) (string, error) {
	var buf [32]byte // 256 bits, comfortably past D4's 128-bit floor
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("upload ticket: %w", err)
	}
	ticket := hex.EncodeToString(buf[:])
	now := u.clock()

	u.mu.Lock()
	if u.tickets == nil {
		u.tickets = make(map[string]*uploadTicket)
	}
	if len(u.tickets) >= maxUploadTickets {
		u.mu.Unlock()
		return "", errors.New("too many upload tickets outstanding")
	}
	u.tickets[ticket] = &uploadTicket{transferID: rt.id, size: rt.upload.Size, createdAt: now}
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
func (u *uploadRegistry) expire(ticket string) {
	u.mu.Lock()
	e, ok := u.tickets[ticket]
	if !ok || e.claimed {
		u.mu.Unlock()
		return
	}
	delete(u.tickets, ticket)
	rt := u.transfers[e.transferID]
	u.mu.Unlock()
	if rt != nil {
		rt.stop()
	}
}

// retireTicket marks a ticket's transfer over. An unclaimed ticket is
// forgotten outright — nothing may claim a transfer that has ended — and a
// claimed one is kept as "finished" so a second POST is told 410 rather
// than 409.
func (u *uploadRegistry) retireTicket(ticket string) {
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
func (u *uploadRegistry) cancel(transferID string) bool {
	rt := u.get(transferID)
	if rt == nil {
		return false
	}
	rt.stop()
	return true
}

// pick returns the transfers matching a predicate.
func (u *uploadRegistry) pick(match func(*runningTransfer) bool) []*runningTransfer {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []*runningTransfer
	for _, rt := range u.transfers {
		if match(rt) {
			out = append(out, rt)
		}
	}
	return out
}

// ── the D8 teardown path ─────────────────────────────────────────────────

// cancelUploadsFor cancels a set of transfers and waits, BOUNDED, for them
// to unwind. It returns whether every one of them ended within the bound.
//
// This is D8. A transfer holds the binding's use-guard for its lifetime,
// because Handle.Upload takes one per call and the sink is reachable no
// other way (Binding.provider is unexported by design, D7). Binding.close
// waits for that guard to drain (internal/filesystem/binding.go:187), so
// the ONLY thing standing between files.close and a wait as long as the
// upload is that the cancel happens first: the context is cancelled, the
// body is closed, the sink unwinds within one lane call, the guard drops
// and the close proceeds.
//
// Which makes the ordering load-bearing rather than tidy, and the bound the
// honest statement of what is left: cancel, wait up to uploadUnwindTimeout,
// then close regardless. Nothing here ever waits for an upload to finish.
func (s *WSServer) cancelUploadsFor(transfers []*runningTransfer) bool {
	if len(transfers) == 0 {
		return true
	}
	for _, rt := range transfers {
		rt.stop()
	}
	deadline := time.NewTimer(uploadUnwindTimeout)
	defer deadline.Stop()
	for _, rt := range transfers {
		select {
		case <-rt.done:
		case <-deadline.C:
			s.log.Warn("upload did not unwind within the bound; closing anyway",
				"transfer_id", rt.id, "binding_id", rt.bindingID, "maxWait", uploadUnwindTimeout)
			return false
		}
	}
	return true
}

// cancelBindingUploads cancels every transfer of one binding — files.close.
func (s *WSServer) cancelBindingUploads(bid string) {
	s.cancelUploadsFor(s.uploads.pick(func(rt *runningTransfer) bool { return rt.bindingID == bid }))
}

// cancelSessionUploads cancels every transfer of one session — the terminal
// closing, which closes its bindings (spec §5.1).
func (s *WSServer) cancelSessionUploads(sid session.ID) {
	s.cancelUploadsFor(s.uploads.pick(func(rt *runningTransfer) bool { return rt.sessionID == sid }))
}

// cancelAllUploads cancels every transfer — server shutdown. Without it a
// transfer outlives the server that started it, holding a use-guard and a
// pooled SSH reference nothing will ever release.
func (s *WSServer) cancelAllUploads() {
	s.cancelUploadsFor(s.uploads.pick(func(*runningTransfer) bool { return true }))
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
	// mintUploadTicket mints the sink ticket naming rt's body and arms its
	// expiry.
	mintUploadTicket(rt *runningTransfer) (string, error)
	// retireUploadTicket ends a ticket's life: forgotten if it was never
	// claimed, marked finished if it was.
	retireUploadTicket(ticket string)
	// startUpload registers rt and runs it. The goroutine owns h and its
	// release for the transfer's lifetime.
	startUpload(rt *runningTransfer, h filesystem.Handle, release func()) error
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

func (s *WSServer) mintUploadTicket(rt *runningTransfer) (string, error) {
	return s.uploads.mintTicket(rt)
}

func (s *WSServer) retireUploadTicket(ticket string) { s.uploads.retireTicket(ticket) }

func (s *WSServer) uploadFor(transferID string) *runningTransfer { return s.uploads.get(transferID) }

func (s *WSServer) cancelUpload(transferID string) bool { return s.uploads.cancel(transferID) }

func (s *WSServer) uploadURL(ticket string) string { return uploadRoutePrefix + ticket }

func (s *WSServer) startUpload(rt *runningTransfer, h filesystem.Handle, release func()) error {
	if err := s.uploads.add(rt); err != nil {
		return err
	}
	go s.runUpload(rt, h, release)
	return nil
}

// runUpload is the transfer's own goroutine. It waits for a body when one
// is expected, writes it through the binding's sink, and settles the
// transfer's terminal state before anything can observe it as over.
func (s *WSServer) runUpload(rt *runningTransfer, h filesystem.Handle, release func()) {
	defer close(rt.done)
	// The handle's use-guard is released here and only here: it is held for
	// the transfer, and the teardown paths above cancel before they close
	// so that holding it can never make files.close wait for an upload.
	defer release()

	var body io.ReadCloser
	if rt.ticket != "" {
		select {
		case body = <-rt.body:
		case <-rt.ctx.Done():
			rt.finish(uploadStateCancelled, transfer.Outcome{}, rt.ctx.Err(), s.uploads.clock())
			s.uploads.retireTicket(rt.ticket)
			return
		}
	}

	out, err := h.Upload(rt.ctx, rt.upload, body, rt.progress)
	if body != nil {
		_ = body.Close()
	}
	rt.finish(uploadStateOf(out, err, rt.ctx.Err()), out, err, s.uploads.clock())
	s.uploads.retireTicket(rt.ticket)
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

// ── handlers ──────────────────────────────────────────────────────────────

// uploadHandlers answers files.upload and files.uploadCancel. Like every
// files.* handler it holds the FilesystemBindingOperation and a
// transport-owned seam, never the *WSServer.
type uploadHandlers struct {
	op      capability.FilesystemBindingOperation // nil → filesystem not wired
	machine uploadMachine
	r       Responder
}

// handleUpload mints a transfer and starts it — and does not wait for it.
//
// It follows handleList's shape exactly (ws_files.go:539): the op.Run
// wrapper, Acquire re-checking that THIS connection owns the binding's
// session (D15) and taking the use-guard, and filesErrorCode for the
// mapping. The one deliberate difference is the guard's lifetime: on the
// paths that answer without starting anything it is released here, and on
// the path that starts a transfer it passes to the transfer's goroutine.
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
		started := false
		defer func() {
			if !started {
				release()
			}
		}()

		// R1, asked of the binding and of nothing else.
		if unsupported := uploadSupported(ctx, handle); unsupported != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(unsupported), Message: unsupported.Error()})
			return nil
		}
		if params.SourceTicket != "" {
			// The mint side is Task 8. Refusing is the honest answer: a
			// ticket nothing minted names nothing, and treating it as a
			// stream upload would silently change what the caller asked
			// for.
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown sourceTicket"})
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

		id, err := newUploadID()
		if err != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		// The transfer outlives this request by design — files.upload
		// mints and starts, and returns (D8) — so it cannot carry the
		// request's context. Owner: the transfer, bounded by its SESSION
		// exactly as its binding is (spec §5.1), never by the WebSocket,
		// which an AD-9 reconnect replaces underneath a running upload.
		// Closing event: cancelUploadsFor, reached from files.close, from
		// session teardown, from files.uploadCancel, from the ticket's
		// mint-side expiry timer and from server shutdown.
		tctx, cancel := context.WithCancel(context.Background())
		rt := &runningTransfer{
			id:        id,
			sessionID: sid,
			bindingID: params.BindingID,
			upload: transfer.Upload{
				DestDir:  params.DestDir,
				Name:     params.Name,
				Size:     params.Size,
				OnExists: decision,
			},
			ctx:    tctx,
			cancel: cancel,
			body:   make(chan io.ReadCloser, 1),
			done:   make(chan struct{}),
		}
		if decision != transfer.Skip {
			// The stream source: the sink waits for a body, and the ticket
			// is what authorises the request that carries it (D4).
			ticket, err := h.machine.mintUploadTicket(rt)
			if err != nil {
				cancel()
				_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
				return nil
			}
			rt.ticket = ticket
		}
		if err := h.machine.startUpload(rt, handle, release); err != nil {
			cancel()
			h.machine.retireUploadTicket(rt.ticket)
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		started = true

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
	if rt := h.machine.uploadFor(params.TransferID); rt != nil && state.has(rt.sessionID) {
		h.machine.cancelUpload(params.TransferID)
	}
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}

// ── helpers ───────────────────────────────────────────────────────────────

// uploadSupported asks the BINDING whether it can be written to, and this
// is the whole of R1's enforcement on the wire.
//
// The question has exactly one answer-holder. A local binding was
// registered with no sink, Binding.provider is unexported, and Acquire
// returns a Handle — so nothing downstream can re-derive the capability
// from the provider or from the endpoint attestation, which is D7's
// structural guarantee working as designed. Handle.Upload is therefore the
// only surface that answers, and the zero Upload is how it is asked without
// moving anything: the sink's own validate() refuses an empty DestDir
// before any round trip (internal/transfer/sink.go:381), while a binding
// with no sink answers *ErrUploadUnsupported without reaching a provider at
// all. Anything else means the binding CAN write and the probe is done.
func uploadSupported(ctx context.Context, h filesystem.Handle) error {
	_, err := h.Upload(ctx, transfer.Upload{}, nil, nil)
	var unsupported *filesystem.ErrUploadUnsupported
	if errors.As(err, &unsupported) {
		return err
	}
	return nil
}

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

// newUploadID mints a transfer id — the same 16-byte crypto/rand shape as
// every other backend-minted id on this surface. Unlike the sink ticket it
// is not a credential: cancelling by it still re-checks that the caller
// owns the transfer's session.
func newUploadID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("upload id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
