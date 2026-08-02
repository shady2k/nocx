package profile

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *JSONStore {
	t.Helper()
	return NewJSONStore(filepath.Join(t.TempDir(), "p.json"))
}

// ClearSecretRefs is the metadata-first half of deleting a secret
// (ADR-0011 §4): every profile binding to the secret — password, key and
// key-passphrase — is removed in ONE write, so nothing keeps pointing at a
// store entry that is about to be gone. Group-default bindings are cleared
// too: they are metadata that points at the same store entry.
func TestClearSecretRefs_ClearsEveryBindingInOneWrite(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProfile(SSHProfile{
		Base: Base{ID: "p1", Type: "ssh", Name: "one"},
		Options: StoredSSHProfileOptions{
			Host:                "h1",
			PasswordSecret:      "sec:password",
			KeySecret:           "sec:key",
			KeyPassphraseSecret: "sec:passphrase",
		},
	}); err != nil {
		t.Fatalf("create p1: %v", err)
	}
	if err := s.CreateProfile(SSHProfile{
		Base:    Base{ID: "p2", Type: "ssh", Name: "two"},
		Options: StoredSSHProfileOptions{Host: "h2", PasswordSecret: "sec:other"},
	}); err != nil {
		t.Fatalf("create p2: %v", err)
	}
	if err := s.CreateGroup(ProfileGroup{
		ID: "g1", Name: "Prod",
		Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{
				PasswordSecret: Ptr("sec:password"),
			},
		},
	}); err != nil {
		t.Fatalf("create g1: %v", err)
	}

	if err := s.ClearSecretRefs("sec:password"); err != nil {
		t.Fatalf("ClearSecretRefs: %v", err)
	}

	profiles, err := s.LoadProfiles()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var p1, p2 SSHProfile
	for _, p := range profiles {
		switch p.ID {
		case "p1":
			p1 = p
		case "p2":
			p2 = p
		}
	}
	if p1.Options.PasswordSecret != "" {
		t.Errorf("p1 password ref = %q, want cleared", p1.Options.PasswordSecret)
	}
	if p1.Options.KeySecret != "sec:key" {
		t.Errorf("p1 key ref changed: %q", p1.Options.KeySecret)
	}
	if p1.Options.KeyPassphraseSecret != "sec:passphrase" {
		t.Errorf("p1 passphrase ref changed: %q", p1.Options.KeyPassphraseSecret)
	}
	if p2.Options.PasswordSecret != "sec:other" {
		t.Errorf("p2 password ref changed: %q", p2.Options.PasswordSecret)
	}

	groups, err := s.LoadGroups()
	if err != nil {
		t.Fatalf("load groups: %v", err)
	}
	if g := groups[0].Defaults.SparseSSHOptions.PasswordSecret; g != nil {
		t.Errorf("group default password ref = %v, want cleared", *g)
	}
}

// Deleting a secret nothing references is a no-op write: the store stays
// intact and the call succeeds.
func TestClearSecretRefs_NoReferenceIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreateProfile(SSHProfile{
		Base:    Base{ID: "p1", Type: "ssh", Name: "one"},
		Options: StoredSSHProfileOptions{Host: "h1"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.ClearSecretRefs("sec:never-stored"); err != nil {
		t.Fatalf("ClearSecretRefs(absent): %v", err)
	}
	profiles, err := s.LoadProfiles()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(profiles))
	}
}

