package transport

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

// seedSecretUsageStore writes a profile with a direct password binding and a
// profile inheriting one from its group, then returns the WSServer harness.
func seedSecretUsageStore(t *testing.T) (*profile.JSONStore, *fakeVaultLifecycle, *websocket.Conn, *WSServer) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))

	if err := ps.CreateGroup(profile.ProfileGroup{
		ID: "g1", Name: "Prod",
		Defaults: &profile.ProfileDefaults{
			SparseSSHOptions: profile.SparseSSHOptions{
				PasswordSecret: profile.Ptr("sec:group:1"),
			},
		},
	}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	for _, p := range []profile.SSHProfile{
		{
			Base:    profile.Base{ID: "p-direct", Type: "ssh", Name: "direct", Group: "g1"},
			Options: profile.StoredSSHProfileOptions{Host: "direct.example.com", PasswordSecret: "sec:direct:1"},
		},
		{
			Base:    profile.Base{ID: "p-inherit", Type: "ssh", Name: "inherit", Group: "g1"},
			Options: profile.StoredSSHProfileOptions{Host: "inherit.example.com"},
		},
	} {
		if err := ps.CreateProfile(p); err != nil {
			t.Fatalf("CreateProfile %s: %v", p.ID, err)
		}
	}

	life := &fakeVaultLifecycle{
		state:           vault.StateUnsealed,
		resolveRowID:    credential.SecretID("sec:direct:1"),
		resolveRowFound: true,
	}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithVaultLifecycle(life))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })
	return ps, life, conn, ws
}

func TestSecretUsageRPC_DirectAndGroupInheritance(t *testing.T) {
	_, life, conn, _ := seedSecretUsageStore(t)

	// The row for the DIRECT binding: one profile, sourced from the profile.
	life.resolveRowID = credential.SecretID("sec:direct:1")
	resp := jsonrpcCall(t, conn, "secrets.usage", map[string]any{"row": "secrow:direct"})
	var direct struct {
		Result struct {
			Profiles []profile.ProfileRef `json:"profiles"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &direct); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if len(direct.Result.Profiles) != 1 {
		t.Fatalf("direct profiles = %d, want 1", len(direct.Result.Profiles))
	}
	if direct.Result.Profiles[0].ProfileID != "p-direct" || direct.Result.Profiles[0].Source != "profile" {
		t.Errorf("direct ref = %+v, want p-direct via profile", direct.Result.Profiles[0])
	}

	// The row for the GROUP binding: inherited, sourced from the group.
	life.resolveRowID = credential.SecretID("sec:group:1")
	resp = jsonrpcCall(t, conn, "secrets.usage", map[string]any{"row": "secrow:group"})
	var inherited struct {
		Result struct {
			Profiles []profile.ProfileRef `json:"profiles"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &inherited); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	// Only p-inherit uses the group secret: p-direct carries its own, which
	// wins over the group default.
	if len(inherited.Result.Profiles) != 1 {
		t.Fatalf("inherited profiles = %d, want 1", len(inherited.Result.Profiles))
	}
	ref := inherited.Result.Profiles[0]
	if ref.ProfileID != "p-inherit" || ref.Source != "group" {
		t.Errorf("inherited ref = %+v, want p-inherit via group", ref)
	}
}

func TestSecretUsageRPC_UnknownRowReturnsEmpty(t *testing.T) {
	_, life, conn, _ := seedSecretUsageStore(t)
	life.resolveRowFound = false

	resp := jsonrpcCall(t, conn, "secrets.usage", map[string]any{"row": "secrow:nonexistent"})
	var result struct {
		Result struct {
			Profiles []profile.ProfileRef `json:"profiles"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Result.Profiles == nil || len(result.Result.Profiles) != 0 {
		t.Errorf("unknown row profiles = %v, want empty non-nil list", result.Result.Profiles)
	}
}

func TestSecretUsageRPC_Unwired(t *testing.T) {
	// Without a vault lifecycle, secrets.usage reports itself unavailable.
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })

	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	resp := jsonrpcCall(t, conn, "secrets.usage", map[string]any{"row": "secrow:x"})
	var check struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &check); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if check.Error == nil {
		t.Fatal("expected error for unwired secrets.usage")
	}
	if check.Error.Code != -32601 {
		t.Errorf("error code = %d, want -32601", check.Error.Code)
	}
}
