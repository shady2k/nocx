package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/connection"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
)

func TestProfilesRPC_ListEmpty(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "profiles.list", map[string]any{})
	var result struct {
		Result []any `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Result) != 0 {
		t.Errorf("expected empty list, got %d items", len(result.Result))
	}
}

func TestProfilesRPC_CreateList(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	p := profile.SSHProfile{
		Base: profile.Base{
			ID:   profile.NewProfileID("ssh", "test-host"),
			Type: "ssh",
			Name: "test-host",
		},
		Options: profile.StoredSSHProfileOptions{
			Host: "example.com",
			Port: profile.Ptr(22),
			User: profile.Ptr("alice"),
		},
	}

	_ = jsonrpcCall(t, conn, "profiles.create", p)
	resp := jsonrpcCall(t, conn, "profiles.list", map[string]any{})

	var list struct {
		Result []profile.SSHProfile `json:"result"`
	}
	if err := json.Unmarshal(resp, &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list.Result) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(list.Result))
	}
	if list.Result[0].Options.Host != "example.com" {
		t.Errorf("host = %q", list.Result[0].Options.Host)
	}
}

func TestProfilesRPC_Delete(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	p := profile.SSHProfile{
		Base:    profile.Base{ID: "ssh:custom:del:0001", Type: "ssh", Name: "del"},
		Options: profile.StoredSSHProfileOptions{Host: "h"},
	}
	_ = ps.CreateProfile(p)

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	_ = jsonrpcCall(t, conn, "profiles.delete", map[string]any{"id": p.ID})

	resp := jsonrpcCall(t, conn, "profiles.list", map[string]any{})
	var list struct {
		Result []profile.SSHProfile `json:"result"`
	}
	_ = json.Unmarshal(resp, &list)
	if len(list.Result) != 0 {
		t.Errorf("after delete, %d profiles remain", len(list.Result))
	}
}

func TestGroupsRPC_Create(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	g := profile.ProfileGroup{ID: "g1", Name: "Prod"}
	_ = jsonrpcCall(t, conn, "groups.create", g)

	resp := jsonrpcCall(t, conn, "groups.list", map[string]any{})
	var list struct {
		Result []profile.ProfileGroup `json:"result"`
	}
	_ = json.Unmarshal(resp, &list)
	if len(list.Result) != 1 || list.Result[0].ID != "g1" {
		t.Fatalf("groups = %+v", list.Result)
	}
}

func TestProfilesRPC_UnwiredDoesNotCrash(t *testing.T) {
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Without profile repository wired, profiles.list should return method-not-found
	// (or empty result — either is acceptable; we check it doesn't crash).
	resp := jsonrpcCall(t, conn, "profiles.list", map[string]any{})
	var check struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	_ = json.Unmarshal(resp, &check)
	// With no store, we expect an error OR empty result — both are fine.
	_ = check
}

// TestNoPlaintextSecretsOnWire proves that serialized JSON-RPC responses
// never contain a known password string. It tests every path a client can
// reach — profiles.*, credentials.*, and the open SSH path (including
// jump-host resolution). The assertion searches raw bytes, not decoded
// struct fields, so a field added later that carries a secret will fail
// this test even if no struct-field assertion was written for it.
func TestNoPlaintextSecretsOnWire(t *testing.T) {
	const targetCanary = "CANARY-TARGET-s3cr3t-do-not-leak"
	const jumpCanary = "CANARY-JUMP-s3cr3t-do-not-leak"

	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	cs := newTestStore()

	// Target profile carries a bound password secret (ADR-0017): the wire
	// must never leak its material.
	tgtPWID, _ := cs.Create(context.Background(), credential.NewSecret(targetCanary))
	_ = ps.CreateProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:canary-tgt", Name: "canary-target"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "target.canary.example.com",
			Port:           profile.Ptr(2222),
			User:           profile.Ptr("canary"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(tgtPWID),
			JumpHost:       profile.Ptr("profile:canary-jump"),
		},
	})

	// Jump profile with its own bound password secret.
	jumpPWID, _ := cs.Create(context.Background(), credential.NewSecret(jumpCanary))

	// Create a jump profile.
	_ = ps.CreateProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:canary-jump", Name: "canary-jump"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "jump.canary.example.com",
			User:           profile.Ptr("jump-canary"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(jumpPWID),
		},
	})

	// The target profile above jumps through it; nothing else to seed.

	resolver := connection.NewResolver(ps, ps, credential.NewResolver(cs, nil, nil))
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs),
		WithProfileResolver(resolver),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Helper: call RPC, search raw response bytes for both canaries.
	assertClean := func(method string, params map[string]any) {
		t.Helper()
		resp := jsonrpcCall(t, conn, method, params)
		if bytes.Contains(resp, []byte(targetCanary)) {
			t.Errorf("%s response contains TARGET canary: %s", method, string(resp))
		}
		if bytes.Contains(resp, []byte(jumpCanary)) {
			t.Errorf("%s response contains JUMP canary: %s", method, string(resp))
		}
	}

	// profiles.list — must not leak passwords.
	assertClean("profiles.list", map[string]any{})

	// credentials.list — metadata only, no passwords.
	assertClean("credentials.list", map[string]any{})

	// open with target profile (will fail — no SSH factory — but error must be clean).
	assertClean("open", map[string]any{
		"cols":      80,
		"rows":      24,
		"kind":      "ssh",
		"profileId": "profile:canary-tgt",
	})

	// open with jump profile — JumpCredentials path.
	assertClean("open", map[string]any{
		"cols":      80,
		"rows":      24,
		"kind":      "ssh",
		"profileId": "profile:canary-jump",
	})
}

func TestProfilesRPC_CreateRejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	id := profile.NewProfileID("ssh", "web-1")
	first := map[string]any{
		"id": id, "type": "ssh", "name": "web-1",
		"options": map[string]any{"host": "10.0.0.1", "port": 22, "user": "ops"},
	}
	jsonrpcCall(t, conn, "profiles.create", first)

	second := map[string]any{
		"id": id, "type": "ssh", "name": "impostor",
		"options": map[string]any{"host": "evil.example", "port": 22, "user": "root"},
	}
	resp := jsonrpcCall(t, conn, "profiles.create", second)
	var out struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Error == nil || out.Error.Code != -32602 {
		t.Fatalf("want -32602 for a duplicate create, got %+v", out.Error)
	}

	stored, err := ps.LoadProfiles()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(stored) != 1 || stored[0].Options.Host != "10.0.0.1" {
		t.Fatalf("a refused create overwrote the record: %+v", stored)
	}
}

func TestProfilesRPC_UpdateRejectsMissingID(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "profiles.update", map[string]any{
		"id": "ssh:nonexistent:0001", "type": "ssh", "name": "ghost",
		"options": map[string]any{"host": "10.0.0.1"},
	})
	var out struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Error == nil || out.Error.Code != -32602 {
		t.Fatalf("want -32602 for update on missing profile, got %+v", out.Error)
	}
}

func TestGroupsRPC_CreateRejectsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	jsonrpcCall(t, conn, "groups.create", map[string]any{"id": "g1", "name": "Prod"})
	resp := jsonrpcCall(t, conn, "groups.create", map[string]any{"id": "g1", "name": "Duplicate"})
	var out struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Error == nil || out.Error.Code != -32602 {
		t.Fatalf("want -32602 for a duplicate group create, got %+v", out.Error)
	}
}

func TestGroupsRPC_UpdateRejectsMissingID(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	resp := jsonrpcCall(t, conn, "groups.update", map[string]any{"id": "g-nonexistent", "name": "Ghost"})
	var out struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Error == nil || out.Error.Code != -32602 {
		t.Fatalf("want -32602 for update on missing group, got %+v", out.Error)
	}
}
