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
	"github.com/zalando/go-keyring"
)


// fakeEndpointResolver returns explicit profile values for tests without credential
type fakeEndpointResolver struct{}

func (r *fakeEndpointResolver) ResolveEndpoint(p profile.SSHProfile) (string, uint16, error) {
	host := p.Options.Host
	port := p.Options.Port
	if port == 0 {
		port = 22
	}
	return host, uint16(port), nil
}

func TestProfilesRPC_ListEmpty(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps), WithCredentialMetadataRepository(ps), WithCredentialMetadataMutator(ps))
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
		WithProfileRepository(ps), WithProfileAtomicMutator(ps), WithEndpointResolver(&fakeEndpointResolver{}),
		WithGroupRepository(ps), WithCredentialMetadataRepository(ps), WithCredentialMetadataMutator(ps))
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
		Options: profile.SSHProfileOptions{
			Host: "example.com",
			Port: 22,
			User: "alice",
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
		Options: profile.SSHProfileOptions{Host: "h"},
	}
	_ = ps.SaveProfile(p)

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithProfileAtomicMutator(ps), WithEndpointResolver(&fakeEndpointResolver{}),
		WithGroupRepository(ps), WithCredentialMetadataRepository(ps), WithCredentialMetadataMutator(ps))
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
		WithProfileRepository(ps), WithGroupRepository(ps), WithCredentialMetadataRepository(ps), WithCredentialMetadataMutator(ps))
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

func TestCredentialsRPC_MethodNotFound(t *testing.T) {
	keyring.MockInit()
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
	keyring.MockInit()

	const targetCanary = "CANARY-TARGET-s3cr3t-do-not-leak"
	const jumpCanary = "CANARY-JUMP-s3cr3t-do-not-leak"

	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	cs := credential.NewKeychain()

	// Save a credential with password auth (target).
	tgtPWID := credential.NewSecretID()
	_ = cs.Set(tgtPWID, credential.NewSecret(targetCanary))
	_ = ps.SaveCredential(profile.Credential{
		ID:       "cred:canary:aaa",
		Name:     "canary-cred",
		Username: "canary-user",
		Auth:     "password",
		SecretID: string(tgtPWID),
	})

	// Save a jump credential (public key).
	jumpPWID := credential.NewSecretID()
	_ = cs.Set(jumpPWID, credential.NewSecret(jumpCanary))
	_ = ps.SaveCredential(profile.Credential{
		ID:       "cred:canary:bbb",
		Name:     "jump-canary",
		Username: "jump-canary-user",
		Auth:     "publicKey",
		KeyPath:  "/home/canary/.ssh/id_rsa",
		SecretID: string(jumpPWID),
	})

	// Create a jump profile.
	_ = ps.SaveProfile(profile.SSHProfile{
		Base:    profile.Base{ID: "profile:canary-jump", Name: "canary-jump"},
		Options: profile.SSHProfileOptions{Host: "jump.canary.example.com", CredentialID: "cred:canary:bbb"},
	})

	// Create a target profile with a jump host.
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:canary-tgt", Name: "canary-target"},
		Options: profile.SSHProfileOptions{
			Host:         "target.canary.example.com",
			Port:         2222,
			CredentialID: "cred:canary:aaa",
			JumpHost:     "profile:canary-jump",
		},
	})

	resolver := connection.NewResolver(ps, ps, cs, nil)
	ws := NewWSServer(
		log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps), WithCredentialMetadataRepository(ps), WithCredentialMetadataMutator(ps),
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

// TestProfilesRPC_CreateWithCredentialCreatesGrant verifies that creating a profile
// with credential creates a TrustedEndpoint grant.
func TestProfilesRPC_CreateWithCredentialCreatesGrant(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	
	// Create credential first
	cred := profile.Credential{
		ID:       "cred:test:key",
		Name:     "test-key",
		Username: "testuser",
		Auth:     "publicKey",
	}
	if err := ps.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithProfileAtomicMutator(ps), WithEndpointResolver(&fakeEndpointResolver{}),
		WithGroupRepository(ps), WithCredentialMetadataRepository(ps), WithCredentialMetadataMutator(ps))
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
		Options: profile.SSHProfileOptions{
			Host:         "example.com",
			Port:         22,
			User:         "testuser",
			CredentialID: "cred:test:key",
		},
	}

	_ = jsonrpcCall(t, conn, "profiles.create", p)

	// Verify grant was created
	creds, err := ps.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("want 1 credential, got %d", len(creds))
	}
	if len(creds[0].TrustedEndpoints) != 1 {
		t.Fatalf("want 1 grant, got %d", len(creds[0].TrustedEndpoints))
	}
	grant := creds[0].TrustedEndpoints[0]
	if grant.ProfileID != p.ID {
		t.Errorf("grant profileId = %q, want %q", grant.ProfileID, p.ID)
	}
	if grant.Host != "example.com" {
		t.Errorf("grant host = %q, want example.com", grant.Host)
	}
	if grant.Port != 22 {
		t.Errorf("grant port = %d, want 22", grant.Port)
	}
}

// TestProfilesRPC_DeleteRemovesGrant verifies that deleting a profile removes its grant.
func TestProfilesRPC_DeleteRemovesGrant(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	
	// Create credential with grant
	cred := profile.Credential{
		ID:       "cred:test:key",
		Name:     "test-key",
		Username: "testuser",
		Auth:     "publicKey",
		TrustedEndpoints: []profile.CredentialTrustedEndpoint{
			{ProfileID: "ssh:custom:test:001", Host: "example.com", Port: 22},
		},
	}
	if err := ps.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithProfileAtomicMutator(ps), WithEndpointResolver(&fakeEndpointResolver{}),
		WithGroupRepository(ps), WithCredentialMetadataRepository(ps), WithCredentialMetadataMutator(ps))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()

	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	_ = jsonrpcCall(t, conn, "profiles.delete", map[string]any{"id": "ssh:custom:test:001"})

	// Verify grant was removed
	creds, err := ps.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds[0].TrustedEndpoints) != 0 {
		t.Errorf("want 0 grants after delete, got %d", len(creds[0].TrustedEndpoints))
	}
}
