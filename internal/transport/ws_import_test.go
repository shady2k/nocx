package transport

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/importer"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"

	"golang.org/x/crypto/pbkdf2"
)

// ── helpers ────────────────────────────────────────────────────────────

// encryptTabbyVaultForTest encrypts a plaintext JSON string into a TabbyVault
// using the same format as Tabby's vault.service.ts.
func encryptTabbyVaultForTest(t *testing.T, plaintext, passphrase string) importer.TabbyVault {
	t.Helper()
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		t.Fatalf("rand.Read (salt): %v", err)
	}
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand.Read (iv): %v", err)
	}
	key := pbkdf2.Key([]byte(passphrase), salt, 100_000, 32, sha512.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	padded := pkcs7Pad([]byte(plaintext), aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	return importer.TabbyVault{
		Version:   1,
		Encrypted: true,
		Contents:  base64.StdEncoding.EncodeToString(ciphertext),
		KeySalt:   hex.EncodeToString(salt),
		IV:        hex.EncodeToString(iv),
	}
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padLen := blockSize - len(data)%blockSize
	pad := make([]byte, padLen)
	for i := range pad {
		pad[i] = byte(padLen)
	}
	return append(data, pad...)
}

// buildImportConfigYAML builds a Tabby config YAML string with the given vault.
func buildImportConfigYAML(t *testing.T, vault *importer.TabbyVault, secretCount int) string {
	t.Helper()
	header := `version: 1
profiles:
  - id: "ssh:custom:p1"
    type: "ssh"
    name: "web-01"
    options:
      host: "web.example.com"
      port: 22
      user: "deploy"
groups:
  - id: "g:prod"
    name: "Production"
`
	if vault != nil {
		header += fmt.Sprintf("vault:\n  version: %d\n  encrypted: %t\n  contents: %q\n  keySalt: %q\n  iv: %q\n",
			vault.Version, vault.Encrypted, vault.Contents, vault.KeySalt, vault.IV)
	}
	return header
}

// failAfterStore fails Create calls after the Nth success.
type failAfterStore struct {
	mu        sync.Mutex
	successes int
	failAfter int
	err       error
	created   []string // values created so far
}

func newFailAfterStore(failAfter int) *failAfterStore {
	return &failAfterStore{failAfter: failAfter, err: context.DeadlineExceeded}
}

func (f *failAfterStore) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes++
	if f.successes > f.failAfter {
		return "", f.err
	}
	var val string
	if err := value.Use(func(b []byte) error {
		val = string(b)
		return nil
	}); err != nil {
		return "", err
	}
	id := credential.SecretID("test:" + val)
	f.created = append(f.created, val)
	return id, nil
}

func (f *failAfterStore) Get(_ context.Context, _ credential.SecretID) (credential.Secret, error) {
	return credential.Secret{}, nil
}

func (f *failAfterStore) Delete(_ context.Context, _ credential.SecretID) error {
	return nil
}

func (f *failAfterStore) Exists(_ context.Context, _ credential.SecretID) (bool, error) {
	return false, nil
}

