package profile

import "testing"

// A profile holding a reference in every secret-binding field. The point of
// the fixture is that a reset must find all of them: a sweep that clears only
// one field leaves a store that still claims to hold secrets that are gone.
func profileWithReferencesEverywhere() SSHProfile {
	return SSHProfile{
		Base: Base{ID: "ssh:everywhere:1", Type: "ssh", Name: "everywhere"},
		Options: StoredSSHProfileOptions{
			Host:                "h",
			PasswordSecret:      "sec:v1:file:aaaa",
			KeySecret:           "sec:v1:file:bbbb",
			KeyPassphraseSecret: "sec:v1:file:cccc",
		},
	}
}

func TestCountSecretReferences_CountsEveryPlaceAReferenceLives(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProfile(profileWithReferencesEverywhere()); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	impact, err := s.CountSecretReferences()
	if err != nil {
		t.Fatalf("CountSecretReferences: %v", err)
	}
	if impact.SecretCount != 3 {
		t.Errorf("SecretCount = %d, want 3", impact.SecretCount)
	}
	if impact.ProfileCount != 1 {
		t.Errorf("ProfileCount = %d, want 1", impact.ProfileCount)
	}
}

// Distinct secrets, not distinct fields. One secret shared by two binding
// fields is one thing the user loses, and telling them "2 saved passwords"
// when there is one overstates the damage in a confirmation they are reading
// to decide.
func TestCountSecretReferences_CountsASharedSecretOnce(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProfile(SSHProfile{
		Base: Base{ID: "ssh:shared:1", Type: "ssh", Name: "shared"},
		Options: StoredSSHProfileOptions{
			Host:           "h",
			PasswordSecret: "sec:v1:file:same",
			KeySecret:      "sec:v1:file:same",
		},
	}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	impact, err := s.CountSecretReferences()
	if err != nil {
		t.Fatalf("CountSecretReferences: %v", err)
	}
	if impact.SecretCount != 1 {
		t.Errorf("SecretCount = %d, want 1", impact.SecretCount)
	}
}

// A profile with no stored material must not be counted as affected — the
// confirmation would name connections that lose nothing.
func TestCountSecretReferences_IgnoresProfilesHoldingNothing(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProfile(SSHProfile{
		Base:    Base{ID: "ssh:agent:1", Type: "ssh", Name: "agent"},
		Options: StoredSSHProfileOptions{Host: "h"},
	}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	impact, err := s.CountSecretReferences()
	if err != nil {
		t.Fatalf("CountSecretReferences: %v", err)
	}
	if impact.SecretCount != 0 || impact.ProfileCount != 0 {
		t.Errorf("impact = %+v, want zero", impact)
	}
}

func TestCountSecretReferences_CountsProfilesHoldingReferences(t *testing.T) {
	s := newTestStore(t)
	profileUsing := func(id, name, ref string) SSHProfile {
		return SSHProfile{
			Base:    Base{ID: id, Type: "ssh", Name: name},
			Options: StoredSSHProfileOptions{Host: "h", PasswordSecret: ref},
		}
	}
	for _, p := range []SSHProfile{
		profileUsing("p1", "one", "sec:everywhere:1"),
		profileUsing("p2", "two", "sec:everywhere:1"),
		// Holds nothing: unaffected.
		profileUsing("p3", "three", ""),
	} {
		if err := s.CreateProfile(p); err != nil {
			t.Fatalf("CreateProfile %s: %v", p.ID, err)
		}
	}

	impact, err := s.CountSecretReferences()
	if err != nil {
		t.Fatalf("CountSecretReferences: %v", err)
	}
	if impact.ProfileCount != 2 {
		t.Errorf("ProfileCount = %d, want 2 — only profiles that lose something", impact.ProfileCount)
	}
}

// The operation the reset performs. Every reference goes; nothing else about
// the profile does — the user keeps their connections, their usernames and
// their key paths, and only stops claiming to hold secrets that no longer
// exist.
func TestClearAllSecretReferences_ClearsEveryPlaceAndKeepsTheRest(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProfile(profileWithReferencesEverywhere()); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	impact, err := s.ClearAllSecretReferences()
	if err != nil {
		t.Fatalf("ClearAllSecretReferences: %v", err)
	}
	if impact.SecretCount != 3 {
		t.Errorf("reported SecretCount = %d, want 3", impact.SecretCount)
	}

	profiles, err := s.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profile count = %d, want 1 — clearing references must not delete records", len(profiles))
	}
	p := profiles[0]

	if p.Options.PasswordSecret != "" || p.Options.KeySecret != "" || p.Options.KeyPassphraseSecret != "" {
		t.Errorf("secret bindings survived: %+v", p.Options)
	}
	// The identity of the profile is untouched.
	if p.Name != "everywhere" || p.Options.Host != "h" {
		t.Errorf("clearing references changed the profile itself: %+v", p)
	}
}

// The reset is re-run after being interrupted, so this must be safe to call
// twice. The second call reports nothing cleared, which is what the UI needs
// in order not to claim it destroyed something again.
func TestClearAllSecretReferences_IsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProfile(profileWithReferencesEverywhere()); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if _, err := s.ClearAllSecretReferences(); err != nil {
		t.Fatalf("first clear: %v", err)
	}

	impact, err := s.ClearAllSecretReferences()
	if err != nil {
		t.Fatalf("second clear: %v", err)
	}
	if impact.SecretCount != 0 {
		t.Errorf("second clear reported SecretCount = %d, want 0", impact.SecretCount)
	}
}

