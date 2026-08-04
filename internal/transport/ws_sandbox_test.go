package transport

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
)

// sandboxTestService is a deterministic sandbox.Service for the wire
// contract tests: the status and Prepare outcome are scripted.
type sandboxTestService struct {
	status  sandbox.Status
	prepErr error
	policy  *sandbox.Policy
}

func (s *sandboxTestService) Status(context.Context) sandbox.Status { return s.status }

func (s *sandboxTestService) Prepare(_ context.Context, _ sandbox.Request, _ sandbox.CommandSpec) (*sandbox.PreparedCommand, error) {
	if !s.status.Available {
		return nil, &sandbox.StatusError{Status: s.status}
	}
	if s.prepErr != nil {
		return nil, s.prepErr
	}
	// A real, short-lived payload: the PTY opens, the process exits.
	cmd := exec.Command("/usr/bin/true") //nolint:gosec // fixed test payload
	return &sandbox.PreparedCommand{Cmd: cmd, Backend: s.status.Backend, Policy: s.policy}, nil
}

// sandboxPTYFactory routes the sandbox request into the real NewLocal so the
// open path is the production one (minus kernel enforcement, which the stub
// service supplies).
type sandboxPTYFactory struct {
	svc sandbox.Service
}

func (f *sandboxPTYFactory) NewPTY(_ context.Context, cfg pty.Config) (pty.Pty, error) {
	return pty.NewLocal(log.NewSlogAdapter(nil), cfg, pty.WithSandboxService(f.svc))
}

// newSandboxHarness wires a WSServer with a scripted sandbox service, a real
// settings registry (flag control), and a sandbox-aware PTY factory.
func newSandboxHarness(t *testing.T, svc *sandboxTestService) (*WSServer, *settings.Registry) {
	t.Helper()
	dir := t.TempDir()
	reg := settings.New(storage.NewDocumentStore(dir), newTestStore())
	sess := session.New(log.NewSlogAdapter(nil), &sandboxPTYFactory{svc: svc})
	ws := NewWSServer(log.NewSlogAdapter(nil), sess,
		WithSettingsRegistry(reg),
		WithSandboxService(svc))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws, reg
}

func sandboxOpenParams(workspace string) map[string]any {
	return map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0, "enhanced": true,
		"sandbox": map[string]any{"workspace": workspace},
	}
}

