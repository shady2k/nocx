package transport

// The download surface on the wire: files.download and
// files.downloadCancel on the control plane, and GET /download/{ticket}
// that carries the bytes. It is upload's mirror
// (.internal/specs/2026-08-21-upload-to-the-active-tab-design.md), and this
// comment is about the three places the mirror is exact, the one place it
// is not, and why.
//
// # R1 carries over unchanged, and it is the same mechanism
//
// A download is addressed by a bindingId and authorised by Registry.Acquire
// re-checking that the binding's session belongs to the requesting
// connection. Reading a file off the WRONG host is as wrong as writing to
// it, and the addressing is what makes it inexpressible: a binding names
// one session's filesystem and nothing else. The capability question is
// asked of the BINDING and of nothing else — Handle.Downloader — and the
// source it returns is the source the transfer then runs on, so the
// capability and the thing it authorises are one answer, exactly as
// handleUpload's Uploader call is.
//
// # D8 carries over unchanged, and this side is easier to keep
//
// The transfer must not hold the binding's use-guard: Binding.Close waits
// for guards to drain, and a guard held for the length of a download would
// make files.close and session teardown wait for a 400 MB file. So the
// guard covers the synchronous files.download call — Acquire, the
// capability question, and the OPEN — and is dropped before the transfer
// runs, which is Handle.Downloader's own contract. What makes running
// unguarded safe is the lease underneath rather than hope: closing the
// provider closes the SFTP session and client, so a read racing a close
// fails and reports how far it got.
//
// A download is registered in the SAME registry an upload is, so the D8
// teardown path — cancelTransfersFor, reached from files.close, from session
// teardown, from the ticket's expiry timer and from shutdown — cancels it
// with no new code and, more to the point, with no second place for any of
// those callers to remember to look.
//
// # R2 does NOT carry over, and the asymmetry is correct rather than an
// oversight
//
// files.upload has no sourcePath because a source path on the BACKEND's
// disk is scoped by nothing: a renderer that could spell one could ask the
// backend to read ~/.ssh/id_ed25519 and send it to a host of the
// renderer's choosing, and binding ownership proves which terminal the
// caller owns while proving nothing at all about the backend's filesystem.
// That is why an upload's backend-side source is named by an opaque ticket
// minted at the moment a human chose the file.
//
// A download's source is a path on the REMOTE host, inside a binding the
// caller already owns — and can already enumerate with files.list and read
// with files.read. Naming it is therefore not new authority; it is the
// same authority in a different bound. So there is no source ticket here,
// and inventing one would be a credential that authorised nothing the
// caller did not already have, which is worse than not having it: a check
// that cannot fail is read by the next person as a check.
//
// The one thing genuinely new is the BOUND. files.read is capped at 8 MiB,
// buffered whole, decoded as text and reported as truncated past the cap;
// a download is unbounded and streamed. So the path is validated the way
// files.upload's destDir is — absolute, clean, bounded, and then by the
// provider, which owns syntax — and the provider refuses anything that is
// not a regular file, because a directory has no byte stream and a fifo
// blocks.
//
// # Where the bytes go, argued against the same three costs D3 weighed
//
// D3 sent upload's bytes over a streamed POST rather than a new binary
// msg-type on the data plane. Each of its three arguments is stronger in
// this direction, and there is a fourth that is decisive on its own.
//
//  1. CONTENTION. D3's case was that an upload runs renderer→backend, the
//     same direction as keystrokes, and would queue ahead of them. A
//     download runs backend→renderer — the same direction as bulk PTY
//     OUTPUT, which is the traffic the terminal's responsiveness IS. So the
//     collision is not merely present here, it is head-on: a 400 MB
//     download multiplexed onto that socket sits in front of the frames a
//     person is reading.
//
//  2. BACKPRESSURE. The outbound queue is bounded (outbound.DefaultQueueDepth)
//     and deliberately LOSSY — a full queue drops frames and trips the
//     stall notice the renderer treats as a cue to reconnect. PTY output
//     may be dropped; a file's bytes may not. Making them undroppable
//     means inventing application-level credit in the server→client
//     direction, an ack for it, a chunk sequence and reconnect semantics —
//     the same missing half D3 named, in the mirror. An HTTP response gets
//     all of it from TCP for nothing: the handler's write blocks when the
//     client stops reading, and that backpressure reaches the source's copy
//     loop as a slow Write.
//
//  3. A SECOND CODEC. frame.go and frame.ts are two codecs pinned to each
//     other by golden vectors, and keeping them about PTY only is worth as
//     much here as there.
//
//  4. And the one that decides it without the other three: A DOWNLOAD HAS
//     TO BECOME A FILE. A browser saves a response to disk itself, streaming
//     it, and a page cannot. Bytes arriving as WebSocket messages have to be
//     accumulated in the renderer's heap and handed to the platform as one
//     Blob, so a 2 GB download would need 2 GB of renderer memory before
//     anything reached the disk. There is no version of that which is not
//     worse.
//
// The route therefore reuses the upload's machinery rather than
// duplicating it: the same OriginPolicy through the same allowTransferOrigin,
// the same guarded listener bounding its header block, the same one-shot
// ticket store with the same four states and the same TTL, and the same
// registry. What is genuinely new is a per-WRITE stall deadline instead of
// a per-read one, and the response framing.
//
// # The asymmetry that governs every failure below
//
// An upload can be undone: its bytes land in a temp file and a failure
// before the promote leaves the destination exactly as it was. A download
// cannot. Bytes handed to the client are gone, the status line is written
// before the first of them, and neither can be revised. So there is no
// stranded list here and no rollback — what replaces them is that the
// response is FRAMED at the size measured on the pinned handle, so a
// transfer that fails part-way arrives as a body short of its own declared
// length, which every HTTP client treats as the failure it is.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transfer"
	"github.com/shady2k/nocx/internal/transport/control"
)

