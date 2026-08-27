package connection

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/vault"
	gossh "golang.org/x/crypto/ssh"
)

// stubProfileStore implements profile.ProfileRepository in memory.
type stubProfileStore struct {
	profiles map[string]profile.SSHProfile
	groups   map[string]profile.ProfileGroup
}

func newStubProfileStore() *stubProfileStore {
	return &stubProfileStore{
		profiles: make(map[string]profile.SSHProfile),
		groups:   make(map[string]profile.ProfileGroup),
	}
}

// --- profile.ProfileRepository ---

func (s *stubProfileStore) LoadProfiles() ([]profile.SSHProfile, error) {
	out := make([]profile.SSHProfile, 0, len(s.profiles))
	for _, p := range s.profiles {
		out = append(out, p)
	}
	return out, nil
}

func (s *stubProfileStore) CreateProfile(p profile.SSHProfile) error {
	if _, ok := s.profiles[p.ID]; ok {
		return profile.ErrProfileExists
	}
	s.profiles[p.ID] = p
	return nil
}

func (s *stubProfileStore) UpdateProfile(p profile.SSHProfile) error {
	if _, ok := s.profiles[p.ID]; !ok {
		return profile.ErrProfileNotFound
	}
	s.profiles[p.ID] = p
	return nil
}

func (s *stubProfileStore) DeleteProfile(id string) error {
	delete(s.profiles, id)
	return nil
}

// SaveProfile is a test-local helper: test fixtures want "put this
// in the store" without worrying about existence.
func (s *stubProfileStore) SaveProfile(p profile.SSHProfile) error {
	s.profiles[p.ID] = p
	return nil
}

// SaveGroup is a test-local helper for the same reason.
func (s *stubProfileStore) SaveGroup(g profile.ProfileGroup) error {
	s.groups[g.ID] = g
	return nil
}

// LoadGroups / CreateGroup / UpdateGroup / DeleteGroup satisfy
// profile.GroupRepository where required.
func (s *stubProfileStore) LoadGroups() ([]profile.ProfileGroup, error) {
	out := make([]profile.ProfileGroup, 0, len(s.groups))
	for _, g := range s.groups {
		out = append(out, g)
	}
	return out, nil
}

func (s *stubProfileStore) CreateGroup(g profile.ProfileGroup) error {
	if _, ok := s.groups[g.ID]; ok {
		return profile.ErrGroupExists
	}
	s.groups[g.ID] = g
	return nil
}

func (s *stubProfileStore) UpdateGroup(g profile.ProfileGroup) error {
	if _, ok := s.groups[g.ID]; !ok {
		return profile.ErrGroupNotFound
	}
	s.groups[g.ID] = g
	return nil
}

func (s *stubProfileStore) DeleteGroup(id string) error {
	delete(s.groups, id)
	return nil
}

// stubSecretStore implements credential.SecretStore in memory.
type stubSecretStore struct {
	secrets map[credential.SecretID]credential.Secret
}

func newStubSecretStore() *stubSecretStore {
	return &stubSecretStore{secrets: make(map[credential.SecretID]credential.Secret)}
}

func (s *stubSecretStore) Create(ctx context.Context, value credential.Secret) (credential.SecretID, error) {
	id, err := vault.MintReferenceForTest(vault.ProviderFile)
	if err != nil {
		return "", err
	}
	s.secrets[id] = value
	return id, nil
}

func (s *stubSecretStore) Get(ctx context.Context, id credential.SecretID) (credential.Secret, error) {
	val, ok := s.secrets[id]
	if !ok {
		return credential.Secret{}, nil
	}
	return val, nil
}

func (s *stubSecretStore) Resolve(ctx context.Context, id credential.SecretID, why credential.Stance) (credential.Secret, error) {
	return credential.NewResolver(s, nil, nil).Resolve(ctx, id, why)
}

func (s *stubSecretStore) Delete(ctx context.Context, id credential.SecretID) error {
	delete(s.secrets, id)
	return nil
}

func (s *stubSecretStore) Exists(ctx context.Context, id credential.SecretID) (bool, error) {
	_, ok := s.secrets[id]
	return ok, nil
}

