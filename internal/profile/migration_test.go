package profile

import (
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/storage"
)

// TestMigration_MatchCreatesGrant verifies that legacy binding matching canonical
// endpoint creates a TrustedEndpoint grant.
func TestMigration_MatchCreatesGrant(t *testing.T) {
	v0Data := `{
		"profiles": [{
			"id": "p1",
			"type": "ssh",
			"name": "prod",
			"options": {
				"host": "prod.example.com",
				"port": 22,
				"credentialId": "c1"
			}
		}],
		"credentials": [{
			"id": "c1",
			"name": "prod-key",
			"username": "admin",
			"auth": "publicKey",
			"host": "prod.example.com",
			"port": 22
		}]
	}`

	// Resolver returns same host/port (no SSH config alias)
	resolver := func(prof SSHProfile) (string, uint16, error) {
		return prof.Options.Host, uint16(prof.Options.Port), nil
	}

	migrated, err := migrateV0ToV1([]byte(v0Data), resolver)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var v1 storeDataV0
	if err := json.Unmarshal(migrated, &v1); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}

	if len(v1.Credentials) != 1 {
		t.Fatalf("want 1 credential, got %d", len(v1.Credentials))
	}

	cred := v1.Credentials[0]
	if len(cred.TrustedEndpoints) != 1 {
		t.Fatalf("want 1 grant, got %d", len(cred.TrustedEndpoints))
	}

	grant := cred.TrustedEndpoints[0]
	if grant.ProfileID != "p1" {
		t.Errorf("grant profileId = %q, want p1", grant.ProfileID)
	}
	if grant.Host != "prod.example.com" {
		t.Errorf("grant host = %q, want prod.example.com", grant.Host)
	}
	if grant.Port != 22 {
		t.Errorf("grant port = %d, want 22", grant.Port)
	}

	// Legacy fields cleared
	if cred.Host != "" || cred.Port != 0 {
		t.Errorf("legacy fields not cleared: host=%q, port=%d", cred.Host, cred.Port)
	}
}

// TestMigration_AliasResolution verifies that SSH config alias is resolved to
// canonical endpoint and grant uses canonical, not alias.
func TestMigration_AliasResolution(t *testing.T) {
	v0Data := `{
		"profiles": [{
			"id": "p1",
			"type": "ssh",
			"name": "prod",
			"options": {
				"host": "prod",
				"port": 22,
				"credentialId": "c1"
			}
		}],
		"credentials": [{
			"id": "c1",
			"name": "prod-key",
			"username": "admin",
			"auth": "publicKey",
			"host": "prod.example.com",
			"port": 22
		}]
	}`

	// Resolver expands alias "prod" -> "prod.example.com"
	resolver := func(prof SSHProfile) (string, uint16, error) {
		if prof.Options.Host == "prod" {
			return "prod.example.com", 22, nil
		}
		return prof.Options.Host, uint16(prof.Options.Port), nil
	}

	migrated, err := migrateV0ToV1([]byte(v0Data), resolver)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var v1 storeDataV0
	if err := json.Unmarshal(migrated, &v1); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}

	cred := v1.Credentials[0]
	if len(cred.TrustedEndpoints) != 1 {
		t.Fatalf("want 1 grant, got %d", len(cred.TrustedEndpoints))
	}

	grant := cred.TrustedEndpoints[0]
	// Grant must use canonical host, not alias
	if grant.Host != "prod.example.com" {
		t.Errorf("grant host = %q, want prod.example.com (canonical)", grant.Host)
	}
}

// TestMigration_MismatchMarksReview verifies that legacy binding not matching
// canonical endpoint marks profile as requiresReview and creates no grant.
func TestMigration_MismatchMarksReview(t *testing.T) {
	v0Data := `{
		"profiles": [{
			"id": "p1",
			"type": "ssh",
			"name": "prod",
			"options": {
				"host": "prod.example.com",
				"port": 22,
				"credentialId": "c1"
			}
		}],
		"credentials": [{
			"id": "c1",
			"name": "prod-key",
			"username": "admin",
			"auth": "publicKey",
			"host": "different.example.com",
			"port": 22
		}]
	}`

	resolver := func(prof SSHProfile) (string, uint16, error) {
		return prof.Options.Host, uint16(prof.Options.Port), nil
	}

	migrated, err := migrateV0ToV1([]byte(v0Data), resolver)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var v1 storeDataV0
	if err := json.Unmarshal(migrated, &v1); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}

	// Profile marked as requiresReview
	if !v1.Profiles[0].Options.RequiresReview {
		t.Error("profile not marked as requiresReview")
	}

	// No grant created
	cred := v1.Credentials[0]
	if len(cred.TrustedEndpoints) != 0 {
		t.Errorf("want 0 grants for mismatch, got %d", len(cred.TrustedEndpoints))
	}
}

