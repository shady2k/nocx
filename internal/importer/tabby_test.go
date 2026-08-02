package importer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/profile"
	"golang.org/x/crypto/pbkdf2"
)

func TestParseTabbyConfig(t *testing.T) {
	data, err := os.ReadFile("testdata/tabby-config.yml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	cfg, err := ParseTabbyConfig(data)
	if err != nil {
		t.Fatalf("ParseTabbyConfig: %v", err)
	}

	if cfg.Version != 8 {
		t.Errorf("version = %d, want 8", cfg.Version)
	}
	if len(cfg.Profiles) != 3 {
		t.Fatalf("profiles = %d, want 3", len(cfg.Profiles))
	}
	if len(cfg.Groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(cfg.Groups))
	}

	// Spot-check the first profile.
	p := cfg.Profiles[0]
	if p.Type != "ssh" {
		t.Errorf("type = %q, want ssh", p.Type)
	}
	if p.Options.Host != "prod-web1.example.com" {
		t.Errorf("host = %q", p.Options.Host)
	}
	if p.Options.User != "deploy" {
		t.Errorf("user = %q", p.Options.User)
	}
}

func TestImportSSHProfilesOnly(t *testing.T) {
	data, _ := os.ReadFile("testdata/tabby-config.yml")
	cfg, err := ParseTabbyConfig(data)
	if err != nil {
		t.Fatalf("ParseTabbyConfig: %v", err)
	}

	profStore := newMemStore()
	if err := ImportProfiles(cfg, profStore, "ssh"); err != nil {
		t.Fatalf("ImportProfiles: %v", err)
	}

	profs, _ := profStore.LoadProfiles()
	if len(profs) != 3 {
		t.Fatalf("imported %d profiles, want 3 (all are ssh)", len(profs))
	}
	for _, p := range profs {
		if p.Type != "ssh" {
			t.Errorf("non-ssh profile imported: %q", p.Type)
		}
	}
}

func TestImportGroups(t *testing.T) {
	data, _ := os.ReadFile("testdata/tabby-config.yml")
	cfg, _ := ParseTabbyConfig(data)

	profStore := newMemStore()
	if err := ImportGroups(cfg, profStore); err != nil {
		t.Fatalf("ImportGroups: %v", err)
	}

	groups, _ := profStore.LoadGroups()
	if len(groups) != 3 {
		t.Fatalf("imported %d groups, want 3", len(groups))
	}

	var dev *profile.ProfileGroup
	for i, g := range groups {
		if g.ID == "g-dev" {
			dev = &groups[i]
		}
	}
	if dev == nil {
		t.Fatal("g-dev not imported")
	}
	if dev.ParentGroupID != "g-prod" {
		t.Errorf("g-dev parentGroupId = %q, want g-prod", dev.ParentGroupID)
	}
	if dev.Name != "Development" {
		t.Errorf("g-dev name = %q, want Development", dev.Name)
	}
}

func TestImportPreservesNestedGrouping(t *testing.T) {
	data, _ := os.ReadFile("testdata/tabby-config.yml")
	cfg, _ := ParseTabbyConfig(data)

	profStore := newMemStore()
	_ = ImportGroups(cfg, profStore)
	_ = ImportProfiles(cfg, profStore, "ssh")

	profs, _ := profStore.LoadProfiles()
	var devBox *profile.SSHProfile
	for i, p := range profs {
		if p.Name == "dev-box" {
			devBox = &profs[i]
		}
	}
	if devBox == nil {
		t.Fatal("dev-box profile not imported")
	}
	if devBox.Group != "g-dev" {
		t.Errorf("dev-box group = %q, want g-dev", devBox.Group)
	}
}

func TestDedupByHostPortUser(t *testing.T) {
	data, _ := os.ReadFile("testdata/tabby-config.yml")
	cfg, _ := ParseTabbyConfig(data)

	profStore := newMemStore()
	_ = ImportProfiles(cfg, profStore, "ssh")
	// Re-import should not duplicate.
	_ = ImportProfiles(cfg, profStore, "ssh")

	profs, _ := profStore.LoadProfiles()
	if len(profs) != 3 {
		t.Errorf("after re-import, %d profiles (should dedup to 3)", len(profs))
	}
}

