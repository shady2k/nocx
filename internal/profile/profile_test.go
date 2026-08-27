package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHardcodedDefaults(t *testing.T) {
	d := hardcodedDefaults()
	if d.Port == nil || *d.Port != 22 {
		t.Errorf("default port = %v, want 22", d.Port)
	}
	if d.User == nil || *d.User == "" {
		t.Errorf("default user should be set, got nil/empty")
	}
	if d.BehaviorOnSessionEnd == nil || *d.BehaviorOnSessionEnd != BehaviorAuto {
		t.Errorf("default behavior = %v, want auto", d.BehaviorOnSessionEnd)
	}
}

func TestProfileIDNamespace(t *testing.T) {
	id := NewProfileID("ssh", "  My Host  ")
	if !isNamespacedID(id) {
		t.Fatalf("generated id %q is not namespaced", id)
	}
	parsed, ok := parseNamespacedID(id)
	if !ok {
		t.Fatalf("could not parse id %q", id)
	}
	if parsed.Type != "ssh" {
		t.Errorf("type = %q, want ssh", parsed.Type)
	}
	if parsed.Name != "my-host" {
		t.Errorf("name = %q, want my-host", parsed.Name)
	}
}

func TestProfileIDSlugPreservesUnicodeLowercaseIntoASCII(t *testing.T) {
	id := NewProfileID("ssh", "K")
	parsed, ok := parseNamespacedID(id)
	if !ok {
		t.Fatalf("could not parse id %q", id)
	}
	if parsed.Name != "k" {
		t.Fatalf("slug = %q, want k — bounded slugification must preserve strings.ToLower semantics", parsed.Name)
	}
}

func TestMintedIDsRespectThe128RuneBound(t *testing.T) {
	const wantMaxIDRunes = 128
	longASCII := strings.Repeat("x", 10_000)
	longUnicode := strings.Repeat("界", 10_000)
	tests := map[string]func() string{
		"profile long name":      func() string { return NewProfileID("ssh", longASCII) },
		"profile unicode name":   func() string { return NewProfileID("ssh", longUnicode) },
		"profile long namespace": func() string { return NewProfileID(longUnicode, "host") },
		"group":                  func() string { return NewGroupID(longASCII) },
		"endpoint":               func() string { return NewEndpointID(longASCII) },
	}
	for name, mint := range tests {
		t.Run(name, func(t *testing.T) {
			id := mint()
			if got := utf8.RuneCountInString(id); got > wantMaxIDRunes {
				t.Fatalf("minted id has %d runes, want at most %d", got, wantMaxIDRunes)
			}
			if _, ok := parseNamespacedID(id); !ok {
				t.Fatalf("minted id %q is not namespaced", id)
			}
		})
	}
}

func TestMintedIDSlugBudgetsAreInclusive(t *testing.T) {
	const wantMaxIDRunes = 128
	tests := []struct {
		name   string
		budget int
		mint   func(string) string
	}{
		{name: "profile", budget: 84, mint: func(name string) string { return NewProfileID("ssh", name) }},
		{name: "group", budget: 82, mint: NewGroupID},
		{name: "endpoint", budget: 79, mint: NewEndpointID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			atLimit := strings.Repeat("a", tc.budget)
			overLimit := atLimit + "b"
			for label, input := range map[string]string{"at limit": atLimit, "over limit": overLimit} {
				id := tc.mint(input)
				parts, ok := parseNamespacedID(id)
				if !ok {
					t.Fatalf("%s: minted id %q is not namespaced", label, id)
				}
				if parts.Name != atLimit {
					t.Errorf("%s: slug = %q, want %q", label, parts.Name, atLimit)
				}
				if got := utf8.RuneCountInString(id); got != wantMaxIDRunes {
					t.Errorf("%s: id has %d runes, want %d", label, got, wantMaxIDRunes)
				}
			}
		})
	}
}

func TestMintedIDTruncationKeepsUUIDUniqueness(t *testing.T) {
	name := strings.Repeat("same-prefix", 100)
	first := NewProfileID("ssh", name)
	second := NewProfileID("ssh", name)
	if first == second {
		t.Fatalf("two mints returned the same id %q", first)
	}
}

