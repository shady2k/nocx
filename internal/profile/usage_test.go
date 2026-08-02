package profile

import "testing"

func secPtr(s string) *string { return &s }

// ── Direct reference ───────────────────────────────────────────────────────

func TestSecretUsage_Direct(t *testing.T) {
	usage := ComputeSecretUsage([]SSHProfile{{
		Base: Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{
			Host:           "web.example.com",
			PasswordSecret: "sec:direct:1",
		},
	}}, nil, SparseSSHOptions{})

	if len(usage) != 1 {
		t.Fatalf("usage = %d entries, want 1", len(usage))
	}
	if usage[0].SecretID != "sec:direct:1" {
		t.Errorf("SecretID = %q, want sec:direct:1", usage[0].SecretID)
	}
	if len(usage[0].Profiles) != 1 {
		t.Fatalf("profiles = %d, want 1", len(usage[0].Profiles))
	}
	ref := usage[0].Profiles[0]
	if ref.ProfileID != "p1" || ref.Source != "profile" {
		t.Errorf("ref = %+v, want p1 via profile", ref)
	}
}

// ── Group inheritance ──────────────────────────────────────────────────────

func TestSecretUsage_GroupInherited(t *testing.T) {
	groups := []ProfileGroup{{
		ID:   "g1",
		Name: "Prod",
		Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{
				PasswordSecret: secPtr("sec:group:1"),
			},
		},
	}}
	usage := ComputeSecretUsage([]SSHProfile{{
		Base:    Base{ID: "p1", Type: "ssh", Name: "web", Group: "g1"},
		Options: StoredSSHProfileOptions{Host: "web.example.com"},
	}}, groups, SparseSSHOptions{})

	if len(usage) != 1 {
		t.Fatalf("usage = %d entries, want 1", len(usage))
	}
	ref := usage[0].Profiles[0]
	if ref.Source != "group" || ref.GroupID != "g1" || ref.GroupName != "Prod" {
		t.Errorf("ref = %+v, want inherited from group g1", ref)
	}
}

// ── Profile overrides group ────────────────────────────────────────────────

func TestSecretUsage_ProfileOverridesGroup(t *testing.T) {
	groups := []ProfileGroup{{
		ID: "g1", Name: "Prod",
		Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{PasswordSecret: secPtr("sec:group:1")},
		},
	}}
	usage := ComputeSecretUsage([]SSHProfile{{
		Base: Base{ID: "p1", Type: "ssh", Name: "web", Group: "g1"},
		Options: StoredSSHProfileOptions{
			Host:           "web.example.com",
			PasswordSecret: "sec:direct:1",
		},
	}}, groups, SparseSSHOptions{})

	if len(usage) != 1 {
		t.Fatalf("usage = %d entries, want 1", len(usage))
	}
	if usage[0].SecretID != "sec:direct:1" {
		t.Errorf("SecretID = %q, want the profile's own sec:direct:1", usage[0].SecretID)
	}
}

// ── One secret shared by two profiles ──────────────────────────────────────

func TestSecretUsage_SharedSecret(t *testing.T) {
	usage := ComputeSecretUsage([]SSHProfile{
		{
			Base: Base{ID: "p1", Type: "ssh", Name: "web"},
			Options: StoredSSHProfileOptions{
				Host:           "web.example.com",
				PasswordSecret: "sec:shared:1",
			},
		},
		{
			Base: Base{ID: "p2", Type: "ssh", Name: "db"},
			Options: StoredSSHProfileOptions{
				Host:           "db.example.com",
				PasswordSecret: "sec:shared:1",
			},
		},
	}, nil, SparseSSHOptions{})

	if len(usage) != 1 {
		t.Fatalf("usage = %d entries, want 1", len(usage))
	}
	if len(usage[0].Profiles) != 2 {
		t.Fatalf("profiles = %d, want 2", len(usage[0].Profiles))
	}
}

// ── Same secret in two fields of one profile is one use ────────────────────

func TestSecretUsage_SameSecretInTwoFields(t *testing.T) {
	usage := ComputeSecretUsage([]SSHProfile{{
		Base: Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{
			Host:           "web.example.com",
			PasswordSecret: "sec:one:1",
			KeySecret:      "sec:one:1", // same secret bound as password AND key
		},
	}}, nil, SparseSSHOptions{})

	if len(usage) != 1 {
		t.Fatalf("usage = %d entries, want 1", len(usage))
	}
	if len(usage[0].Profiles) != 1 {
		t.Fatalf("profiles = %d, want 1 — one profile using it, not two", len(usage[0].Profiles))
	}
}

// ── Nested groups — nearest ancestor wins ──────────────────────────────────

func TestSecretUsage_NestedGroups(t *testing.T) {
	groups := []ProfileGroup{
		{ID: "g1", Name: "Parent", Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{PasswordSecret: secPtr("sec:parent:1")},
		}},
		{ID: "g2", Name: "Child", ParentGroupID: "g1", Defaults: &ProfileDefaults{
			SparseSSHOptions: SparseSSHOptions{PasswordSecret: secPtr("sec:child:1")},
		}},
		{ID: "g3", Name: "Leaf", ParentGroupID: "g2"}, // no secret — inherits from g2
	}
	usage := ComputeSecretUsage([]SSHProfile{{
		Base:    Base{ID: "p1", Type: "ssh", Name: "leaf", Group: "g3"},
		Options: StoredSSHProfileOptions{Host: "leaf.example.com"},
	}}, groups, SparseSSHOptions{})

	if len(usage) != 1 {
		t.Fatalf("usage = %d entries, want 1", len(usage))
	}
	if usage[0].SecretID != "sec:child:1" {
		t.Errorf("SecretID = %q, want nearest ancestor sec:child:1", usage[0].SecretID)
	}
	if ref := usage[0].Profiles[0]; ref.GroupID != "g2" {
		t.Errorf("group = %q, want g2 (nearest ancestor with a secret)", ref.GroupID)
	}
}

// ── Global defaults ────────────────────────────────────────────────────────

func TestSecretUsage_Global(t *testing.T) {
	global := SparseSSHOptions{PasswordSecret: secPtr("sec:global:1")}
	usage := ComputeSecretUsage([]SSHProfile{{
		Base:    Base{ID: "p1", Type: "ssh", Name: "web"},
		Options: StoredSSHProfileOptions{Host: "web.example.com"},
	}}, nil, global)

	if len(usage) != 1 {
		t.Fatalf("usage = %d entries, want 1", len(usage))
	}
	if ref := usage[0].Profiles[0]; ref.Source != "global" {
		t.Errorf("source = %q, want global", ref.Source)
	}
}
