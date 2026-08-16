package transport

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/settings"
	"github.com/shady2k/nocx/internal/storage"
)

// sandboxTestService is a deterministic, recording sandbox.Service for the
// wire contract tests: the status and Prepare outcome are scripted, and every
// Prepare call records the Request it received so tests can assert globals and
// deltas reach the backend.
type sandboxTestService struct {
	status  sandbox.Status
	prepErr error
	policy  *sandbox.Policy

	mu       sync.Mutex
	prepared int
	lastReq  sandbox.Request
}

func (s *sandboxTestService) Status(context.Context) sandbox.Status { return s.status }
func (s *sandboxTestService) NewRuntimeRoot() (string, error) {
	return sandbox.NewRuntimeRoot(os.TempDir())
}

func (s *sandboxTestService) Prepare(_ context.Context, req sandbox.Request, _ sandbox.CommandSpec) (*sandbox.PreparedCommand, error) {
	s.mu.Lock()
	s.prepared++
	s.lastReq = req
	s.mu.Unlock()
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

func (s *sandboxTestService) prepareCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepared
}

func (s *sandboxTestService) request() sandbox.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastReq
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
func newSandboxHarness(t *testing.T, svc *sandboxTestService, loggers ...log.Logger) (*WSServer, *settings.Registry) {
	t.Helper()
	logger := log.Logger(log.NewSlogAdapter(nil))
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	dir := t.TempDir()
	reg := settings.New(storage.NewDocumentStore(dir), newTestStore())
	sess := session.New(logger, &sandboxPTYFactory{svc: svc})
	ws := NewWSServer(logger, sess,
		WithSettingsRegistry(reg),
		WithSandboxService(svc))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	return ws, reg
}

// snapshotRevision reads the registry's current revision so a test can send a
// matching settingsRevision without hardcoding a count of mutations.
func snapshotRevision(t *testing.T, reg *settings.Registry) int {
	t.Helper()
	snap, err := reg.GetSnapshot()
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	return snap.Revision
}

// sandboxOpenParams builds a valid local open with a sandbox block. The
// obsolete `enhanced` member is deliberately absent: strict decoding rejects
// it.
func sandboxOpenParams(workspace string, revision int) map[string]any {
	return map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
		"sandbox": map[string]any{
			"workspace":        workspace,
			"settingsRevision": revision,
		},
	}
}

// manyStrings returns n distinct non-empty strings.
func manyStrings(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "p" + strconv.Itoa(i)
	}
	return out
}

func putOpenCodeOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, sandbox.OpenCodeIntentName)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // executable fixture
		t.Fatalf("write opencode fixture: %v", err)
	}
	t.Setenv("PATH", dir)
}

