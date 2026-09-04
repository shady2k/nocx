package profile

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// desiredMode cascade — the first of the three delivery axes (spec §3.5,
// nocx-mlm7). One row per cascade layer, covering each of the three mode
// values: the field set ONLY at that layer resolves to it, and a lower layer
// setting it too must not win. The final row is the case a user actually
// relies on: explicit raw at the profile over script at the group.
// ---------------------------------------------------------------------------

func TestResolveEffectiveProfile_DesiredModePrecedence(t *testing.T) {
	tests := []struct {
		name       string
		profile    SSHProfile
		groups     []ProfileGroup
		global     SparseSSHOptions
		want       DesiredMode
		wantSource FieldSource
	}{
		{
			name: "hardcoded default is auto — silence, not an answer (ADR-0033)",
			profile: SSHProfile{
				Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
				Options: StoredSSHProfileOptions{Host: "h"},
			},
			want:       DesiredAuto,
			wantSource: FieldSourceDefault,
		},
		{
			name: "global layer resolves helper",
			profile: SSHProfile{
				Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
				Options: StoredSSHProfileOptions{Host: "h"},
			},
			global:     SparseSSHOptions{DesiredMode: new(DesiredHelper)},
			want:       DesiredHelper,
			wantSource: FieldSourceGlobal,
		},
		{
			name: "ancestor group layer, no nearer layer",
			profile: SSHProfile{
				Base:    Base{ID: "p1", Type: "ssh", Name: "web", Group: "g2"},
				Options: StoredSSHProfileOptions{Host: "h"},
			},
			groups: []ProfileGroup{
				{ID: "g1", Name: "Root", Defaults: &ProfileDefaults{
					SparseSSHOptions: SparseSSHOptions{DesiredMode: new(DesiredRaw)},
				}},
				{ID: "g2", Name: "Leaf", ParentGroupID: "g1"},
			},
			want:       DesiredRaw,
			wantSource: fieldSourceForGroup("g1"),
		},
		{
			name: "nearest group wins over ancestor",
			profile: SSHProfile{
				Base:    Base{ID: "p1", Type: "ssh", Name: "web", Group: "g2"},
				Options: StoredSSHProfileOptions{Host: "h"},
			},
			groups: []ProfileGroup{
				{ID: "g1", Name: "Root", Defaults: &ProfileDefaults{
					SparseSSHOptions: SparseSSHOptions{DesiredMode: new(DesiredScript)},
				}},
				{ID: "g2", Name: "Leaf", ParentGroupID: "g1", Defaults: &ProfileDefaults{
					SparseSSHOptions: SparseSSHOptions{DesiredMode: new(DesiredRaw)},
				}},
			},
			want:       DesiredRaw,
			wantSource: fieldSourceForGroup("g2"),
		},
		{
			name: "profile wins over group",
			profile: SSHProfile{
				Base:    Base{ID: "p1", Type: "ssh", Name: "web", Group: "g1"},
				Options: StoredSSHProfileOptions{Host: "h", DesiredMode: new(DesiredHelper)},
			},
			groups: []ProfileGroup{
				{ID: "g1", Name: "Prod", Defaults: &ProfileDefaults{
					SparseSSHOptions: SparseSSHOptions{DesiredMode: new(DesiredScript)},
				}},
			},
			want:       DesiredHelper,
			wantSource: FieldSourceProfile,
		},
		{
			name: "explicit raw at profile over script at group",
			profile: SSHProfile{
				Base:    Base{ID: "p1", Type: "ssh", Name: "web", Group: "g1"},
				Options: StoredSSHProfileOptions{Host: "h", DesiredMode: new(DesiredRaw)},
			},
			groups: []ProfileGroup{
				{ID: "g1", Name: "Prod", Defaults: &ProfileDefaults{
					SparseSSHOptions: SparseSSHOptions{DesiredMode: new(DesiredScript)},
				}},
			},
			want:       DesiredRaw,
			wantSource: FieldSourceProfile,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eff, err := ResolveEffectiveProfile(tt.profile, tt.groups, tt.global)
			if err != nil {
				t.Fatalf("ResolveEffectiveProfile: %v", err)
			}
			if got := eff.ResolvedOptions.DesiredMode; got != tt.want {
				t.Errorf("desiredMode = %q, want %q", got, tt.want)
			}
			if got := eff.Source["desiredMode"]; got != tt.wantSource {
				t.Errorf("provenance for desiredMode = %q, want %q", got, tt.wantSource)
			}
		})
	}
}