//nolint:errcheck
func TestResolver_BoundSecretMode(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	pwID, _ := ss.Create(context.Background(), credential.NewSecret("s3cret"))

	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:1", Name: "staging"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "staging.example.com",
			Port:           profile.Ptr(2222),
			User:           profile.Ptr("deploy"),
			Auth:           profile.Ptr(profile.AuthMode("publicKey")),
			KeyPath:        profile.Ptr("/home/user/.ssh/work_rsa"),
			PasswordSecret: string(pwID),
		},
	})

	r := NewResolver(ps, ps, ss)
	host, cfg, err := r.Resolve("profile:1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if host != "staging.example.com" {
		t.Errorf("host = %q, want staging.example.com", host)
	}
	if cfg.Port != 2222 {
		t.Errorf("Port = %d, want 2222", cfg.Port)
	}
	if cfg.User != "deploy" {
		t.Errorf("User = %q, want deploy", cfg.User)
	}
	if cfg.AuthMode != "publicKey" {
		t.Errorf("AuthMode = %q, want publicKey", cfg.AuthMode)
	}
	if cfg.KeyFile != "/home/user/.ssh/work_rsa" {
		t.Errorf("KeyFile = %q, want /home/user/.ssh/work_rsa", cfg.KeyFile)
	}

	if cfg.Secrets == nil {
		t.Fatal("Secrets is nil, want wired secret store")
	}
	if cfg.SecretID != pwID {
		t.Errorf("SecretID = %q, want %q", cfg.SecretID, pwID)
	}
}

//nolint:errcheck
func TestResolver_InlineMode(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:inline", Name: "legacy"},
		Options: profile.StoredSSHProfileOptions{
			Host: "legacy.example.com",
			Port: profile.Ptr(22),
			User: profile.Ptr("admin"),
			Auth: profile.Ptr(profile.AuthMode("password")),
		},
	})

	r := NewResolver(ps, ps, ss)
	host, cfg, err := r.Resolve("profile:inline")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if host != "legacy.example.com" {
		t.Errorf("host = %q, want legacy.example.com", host)
	}
	if cfg.User != "admin" {
		t.Errorf("User = %q, want admin", cfg.User)
	}
	if cfg.AuthMode != "password" {
		t.Errorf("AuthMode = %q, want password", cfg.AuthMode)
	}
	if cfg.Port != 22 {
		t.Errorf("Port = %d, want 22", cfg.Port)
	}
	if cfg.Secrets != nil {
		t.Error("Secrets should be nil in inline mode")
	}
}

func TestResolver_RejectsOptionLikeStoredHostWithoutConfigResolver(t *testing.T) {
	ps := newStubProfileStore()
	if err := ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:option-host", Name: "option-host"},
		Options: profile.StoredSSHProfileOptions{
			Host: "-F/tmp/attacker_config",
			Port: profile.Ptr(22),
		},
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	_, _, err := NewResolver(ps, ps, newStubSecretStore()).Resolve("profile:option-host")
	if err == nil || !strings.Contains(err.Error(), "host must not begin with a dash") {
		t.Fatalf("Resolve error = %v, want option-like host refusal before any oracle or dial", err)
	}
}

//nolint:errcheck
func TestResolver_UnknownProfile(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	r := NewResolver(ps, ps, ss)
	_, _, err := r.Resolve("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

//nolint:errcheck
func TestResolver_JumpHost(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	// Jump profile
	jumpPWID, _ := ss.Create(context.Background(), credential.NewSecret("jump-secret"))
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:jump", Name: "jump"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "jump.example.com",
			Port:           profile.Ptr(22),
			User:           profile.Ptr("jumpuser"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(jumpPWID),
		},
	})

	// Target profile
	tgtPWID, _ := ss.Create(context.Background(), credential.NewSecret("tgt-secret"))
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:tgt", Name: "target"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "target.internal",
			Port:           profile.Ptr(2222),
			User:           profile.Ptr("tgtuser"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(tgtPWID),
			JumpHost:       profile.Ptr("profile:jump"),
		},
	})

	r := NewResolver(ps, ps, ss)
	host, cfg, err := r.Resolve("profile:tgt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if host != "target.internal" {
		t.Errorf("host = %q, want target.internal", host)
	}

	if cfg.Secrets == nil {
		t.Fatal("Target Secrets is nil")
	}
	if cfg.SecretID != tgtPWID {
		t.Errorf("Target SecretID = %q, want %q", cfg.SecretID, tgtPWID)
	}

	if cfg.JumpHost != "jump.example.com" {
		t.Errorf("JumpHost = %q, want jump.example.com", cfg.JumpHost)
	}
	if cfg.JumpPort != 22 {
		t.Errorf("JumpPort = %d, want 22", cfg.JumpPort)
	}
	if cfg.JumpUser != "jumpuser" {
		t.Errorf("JumpUser = %q, want jumpuser", cfg.JumpUser)
	}
	if cfg.JumpSecrets == nil {
		t.Error("JumpSecrets is nil")
	}
	if cfg.JumpSecretID != jumpPWID {
		t.Errorf("JumpSecretID = %q, want %q", cfg.JumpSecretID, jumpPWID)
	}
}