func TestImportToJSONStore(t *testing.T) {
	data, _ := os.ReadFile("testdata/tabby-config.yml")
	cfg, _ := ParseTabbyConfig(data)

	dir := t.TempDir()
	store := profile.NewJSONStore(filepath.Join(dir, "imported.json"))

	if err := ImportProfiles(cfg, store, "ssh"); err != nil {
		t.Fatalf("ImportProfiles to JSONStore: %v", err)
	}
	if err := ImportGroups(cfg, store); err != nil {
		t.Fatalf("ImportGroups to JSONStore: %v", err)
	}

	// Reload from disk to verify persistence.
	store2 := profile.NewJSONStore(filepath.Join(dir, "imported.json"))
	profs, _ := store2.LoadProfiles()
	if len(profs) != 3 {
		t.Errorf("reloaded %d profiles, want 3", len(profs))
	}
	groups, _ := store2.LoadGroups()
	if len(groups) != 3 {
		t.Errorf("reloaded %d groups, want 3", len(groups))
	}
}

func TestImportHandlesNonSSHProfiles(t *testing.T) {
	// A config with a mix of ssh and non-ssh profiles should import only ssh.
	yaml := []byte(`
version: 8
profiles:
  - id: "ssh:custom:ssh1:1111"
    type: ssh
    name: ssh1
    options: {host: h1, port: 22, user: u1}
  - id: "local:custom:loc1:2222"
    type: local
    name: loc1
    options: {command: /bin/zsh}
`)
	cfg, err := ParseTabbyConfig(yaml)
	if err != nil {
		t.Fatalf("ParseTabbyConfig: %v", err)
	}

	store := newMemStore()
	if err := ImportProfiles(cfg, store, "ssh"); err != nil {
		t.Fatalf("ImportProfiles: %v", err)
	}
	profs, _ := store.LoadProfiles()
	if len(profs) != 1 {
		t.Fatalf("imported %d profiles, want 1 (ssh only)", len(profs))
	}
	if profs[0].Type != "ssh" {
		t.Errorf("type = %q, want ssh", profs[0].Type)
	}
}

