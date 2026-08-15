package ssh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/credential"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// readFileFn abstracts os.ReadFile for test injection. Tests replace this
// with a spy to assert that no file I/O occurs when the vault key path is
// taken (KeySecretID set).
var readFileFn = os.ReadFile

// ---------------------------------------------------------------------------
// Auth fallback chain (Tabby-parity)
// ---------------------------------------------------------------------------

// authMethodKind labels a bucket in the fallback chain.
type authMethodKind int

const (
	kindNone authMethodKind = iota
	kindPublicKey
	kindAgent
	kindSavedPassword
	kindKeyboardInteractive
	kindPromptPassword
	kindHostbased
)

// authChainEntry is one bucket in the ordered fallback chain.
type authChainEntry struct {
	kind   authMethodKind
	method gossh.AuthMethod
	// secret holds a stored password/passphrase for late-bind auth buckets.
	// It is a credential.Secret so it cannot leak via logging or marshaling;
	// auth methods read it through Use at auth time only (see
	// passwordCallbackFromSecret).
	secret credential.Secret
}

// buildAuthChain builds the ordered auth fallback chain, porting Tabby's
// SSHSession.init(). Order: none → publicKey(s) → agent → savedPassword →
// keyboard-interactive → promptPassword → hostbased.
func (rc *RealClient) buildAuthChain(ctx context.Context, resolved *resolvedConfig, cfg *ConnectConfig) ([]authChainEntry, error) {
	if len(cfg.AuthMethods) > 0 {
		chain := make([]authChainEntry, 0, len(cfg.AuthMethods))
		for _, m := range cfg.AuthMethods {
			chain = append(chain, authChainEntry{kind: kindPublicKey, method: m})
		}
		return chain, nil
	}

	mode := cfg.AuthMode
	var chain []authChainEntry

	chain = append(chain, authChainEntry{kind: kindNone})
	if mode == "" || mode == "publicKey" {
		if err := rc.addPublicKeyMethods(ctx, &chain, resolved, cfg); err != nil {
			return nil, err
		}
	}
	if (mode == "" || mode == "agent") && rc.agentAvailable() {
		rc.addAgentMethods(&chain)
	}

	if mode == "" || mode == "password" {
		rc.addPasswordMethods(ctx, &chain, cfg)
	}

	if mode == "" || mode == "keyboardInteractive" {
		rc.addKeyboardInteractiveMethods(ctx, &chain, cfg)
	}

	if mode == "" || mode == "password" {
		chain = append(chain, authChainEntry{
			kind:   kindPromptPassword,
			method: rc.promptPasswordMethod(ctx, cfg, resolved, hasStoredPasswordRung(chain)),
		})
	}

	chain = append(chain, authChainEntry{kind: kindHostbased})

	return chain, nil
}

func (rc *RealClient) addPublicKeyMethods(ctx context.Context, chain *[]authChainEntry, resolved *resolvedConfig, cfg *ConnectConfig) error {
	// Vault-stored key material path. When KeySecretID is set, load key
	// bytes from the SecretStore exclusively — no file-based fallback.
	if cfg.KeySecretID != "" {
		// Guard: KeySecretID and KeyFile are mutually exclusive. If both
		// are set upstream it is a bug that must be loud, not silently
		// resolved by precedence.
		if cfg.KeyFile != "" {
			return fmt.Errorf("both KeySecretID and KeyFile are set; vault-key path refused — this is a bug")
		}
		if cfg.Secrets == nil {
			return fmt.Errorf("KeySecretID set but no SecretStore configured")
		}
		return rc.addVaultKeyMethod(ctx, chain, cfg)
	}

	// File-based key path: explicit identity file from cfg.KeyFile resolved
	// through ~/.ssh/config.
	if resolved.identityFile != "" {
		if signer, err := rc.loadKey(ctx, resolved.identityFile, cfg); err == nil {
			*chain = append(*chain, authChainEntry{kind: kindPublicKey, method: gossh.PublicKeys(signer)})
		}
	}

	// Default key discovery: try conventional paths as fallback.
	for _, path := range defaultKeyPaths() {
		signer, err := rc.loadKey(ctx, path, cfg)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			var encKeyErr *ErrEncryptedKey
			if errors.As(err, &encKeyErr) {
				rc.log.Debug("skipping encrypted key in chain", "path", path)
			}
			continue
		}
		*chain = append(*chain, authChainEntry{kind: kindPublicKey, method: gossh.PublicKeys(signer)})
	}

	return nil
}