//nolint:errcheck
func TestResolver_JumpHostInlineMode(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	// Jump profile (inline, no credential)
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:jump-inline", Name: "jump"},
		Options: profile.StoredSSHProfileOptions{
			Host: "jump.inline.com",
			Port: profile.Ptr(22),
			User: profile.Ptr("jumper"),
			Auth: profile.Ptr(profile.AuthMode("publicKey")),
		},
	})

	// Target profile
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:tgt2", Name: "target"},
		Options: profile.StoredSSHProfileOptions{
			Host:     "target.inline",
			Port:     profile.Ptr(3333),
			JumpHost: profile.Ptr("profile:jump-inline"),
		},
	})

	r := NewResolver(ps, ps, ss)
	host, cfg, err := r.Resolve("profile:tgt2")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if host != "target.inline" {
		t.Errorf("host = %q, want target.inline", host)
	}
	if cfg.JumpHost != "jump.inline.com" {
		t.Errorf("JumpHost = %q, want jump.inline.com", cfg.JumpHost)
	}
	if cfg.Secrets != nil {
		t.Error("Secrets should be nil for target without credential")
	}
}

//nolint:errcheck
func TestResolver_CarriesTargetBinding(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	pwID, _ := ss.Create(context.Background(), credential.NewSecret("pw"))
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:bound", Name: "bound"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "bound.example.com",
			Port:           profile.Ptr(2222),
			User:           profile.Ptr("u"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(pwID),
		},
	})

	r := NewResolver(ps, ps, ss)
	_, cfg, err := r.Resolve("profile:bound")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.AuthorizedEndpoint != "bound.example.com:2222" {
		t.Errorf("AuthorizedEndpoint = %q, want bound.example.com:2222", cfg.AuthorizedEndpoint)
	}
}

// TestResolver_BoundSecretSurfacesAuthorizedEndpoint pins that a profile
// whose bound secret carries no endpoint still produces an AuthorizedEndpoint
// from the profile itself.
func TestResolver_BoundSecretSurfacesAuthorizedEndpoint(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	pwID, _ := ss.Create(context.Background(), credential.NewSecret("pw"))
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:unbound", Name: "unbound"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "any.example.com",
			Port:           profile.Ptr(22),
			User:           profile.Ptr("u"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(pwID),
		},
	})

	r := NewResolver(ps, ps, ss)
	_, cfg, err := r.Resolve("profile:unbound")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.AuthorizedEndpoint == "" {
		t.Error("AuthorizedEndpoint = empty, want endpoint from profile (credential is linked)")
	}
	if cfg.AuthorizedEndpoint != "any.example.com:22" {
		t.Errorf("AuthorizedEndpoint = %q, want any.example.com:22 (from profile host:port)", cfg.AuthorizedEndpoint)
	}
}

func TestResolver_CarriesJumpBinding(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	// Jump credential with binding
	jumpPWID, _ := ss.Create(context.Background(), credential.NewSecret("jpw"))
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:jumpb", Name: "jumpb"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "jump-bound.example.com",
			Port:           profile.Ptr(2222),
			User:           profile.Ptr("ju"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(jumpPWID),
		},
	})

	// Target
	tgtPWID, _ := ss.Create(context.Background(), credential.NewSecret("tpw"))
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:tgtb", Name: "tgtb"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "tgt-bound.example.com",
			Port:           profile.Ptr(3333),
			User:           profile.Ptr("tu"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(tgtPWID),
			JumpHost:       profile.Ptr("profile:jumpb"),
		},
	})

	r := NewResolver(ps, ps, ss)
	_, cfg, err := r.Resolve("profile:tgtb")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.AuthorizedEndpoint != "tgt-bound.example.com:3333" {
		t.Errorf("AuthorizedEndpoint = %q, want tgt-bound.example.com:3333", cfg.AuthorizedEndpoint)
	}
	if cfg.JumpAuthorizedEndpoint != "jump-bound.example.com:2222" {
		t.Errorf("JumpAuthorizedEndpoint = %q, want jump-bound.example.com:2222", cfg.JumpAuthorizedEndpoint)
	}
}