// ---------------------------------------------------------------------------
// Vault decryption helpers and tests.
// ---------------------------------------------------------------------------
// encryptTestVault encrypts a TabbyVaultContents for test purposes.
// contents is marshalled as JSON directly — Config and Secrets are
// json.RawMessage so callers specify literal JSON strings.
func encryptTestVault(t *testing.T, contents *TabbyVaultContents, passphrase string) *TabbyVault {
	t.Helper()
	plaintext, err := json.Marshal(contents)
	if err != nil {
		t.Fatalf("marshal test vault: %v", err)
	}
	salt := make([]byte, 8)
	if _, err2 := rand.Read(salt); err2 != nil {
		t.Fatalf("rand salt: %v", err2)
	}
	iv := make([]byte, 16)
	if _, err2 := rand.Read(iv); err2 != nil {
		t.Fatalf("rand iv: %v", err2)
	}
	key := pbkdf2.Key([]byte(passphrase), salt, 100000, 32, sha512.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	padLen := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	return &TabbyVault{
		Version:   1,
		Encrypted: true,
		Contents:  base64.StdEncoding.EncodeToString(ciphertext),
		KeySalt:   hex.EncodeToString(salt),
		IV:        hex.EncodeToString(iv),
	}
}

func TestDecryptTabbyVaultHappyPath(t *testing.T) {
	original := &TabbyVaultContents{
		Config:  json.RawMessage(`{"version":1}`),
		Secrets: json.RawMessage(`[{"type":"ssh-key","key":"dev-key-1","value":"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ..."},{"type":"password","key":"prod-pw","value":"hunter2"}]`),
	}
	vault := encryptTestVault(t, original, "correct-horse-battery-staple")
	got, err := DecryptTabbyVault(vault, "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("DecryptTabbyVault: %v", err)
	}
	sec := got.DecodedSecrets()
	if len(sec) != 2 {
		t.Fatalf("secrets = %d, want 2", len(sec))
	}
	if sec[0].Type != "ssh-key" || string(sec[0].Key) != `"dev-key-1"` || string(sec[0].Value) != `"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ..."` {
		t.Errorf("secret[0] mismatch: type=%q key=%s val=%s", sec[0].Type, string(sec[0].Key), string(sec[0].Value))
	}
	if sec[1].Type != "password" || string(sec[1].Key) != `"prod-pw"` || string(sec[1].Value) != `"hunter2"` {
		t.Errorf("secret[1] mismatch: type=%q key=%s val=%s", sec[1].Type, string(sec[1].Key), string(sec[1].Value))
	}
}

func TestDecryptTabbyVaultHappyPathNullConfig(t *testing.T) {
	original := &TabbyVaultContents{
		Config:  json.RawMessage(`null`),
		Secrets: json.RawMessage(`[{"type":"password","key":"k","value":"v"}]`),
	}
	vault := encryptTestVault(t, original, "pass")
	got, err := DecryptTabbyVault(vault, "pass")
	if err != nil {
		t.Fatalf("DecryptTabbyVault: %v", err)
	}
	if len(got.DecodedSecrets()) != 1 {
		t.Fatalf("secrets = %d, want 1", len(got.DecodedSecrets()))
	}
}

func TestDecryptTabbyVaultWrongPassphrase(t *testing.T) {
	original := &TabbyVaultContents{
		Secrets: json.RawMessage(`[{"type":"password","key":"k","value":"v"}]`),
	}
	vault := encryptTestVault(t, original, "correct-horse-battery-staple")
	_, err := DecryptTabbyVault(vault, "wrong-passphrase")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultNilVault(t *testing.T) {
	_, err := DecryptTabbyVault(nil, "passphrase")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultWrongVersion(t *testing.T) {
	original := &TabbyVaultContents{
		Config:  json.RawMessage(`null`),
		Secrets: json.RawMessage(`[{"type":"password","key":"k","value":"v"}]`),
	}
	vault := encryptTestVault(t, original, "pass")
	vault.Version = 2
	_, err := DecryptTabbyVault(vault, "pass")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultNotEncrypted(t *testing.T) {
	v := &TabbyVault{Version: 1, Encrypted: false, Contents: "AAAA", KeySalt: "00", IV: "00000000000000000000000000000000"}
	_, err := DecryptTabbyVault(v, "passphrase")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultEmptyPassphrase(t *testing.T) {
	original := &TabbyVaultContents{
		Secrets: json.RawMessage(`[{"type":"password","key":"k","value":"v"}]`),
	}
	vault := encryptTestVault(t, original, "some-pass")
	_, err := DecryptTabbyVault(vault, "")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultInvalidSaltHex(t *testing.T) {
	original := &TabbyVaultContents{
		Secrets: json.RawMessage(`[{"type":"password","key":"k","value":"v"}]`),
	}
	vault := encryptTestVault(t, original, "pass")
	vault.KeySalt = "ZZZ"
	_, err := DecryptTabbyVault(vault, "pass")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultWrongSaltLength(t *testing.T) {
	v := &TabbyVault{
		Version:   1,
		Encrypted: true,
		Contents:  base64.StdEncoding.EncodeToString(make([]byte, 16)),
		KeySalt:   hex.EncodeToString(make([]byte, 4)),
		IV:        hex.EncodeToString(make([]byte, 16)),
	}
	_, err := DecryptTabbyVault(v, "pass")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultInvalidIVHex(t *testing.T) {
	original := &TabbyVaultContents{
		Secrets: json.RawMessage(`[{"type":"password","key":"k","value":"v"}]`),
	}
	vault := encryptTestVault(t, original, "pass")
	vault.IV = "ZZZ"
	_, err := DecryptTabbyVault(vault, "pass")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultWrongIVLength(t *testing.T) {
	v := &TabbyVault{
		Version:   1,
		Encrypted: true,
		Contents:  base64.StdEncoding.EncodeToString(make([]byte, 16)),
		KeySalt:   hex.EncodeToString(make([]byte, 8)),
		IV:        hex.EncodeToString(make([]byte, 8)),
	}
	_, err := DecryptTabbyVault(v, "pass")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultInvalidContentsBase64(t *testing.T) {
	original := &TabbyVaultContents{
		Secrets: json.RawMessage(`[{"type":"password","key":"k","value":"v"}]`),
	}
	vault := encryptTestVault(t, original, "pass")
	vault.Contents = "!!!invalid-base64!!!"
	_, err := DecryptTabbyVault(vault, "pass")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultEmptyContents(t *testing.T) {
	v := &TabbyVault{
		Version:   1,
		Encrypted: true,
		Contents:  "",
		KeySalt:   hex.EncodeToString(make([]byte, 8)),
		IV:        hex.EncodeToString(make([]byte, 16)),
	}
	_, err := DecryptTabbyVault(v, "pass")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultCiphertextNotBlockAligned(t *testing.T) {
	v := &TabbyVault{
		Version:   1,
		Encrypted: true,
		Contents:  base64.StdEncoding.EncodeToString([]byte("short")),
		KeySalt:   hex.EncodeToString(make([]byte, 8)),
		IV:        hex.EncodeToString(make([]byte, 16)),
	}
	_, err := DecryptTabbyVault(v, "pass")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultGarbageDecryptsToInvalidJSON(t *testing.T) {
	v := &TabbyVault{
		Version:   1,
		Encrypted: true,
		Contents:  base64.StdEncoding.EncodeToString(make([]byte, 32)),
		KeySalt:   hex.EncodeToString(make([]byte, 8)),
		IV:        hex.EncodeToString(make([]byte, 16)),
	}
	_, err := DecryptTabbyVault(v, "some-passphrase")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultCiphertextTooLarge(t *testing.T) {
	big := make([]byte, (tabbyMaxCiphertextLen+1)*4/3+100)
	for i := range big {
		big[i] = 'A'
	}
	v := &TabbyVault{
		Version:   1,
		Encrypted: true,
		Contents:  string(big),
		KeySalt:   hex.EncodeToString(make([]byte, 8)),
		IV:        hex.EncodeToString(make([]byte, 16)),
	}
	_, err := DecryptTabbyVault(v, "pass")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONMissingType(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[{"key":"k","value":"v"}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONMissingKey(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[{"type":"password","value":"v"}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONMissingValue(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[{"type":"password","key":"k"}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONMissingConfig(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"secrets":[{"type":"password","key":"k","value":"v"}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONMissingSecrets(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONUnknownSecretField(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[{"type":"password","key":"k","value":"v","extra":"x"}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONUnknownTopLevelField(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[],"extra":"x"}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONTrailingTokens(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[]}trailing`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONEmptyType(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[{"type":"","key":"k","value":"v"}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONMissingKeyField(t *testing.T) {
	// Key omitted entirely (nil RawMessage).
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[{"type":"password","value":"v"}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONKeyIsObject(t *testing.T) {
	// Tabby file secrets use object keys: {"id":"...","description":"..."}
	original := &TabbyVaultContents{
		Config:  json.RawMessage(`null`),
		Secrets: json.RawMessage(`[{"type":"ssh-key","key":{"id":"file-1","description":"my key"},"value":"ssh-rsa AAAA..."}]`),
	}
	vault := encryptTestVault(t, original, "pass")
	got, err := DecryptTabbyVault(vault, "pass")
	if err != nil {
		t.Fatalf("DecryptTabbyVault: %v", err)
	}
	if len(got.DecodedSecrets()) != 1 {
		t.Fatalf("secrets = %d, want 1", len(got.DecodedSecrets()))
	}
	if string(got.DecodedSecrets()[0].Key) != `{"id":"file-1","description":"my key"}` {
		t.Errorf("key = %s", string(got.DecodedSecrets()[0].Key))
	}
}

func TestDecryptTabbyVaultJSONEmptyKeyString(t *testing.T) {
	// Explicit "" is allowed — it's a valid JSON string value.
	original := &TabbyVaultContents{
		Config:  json.RawMessage(`null`),
		Secrets: json.RawMessage(`[{"type":"password","key":"","value":"v"}]`),
	}
	vault := encryptTestVault(t, original, "pass")
	got, err := DecryptTabbyVault(vault, "pass")
	if err != nil {
		t.Fatalf("DecryptTabbyVault: %v", err)
	}
	if string(got.DecodedSecrets()[0].Key) != `""` {
		t.Errorf("key = %s, want empty string", string(got.DecodedSecrets()[0].Key))
	}
}

func TestDecryptTabbyVaultJSONKeyTooLarge(t *testing.T) {
	// Key JSON larger than tabbyMaxSecretKeyLen raw bytes.
	// {"x":"<padding>"} has 8 bytes overhead; padding must push total over max.
	const keyOverhead = 8
	padding := tabbyMaxSecretKeyLen - keyOverhead + 1
	keyJSON := `{"x":"` + string(make([]byte, padding)) + `"}`
	if len(keyJSON) <= tabbyMaxSecretKeyLen {
		t.Fatalf("test key too small: %d", len(keyJSON))
	}
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[{"type":"password","key":`+keyJSON+`,"value":"v"}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONEmptyValue(t *testing.T) {
	// Explicit empty value is allowed if Tabby permits it.
	original := &TabbyVaultContents{
		Config:  json.RawMessage(`null`),
		Secrets: json.RawMessage(`[{"type":"password","key":"k","value":""}]`),
	}
	vault := encryptTestVault(t, original, "pass")
	got, err := DecryptTabbyVault(vault, "pass")
	if err != nil {
		t.Fatalf("DecryptTabbyVault: %v", err)
	}
	if string(got.DecodedSecrets()[0].Value) != `""` {
		t.Errorf("value = %s", string(got.DecodedSecrets()[0].Value))
	}
}

func TestDecryptTabbyVaultJSONValueIsNull(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[{"type":"password","key":"k","value":null}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONValueIsObject(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":[{"type":"password","key":"k","value":{}}]}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONTypeTooLong(t *testing.T) {
	typ := string(make([]byte, tabbyMaxSecretTypeLen+1))
	ciphertext := encryptTestPlaintext(t, fmt.Sprintf(`{"config":null,"secrets":[{"type":%q,"key":"k","value":"v"}]}`, typ))
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONValueTooLong(t *testing.T) {
	value := string(make([]byte, tabbyMaxSecretValueLen+1))
	ciphertext := encryptTestPlaintext(t, fmt.Sprintf(`{"config":null,"secrets":[{"type":"password","key":"k","value":%q}]}`, value))
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultJSONTooManySecrets(t *testing.T) {
	secrets := make([]TabbySecret, tabbyMaxSecrets+1)
	for i := range secrets {
		secrets[i] = TabbySecret{Type: "password", Key: json.RawMessage(fmt.Sprintf(`"k%d"`, i)), Value: json.RawMessage(`"v"`)}
	}
	contentsJSON, _ := json.Marshal(struct {
		Config  json.RawMessage `json:"config"`
		Secrets []TabbySecret   `json:"secrets"`
	}{Config: json.RawMessage(`null`), Secrets: secrets})
	vault := encryptTestPlaintextFromBytes(t, contentsJSON, "pw")
	_, err := DecryptTabbyVault(vault, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultExactMaxSecrets(t *testing.T) {
	secrets := make([]TabbySecret, tabbyMaxSecrets)
	for i := range secrets {
		secrets[i] = TabbySecret{Type: "password", Key: json.RawMessage(fmt.Sprintf(`"k%d"`, i)), Value: json.RawMessage(`"v"`)}
	}
	contentsJSON, _ := json.Marshal(struct {
		Config  json.RawMessage `json:"config"`
		Secrets []TabbySecret   `json:"secrets"`
	}{Config: json.RawMessage(`null`), Secrets: secrets})
	vault := encryptTestPlaintextFromBytes(t, contentsJSON, "pw")
	got, err := DecryptTabbyVault(vault, "pw")
	if err != nil {
		t.Fatalf("DecryptTabbyVault: %v", err)
	}
	if len(got.DecodedSecrets()) != tabbyMaxSecrets {
		t.Fatalf("secrets = %d, want %d", len(got.DecodedSecrets()), tabbyMaxSecrets)
	}
}

func TestDecryptTabbyVaultJSONNotAnObject(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `"just a string"`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

func TestDecryptTabbyVaultSecretsNotArray(t *testing.T) {
	ciphertext := encryptTestPlaintext(t, `{"config":null,"secrets":"not-an-array"}`)
	v := &TabbyVault{Version: 1, Encrypted: true, Contents: ciphertext.Contents, KeySalt: ciphertext.KeySalt, IV: ciphertext.IV}
	_, err := DecryptTabbyVault(v, "pw")
	if err != ErrDecryptFailed {
		t.Fatalf("expected ErrDecryptFailed, got %v", err)
	}
}

// encryptTestPlaintext encrypts a raw JSON string for test purposes.
func encryptTestPlaintext(t *testing.T, plaintext string) *TabbyVault {
	return encryptTestPlaintextFromBytes(t, []byte(plaintext), "pw")
}

// encryptTestPlaintextFromBytes encrypts raw JSON bytes with the given passphrase.
func encryptTestPlaintextFromBytes(t *testing.T, plaintext []byte, passphrase string) *TabbyVault {
	t.Helper()
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		t.Fatalf("rand salt: %v", err)
	}
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand iv: %v", err)
	}
	key := pbkdf2.Key([]byte(passphrase), salt, 100000, 32, sha512.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	pt := plaintext
	padLen := aes.BlockSize - len(pt)%aes.BlockSize
	padded := make([]byte, len(pt)+padLen)
	copy(padded, pt)
	for i := len(pt); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)
	return &TabbyVault{
		Version:   1,
		Encrypted: true,
		Contents:  base64.StdEncoding.EncodeToString(ciphertext),
		KeySalt:   hex.EncodeToString(salt),
		IV:        hex.EncodeToString(iv),
	}
}
