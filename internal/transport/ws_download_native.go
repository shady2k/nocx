package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/shady2k/nocx/internal/transfer"
	"github.com/shady2k/nocx/internal/transport/control"
)

// DownloadSaveTarget is the backend-only answer to the native save dialog.
// It deliberately contains the already-injected destination capability and
// transfer instruction: no local path crosses the renderer or the wire.
type DownloadSaveTarget struct {
	Sink   transfer.Sink
	Upload transfer.Upload
}

func (t DownloadSaveTarget) validate(size int64) error {
	if t.Sink == nil {
		return errors.New("native download destination has no sink")
	}
	if t.Upload.Size != size {
		return errors.New("native download destination has the wrong size")
	}
	return t.Upload.Validate()
}

// DownloadSavePicker is the optional desktop half of DialogService. Browser
// and dev-web compositions omit it; the Wails adapter returns a local durable
// Sink target or nil when the person cancels the platform dialog.
type DownloadSavePicker interface {
	PickDownloadSave(ctx context.Context, name string, size int64) (*DownloadSaveTarget, error)
}

type filesDownloadSaveParams struct {
	TransferID string `json:"transferId"`
}

func validateFilesDownloadSaveRaw(raw json.RawMessage) string {
	var p filesDownloadSaveParams
	if msg := decodeParamsStrict(raw, &p); msg != "" {
		return msg
	}
	if !isLowerHex(p.TransferID, 32) {
		return "transferId is required and must be the 32-hex id the backend minted"
	}
	return ""
}

var errNativeDownloadSaveWire = errors.New("native download save failed")

type downloadDestinationResult struct {
	sent    int64
	outcome transfer.Outcome
	state   string
	// err is backend-only and may name the selected destination; wireErr is
	// the path-free terminal reason retained for the renderer.
	err     error
	wireErr error
}

type downloadDestination interface {
	receive(context.Context, transfer.Source, *transfer.Download, func(int64)) downloadDestinationResult
}

type writerDownloadDestination struct{ writer io.Writer }

func (d writerDownloadDestination) receive(
	ctx context.Context,
	source transfer.Source,
	download *transfer.Download,
	progress func(int64),
) downloadDestinationResult {
	sent, err := source.Get(ctx, download, d.writer, progress)
	return downloadDestinationResult{sent: sent, state: downloadStateOf(err, ctx.Err()), err: err, wireErr: err}
}

type sinkDownloadDestination struct{ target DownloadSaveTarget }

func (d sinkDownloadDestination) receive(
	ctx context.Context,
	source transfer.Source,
	download *transfer.Download,
	progress func(int64),
) downloadDestinationResult {
	sent, outcome, err := transfer.SaveDownload(
		ctx,
		source,
		download,
		d.target.Sink,
		d.target.Upload,
		progress,
	)
	state := downloadStateOf(err, ctx.Err())
	wireErr := err
	if state == downloadStateFailed {
		wireErr = errNativeDownloadSaveWire
	}
	return downloadDestinationResult{
		sent: sent, outcome: outcome, state: state,
		err: err, wireErr: wireErr,
	}
}

type failedDownloadDestination struct{ err error }

func (d failedDownloadDestination) receive(
	context.Context,
	transfer.Source,
	*transfer.Download,
	func(int64),
) downloadDestinationResult {
	return downloadDestinationResult{
		state: downloadStateFailed, err: d.err, wireErr: errNativeDownloadSaveWire,
	}
}

func (s *WSServer) claimDownloadSave(transferID string, state *connState) (*runningTransfer, transferClaim) {
	rt := s.transfers.get(transferID)
	if rt == nil || rt.dir != dirDownload || !state.has(rt.sessionID) {
		return nil, transferClaimUnknown
	}
	return s.transfers.claim(rt.ticket, dirDownload, -1)
}

func (rt *runningTransfer) attachDestination(destination downloadDestination) bool {
	select {
	case rt.dest <- destination:
		return true
	case <-rt.done:
		return false
	}
}

type downloadSaveMachine interface {
	claimDownloadSave(transferID string, state *connState) (*runningTransfer, transferClaim)
}

type downloadSaveHandlers struct {
	dialog  *dialogServiceHolder
	admit   control.Admission
	submit  control.Submission
	machine downloadSaveMachine
	r       Responder
}

func (h downloadSaveHandlers) handleDownloadSave(ctx context.Context, state *connState, req jsonrpcRequest) {
	var params filesDownloadSaveParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params"})
		return
	}
	if ctx.Err() != nil {
		return
	}

	// This handler runs through ImmediateSubmission and does only the cheap
	// linearization here. Claiming before the rejectable dialog queue means a
	// saturated queue cannot leave a pinned handle waiting for ticket TTL.
	rt, claim := h.machine.claimDownloadSave(params.TransferID, state)
	if claim != transferClaimOK {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: download is unknown or already running"})
		return
	}
	if ctx.Err() != nil {
		// ImmediateSubmission runs on the WebSocket read loop. CancelFunc is
		// nonblocking; runDownload owns closing the pinned remote handle on its
		// goroutine. rt.stop would perform unconstrained SSH Close I/O here.
		rt.cancel()
		return
	}

	picker, ok := h.dialog.get().(DownloadSavePicker)
	if !ok {
		err := errors.New("native download save not available")
		rt.attachDestination(failedDownloadDestination{err: err})
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: err.Error()})
		return
	}

	task := control.Task{Run: func(context.Context) {
		h.runClaimedDownloadSave(rt, picker, req)
	}}
	if rej := h.submit.TrySubmit(rt.ctx, task); rej != nil {
		err := fmt.Errorf("download save queue: %s", rej.Reason)
		rt.attachDestination(failedDownloadDestination{err: err})
		_ = h.r.TryError(req.ID, saturationRPCError(req.Method, rej))
	}
}

func (h downloadSaveHandlers) runClaimedDownloadSave(
	rt *runningTransfer,
	picker DownloadSavePicker,
	req jsonrpcRequest,
) {
	// The claim closed the connection-owned authorization interval. The task
	// and dialog are session-owned, like the browser fetch: reconnect never
	// cancels them, while session close and shutdown cancel rt.ctx.
	opCtx := rt.ctx
	permit, rej := h.admit.TryAcquire(opCtx)
	if rej != nil {
		if opCtx.Err() != nil {
			_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
			return
		}
		err := fmt.Errorf("download save dialog: %s", rej.Reason)
		rt.attachDestination(failedDownloadDestination{err: err})
		_ = h.r.TryError(req.ID, saturationRPCError(req.Method, rej))
		return
	}
	defer permit.Release()

	target, err := picker.PickDownloadSave(opCtx, rt.download.Name, rt.download.Size)
	if err != nil {
		if opCtx.Err() != nil {
			_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
			return
		}
		rt.attachDestination(failedDownloadDestination{err: err})
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "files.downloadSave: native download save failed"})
		return
	}
	if target == nil {
		rt.stop()
		_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
		return
	}
	if err := target.validate(rt.download.Size); err != nil {
		rt.attachDestination(failedDownloadDestination{err: err})
		_ = h.r.TryError(req.ID, RPCError{Code: -32603, Message: "files.downloadSave: native download save failed"})
		return
	}
	if opCtx.Err() != nil {
		rt.stop()
		_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
		return
	}
	if !rt.attachDestination(sinkDownloadDestination{target: *target}) {
		_ = h.r.TryError(req.ID, RPCError{Code: -32602, Message: "Invalid params: download is no longer running"})
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(struct{}{}))
}
