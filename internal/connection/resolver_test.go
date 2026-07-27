package connection

import (
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
)

// stubProfileStore implements both profile.ProfileRepository and
// profile.CredentialMetadataRepository in memory, for tests that
// need both.
type stubProfileStore struct {
	profiles    map[string]profile.SSHProfile
	credentials map[string]profile.Credential
}

func newStubProfileStore() *stubProfileStore {
	return &stubProfileStore{
		profiles:    make(map[string]profile.SSHProfile),
		credentials: make(map[string]profile.Credential),
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

func (s *stubProfileStore) SaveProfile(p profile.SSHProfile) error {
	s.profiles[p.ID] = p
	return nil
}

func (s *stubProfileStore) DeleteProfile(id string) error {
	delete(s.profiles, id)
	return nil
}

// --- profile.CredentialMetadataRepository ---

func (s *stubProfileStore) LoadCredentials() ([]profile.Credential, error) {
	out := make([]profile.Credential, 0, len(s.credentials))
	for _, c := range s.credentials {
		out = append(out, c)
	}
	return out, nil
}

func (s *stubProfileStore) SaveCredential(c profile.Credential) error {
	s.credentials[c.ID] = c
	return nil
}

func (s *stubProfileStore) DeleteCredential(id string) error {
	delete(s.credentials, id)
	return nil
}

// stubSecretStore implements credential.SecretStore in memory.
type stubSecretStore struct {
	secrets map[credential.SecretID]credential.Secret
}

func newStubSecretStore() *stubSecretStore {
	return &stubSecretStore{secrets: make(map[credential.SecretID]credential.Secret)}
}

func (s *stubSecretStore) Get(id credential.SecretID) (credential.Secret, error) {
	val, ok := s.secrets[id]
	if !ok {
		return credential.Secret{}, nil
	}
	return val, nil
}

func (s *stubSecretStore) Set(id credential.SecretID, value credential.Secret) error {
	s.secrets[id] = value
	return nil
}

func (s *stubSecretStore) Delete(id credential.SecretID) error {
	delete(s.secrets, id)
	return nil
}

func (s *stubSecretStore) Exists(id credential.SecretID) (bool, error) {
	_, ok := s.secrets[id]
	return ok, nil
}

//nolint:errcheck
func TestResolver_CredentialMode(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	pwID := credential.NewSecretID()
	_ = ss.Set(pwID, credential.NewSecret("s3cret"))

	_ = ps.SaveCredential(profile.Credential{
		ID:       "cred:work:abc123",
		Name:     "work-key",
		Username: "deploy",
		Auth:     "publicKey",
		KeyPath:  "/home/user/.ssh/work_rsa",
		SecretID: string(pwID),
	})

	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:1", Name: "staging"},
		Options: profile.SSHProfileOptions{
			Host:         "staging.example.com",
			Port:         2222,
			CredentialID: "cred:work:abc123",
		},
	})

	r := NewResolver(ps, ps, ss, nil)
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
		Options: profile.SSHProfileOptions{
			Host: "legacy.example.com",
			Port: 22,
			User: "admin",
			Auth: "password",
		},
	})

	r := NewResolver(ps, ps, ss, nil)
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

