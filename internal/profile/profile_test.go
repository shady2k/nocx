package profile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfileDefaults(t *testing.T) {
	p := SSHProfile{
		Base: Base{
			ID:   "ssh:custom:test:0001",
			Type: "ssh",
			Name: "test-host",
		},
		Options: SSHProfileOptions{
			Host: "example.com",
			User: "alice",
		},
	}

	applyDefaults(&p)

	if p.Options.Port != 22 {
		t.Errorf("default port = %d, want 22", p.Options.Port)
	}
	if p.Options.Auth != AuthAuto {
		t.Errorf("default auth = %v, want %v", p.Options.Auth, AuthAuto)
	}
	if p.BehaviorOnSessionEnd != BehaviorAuto {
		t.Errorf("default behavior = %v, want %v", p.BehaviorOnSessionEnd, BehaviorAuto)
	}
}

func TestProfileIDNamespace(t *testing.T) {
	id := NewProfileID("ssh", "my-host")
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

func TestDefaultsMergeOrder(t *testing.T) {
	hardcoded := SSHProfileOptions{Port: 22, Auth: AuthAuto}
	providerDefaults := SSHProfileOptions{User: "root", KeepaliveInterval: 5000}
	globalDefaults := SSHProfileOptions{User: "globaluser", Auth: AuthPassword}
	groupDefaults := SSHProfileOptions{CredentialID: "cred:prod-key:123"}

	profile := SSHProfileOptions{Host: "example.com"}

	merged := mergeSSHOptions(hardcoded, providerDefaults, globalDefaults, groupDefaults, profile)

	if merged.Port != 22 {
		t.Errorf("port from hardcoded = %d, want 22", merged.Port)
	}
	if merged.User != "globaluser" {
		t.Errorf("user should prefer global over provider: got %q, want globaluser", merged.User)
	}
	if merged.Auth != AuthPassword {
		t.Errorf("auth should prefer global over provider default: got %v, want %v", merged.Auth, AuthPassword)
	}
	if merged.CredentialID != "cred:prod-key:123" {
		t.Errorf("group defaults credential ID not applied: %v", merged.CredentialID)
	}
	if merged.Host != "example.com" {
		t.Errorf("profile host should win: got %q, want example.com", merged.Host)
	}
}

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
		Options: SSHProfileOptions{
			Host: "host.example.com",
			Port: 2222,
			User: "bob",
			Auth: AuthPublicKey,
		},
	}
	grp := ProfileGroup{ID: "g1", Name: "Prod"}

	if err := store.SaveProfile(prof.ToPartial()); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := store.SaveGroup(grp); err != nil {
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
		Options: SSHProfileOptions{Host: "h"},
	}
	_ = store.SaveProfile(prof.ToPartial())

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
	_ = store.SaveProfile(prof.ToPartial())

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

func TestSaveCredentialAcceptsEmptyTrustedEndpoints(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	// ADR-0013: empty TrustedEndpoints is valid - means "no remote endpoint authorized"
	c := Credential{
		ID:               NewCredentialID("empty-grants"),
		Name:             "empty-grants",
		Username:         "bob",
		Auth:             AuthPassword,
		TrustedEndpoints: []CredentialTrustedEndpoint{},
	}
	if err := store.SaveCredential(c); err != nil {
		t.Fatalf("SaveCredential with empty TrustedEndpoints: %v", err)
	}
	creds, err := store.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("want 1 credential, got %d", len(creds))
	}
	if len(creds[0].TrustedEndpoints) != 0 {
		t.Fatalf("want empty TrustedEndpoints after reload, got %v", creds[0].TrustedEndpoints)
	}
}

func TestSaveCredentialAcceptsTrustedEndpoints(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	// ADR-0013: TrustedEndpoints replace legacy Host/Port binding
	c := Credential{
		ID:       NewCredentialID("with-grants"),
		Name:     "with-grants",
		Username: "bob",
		Auth:     AuthPassword,
		TrustedEndpoints: []CredentialTrustedEndpoint{
			{ProfileID: "profile:prod", Host: "prod.example.com", Port: 22},
			{ProfileID: "profile:staging", Host: "staging.example.com", Port: 22},
		},
	}
	if err := store.SaveCredential(c); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}
	creds, err := store.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("want 1 credential, got %d", len(creds))
	}
	if len(creds[0].TrustedEndpoints) != 2 {
		t.Fatalf("want 2 TrustedEndpoints, got %d", len(creds[0].TrustedEndpoints))
	}
	if creds[0].TrustedEndpoints[0].Host != "prod.example.com" {
		t.Fatalf("want first endpoint host prod.example.com, got %s", creds[0].TrustedEndpoints[0].Host)
	}
	if creds[0].TrustedEndpoints[1].Host != "staging.example.com" {
		t.Fatalf("want second endpoint host staging.example.com, got %s", creds[0].TrustedEndpoints[1].Host)
	}
}
