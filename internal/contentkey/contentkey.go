// Package contentkey owns the ContentDB key's lifecycle (nocx-rtg0.9,
// amended by nocx-rtg0.14, ADR-0018 §3).
//
// The key has two homes, and the vault's file provider appears in neither:
//
//   - An OS keystore exists (macOS Keychain, freedesktop Secret Service,
//     Windows Credential Manager) → the key lives there, in the system
//     provider's derived slot — the same arrangement as before the
//     amendment, reached without the vault instance.
//   - There is none → the key is DERIVED at startup from a per-machine salt
//     and the machine's identity. No passphrase is ever requested for
//     history, on any platform.
//
// The threat model is narrow and stated (owner decision 2026-08-02): a copy
// of the FILE must not be readable as it stands. Not a live attacker, not
// other processes running as this user — that boundary is not defensible on
// a desktop. What encryption must defeat is the DETACHED copy: a backup, a
// synced folder, an exported file, a pulled disk. So the salt is 32 random
// bytes, mode 0600, in the CONFIG directory — never in the data directory
// beside content.db: a copy of the data directory carries nothing that opens
// it. The machine-id is not a secret (/etc/machine-id is mode 444), which is
// exactly why it cannot be the only ingredient.
//
// The reference is DERIVED, never persisted. settings.json is a portable
// document — it syncs between machines, is backed up, gets copied — and a
// sec:v1:<provider>:<id> reference points at a provider slot on THIS
// machine: carried elsewhere it points at nothing. The slot itself is the
// source of truth in the keystore branch.
//
// Two invariants carry the whole lifecycle. The read never passes through a
// vault seal gate: the key is read ONCE at startup and held for the process,
// so a vault auto-seal can never make history unreadable. And the database's
// existence is the "was history ever created" marker: when the key cannot be
// reconstructed — no keystore slot, no salt — but content.db exists, the key
// is LOST and must never be re-minted (a new key would strand the existing
// database while the UI claimed history worked).
package contentkey

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/vault"
)

// ErrKeyLost reports that the database exists but its key is gone from every
// home — no OS keystore slot holds it and no salt remains to derive it. The
// key is cryptographically unrecoverable (ADR-0018 §5) and MUST NOT be
// re-minted.
var ErrKeyLost = errors.New("content key: database exists but its key is gone (no keystore slot, no salt)")

// ProviderRegistry is the provider seam. Implemented by *vault.Registry.
// Only the system provider — the OS keystore — is ever used, and only when
// it is available; the file provider is deliberately out of this key's
// lifecycle (nocx-rtg0.14).
type ProviderRegistry interface {
	Get(id vault.ProviderID) (vault.Provider, bool)
	Writable(id vault.ProviderID) (vault.WritableProvider, bool)
}

// Config is the dependency set for the key lifecycle.
type Config struct {
	// Registry reaches the providers. The system provider (the OS keystore)
	// is the key's home when one exists.
	Registry ProviderRegistry
	// KeyID derives the deterministic keystore slot reference. The sec:v1
	// grammar is vault-owned; the default is vault.ContentKeyID.
	KeyID func(p vault.ProviderID) (credential.SecretID, error)
	// SystemReady reports whether an OS keystore is available. Injected so
	// the composition root probes once — a probe is a real keychain write,
	// and probing twice at startup would be a second permission prompt.
	SystemReady bool
	// DBPath is the content.db path. Its existence distinguishes first run
	// (create the key) from a lost key (never re-mint). The database lives
	// in the DATA directory, never in the portable config directory.
	DBPath string
	// SaltPath is the 0600 salt file in the CONFIG directory — never beside
	// content.db. Its existence means "this machine's key is derived"; it is
	// authoritative, so a keystore appearing later cannot strand derived
	// history by minting elsewhere.
	SaltPath string
	// MachineID and UserID supply the derivation ingredients. When nil, the
	// platform identity is read (Linux: /etc/machine-id + uid; macOS:
	// IOPlatformUUID + uid; Windows: MachineGuid + user SID). Injectable so
	// the failure path is testable without a machine that cannot name
	// itself.
	MachineID func() (string, error)
	UserID    func() (string, error)
	Logger    log.Logger
}

// LoadOrCreate returns the 32-byte ContentDB key. Called once at startup;
// the caller holds the bytes for the life of the process.
func LoadOrCreate(ctx context.Context, cfg Config) ([]byte, error) {
	if cfg.Logger == nil {
		cfg.Logger = log.NewSlogAdapter(nil)
	}
	if cfg.KeyID == nil {
		cfg.KeyID = vault.ContentKeyID
	}

	// A salt file means the key was derived on this machine, and the salt is
	// authoritative: even if an OS keystore has appeared since, minting into
	// it would rotate the key under the existing database. Derive.
	if _, err := os.Stat(cfg.SaltPath); err == nil {
		return deriveKey(cfg)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("content key: stat salt: %w", err)
	}

	// No salt: the OS keystore is the key's home when one exists.
	if cfg.SystemReady {
		return keystoreFindOrCreate(ctx, cfg)
	}

	// Neither salt nor keystore. If the database exists the key is LOST —
	// re-minting would rotate it and strand the database. If not, first run.
	if _, err := os.Stat(cfg.DBPath); err == nil {
		return nil, ErrKeyLost
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("content key: stat database: %w", err)
	}
	return createDerived(ctx, cfg)
}

