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
// OpenFile receives the connection's context, and an adapter MAY observe
// ctx.Done and dismiss its dialog where the native API allows it. Where the
// native API does not allow it (the Wails runtime's OpenFileDialog cannot be
// cancelled once shown), the adapter MUST return normally, and the transport
// then keeps the capability busy — refusing every dialog.openFile from any
// connection — until the adapter actually returns. The transport never
// assumes a prompt return from a cancelled context, and an adapter must never
// assume its ctx will be cancelled at all.
type DialogService interface {
	// OpenFile opens the platform file picker and returns the chosen
	// ABSOLUTE path, or "" when the user cancelled. The runtime's own error
	// is returned as-is. The context may be cancelled on disconnect; see
	// the cancellation contract above.
	OpenFile(ctx context.Context) (string, error)
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

// dialogHandlers answers dialog.openFile. It holds the dialog service holder
// and its Responder; nothing else. The off-loop machinery (dialog admission
// composed with the lane, inflight registration) lives in the registration,
// not in the handler.
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
	path, err := ds.OpenFile(ctx)
	if err != nil {
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "dialog.openFile: ", err))
		return
	}
	resp := struct {
		Path string `json:"path"`
	}{Path: path}
	_ = h.r.TryResult(req.ID, mustMarshal(resp))
}

// UploadPicker is the OPTIONAL half of the native dialog seam: a picker
// that mints a source ticket instead of returning a path (R2). It is a
// second interface rather than a second method on DialogService because
// dialog.openFile has existing callers whose whole answer is a path — the
// key-material input types one into a field — and upload has the opposite
// requirement: the renderer must never learn where the file came from.
// One seam, two questions, and the adapter answers whichever it can.
//
// A DialogService that does not implement this reports the method
// unavailable, exactly as a missing service does. That is the honest
// degrade: no Wails means no native picker, and there is nothing to invent
// in its place.
type UploadPicker interface {
	// OpenFileForUpload opens the platform file picker and returns the
	// ticket, base name and size of the chosen file, or the zero value
	// when the user cancelled. The cancellation contract is
	// DialogService.OpenFile's, unchanged: this call shares the
	// capacity-one dialog admission with it, so no second picker can stack
	// over the first.
	OpenFileForUpload(ctx context.Context) (SourcePick, error)
}

// handleDialogOpenFileForUpload answers dialog.openFileForUpload: the
// native picker as an upload source. It is a sibling of
// handleDialogOpenFile, not a replacement — the two differ in exactly one
// thing, which is the only thing that matters here: this one never returns
// a path.
//
// The mint happens behind the seam, at the moment the human chose the file.
// Nothing on the wire reaches it: the method takes no params, and the
// result carries a ticket the renderer can echo and could not have written.
func (h dialogHandlers) handleDialogOpenFileForUpload(ctx context.Context, req jsonrpcRequest) {
	picker, ok := h.dialog.get().(UploadPicker)
	if !ok {
		// Both absences land here and mean the same thing to the renderer:
		// no service at all (dev-web, where the type assertion on a nil
		// interface fails), or a service whose platform cannot mint.
		_ = h.r.TryError(req.ID, RPCError{Code: -32601, Message: "native file picker not available"})
		return
	}

	// Off the read loop under the dialog admission, for the reason
	// handleDialogOpenFile states at length: a native picker can stay open
	// for minutes and must not freeze the socket, and the held permit is
	// what stops a second picker stacking over the first.
	pick, err := picker.OpenFileForUpload(ctx)
	if err != nil {
		// rpcErrorFor carries the adapter's own message. The adapter is
		// contracted not to put the path in it (ws_upload_source.go), and
		// the store's refusals are worded that way already.
		_ = h.r.TryError(req.ID, rpcErrorFor(-32603, "dialog.openFileForUpload: ", err))
		return
	}
	_ = h.r.TryResult(req.ID, mustMarshal(pick))
}