func TestImportTabby_NoVault(t *testing.T) {
	// Plain config with no vault — existing behavior.
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(newTestStore()), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	config := `version: 1
profiles:
  - id: "ssh:custom:p1"
    type: "ssh"
    name: "web-01"
    options:
      host: "web.example.com"
      port: 22
      user: "deploy"
`
	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config": config,
	})
	var result struct {
		Result int `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Result != 1 {
		t.Errorf("expected 1 profile imported, got %d", result.Result)
	}
	profs, _ := ps.LoadProfiles()
	if len(profs) != 1 {
		t.Errorf("expected 1 profile in store, got %d", len(profs))
	}
}

func TestImportTabby_EncryptedVaultNoPassphrase(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(newTestStore()), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	vault := encryptTabbyVaultForTest(t, `{"config":null,"secrets":[]}`, "pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config": config,
		// no passphrase
	})
	var errResp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResp.Error == nil {
		t.Fatal("expected error for encrypted vault without passphrase, got success")
	}
	if errResp.Error.Code != -32603 {
		t.Errorf("expected code -32603, got %d", errResp.Error.Code)
	}
}

func TestImportTabby_WrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(newTestStore()), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Encrypt with "correct-pw" but decrypt with "wrong-pw".
	vault := encryptTabbyVaultForTest(t, `{"config":null,"secrets":[]}`, "correct-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config":     config,
		"passphrase": "wrong-pw",
	})
	var errResp struct {
		Error *struct {
			Code int `json:"code"`
			Data *struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResp.Error == nil {
		t.Fatal("expected error for wrong passphrase, got success")
	}
}

func TestImportTabby_HappyPath(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	cs := newTestStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs),
		WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Vault with a password secret matching the profile's host+port+user and a
	// key-passphrase secret.
	//
	// Both key shapes are copied from upstream — tabby-ssh's
	// passwordStorage.service.ts, getVaultKeyForConnection ({user, host, port})
	// and getVaultKeyForPrivateKey ({hash}). The passphrase key was a plain
	// string here until it was checked against Tabby: that shape is one no
	// Tabby ever writes, and the importer it was written alongside agreed with
	// it, so the pair was self-consistent and wrong together.
	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"},
		{"type":"ssh:key-passphrase","key":{"hash":"9f2c4e1a77b0d3e5"},"value":"passphrase-val"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	// Verify fixture parses before RPC.
	cfg, err := importer.ParseTabbyConfig([]byte(config))
	if err != nil {
		t.Fatalf("fixture config parse: %v", err)
	}
	t.Logf("parsed vault: encrypted=%v, nil=%v", cfg.Vault != nil && cfg.Vault.Encrypted, cfg.Vault == nil)
	if cfg.Vault == nil || !cfg.Vault.Encrypted {
		t.Fatal("fixture vault not parsed as encrypted — YAML fixture is wrong")
	}
	t.Logf("profiles: %+v", cfg.Profiles[0])

	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config":     config,
		"passphrase": "test-pw",
	})
	var result struct {
		Result int `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Result != 1 {
		t.Errorf("expected 1 profile imported, got %d", result.Result)
	}

	// The profile carries the minted password binding directly (ADR-0017):
	// no credential record is created, and the secret exists in the store.
	profs, _ := ps.LoadProfiles()
	if len(profs) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profs))
	}
	p := profs[0]
	if p.Options.PasswordSecret == "" {
		t.Error("profile should have PasswordSecret set from vault secret matching")
	}
	if p.NeedsReview {
		t.Error("imported profile should not be marked for review")
	}
	if _, err := cs.Get(context.Background(), credential.SecretID(p.Options.PasswordSecret)); err != nil {
		t.Errorf("bound password secret %q: %v", p.Options.PasswordSecret, err)
	}
}

// A secret type we do not handle is skipped, and the rest of the import still
// happens.
//
// Tabby's vault is shared by every plugin the user has installed, so an
// unfamiliar type is ordinary, not exceptional. Failing the whole call on one
// would throw away the profiles and groups that converted fine — and the user
// would see a Tabby import that simply refuses, with no way to tell which of
// their secrets caused it.
func TestImportTabby_UnhandledSecretTypeIsSkipped(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(newTestStore()), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// One secret we handle, one we do not.
	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:totp","key":"web.example.com","value":"123456"},
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config":     config,
		"passphrase": "pw",
	})
	var got struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if got.Error != nil {
		t.Fatalf("an unhandled secret type must not fail the import: %s", got.Error.Message)
	}

	// The handled secret still arrived, bound to its profile (ADR-0017):
	// no credential record is created, but the profile carries the minted
	// password.
	profs, err := ps.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(profs) != 1 || profs[0].Options.PasswordSecret == "" {
		t.Fatalf("expected the password secret to import despite the unknown one, got %d profiles with a binding", len(profs))
	}
}