// jsonrpcCallRaw sends an "open" frame whose params are a raw JSON string, so
// tests can express shapes a Go map cannot (duplicate members, trailing JSON).
func jsonrpcCallRaw(t *testing.T, conn *websocket.Conn, method, params string) json.RawMessage {
	t.Helper()
	req := []byte(`{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`)
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, resp, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var check struct {
			ID *json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(resp, &check)
		if check.ID == nil {
			continue
		}
		return resp
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
	putOpenCodeOnPath(t)
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock, ABI: 9}}
	ws, _ := newSandboxHarness(t, svc)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "sandbox.status", map[string]any{})
	var result struct {
		Result struct {
			Available bool                 `json:"available"`
			Backend   string               `json:"backend"`
			ABI       int                  `json:"abi"`
			Intent    sandbox.IntentStatus `json:"intent"`
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
	if result.Result.Intent != (sandbox.IntentStatus{Name: sandbox.OpenCodeIntentName, Available: true}) {
		t.Errorf("sandbox.status intent = %+v, want available opencode", result.Result.Intent)
	}
}

func TestSandboxStatus_ReportsMissingOpenCode(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, _ := newSandboxHarness(t, svc)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "sandbox.status", map[string]any{})
	var result struct {
		Result struct {
			Intent sandbox.IntentStatus `json:"intent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := sandbox.IntentStatus{
		Name: sandbox.OpenCodeIntentName, Available: false, Reason: sandbox.ReasonOpenCodeNotFound,
	}
	if result.Result.Intent != want {
		t.Fatalf("intent = %+v, want %+v", result.Result.Intent, want)
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

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(t.TempDir(), 0))
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

	params := sandboxOpenParams(t.TempDir(), snapshotRevision(t, reg))
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
	rev := snapshotRevision(t, reg)

	for _, bad := range []string{"relative/path", filepath.Join(t.TempDir(), "missing")} {
		resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(bad, rev))
		if code, _ := openError(t, resp); code != -32602 {
			t.Errorf("workspace %q: code=%d, want -32602", bad, code)
		}
		if strings.Contains(string(resp), bad) {
			t.Errorf("workspace path leaked in wire error: %s", resp)
		}
	}
}

func TestOpen_SandboxHappyPath(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "workspace")
	if err := os.MkdirAll(ws, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	canon, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	svc := &sandboxTestService{
		status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock, ABI: 9},
		policy: &sandbox.Policy{
			Workspace:     canon,
			WritableRoots: []string{canon, filepath.Join(base, "rt", "home"), filepath.Join(base, "rt", "tmp")},
		},
	}
	wsrv, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, wsrv)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(ws, snapshotRevision(t, reg)))
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
	if result.Result.Sandbox.Backend != sandbox.BackendLandlock || result.Result.Sandbox.Workspace != canon {
		t.Errorf("sandbox = %+v, want backend landlock workspace %q", result.Result.Sandbox, canon)
	}
	if len(result.Result.Sandbox.WritableRoots) != 3 {
		t.Errorf("writableRoots = %v, want 3 roots", result.Result.Sandbox.WritableRoots)
	}
	// The canonical workspace drives session CWD (ADR-0034 item 9): the same
	// value reaches session.Config.Cwd and comes back as the open result's
	// cwd, matching the policy's workspace — never the raw input.
	if result.Result.Cwd != canon {
		t.Errorf("cwd = %q, want canonical workspace %q", result.Result.Cwd, canon)
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

	resp := jsonrpcCall(t, conn, "open", map[string]any{"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0})
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
	privatePath := filepath.Join(ws, "private-workspace")
	svc := &sandboxTestService{
		status:  sandbox.Status{Available: true, Backend: sandbox.BackendLandlock},
		prepErr: sandbox.NewSetupErrorf("helper rejected %s", privatePath),
	}
	var logs syncBuffer
	logger := log.NewSlogAdapter(slog.New(slog.NewJSONHandler(&logs, nil)))
	wsrv, reg := newSandboxHarness(t, svc, logger)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, wsrv)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(ws, snapshotRevision(t, reg)))
	code, reason := openError(t, resp)
	if code != -32012 || reason != "setup-failed" {
		t.Errorf("code=%d reason=%q, want -32012 setup-failed", code, reason)
	}
	if strings.Contains(string(resp), privatePath) {
		t.Errorf("private path leaked in wire response: %s", resp)
	}
	logged := logs.String()
	if strings.Contains(logged, privatePath) {
		t.Errorf("private path leaked in backend log: %s", logged)
	}
	if !strings.Contains(logged, "sandbox setup failed") {
		t.Errorf("sanitized setup diagnostic missing from backend log: %s", logged)
	}
}

func TestOpen_SandboxLaunchUnavailable(t *testing.T) {
	workspace := t.TempDir()
	svc := &sandboxTestService{
		status:  sandbox.Status{Available: true, Backend: sandbox.BackendLandlock},
		prepErr: &sandbox.LaunchError{Reason: sandbox.ReasonOpenCodeNotFound},
	}
	wsrv, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, wsrv)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(workspace, snapshotRevision(t, reg)))
	code, reason := openError(t, resp)
	if code != -32012 || reason != sandbox.ReasonOpenCodeNotFound {
		t.Fatalf("code=%d reason=%q, want -32012 %q", code, reason, sandbox.ReasonOpenCodeNotFound)
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

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(ws, snapshotRevision(t, reg)))
	code, reason := openError(t, resp)
	if code != -32011 || reason != sandbox.ReasonLandlockABITooOld {
		t.Errorf("code=%d reason=%q, want -32011 %q", code, reason, sandbox.ReasonLandlockABITooOld)
	}
}

// TestOpen_SandboxStrictShapes drives the real socket with every malformed
// sandbox shape a map can express. Every case must fail with -32602 before
// any PTY is prepared, so a malformed opt-in can never become an ordinary
// launch.
func TestOpen_SandboxStrictShapes(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	wsPath := t.TempDir()
	rev := snapshotRevision(t, reg)

	base := func(sandbox any) map[string]any {
		return map[string]any{"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0, "sandbox": sandbox}
	}
	valid := func() map[string]any {
		return map[string]any{"workspace": wsPath, "settingsRevision": rev}
	}

	cases := []struct {
		name   string
		params any
	}{
		{"sandbox null", base(nil)},
		{"sandbox not object", base("x")},
		{"sandbox unknown member", base(map[string]any{"workspace": wsPath, "settingsRevision": rev, "bogus": 1})},
		{"sandbox missing workspace", base(map[string]any{"settingsRevision": rev})},
		{"sandbox missing revision", base(map[string]any{"workspace": wsPath})},
		{"sandbox workspace wrong type", base(map[string]any{"workspace": 123, "settingsRevision": rev})},
		{"sandbox workspace null", base(map[string]any{"workspace": nil, "settingsRevision": rev})},
		{"sandbox revision null", base(map[string]any{"workspace": wsPath, "settingsRevision": nil})},
		{"sandbox revision negative", base(map[string]any{"workspace": wsPath, "settingsRevision": -1})},
		{"sandbox revision wrong type", base(map[string]any{"workspace": wsPath, "settingsRevision": "1"})},
		{"sandbox add null", base(map[string]any{"workspace": wsPath, "settingsRevision": rev, "add": nil})},
		{"sandbox add not array", base(map[string]any{"workspace": wsPath, "settingsRevision": rev, "add": "x"})},
		{"sandbox add non-string element", base(map[string]any{"workspace": wsPath, "settingsRevision": rev, "add": []any{1}})},
		{"sandbox add null element", base(map[string]any{"workspace": wsPath, "settingsRevision": rev, "add": []any{nil}})},
		{"sandbox add duplicate", base(map[string]any{"workspace": wsPath, "settingsRevision": rev, "add": []string{"a", "a"}})},
		{"sandbox add oversized", base(map[string]any{"workspace": wsPath, "settingsRevision": rev, "add": manyStrings(33)})},
		{"sandbox remove null", base(map[string]any{"workspace": wsPath, "settingsRevision": rev, "remove": nil})},
		{"sandbox remove duplicate", base(map[string]any{"workspace": wsPath, "settingsRevision": rev, "remove": []string{"a", "a"}})},
		{"outer unknown member enhanced", map[string]any{"cols": 80, "rows": 24, "enhanced": true, "sandbox": valid()}},
		{"ssh with sandbox", map[string]any{"cols": 80, "rows": 24, "kind": "ssh", "host": "example.com", "sandbox": valid()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := jsonrpcCall(t, conn, "open", tc.params)
			if code, _ := openError(t, resp); code != -32602 {
				t.Errorf("code=%d, want -32602 (resp %s)", code, resp)
			}
		})
	}
	if svc.prepareCount() != 0 {
		t.Errorf("Prepare called %d times, want 0 (no PTY on malformed sandbox)", svc.prepareCount())
	}
}

// TestOpen_SandboxDuplicateMembers drives shapes a Go map cannot: duplicate
// object keys at the outer and nested level, which must be refused as invalid
// params.
func TestOpen_SandboxDuplicateMembers(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	wsPath := t.TempDir()
	rev := strconv.Itoa(snapshotRevision(t, reg))

	cases := []struct {
		name   string
		params string
	}{
		{"outer duplicate member", `{"cols":80,"cols":80,"rows":24,"sandbox":{"workspace":"` + wsPath + `","settingsRevision":` + rev + `}}`},
		{"sandbox duplicate member", `{"cols":80,"rows":24,"sandbox":{"workspace":"` + wsPath + `","workspace":"` + wsPath + `","settingsRevision":` + rev + `}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := jsonrpcCallRaw(t, conn, "open", tc.params)
			if code, _ := openError(t, resp); code != -32602 {
				t.Errorf("code=%d, want -32602 (resp %s)", code, resp)
			}
		})
	}
}