// addVaultKeyMethod loads private key bytes from the SecretStore by
// KeySecretID and builds the signer. When PassphraseSecretID is also set,
// the passphrase is loaded and used to decrypt the key. Key bytes never
// touch disk.
//
// Any error — vault sealed, secret missing, malformed key material — is
// propagated to the caller. There is no silent fallback to file-based keys.
func (rc *RealClient) addVaultKeyMethod(ctx context.Context, chain *[]authChainEntry, cfg *ConnectConfig) error {
	secret, err := rc.getSecretWithUnlock(ctx, cfg, cfg.KeySecretID, "load the stored key")
	if err != nil {
		return err
	}
	if secret.IsEmpty() {
		return fmt.Errorf("key material not found for vault secret %q", cfg.KeySecretID)
	}

	var signer gossh.Signer
	if useErr := secret.Use(func(keyBytes []byte) error {
		if cfg.PassphraseSecretID == "" {
			s, parseErr := gossh.ParsePrivateKey(keyBytes)
			if parseErr != nil {
				var passErr *gossh.PassphraseMissingError
				if errors.As(parseErr, &passErr) {
					// Typed so the probe reports "needs interactive" and the
					// renderer can ask for the passphrase instead of failing
					// the connection (the key is fine; it is only locked).
					return &ErrEncryptedKey{Path: "the stored key"}
				}
				return fmt.Errorf("parse vault key: %w", parseErr)
			}
			signer = s
			return nil
		}

		// Encrypted key: load the passphrase from the same store.
		pwSecret, pwErr := rc.getSecretWithUnlock(ctx, cfg, cfg.PassphraseSecretID, "load the key passphrase")
		if pwErr != nil {
			return pwErr
		}
		if pwSecret.IsEmpty() {
			return fmt.Errorf("passphrase not found for encrypted vault key %q", cfg.KeySecretID)
		}

		return pwSecret.Use(func(passphrase []byte) error {
			s, parseErr := gossh.ParsePrivateKeyWithPassphrase(keyBytes, passphrase)
			if parseErr != nil {
				return fmt.Errorf("parse vault key with passphrase: %w", parseErr)
			}
			signer = s
			return nil
		})
	}); useErr != nil {
		return useErr
	}

	*chain = append(*chain, authChainEntry{kind: kindPublicKey, method: gossh.PublicKeys(signer)})
	return nil
}

func (rc *RealClient) addAgentMethods(chain *[]authChainEntry) {
	if am := rc.agentMethods(); len(am) > 0 {
		for _, m := range am {
			*chain = append(*chain, authChainEntry{kind: kindAgent, method: m})
		}
	}
}

// passwordCallbackFromSecret builds a gossh.PasswordCallback that
// materializes the plaintext through Secret.Use only when the SSH server
// challenges for it — so the password lives in memory for the duration of
// the callback, not for the lifetime of the chain. An empty secret returns
// ("", nil), matching the previous empty-string behaviour.
func passwordCallbackFromSecret(s credential.Secret) gossh.AuthMethod {
	return gossh.PasswordCallback(func() (string, error) {
		var pw string
		if err := s.Use(func(b []byte) error { pw = string(b); return nil }); err != nil {
			return "", err
		}
		return pw, nil
	})
}

func (rc *RealClient) addPasswordMethods(ctx context.Context, chain *[]authChainEntry, cfg *ConnectConfig) {
	if cfg.Secrets != nil && cfg.SecretID != "" {
		if stored, err := rc.getSecretWithUnlock(ctx, cfg, cfg.SecretID, "read the stored password"); err == nil && !stored.IsEmpty() {
			*chain = append(*chain, authChainEntry{
				kind:   kindSavedPassword,
				method: passwordCallbackFromSecret(stored),
				secret: stored,
			})
		} else if err != nil {
			rc.log.Debug("secret lookup failed", "secretID", cfg.SecretID, "error", err)
		}
	}
}