//nolint:errcheck
func TestResolver_UnknownProfile(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	r := NewResolver(ps, ps, ss, nil)
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
	jumpPWID := credential.NewSecretID()
	_ = ss.Set(jumpPWID, credential.NewSecret("jump-secret"))
	_ = ps.SaveCredential(profile.Credential{
		ID: "cred:jump:xyz", Name: "jump-cred", Username: "jumpuser", Auth: "publicKey",
		KeyPath:  "/home/user/.ssh/jump_rsa",
		SecretID: string(jumpPWID),
	})
	_ = ps.SaveProfile(profile.SSHProfile{
		Base:    profile.Base{ID: "profile:jump", Name: "jump"},
		Options: profile.SSHProfileOptions{Host: "jump.example.com", Port: 22, CredentialID: "cred:jump:xyz"},
	})

	// Target profile
	tgtPWID := credential.NewSecretID()
	_ = ss.Set(tgtPWID, credential.NewSecret("tgt-secret"))
	_ = ps.SaveCredential(profile.Credential{
		ID:       "cred:tgt:def",
		Name:     "tgt-cred",
		Username: "tgtuser",
		Auth:     "password",
		SecretID: string(tgtPWID),
	})
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:tgt", Name: "target"},
		Options: profile.SSHProfileOptions{
			Host:         "target.internal",
			Port:         2222,
			CredentialID: "cred:tgt:def",
			JumpHost:     "profile:jump",
		},
	})

	r := NewResolver(ps, ps, ss, nil)
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
		Options: profile.SSHProfileOptions{
			Host: "jump.inline.com",
			Port: 22,
			User: "jumper",
			Auth: "publicKey",
		},
	})

	// Target profile
	_ = ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "profile:tgt2", Name: "target"},
		Options: profile.SSHProfileOptions{
			Host:     "target.inline",
			Port:     3333,
			JumpHost: "profile:jump-inline",
		},
	})

	r := NewResolver(ps, ps, ss, nil)
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

	pwID := credential.NewSecretID()
	_ = ss.Set(pwID, credential.NewSecret("pw"))
	_ = ps.SaveCredential(profile.Credential{
		ID:       "cred:bound:aaa",
		Name:     "bound-cred",
		Username: "u",
		Auth:     "password",
		Host:     "bound.example.com",
		Port:     2222,
		SecretID: string(pwID),
		TrustedEndpoints: []profile.CredentialTrustedEndpoint{
			{ProfileID: "profile:bound", Host: "bound.example.com", Port: 2222},
		},
	})
	_ = ps.SaveProfile(profile.SSHProfile{
		Base:    profile.Base{ID: "profile:bound", Name: "bound"},
		Options: profile.SSHProfileOptions{Host: "bound.example.com", Port: 2222, CredentialID: "cred:bound:aaa"},
	})

	// ADR-0013: endpointResolver required for grant check
	resolver := func(p profile.SSHProfile) (string, uint16, error) {
		return p.Options.Host, uint16(p.Options.Port), nil
	}
	r := NewResolver(ps, ps, ss, resolver)
	_, cfg, err := r.Resolve("profile:bound")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.BoundHost != "bound.example.com" {
		t.Errorf("BoundHost = %q, want bound.example.com", cfg.BoundHost)
	}
	if cfg.BoundPort != 2222 {
		t.Errorf("BoundPort = %d, want 2222", cfg.BoundPort)
	}
}

//nolint:errcheck
func TestResolver_UnboundCredentialSurfacesEmpty(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	pwID := credential.NewSecretID()
	_ = ss.Set(pwID, credential.NewSecret("pw"))
	_ = ps.SaveCredential(profile.Credential{
		ID:       "cred:unbound:bbb",
		Name:     "unbound",
		Username: "u",
		Auth:     "password",
		Host:     "", // unbound
		Port:     0,
		SecretID: string(pwID),
	})
	_ = ps.SaveProfile(profile.SSHProfile{
		Base:    profile.Base{ID: "profile:unbound", Name: "unbound"},
		Options: profile.SSHProfileOptions{Host: "any.example.com", CredentialID: "cred:unbound:bbb"},
	})

	r := NewResolver(ps, ps, ss, nil)
	_, cfg, err := r.Resolve("profile:unbound")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.BoundHost != "" {
		t.Errorf("BoundHost = %q, want empty (unbound)", cfg.BoundHost)
	}
	if cfg.BoundPort != 0 {
		t.Errorf("BoundPort = %d, want 0", cfg.BoundPort)
	}
}