// TestResolve_PublishesBoundSecret pins the contract between the profile's
// secret binding and the connection pool. poolKeyFor (ssh_dial.go:38) keys
// on cfg.SecretID, so publishing the bound reference is what makes a rotation
// (vault.replaceSecret keeping the id, changing the material) produce the
// same pool key, while replacing the reference produces a different one —
// with no change anywhere in internal/ssh. Asserting it here is what stops a
// later refactor from quietly publishing something else.
//
//nolint:errcheck
func TestResolve_PublishesBoundSecret(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	prof := profile.SSHProfile{
		Base:    profile.Base{ID: "ssh:custom:web-1:1", Type: "ssh", Name: "web-1"},
		Options: profile.StoredSSHProfileOptions{Host: "10.0.0.1", Port: profile.Ptr(22), PasswordSecret: "sec:7"},
	}
	_ = ps.SaveProfile(prof)

	r := NewResolver(ps, ps, ss)

	_, cfg, err := r.Resolve(prof.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if string(cfg.SecretID) != "sec:7" {
		t.Fatalf("SecretID = %q, want sec:7 (the profile's binding)", cfg.SecretID)
	}
	first := cfg.SecretID

	// Replace the binding (a new password saved over the old one) and
	// resolve again.
	prof.Options.PasswordSecret = "sec:8"
	_ = ps.SaveProfile(prof)

	_, cfg2, err := r.Resolve(prof.ID)
	if err != nil {
		t.Fatalf("Resolve after replacement: %v", err)
	}
	if string(cfg2.SecretID) != "sec:8" {
		t.Fatalf("SecretID = %q, want sec:8", cfg2.SecretID)
	}
	if cfg2.SecretID == first {
		t.Fatal("replacing the binding left the SecretID unchanged; the pool would reuse the old transport")
	}
}

// TestResolver_MultiHopJump verifies that a target behind two bastions
// carries the full recursive JumpConfig chain through to the ConnectConfig.
func TestResolver_MultiHopJump(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	// Inner bastion (closest to client) - jumps through no one
	innerPWID, _ := ss.Create(context.Background(), credential.NewSecret("inner-secret"))
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:inner", Name: "inner-bastion"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "inner.corp.net",
			Port:           profile.Ptr(2201),
			User:           profile.Ptr("inneruser"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(innerPWID),
		},
	})

	// Outer bastion (closest to target) - jumps through inner
	outerPWID, _ := ss.Create(context.Background(), credential.NewSecret("outer-secret"))
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:outer", Name: "outer-bastion"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "outer.corp.net",
			Port:           profile.Ptr(2200),
			User:           profile.Ptr("outeruser"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(outerPWID),
			JumpHost:       profile.Ptr("profile:inner"),
		},
	})

	// Target profile - jumps through outer
	tgtPWID, _ := ss.Create(context.Background(), credential.NewSecret("tgt-secret"))
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:tgt", Name: "target"},
		Options: profile.StoredSSHProfileOptions{
			Host:           "target.internal",
			Port:           profile.Ptr(2222),
			User:           profile.Ptr("tgtuser"),
			Auth:           profile.Ptr(profile.AuthMode("password")),
			PasswordSecret: string(tgtPWID),
			JumpHost:       profile.Ptr("profile:outer"),
		},
	})

	r := NewResolver(ps, ps, ss)
	host, cfg, err := r.Resolve("profile:tgt")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if host != "target.internal" {
		t.Errorf("host = %q, want target.internal", host)
	}
	if cfg.SecretID != tgtPWID {
		t.Errorf("Target SecretID = %q, want %q", cfg.SecretID, tgtPWID)
	}

	// First hop: outer bastion
	if cfg.JumpHost != "outer.corp.net" {
		t.Errorf("JumpHost = %q, want outer.corp.net", cfg.JumpHost)
	}
	if cfg.JumpConfig == nil {
		t.Fatal("JumpConfig is nil, want outer bastion config")
	}
	if cfg.JumpConfig.Port != 2200 {
		t.Errorf("JumpConfig.Port = %d, want 2200", cfg.JumpConfig.Port)
	}
	if cfg.JumpConfig.User != "outeruser" {
		t.Errorf("JumpConfig.User = %q, want outeruser", cfg.JumpConfig.User)
	}
	if cfg.JumpConfig.SecretID != outerPWID {
		t.Errorf("JumpConfig.SecretID = %q, want %q", cfg.JumpConfig.SecretID, outerPWID)
	}

	// Second hop: inner bastion (outer's jump)
	if cfg.JumpConfig.JumpHost != "inner.corp.net" {
		t.Errorf("JumpConfig.JumpHost = %q, want inner.corp.net", cfg.JumpConfig.JumpHost)
	}
	if cfg.JumpConfig.JumpConfig == nil {
		t.Fatal("JumpConfig.JumpConfig is nil, want inner bastion config")
	}
	if cfg.JumpConfig.JumpConfig.SecretID != innerPWID {
		t.Errorf("JumpConfig.JumpConfig.SecretID = %q, want %q", cfg.JumpConfig.JumpConfig.SecretID, innerPWID)
	}
	if cfg.JumpConfig.JumpConfig.JumpConfig != nil {
		t.Errorf("JumpConfig.JumpConfig.JumpConfig should be nil (no more hops), got %v", cfg.JumpConfig.JumpConfig.JumpConfig)
	}
}