func (rc *RealClient) addKeyboardInteractiveMethods(ctx context.Context, chain *[]authChainEntry, cfg *ConnectConfig) {
	if cfg.Secrets != nil && cfg.SecretID != "" {
		if stored, err := rc.getSecretWithUnlock(ctx, cfg, cfg.SecretID, "read the stored secret"); err == nil && !stored.IsEmpty() {
			*chain = append(*chain, authChainEntry{kind: kindKeyboardInteractive, secret: stored})
		}
	}
	*chain = append(*chain, authChainEntry{kind: kindKeyboardInteractive})
}

// lookupKeyPassphrase resolves a private-key passphrase by SecretID from the
// SecretStore. It returns a credential.Secret so the passphrase is
// non-serializable; callers read it through Secret.Use.
func (rc *RealClient) lookupKeyPassphrase(ctx context.Context, store credential.SecretStore, id credential.SecretID) (credential.Secret, error) {
	if store == nil || id == "" {
		return credential.Secret{}, nil
	}
	return store.Get(ctx, id)
}

// getSecretWithUnlock reads the stored secret. The unlock is NOT this
// layer's: a sealed vault is a sealed-vault failure that propagates to the
// session.open handler, which emits the canonical sealed error the renderer
// turns into the unlock prompt; the whole open is re-sent once the vault
// answers (ADR-0032).
func (rc *RealClient) getSecretWithUnlock(ctx context.Context, cfg *ConnectConfig, id credential.SecretID, reason string) (credential.Secret, error) {
	return cfg.Secrets.Get(ctx, id)
}

func authMethodsFromChain(chain []authChainEntry) []gossh.AuthMethod {
	var methods []gossh.AuthMethod
	for _, entry := range chain {
		if entry.method != nil {
			methods = append(methods, entry.method)
		}
	}
	return methods
}

// agentAvailable checks whether SSH_AUTH_SOCK is set.
func (rc *RealClient) agentAvailable() bool {
	return os.Getenv("SSH_AUTH_SOCK") != ""
}

// defaultKeyPaths returns the conventional default private key paths.
func defaultKeyPaths() []string {
	home := os.Getenv("HOME")
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".ssh", "id_rsa"),
		filepath.Join(home, ".ssh", "id_ecdsa"),
	}
}

func (rc *RealClient) loadKey(ctx context.Context, path string, cfg *ConnectConfig) (gossh.Signer, error) {
	data, err := readFileFn(path)
	if err != nil {
		return nil, err
	}

	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		var passErr *gossh.PassphraseMissingError
		if !errors.As(err, &passErr) {
			return nil, fmt.Errorf("parse key %s: %w", path, err)
		}

		// Attempt stored passphrase if the caller threaded a config.
		if cfg == nil || cfg.Secrets == nil || cfg.PassphraseSecretID == "" {
			return nil, &ErrEncryptedKey{Path: path}
		}

		secret, lookupErr := rc.lookupKeyPassphrase(ctx, cfg.Secrets, cfg.PassphraseSecretID)
		if lookupErr != nil || secret.IsEmpty() {
			return nil, &ErrEncryptedKey{Path: path}
		}

		// Parse inside Secret.Use so the passphrase []byte never
		// escapes the callback (ADR-0011 §2).
		var withPw gossh.Signer
		if useErr := secret.Use(func(passphrase []byte) error {
			var parseErr error
			withPw, parseErr = gossh.ParsePrivateKeyWithPassphrase(data, passphrase)
			return parseErr
		}); useErr != nil {
			return nil, &ErrEncryptedKey{Path: path}
		}
		return withPw, nil
	}
	return signer, nil
}

func (rc *RealClient) agentMethods() []gossh.AuthMethod {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}

	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}
	_ = conn.Close()

	return []gossh.AuthMethod{
		gossh.PublicKeysCallback(func() ([]gossh.Signer, error) {
			conn, err := net.Dial("unix", sock)
			if err != nil {
				return nil, err
			}
			defer func() { _ = conn.Close() }()
			return agent.NewClient(conn).Signers()
		}),
	}
}