func TestGroupTreeNested(t *testing.T) {
	groups := []ProfileGroup{
		{ID: "g1", Name: "Prod"},
		{ID: "g2", Name: "Staging", ParentGroupID: "g1"},
		{ID: "g3", Name: "Dev", ParentGroupID: "g2"},
		{ID: "g4", Name: "Orphan"},
	}

	roots := BuildGroupTree(groups)
	if len(roots) != 2 {
		t.Fatalf("roots = %d, want 2 (g1 + orphan g4)", len(roots))
	}

	var prod, orphan *treeNode
	for i := range roots {
		if roots[i].ID == "g1" {
			prod = &roots[i]
		}
		if roots[i].ID == "g4" {
			orphan = &roots[i]
		}
	}
	if prod == nil || orphan == nil {
		t.Fatalf("g1/g4 not found in roots")
	}
	if len(orphan.Children) != 0 {
		t.Errorf("orphan g4 has %d children, want 0", len(orphan.Children))
	}
	if len(prod.Children) != 1 || prod.Children[0].ID != "g2" {
		t.Fatalf("prod g1 children wrong: %+v", prod.Children)
	}
	if len(prod.Children[0].Children) != 1 || prod.Children[0].Children[0].ID != "g3" {
		t.Fatalf("g2 should have g3 as only child: %+v", prod.Children[0].Children)
	}
}

func TestGroupPathBreadcrumb(t *testing.T) {
	groups := []ProfileGroup{
		{ID: "g1", Name: "Prod"},
		{ID: "g2", Name: "Staging", ParentGroupID: "g1"},
		{ID: "g3", Name: "web-1", ParentGroupID: "g2"},
	}

	path := ResolveGroupPath(groups, "g3")
	want := []string{"Prod", "Staging", "web-1"}
	if len(path) != len(want) {
		t.Fatalf("path len = %d, want %d (%v)", len(path), len(want), path)
	}
	for i, s := range want {
		if path[i] != s {
			t.Errorf("path[%d] = %q, want %q", i, path[i], s)
		}
	}
}

func TestGroupPathCycleGuard(t *testing.T) {
	groups := []ProfileGroup{
		{ID: "g1", Name: "A", ParentGroupID: "g2"},
		{ID: "g2", Name: "B", ParentGroupID: "g1"},
	}
	path := ResolveGroupPath(groups, "g1")
	if len(path) == 0 {
		t.Fatalf("cycle should not produce empty path")
	}
	if len(path) > 32 {
		t.Errorf("cycle guard failed, path length = %d", len(path))
	}
}

// ---------------------------------------------------------------------------
// Effective profile resolution
// ---------------------------------------------------------------------------

func TestResolveEffectiveProfile_BasicInheritance(t *testing.T) {
	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "web", Group: "g1"},
		Options: StoredSSHProfileOptions{
			Host: "web.example.com",
		},
	}
	groups := []ProfileGroup{
		{ID: "g1", Name: "Prod", Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{
				PasswordSecret: new("sec:prod:1"),
				Port:           new(2222),
			},
		}},
	}
	eff, err := ResolveEffectiveProfile(profile, groups, SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if eff.ResolvedOptions.Port != 2222 {
		t.Errorf("port = %d, want 2222 (inherited from group)", eff.ResolvedOptions.Port)
	}
	if eff.ResolvedOptions.PasswordSecret != "sec:prod:1" {
		t.Errorf("passwordSecret = %q, want sec:prod:1", eff.ResolvedOptions.PasswordSecret)
	}
	if src, ok := eff.Source["port"]; !ok || string(src) != "group:g1" {
		t.Errorf("provenance for port = %q, want group:g1", src)
	}
	if src, ok := eff.Source["passwordSecret"]; !ok || string(src) != "group:g1" {
		t.Errorf("provenance for passwordSecret = %q, want group:g1", src)
	}
}