func TestImportTabby_InvalidSecretValue(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(newTestStore()), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Value is null (not a string).
	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":null}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config":     config,
		"passphrase": "pw",
	})
	var errResp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResp.Error == nil {
		t.Fatal("expected error for invalid secret value, got success")
	}
}

func TestImportTabby_CreateFailsMidway(t *testing.T) {
	// Create fails on the 3rd of 5 secrets. Secrets 1-2 are created as orphans.
	// The profile/credential metadata store is unchanged (import never reaches AtomicImport).
	// Orphaned secrets will be cleaned up by reconciliation on next start.
	//
	// This test verifies:
	//   1. The error is surfaced to the caller.
	//   2. Orphaned secrets 1-2 exist in the SecretStore.
	//   3. The metadata store has NO credentials (import never completed).
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	failStore := newFailAfterStore(2) // succeeds 2 times, fails on 3rd
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(failStore), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// 5 secrets. Profiles don't need to match since they won't be imported anyway.
	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"a","host":"a.example.com","port":22},"value":"s1"},
		{"type":"ssh:password","key":{"user":"b","host":"b.example.com","port":22},"value":"s2"},
		{"type":"ssh:password","key":{"user":"c","host":"c.example.com","port":22},"value":"s3"},
		{"type":"ssh:password","key":{"user":"d","host":"d.example.com","port":22},"value":"s4"},
		{"type":"ssh:password","key":{"user":"e","host":"e.example.com","port":22},"value":"s5"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config":     config,
		"passphrase": "pw",
	})
	var errResp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResp.Error == nil {
		t.Fatal("expected error from failing SecretStore, got success")
	}

	// Secrets 1-2 were created (they're orphans now).
	failStore.mu.Lock()
	orphans := len(failStore.created)
	failStore.mu.Unlock()
	if orphans != 2 {
		t.Errorf("expected 2 orphaned secrets, got %d", orphans)
	}
	if orphans > 0 && failStore.created[0] != "s1" {
		t.Errorf("expected orphan s1, got %q", failStore.created[0])
	}
	if orphans > 1 && failStore.created[1] != "s2" {
		t.Errorf("expected orphan s2, got %q", failStore.created[1])
	}
}

func TestImportTabby_SealedVault(t *testing.T) {
	// A sealed vault answers with the canonical sealed error — code
	// -32001, reason vault-sealed — the shape the renderer's dispatcher
	// turns into the unlock prompt (ADR-0032). The answer is immediate;
	// nothing blocks on an ask.
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	sealedStore := newFailAfterStore(0)
	sealedStore.err = vault.ErrVaultSealed
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(sealedStore), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config":     config,
		"passphrase": "pw",
	})
	var errResp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    *struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResp.Error == nil {
		t.Fatal("expected error for sealed vault, got success")
	}
	if errResp.Error.Code != -32001 {
		t.Errorf("error code = %d, want -32001 (vault-sealed)", errResp.Error.Code)
	}
	if errResp.Error.Data == nil || errResp.Error.Data.Reason != "vault-sealed" {
		t.Errorf("error data.reason = %v, want 'vault-sealed'", errResp.Error.Data)
	}
	assertNoPendingAsk(t, ws)
}

func TestImportTabby_VaultSecrets(t *testing.T) {
	// Import with credentials + credMeta via the atomic import path.
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	cs := newTestStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config":     config,
		"passphrase": "pw",
	})
	var result struct {
		Result int `json:"result"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if result.Result != 1 {
		t.Errorf("expected 1 profile imported, got %d", result.Result)
	}

	profs, _ := ps.LoadProfiles()
	if len(profs) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profs))
	}
	if profs[0].Options.PasswordSecret == "" {
		t.Error("profile should carry the minted password secret")
	}
	if _, err := cs.Get(context.Background(), credential.SecretID(profs[0].Options.PasswordSecret)); err != nil {
		t.Errorf("bound password secret: %v", err)
	}
}

func TestImportTabby_NoCredentialStore(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithProfileService(svc))
	// No WithCredentialStore.
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.importTabby", map[string]any{
		"config":     config,
		"passphrase": "pw",
	})
	var errResp struct {
		Error *struct {
			Data *struct {
				Reason string `json:"reason"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResp.Error == nil {
		t.Fatal("expected error when credential store is nil, got success")
	}
}

