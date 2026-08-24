package transport

// sandbox.profile.get / set / delete / grant.get and the open profileRevision
// gate (design 2026-08-23 §4.3): the backend resolves pane → workspace and
// composes the effective profile, never trusting a renderer-supplied source.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/sandbox"
	"github.com/shady2k/nocx/internal/settings"
)

func TestCheckSandboxProfileRevision(t *testing.T) {
	standard := effectiveSandboxProfile{Source: sandbox.ProfileSourceStandard, Revision: 3}
	workspace := effectiveSandboxProfile{Source: sandbox.ProfileSourceWorkspace, Revision: 7}

	cases := []struct {
		name    string
		eff     effectiveSandboxProfile
		rev     *int64
		wantErr bool
	}{
		{"standard accepts null", standard, nil, false},
		{"standard rejects any revision", standard, new(int64(3)), true},
		{"workspace rejects null", workspace, nil, true},
		{"workspace rejects wrong revision", workspace, new(int64(6)), true},
		{"workspace accepts exact revision", workspace, new(int64(7)), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSandboxProfileRevision(tc.rev, tc.eff)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkSandboxProfileRevision(%v, %v) = %v, wantErr %v", tc.rev, tc.eff.Source, err, tc.wantErr)
			}
		})
	}
}

func profileRPCError(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var env struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, raw)
	}
	if env.Error == nil {
		return 0
	}
	return env.Error.Code
}