func TestResolveEffectiveProfile_ProfileOverridesGroup(t *testing.T) {
	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "web", Group: "g1"},
		Options: StoredSSHProfileOptions{
			Host: "web.example.com",
			Port: new(2222),
			User: new("bob"),
		},
	}
	groups := []ProfileGroup{
		{ID: "g1", Name: "Prod", Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{
				Port: new(2223),
				User: new("alice"),
			},
		}},
	}
	eff, err := ResolveEffectiveProfile(profile, groups, SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if eff.ResolvedOptions.Port != 2222 {
		t.Errorf("port = %d, want 2222 (profile overrides group)", eff.ResolvedOptions.Port)
	}
	if eff.ResolvedOptions.User != "bob" {
		t.Errorf("user = %q, want bob (profile overrides group)", eff.ResolvedOptions.User)
	}
	if src := eff.Source["port"]; string(src) != "profile" {
		t.Errorf("provenance for port = %q, want profile", src)
	}
}

func TestResolveEffectiveProfile_InheritedTrueOverriddenToFalse(t *testing.T) {
	// Group has agentForward = true; profile explicitly overrides to false via
	// the sparse type (the stored SSHProfileOptions cannot carry explicit false,
	// but the sparse type can distinguish nil-from-false).
	groups := []ProfileGroup{
		{ID: "g1", Name: "Prod", Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{
				AgentForward: new(true),
			},
		}},
	}
	// Construct a sparse override for the profile layer with explicit false.
	af := false
	profileSparse := SparseSSHOptions{AgentForward: &af}

	// Manual merge: start with hardcoded, apply group, then sparse profile.
	acc := hardcodedDefaults()
	source := map[string]FieldSource{"port": FieldSourceDefault, "user": FieldSourceDefault, "behaviorOnSessionEnd": FieldSourceDefault}
	applySparseLayer(&acc, &source, groups[0].Defaults.SparseSSHOptions, fieldSourceForGroup("g1"))
	applySparseLayer(&acc, &source, profileSparse, FieldSourceProfile)

	if acc.AgentForward == nil {
		t.Fatal("AgentForward should be set")
	}
	if *acc.AgentForward {
		t.Error("AgentForward = true, want false (profile override)")
	}
	if src := source["agentForward"]; string(src) != "profile" {
		t.Errorf("provenance for agentForward = %q, want profile", src)
	}
}

func TestResolveEffectiveProfile_InheritVsExplicitZero(t *testing.T) {
	// Group global has port = 2222. Profile has no port set.
	// Effective port should be 2222 from group.
	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "web", Group: "g1"},
		Options: StoredSSHProfileOptions{
			Host: "web.example.com",
		},
	}
	groups := []ProfileGroup{
		{ID: "g1", Name: "Prod", Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{
				Port: new(2222),
			},
		}},
	}
	eff, err := ResolveEffectiveProfile(profile, groups, SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if eff.ResolvedOptions.Port != 2222 {
		t.Errorf("port = %d, want 2222 (inherited)", eff.ResolvedOptions.Port)
	}
	if src := eff.Source["port"]; string(src) != "group:g1" {
		t.Errorf("provenance = %q, want group:g1", src)
	}
}

func TestResolveEffectiveProfile_GroupChainPrecedence(t *testing.T) {
	// Parent group sets port=2222, child group sets port=3333.
	// Profile inherits from child (nearest ancestor wins).
	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "web", Group: "g2"},
		Options: StoredSSHProfileOptions{
			Host: "web.example.com",
		},
	}
	groups := []ProfileGroup{
		{ID: "g1", Name: "Prod", Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{
				Port: new(2222),
			},
		}},
		{ID: "g2", Name: "Staging", ParentGroupID: "g1", Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{
				Port: new(3333),
			},
		}},
	}
	eff, err := ResolveEffectiveProfile(profile, groups, SparseSSHOptions{})
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if eff.ResolvedOptions.Port != 3333 {
		t.Errorf("port = %d, want 3333 (nearest group wins)", eff.ResolvedOptions.Port)
	}
	if src := eff.Source["port"]; string(src) != "group:g2" {
		t.Errorf("provenance = %q, want group:g2", src)
	}
}

func TestResolveEffectiveProfile_GlobalDefaults(t *testing.T) {
	profile := SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{
			Host: "web.example.com",
		},
	}
	global := SparseSSHOptions{
		PasswordSecret: new("sec:global:1"),
		Port:           new(2222),
	}
	eff, err := ResolveEffectiveProfile(profile, nil, global)
	if err != nil {
		t.Fatalf("ResolveEffectiveProfile: %v", err)
	}
	if eff.ResolvedOptions.Port != 2222 {
		t.Errorf("port = %d, want 2222 (global)", eff.ResolvedOptions.Port)
	}
	if src := eff.Source["port"]; string(src) != "global" {
		t.Errorf("provenance = %q, want global", src)
	}
}