func TestTabbyPreview_HappyPath(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	cs := newTestStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs),
		WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"},
		{"type":"ssh:key-passphrase","key":{"hash":"9f2c4e1a77b0d3e5"},"value":"passphrase-val"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{
		"config":     config,
		"passphrase": "test-pw",
	})
	var preview struct {
		Result struct {
			ProfilesToImport int             `json:"profilesToImport"`
			GroupsToImport   int             `json:"groupsToImport"`
			SecretsToImport  int             `json:"secretsToImport"`
			SkippedSecrets   []SkippedInfo   `json:"skippedSecrets"`
			Collisions       []CollisionInfo `json:"collisions"`
			SecretProvider   string          `json:"secretProvider"`
			PlanToken        string          `json:"planToken"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &preview); err != nil {
		t.Fatalf("unmarshal preview: %v\nraw: %s", err, string(resp))
	}
	if preview.Result.ProfilesToImport != 1 {
		t.Errorf("profilesToImport=1, got %d", preview.Result.ProfilesToImport)
	}
	if preview.Result.GroupsToImport != 1 {
		t.Errorf("groupsToImport=1, got %d", preview.Result.GroupsToImport)
	}
	if preview.Result.SecretsToImport != 2 {
		t.Errorf("secretsToImport=2, got %d", preview.Result.SecretsToImport)
	}
	if len(preview.Result.SkippedSecrets) > 0 {
		t.Errorf("unexpected skipped secrets: %+v", preview.Result.SkippedSecrets)
	}
	if preview.Result.PlanToken == "" {
		t.Error("planToken is empty")
	}
	if preview.Result.SecretProvider == "" {
		t.Error("secretProvider is empty")
	}

	// Verify the store was NOT written to by the preview.
	profs, _ := ps.LoadProfiles()
	if len(profs) != 0 {
		t.Errorf("expected 0 profiles in store after preview, got %d", len(profs))
	}
}

func TestTabbyPreview_NoPassphrase(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	cs := newTestStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs),
		WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	vaultContent := `{"config":null,"secrets":[]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{
		"config": config,
	})
	var errResp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(resp))
	}
	if errResp.Error == nil {
		t.Fatal("expected error for missing passphrase")
	}
	if errResp.Error.Code != -32603 {
		t.Errorf("expected code -32603, got %d", errResp.Error.Code)
	}
}