// keystoreFindOrCreate finds the key in the system provider's derived slot,
// or mints it there on first run. The database's existence is the
// lost-key marker: an empty slot beside an existing database means the key
// is gone, never to be re-minted.
func keystoreFindOrCreate(ctx context.Context, cfg Config) ([]byte, error) {
	const p = vault.ProviderSystem
	prov, ok := cfg.Registry.Get(p)
	if !ok {
		return nil, fmt.Errorf("content key: provider %q unavailable", p)
	}
	id, err := cfg.KeyID(p)
	if err != nil {
		return nil, err
	}
	sec, err := prov.Get(ctx, id)
	switch {
	case err == nil:
		key, usable := secretBytes(sec)
		if !usable || len(key) != 32 {
			return nil, fmt.Errorf("content key: slot %q holds unusable material (len %d)", id, len(key))
		}
		return key, nil
	case !errors.Is(err, vault.ErrSecretNotFound):
		return nil, fmt.Errorf("content key: probe %q: %w", p, err)
	}

	if _, err := os.Stat(cfg.DBPath); err == nil {
		return nil, ErrKeyLost
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("content key: stat database: %w", err)
	}

	w, ok := cfg.Registry.Writable(p)
	if !ok {
		return nil, fmt.Errorf("content key: provider %q is not writable", p)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("content key: generate: %w", err)
	}
	if err := w.Put(ctx, id, credential.NewSecretBytes(key)); err != nil {
		return nil, fmt.Errorf("content key: store at %q: %w", p, err)
	}
	cfg.Logger.Info("content key minted", "provider", p)
	return key, nil
}

// createDerived mints the salt and derives the key from it. The machine
// identity is read BEFORE the salt is written: a host that cannot name
// itself must not leave a salt behind, or the next start would derive and
// fail again with a salt that now exists.
func createDerived(ctx context.Context, cfg Config) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("content key: generate salt: %w", err)
	}
	key, err := deriveKeyWithSalt(cfg, salt)
	if err != nil {
		return nil, err
	}
	if err := writeSalt(cfg.SaltPath, salt); err != nil {
		return nil, err
	}
	cfg.Logger.Info("content key derived", "salt", cfg.SaltPath)
	return key, nil
}

// deriveKey reads the persisted salt and derives from it. The caller has
// already established the salt exists; a read failure here is corruption,
// never a first run.
func deriveKey(cfg Config) ([]byte, error) {
	salt, err := os.ReadFile(cfg.SaltPath)
	if err != nil {
		return nil, fmt.Errorf("content key: read salt: %w", err)
	}
	if len(salt) != saltLen {
		return nil, fmt.Errorf("content key: salt file is corrupt (len %d, want %d)", len(salt), saltLen)
	}
	return deriveKeyWithSalt(cfg, salt)
}

// deriveKeyWithSalt runs the HKDF derivation (nocx-rtg0.14):
//
//	key = HKDF-SHA256( ikm = salt ‖ machine-id ‖ uid, info = "nocx-contentdb-v1" )
//
// The concatenation is length-framed so no two (salt, machine-id, uid)
// triples can collide at the boundaries; the brief's ingredients are exactly
// the input key material. machine-id is not a secret — the salt is the
// secret ingredient, which is precisely why the salt must never sit beside
// content.db.
func deriveKeyWithSalt(cfg Config, salt []byte) ([]byte, error) {
	machineID := cfg.MachineID
	if machineID == nil {
		// The salt's own directory is the config directory, which is also
		// where a minted id belongs when the host exposes none of its own.
		configDir := filepath.Dir(cfg.SaltPath)
		machineID = func() (string, error) { return machineIDOrMinted(configDir) }
	}
	userID := cfg.UserID
	if userID == nil {
		userID = readUserID
	}
	machine, err := machineID()
	if err != nil {
		return nil, fmt.Errorf("content key: machine identity: %w", err)
	}
	user, err := userID()
	if err != nil {
		return nil, fmt.Errorf("content key: user identity: %w", err)
	}

	ikm := make([]byte, 0, saltLen+8+len(machine)+8+len(user))
	ikm = append(ikm, salt...)
	ikm = appendUint64(ikm, uint64(len(machine)))
	ikm = append(ikm, machine...)
	ikm = appendUint64(ikm, uint64(len(user)))
	ikm = append(ikm, user...)
	return hkdfKey(ikm)
}

func appendUint64(b []byte, v uint64) []byte {
	return append(b,
		byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32), // #nosec G115 -- value is range-bounded by the protocol or preceding validation
		byte(v>>24), byte(v>>16), byte(v>>8), byte(v)) // #nosec G115 -- value is range-bounded by the protocol or preceding validation
}

// writeSalt writes the salt atomically-enough: O_EXCL with 0600, so a racing
// second start cannot clobber the first salt (0600 has no group/other bits,
// so a umask cannot widen it). The parent — the config directory — is
// created 0700 if missing: on a fresh machine nothing has written there yet,
// and the salt must not be the first casualty of that.
func writeSalt(path string, salt []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("content key: create salt dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec -- path is the app-owned config-dir salt, never caller input
	if err != nil {
		return fmt.Errorf("content key: create salt: %w", err)
	}
	if _, err := f.Write(salt); err != nil {
		_ = f.Close()
		return fmt.Errorf("content key: write salt: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("content key: close salt: %w", err)
	}
	return nil
}

// secretBytes extracts the plaintext through Use. Returns ok=false when Use
// itself failed.
func secretBytes(s credential.Secret) (key []byte, ok bool) {
	var out []byte
	err := s.Use(func(b []byte) error {
		out = append(out, b...)
		return nil
	})
	return out, err == nil
}
