package ssh

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/shady2k/nocx/internal/credential"
	gossh "golang.org/x/crypto/ssh"
)

// readFileFn abstracts os.ReadFile for test injection. Tests replace this
// with a spy to assert that no file I/O occurs when the vault key path is
// taken (KeySecretID set).
var readFileFn = os.ReadFile

// storedKeySigner loads the private key material a config's KeySecretID names
// — with PassphraseSecretID when the key is encrypted — and answers the
// signer. It is the SAME construction addVaultKeyMethod appends to a chain,
// and it is a function of its own because a second caller needs the signer
// rather than a link in a chain: the helper's `sign` reverse op signs a
// challenge with a stored key without ever building an auth chain
// (internal/ssh's own seam, used by internal/app's reverse handlers).
//
// It is a pure extraction and changes nothing about the path: the errors, the
// laziness of the passphrase read, and the refusal to fall back to a file are
// all as they were.
func (rc *RealClient) storedKeySigner(ctx context.Context, cfg *ConnectConfig) (gossh.Signer, error) {
	secret, err := rc.getSecretWithUnlock(ctx, cfg, cfg.KeySecretID, "load the stored key")
	if err != nil {
		return nil, err
	}
	if secret.IsEmpty() {
		return nil, fmt.Errorf("key material not found for vault secret %q", cfg.KeySecretID)
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
		return nil, useErr
	}
	return signer, nil
}

// lookupKeyPassphrase resolves a private-key passphrase through the mandatory
// operation stance.
func (rc *RealClient) lookupKeyPassphrase(ctx context.Context, resolver credential.Resolver, id credential.SecretID) (credential.Secret, error) {
	if resolver == nil || id == "" {
		return credential.Secret{}, nil
	}
	return resolver.Resolve(ctx, id, credential.Operation("load the key passphrase"))
}

func (rc *RealClient) getSecretWithUnlock(ctx context.Context, cfg *ConnectConfig, id credential.SecretID, reason string) (credential.Secret, error) {
	return cfg.Secrets.Resolve(ctx, id, credential.Operation(reason))
}

// agentAvailable checks whether SSH_AUTH_SOCK is set.
func (rc *RealClient) agentAvailable() bool {
	return os.Getenv("SSH_AUTH_SOCK") != ""
}

// loadKey reads and parses one private key file, unlocking it with the
// profile's stored passphrase when it has one and the caller threaded one.
//
// A key path reached here may come from the profile, from ~/.ssh/config's
// IdentityFile, or from the default list ssh -G answers with — there is no
// longer a hard-coded list beside that answer, because two lists of "the keys
// this machine would offer" is exactly the second implementation that drifts.
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