func TestTabbyPreview_UnhandledSecrets(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	cs := newTestStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs),
		WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Include a handled password AND an unhandled "file" secret type.
	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"},
		{"type":"file","key":"some-key","value":"cHJpdmF0ZS1rZXk="}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	resp := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{
		"config":     config,
		"passphrase": "test-pw",
	})
	var preview struct {
		Result struct {
			SecretsToImport int           `json:"secretsToImport"`
			SkippedSecrets  []SkippedInfo `json:"skippedSecrets"`
			PlanToken       string        `json:"planToken"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &preview); err != nil {
		t.Fatalf("unmarshal preview: %v\nraw: %s", err, string(resp))
	}
	if preview.Result.SecretsToImport != 1 {
		t.Errorf("secretsToImport=1, got %d", preview.Result.SecretsToImport)
	}
	if len(preview.Result.SkippedSecrets) != 1 {
		t.Fatalf("expected 1 skipped secret, got %d: %+v", len(preview.Result.SkippedSecrets), preview.Result.SkippedSecrets)
	}
	if preview.Result.SkippedSecrets[0].SecretType != "file" {
		t.Errorf("skipped secret type = %q, want %q", preview.Result.SkippedSecrets[0].SecretType, "file")
	}
	if preview.Result.SkippedSecrets[0].Reason == "" {
		t.Error("skipped secret reason is empty")
	}
}

func TestTabbyPreview_Collisions(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	cs := newTestStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs),
		WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Pre-populate a profile with the same ID the fixture uses (ssh:custom:p1).
	existingProfile := profile.SSHProfile{
		Base: profile.Base{
			ID:   "ssh:custom:p1",
			Name: "web-01",
		},
		Options: profile.StoredSSHProfileOptions{
			Host: "old-host.example.com",
		},
	}
	if err := ps.CreateProfile(existingProfile); err != nil {
		t.Fatalf("create existing profile: %v", err)
	}

	// Vault with a password secret.
	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 1)

	resp := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{
		"config":     config,
		"passphrase": "test-pw",
	})
	var preview struct {
		Result struct {
			ProfileEntries []ProfileEntry  `json:"profileEntries"`
			Collisions     []CollisionInfo `json:"collisions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &preview); err != nil {
		t.Fatalf("unmarshal preview: %v\nraw: %s", err, string(resp))
	}

	// Profile "web-01" collides with existing ssh:custom:p1 → overwrite.
	foundOverwrite := false
	for _, c := range preview.Result.Collisions {
		if c.Kind == "profile" && c.Policy == "overwrite" {
			foundOverwrite = true
			break
		}
	}
	if !foundOverwrite {
		t.Errorf("expected profile overwrite collision, got collisions: %+v", preview.Result.Collisions)
	}

	// Profile entry should show "overwrite" action.
	foundOverwriteEntry := false
	for _, e := range preview.Result.ProfileEntries {
		if e.Name == "web-01" && e.Action == "overwrite" {
			foundOverwriteEntry = true
			break
		}
	}
	if !foundOverwriteEntry {
		t.Errorf("expected profile entry with action=overwrite, got entries: %+v", preview.Result.ProfileEntries)
	}
}

func TestTabbyExecute_HappyPath(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	cs := newTestStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs),
		WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Preview to get a plan token.
	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"},
		{"type":"ssh:key-passphrase","key":{"hash":"9f2c4e1a77b0d3e5"},"value":"passphrase-val"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	previewResp := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{
		"config":     config,
		"passphrase": "test-pw",
	})
	type previewResult struct {
		PlanToken string `json:"planToken"`
	}
	var pr struct {
		Result previewResult `json:"result"`
	}
	if err := json.Unmarshal(previewResp, &pr); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if pr.Result.PlanToken == "" {
		t.Fatal("planToken is empty")
	}

	// Execute with the plan token.
	execResp := jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{
		"planToken": pr.Result.PlanToken,
	})
	var execResult struct {
		Result profile.ImportResult `json:"result"`
	}
	if err := json.Unmarshal(execResp, &execResult); err != nil {
		t.Fatalf("unmarshal execute: %v\nraw: %s", err, string(execResp))
	}
	if execResult.Result.ProfilesImported != 1 {
		t.Errorf("profilesImported=1, got %d", execResult.Result.ProfilesImported)
	}
	if execResult.Result.GroupsImported != 1 {
		t.Errorf("groupsImported=1, got %d", execResult.Result.GroupsImported)
	}
	if len(execResult.Result.ImportErrors) > 0 {
		t.Errorf("import errors: %v", execResult.Result.ImportErrors)
	}

	// The profile carries the minted password binding directly (ADR-0017):
	// no credential record is created, and the secret exists in the store.
	profs, _ := ps.LoadProfiles()
	if len(profs) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profs))
	}
	p := profs[0]
	if p.Options.PasswordSecret == "" {
		t.Fatal("profile should carry the minted password secret")
	}

	// The bound secret exists and holds the vault's password.
	sec, err := cs.Get(context.Background(), credential.SecretID(p.Options.PasswordSecret))
	if err != nil {
		t.Fatalf("Get bound secret: %v", err)
	}
	var val string
	if err := sec.Use(func(b []byte) error {
		val = string(bytes.TrimRight(b, "\x00"))
		return nil
	}); err != nil {
		t.Fatalf("Use bound secret: %v", err)
	}
	if val != "hunter2" {
		t.Errorf("bound password value = %q, want %q", val, "hunter2")
	}
}