// TestResolver_MultiHopCycleDetected verifies that a multi-hop chain with a
// cycle is rejected at resolve time.
func TestResolver_MultiHopCycleDetected(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	// Two profiles that reference each other in a cycle
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:a", Name: "a"},
		Options: profile.StoredSSHProfileOptions{
			Host:     "host-a.net",
			JumpHost: profile.Ptr("profile:b"),
		},
	})
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:b", Name: "b"},
		Options: profile.StoredSSHProfileOptions{
			Host:     "host-b.net",
			JumpHost: profile.Ptr("profile:a"),
		},
	})

	r := NewResolver(ps, ps, ss)
	_, _, err := r.Resolve("profile:a")
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
}

// TestResolver_KeySecretBinding verifies that the profile's key and
// passphrase bindings are resolved into cfg.KeySecretID and
// cfg.PassphraseSecretID on the ConnectConfig.
//
//nolint:errcheck
func TestResolver_KeySecretBinding(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:km:1", Name: "km-test"},
		Options: profile.StoredSSHProfileOptions{
			Host:                "km.example.com",
			User:                profile.Ptr("deploy"),
			Auth:                profile.Ptr(profile.AuthMode("publicKey")),
			KeySecret:           "vault-sec:abc123",
			KeyPassphraseSecret: "vault-sec:pass456",
		},
	})

	r := NewResolver(ps, ps, ss)
	_, cfg, err := r.Resolve("profile:km:1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.KeySecretID != "vault-sec:abc123" {
		t.Errorf("KeySecretID = %q, want vault-sec:abc123", cfg.KeySecretID)
	}
	if cfg.PassphraseSecretID != "vault-sec:pass456" {
		t.Errorf("PassphraseSecretID = %q, want vault-sec:pass456", cfg.PassphraseSecretID)
	}
	if cfg.KeyFile != "" {
		t.Errorf("KeyFile = %q, want empty (key material replaces path)", cfg.KeyFile)
	}
}

// TestResolver_ModeFromEffectiveProfile: the effective desiredMode field
// (profile > group > global > default) is stamped verbatim onto the
// ConnectConfig (nocx-mlm7) — the ssh layer gates open-time integration on
// it directly (script integrates at startup; raw and relay open a plain
// shell — relay is inert this epic) and the open ack reports the same AXIS
// value, which must keep relay distinguishable from raw. One row per mode,
// plus the unset default. The LaunchPolicy translation is retired: the
// resolver stamps the mode, and nothing else.
func TestResolver_ModeFromEffectiveProfile(t *testing.T) {
	cases := []struct {
		name     string
		mode     *profile.DesiredMode
		wantMode string
	}{
		{name: "unset defaults to script", wantMode: "script"},
		{name: "script", mode: profile.Ptr(profile.DesiredScript), wantMode: "script"},
		{name: "raw", mode: profile.Ptr(profile.DesiredRaw), wantMode: "raw"},
		{name: "relay", mode: profile.Ptr(profile.DesiredRelay), wantMode: "relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := newStubProfileStore()
			ss := newStubSecretStore()

			_ = ps.SaveProfile(profile.SSHProfile{
				Base: profile.Base{ID: "profile:si:1", Name: "si-test"},
				Options: profile.StoredSSHProfileOptions{
					Host:        "si.example.com",
					User:        profile.Ptr("deploy"),
					DesiredMode: tc.mode,
				},
			})

			r := NewResolver(ps, ps, ss)
			_, cfg, err := r.Resolve("profile:si:1")
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got := cfg.DesiredMode; got != tc.wantMode {
				t.Errorf("DesiredMode = %q, want %q", got, tc.wantMode)
			}
		})
	}
}