func TestSandboxProfileLifecycleOverTheWire(t *testing.T) {
	base := t.TempDir()
	wDir := filepath.Join(base, "writable")
	rDir := filepath.Join(base, "reference")
	for _, d := range []string{wDir, rDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	wCanon, _ := filepath.EvalSymlinks(wDir)
	rCanon, _ := filepath.EvalSymlinks(rDir)

	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Absent profile: standard source, inherited.
	get := jsonrpcCall(t, conn, "sandbox.profile.get", map[string]any{"paneId": paneID1})
	var getResult struct {
		Result struct {
			WorkspaceID   string   `json:"workspaceId"`
			Source        string   `json:"source"`
			Revision      int64    `json:"revision"`
			Inherited     bool     `json:"inherited"`
			WritablePaths []string `json:"writablePaths"`
			ReadOnlyPaths []string `json:"readOnlyPaths"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(get, &getResult); err != nil || getResult.Error != nil {
		t.Fatalf("profile.get = %s, err %v", get, err)
	}
	if getResult.Result.WorkspaceID != wsID1 || getResult.Result.Source != "standard" || !getResult.Result.Inherited {
		t.Fatalf("profile.get = %+v", getResult.Result)
	}

	// Create an explicit profile (revision 0 → 1).
	set := jsonrpcCall(t, conn, "sandbox.profile.set", map[string]any{
		"workspaceId":      wsID1,
		"expectedRevision": 0,
		"writablePaths":    []string{wDir},
		"readOnlyPaths":    []string{rDir},
	})
	var setResult struct {
		Result struct {
			WorkspaceID   string   `json:"workspaceId"`
			Revision      int64    `json:"revision"`
			WritablePaths []string `json:"writablePaths"`
			ReadOnlyPaths []string `json:"readOnlyPaths"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(set, &setResult); err != nil || setResult.Error != nil {
		t.Fatalf("profile.set = %s, err %v", set, err)
	}
	if setResult.Result.Revision != 1 || len(setResult.Result.WritablePaths) != 1 || setResult.Result.WritablePaths[0] != wCanon {
		t.Fatalf("profile.set = %+v", setResult.Result)
	}

	// Now the effective profile is the explicit workspace profile.
	get = jsonrpcCall(t, conn, "sandbox.profile.get", map[string]any{"paneId": paneID1})
	if err := json.Unmarshal(get, &getResult); err != nil || getResult.Error != nil {
		t.Fatalf("profile.get after set = %s, err %v", get, err)
	}
	if getResult.Result.Source != "workspace" || getResult.Result.Inherited || getResult.Result.Revision != 1 {
		t.Fatalf("profile.get after set = %+v", getResult.Result)
	}
	if len(getResult.Result.ReadOnlyPaths) != 1 || getResult.Result.ReadOnlyPaths[0] != rCanon {
		t.Fatalf("profile.get after set ro = %v", getResult.Result.ReadOnlyPaths)
	}

	// Delete returns the workspace to standard inheritance.
	del := jsonrpcCall(t, conn, "sandbox.profile.delete", map[string]any{
		"workspaceId": wsID1, "expectedRevision": 1,
	})
	var delResult struct {
		Result struct {
			WorkspaceID string `json:"workspaceId"`
		} `json:"result"`
		Error *struct{ Code int } `json:"error"`
	}
	if err := json.Unmarshal(del, &delResult); err != nil || delResult.Error != nil {
		t.Fatalf("profile.delete = %s, err %v", del, err)
	}
	if delResult.Result.WorkspaceID != wsID1 {
		t.Fatalf("profile.delete = %+v", delResult.Result)
	}

	get = jsonrpcCall(t, conn, "sandbox.profile.get", map[string]any{"paneId": paneID1})
	if err := json.Unmarshal(get, &getResult); err != nil || getResult.Error != nil {
		t.Fatalf("profile.get after delete = %s, err %v", get, err)
	}
	if getResult.Result.Source != "standard" || !getResult.Result.Inherited {
		t.Fatalf("profile.get after delete = %+v", getResult.Result)
	}
}

func TestSandboxProfileSetRefusesDefaultWorkspace(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, _ := newSandboxHarness(t, svc)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	raw := jsonrpcCall(t, conn, "sandbox.profile.set", map[string]any{
		"workspaceId":      "workspace:default",
		"expectedRevision": 0,
		"writablePaths":    []string{},
		"readOnlyPaths":    []string{},
	})
	if code := profileRPCError(t, raw); code != -32602 {
		t.Fatalf("profile.set on default = code %d, want -32602 (%s)", code, raw)
	}
}

func TestSandboxGrantGetReturnsNullWithoutGrant(t *testing.T) {
	svc := &sandboxTestService{status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock}}
	ws, _ := newSandboxHarness(t, svc)
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	raw := jsonrpcCall(t, conn, "sandbox.grant.get", map[string]any{"paneId": paneID1})
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Error != nil {
		t.Fatalf("grant.get = %s, err %v", raw, err)
	}
	if string(env.Result) != "null" {
		t.Fatalf("grant.get result = %s, want null", env.Result)
	}
}

// A workspace-profile launch must carry the exact per-workspace revision; a
// stale one is refused before any PTY is prepared (design §4.3).
func TestOpenSandboxWorkspaceProfileRevisionGate(t *testing.T) {
	base := t.TempDir()
	wDir := filepath.Join(base, "workspace")
	if err := os.MkdirAll(wDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	wCanon, _ := filepath.EvalSymlinks(wDir)
	svc := &sandboxTestService{
		status: sandbox.Status{Available: true, Backend: sandbox.BackendLandlock},
		policy: &sandbox.Policy{Workspace: wCanon, WritableRoots: []string{wCanon}, ReadOnlyRoots: []string{}},
	}
	ws, reg := newSandboxHarness(t, svc)
	if err := reg.SetBool(settings.SandboxEnabled, true); err != nil {
		t.Fatalf("SetBool: %v", err)
	}
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Create an explicit workspace profile for wsID1.
	set := jsonrpcCall(t, conn, "sandbox.profile.set", map[string]any{
		"workspaceId": wsID1, "expectedRevision": 0,
		"writablePaths": []string{}, "readOnlyPaths": []string{},
	})
	if code := profileRPCError(t, set); code != 0 {
		t.Fatalf("profile.set = code %d (%s)", code, set)
	}

	rev := snapshotRevision(t, reg)
	openParams := map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
		"paneId": paneID1,
		"sandbox": map[string]any{
			"workspace":        wDir,
			"settingsRevision": rev,
			"profileRevision":  99, // stale: the stored revision is 1
		},
	}
	raw := jsonrpcCall(t, conn, "open", openParams)
	if code, _ := openError(t, raw); code != -32602 {
		t.Fatalf("open with stale profileRevision = code %d, want -32602 (%s)", code, raw)
	}

	// The exact revision is accepted (the stub service then prepares).
	openParams["sandbox"] = map[string]any{
		"workspace":        wDir,
		"settingsRevision": rev,
		"profileRevision":  1,
	}
	raw = jsonrpcCall(t, conn, "open", openParams)
	var success struct {
		Result *struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &success); err != nil || success.Error != nil || success.Result == nil || success.Result.SessionID == "" {
		t.Fatalf("open with exact profileRevision = %s (err %v)", raw, err)
	}
}
