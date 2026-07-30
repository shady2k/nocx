package profile

import (
	"errors"
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

// nocx-wd2m. Host binding is mandatory (nocx-mon), and the rule has to bite at
// save time. Enforcing it only at connect time — which is where checkBinding
// lives — means the user stores a secret, walks away, and meets the refusal
// later as a broken connection instead of a rejected form.
//
// The store is the enforcement point on purpose: it is the one path every
// writer goes through, so a future caller cannot route around the rule the way
// it could around a check sitting in the transport handler.
func TestSaveCredentialRejectsMissingHost(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	unbound := Credential{
		ID:       NewCredentialID("unbound"),
		Name:     "unbound",
		Username: "bob",
		Auth:     AuthPassword,
	}
	if err := store.SaveCredential(unbound); err == nil {
		t.Fatal("SaveCredential accepted a credential with no host; an unbound credential must not be storable")
	} else if !errors.Is(err, ErrCredentialHostRequired) {
		t.Fatalf("want ErrCredentialHostRequired, got %T: %v", err, err)
	}

	creds, err := store.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("a rejected credential must not be persisted; store holds %d", len(creds))
	}
}

func TestSaveCredentialRejectsWhitespaceOnlyHost(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	// " " is not a host. Accepting it would satisfy the letter of the rule and
	// none of its purpose — checkBinding compares against a resolved hostname
	// and would never match, so the credential would be storable and useless.
	c := Credential{
		ID:       NewCredentialID("spacey"),
		Name:     "spacey",
		Username: "bob",
		Auth:     AuthPassword,
		Host:     "   ",
	}
	if err := store.SaveCredential(c); !errors.Is(err, ErrCredentialHostRequired) {
		t.Fatalf("want ErrCredentialHostRequired for a whitespace host, got %v", err)
	}
}

func TestSaveCredentialAcceptsBoundCredential(t *testing.T) {
	store := NewJSONStore(filepath.Join(t.TempDir(), "profiles.json"))

	c := Credential{
		ID:       NewCredentialID("bound"),
		Name:     "bound",
		Username: "bob",
		Auth:     AuthPassword,
		Host:     "prod.example.com",
	}
	if err := store.SaveCredential(c); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}
	creds, err := store.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds) != 1 || creds[0].Host != "prod.example.com" {
		t.Fatalf("want the bound credential stored, got %+v", creds)
	}
}

// ── Connection snapshot (ADR-0015) ─────────────────────────────────────

func TestConnectionSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewJSONStore(filepath.Join(dir, "profiles.json"))

	prof := SSHProfile{
		Base:    Base{ID: "ssh:custom:snap:0001", Type: "ssh", Name: "snap"},
		Options: SSHProfileOptions{Host: "h.example.com", Port: 22, User: "u"},
	}
	grp := ProfileGroup{ID: "g1", Name: "G1"}

	if err := store.SaveProfile(prof.ToPartial()); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := store.SaveGroup(grp); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}

	snap, err := store.LoadConnectionSnapshot()
	if err != nil {
		t.Fatalf("LoadConnectionSnapshot: %v", err)
	}
	if len(snap.Profiles) != 1 || snap.Profiles[0].Name != "snap" {
		t.Errorf("profiles = %+v", snap.Profiles)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].ID != "g1" {
		t.Errorf("groups = %+v", snap.Groups)
	}
}

func TestReplaceConnectionSnapshotPreservesCredentials(t *testing.T) {
	dir := t.TempDir()
	store := NewJSONStore(filepath.Join(dir, "profiles.json"))

	cred := Credential{
		ID:       NewCredentialID("keep"),
		Name:     "keep",
		Username: "alice",
		Auth:     AuthPassword,
		Host:     "keep.example.com",
	}
	if err := store.SaveCredential(cred); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}
	prof := SSHProfile{
		Base:    Base{ID: "ssh:custom:rep:0001", Type: "ssh", Name: "rep"},
		Options: SSHProfileOptions{Host: "h.example.com", CredentialID: cred.ID},
	}
	if err := store.SaveProfile(prof.ToPartial()); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	newSnap := ConnectionSnapshot{
		Profiles: []SSHProfile{
			{Base: Base{ID: "ssh:custom:rep2:0001", Type: "ssh", Name: "rep2"}, Options: SSHProfileOptions{Host: "h2.example.com"}},
		},
		Groups: []ProfileGroup{{ID: "g2", Name: "G2"}},
	}
	if err := store.ReplaceConnectionSnapshot(newSnap); err != nil {
		t.Fatalf("ReplaceConnectionSnapshot: %v", err)
	}

	creds, err := store.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(creds) != 1 || creds[0].ID != cred.ID {
		t.Errorf("credentials should be preserved, got %+v", creds)
	}

	profs, _ := store.LoadProfiles()
	if len(profs) != 1 || profs[0].Name != "rep2" {
		t.Errorf("profiles should be replaced, got %+v", profs)
	}
	grps, _ := store.LoadGroups()
	if len(grps) != 1 || grps[0].ID != "g2" {
		t.Errorf("groups should be replaced, got %+v", grps)
	}
}