// TestResolver_SavedProfileCarriesRemoteInstaller: the SFTP carrier is
// wired at the composition root and stamped on every ConnectConfig the
// resolver builds for a saved profile (nocx-mlm7 P8). This is the
// reachability proof: a saved connection reaches the installer through
// Resolve → buildConfig → session.sshOptionsFromConfig →
// ssh_real.go:shellStartCommand. Direct-host opens never see it — the
// resolver is the only path that stamps it.
func TestResolver_SavedProfileCarriesRemoteInstaller(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:si:1", Name: "si-test"},
		Options: profile.StoredSSHProfileOptions{
			Host: "si.example.com",
			User: profile.Ptr("deploy"),
		},
	})

	installer := &recordingRemoteInstaller{}
	r := NewResolver(ps, ps, ss, WithRemoteInstaller(installer))
	_, cfg, err := r.Resolve("profile:si:1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.RemoteInstaller == nil {
		t.Fatal("saved profile ConnectConfig carries no RemoteInstaller — the SFTP carrier never publishes")
	}

	// A resolver without the option stamps none: the publish is opt-in per
	// composition root, never a resolver default.
	r2 := NewResolver(ps, ps, ss)
	_, cfg2, err := r2.Resolve("profile:si:1")
	if err != nil {
		t.Fatalf("Resolve (no installer): %v", err)
	}
	if cfg2.RemoteInstaller != nil {
		t.Error("resolver without WithRemoteInstaller stamped one anyway")
	}
}

// recordingRemoteInstaller is the smallest ssh.RemoteInstaller double: the
// resolver only stores the value, it never calls it.
type recordingRemoteInstaller struct{}

func (*recordingRemoteInstaller) GetRemoteHome(*gossh.Client) (string, error) { return "", nil }
func (*recordingRemoteInstaller) EnsureInstalledRemote(context.Context, *gossh.Client, string) error {
	return nil
}
func (*recordingRemoteInstaller) RemoteStartCommand() string { return "" }
func (*recordingRemoteInstaller) UninstallRemote(context.Context, *gossh.Client, string) ([]string, []string, error) {
	return nil, nil, nil
}

// TestResolver_KeepaliveDefaultsWhenTheProfileNamesNone pins the mechanism
// that is now the only thing able to end a session whose transport died in
// silence (nocx-o2le). A write into such a connection never returns and never
// errors; the session's write queue can report that it is stuck but cannot
// close it. If a profile that sets no keepalive resolves to no keepalive, that
// tab hangs until the user kills it.
func TestResolver_KeepaliveDefaultsWhenTheProfileNamesNone(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:bare", Name: "bare"},
		Options: profile.StoredSSHProfileOptions{
			Host: "bare.example.com",
			Port: profile.Ptr(22),
			User: profile.Ptr("admin"),
		},
	})

	r := NewResolver(ps, ps, ss)
	_, cfg, err := r.Resolve("profile:bare")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.KeepaliveInterval != defaultKeepaliveInterval {
		t.Errorf("KeepaliveInterval = %v, want %v — a profile with no keepalive would never notice a dead transport",
			cfg.KeepaliveInterval, defaultKeepaliveInterval)
	}
	if cfg.KeepaliveCountMax != defaultKeepaliveCountMax {
		t.Errorf("KeepaliveCountMax = %d, want %d", cfg.KeepaliveCountMax, defaultKeepaliveCountMax)
	}
}

// TestResolver_KeepaliveFromTheProfileWins is the other end: the default must
// not overwrite what the user asked for.
func TestResolver_KeepaliveFromTheProfileWins(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:tuned", Name: "tuned"},
		Options: profile.StoredSSHProfileOptions{
			Host:              "tuned.example.com",
			Port:              profile.Ptr(22),
			User:              profile.Ptr("admin"),
			KeepaliveInterval: profile.Ptr(5000),
			KeepaliveCountMax: profile.Ptr(9),
		},
	})

	r := NewResolver(ps, ps, ss)
	_, cfg, err := r.Resolve("profile:tuned")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.KeepaliveInterval != 5*time.Second {
		t.Errorf("KeepaliveInterval = %v, want 5s (the profile's 5000ms)", cfg.KeepaliveInterval)
	}
	if cfg.KeepaliveCountMax != 9 {
		t.Errorf("KeepaliveCountMax = %d, want 9", cfg.KeepaliveCountMax)
	}
}