func TestResolveEffectiveProfile_DesiredModeInvalidStoredFallsBackToDefault(t *testing.T) {
	// A stored value this build does not recognise falls back to the default
	// (auto — ADR-0033) rather than being treated as a silent no-op. auto
	// wraps and installs the scripts exactly as script does, so the safe
	// behaviour for an unrecognised choice is unchanged; what it does not do
	// is claim the user answered. The provenance says "default", so the
	// effective view shows the fallback instead of a value that never takes
	// effect.
	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "web", Group: "g1"},
		Options: StoredSSHProfileOptions{
			Host:        "h",
			DesiredMode: new(DesiredMode("sometimes")),
		},
	}
	groups := []ProfileGroup{
		{ID: "g1", Name: "Prod", Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{DesiredMode: new(DesiredRaw)},
		}},
	}
	eff, err := ResolveEffectiveProfile(profile, groups, SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if got := eff.ResolvedOptions.DesiredMode; got != DesiredAuto {
		t.Errorf("desiredMode = %q, want %q (fallback)", got, DesiredAuto)
	}
	if got := eff.Source["desiredMode"]; got != FieldSourceDefault {
		t.Errorf("provenance for desiredMode = %q, want %q", got, FieldSourceDefault)
	}
}

// ---------------------------------------------------------------------------
// helperConsent — the third axis (spec §3.5). Persisted per destination,
// NEVER inherited through the cascade: a group cannot express consent,
// script mode never reads it.
// ---------------------------------------------------------------------------

func TestResolveEffectiveProfile_HelperConsentInvalidStoredFallsBackToUnknown(t *testing.T) {
	// Consent is per-destination and not part of the cascade, so the same
	// unrecognised-value rule applies at the profile's own stored options:
	// an unknown value must never reach the wire dressed as a real choice.
	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{
			Host:          "h",
			HelperConsent: new(HelperConsent("maybe")),
		},
	}
	eff, err := ResolveEffectiveProfile(profile, nil, SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if got := *eff.Profile.Options.HelperConsent; got != ConsentUnknown {
		t.Errorf("helperConsent = %q, want %q (fallback)", got, ConsentUnknown)
	}
}

func TestHelperConsentIsNotAnInheritableDefaultKey(t *testing.T) {
	// Consent is per destination, so the group-defaults allowlist must
	// reject it as an unknown key — a group "default consent" would be a
	// second, conflicting owner of one fact.
	var d ProfileDefaults
	if err := d.UnmarshalJSON([]byte(`{"helperConsent":"granted"}`)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if keys := d.UnknownKeys(); len(keys) != 1 || keys[0] != "helperConsent" {
		t.Errorf("UnknownKeys = %v, want [helperConsent] — consent must not be inheritable", keys)
	}
}

func TestHelperConsentJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	store := NewJSONStore(path)

	prof := SSHProfile{
		Base: Base{ID: NewProfileID("ssh", "consent-roundtrip"), Type: "ssh", Name: "consent-roundtrip"},
		Options: StoredSSHProfileOptions{
			Host:          "h",
			HelperConsent: new(ConsentGranted),
		},
	}
	if err := store.CreateProfile(prof); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	loaded, err := NewJSONStore(path).LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d profiles, want 1", len(loaded))
	}
	if loaded[0].Options.HelperConsent == nil || *loaded[0].Options.HelperConsent != ConsentGranted {
		t.Errorf("round-tripped helperConsent = %v, want granted", loaded[0].Options.HelperConsent)
	}
}