func TestResolveEffectiveProfile_HostRequired(t *testing.T) {
	profile := SSHProfile{
		Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{},
	}
	_, err := ResolveEffectiveProfile(profile, nil, SparseSSHOptions{})
	if err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestResolveEffectiveProfile_WhitespaceOnlyHostRejected(t *testing.T) {
	profile := SSHProfile{
		Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{Host: "  "},
	}
	_, err := ResolveEffectiveProfile(profile, nil, SparseSSHOptions{})
	if err == nil {
		t.Fatal("expected error for whitespace-only host")
	}
}

// ---------------------------------------------------------------------------
// Group graph validation
// ---------------------------------------------------------------------------

func TestValidateGroupTree_MissingParent(t *testing.T) {
	groups := []ProfileGroup{
		{ID: "g1", ParentGroupID: "nonexistent"},
	}
	err := ValidateGroupTree(groups)
	if err == nil {
		t.Fatal("expected error for missing parent")
	}
}

func TestValidateGroupTree_Cycle(t *testing.T) {
	groups := []ProfileGroup{
		{ID: "g1", ParentGroupID: "g2"},
		{ID: "g2", ParentGroupID: "g1"},
	}
	err := ValidateGroupTree(groups)
	if err == nil {
		t.Fatal("expected error for cycle")
	}
}

func TestValidateGroupTree_DepthExceeded(t *testing.T) {
	groups := make([]ProfileGroup, maxGroupDepth+1)
	for i := range maxGroupDepth + 1 {
		groups[i] = ProfileGroup{
			ID:   fmt.Sprintf("g%d", i),
			Name: fmt.Sprintf("Level%d", i),
		}
		if i > 0 {
			groups[i].ParentGroupID = fmt.Sprintf("g%d", i-1)
		}
	}
	err := ValidateGroupTree(groups)
	if err == nil {
		t.Fatal("expected error for depth > 32")
	}
}

// ---------------------------------------------------------------------------
// DecodeDefaults (legacy map[string]any)
// ---------------------------------------------------------------------------

func TestDecodeDefaults_UnknownKeyRecorded(t *testing.T) {
	m := map[string]any{
		"passwordSecret": "sec:prod:1",
		"unknownField":   "value",
	}
	d, err := DecodeDefaults(m)
	if err != nil {
		t.Fatalf("DecodeDefaults should succeed with unknown keys: %v", err)
	}
	if keys := d.UnknownKeys(); len(keys) != 1 || keys[0] != "unknownField" {
		t.Fatalf("UnknownKeys = %v, want [unknownField]", keys)
	}
	if err := d.Validate(); err == nil {
		t.Fatal("Validate should error for unknown keys")
	}
}

func TestDecodeDefaults_LegacyMapDecodes(t *testing.T) {
	m := map[string]any{
		"passwordSecret": "sec:prod:1",
		"port":           2222.0,
		"user":           "bob",
	}
	d, err := DecodeDefaults(m)
	if err != nil {
		t.Fatalf("DecodeDefaults: %v", err)
	}
	s := d.SparseSSHOptions
	if s.PasswordSecret == nil || *s.PasswordSecret != "sec:prod:1" {
		t.Errorf("passwordSecret = %v, want sec:prod:1", s.PasswordSecret)
	}
	if s.Port == nil || *s.Port != 2222 {
		t.Errorf("port = %v, want 2222", s.Port)
	}
	if s.User == nil || *s.User != "bob" {
		t.Errorf("user = %v, want bob", s.User)
	}
}

func TestDecodeDefaults_UnknownKeysListed(t *testing.T) {
	m := map[string]any{
		"host":            "should-not-fail-now",
		"canBeJumpServer": true,
	}
	d, err := DecodeDefaults(m)
	if err != nil {
		t.Fatalf("DecodeDefaults should succeed with unknown keys: %v", err)
	}
	keys := d.UnknownKeys()
	if len(keys) != 2 {
		t.Fatalf("UnknownKeys = %v, want 2 keys", keys)
	}
	if err := d.Validate(); err == nil {
		t.Fatal("Validate should error for unknown keys")
	}
}

// ---------------------------------------------------------------------------
// ProfileDefaults custom JSON — unknown keys recorded, not rejected
// ---------------------------------------------------------------------------

func TestProfileDefaultsUnmarshal_UnknownKeyRecorded(t *testing.T) {
	data := []byte(`{"passwordSecret":"sec:x","host":"forbidden"}`)
	var d ProfileDefaults
	if err := d.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON should not error with unknown keys: %v", err)
	}
	if keys := d.UnknownKeys(); len(keys) != 1 || keys[0] != "host" {
		t.Fatalf("UnknownKeys = %v, want [host]", keys)
	}
	if err := d.Validate(); err == nil {
		t.Fatal("Validate should error for unknown keys")
	}
}

