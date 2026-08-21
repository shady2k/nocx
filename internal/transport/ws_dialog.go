package transport

import (
	"context"
	"sync"
)

// DialogService opens native platform dialogs on behalf of the renderer. It
// is a control-plane capability (AD-1): the renderer has no path to the Wails
// runtime, so a native file picker is reached through this method over the
// same WebSocket as everything else.
//
// The service is often absent. The dev-web harness has no Wails at all, and
// that is the configuration the app is developed and tested in. Absence is
// reported as a JSON-RPC -32601 error, which the renderer treats as "type the
// path by hand" rather than as a failure.
//
// # Cancellation — the platform adapter contract
//
// Every method here receives the connection's context, and an adapter MAY
// observe ctx.Done and dismiss its dialog where the native API allows it.
// Where the native API does not allow it (the Wails runtime's OpenFileDialog
// cannot be cancelled once shown), the adapter MUST return normally, and the
// transport then keeps the capability busy — refusing every dialog.* method
// from any connection — until the adapter actually returns. The transport
// never assumes a prompt return from a cancelled context, and an adapter must
// never assume its ctx will be cancelled at all.
//
// The capability is ONE native dialog, not one per method: the file and the
// directory picker share a single admission slot, so a directory picker
// cannot open over a file picker either.
type DialogService interface {
	// OpenFile opens the platform file picker and returns the chosen
	// ABSOLUTE path, or "" when the user cancelled. The runtime's own error
	// is returned as-is. The context may be cancelled on disconnect; see
	// the cancellation contract above.
	OpenFile(ctx context.Context) (string, error)

	// OpenDirectory opens the platform directory picker and returns the
	// chosen ABSOLUTE path, or "" when the user cancelled. The runtime's own
	// error is returned as-is. It inherits the cancellation contract above
	// verbatim — the Wails runtime's directory dialog cannot be cancelled
	// once shown any more than its file dialog can.
	OpenDirectory(ctx context.Context) (string, error)
}

// dialogServiceHolder is the transport's mutable dialog-service seam: the
// mutex and the service it guards. The service is assigned post-construction
// (SetDialogService, below) while a handler may be reading it, so the handler
// holds the holder — pointing at the WSServer's own dialog state, one mutex
// and one service shared between the setter and the readers — and reads the
// CURRENT service per call.
type dialogServiceHolder struct {
	mu  *sync.RWMutex
	svc *DialogService
}

// get returns the current dialog service, or nil when none is wired.
func (h *dialogServiceHolder) get() DialogService {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return *h.svc
}

// dialogService is set post-construction: the Wails context it needs only
// exists inside WailsApp.startup (main.go), which runs after the transport is
// built. The handler may be reading it while startup assigns it, so the field
// is mutex-guarded.
func (s *WSServer) SetDialogService(ds DialogService) {
	s.dialogMu.Lock()
	defer s.dialogMu.Unlock()
	s.dialogService = ds
}

// dialogHandlers answers dialog.openFile and dialog.openDirectory. It holds
// the dialog service holder and its Responder; nothing else. The off-loop
// machinery (dialog admission composed with the lane, inflight registration)
// lives in the registration, not in the handler.
type dialogHandlers struct {
	dialog *dialogServiceHolder
	r      Responder
}

func (h dialogHandlers) handleDialogOpenFile(ctx context.Context, req jsonrpcRequest) {
	ds := h.dialog.get()
	if ds == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "dialog not available"})
		return
	}

	// The dialog runs OFF the read loop under the dialog admission
	// (ws_control.go): a native picker can stay open for minutes, and it
	// must not freeze the socket. The task context derives from the
	// connection context so a disconnect cancels a cancel-aware adapter;
	// a NON-cooperative adapter (the real Wails runtime cannot cancel the
	// picker — see DialogService.OpenFile) keeps the admission permit
	// until it actually returns, and that held permit is what refuses a
	// second dialog.openFile from any connection: no second picker ever
	// stacks over the first. A refused submit answers the control-saturated
	// error; a dead socket's response is dropped by the Responder.
	// The dispatch admitted this task via the dialog submission (the dialog
	// admission composed with the lane, registered in the inflight set
	// BEFORE TrySubmit so shutdown cancels it and waits, bounded, for it).
	// The task context derives from the connection so a disconnect cancels
	// a cancel-aware adapter; a NON-cooperative adapter (the real Wails
	// runtime cannot cancel the picker — see DialogService.OpenFile) keeps
	// the admission permit until it actually returns, and that held permit
	// is what refuses a second dialog.openFile from any connection: no
	// second picker ever stacks over the first. A refused submit (a dialog
	// already open) was answered by the dispatcher with the control-
	// saturated error; a dead socket's response is dropped by the Responder.
	h.answerPath(req, "dialog.openFile: ", func() (string, error) { return ds.OpenFile(ctx) })
}

// handleDialogOpenDirectory answers dialog.openDirectory. It is the file
// picker's sibling in every respect the transport can see: the same absent-
// runtime -32601, the same admission (so no second picker stacks over a
// first, whichever method opened it), and the same result shape. Only the
// native call underneath differs, which is why the two share answerPath
// rather than each owning a copy of the reply.
func (h dialogHandlers) handleDialogOpenDirectory(ctx context.Context, req jsonrpcRequest) {
	ds := h.dialog.get()
	if ds == nil {
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "dialog not available"})
		return
	}
	h.answerPath(req, "dialog.openDirectory: ", func() (string, error) { return ds.OpenDirectory(ctx) })
}

// answerPath runs one native picker and writes its single reply: the runtime's
// own error as -32603 under the method's prefix, or the chosen ABSOLUTE path.
// A cancelled picker is "" and is therefore a RESULT — a caller can tell a
// dismissal from a failure by which half of the envelope arrived, never by
// inspecting the path.
func (h dialogHandlers) answerPath(req jsonrpcRequest, errPrefix string, open func() (string, error)) {
	path, err := open()
	if err != nil {
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, errPrefix, err))
		return
	}
	resp := struct {
		Path string `json:"path"`
	}{Path: path}
	_ = h.r.TryResult(req.ID, mustMarshal(resp))
}