// The terminal states a download reports. They are files.downloadDone's
// outcome enum, and they are three rather than upload's four: there is no
// "skipped", because nothing on the far host is being replaced and so
// there is no collision question to answer.
const (
	downloadStateSent      = "sent"
	downloadStateCancelled = "cancelled"
	downloadStateFailed    = "failed"
)

// ── wire shapes (contracts/files.download*.schema.json) ──────────────────

// filesDownloadParams is the whole of what a renderer may say about a
// download: which binding, and which path on the host that binding views.
//
// It is decoded with decodeParamsStrict for the reason files.upload is,
// even though R2 is not what is being enforced: a tolerant decoder accepts
// a field, drops it silently, and reads as "that parameter is ignored",
// which is one refactor away from being read.
type filesDownloadParams struct {
	BindingID string `json:"bindingId"`
	Path      string `json:"path"`
}

// filesDownloadResult is what files.download answers. There is exactly one
// branch, and the contrast with files.upload's three is the design showing
// through: an upload has a collision question to ask before a byte moves
// and a source that may already be in hand, and a download has neither.
type filesDownloadResult struct {
	TransferID string `json:"transferId"`
	Ticket     string `json:"ticket"`
	URL        string `json:"url"`
	// Name is the base name of the file, which is what it will be called
	// when it lands. Never a path: the person asked for one file and the
	// directory it came from is already on their screen.
	Name string `json:"name"`
	// Size is the length measured on the OPEN handle, and it is the number
	// the fetch declares as its Content-Length. It is authoritative rather
	// than advisory because the handle is pinned: nothing that happens to
	// the NAME between this answer and the fetch can make it describe
	// different bytes.
	Size int64 `json:"size"`
}

type filesDownloadCancelParams struct {
	TransferID string `json:"transferId"`
}

