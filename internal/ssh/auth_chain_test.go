package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/zalando/go-keyring"
	gossh "golang.org/x/crypto/ssh"
)

// authMethodKind labels a bucket in the fallback chain.
// Defined in ssh_real.go; tests reference the same constants.

func TestAuthChainOrderAuto(t *testing.T) {
	keyring.MockInit()
	rc := newTestRealClient(t)
	ctx := context.Background()

	// With a key file set + credential store wired + agent available, Auto
	// should include: publicKey, agent, savedPassword, keyboardInteractive,
	// promptPassword (in that relative order).
	dir := t.TempDir()
	keyPath := writeTestKey(t, dir)

	store := newTestStore()
	id, err := store.Create(ctx, credential.NewSecret("pw123"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resolved := &resolvedConfig{identityFile: keyPath, user: "alice", hostName: "h"}
	cfg := &ConnectConfig{
		Secrets:  store,
		SecretID: id,
	}

	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}

	// Verify publicKey is present and early.
	foundPubKey := false
	foundSavedPw := false
	foundPromptPw := false
	pubKeyIdx, savedPwIdx, promptPwIdx := -1, -1, -1
	for i, m := range chain {
		switch m.kind {
		case kindPublicKey:
			foundPubKey = true
			pubKeyIdx = i
		case kindSavedPassword:
			foundSavedPw = true
			savedPwIdx = i
		case kindPromptPassword:
			foundPromptPw = true
			promptPwIdx = i
		}
	}
	if !foundPubKey || !foundSavedPw || !foundPromptPw {
		t.Fatalf("chain missing buckets: pubKey=%v savedPw=%v promptPw=%v", foundPubKey, foundSavedPw, foundPromptPw)
	}
	if pubKeyIdx > savedPwIdx {
		t.Errorf("publicKey (%d) should come before savedPassword (%d)", pubKeyIdx, savedPwIdx)
	}
	if savedPwIdx > promptPwIdx {
		t.Errorf("savedPassword (%d) should come before promptPassword (%d)", savedPwIdx, promptPwIdx)
	}
}

func TestAuthChainFilterByAuthMode(t *testing.T) {
	keyring.MockInit()
	rc := newTestRealClient(t)
	ctx := context.Background()
	dir := t.TempDir()
	keyPath := writeTestKey(t, dir)

	store := newTestStore()
	id, err := store.Create(ctx, credential.NewSecret("pw"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resolved := &resolvedConfig{identityFile: keyPath, user: "alice", hostName: "h"}

	// auth=password should EXCLUDE publicKey bucket, include password buckets.
	cfg := &ConnectConfig{Secrets: store, SecretID: id, AuthMode: "password"}
	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	for _, m := range chain {
		if m.kind == kindPublicKey {
			t.Error("auth=password should not include publicKey bucket")
		}
	}

	// auth=publicKey should EXCLUDE password buckets, include publicKey.
	cfg2 := &ConnectConfig{Secrets: store, SecretID: id, AuthMode: "publicKey"}
	chain2, err := rc.buildAuthChain(ctx, resolved, cfg2)
	if err != nil {
		t.Fatalf("buildAuthChain auth=publicKey: %v", err)
	}
	foundPubKey, foundPw := false, false
	for _, m := range chain2 {
		if m.kind == kindPublicKey {
			foundPubKey = true
		}
		if m.kind == kindSavedPassword || m.kind == kindPromptPassword {
			foundPw = true
		}
	}
	if !foundPubKey {
		t.Error("auth=publicKey should include publicKey bucket")
	}
	if foundPw {
		t.Error("auth=publicKey should exclude password buckets")
	}
}

func TestAuthChainExplicitMethodsBypass(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	resolved := &resolvedConfig{user: "alice", hostName: "h"}

	// Explicit AuthMethods bypass the chain builder entirely.
	explicit := []gossh.AuthMethod{gossh.Password("explicit")}
	cfg := &ConnectConfig{AuthMethods: explicit}
	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("explicit methods should bypass chain, got %d methods", len(chain))
	}
}

func TestAuthChainLateBindCredential(t *testing.T) {
	keyring.MockInit()
	rc := newTestRealClient(t)
	ctx := context.Background()
	dir := t.TempDir()
	keyPath := writeTestKey(t, dir)

	// Set up a credential store with a saved password for the identity.
	store := newTestStore()
	id, err := store.Create(ctx, credential.NewSecret("stored-secret"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	resolved := &resolvedConfig{identityFile: keyPath, user: "alice", hostName: "example.com", port: 22}
	cfg := &ConnectConfig{
		Secrets:  store,
		SecretID: id,
	}

	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}

	// Late-bind should inject the stored password as a savedPassword bucket.
	foundStored := false
	for _, m := range chain {
		if m.kind == kindSavedPassword {
			if err := m.secret.Use(func(b []byte) error {
				if string(b) == "stored-secret" {
					foundStored = true
				}
				return nil
			}); err != nil {
				t.Fatalf("secret.Use: %v", err)
			}
		}
	}
	if !foundStored {
		t.Error("late-bind did not inject stored credential as savedPassword")
	}
}

func TestAuthChainDefaultKeyDiscovery(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	// No identityFile, no password, no agent — should fall back to default
	// key files in ~/.ssh/id_*. Those won't exist in test, so chain may be
	// empty or contain only promptPassword.
	resolved := &resolvedConfig{user: "alice", hostName: "h"}
	cfg := &ConnectConfig{}

	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	// We expect at least promptPassword in the chain (or an error if no methods).
	if err != nil {
		// A chain with nothing to send is acceptable here.
		if !errors.Is(err, errNoUsableAuth) {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	foundPrompt := false
	for _, m := range chain {
		if m.kind == kindPromptPassword {
			foundPrompt = true
		}
	}
	if !foundPrompt {
		t.Error("chain should include promptPassword as last resort")
	}
}

func TestResolvePrivateKeyPassphraseByHash(t *testing.T) {
	keyring.MockInit()
	rc := newTestRealClient(t)
	ctx := context.Background()
	store := newTestStore()

	// Verify the lookup/save contract: store a secret, retrieve it,
	// confirm it is non-empty without revealing plaintext.
	hash, err := store.Create(ctx, credential.NewSecret("my-passphrase"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := rc.lookupKeyPassphrase(ctx, store, hash)
	if err != nil {
		t.Fatalf("lookupKeyPassphrase: %v", err)
	}
	if got.IsEmpty() {
		t.Error("lookupKeyPassphrase returned empty Secret for a stored passphrase")
	}
}

// TestLoadKeyWithStoredPassphrase verifies the full path: an encrypted
// private key whose passphrase is stored in the SecretStore is successfully
// parsed when loadKey is called with the matching ConnectConfig.
func TestLoadKeyWithStoredPassphrase(t *testing.T) {
	keyring.MockInit()
	rc := newTestRealClient(t)
	ctx := context.Background()
	store := newTestStore()

	dir := t.TempDir()
	passphrase := "encrypted-key-passphrase"

	// Generate and marshal an encrypted ed25519 key.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	path := filepath.Join(dir, "encrypted_test_key")
	if err = os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	// Store the passphrase in the SecretStore.
	id, err := store.Create(ctx, credential.NewSecret(passphrase))
	if err != nil {
		t.Fatalf("store passphrase: %v", err)
	}

	// Without a config, loadKey returns ErrEncryptedKey (no passphrase available).
	_, err = rc.loadKey(ctx, path, nil)
	if err == nil {
		t.Fatal("expected ErrEncryptedKey with nil config, got nil")
	}
	var encErr *ErrEncryptedKey
	if !errors.As(err, &encErr) {
		t.Fatalf("expected *ErrEncryptedKey, got %T: %v", err, err)
	}

	// With cfg but empty PassphraseSecretID, still ErrEncryptedKey.
	cfgNoPass := &ConnectConfig{Secrets: store}
	_, err = rc.loadKey(ctx, path, cfgNoPass)
	if err == nil {
		t.Fatal("expected ErrEncryptedKey with empty PassphraseSecretID, got nil")
	}
	if !errors.As(err, &encErr) {
		t.Fatalf("expected *ErrEncryptedKey, got %T: %v", err, err)
	}

	// With cfg + valid PassphraseSecretID, the key parses successfully.
	cfg := &ConnectConfig{
		Secrets:            store,
		PassphraseSecretID: id,
	}
	signer, err := rc.loadKey(ctx, path, cfg)
	if err != nil {
		t.Fatalf("loadKey with stored passphrase: %v", err)
	}
	if signer == nil {
		t.Fatal("expected non-nil signer from encrypted key")
	}
}

// newTestRealClient builds a RealClient with test-safe defaults.
func newTestRealClient(t *testing.T) *RealClient {
	t.Helper()
	dir := t.TempDir()
	rc, err := NewReal(
		log.NewSlogAdapter(nil), // nil handler → slog falls back
		WithKnownHostsFile(filepath.Join(dir, "known_hosts")),
		WithConfigResolver(NewStubConfigResolver()),
	)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	return rc
}

// writeTestKey writes an ed25519 private key to dir/key and returns its path.
func writeTestKey(t *testing.T, dir string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	_ = pub
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(dir, "test_key")
	pemBytes := pem.EncodeToMemory(block)
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

// Ensure the new fields compile on ConnectConfig.
func TestConnectConfigNewFields(t *testing.T) {
	cfg := &ConnectConfig{
		AuthMode: "password",
		Secrets:  newTestStore(),
		SecretID: newTestSecretID(),
	}
	if cfg.AuthMode != "password" {
		t.Error("AuthMode not set")
	}
	if cfg.Secrets == nil {
		t.Error("Secrets not set")
	}
	if cfg.SecretID == "" {
		t.Error("SecretID not set")
	}
}

// TestProbeFirstMethodFromExplicitAuthMethods verifies that the probe picks
// the same first method buildAuthChain would — no hardcoded expectation,
// so the test stays correct when the chain's order changes deliberately.
func TestProbeFirstMethodFromExplicitAuthMethods(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	resolved := &resolvedConfig{user: "alice", hostName: "h"}

	// Use two distinguishable types: public key (gossh.PublicKeys) and
	// password (gossh.Password). firstAuthMethod must pick the first entry,
	// which is the public-key method — if it picks the password method
	// instead, the concrete type won't match chain[0].method.
	dir := t.TempDir()
	keyPath := writeTestKey(t, dir)
	signer, err := rc.loadKey(ctx, keyPath, nil)
	if err != nil {
		t.Fatalf("loadKey: %v", err)
	}
	explicit := []gossh.AuthMethod{
		gossh.PublicKeys(signer),
		gossh.Password("fallback"),
	}
	cfg := &ConnectConfig{AuthMethods: explicit}

	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}

	method, err := firstAuthMethod(chain)
	if err != nil {
		t.Fatalf("firstAuthMethod: %v", err)
	}
	if method == nil {
		t.Fatal("firstAuthMethod returned nil method for explicit AuthMethods")
	}
	if len(chain) == 0 {
		t.Fatal("buildAuthChain returned empty chain for explicit AuthMethods")
	}
	if chain[0].method == nil {
		t.Fatal("buildAuthChain's first entry has nil method for explicit AuthMethods")
	}
	// gossh.AuthMethod is incomparable (unexported function-typed method),
	// so we use reflect.TypeOf for a safe concrete-type comparison.
	// Using two different method types (publicKey vs password) means a
	// type mismatch proves firstAuthMethod picked the wrong entry.
	methodType := reflect.TypeOf(method)
	chainType := reflect.TypeOf(chain[0].method)
	if methodType != chainType {
		t.Errorf("firstAuthMethod returned %v, but buildAuthChain's first entry is %v; did it pick entry 1 (password) instead of entry 0?", methodType, chainType)
	}
}

// TestProbeFirstMethodKeyboardInteractive verifies that:
//   - with a stored secret and AuthMode=keyboardInteractive, firstAuthMethod
//     returns a keyboard-interactive method (not a plain password method);
//   - without a stored secret, firstAuthMethod returns ErrEncryptedKey
//     (needs-interactive).
func TestProbeFirstMethodKeyboardInteractive(t *testing.T) {
	keyring.MockInit()
	rc := newTestRealClient(t)
	ctx := context.Background()
	resolved := &resolvedConfig{user: "alice", hostName: "h"}

	t.Run("with stored secret", func(t *testing.T) {
		store := newTestStore()
		id, err := store.Create(ctx, credential.NewSecret("secret-pw"))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		cfg := &ConnectConfig{Secrets: store, SecretID: id, AuthMode: "keyboardInteractive"}

		chain, err := rc.buildAuthChain(ctx, resolved, cfg)
		if err != nil {
			t.Fatalf("buildAuthChain: %v", err)
		}

		method, err := firstAuthMethod(chain)
		if err != nil {
			t.Fatalf("firstAuthMethod with stored secret: %v", err)
		}
		if method == nil {
			t.Fatal("firstAuthMethod returned nil method for keyboardInteractive with stored secret")
		}

		// Concrete-type assertion: the method must be keyboard-interactive,
		typeName := fmt.Sprintf("%T", method)
		if strings.Contains(strings.ToLower(typeName), "password") {
			t.Errorf("firstAuthMethod returned a password-type method (%s), want keyboard-interactive", typeName)
		}
	})

	t.Run("without stored secret", func(t *testing.T) {
		cfg := &ConnectConfig{Secrets: nil, SecretID: "", AuthMode: "keyboardInteractive"}
		chain, err := rc.buildAuthChain(ctx, resolved, cfg)
		if err != nil {
			t.Fatalf("buildAuthChain: %v", err)
		}

		_, err = firstAuthMethod(chain)
		if err == nil {
			t.Fatal("firstAuthMethod: expected ErrEncryptedKey for keyboardInteractive without stored secret, got nil")
		}
		var encErr *ErrEncryptedKey
		if !errors.As(err, &encErr) {
			t.Fatalf("firstAuthMethod: expected *ErrEncryptedKey, got %T: %v", err, err)
		}
	})
}

// memSecretStore is an in-memory credential.SecretStore for tests.
type memSecretStore struct {
	mu   sync.Mutex
	m    map[credential.SecretID]credential.Secret
	next int
}

func (s *memSecretStore) Create(_ context.Context, value credential.Secret) (credential.SecretID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := credential.SecretID(fmt.Sprintf("mem-%d", s.next))
	if s.m == nil {
		s.m = make(map[credential.SecretID]credential.Secret)
	}
	s.m[id] = value
	return id, nil
}

func (s *memSecretStore) Get(_ context.Context, id credential.SecretID) (credential.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	if !ok {
		return credential.Secret{}, nil
	}
	return v, nil
}

func (s *memSecretStore) Resolve(ctx context.Context, id credential.SecretID, why credential.Stance) (credential.Secret, error) {
	return credential.NewResolver(s, nil, nil).Resolve(ctx, id, why)
}

func (s *memSecretStore) Delete(_ context.Context, id credential.SecretID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

func (s *memSecretStore) Exists(_ context.Context, id credential.SecretID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	return ok, nil
}

var testSecretCounter int

func newTestStore() *memSecretStore {
	return &memSecretStore{}
}

func newTestSecretID() credential.SecretID {
	testSecretCounter++
	return credential.SecretID(fmt.Sprintf("test-secret-id-%d", testSecretCounter))
}

// sealedStore returns vault.ErrVaultSealed on every Get, simulating a
// locked vault at connect time.
type sealedStore struct{}

func (s *sealedStore) Create(_ context.Context, _ credential.Secret) (credential.SecretID, error) {
	return "", vault.ErrVaultSealed
}

func (s *sealedStore) Get(_ context.Context, _ credential.SecretID) (credential.Secret, error) {
	return credential.Secret{}, vault.ErrVaultSealed
}

func (s *sealedStore) Resolve(ctx context.Context, id credential.SecretID, _ credential.Stance) (credential.Secret, error) {
	return s.Get(ctx, id)
}

func (s *sealedStore) Delete(_ context.Context, _ credential.SecretID) error {
	return vault.ErrVaultSealed
}

func (s *sealedStore) Exists(_ context.Context, _ credential.SecretID) (bool, error) {
	return false, vault.ErrVaultSealed
}

// generateTestKey generates an ed25519 key and returns both the PEM-encoded
// private key bytes and the gossh.Signer, for tests that need a real key.
func generateTestKey(t *testing.T) ([]byte, gossh.Signer) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(block), signer
}

// TestAddVaultKeyMethod_PlainKey stores an unencrypted key in the
// SecretStore and verifies addPublicKeyMethods loads it as a signer.
func TestAddVaultKeyMethod_PlainKey(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	store := newTestStore()

	keyPEM, _ := generateTestKey(t)
	id, err := store.Create(ctx, credential.NewSecretBytes(keyPEM))
	if err != nil {
		t.Fatalf("store key: %v", err)
	}

	cfg := &ConnectConfig{
		Secrets:     store,
		KeySecretID: id,
	}
	var chain []authChainEntry

	// resolved.identityFile is empty so file path is not triggered.
	resolved := &resolvedConfig{}
	if err := rc.addPublicKeyMethods(ctx, &chain, resolved, cfg); err != nil {
		t.Fatalf("addPublicKeyMethods: %v", err)
	}

	if len(chain) != 1 {
		t.Fatalf("expected 1 publicKey entry, got %d", len(chain))
	}
	if chain[0].kind != kindPublicKey {
		t.Fatalf("expected kindPublicKey, got %v", chain[0].kind)
	}
	if chain[0].method == nil {
		t.Fatal("expected non-nil AuthMethod for vault key")
	}
}

// TestAddVaultKeyMethod_EncryptedKey stores an encrypted key together with
// its passphrase and verifies the signer is produced from both.
func TestAddVaultKeyMethod_EncryptedKey(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	store := newTestStore()

	passphrase := "strong-passphrase-123"
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(block)

	keyID, err := store.Create(ctx, credential.NewSecretBytes(keyPEM))
	if err != nil {
		t.Fatalf("store encrypted key: %v", err)
	}
	pwID, err := store.Create(ctx, credential.NewSecret(passphrase))
	if err != nil {
		t.Fatalf("store passphrase: %v", err)
	}

	cfg := &ConnectConfig{
		Secrets:            store,
		KeySecretID:        keyID,
		PassphraseSecretID: pwID,
	}
	var chain []authChainEntry
	resolved := &resolvedConfig{}

	if err := rc.addPublicKeyMethods(ctx, &chain, resolved, cfg); err != nil {
		t.Fatalf("addPublicKeyMethods: %v", err)
	}

	if len(chain) != 1 {
		t.Fatalf("expected 1 publicKey entry, got %d", len(chain))
	}
	if chain[0].kind != kindPublicKey {
		t.Fatalf("expected kindPublicKey, got %v", chain[0].kind)
	}
	if chain[0].method == nil {
		t.Fatal("expected non-nil AuthMethod for encrypted vault key")
	}
}

// TestAddVaultKeyMethod_SealedVault verifies that when the SecretStore
// returns ErrVaultSealed, the error propagates through buildAuthChain
// rather than being silently swallowed.
func TestAddVaultKeyMethod_SealedVault(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()

	cfg := &ConnectConfig{
		Secrets:     &sealedStore{},
		KeySecretID: "some-key-ref",
	}
	var chain []authChainEntry
	resolved := &resolvedConfig{}

	err := rc.addPublicKeyMethods(ctx, &chain, resolved, cfg)
	if err == nil {
		t.Fatal("expected error from sealed vault, got nil")
	}
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("expected ErrVaultSealed, got %T: %v", err, err)
	}
}

// TestAddVaultKeyMethod_MutualExclusivity verifies that setting both
// KeySecretID and KeyFile produces a loud error.
func TestAddVaultKeyMethod_MutualExclusivity(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()

	cfg := &ConnectConfig{
		Secrets:     newTestStore(),
		KeySecretID: "some-key-ref",
		KeyFile:     "/path/to/some_key",
	}
	var chain []authChainEntry
	resolved := &resolvedConfig{}

	err := rc.addPublicKeyMethods(ctx, &chain, resolved, cfg)
	if err == nil {
		t.Fatal("expected error for mutual exclusivity violation, got nil")
	}
	if !strings.Contains(err.Error(), "both KeySecretID and KeyFile are set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestAddVaultKeyMethod_NoDiskAccess verifies that when KeySecretID is set,
// the vault key path is taken and readFileFn is never called — proving
// file-based key loading is short-circuited before any I/O.
func TestAddVaultKeyMethod_NoDiskAccess(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	store := newTestStore()

	keyPEM, _ := generateTestKey(t)
	id, err := store.Create(ctx, credential.NewSecretBytes(keyPEM))
	if err != nil {
		t.Fatalf("store key: %v", err)
	}

	// Replace readFileFn with a spy that records calls.
	var readFileCalls int
	orig := readFileFn
	readFileFn = func(path string) ([]byte, error) {
		readFileCalls++
		return orig(path)
	}
	defer func() { readFileFn = orig }()

	// Set resolved.identityFile to an existing file with garbage content.
	// The spy on readFileFn will catch any attempted read — the vault key
	// path must never call it.
	dir := t.TempDir()
	garbageFile := filepath.Join(dir, "some_key")
	if err := os.WriteFile(garbageFile, []byte("NOT A KEY"), 0o600); err != nil {
		t.Fatalf("write garbage file: %v", err)
	}

	cfg := &ConnectConfig{
		Secrets:     store,
		KeySecretID: id,
	}
	var chain []authChainEntry
	resolved := &resolvedConfig{identityFile: garbageFile}

	if err := rc.addPublicKeyMethods(ctx, &chain, resolved, cfg); err != nil {
		t.Fatalf("addPublicKeyMethods: %v", err)
	}

	if len(chain) != 1 {
		t.Fatalf("expected 1 publicKey entry from vault, got %d", len(chain))
	}
	if readFileCalls != 0 {
		t.Fatalf("expected 0 readFileFn calls (vault path), got %d", readFileCalls)
	}
}

// TestAddVaultKeyMethod_NoStoreErrors propagates when KeySecretID is set
func TestAddVaultKeyMethod_NoStoreErrors(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()

	cfg := &ConnectConfig{
		KeySecretID: "some-key-ref",
		// Secrets is nil
	}
	var chain []authChainEntry
	resolved := &resolvedConfig{}

	err := rc.addPublicKeyMethods(ctx, &chain, resolved, cfg)
	if err == nil {
		t.Fatal("expected error for nil SecretStore, got nil")
	}
	if !strings.Contains(err.Error(), "no SecretStore configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── The empty credential, reported from the running app ─────────────────
//
// A connection set to publicKey whose credential holds no key material has
// nothing to send. The chain still carries `none` and `hostbased`, neither of
// which carries a method, so the dial went out with an empty Auth list and the
// server answered "attempted methods [none], no supported methods remain" —
// the Go library naming its own internals to a user whose actual problem was
// an empty credential, and pointing them at the host rather than at the
// connection. Both entry points must give the same, true answer.

func TestNoAuthMaterial_ConnectSaysWhichMethodHasNothing(t *testing.T) {
	// HOME with no ~/.ssh keys, so default key discovery finds nothing —
	// exactly the reported machine.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	rc := newTestRealClient(t)
	resolved := &resolvedConfig{user: "root", hostName: "192.168.0.57", port: 22}
	cfg := &ConnectConfig{AuthMode: "publicKey"}

	dial := rc.dialForConnect(context.Background(), "192.168.0.57", resolved, cfg)
	_, err := dial(poolKey{})
	if err == nil {
		t.Fatal("expected a dial refusal when there is nothing to authenticate with")
	}
	var noAuth *ErrNoAuthMethod
	if !errors.As(err, &noAuth) {
		t.Fatalf("expected ErrNoAuthMethod, got %T: %v", err, err)
	}
	if noAuth.Mode != "publicKey" || noAuth.User != "root" || noAuth.Host != "192.168.0.57" {
		t.Fatalf("error lost the context it exists to carry: %+v", noAuth)
	}
	// The sentence names the cause, not the library's internals.
	if strings.Contains(err.Error(), "no supported methods remain") {
		t.Fatalf("the handshake message leaked into the user's answer: %v", err)
	}
	if !strings.Contains(err.Error(), "no key is stored") {
		t.Fatalf("the message does not say what is missing: %v", err)
	}
}

func TestNoAuthMaterial_ProbeSaysTheSameThingAsConnect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	rc := newTestRealClient(t)
	ctx := context.Background()
	resolved := &resolvedConfig{user: "root", hostName: "192.168.0.57", port: 22}
	cfg := &ConnectConfig{AuthMode: "publicKey"}

	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	if _, err := firstAuthMethod(chain); !errors.Is(err, errNoUsableAuth) {
		t.Fatalf("expected errNoUsableAuth from a chain with nothing to send, got %v", err)
	}

	// …and the probe turns it into the same answer the connect path gives,
	// so Test and Connect cannot disagree about the same connection.
	_, probeErr := rc.probeConfig(ctx, "192.168.0.57", cfg)
	var noAuth *ErrNoAuthMethod
	if !errors.As(probeErr, &noAuth) {
		t.Fatalf("expected ErrNoAuthMethod from the probe, got %T: %v", probeErr, probeErr)
	}
	if noAuth.Mode != "publicKey" {
		t.Fatalf("probe lost the mode: %+v", noAuth)
	}
}

// TestNoAuthMaterial_JumpDialSaysWhichMethodHasNothing is the bastion-side
// sibling of the two above. dialJumpForConnect builds its own auth chain and
// dials the bastion directly, so it needs the same empty-chain guard: without
// it the user is told the bastion "attempted methods [none]", which reads as a
// server-side rejection and sends them to look at the wrong machine (nocx-8b1v).
func TestNoAuthMaterial_JumpDialSaysWhichMethodHasNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	rc := newTestRealClient(t)
	resolved := &resolvedConfig{user: "bastionuser", hostName: "bastion.example.com", port: 22}
	cfg := &ConnectConfig{AuthMode: "publicKey"}

	dial := rc.dialJumpForConnect(context.Background(), "bastion.example.com", resolved, cfg)
	_, err := dial(poolKey{})
	if err == nil {
		t.Fatal("expected a refusal when the bastion has nothing to authenticate with")
	}
	var noAuth *ErrNoAuthMethod
	if !errors.As(err, &noAuth) {
		t.Fatalf("expected ErrNoAuthMethod, got %T: %v", err, err)
	}
	if noAuth.Mode != "publicKey" || noAuth.User != "bastionuser" || noAuth.Host != "bastion.example.com" {
		t.Fatalf("error lost the bastion's identity: %+v", noAuth)
	}
	if strings.Contains(err.Error(), "no supported methods remain") {
		t.Fatalf("the handshake message leaked into the user's answer: %v", err)
	}
}
