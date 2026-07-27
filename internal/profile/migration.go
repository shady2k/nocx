package profile

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shady2k/nocx/internal/storage"
)

// profileSchemaVersion is the current schema version for the profile document.
// ADR-0013: version 1 introduces TrustedEndpoints and removes legacy Host/Port.
const profileSchemaVersion storage.SchemaVersion = 1

// EndpointResolver resolves a profile to its canonical endpoint.
// Accepts decoded SSHProfile to avoid recursion (migration cannot load from store).
// Returns canonical host+port after SSH config resolution.
// If resolver is nil or returns error, migration marks profile as requiresReview.
type EndpointResolver func(prof SSHProfile) (host string, port uint16, err error)

// newProfileModule creates an immutable storage.Module for the profile document.
// The module's migration closure captures the resolver for this specific store instance,
// avoiding global state and cross-talk between tests/app.
func newProfileModule(resolver EndpointResolver) storage.Module {
	return storage.Module{
		Name:    "profile",
		Current: profileSchemaVersion,
		Migrations: []storage.Migration{
			{
				From: 0,
				To:   1,
				Up: func(data []byte) ([]byte, error) {
					return migrateV0ToV1(data, resolver)
				},
			},
		},
	}
}

// storeDataV0 is the legacy profile document format without schemaVersion.
type storeDataV0 struct {
	Profiles    []SSHProfile   `json:"profiles,omitempty"`
	Groups      []ProfileGroup `json:"groups,omitempty"`
	Credentials []credentialV0 `json:"credentials,omitempty"`
}

// credentialV0 is the legacy credential format with Host/Port fields.
// Extends current Credential with legacy fields for migration.
type credentialV0 struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Username           string   `json:"username"`
	Auth               AuthMode `json:"auth"`
	KeyPath            string   `json:"keyPath,omitempty"`
	Host               string   `json:"host,omitempty"`
	Port               int      `json:"port,omitempty"`
	SecretID           string   `json:"secretId,omitempty"`
	PassphraseSecretID string   `json:"passphraseSecretId,omitempty"`
	// TrustedEndpoints will be populated during migration
	TrustedEndpoints []CredentialTrustedEndpoint `json:"trustedEndpoints,omitempty"`
}

// migrateV0ToV1 migrates profile document from version 0 to version 1.
// ADR-0013 §Migration: reads legacy Host/Port, creates grants on match with
// canonical endpoint, marks profile as requiresReview on mismatch/unresolvable.
func migrateV0ToV1(data []byte, resolver EndpointResolver) ([]byte, error) {
	var v0 storeDataV0
	if err := json.Unmarshal(data, &v0); err != nil {
		return nil, fmt.Errorf("unmarshal v0 document: %w", err)
	}

	// Build credential ID -> legacy binding map
	credBindings := make(map[string]struct {
		Host string
		Port int
	})
	for _, c := range v0.Credentials {
		credBindings[c.ID] = struct {
			Host string
			Port int
		}{
			Host: c.Host,
			Port: c.Port,
		}
	}

	// Process each profile: resolve canonical endpoint and check legacy binding
	for i := range v0.Profiles {
		prof := &v0.Profiles[i]
		if prof.Options.CredentialID == "" {
			continue // No credential, no grant needed
		}

		// Resolve canonical endpoint for this profile using decoded data
		var canonicalHost string
		var canonicalPort uint16
		var resolveErr error

		if resolver != nil {
			canonicalHost, canonicalPort, resolveErr = resolver(*prof)
		} else {
			resolveErr = fmt.Errorf("no endpoint resolver available")
		}

		if resolveErr != nil {
			// Unresolvable: mark for review, no grant
			prof.Options.RequiresReview = true
			continue
		}

		// Check if legacy binding matches canonical endpoint
		binding, hasBinding := credBindings[prof.Options.CredentialID]
		if !hasBinding || binding.Host == "" {
			// Empty or missing legacy binding: mark for review
			prof.Options.RequiresReview = true
			continue
		}

		// Compare legacy host with canonical (case-insensitive)
		// Legacy Port==0 means "this host, any port" — matches any resolved port
		if !hostMatch(binding.Host, canonicalHost) {
			// Mismatch: mark for review, no grant
			prof.Options.RequiresReview = true
			continue
		}

		// Port comparison: legacy Port==0 matches any, otherwise must match
		if binding.Port != 0 && binding.Port != int(canonicalPort) {
			prof.Options.RequiresReview = true
			continue
		}

		// Match found: add canonical endpoint grant to credential
		// Use canonical values, not raw alias
		credID := prof.Options.CredentialID
		for j := range v0.Credentials {
			cred := &v0.Credentials[j]
			if cred.ID == credID {
				// Add grant if not already present
				grant := CredentialTrustedEndpoint{
					ProfileID: prof.ID,
					Host:      canonicalHost, // Use canonical, not alias
					Port:      canonicalPort, // Use resolved port
				}
				if !hasGrant(cred.TrustedEndpoints, grant) {
					cred.TrustedEndpoints = append(cred.TrustedEndpoints, grant)
				}
				break
			}
		}
	}

	// Clear legacy Host/Port fields from credentials
	for i := range v0.Credentials {
		v0.Credentials[i].Host = ""
		v0.Credentials[i].Port = 0
	}

	// Build v1 document with schemaVersion
	v1Data := struct {
		SchemaVersion int            `json:"schemaVersion"`
		Profiles      []SSHProfile   `json:"profiles,omitempty"`
		Groups        []ProfileGroup `json:"groups,omitempty"`
		Credentials   []credentialV0 `json:"credentials,omitempty"`
	}{
		SchemaVersion: int(profileSchemaVersion),
		Profiles:      v0.Profiles,
		Groups:        v0.Groups,
		Credentials:   v0.Credentials,
	}

	migrated, err := json.Marshal(v1Data)
	if err != nil {
		return nil, fmt.Errorf("marshal v1 document: %w", err)
	}
	return migrated, nil
}

// hostMatch compares two hostnames case-insensitively.
func hostMatch(a, b string) bool {
	return strings.EqualFold(a, b)
}

// hasGrant checks if a grant with the same profileId, host, and port already exists.
func hasGrant(grants []CredentialTrustedEndpoint, grant CredentialTrustedEndpoint) bool {
	for _, g := range grants {
		if g.ProfileID == grant.ProfileID && g.Host == grant.Host && g.Port == grant.Port {
			return true
		}
	}
	return false
}
