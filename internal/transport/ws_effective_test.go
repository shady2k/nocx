package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
)

// TestEffectiveProfile_ProvenanceAndPatch is the required validation test
// from the brief (§6, items 1-7).
//  1. Store a profile with no local port or user.
//  2. Give its group port 2222 and a password-secret binding.
//  3. Call profiles.effective. Assert: port provenance is the group and the
//     passwordSecret field carries the row handle, never the reference.
//  4. Assert the raw JSON contains none of the reference canaries.
//  5. profiles.patch set options.port, then unset it.
//  6. Reload from storage: assert the stored port is absent, not 2222,
//     while the returned effective port is 2222 sourced from the group.
func TestEffectiveProfile_ProvenanceAndPatch(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(dir + "/p.json")
	// Step 2-3: Create a group with port 2222 and a password-secret binding.
	const groupID = "group-prod"
	// A backend-owned reference (ADR-0011 §2) — it must never reach the
	// renderer; the effective DTO carries its row handle instead.
	const pwRef = "sec:v1:test:00112233445566778899aabbccddeeff" //nolint:gosec // a synthetic backend-owned reference (ADR-0011 §2) for the wire-contract test, not a credential

	grp := profile.ProfileGroup{
		ID:   groupID,
		Name: "Prod",
		Defaults: &profile.ProfileDefaults{
			SparseSSHOptions: profile.SparseSSHOptions{
				Port:           intPtr(2222),
				PasswordSecret: strPtr(pwRef),
			},
		},
	}
	if err := ps.CreateGroup(grp); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Identity lives inline on the profile (ADR-0017): user and auth are the
	// profile's own, never resolved from a credential record.
	prof := profile.SSHProfile{
		Base: profile.Base{
			ID:    "ssh:prod-api:1",
			Type:  "ssh",
			Name:  "prod-api",
			Group: groupID,
		},
		Options: profile.StoredSSHProfileOptions{
			Host: "api.prod.example.com",
			User: strPtr("deploy"),
			Auth: profile.Ptr(profile.AuthPublicKey),
		},
	}
	if err := ps.CreateProfile(prof); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	// Wire WSServer with stores.
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)),
		WithProfileRepository(ps), WithGroupRepository(ps),
		WithCredentialStore(newTestStore()))
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)
	t.Cleanup(func() { _ = conn.Close() })

	// Step 4: Call profiles.effective.
	resp := jsonrpcCall(t, conn, "profiles.effective", map[string]any{
		"ids": []string{"ssh:prod-api:1"},
	})

	var effResult struct {
		Result struct {
			Profiles []profile.EffectiveProfileDTO `json:"profiles"`
			Errors   []profileErrorEntry           `json:"errors"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &effResult); err != nil {
		t.Fatalf("unmarshal effective result: %v\nraw: %s", err, string(resp))
	}

	if len(effResult.Result.Errors) > 0 {
		t.Fatalf("unexpected errors: %+v", effResult.Result.Errors)
	}
	if len(effResult.Result.Profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(effResult.Result.Profiles))
	}

	dto := effResult.Result.Profiles[0]
	if dto.ID != "ssh:prod-api:1" {
		t.Errorf("profile id = %q, want ssh:prod-api:1", dto.ID)
	}

	// Assert port provenance is group.
	portField, ok := dto.Fields["port"]
	if !ok {
		t.Fatal("missing field: port")
	}
	if v, isNum := portField.Value.(float64); !isNum || v != 2222 {
		t.Errorf("port value = %v, want 2222", portField.Value)
	}
	if portField.Source.Kind != profile.EffectiveSourceGroup {
		t.Errorf("port source kind = %q, want %q", portField.Source.Kind, profile.EffectiveSourceGroup)
	}
	if portField.Source.ID != groupID {
		t.Errorf("port source id = %q, want %q", portField.Source.ID, groupID)
	}
	if portField.Source.Label != "Prod" {
		t.Errorf("port source label = %q, want Prod", portField.Source.Label)
	}

	// Assert passwordSecret provenance is group, and its value is the ROW
	// HANDLE, never the reference.
	pwField, ok := dto.Fields["passwordSecret"]
	if !ok {
		t.Fatal("missing field: passwordSecret")
	}
	if pwField.Value != vault.RowFor(credential.SecretID(pwRef)) {
		t.Errorf("passwordSecret value = %v, want the row handle of %s", pwField.Value, pwRef)
	}
	if pwField.Source.Kind != profile.EffectiveSourceGroup {
		t.Errorf("passwordSecret source kind = %q, want %q", pwField.Source.Kind, profile.EffectiveSourceGroup)
	}

	// Assert user provenance is the profile itself (ADR-0017).
	userField, ok := dto.Fields["user"]
	if !ok {
		t.Fatal("missing field: user")
	}
	if userField.Value != "deploy" {
		t.Errorf("user value = %v, want deploy", userField.Value)
	}
	if userField.Source.Kind != profile.EffectiveSourceProfile {
		t.Errorf("user source kind = %q, want %q", userField.Source.Kind, profile.EffectiveSourceProfile)
	}

	// Assert auth provenance is the profile itself (ADR-0017).
	authField, ok := dto.Fields["auth"]
	if !ok {
		t.Fatal("missing field: auth")
	}
	if authField.Value != "publicKey" {
		t.Errorf("auth value = %v, want publicKey", authField.Value)
	}
	if authField.Source.Kind != profile.EffectiveSourceProfile {
		t.Errorf("auth source kind = %q, want %q", authField.Source.Kind, profile.EffectiveSourceProfile)
	}

	// Step 5: Assert raw JSON contains none of the canary secret references —
	// the wire carries the row handle, and nothing else.
	rawJSON, err := json.Marshal(effResult.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	rawStr := string(rawJSON)
	if strings.Contains(rawStr, pwRef) {
		t.Errorf("raw JSON leaks the secret reference %q", pwRef)
	}
	if !strings.Contains(rawStr, vault.RowFor(credential.SecretID(pwRef))) {
		t.Errorf("raw JSON does not carry the row handle for %q", pwRef)
	}
	// Step 6: Patch set options.port=2200, then unset options.port.
	patchSetResp := jsonrpcCall(t, conn, "profiles.patch", map[string]any{
		"id":  "ssh:prod-api:1",
		"set": map[string]any{"options.port": float64(2200)},
	})
	var patchSetResult struct {
		Result profile.EffectiveProfileDTO `json:"result"`
	}
	if unmarshalErr := json.Unmarshal(patchSetResp, &patchSetResult); unmarshalErr != nil {
		t.Fatalf("unmarshal patch set result: %v\nraw: %s", err, string(patchSetResp))
	}
	if patchSetResult.Result.ID != "ssh:prod-api:1" {
		t.Errorf("patch set returned id %q", patchSetResult.Result.ID)
	}
	// After set, port should be 2200 from profile.
	pf := patchSetResult.Result.Fields["port"]
	if v, ok := pf.Value.(float64); !ok || v != 2200 {
		t.Errorf("after set, port = %v, want 2200", pf.Value)
	}
	if pf.Source.Kind != profile.EffectiveSourceProfile {
		t.Errorf("after set, port source = %q, want profile", pf.Source.Kind)
	}

	// Now unset port.
	patchUnsetResp := jsonrpcCall(t, conn, "profiles.patch", map[string]any{
		"id":    "ssh:prod-api:1",
		"unset": []string{"options.port"},
	})
	var patchUnsetResult struct {
		Result profile.EffectiveProfileDTO `json:"result"`
	}
	if unmarshalErr := json.Unmarshal(patchUnsetResp, &patchUnsetResult); unmarshalErr != nil {
		t.Fatalf("unmarshal patch unset result: %v\nraw: %s", err, string(patchUnsetResp))
	}
	// After unset, effective port should be 2222 from group.
	puf := patchUnsetResult.Result.Fields["port"]
	if v, ok := puf.Value.(float64); !ok || v != 2222 {
		t.Errorf("after unset, effective port = %v, want 2222", puf.Value)
	}
	if puf.Source.Kind != profile.EffectiveSourceGroup {
		t.Errorf("after unset, port source = %q, want group", puf.Source.Kind)
	}

	// Step 7: Reload from storage and assert stored port is absent (nil).
	storedProfiles, err := ps.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	var stored *profile.SSHProfile
	for i := range storedProfiles {
		if storedProfiles[i].ID == "ssh:prod-api:1" {
			stored = &storedProfiles[i]
			break
		}
	}
	if stored == nil {
		t.Fatal("stored profile not found after reload")
	}
	if stored.Options.Port != nil {
		t.Errorf("stored port = %v, want nil (absent, inheriting from group)", stored.Options.Port)
	}
}

// TestEffectiveProfile_ExplicitFalseSurvivesRoundTrip proves that the
// presence-aware storage correctly preserves explicit false/zero values:
// "profile explicit false/zero" remains distinguishable from "inherit"
// through a store-load-resolve cycle.
//
// This is the foundational bug from §3.3: before this change, the dense-to-
// sparse projection treated false as "not set", so agentForward=false in a
// profile with group default agentForward=true resolved to true, sourced
// "group".
func TestEffectiveProfile_ExplicitFalseSurvivesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ps := profile.NewJSONStore(dir + "/p.json")

	// Group: agentForward = true, keepaliveInterval = 5000.
	const groupID = "group-test"
	grp := profile.ProfileGroup{
		ID:   groupID,
		Name: "TestGroup",
		Defaults: &profile.ProfileDefaults{
			SparseSSHOptions: profile.SparseSSHOptions{
				AgentForward:      profile.Ptr(true),
				KeepaliveInterval: profile.Ptr(5000),
			},
		},
	}
	if err := ps.CreateGroup(grp); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Profile: agentForward = false, keepaliveInterval = 0 (explicit disable).
	prof := profile.SSHProfile{
		Base: profile.Base{
			ID:    "ssh:explicit-false:1",
			Type:  "ssh",
			Name:  "explicit-false",
			Group: groupID,
		},
		Options: profile.StoredSSHProfileOptions{
			Host:              "test.example.com",
			AgentForward:      profile.Ptr(false),
			KeepaliveInterval: profile.Ptr(0),
		},
	}
	if err := ps.CreateProfile(prof); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}

	// Load from storage (round trip).
	loaded, err := ps.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	var stored *profile.SSHProfile
	for i := range loaded {
		if loaded[i].ID == "ssh:explicit-false:1" {
			stored = &loaded[i]
			break
		}
	}
	if stored == nil {
		t.Fatal("stored profile not found after round trip")
	}

	// Assert stored representation preserves explicit false and zero.
	if stored.Options.AgentForward == nil {
		t.Fatal("agentForward should be non-nil after round trip (was explicitly set to false)")
	}
	if *stored.Options.AgentForward {
		t.Errorf("stored agentForward = true, want false")
	}
	if stored.Options.KeepaliveInterval == nil {
		t.Fatal("keepaliveInterval should be non-nil after round trip (was explicitly set to 0)")
	}
	if *stored.Options.KeepaliveInterval != 0 {
		t.Errorf("stored keepaliveInterval = %d, want 0", *stored.Options.KeepaliveInterval)
	}

	// Resolve effective profile — assertions MUST check both value AND provenance.
	groups, err := ps.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}
	eff, err := profile.ResolveEffectiveProfile(*stored, groups, profile.SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}

	// agentForward must be false, sourced "profile" (not inherited "group").
	if eff.ResolvedOptions.AgentForward {
		t.Errorf("effective agentForward = true, want false (profile overrides group default)")
	}
	if eff.Source["agentForward"] != profile.FieldSourceProfile {
		t.Errorf("agentForward source = %q, want %q", eff.Source["agentForward"], profile.FieldSourceProfile)
	}

	// keepaliveInterval must be 0, sourced "profile".
	if eff.ResolvedOptions.KeepaliveInterval != 0 {
		t.Errorf("effective keepaliveInterval = %d, want 0", eff.ResolvedOptions.KeepaliveInterval)
	}
	if eff.Source["keepaliveInterval"] != profile.FieldSourceProfile {
		t.Errorf("keepaliveInterval source = %q, want %q", eff.Source["keepaliveInterval"], profile.FieldSourceProfile)
	}
}

// intPtr returns a pointer to v.
func intPtr(v int) *int { return &v }

// strPtr returns a pointer to v.
func strPtr(v string) *string { return &v }