func TestTabbyExecute_DoubleConsume(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	cs := newTestStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs),
		WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	previewResp := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{
		"config":     config,
		"passphrase": "test-pw",
	})
	type previewResult struct {
		PlanToken string `json:"planToken"`
	}
	var pr struct {
		Result previewResult `json:"result"`
	}
	if err := json.Unmarshal(previewResp, &pr); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	token := pr.Result.PlanToken

	// First execute — should succeed.
	exec1 := jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{
		"planToken": token,
	})
	var result1 struct {
		Result profile.ImportResult `json:"result"`
	}
	if err := json.Unmarshal(exec1, &result1); err != nil {
		t.Fatalf("unmarshal first execute: %v\nraw: %s", err, string(exec1))
	}
	if result1.Result.ProfilesImported != 1 {
		t.Errorf("first execute profilesImported=1, got %d", result1.Result.ProfilesImported)
	}

	// Second execute with same token — should fail.
	exec2 := jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{
		"planToken": token,
	})
	var errResp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(exec2, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(exec2))
	}
	if errResp.Error == nil {
		t.Fatal("expected error on second execute (plan consumed)")
	}
	if errResp.Error.Code != -32603 {
		t.Errorf("expected error code -32603, got %d", errResp.Error.Code)
	}
}

func TestTabbyExecute_SecretStoreFails(t *testing.T) {
	// When the SecretStore fails during execute, the error is surfaced and no
	// metadata is written (import never reaches AtomicImport).
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	failStore := newFailAfterStore(0) // fails on first Create
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(failStore), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Preview first.
	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	previewResp := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{
		"config":     config,
		"passphrase": "test-pw",
	})
	type previewResult struct {
		PlanToken string `json:"planToken"`
	}
	var pr struct {
		Result previewResult `json:"result"`
	}
	if err := json.Unmarshal(previewResp, &pr); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if pr.Result.PlanToken == "" {
		t.Fatal("planToken is empty")
	}

	// Execute with failing store — should fail.
	execResp := jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{
		"planToken": pr.Result.PlanToken,
	})
	var errResp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(execResp, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(execResp))
	}
	if errResp.Error == nil {
		t.Fatal("expected error from failing SecretStore, got success")
	}
}