// TestDecodeOpenParams_TrailingData covers the trailing-JSON rejection that
// cannot be expressed through the wire (the envelope's full decode extracts a
// single params value): the strict decoder itself refuses any bytes after the
// open object.
func TestDecodeOpenParams_TrailingData(t *testing.T) {
	for _, raw := range []string{
		`{"cols":80,"rows":24} {}`,
		`{"cols":80,"rows":24} 42`,
	} {
		if _, err := decodeOpenParams([]byte(raw)); err == nil {
			t.Errorf("decodeOpenParams(%q) = nil error, want trailing-data rejection", raw)
		}
	}
}

// TestSandboxGlobals_RejectsBadSnapshot pins the persisted-global failure
// class: a missing key, a wrong snapshot type, or an over-count list is
// backend state (mapped to -32012 setup-failed), never silently an empty list.
func TestSandboxGlobals_RejectsBadSnapshot(t *testing.T) {
	key := settings.SandboxAllowedWritablePaths.Key()
	cases := []struct {
		name   string
		values map[string]any
	}{
		{"missing key", map[string]any{}},
		{"wrong type", map[string]any{key: 42}},
		{"over count", map[string]any{key: manyStrings(33)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := settings.SettingsSnapshot{Values: tc.values}
			if _, err := sandboxGlobals(snap); err == nil {
				t.Fatalf("sandboxGlobals(%v) = nil error, want error", tc.values)
			}
		})
	}

	snap := settings.SettingsSnapshot{Values: map[string]any{key: []string{"/a", "/b"}}}
	got, err := sandboxGlobals(snap)
	if err != nil {
		t.Fatalf("sandboxGlobals(valid) = %v", err)
	}
	if !reflect.DeepEqual(got, []string{"/a", "/b"}) {
		t.Errorf("sandboxGlobals(valid) = %v, want deep copy of [/a /b]", got)
	}
}