// TestMigration_UnresolvableMarksReview verifies that resolver error marks
// profile as requiresReview.
func TestMigration_UnresolvableMarksReview(t *testing.T) {
	v0Data := `{
		"profiles": [{
			"id": "p1",
			"type": "ssh",
			"name": "broken",
			"options": {
				"host": "broken.example.com",
				"port": 22,
				"credentialId": "c1"
			}
		}],
		"credentials": [{
			"id": "c1",
			"name": "key",
			"username": "admin",
			"auth": "publicKey",
			"host": "broken.example.com",
			"port": 22
		}]
	}`

	resolver := func(prof SSHProfile) (string, uint16, error) {
		return "", 0, storage.ErrVersionTooNew // Simulate resolver error
	}

	migrated, err := migrateV0ToV1([]byte(v0Data), resolver)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var v1 storeDataV0
	if err := json.Unmarshal(migrated, &v1); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}

	if !v1.Profiles[0].Options.RequiresReview {
		t.Error("profile not marked as requiresReview on resolver error")
	}
}

// TestMigration_PortZeroMatchesAny verifies that legacy Port==0 matches any resolved port.
func TestMigration_PortZeroMatchesAny(t *testing.T) {
	v0Data := `{
		"profiles": [{
			"id": "p1",
			"type": "ssh",
			"name": "prod",
			"options": {
				"host": "prod.example.com",
				"port": 2222,
				"credentialId": "c1"
			}
		}],
		"credentials": [{
			"id": "c1",
			"name": "prod-key",
			"username": "admin",
			"auth": "publicKey",
			"host": "prod.example.com",
			"port": 0
		}]
	}`

	resolver := func(prof SSHProfile) (string, uint16, error) {
		return prof.Options.Host, uint16(prof.Options.Port), nil
	}

	migrated, err := migrateV0ToV1([]byte(v0Data), resolver)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var v1 storeDataV0
	if err := json.Unmarshal(migrated, &v1); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}

	cred := v1.Credentials[0]
	if len(cred.TrustedEndpoints) != 1 {
		t.Fatalf("want 1 grant for Port==0, got %d", len(cred.TrustedEndpoints))
	}

	grant := cred.TrustedEndpoints[0]
	if grant.Port != 2222 {
		t.Errorf("grant port = %d, want 2222 (resolved)", grant.Port)
	}

	// Profile not marked as requiresReview
	if v1.Profiles[0].Options.RequiresReview {
		t.Error("profile incorrectly marked as requiresReview for Port==0")
	}
}

// TestMigration_NilResolverMarksAllReview verifies that nil resolver marks all
// profiles with legacy bindings as requiresReview.
func TestMigration_NilResolverMarksAllReview(t *testing.T) {
	v0Data := `{
		"profiles": [{
			"id": "p1",
			"type": "ssh",
			"name": "prod",
			"options": {
				"host": "prod.example.com",
				"port": 22,
				"credentialId": "c1"
			}
		}],
		"credentials": [{
			"id": "c1",
			"name": "prod-key",
			"username": "admin",
			"auth": "publicKey",
			"host": "prod.example.com",
			"port": 22
		}]
	}`

	// nil resolver
	migrated, err := migrateV0ToV1([]byte(v0Data), nil)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var v1 storeDataV0
	if err := json.Unmarshal(migrated, &v1); err != nil {
		t.Fatalf("unmarshal migrated: %v", err)
	}

	if !v1.Profiles[0].Options.RequiresReview {
		t.Error("profile not marked as requiresReview with nil resolver")
	}

	// No grant created
	if len(v1.Credentials[0].TrustedEndpoints) != 0 {
		t.Error("grant created despite nil resolver")
	}
}