// filesDownloadDoneParams is the files.downloadDone notification — the
// transfer's terminal account, retained per session and flushed on attach
// exactly as files.uploadDone is, and for its reason: a lost terminal
// outcome leaves the indicator saying "downloading" for the rest of the
// session about a transfer that ended ten minutes ago.
//
// It carries neither finalName nor stranded, and both absences are the
// direction rather than an economy. Nothing is renamed, because nothing on
// the far host is being written; nothing can be left behind, because
// nothing was created. What replaces them is Bytes, which is the only
// number that says how much of the file the person actually got — and on a
// failed download that number is the whole of the account.
type filesDownloadDoneParams struct {
	TransferID string `json:"transferId"`
	Outcome    string `json:"outcome"`
	Name       string `json:"name"`
	Bytes      int64  `json:"bytes"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
}

// ── validators ───────────────────────────────────────────────────────────

// validateFilesDownloadRaw applies files.upload's path rules to the
// download's source: absolute, clean and bounded, checked before any
// handler runs, so every rejection is -32602 before anything is opened. The
// provider then applies its own syntax, which is the half this cannot do
// because the destination host may spell paths differently from the one we
// are running on.
func validateFilesDownloadRaw(raw json.RawMessage) string {
	var p filesDownloadParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.BindingID, 32) {
		return "bindingId is required and must be the 32-hex id the backend minted"
	}
	return validateFSPath(p.Path, "path")
}

func validateFilesDownloadCancelRaw(raw json.RawMessage) string {
	var p filesDownloadCancelParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.TransferID, 32) {
		return "transferId is required and must be the 32-hex id the backend minted"
	}
	return ""
}

// downloadErrorCode maps a source refusal onto a JSON-RPC code.
//
// The three request-shaped refusals are named explicitly rather than left
// to filesErrorCode, because the source seam reports them as wrapped
// sentinels and not as filesystem's typed errors — internal/transfer may
// not import internal/filesystem (the dependency runs the other way), so
// its adapters speak fs.ErrNotExist, fs.ErrPermission and
// transfer.ErrNotRegular. A permission denial reported as -32603 tells the
// person the server went wrong, which is the wrong thing to do about it.
func downloadErrorCode(err error) int {
	switch {
	case errors.Is(err, fs.ErrNotExist),
		errors.Is(err, fs.ErrPermission),
		errors.Is(err, transfer.ErrNotRegular),
		errors.Is(err, transfer.ErrInvalidDownload):
		return -32602
	}
	return filesErrorCode(err)
}

// ── the machine seam ─────────────────────────────────────────────────────

// downloadMachine is the transport-owned download surface a handler
// reaches. It is a separate, narrower interface from uploadMachine rather
// than a widened one for the reason the house style gives everywhere else:
// a handler should be able to reach exactly the operations it needs and
// nothing else on the server.
type downloadMachine interface {
	// bindingSession is the session a binding this transport issued
	// belongs to; false when the binding is not one of ours. A transfer is
	// bounded by the SESSION exactly as its binding is, never by the
	// WebSocket, which an AD-9 reconnect replaces underneath a running
	// download.
	bindingSession(bid string) (session.ID, bool)
	// startDownload registers rt, mints its ticket and runs it on source —
	// the binding's read half, taken from the handle during the
	// synchronous call and detached from its use-guard (D8). rt.ticket is
	// filled in before the goroutine starts.
	startDownload(rt *runningTransfer, source transfer.Source) error
	// transferFor returns a transfer by id, or nil.
	transferFor(transferID string) *runningTransfer
	// cancelTransfer stops one transfer and reports whether it existed.
	cancelTransfer(transferID string) bool
	// downloadURL is the path a claimed download is fetched from.
	downloadURL(ticket string) string
}

func (s *WSServer) transferFor(transferID string) *runningTransfer {
	return s.transfers.get(transferID)
}

func (s *WSServer) cancelTransfer(transferID string) bool { return s.transfers.cancel(transferID) }

func (s *WSServer) downloadURL(ticket string) string { return downloadRoutePrefix + ticket }

// startDownload registers the transfer BEFORE minting its ticket, for the
// same race startUpload names: the mint arms an expiry timer that cancels
// "the transfer this ticket names", and with a short TTL it can fire before
// the registration.
func (s *WSServer) startDownload(rt *runningTransfer, source transfer.Source) error {
	// A download ALWAYS needs a ticket. There is no counterpart to
	// upload's skip decision and no counterpart to its source ticket:
	// somebody has to come and take the bytes, and the ticket is what
	// authorises them to.
	if err := s.registerAndMint(rt, true); err != nil {
		return err
	}
	go s.emitTransferProgress(rt)
	go s.runDownload(rt, source)
	return nil
}

// runDownload is the transfer's own goroutine. It waits for a fetch to
// claim it, streams the pinned handle into that fetch's response, and
// settles the terminal state before anything can observe the transfer as
// over.
//
// It holds the source and the pinned handle and nothing else. The handle
// files.download came from, and the use-guard that handle carried, were
// both let go when files.download answered (D8).
func (s *WSServer) runDownload(rt *runningTransfer, source transfer.Source) {
	defer close(rt.done)
	// The pinned handle is closed on EVERY path out of this goroutine,
	// including the one where nobody ever fetched. It is the closing end
	// of the interval that opened when files.download called Open: from
	// that call until this line, one descriptor on the source host is held
	// against this transfer, and nothing else in the process may close it.
	defer func() { _ = rt.download.Close() }()

	var dst io.Writer
	select {
	case dst = <-rt.dest:
	case <-rt.ctx.Done():
		// Cancelled, or the ticket's TTL elapsed with nobody fetching. No
		// byte has left the host and none ever will.
		rt.finish(downloadStateCancelled, transfer.Outcome{}, rt.ctx.Err(), s.transfers.clock())
		s.transfers.retireTicket(rt.ticket)
		s.settleDownload(rt)
		return
	}

	sent, err := source.Get(rt.ctx, rt.download, dst, rt.progress)
	rt.finish(downloadStateOf(err, rt.ctx.Err()), transfer.Outcome{}, err, s.transfers.clock())
	s.transfers.retireTicket(rt.ticket)
	// Before the deferred close(rt.done), on every path: anything that
	// observes a transfer as over — the fetch handler, the teardown wait, a
	// test — must find the terminal outcome already delivered or already
	// retained, never in flight behind it.
	s.settleDownload(rt)
	if err != nil {
		// The reason reaches the person through files.downloadDone; the
		// log is for the operator and carries neither the ticket nor the
		// path.
		s.log.Warn("download did not complete",
			"transfer_id", rt.id, "binding_id", rt.bindingID,
			"sent", sent, "total", rt.size(), "error", err)
	}
}

// downloadStateOf maps the source's return onto the outcome enum. A
// cancelled download is not a failure and must not be reported as one: the
// person pressed cancel, the binding went away underneath them, or nobody
// ever came to fetch it.
func downloadStateOf(err, ctxErr error) string {
	switch {
	case err == nil:
		return downloadStateSent
	case errors.Is(err, context.Canceled), ctxErr != nil:
		return downloadStateCancelled
	default:
		return downloadStateFailed
	}
}

// settleDownload is the terminal account. There is no invalidation half:
// a download changes nothing on the host, so no directory becomes stale
// and nothing needs re-listing.
func (s *WSServer) settleDownload(rt *runningTransfer) {
	state, _, sent, err := rt.snapshot()
	p := filesDownloadDoneParams{
		TransferID: rt.id,
		Outcome:    state,
		Name:       rt.downloadName(),
		Bytes:      sent,
		Total:      rt.size(),
	}
	// The reason is carried only for a failure, for files.uploadDone's
	// reason: a cancelled transfer's err is a context cancellation, which
	// is not a fault and must never be shown to a person as one.
	if state == downloadStateFailed && err != nil {
		p.Error = err.Error()
	}
	s.deliverTransferDone(rt.sessionID, rt.id, retainedDone{
		method: "files.downloadDone", params: mustMarshal(p),
	})
}

// downloadName is the base name of a download's pinned file, or "" for a
// transfer that is not one.
func (rt *runningTransfer) downloadName() string {
	if rt.download == nil {
		return ""
	}
	return rt.download.Name
}

// ── handlers ─────────────────────────────────────────────────────────────

// downloadHandlers answers files.download and files.downloadCancel. Like
// every files.* handler it holds the FilesystemBindingOperation and a
// transport-owned seam, never the *WSServer.
type downloadHandlers struct {
	op      capability.FilesystemBindingOperation // nil → filesystem not wired
	machine downloadMachine
	r       Responder
}

// handleDownload pins the bytes and mints the fetch that will carry them —
// and does not wait for it.
//
// The order of the four things it does is the whole of the handler, and
// each one is the last moment something can still be refused for free:
// Acquire proves this connection owns the binding's session, Downloader
// asks the binding whether it can stream at all (R1), Open pins the file
// and measures it, and only then is a transfer registered and a ticket
// minted. Everything that can refuse this request without holding a
// descriptor has refused it before one is held.
func (h downloadHandlers) handleDownload(ctx context.Context, state *connState, req jsonrpcRequest) {
	if h.op == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "files not available"})
		return
	}
	var params filesDownloadParams
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
		// transfer runs on is the source and an already-open handle, both
		// of which outlive the handle, so a running download is never
		// something a close has to wait for.
		defer release()

		// R1, asked of the binding and of nothing else — and the same call
		// is what the transfer will run on, so the capability and the
		// thing it authorises can never be two different answers.
		source, srcErr := handle.Downloader()
		if srcErr != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: filesErrorCode(srcErr), Message: srcErr.Error()})
			return nil
		}

		sid, ok := h.machine.bindingSession(params.BindingID)
		if !ok {
			// Acquire succeeded, so the binding exists in the registry;
			// the transport's own bookkeeping is what is missing, and a
			// transfer with no session could never be torn down with one.
			_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: unknown bindingId"})
			return nil
		}

		// The open happens HERE, inside the guard, and pins the file for
		// the transfer's whole life. Doing it at fetch time instead would
		// mean measuring the size at one moment and sending the bytes from
		// whatever the name resolved to at another — and the size is what
		// the response is framed at, so the two disagreeing is a corrupt
		// download rather than a stale number.
		d, openErr := source.Open(params.Path)
		if openErr != nil {
			_ = h.r.TryError(req.ID, RPCError{Code: downloadErrorCode(openErr), Message: openErr.Error()})
			return nil
		}

		id, err := newTransferID()
		if err != nil {
			_ = d.Close()
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		// The transfer outlives this request by design — files.download
		// mints and starts, and returns (D8) — so it cannot carry the
		// request's context. Owner: the transfer, bounded by its SESSION
		// exactly as its binding is, never by the WebSocket, which an AD-9
		// reconnect replaces underneath a running download. Closing event:
		// cancelTransfersFor, reached from files.close, from session
		// teardown, from files.downloadCancel, from the ticket's
		// mint-side expiry timer and from server shutdown.
		tctx, cancel := context.WithCancel(context.Background())
		rt := &runningTransfer{
			id:        id,
			dir:       dirDownload,
			sessionID: sid,
			bindingID: params.BindingID,
			download:  d,
			ctx:       tctx,
			cancel:    cancel,
			dest:      make(chan io.Writer, 1),
			done:      make(chan struct{}),
			// One slot: the emitter reads the latest byte count off the
			// transfer, so a second pending wake would only ask it to send
			// the same number twice.
			progressWake: make(chan struct{}, 1),
		}
		if err := h.machine.startDownload(rt, source); err != nil {
			cancel()
			_ = d.Close()
			_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: err.Error()})
			return nil
		}
		_ = h.r.TryResult(req.ID, mustMarshal(filesDownloadResult{
			TransferID: rt.id,
			Ticket:     rt.ticket,
			URL:        h.machine.downloadURL(rt.ticket),
			Name:       d.Name,
			Size:       d.Size,
		}))
		return nil
	})
	if err != nil {
		answerOperationRefusal(h.r, req, err)
	}
}

// handleDownloadCancel cancels one download. Idempotent by design, for
// files.uploadCancel's reason: the renderer's cancel button races the
// transfer's own completion every time, and losing that race is not a
// failure the person should be shown.
//
// It refuses to cancel an UPLOAD by id. The two ids are the same shape and
// live in the same map, so naming one here is expressible; honouring it
// would let a cancel aimed at one surface stop a transfer on another, and
// the person would watch the wrong row stop.
func (h downloadHandlers) handleDownloadCancel(ctx context.Context, state *connState, req jsonrpcRequest) {
	var params filesDownloadCancelParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	rt := h.machine.transferFor(params.TransferID)
	if rt != nil && rt.dir == dirDownload && state.has(rt.sessionID) {
		h.machine.cancelTransfer(params.TransferID)
	}
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}

// ── GET /download/{ticket} — the data half ───────────────────────────────

// downloadResponse is the claimed response, wrapped so that it can be
// bounded and, crucially, UNBLOCKED.
//
// It is uploadBody's mirror and honours the mirror half of the same
// contract. transfer.Source.Get checks cancellation between chunks and can
// never abandon a Write already in flight, so cancelling alone would leave
// a client that stopped reading holding a lease and a descriptor open
// indefinitely. Tripping the connection's WRITE deadline is what unblocks
// a blocked Write, and it is the only thing that safely can from another
// goroutine.
//
// It also owns committing the response head, and that is not tidiness. The
// status line and Content-Length cannot be revised once written, so they
// are written as late as possible: at the first byte that is actually going
// out. A transfer that dies before its first byte therefore still has a
// status left to tell the truth with.
type downloadResponse struct {
	w        http.ResponseWriter
	setWrite func(time.Time) error
	flush    func() error
	stall    time.Duration
	// commit writes the response head. Called at most once, at the first
	// byte, or by the handler for the zero-byte case.
	commit func()

	mu        sync.Mutex
	closed    bool
	committed bool
}

// errDownloadClosed is what a Write after Close reports. It carries no
// ticket; the account of what happened reaches the person as
// files.downloadDone.
var errDownloadClosed = errors.New("download: the response was closed")

func (b *downloadResponse) Write(p []byte) (int, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, errDownloadClosed
	}
	first := !b.committed
	b.committed = true
	b.mu.Unlock()
	if first {
		b.commit()
	}
	// Re-armed before EVERY write, so the bound is "no progress for
	// stall", never "the whole transfer within stall": a 2 GB download over
	// a slow link is a working download, and only a link that has stopped
	// moving is a failure.
	_ = b.setWrite(time.Now().Add(b.stall))
	n, err := b.w.Write(p)
	if err != nil {
		return n, err
	}
	// Flushed per chunk so the bytes reach the socket rather than sitting
	// in net/http's buffer: it is what makes the write deadline above
	// describe the LINK rather than a memcpy, and what lets a person see a
	// download move. A server that cannot flush is not an error — the
	// bytes still arrive, one buffer later.
	if b.flush != nil {
		_ = b.flush()
	}
	return n, nil
}

// Close unblocks a Write in flight and refuses every later one. It does not
// close the underlying connection: net/http owns that and closes it when
// the handler returns.
func (b *downloadResponse) Close() error {
	b.mu.Lock()
	already := b.closed
	b.closed = true
	b.mu.Unlock()
	if already {
		return nil
	}
	// A deadline in the past: an in-flight Write returns at once with a
	// timeout, and there is none to race with if it has not started.
	_ = b.setWrite(time.Now().Add(-time.Second))
	return nil
}

// wroteAnything reports whether the response head has been committed, which
// is what decides whether the handler may still choose a status.
func (b *downloadResponse) wroteAnything() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.committed
}

// handleDownloadFetch carries one file's bytes to a claimed ticket.
//
// The ticket is the credential: a GET cannot present a WebSocket
// subprotocol, so possession of the ticket is what authorises reading those
// bytes. That is the mirror of the sink ticket's threat model and it is not
// the same violation: a stolen sink ticket lets somebody put chosen content
// at a path, which is an integrity violation, and a stolen download ticket
// lets somebody READ a file off somebody else's server, which is a
// confidentiality one. Both are why the ticket is 256 bits from
// crypto/rand, one-shot, TTL-bounded, appears in no log line and in no
// response body, and travels in the request path only because the request
// never leaves loopback.
//
// There is deliberately NO OPTIONS route beside this one. A GET carrying no
// request header outside the CORS safelist is a simple request, so a
// browser never sends a preflight for it, and a route answering a request
// nobody makes is code nobody exercises. The response still carries the
// origin headers, because a page reading this with fetch needs them — and
// Access-Control-Expose-Headers is what lets it read the file's name off
// Content-Disposition. If a client ever needs a custom request header here,
// the preflight route has to arrive with it.
func (s *WSServer) handleDownloadFetch(w http.ResponseWriter, r *http.Request) {
	// One request per connection, on every path including the refusals.
	// The ticket is one-shot, so nothing follows a fetch here that could
	// want the connection; it also keeps "a transfer is always the FIRST
	// request on its connection" true, which is the premise the guard's
	// byte offsets are read against.
	w.Header().Set("Connection", "close")

	// Before the ticket is read out of the path, let alone claimed. An
	// origin we refuse must not learn whether a well-formed guess names a
	// live transfer, and must not consume one.
	if !s.allowTransferOrigin(w, r, downloadRoutePrefix) {
		return
	}

	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		// transfer.Source.Get is explicit: a writer that cannot be
		// unblocked at all must not be handed to it. Without a settable
		// deadline this response is exactly that, so it is refused rather
		// than started.
		s.log.Warn("download rejected", "reason", "no_write_deadline", "route", downloadRoutePrefix, "error", err)
		http.Error(w, "this connection cannot carry a download", http.StatusInternalServerError)
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

	// contentLength is not a rule in this direction, so it is passed as
	// zero and ignored; dirDownload is what refuses a SINK ticket presented
	// here, which is the one thing sharing a ticket map makes expressible.
	rt, claim := s.transfers.claim(ticket, dirDownload, 0)
	switch claim {
	case transferClaimUnknown, transferClaimFinished:
		// Both are 410, and 410 means only "this names nothing". Expiry is
		// not one of these states — the mint-side timer already cancelled
		// the transfer at expiry — so nothing here cancels anything.
		http.Error(w, "gone", http.StatusGone)
		return
	case transferClaimRunning:
		// The first claimant keeps its transfer, untouched.
		http.Error(w, "this download is already being fetched", http.StatusConflict)
		return
	case transferClaimNoLength, transferClaimSizeMismatch:
		// Unreachable: the length rules are the upload direction's and
		// claim does not apply them here. Answered rather than ignored so
		// the switch stays exhaustive.
		http.Error(w, "gone", http.StatusGone)
		return
	case transferClaimOK:
	}

	name, size := rt.downloadName(), rt.size()
	body := &downloadResponse{
		w:        w,
		setWrite: rc.SetWriteDeadline,
		flush:    rc.Flush,
		stall:    s.transfers.stallTimeout(),
		commit:   func() { writeDownloadHead(w, name, size) },
	}
	if !rt.attachWriter(body) {
		// The transfer ended between the claim and the hand-off. Nothing
		// has been committed, so the status is still ours to choose.
		http.Error(w, "gone", http.StatusGone)
		return
	}

	// The handler waits for the transfer, because net/http invalidates the
	// response writer the moment it returns and the source is writing to
	// it.
	//
	// A client that goes away CLOSES the response and does not cancel the
	// transfer, and the difference is upload's: closing is what unwinds a
	// Write the source has already blocked in, while cancelling would
	// additionally relabel the outcome — a dropped connection is a client
	// that stopped taking the file, which is a FAILURE the person should
	// see reported as one, and "cancelled" is reserved for somebody
	// actually asking.
	select {
	case <-rt.done:
	case <-r.Context().Done():
		_ = body.Close()
		<-rt.done
	}

	// The deadline is cleared before anything else is written. Cancelling
	// the transfer trips it deliberately — that is what unblocks a Write
	// already in flight — and a deadline left in the past would then make
	// the refusal below unwritable, so a person who cancelled would get a
	// dropped connection instead of a status. The one case where this
	// changes nothing is the client that went away, which is not reading
	// either way.
	_ = rc.SetWriteDeadline(time.Time{})

	state, _, _, _ := rt.snapshot()
	if body.wroteAnything() {
		// The head is written and the bytes are gone; the status cannot be
		// revised and must not be. A transfer that failed part-way is
		// visible as a body short of the Content-Length it declared, which
		// is what every HTTP client already treats as a broken transfer —
		// and the authoritative account reaches the person as
		// files.downloadDone regardless.
		return
	}
	switch state {
	case downloadStateSent:
		// An empty file is a file: zero bytes means Write was never
		// called, so the head has not been committed and this is where it
		// is. A 200 with Content-Length: 0 is the correct download of an
		// empty file, not a failure.
		writeDownloadHead(w, name, size)
	case downloadStateCancelled:
		http.Error(w, "the transfer was cancelled", http.StatusInternalServerError)
	default:
		http.Error(w, "the transfer failed", http.StatusInternalServerError)
	}
}

// attachWriter hands the claimed response to the running goroutine and
// records it so stop can unblock it. It reports false when the transfer
// ended between the claim and the hand-off.
//
// It is attach's mirror, and it writes the same rt.closer field: what stop
// has to unblock is "whichever end of this transfer the network owns", and
// that is a request body one way and a response the other.
func (rt *runningTransfer) attachWriter(body *downloadResponse) bool {
	rt.mu.Lock()
	rt.closer = body
	rt.mu.Unlock()
	select {
	case rt.dest <- body:
		return true
	case <-rt.done:
		return false
	}
}

// writeDownloadHead writes the response head. It is called at most once per
// request — from the first byte out, or from the handler for a zero-byte
// file — because a status line and a Content-Length cannot be revised.
func writeDownloadHead(w http.ResponseWriter, name string, size int64) {
	h := w.Header()
	// application/octet-stream, always. Sniffing the content to guess a
	// type would let a file on somebody's server decide how a browser
	// treats the reply, and nosniff is what stops the browser guessing for
	// itself. A download is bytes to save, never a document to render.
	h.Set("Content-Type", "application/octet-stream")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	h.Set("Content-Disposition", contentDisposition(name))
	// The reply names one file on somebody's machine at one moment and the
	// ticket that fetched it is already spent, so there is nothing here a
	// cache could ever legitimately serve again.
	h.Set("Cache-Control", "no-store")
	// Without this a cross-origin page can read the BODY and not the
	// headers, so it would receive the bytes and not the name — and under
	// `dev-web` every request here is cross-origin by construction.
	h.Set("Access-Control-Expose-Headers", "Content-Disposition, Content-Length")
	w.WriteHeader(http.StatusOK)
}

// contentDisposition builds the header that names the file, in both of the
// forms RFC 6266 asks for: a quoted ASCII fallback for clients that read
// only that, and filename* in RFC 5987 form for the real name.
//
// Sanitising is not cosmetic here, it is the header-injection defence. A
// POSIX file name may contain anything but '/' and NUL — including CR and
// LF, which would end this header and begin another one of the caller's
// choosing, and including quotes and backslashes, which would end the
// quoted string. The path was validated as absolute, clean and bounded and
// none of those checks refuse a control character, and they should not: a
// file with a newline in its name is a file, and it must be downloadable
// rather than a way to write headers.
//
// So the fallback keeps only printable ASCII minus the two characters that
// end a quoted string, and filename* percent-encodes everything outside
// RFC 5987's attr-char set, which admits no control character by
// construction.
func contentDisposition(name string) string {
	if name == "" {
		name = "download"
	}
	var ascii strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f: // control characters, CR and LF among them
			ascii.WriteByte('_')
		case r == '"' || r == '\\':
			ascii.WriteByte('_')
		case r > 0x7e:
			ascii.WriteByte('_')
		default:
			ascii.WriteRune(r)
		}
	}
	fallback := ascii.String()
	if strings.TrimLeft(fallback, "_") == "" {
		// A name that survives as nothing but underscores tells the person
		// less than a word does.
		fallback = "download"
	}
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", fallback, rfc5987(name))
}

// rfc5987 percent-encodes a name into the ext-value form of RFC 5987. The
// unreserved set is the standard's attr-char minus the characters that
// would be ambiguous inside a header parameter.
func rfc5987(s string) string {
	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$&+-.^_`|~"
	if !utf8.ValidString(s) {
		// A name that is not valid UTF-8 cannot be declared as UTF-8. The
		// bytes are still encoded, one at a time, so the person sees
		// something rather than nothing.
		s = strings.ToValidUTF8(s, "_")
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// filesDownloadSpecs declares the two download control methods. They are
// registered on the SAME bounded submission as the other binding methods,
// so a client cannot use them to get a second queue's worth of work past
// the filesystem domain's bound.
func (s *WSServer) filesDownloadSpecs(bindingOp capability.FilesystemBindingOperation, bindingSub control.Submission) []methodSpec {
	return []methodSpec{
		reg(bindingSub, "files.download", params(validateFilesDownloadRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := downloadHandlers{op: bindingOp, machine: s, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleDownload(ctx, state, req) }
		}),
		reg(bindingSub, "files.downloadCancel", params(validateFilesDownloadCancelRaw), func(w *wsConn, state *connState, r Responder) handlerFunc {
			h := downloadHandlers{op: bindingOp, machine: s, r: r}
			return func(ctx context.Context, req jsonrpcRequest) { h.handleDownloadCancel(ctx, state, req) }
		}),
	}
}

// Compile-time proof that the server is the machine the handlers ask for.
var _ downloadMachine = (*WSServer)(nil)