func TestDesiredModeStoredOptionsMarshal(t *testing.T) {
	// The stored JSON must carry the field with the exact enum spelling
	// (host is always present — it has no omitempty).
	raw, err := json.Marshal(StoredSSHProfileOptions{DesiredMode: new(DesiredRaw)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(raw); got != `{"host":"","desiredMode":"raw"}` {
		t.Errorf("marshalled = %s, want {\"host\":\"\",\"desiredMode\":\"raw\"}", got)
	}
}

func TestHelperConsentStoredOptionsMarshal(t *testing.T) {
	raw, err := json.Marshal(StoredSSHProfileOptions{HelperConsent: new(ConsentDenied)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if got := string(raw); got != `{"host":"","helperConsent":"denied"}` {
		t.Errorf("marshalled = %s, want {\"host\":\"\",\"helperConsent\":\"denied\"}", got)
	}
}

// ---------------------------------------------------------------------------
// Patch paths
// ---------------------------------------------------------------------------

func TestApplyPatchDesiredMode(t *testing.T) {
	opts := &StoredSSHProfileOptions{}

	if !ApplyPatchSet(opts, "options.desiredMode", "helper") {
		t.Fatal("ApplyPatchSet(options.desiredMode) returned false for a known path")
	}
	if opts.DesiredMode == nil || *opts.DesiredMode != DesiredHelper {
		t.Fatalf("after set: DesiredMode = %v, want helper", opts.DesiredMode)
	}

	if !ApplyPatchSet(opts, "options.desiredMode", "raw") {
		t.Fatal("ApplyPatchSet(options.desiredMode) returned false for a known path")
	}
	if opts.DesiredMode == nil || *opts.DesiredMode != DesiredRaw {
		t.Fatalf("after re-set: DesiredMode = %v, want raw", opts.DesiredMode)
	}

	if !ApplyPatchUnset(opts, "options.desiredMode") {
		t.Fatal("ApplyPatchUnset(options.desiredMode) returned false for a known path")
	}
	if opts.DesiredMode != nil {
		t.Fatalf("after unset: DesiredMode = %v, want nil (inherit)", opts.DesiredMode)
	}

	if ApplyPatchSet(opts, "options.notARealPath", "helper") {
		t.Fatal("ApplyPatchSet accepted an unknown path")
	}
	if !PatchPathAllowed("options.desiredMode") {
		t.Fatal("options.desiredMode must be an allowed patch path")
	}
}

func TestApplyPatchHelperConsent(t *testing.T) {
	opts := &StoredSSHProfileOptions{}

	if !ApplyPatchSet(opts, "options.helperConsent", "granted") {
		t.Fatal("ApplyPatchSet(options.helperConsent) returned false for a known path")
	}
	if opts.HelperConsent == nil || *opts.HelperConsent != ConsentGranted {
		t.Fatalf("after set: HelperConsent = %v, want granted", opts.HelperConsent)
	}

	if !ApplyPatchUnset(opts, "options.helperConsent") {
		t.Fatal("ApplyPatchUnset(options.helperConsent) returned false for a known path")
	}
	if opts.HelperConsent != nil {
		t.Fatalf("after unset: HelperConsent = %v, want nil", opts.HelperConsent)
	}

	if !PatchPathAllowed("options.helperConsent") {
		t.Fatal("options.helperConsent must be an allowed patch path")
	}
}

// ---------------------------------------------------------------------------
// Group defaults allowlist — the mode must be known, not an unknown key
// ---------------------------------------------------------------------------

func TestDesiredModeIsAnAllowedDefaultKey(t *testing.T) {
	var d ProfileDefaults
	if err := d.UnmarshalJSON([]byte(`{"desiredMode":"helper"}`)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if keys := d.UnknownKeys(); len(keys) != 0 {
		t.Errorf("UnknownKeys = %v, want none — desiredMode must be a known default key", keys)
	}
	if d.DesiredMode == nil || *d.DesiredMode != DesiredHelper {
		t.Errorf("decoded desiredMode = %v, want helper", d.DesiredMode)
	}
	if err := d.Validate(); err != nil {
		t.Errorf("Validate = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Effective DTO — the mode rides the fields map with provenance; the consent
// rides the DTO as a required top-level field so a helper selection can never
// silently pretend to be granted.
// ---------------------------------------------------------------------------

func TestToEffectiveDTOIncludesDesiredMode(t *testing.T) {
	profile := SSHProfile{
		Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{Host: "h", DesiredMode: new(DesiredRaw)},
	}
	eff, err := ResolveEffectiveProfile(profile, nil, SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	dto := ToEffectiveDTO(eff, nil)
	f, ok := dto.Fields["desiredMode"]
	if !ok {
		t.Fatal("effective fields missing desiredMode")
	}
	if f.Value != "raw" {
		t.Errorf("desiredMode value = %v, want raw", f.Value)
	}
	if f.Source.Kind != EffectiveSourceProfile {
		t.Errorf("desiredMode source kind = %q, want profile", f.Source.Kind)
	}
}

func TestToEffectiveDTOCarriesHelperConsent(t *testing.T) {
	// Unset consent resolves to unknown — the required field is always
	// present, never absent-and-assumed.
	eff, err := ResolveEffectiveProfile(SSHProfile{
		Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{Host: "h"},
	}, nil, SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if got := ToEffectiveDTO(eff, nil).HelperConsent; got != ConsentUnknown {
		t.Errorf("unset helperConsent = %q, want unknown", got)
	}

	eff, err = ResolveEffectiveProfile(SSHProfile{
		Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{Host: "h", HelperConsent: new(ConsentGranted)},
	}, nil, SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if got := ToEffectiveDTO(eff, nil).HelperConsent; got != ConsentGranted {
		t.Errorf("granted helperConsent = %q, want granted", got)
	}
}