// TestOpen_SandboxStaleRevision: a settingsRevision that does not match the
// snapshot revision is refused with -32602 (no data.reason) before any PTY.
func TestOpen_SandboxStaleRevision(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()
	rev := snapshotRevision(t, reg)

	resp := jsonrpcCall(t, conn, "open", sandboxOpenParams(t.TempDir(), rev+1))
	code, reason := openError(t, resp)
	if code != -32602 {
		t.Errorf("code=%d, want -32602", code)
	}
	if reason != "" {
		t.Errorf("stale revision must omit data.reason, got %q", reason)
	}
	if svc.prepareCount() != 0 {
		t.Errorf("Prepare called %d times, want 0 (no PTY on stale revision)", svc.prepareCount())
	}
}

// TestOpen_SandboxGlobalsAndDeltas: the snapshot baseline and the per-tab
// add/remove deltas reach the backend as one Request (workspace canonical,
// globals deep-copied from the snapshot, add/remove verbatim).
func TestOpen_SandboxGlobalsAndDeltas(t *testing.T) {
	base := t.TempDir()
	canon := func(p string) string {
		t.Helper()
		c, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatalf("EvalSymlinks %s: %v", p, err)
		}
		return c
	}
	ws := filepath.Join(base, "workspace")
	g1 := filepath.Join(base, "global1")
	g2 := filepath.Join(base, "global2")
	a1 := filepath.Join(base, "add1")
	for _, d := range []string{ws, g1, g2, a1} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	svc := &sandboxTestService{
		status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock},
		policy: &sandbox.Policy{Workspace: ws, WritableRoots: []string{ws}},
	}
	wsrv, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	if err := reg.SetPaths(settings.SandboxAllowedWritablePaths, []string{g1, g2}); err != nil {
		t.Fatalf("SetPaths: %v", err)
	}
	conn := connectWS(t, wsrv)
	defer func() { _ = conn.Close() }()

	params := sandboxOpenParams(ws, snapshotRevision(t, reg))
	sandbox, ok := params["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("sandbox params is %T, want map[string]any", params["sandbox"])
	}
	sandbox["add"] = []string{a1}
	sandbox["remove"] = []string{g2}
	resp := jsonrpcCall(t, conn, "open", params)
	var env struct {
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &env); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, resp)
	}
	if env.Error != nil {
		t.Fatalf("open error: %+v", env.Error)
	}

	got := svc.request()
	if got.Workspace != canon(ws) {
		t.Errorf("Workspace = %q, want canonical %q", got.Workspace, canon(ws))
	}
	if !reflect.DeepEqual(got.Global, []string{canon(g1), canon(g2)}) {
		t.Errorf("Global = %v, want [%s %s]", got.Global, canon(g1), canon(g2))
	}
	if !reflect.DeepEqual(got.Add, []string{a1}) {
		t.Errorf("Add = %v, want [%s]", got.Add, a1)
	}
	if !reflect.DeepEqual(got.Remove, []string{g2}) {
		t.Errorf("Remove = %v, want [%s]", got.Remove, g2)
	}
}