func TestProfileDefaultsUnmarshal_ValidKeys(t *testing.T) {
	data := []byte(`{"passwordSecret":"sec:x","port":2222,"agentForward":true}`)
	var d ProfileDefaults
	if err := d.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	s := d.SparseSSHOptions
	if s.PasswordSecret == nil || *s.PasswordSecret != "sec:x" {
		t.Errorf("passwordSecret = %v", s.PasswordSecret)
	}
	if s.Port == nil || *s.Port != 2222 {
		t.Errorf("port = %v", s.Port)
	}
	if s.AgentForward == nil || !*s.AgentForward {
		t.Errorf("agentForward = %v, want true", s.AgentForward)
	}
}

// ---------------------------------------------------------------------------
// ResolveGroupChain
// ---------------------------------------------------------------------------

func TestResolveGroupChain_Simple(t *testing.T) {
	groups := []ProfileGroup{
		{ID: "g1", Name: "Root"},
		{ID: "g2", Name: "Child", ParentGroupID: "g1"},
		{ID: "g3", Name: "Grandchild", ParentGroupID: "g2"},
	}
	chain, err := ResolveGroupChain(groups, "g3")
	if err != nil {
		t.Fatalf("ResolveGroupChain: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain length = %d, want 2", len(chain))
	}
	// Returns nearest ancestor first, then parent.
	if chain[0].ID != "g2" {
		t.Errorf("chain[0] = %s, want g2", chain[0].ID)
	}
	if chain[1].ID != "g1" {
		t.Errorf("chain[1] = %s, want g1", chain[1].ID)
	}
}

func TestResolveGroupChain_CycleError(t *testing.T) {
	groups := []ProfileGroup{
		{ID: "g1", ParentGroupID: "g2"},
		{ID: "g2", ParentGroupID: "g1"},
	}
	_, err := ResolveGroupChain(groups, "g1")
	if err == nil {
		t.Fatal("expected error for cycle")
	}
}

// ---------------------------------------------------------------------------
// Existing JSON store tests
// ---------------------------------------------------------------------------

func TestJSONStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	store := NewJSONStore(path)

	prof := SSHProfile{
		Base: Base{
			ID:    NewProfileID("ssh", "roundtrip"),
			Type:  "ssh",
			Name:  "roundtrip",
			Group: "g1",
		},
		Options: StoredSSHProfileOptions{
			Host: "host.example.com",
			Port: new(2222),
			User: new("bob"),
			Auth: new(AuthPublicKey),
		},
	}
	grp := ProfileGroup{ID: "g1", Name: "Prod"}

	if err := store.CreateProfile(prof); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := store.CreateGroup(grp); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}

	store2 := NewJSONStore(path)
	loaded, err := store2.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("loaded %d profiles, want 1", len(loaded))
	}
	if loaded[0].Name != "roundtrip" {
		t.Errorf("loaded name = %q", loaded[0].Name)
	}
	groups, err := store2.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].ID != "g1" {
		t.Fatalf("loaded groups = %+v", groups)
	}
}

func TestJSONStoreDeleteProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	store := NewJSONStore(path)

	prof := SSHProfile{
		Base:    Base{ID: "ssh:custom:del:0001", Type: "ssh", Name: "del"},
		Options: StoredSSHProfileOptions{Host: "h"},
	}
	_ = store.CreateProfile(prof)

	if err := store.DeleteProfile(prof.ID); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	loaded, _ := store.LoadProfiles()
	if len(loaded) != 0 {
		t.Errorf("after delete, %d profiles remain", len(loaded))
	}
}