//nolint:errcheck
func TestResolver_CarriesJumpBinding(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	// Jump credential with binding
	jumpPWID := credential.NewSecretID()
	_ = ss.Set(jumpPWID, credential.NewSecret("jpw"))
	_ = ps.SaveCredential(profile.Credential{
		ID:       "cred:jumpbound:ccc",
		Name:     "jump-bound",
		Username: "ju",
		Auth:     "password",
		Host:     "jump-bound.example.com",
		Port:     2222,
		SecretID: string(jumpPWID),
		TrustedEndpoints: []profile.CredentialTrustedEndpoint{
			{ProfileID: "profile:jumpb", Host: "jump-bound.example.com", Port: 2222},
		},
	})
	_ = ps.SaveProfile(profile.SSHProfile{
		Base:    profile.Base{ID: "profile:jumpb", Name: "jumpb"},
		Options: profile.SSHProfileOptions{Host: "jump-bound.example.com", Port: 2222, CredentialID: "cred:jumpbound:ccc"},
	})

	// Target
	tgtPWID := credential.NewSecretID()
	_ = ss.Set(tgtPWID, credential.NewSecret("tpw"))
	_ = ps.SaveCredential(profile.Credential{
		ID:       "cred:tgtbound:ddd",
		Name:     "tgt-bound",
		Username: "tu",
		Auth:     "password",
		Host:     "tgt-bound.example.com",
		Port:     3333,
		SecretID: string(tgtPWID),
		TrustedEndpoints: []profile.CredentialTrustedEndpoint{
			{ProfileID: "profile:tgtb", Host: "tgt-bound.example.com", Port: 22},
		},
	})
	_ = ps.SaveProfile(profile.SSHProfile{
		Base:    profile.Base{ID: "profile:tgtb", Name: "tgtb"},
		Options: profile.SSHProfileOptions{Host: "tgt-bound.example.com", Port: 22, CredentialID: "cred:tgtbound:ddd", JumpHost: "profile:jumpb"},
	})

	resolver := func(p profile.SSHProfile) (string, uint16, error) { return p.Options.Host, uint16(p.Options.Port), nil }
	r := NewResolver(ps, ps, ss, resolver)
	_, cfg, err := r.Resolve("profile:tgtb")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.BoundHost != "tgt-bound.example.com" {
		t.Errorf("BoundHost = %q, want tgt-bound.example.com", cfg.BoundHost)
	}
	if cfg.BoundPort != 22 {
		t.Errorf("BoundPort = %d, want 22 (canonical endpoint port)", cfg.BoundPort)
	}
	if cfg.JumpBoundHost != "jump-bound.example.com" {
		t.Errorf("JumpBoundHost = %q, want jump-bound.example.com", cfg.JumpBoundHost)
	}
	if cfg.JumpBoundPort != 2222 {
		t.Errorf("JumpBoundPort = %d, want 2222", cfg.JumpBoundPort)
	}
}

// TestBuildConfig_GrantCheckCalled verifies that buildConfig calls endpointResolver
// and sets BoundHost/BoundPort from canonical endpoint.
func TestBuildConfig_GrantCheckCalled(t *testing.T) {
	ps := newStubProfileStore()
	ss := newStubSecretStore()

	pwID := credential.NewSecretID()
	_ = ss.Set(pwID, credential.NewSecret("pw"))
	_ = ps.SaveCredential(profile.Credential{
		ID:       "cred:test",
		Name:     "test",
		Username: "u",
		Auth:     "password",
		SecretID: string(pwID),
		TrustedEndpoints: []profile.CredentialTrustedEndpoint{
			{ProfileID: "profile:test", Host: "resolved.example.com", Port: 2222},
		},
	})
	_ = ps.SaveProfile(profile.SSHProfile{
		Base:    profile.Base{ID: "profile:test", Name: "test"},
		Options: profile.SSHProfileOptions{Host: "alias.example.com", Port: 2222, CredentialID: "cred:test"},
	})

	resolverCalled := false
	resolver := func(p profile.SSHProfile) (string, uint16, error) {
		resolverCalled = true
		t.Logf("resolver called: host=%s port=%d", p.Options.Host, p.Options.Port)
		return "resolved.example.com", 2222, nil
	}

	r := NewResolver(ps, ps, ss, resolver)
	_, cfg, err := r.Resolve("profile:test")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !resolverCalled {
		t.Fatal("endpointResolver was not called")
	}

	t.Logf("cfg.BoundHost=%q cfg.BoundPort=%d", cfg.BoundHost, cfg.BoundPort)

	if cfg.BoundHost != "resolved.example.com" {
		t.Errorf("BoundHost = %q, want resolved.example.com", cfg.BoundHost)
	}
	if cfg.BoundPort != 2222 {
		t.Errorf("BoundPort = %d, want 2222", cfg.BoundPort)
	}
}
