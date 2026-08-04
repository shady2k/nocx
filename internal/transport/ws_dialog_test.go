package transport

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeDialogService returns canned paths; "" means the user cancelled.
type fakeDialogService struct {
	filePath      string
	fileErr       error
	directoryPath string
	directoryErr  error
}

func (f *fakeDialogService) OpenFile(_ context.Context) (string, error) {
	return f.filePath, f.fileErr
}

func (f *fakeDialogService) OpenDirectory(_ context.Context) (string, error) {
	return f.directoryPath, f.directoryErr
}

// dialog.openFile is a control-plane capability (AD-1): the renderer cannot
// reach the Wails runtime, so it asks the backend. The runtime is often
// absent — the dev-web harness has no Wails at all — and the method must
// report itself unavailable rather than fail, so the surface can degrade to
// typing the path by hand.
func TestDialogOpenFile_UnavailableWithoutService(t *testing.T) {
	h := newInventoryHarness(t)
	resp := jsonrpcCall(t, h.conn, "dialog.openFile", map[string]any{})
	var errResult struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResult); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResult.Error == nil {
		t.Fatalf("expected 'not available' error, got %s", string(resp))
	}
	if errResult.Error.Code != -32601 {
		t.Errorf("code = %d, want -32601 (method not available)", errResult.Error.Code)
	}
}

func TestDialogOpenDirectory_UnavailableWithoutService(t *testing.T) {
	h := newInventoryHarness(t)
	resp := jsonrpcCall(t, h.conn, "dialog.openDirectory", map[string]any{})
	var errResult struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResult); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResult.Error == nil {
		t.Fatalf("expected 'not available' error, got %s", string(resp))
	}
	if errResult.Error.Code != -32601 {
		t.Errorf("code = %d, want -32601 (method not available)", errResult.Error.Code)
	}
}

func TestDialogOpenFile_ReturnsAbsolutePath(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{filePath: "/home/dev/.ssh/id_ed25519"})

	resp := jsonrpcCall(t, h.conn, "dialog.openFile", map[string]any{})
	var result struct {
		Result struct {
			Path string `json:"path"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Error != nil {
		t.Fatalf("dialog.openFile: %+v", result.Error)
	}
	if result.Result.Path != "/home/dev/.ssh/id_ed25519" {
		t.Errorf("path = %q, want %q", result.Result.Path, "/home/dev/.ssh/id_ed25519")
	}
}

// A cancelled picker yields an empty path — the renderer treats "" as "no
// change", never as an error.
func TestDialogOpenFile_CancelledYieldsEmptyPath(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{filePath: ""})

	resp := jsonrpcCall(t, h.conn, "dialog.openFile", map[string]any{})
	var result struct {
		Result struct {
			Path string `json:"path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Result.Path != "" {
		t.Errorf("path = %q, want empty on cancel", result.Result.Path)
	}
}

func TestDialogOpenDirectory_RoutesToDirectoryPicker(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{
		filePath:      "/wrong-file-picker",
		directoryPath: "/home/dev/projects/nocx",
	})

	resp := jsonrpcCall(t, h.conn, "dialog.openDirectory", map[string]any{})
	var result struct {
		Result struct {
			Path string `json:"path"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Error != nil {
		t.Fatalf("dialog.openDirectory: %+v", result.Error)
	}
	if result.Result.Path != "/home/dev/projects/nocx" {
		t.Errorf("path = %q, want directory path", result.Result.Path)
	}
}

func TestDialogOpenDirectory_CancelledYieldsEmptyPath(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{directoryPath: ""})

	resp := jsonrpcCall(t, h.conn, "dialog.openDirectory", map[string]any{})
	var result struct {
		Result struct {
			Path string `json:"path"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Result.Path != "" {
		t.Errorf("path = %q, want empty on cancel", result.Result.Path)
	}
}

func TestDialogOpenDirectory_ReportsPickerError(t *testing.T) {
	h := newInventoryHarness(t)
	h.ws.SetDialogService(&fakeDialogService{directoryErr: errors.New("directory picker failed")})

	resp := jsonrpcCall(t, h.conn, "dialog.openDirectory", map[string]any{})
	var result struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Error == nil {
		t.Fatalf("expected picker error, got %s", string(resp))
	}
	if result.Error.Code != -32603 {
		t.Errorf("code = %d, want -32603", result.Error.Code)
	}
}