func openError(t *testing.T, resp json.RawMessage) (code int, reason string) {
	t.Helper()
	var env struct {
		Error *struct {
			Code int `json:"code"`
			Data *struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal response: %v (raw %s)", err, resp)
	}
	if env.Error == nil {
		t.Fatalf("expected error, got %s", resp)
	}
	reason = ""
	if env.Error.Data != nil {
		reason = env.Error.Data.Reason
	}
	return env.Error.Code, reason
}

func TestSandboxStatus_ReportsBackend(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock, ABI: 9}}
	ws, _ := newSandboxHarness(t, svc)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "sandbox.status", map[string]any{})
	var result struct {
		Result struct {
			Available bool   `json:"available"`
			Backend   string `json:"backend"`
			ABI       int    `json:"abi"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, resp)
	}
	if result.Error != nil {
		t.Fatalf("sandbox.status error: %+v", result.Error)
	}
	if !result.Result.Available || result.Result.Backend != sandbox.BackendLandlock || result.Result.ABI != 9 {
		t.Errorf("sandbox.status = %+v, want available landlock ABI 9", result.Result)
	}
}

func TestSandboxStatus_UnavailableWithoutService(t *testing.T) {
	reg := settings.New(storage.NewDocumentStore(t.TempDir()), newTestStore())
	sess := session.New(log.NewSlogAdapter(nil), &stubPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(nil))})
	ws := NewWSServer(log.NewSlogAdapter(nil), sess, WithSettingsRegistry(reg))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "sandbox.status", map[string]any{})
	var env struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error == nil || env.Error.Code != -32601 {
		t.Fatalf("expected -32601, got %s", resp)
	}
}

func TestOpen_SandboxFlagOff(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, _ := newSandboxHarness(t, svc) // flag defaults to false
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(t.TempDir()))
	code, reason := openError(t, resp)
	if code != -32010 || reason != "feature-disabled" {
		t.Errorf("code=%d reason=%q, want -32010 feature-disabled", code, reason)
	}
}

func TestOpen_SandboxSSHRejected(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	params := sandboxOpenParams(t.TempDir())
	params["kind"] = "ssh"
	params["host"] = "example.com"
	resp := jsonrpcCall(t, conn, "open", params)
	code, _ := openError(t, resp)
	if code != -32602 {
		t.Errorf("code=%d, want -32602", code)
	}
}

func TestOpen_SandboxInvalidWorkspace(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	for _, bad := range []string{"relative/path", filepath.Join(t.TempDir(), "missing")} {
		resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(bad))
		if code, _ := openError(t, resp); code != -32602 {
			t.Errorf("workspace %q: code=%d, want -32602", bad, code)
		}
	}
}

func TestOpen_SandboxHappyPath(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	if err := os.MkdirAll(ws, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	svc := &sandboxTestService{
		status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock, ABI: 9},
		policy: &sandbox.Policy{
			Workspace:     ws,
			WritableRoots: []string{ws, filepath.Join(base, "rt", "home"), filepath.Join(base, "rt", "tmp")},
		},
	}
	wsrv, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, wsrv)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(ws))
	var result struct {
		Result struct {
			SessionID string `json:"sessionId"`
			Cwd       string `json:"cwd"`
			Sandbox   *struct {
				Backend       string   `json:"backend"`
				Workspace     string   `json:"workspace"`
				WritableRoots []string `json:"writableRoots"`
			} `json:"sandbox"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, resp)
	}
	if result.Error != nil {
		t.Fatalf("open error: %+v", result.Error)
	}
	if result.Result.SessionID == "" {
		t.Fatal("no sessionId in open result")
	}
	if result.Result.Sandbox == nil {
		t.Fatal("open result missing sandbox metadata")
	}
	if result.Result.Sandbox.Backend != sandbox.BackendLandlock || result.Result.Sandbox.Workspace != ws {
		t.Errorf("sandbox = %+v, want backend landlock workspace %q", result.Result.Sandbox, ws)
	}
	if len(result.Result.Sandbox.WritableRoots) != 3 {
		t.Errorf("writableRoots = %v, want 3 roots", result.Result.Sandbox.WritableRoots)
	}
}

func TestOpen_OrdinaryResultHasNoSandbox(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", map[string]any{"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0, "enhanced": true})
	var env struct {
		Result map[string]any   `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("open error: %+v", env.Error)
	}
	if _, ok := env.Result["sandbox"]; ok {
		t.Errorf("ordinary open result must omit sandbox: %v", env.Result)
	}
}

func TestOpen_SandboxSetupFailure(t *testing.T) {
	ws := t.TempDir()
	svc := &sandboxTestService{
		status:  sandbox.Status{Available: true, Backend: sandbox.BackendLandlock},
		prepErr: sandbox.NewSetupErrorf("helper timed out"),
	}
	wsrv, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, wsrv)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(ws))
	code, reason := openError(t, resp)
	if code != -32012 || reason != "setup-failed" {
		t.Errorf("code=%d reason=%q, want -32012 setup-failed", code, reason)
	}
}

func TestOpen_SandboxBackendUnavailable(t *testing.T) {
	ws := t.TempDir()
	svc := &sandboxTestService{
		status: sandbox.Status{Available: false, Backend: sandbox.BackendLandlock, Reason: sandbox.ReasonLandlockABITooOld, ABI: 2},
	}
	wsrv, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, wsrv)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(ws))
	code, reason := openError(t, resp)
	if code != -32011 || reason != sandbox.ReasonLandlockABITooOld {
		t.Errorf("code=%d reason=%q, want -32011 %q", code, reason, sandbox.ReasonLandlockABITooOld)
	}
}
