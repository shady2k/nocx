// Package connection resolves profile IDs into SSH connect configurations.
// It is the single point where a profile ID becomes a concrete host, user, auth
// method and (through the credential store) a late-bound password.
//
// Nothing in the transport, session or SSH layer carries plaintext after the
// resolver is wired in: passwords stay in the credential store until the SSH
// auth chain pulls them at connect time.
package connection

import (
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/ssh"
)

// Resolver maps profile IDs to ssh.ConnectConfig with credential wiring.
type Resolver struct {
	profiles         profile.ProfileRepository
	credMeta         profile.CredentialMetadataRepository
	secrets          credential.SecretStore
	endpointResolver profile.EndpointResolver // ADR-0013: for canonical endpoint resolution and grant check
}

// NewResolver creates a Resolver backed by the given stores.
// ADR-0013: endpointResolver is optional; if nil, grant checks are skipped (migration-only mode).
func NewResolver(pr profile.ProfileRepository, cmr profile.CredentialMetadataRepository, ss credential.SecretStore, er profile.EndpointResolver) *Resolver {
	return &Resolver{profiles: pr, credMeta: cmr, secrets: ss, endpointResolver: er}
}

// Resolve maps a profile ID to a Resolved ready for SSH connection.
// The returned config has:
//   - Host from the profile (for ~/.ssh/config alias resolution)
//   - User/AuthMode/KeyPath from the credential (if CredentialID is set) or
//     from the profile's inline fields
//   - SecretStore + SecretID wired for late-bound password resolution
//     (only when a credential is linked)
//   - Jump host fields resolved recursively (with cycle detection)
//   - AuthorizationRevision set after grant check (ADR-0013)
//
// Passwords are never set as plaintext on the returned config.
func (r *Resolver) Resolve(profileID string) (host string, cfg *ssh.ConnectConfig, err error) {
	prof, err := r.findProfile(profileID)
	if err != nil {
		return "", nil, err
	}

	visited := map[string]bool{profileID: true}
	cfg, err = r.buildConfig(&prof, visited)
	if err != nil {
		return "", nil, err
	}

	return prof.Options.Host, cfg, nil
}

// findProfile loads the profile by ID from the store.
func (r *Resolver) findProfile(id string) (profile.SSHProfile, error) {
	profs, err := r.profiles.LoadProfiles()
	if err != nil {
		return profile.SSHProfile{}, fmt.Errorf("load profiles: %w", err)
	}
	for _, p := range profs {
		if p.ID == id {
			return p, nil
		}
	}
	return profile.SSHProfile{}, fmt.Errorf("profile %s: %w", id, ErrProfileNotFound)
}

// buildConfig constructs a ConnectConfig from a profile, handling credential
// resolution and jump host recursion.
func (r *Resolver) buildConfig(prof *profile.SSHProfile, visited map[string]bool) (*ssh.ConnectConfig, error) {
	cfg := &ssh.ConnectConfig{}
	cfg.Port = prof.Options.Port

	if prof.Options.CredentialID != "" {
		cred, err := r.findCredential(prof.Options.CredentialID)
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", prof.ID, err)
		}
		cfg.User = cred.Username
		cfg.AuthMode = string(cred.Auth)
		cfg.KeyFile = cred.KeyPath

		cfg.BoundHost = cred.Host
		cfg.BoundPort = cred.Port

		// Wire SecretStore for late-bound password/passphrase resolution
		// via opaque SecretID references (ADR-0011 §2).
		cfg.Secrets = r.secrets
		if cred.SecretID != "" {
			cfg.SecretID = credential.SecretID(cred.SecretID)
		}
		if cred.PassphraseSecretID != "" {
			cfg.PassphraseSecretID = credential.SecretID(cred.PassphraseSecretID)
		}
	} else {
		cfg.User = prof.Options.User
		cfg.AuthMode = string(prof.Options.Auth)
	}

	// Resolve jump host if set.
	if prof.Options.JumpHost != "" {
		if visited[prof.Options.JumpHost] {
			return nil, fmt.Errorf("cyclic jump host reference: %s -> %s", prof.ID, prof.Options.JumpHost)
		}
		visited[prof.Options.JumpHost] = true

		jumpProf, err := r.findProfile(prof.Options.JumpHost)
		if err != nil {
			return nil, fmt.Errorf("jump host %s: %w", prof.Options.JumpHost, err)
		}

		jumpCfg, err := r.buildConfig(&jumpProf, visited)
		if err != nil {
			return nil, fmt.Errorf("jump host %s: %w", prof.Options.JumpHost, err)
		}

		cfg.JumpHost = jumpProf.Options.Host
		cfg.JumpPort = jumpProf.Options.Port
		cfg.JumpUser = jumpCfg.User
		cfg.JumpKeyFile = jumpCfg.KeyFile
		cfg.JumpAuthMode = jumpCfg.AuthMode

		if jumpCfg.Secrets != nil {
			cfg.JumpSecrets = jumpCfg.Secrets
			cfg.JumpSecretID = jumpCfg.SecretID
			cfg.JumpPassphraseSecretID = jumpCfg.PassphraseSecretID
			cfg.JumpBoundHost = jumpCfg.BoundHost
			cfg.JumpBoundPort = jumpCfg.BoundPort
		}
	}

	return cfg, nil
}

// findCredential loads a credential by ID from the credential metadata store.
func (r *Resolver) findCredential(id string) (profile.Credential, error) {
	creds, err := r.credMeta.LoadCredentials()
	if err != nil {
		return profile.Credential{}, fmt.Errorf("load credentials: %w", err)
	}
	for _, c := range creds {
		if c.ID == id {
			return c, nil
		}
	}
	return profile.Credential{}, fmt.Errorf("credential %s: %w", id, ErrProfileNotFound)
}

// ErrProfileNotFound is returned when a profile or credential ID is not found.
var ErrProfileNotFound = errors.New("not found")

// ErrCredentialNotAuthorized is returned when a credential has no grant for the target endpoint (ADR-0013).
var ErrCredentialNotAuthorized = errors.New("credential not authorized for this endpoint")