func TestClearAllSecretReferences_OnAnEmptyStore(t *testing.T) {
	s := newTestStore(t)
	impact, err := s.ClearAllSecretReferences()
	if err != nil {
		t.Fatalf("ClearAllSecretReferences: %v", err)
	}
	if impact.SecretCount != 0 || impact.ProfileCount != 0 {
		t.Errorf("impact = %+v, want zero", impact)
	}
}

// --- endpoints (ADR-0030, ADR-0031) -------------------------------------

// An endpoint holding a credential reference is a second kind of record the
// reset must count: a preview that ignores it under-reports what the user
// is about to lose.
func TestCountSecretReferences_CountsEndpointReferences(t *testing.T) {
	s := newTestStore(t)
	e := validTestEndpoint()
	e.CredentialRef = "sec:v1:file:endpointkey000000000000000000001"
	if err := s.CreateEndpoint(e); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	impact, err := s.CountSecretReferences()
	if err != nil {
		t.Fatalf("CountSecretReferences: %v", err)
	}
	if impact.SecretCount != 1 {
		t.Errorf("SecretCount = %d, want 1", impact.SecretCount)
	}
	if impact.ProfileCount != 0 {
		t.Errorf("ProfileCount = %d, want 0 — an endpoint is not a connection", impact.ProfileCount)
	}
	if impact.EndpointCount != 1 {
		t.Errorf("EndpointCount = %d, want 1", impact.EndpointCount)
	}
}

// An endpoint without a credential stores nothing and loses nothing: it is
// not counted, exactly like a profile with no stored material.
func TestCountSecretReferences_IgnoresEndpointsHoldingNothing(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateEndpoint(validTestEndpoint()); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}
	impact, err := s.CountSecretReferences()
	if err != nil {
		t.Fatalf("CountSecretReferences: %v", err)
	}
	if impact.SecretCount != 0 || impact.EndpointCount != 0 {
		t.Errorf("impact = %+v, want zero", impact)
	}
}

// Distinct secrets, not distinct records: a secret shared between a profile
// and an endpoint is one thing the user loses, not two — and the two kinds
// are counted separately because they answer different questions.
func TestCountSecretReferences_DistinctAcrossKinds(t *testing.T) {
	s := newTestStore(t)
	ref := "sec:v1:file:sharedacrosskinds000000000001"
	if err := s.CreateProfile(SSHProfile{
		Base:    Base{ID: "ssh:p:1", Type: "ssh", Name: "p"},
		Options: StoredSSHProfileOptions{Host: "h", PasswordSecret: ref},
	}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	e := validTestEndpoint()
	e.CredentialRef = ref
	if err := s.CreateEndpoint(e); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	impact, err := s.CountSecretReferences()
	if err != nil {
		t.Fatalf("CountSecretReferences: %v", err)
	}
	if impact.SecretCount != 1 {
		t.Errorf("SecretCount = %d, want 1", impact.SecretCount)
	}
	if impact.ProfileCount != 1 || impact.EndpointCount != 1 {
		t.Errorf("impact = %+v, want one profile and one endpoint", impact)
	}
}

// The reset clears endpoint references along with profile ones, in the same
// one write: the endpoint record survives with an empty credential, exactly
// as a profile survives without its password.
func TestClearAllSecretReferences_ClearsEndpointReferencesAndKeepsTheRecord(t *testing.T) {
	s := newTestStore(t)
	e := validTestEndpoint()
	e.CredentialRef = "sec:v1:file:endpointkey000000000000000000001"
	if err := s.CreateEndpoint(e); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	impact, err := s.ClearAllSecretReferences()
	if err != nil {
		t.Fatalf("ClearAllSecretReferences: %v", err)
	}
	if impact.SecretCount != 1 || impact.EndpointCount != 1 {
		t.Errorf("impact = %+v, want 1 secret and 1 endpoint", impact)
	}

	got, err := s.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if len(got) != 1 || got[0].ID != e.ID {
		t.Fatalf("endpoints = %+v, want the record to survive", got)
	}
	if got[0].CredentialRef != "" {
		t.Errorf("CredentialRef = %q, want cleared", got[0].CredentialRef)
	}
}

// The per-secret metadata-first path (a key deleted on the Secrets page)
// clears the endpoint's reference in the same write that clears profile
// bindings — the endpoint survives, credential-less.
func TestClearSecretRefs_ClearsEndpointReference(t *testing.T) {
	s := newTestStore(t)
	ref := "sec:v1:file:deletedfromsecrets0000000000001"
	if err := s.CreateProfile(SSHProfile{
		Base:    Base{ID: "ssh:p:1", Type: "ssh", Name: "p"},
		Options: StoredSSHProfileOptions{Host: "h", PasswordSecret: ref},
	}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	e := validTestEndpoint()
	e.CredentialRef = ref
	if err := s.CreateEndpoint(e); err != nil {
		t.Fatalf("CreateEndpoint: %v", err)
	}

	if err := s.ClearSecretRefs(ref); err != nil {
		t.Fatalf("ClearSecretRefs: %v", err)
	}

	profs, err := s.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if profs[0].Options.PasswordSecret != "" {
		t.Errorf("profile binding = %q, want cleared", profs[0].Options.PasswordSecret)
	}
	eps, err := s.LoadEndpoints()
	if err != nil {
		t.Fatalf("LoadEndpoints: %v", err)
	}
	if eps[0].CredentialRef != "" {
		t.Errorf("endpoint CredentialRef = %q, want cleared", eps[0].CredentialRef)
	}
}