func TestJSONStoreFileNotExists(t *testing.T) {
	dir := t.TempDir()
	store := NewJSONStore(filepath.Join(dir, "missing.json"))
	profs, err := store.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles on missing file should not error: %v", err)
	}
	if len(profs) != 0 {
		t.Errorf("expected 0 profiles from missing file, got %d", len(profs))
	}
}

func TestJSONStoreAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	store := NewJSONStore(path)

	prof := SSHProfile{Base: Base{ID: "ssh:custom:atom:0001", Type: "ssh", Name: "atom"}}
	_ = store.CreateProfile(prof)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "profiles.json" {
			t.Errorf("unexpected file/dir leftover: %s", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------

func TestLoadGroupsThroughUnknownDefaultKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.json")

	// A document with a group whose defaults contain an unknown key.
	doc := `{
		"groups": [
			{
				"id": "g1",
				"name": "Prod",
				"defaults": {
					"port": 2222,
					"someOldKey": "still-here",
					"unknownField": 42
				}
			}
		],
		"profiles": [
			{
				"id": "ssh:custom:web:0001",
				"type": "ssh",
				"name": "web",
				"group": "g1",
				"options": { "host": "web.example.com" }
			}
		]
	}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store := NewJSONStore(path)

	// LoadGroups succeeds and returns the group.
	groups, err := store.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups with unknown default keys should succeed: %v", err)
	}
	if len(groups) != 1 || groups[0].ID != "g1" {
		t.Fatalf("LoadGroups = %+v, want 1 group (g1)", groups)
	}
	if groups[0].Defaults == nil {
		t.Fatal("group defaults should not be nil")
	}
	keys := groups[0].Defaults.UnknownKeys()
	if len(keys) != 2 {
		t.Fatalf("UnknownKeys = %v, want 2 keys", keys)
	}
	// Verify at least one known key survived.
	opts := groups[0].Defaults.SparseSSHOptions
	if opts.Port == nil || *opts.Port != 2222 {
		t.Errorf("port = %v, want 2222", opts.Port)
	}

	// LoadProfiles succeeds and returns the profile.
	profs, err := store.LoadProfiles()
	if err != nil {
		t.Fatalf("LoadProfiles with unknown default keys should succeed: %v", err)
	}
	if len(profs) != 1 || profs[0].Name != "web" {
		t.Fatalf("LoadProfiles = %+v, want 1 profile (web)", profs)
	}
}

func TestResolveThroughUnknownDefaultKeysFails(t *testing.T) {
	// A group with unknown keys must still load but resolving a profile
	// through that group must fail.
	groups := []ProfileGroup{
		{
			ID:   "g1",
			Name: "Prod",
			Defaults: func() *ProfileDefaults {
				d := ProfileDefaults{}
				_ = d.UnmarshalJSON([]byte(`{"port":2222,"someOldKey":"still-here"}`))
				return &d
			}(),
		},
	}
	profile := SSHProfile{
		Base: Base{ID: "ssh:custom:web:0001", Type: "ssh", Name: "web", Group: "g1"},
		Options: StoredSSHProfileOptions{
			Host: "web.example.com",
		},
	}

	_, err := ResolveEffectiveProfile(profile, groups, SparseSSHOptions{})
	if err == nil {
		t.Fatal("ResolveEffectiveProfile should fail when a group has unknown keys")
	}
	if !strings.Contains(err.Error(), "g1") {
		t.Errorf("error should name the group id, got: %v", err)
	}
	if !strings.Contains(err.Error(), "someOldKey") {
		t.Errorf("error should name the unknown key, got: %v", err)
	}
}

func TestSaveGroupRejectsUnknownDefaultKeys(t *testing.T) {
	// A group whose defaults carry unknown keys must be rejected by
	// CreateGroup and UpdateGroup.
	store := NewJSONStore(filepath.Join(t.TempDir(), "p.json"))

	// Create a group with unknown keys (simulate loading from a legacy doc).
	badDefaults := &ProfileDefaults{}
	if err := badDefaults.UnmarshalJSON([]byte(`{"port":2222,"someOldKey":"still-here"}`)); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	g := ProfileGroup{
		ID:       "g1",
		Name:     "Prod",
		Defaults: badDefaults,
	}

	err := store.CreateGroup(g)
	if err == nil {
		t.Fatal("CreateGroup should reject a group with unknown default keys")
	}
	if !strings.Contains(err.Error(), "g1") {
		t.Errorf("error should name group id, got: %v", err)
	}
	if !strings.Contains(err.Error(), "someOldKey") {
		t.Errorf("error should name the unknown key, got: %v", err)
	}

	// Now create a clean group, then try to update it with bad defaults.
	clean := ProfileGroup{ID: "g2", Name: "Staging"}
	if cleanErr := store.CreateGroup(clean); cleanErr != nil {
		t.Fatalf("CreateGroup(clean): %v", cleanErr)
	}

	err = store.UpdateGroup(ProfileGroup{
		ID:       "g2",
		Name:     "Staging",
		Defaults: badDefaults,
	})
	if err == nil {
		t.Fatal("UpdateGroup should reject a group with unknown default keys")
	}
	if !strings.Contains(err.Error(), "g2") {
		t.Errorf("error should name group id, got: %v", err)
	}
	if !strings.Contains(err.Error(), "someOldKey") {
		t.Errorf("error should name the unknown key, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// nocx-jb20.6: "auth" must be an allowed group-defaults key
// ---------------------------------------------------------------------------

func TestProfileDefaultsAuthAllowedAndRoundTrips(t *testing.T) {
	// "auth" is a real inheritable SparseSSHOptions field (json:"auth,omitempty")
	// that applySparseLayer merges. allowedFields must list it, or
	// UnmarshalJSON records it as unknown and Validate rejects the group.
	data := []byte(`{"auth":"publicKey","port":2222}`)
	var d ProfileDefaults
	if err := d.UnmarshalJSON(data); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if keys := d.UnknownKeys(); len(keys) != 0 {
		t.Fatalf("auth should be a known key, got unknown keys: %v", keys)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate should accept auth, got: %v", err)
	}
	if d.Auth == nil || *d.Auth != AuthPublicKey {
		t.Fatalf("Auth = %v, want publicKey", d.Auth)
	}
}

func TestGroupAuthDefaultRoundTripsThroughCreateUpdateApply(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "p.json"))

	auth := AuthPublicKey
	g := ProfileGroup{
		ID:   "g1",
		Name: "Prod",
		Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{Auth: &auth},
		},
	}

	if err := store.CreateGroup(g); err != nil {
		t.Fatalf("CreateGroup with auth default: %v", err)
	}

	// Update with a different auth value.
	password := AuthPassword
	g.Defaults.Auth = &password
	if err := store.UpdateGroup(g); err != nil {
		t.Fatalf("UpdateGroup with auth default: %v", err)
	}

	// Reload from disk and confirm the value survived the round trip.
	loaded, err := store.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}
	if len(loaded) != 1 || loaded[0].ID != "g1" {
		t.Fatalf("LoadGroups = %+v, want 1 group (g1)", loaded)
	}
	if loaded[0].Defaults == nil || loaded[0].Defaults.Auth == nil {
		t.Fatal("reloaded group auth default is nil")
	}
	if *loaded[0].Defaults.Auth != AuthPassword {
		t.Errorf("reloaded auth = %q, want %q", *loaded[0].Defaults.Auth, AuthPassword)
	}

	// ApplyGroups must also accept the auth default.
	if err := store.ApplyGroups([]ProfileGroup{g}); err != nil {
		t.Fatalf("ApplyGroups with auth default: %v", err)
	}
}

func TestAllowedDefaultKeysMatchesSparseSSHOptionsTags(t *testing.T) {
	// A drift guard: every json field tag on SparseSSHOptions must appear in
	// allowedFields, so the next field added to the sparse struct cannot fall
	// out of the allow-list the way "auth" did.
	var s SparseSSHOptions
	rt := reflect.TypeOf(s)
	for i := range rt.NumField() {
		f := rt.Field(i)
		tag := f.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		if !allowedFields[name] {
			t.Errorf("SparseSSHOptions field %q (json:%q) is missing from allowedFields", f.Name, name)
		}
	}
}