func TestApplyGroups_MultiGroupUpdate(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateGroup(ProfileGroup{ID: "g1", Name: "Root"}); err != nil {
		t.Fatalf("create g1: %v", err)
	}
	if err := s.CreateGroup(ProfileGroup{ID: "g2", Name: "Child", ParentGroupID: "g1"}); err != nil {
		t.Fatalf("create g2: %v", err)
	}
	if err := s.CreateGroup(ProfileGroup{ID: "g3", Name: "Grandchild", ParentGroupID: "g2"}); err != nil {
		t.Fatalf("create g3: %v", err)
	}

	// Apply two changes atomically: reparent g2 to root, reparent g3 to g1.
	// The old handleGroupApply would need two sequential calls; the second
	// call's validation would not see the first call's change to g2's
	// ParentGroupID, so the tree state viewed by the second validation would
	// differ from what is written.
	err := s.ApplyGroups([]ProfileGroup{
		{ID: "g2", Name: "Child", ParentGroupID: ""},
		{ID: "g3", Name: "Grandchild", ParentGroupID: "g1"},
	})
	if err != nil {
		t.Fatalf("ApplyGroups: %v", err)
	}

	groups, err := s.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}

	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}

	// Assert by ID, not position.
	for _, g := range groups {
		switch g.ID {
		case "g1":
			if g.ParentGroupID != "" || g.Name != "Root" {
				t.Errorf("g1 = %+v, want Root with no parent", g)
			}
		case "g2":
			if g.ParentGroupID != "" {
				t.Errorf("g2 ParentGroupID = %q, want empty (reparented to root)", g.ParentGroupID)
			}
		case "g3":
			if g.ParentGroupID != "g1" {
				t.Errorf("g3 ParentGroupID = %q, want g1 (reparented under g1)", g.ParentGroupID)
			}
		}
	}
}

func TestApplyGroups_RejectsCycle(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateGroup(ProfileGroup{ID: "g1", Name: "Root"}); err != nil {
		t.Fatalf("create g1: %v", err)
	}
	if err := s.CreateGroup(ProfileGroup{ID: "g2", Name: "Child", ParentGroupID: "g1"}); err != nil {
		t.Fatalf("create g2: %v", err)
	}

	// Apply two changes that together form a cycle: g1 -> g2, g2 -> g1.
	// With the old sequential pattern (LoadGroups → validate → UpdateGroup),
	// g1 would be updated first, then g2 would fail validation — leaving the
	// store in an inconsistent state (g1 reparented, g2 unchanged).
	// ApplyGroups must reject both under one lock.
	err := s.ApplyGroups([]ProfileGroup{
		{ID: "g1", Name: "Root", ParentGroupID: "g2"},
		{ID: "g2", Name: "Child", ParentGroupID: "g1"},
	})
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}

	// Store must be unchanged: g1 root, g2 child of g1. Assert by ID, not
	// position — LoadGroups makes no ordering guarantee.
	groups, err := s.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	for _, g := range groups {
		switch g.ID {
		case "g1":
			if g.ParentGroupID != "" {
				t.Errorf("g1 ParentGroupID = %q after rejected cycle, want empty", g.ParentGroupID)
			}
		case "g2":
			if g.ParentGroupID != "g1" {
				t.Errorf("g2 ParentGroupID = %q after rejected cycle, want g1", g.ParentGroupID)
			}
		}
	}
}

func TestApplyGroups_RejectsUnknownGroup(t *testing.T) {
	s := newTestStore(t)

	if err := s.CreateGroup(ProfileGroup{ID: "g1", Name: "Root"}); err != nil {
		t.Fatalf("create g1: %v", err)
	}

	err := s.ApplyGroups([]ProfileGroup{
		{ID: "g1", Name: "Still Root"},
		{ID: "g2", Name: "Phantom"},
	})
	if !errors.Is(err, ErrGroupNotFound) {
		t.Fatalf("err = %v, want ErrGroupNotFound", err)
	}

	// g1 must be unchanged.
	groups, err := s.LoadGroups()
	if err != nil {
		t.Fatalf("LoadGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "Root" {
		t.Fatalf("g1 mutated after rejected unknown group: %+v", groups[0])
	}
}

func TestApplyGroups_EmptySlice(t *testing.T) {
	s := newTestStore(t)
	if err := s.ApplyGroups(nil); err != nil {
		t.Fatalf("nil slice: %v", err)
	}
	if err := s.ApplyGroups([]ProfileGroup{}); err != nil {
		t.Fatalf("empty slice: %v", err)
	}
}
