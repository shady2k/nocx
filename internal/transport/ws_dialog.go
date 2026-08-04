package transport

import "context"

// DialogService opens native platform dialogs on behalf of the renderer. It
// is a control-plane capability (AD-1): the renderer has no path to the Wails
// runtime, so a native picker is reached through this method over the same
// WebSocket as everything else.
//
// The service is often absent. The dev-web harness has no Wails at all, and
// that is the configuration the app is developed and tested in. Absence is
// reported as a JSON-RPC -32601 error, which the renderer treats as "type the
// path by hand" rather than as a failure.
type DialogService interface {
	// OpenFile opens the platform file picker and returns the chosen
	// ABSOLUTE path, or "" when the user cancelled. The runtime's own error
	// is returned as-is.
	OpenFile(ctx context.Context) (string, error)
	// OpenDirectory opens the platform folder picker and returns the chosen
	// ABSOLUTE directory, or "" when the user cancelled (design spec §4.3).
	OpenDirectory(ctx context.Context) (string, error)
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

// handleDialogMethod routes dialog.* control-plane methods (dialog.openFile,
// dialog.openDirectory) to the native picker.
func (s *WSServer) handleDialogMethod(wconn *wsConn, req jsonrpcRequest) {
	s.dialogMu.RLock()
	ds := s.dialogService
	s.dialogMu.RUnlock()

	if ds == nil {
		_ = wconn.writeJSON(newJSONRPCError(req.ID, -32601, "dialog not available"))
		return
	}

	var (
		path string
		err  error
	)
	switch req.Method {
	case "dialog.openDirectory":
		path, err = ds.OpenDirectory(context.Background())
	default: // dialog.openFile
		path, err = ds.OpenFile(context.Background())
	}
	if err != nil {
		_ = wconn.writeJSON(rpcErrorFor(req.ID, -32603, req.Method+": ", err))
		return
	}
	resp := struct {
		Path string `json:"path"`
	}{Path: path}
	_ = wconn.writeJSON(newJSONRPCResult(req.ID, mustMarshal(resp)))
}