func TestTabbyExecute_ProfileAlreadyExists(t *testing.T) {
	// When the execute is run and a profile with the same ID already exists,
	// AtomicImport overwrites it (not an error). Verify the profile is updated.
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	cs := newTestStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(cs), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	// Pre-populate a profile with the fixture ID.
	existingProfile := profile.SSHProfile{
		Base: profile.Base{
			ID:   "ssh:custom:p1",
			Name: "web-01",
		},
		Options: profile.StoredSSHProfileOptions{
			Host: "old-host.example.com",
		},
	}
	if err := ps.CreateProfile(existingProfile); err != nil {
		t.Fatalf("create existing profile: %v", err)
	}

	vaultContent := `{"config":null,"secrets":[]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	previewResp := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{
		"config":     config,
		"passphrase": "test-pw",
	})
	type previewResult struct {
		PlanToken string `json:"planToken"`
	}
	var pr struct {
		Result previewResult `json:"result"`
	}
	if err := json.Unmarshal(previewResp, &pr); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if pr.Result.PlanToken == "" {
		t.Fatal("planToken is empty")
	}

	// Execute — should succeed (AtomicImport overwrites profile).
	execResp := jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{
		"planToken": pr.Result.PlanToken,
	})
	var execResult struct {
		Result profile.ImportResult `json:"result"`
	}
	if err := json.Unmarshal(execResp, &execResult); err != nil {
		t.Fatalf("unmarshal execute: %v\nraw: %s", err, string(execResp))
	}
	if len(execResult.Result.ImportErrors) > 0 {
		t.Errorf("import errors: %v", execResult.Result.ImportErrors)
	}
	if execResult.Result.ProfilesImported != 1 {
		t.Errorf("profilesImported=1, got %d", execResult.Result.ProfilesImported)
	}

	// Verify the profile was updated (overwritten).
	profs, _ := ps.LoadProfiles()
	if len(profs) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profs))
	}
	if profs[0].Options.Host != "web.example.com" {
		t.Errorf("profile host = %q, want %q", profs[0].Options.Host, "web.example.com")
	}
}

// failOnceStore fails the first Create call, then succeeds. Simulates a
// vault-sealed error that the frontend retries after unlock.
type failOnceStore struct {
	mu    sync.Mutex
	calls int
}

func newFailOnceStore() *failOnceStore {
	return &failOnceStore{}
}

func (f *failOnceStore) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return "", context.DeadlineExceeded
	}
	var val string
	if err := value.Use(func(b []byte) error {
		val = string(b)
		return nil
	}); err != nil {
		return "", err
	}
	return credential.SecretID("test:" + val), nil
}

func (f *failOnceStore) Get(_ context.Context, _ credential.SecretID) (credential.Secret, error) {
	return credential.Secret{}, nil
}

func (f *failOnceStore) Delete(_ context.Context, _ credential.SecretID) error {
	return nil
}

func (f *failOnceStore) Exists(_ context.Context, _ credential.SecretID) (bool, error) {
	return false, nil
}

func TestTabbyExecute_VaultRetry(t *testing.T) {
	// Simulates vault-sealed on first attempt: the plan is claimed and released
	// on failure, allowing a retry with the same token to succeed.
	dir := t.TempDir()
	ps := profile.NewJSONStore(filepath.Join(dir, "p.json"))
	svc := profile.NewProfileService(ps)
	onceStore := newFailOnceStore()
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(onceStore), WithProfileService(svc))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	vaultContent := `{"config":null,"secrets":[
		{"type":"ssh:password","key":{"user":"deploy","host":"web.example.com","port":22},"value":"hunter2"}
	]}`
	vault := encryptTabbyVaultForTest(t, vaultContent, "test-pw")
	config := buildImportConfigYAML(t, &vault, 0)

	// Preview to get the plan token.
	previewResp := jsonrpcCall(t, conn, "profiles.tabbyPreview", map[string]any{
		"config":     config,
		"passphrase": "test-pw",
	})
	type previewResult struct {
		PlanToken string `json:"planToken"`
	}
	var pr struct {
		Result previewResult `json:"result"`
	}
	if err := json.Unmarshal(previewResp, &pr); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	token := pr.Result.PlanToken
	if token == "" {
		t.Fatal("planToken is empty")
	}

	// First execute — should fail (store returns error).
	exec1 := jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{
		"planToken": token,
	})
	var errResp struct {
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(exec1, &errResp); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(exec1))
	}
	if errResp.Error == nil {
		t.Fatal("expected error on first execute, got success")
	}

	// Second execute with the same token — should succeed (plan was released).
	exec2 := jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{
		"planToken": token,
	})
	var result2 struct {
		Result profile.ImportResult `json:"result"`
	}
	if err := json.Unmarshal(exec2, &result2); err != nil {
		t.Fatalf("unmarshal execute 2: %v\nraw: %s", err, string(exec2))
	}
	if len(result2.Result.ImportErrors) > 0 {
		t.Errorf("import errors: %v", result2.Result.ImportErrors)
	}
	if result2.Result.ProfilesImported != 1 {
		t.Errorf("profilesImported=1, got %d", result2.Result.ProfilesImported)
	}

	// Third execute with the same token — should fail (consumed after success).
	exec3 := jsonrpcCall(t, conn, "profiles.tabbyExecute", map[string]any{
		"planToken": token,
	})
	var errResp3 struct {
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(exec3, &errResp3); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, string(exec3))
	}
	if errResp3.Error == nil {
		t.Fatal("expected error on third execute (plan consumed after success)")
	}
}
