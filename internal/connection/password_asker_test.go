package connection

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/vault"
)

// fakeWireAsker records requests and returns a canned answer, standing in
// for the transport's RequestConnectionPassword.
type fakeWireAsker struct {
	reqs   []ssh.PasswordRequest
	ans    ssh.PasswordAnswer
	askErr error
}

func (f *fakeWireAsker) ask(_ context.Context, req ssh.PasswordRequest) (ssh.PasswordAnswer, error) {
	f.reqs = append(f.reqs, req)
	if f.askErr != nil {
		return ssh.PasswordAnswer{}, f.askErr
	}
	return f.ans, nil
}

// fakeSecretCreator stands in for the vault's named-create surface. It can
// fail the first n calls with ErrVaultSealed (the sealed-then-unlocked
// shape) and records what it was asked to store.
type fakeSecretCreator struct {
	calls     []vault.SecretMeta
	values    []string
	sealed    int // remaining ErrVaultSealed failures before success
	nextID    int
	nextName  int
	createErr error
}

func (f *fakeSecretCreator) CreateNamedResolved(_ context.Context, value credential.Secret, meta vault.SecretMeta) (credential.SecretID, string, error) {
	f.calls = append(f.calls, meta)
	var v string
	_ = value.Use(func(b []byte) error { v = string(b); return nil })
	f.values = append(f.values, v)
	if f.createErr != nil {
		return "", "", f.createErr
	}
	if f.sealed > 0 {
		f.sealed--
		return "", "", vault.ErrVaultSealed
	}
	f.nextID++
	f.nextName++
	return credential.SecretID("sec:v1:file:test-" + string(rune('a'+f.nextID-1))), "stored-" + string(rune('a'+f.nextName-1)), nil
}

// seededProfile puts one password-mode profile in the stub store and
// returns a resolver with the given wiring.
func seededResolver(t *testing.T, opts ...ResolverOption) (*Resolver, *stubProfileStore, *fakeSecretCreator) {
	t.Helper()
	ps := newStubProfileStore()
	if err := ps.SaveProfile(profile.SSHProfile{
		Base: profile.Base{ID: "p1", Type: "ssh", Name: "prod-web"},
		Options: profile.StoredSSHProfileOptions{
			Host: "web.example.com",
			User: profile.Ptr("deploy"),
			Auth: profile.Ptr(profile.AuthMode("password")),
		},
	}); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	creator := &fakeSecretCreator{}
	opts = append([]ResolverOption{WithSecretCreator(creator)}, opts...)
	r := NewResolver(ps, ps, newStubSecretStore(), opts...)
	return r, ps, creator
}

// TestPasswordAsker_RememberStoresAndBinds is the happy path: accept +
// remember stores the password as a vault secret named per account
// (user@host — the same host with a different user is a different secret)
// and points the profile's password binding at it (ADR-0017), so the next
// open resolves silently.
func TestPasswordAsker_RememberStoresAndBinds(t *testing.T) {
	wire := &fakeWireAsker{ans: ssh.PasswordAnswer{Password: "hunter2", Remember: true}}
	r, ps, creator := seededResolver(t, WithPasswordAsker(wire.ask))

	asker := r.askerFor("p1")
	ans, err := asker.RequestConnectionPassword(context.Background(), ssh.PasswordRequest{
		Connection: "prod-web", User: "deploy", Host: "web.example.com",
	})
	if err != nil {
		t.Fatalf("RequestConnectionPassword: %v", err)
	}
	if ans.Password != "hunter2" {
		t.Errorf("answer password = %q", ans.Password)
	}

	// The secret is stored with the per-account name and password kind.
	if len(creator.calls) != 1 {
		t.Fatalf("creator called %d times, want 1", len(creator.calls))
	}
	if creator.calls[0].Name != "deploy@web.example.com" {
		t.Errorf("secret name = %q, want per-account deploy@web.example.com", creator.calls[0].Name)
	}
	if creator.calls[0].Kind != vault.KindPassword {
		t.Errorf("secret kind = %q, want password", creator.calls[0].Kind)
	}
	if creator.values[0] != "hunter2" {
		t.Errorf("stored value = %q", creator.values[0])
	}

	// The profile references the stored secret.
	bound := ps.profiles["p1"].Options.PasswordSecret
	if bound != "sec:v1:file:test-a" {
		t.Errorf("profile passwordSecret = %q, want the created secret", bound)
	}
}

// TestPasswordAsker_UseOnceStoresNothing pins the decline path: use once,
// store nothing — the creator is never touched and the profile keeps no
// binding.
func TestPasswordAsker_UseOnceStoresNothing(t *testing.T) {
	wire := &fakeWireAsker{ans: ssh.PasswordAnswer{Password: "once", Remember: false}}
	r, ps, creator := seededResolver(t, WithPasswordAsker(wire.ask))

	asker := r.askerFor("p1")
	ans, err := asker.RequestConnectionPassword(context.Background(), ssh.PasswordRequest{
		Connection: "prod-web", User: "deploy", Host: "web.example.com",
	})
	if err != nil {
		t.Fatalf("RequestConnectionPassword: %v", err)
	}
	if ans.Password != "once" {
		t.Errorf("answer password = %q", ans.Password)
	}
	if len(creator.calls) != 0 {
		t.Errorf("creator called %d times, want 0 (use-once stores nothing)", len(creator.calls))
	}
	if ps.profiles["p1"].Options.PasswordSecret != "" {
		t.Errorf("profile gained a binding: %q", ps.profiles["p1"].Options.PasswordSecret)
	}
}

// TestPasswordAsker_WireErrorPropagates pins the wire outcomes (no client
// connected, prompt cancelled) reaching the connection unchanged — the
// connection fails with the wire's own reason, never with ErrNoAuthMethod.
func TestPasswordAsker_WireErrorPropagates(t *testing.T) {
	wireErr := errors.New("connection password prompt cancelled")
	wire := &fakeWireAsker{askErr: wireErr}
	r, _, creator := seededResolver(t, WithPasswordAsker(wire.ask))

	asker := r.askerFor("p1")
	_, err := asker.RequestConnectionPassword(context.Background(), ssh.PasswordRequest{
		Connection: "prod-web", User: "deploy", Host: "web.example.com",
	})
	if !errors.Is(err, wireErr) {
		t.Fatalf("error = %v, want the wire's own error", err)
	}
	if len(creator.calls) != 0 {
		t.Errorf("creator called %d times after a failed ask", len(creator.calls))
	}
}

// TestPasswordAsker_SealedVaultPropagatesTheSealedError pins the remember
// path's failure: the vault is sealed, the create fails, and the asker
// reports the failure preserving the sealed error — errors.Is must still
// find ErrVaultSealed through the wrap, because the session.open handler
// and the dispatcher seam normalize exactly that error into the canonical
// shape the renderer turns into the unlock prompt (ADR-0032). The unlock
// is NOT this layer's.
func TestPasswordAsker_SealedVaultPropagatesTheSealedError(t *testing.T) {
	wire := &fakeWireAsker{ans: ssh.PasswordAnswer{Password: "pw", Remember: true}}
	r, _, _ := seededResolver(t,
		WithPasswordAsker(wire.ask),
		WithSecretCreator(&fakeSecretCreator{createErr: vault.ErrVaultSealed}),
	)
	asker := r.askerFor("p1")
	_, err := asker.RequestConnectionPassword(context.Background(), ssh.PasswordRequest{
		Connection: "prod-web", User: "deploy", Host: "web.example.com",
	})
	if err == nil {
		t.Fatal("expected a sealed-vault failure, got nil")
	}
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Errorf("error does not unwrap to ErrVaultSealed: %v", err)
	}
	if !strings.Contains(err.Error(), "was not saved") {
		t.Errorf("message does not say the password was not saved: %q", err.Error())
	}
}

// TestPasswordAsker_SealedVaultDoesNotRetry pins the boundary: the asker
// fails once and propagates — the unlock+replay is the renderer's, and a
// second create here would be a second owner of the same behaviour
// (ADR-0032).
func TestPasswordAsker_SealedVaultDoesNotRetry(t *testing.T) {
	wire := &fakeWireAsker{ans: ssh.PasswordAnswer{Password: "pw", Remember: true}}
	creator := &fakeSecretCreator{createErr: vault.ErrVaultSealed}
	r, _, _ := seededResolver(t,
		WithPasswordAsker(wire.ask),
		WithSecretCreator(creator),
	)
	asker := r.askerFor("p1")
	_, err := asker.RequestConnectionPassword(context.Background(), ssh.PasswordRequest{
		Connection: "prod-web", User: "deploy", Host: "web.example.com",
	})
	if err == nil {
		t.Fatal("expected a sealed-vault failure, got nil")
	}
	if len(creator.calls) != 1 {
		t.Errorf("creator called %d times, want exactly 1 (no hidden retry)", len(creator.calls))
	}
}

// TestPasswordAsker_BindFailureNeverClaimsRemembered pins the partial
// failure: the secret was stored but the profile could not reference it —
// the asker fails loudly with a message that says exactly that, never a
// silent success that leaves the next open prompting again.
func TestPasswordAsker_BindFailureNeverClaimsRemembered(t *testing.T) {
	wire := &fakeWireAsker{ans: ssh.PasswordAnswer{Password: "pw", Remember: true}}
	r, _, creator := seededResolver(t, WithPasswordAsker(wire.ask))

	// A profile that does not exist anymore: findProfile fails, the
	// binding cannot land.
	asker := r.askerFor("ghost")
	_, err := asker.RequestConnectionPassword(context.Background(), ssh.PasswordRequest{
		Connection: "prod-web", User: "deploy", Host: "web.example.com",
	})
	if err == nil {
		t.Fatal("expected a bind failure, got nil")
	}
	if !strings.Contains(err.Error(), "could not be bound") {
		t.Errorf("message does not say the binding failed: %q", err.Error())
	}
	if len(creator.calls) != 1 {
		t.Errorf("creator called %d times, want the store to have happened once", len(creator.calls))
	}
}

// TestPasswordAsker_CreatorMissingFailsLoud: a remember answer with no
// secret store wired must fail loudly, not silently degrade to use-once.
func TestPasswordAsker_CreatorMissingFailsLoud(t *testing.T) {
	wire := &fakeWireAsker{ans: ssh.PasswordAnswer{Password: "pw", Remember: true}}
	r, _, _ := seededResolver(t, WithPasswordAsker(wire.ask))
	// Replace the creator with nil — seededResolver wired one by default.
	r.creator = nil

	asker := r.askerFor("p1")
	_, err := asker.RequestConnectionPassword(context.Background(), ssh.PasswordRequest{
		Connection: "prod-web", User: "deploy", Host: "web.example.com",
	})
	if err == nil {
		t.Fatal("expected a loud failure, got nil")
	}
	if !strings.Contains(err.Error(), "not saved") {
		t.Errorf("message = %q", err.Error())
	}
}

// TestResolver_CarriesPromptRungForSavedProfiles pins the wiring: with the
// ask available, a resolved profile's config carries the prompt rung and
// the connection name (nocx-s8jn); without it, the config is exactly as
// before.
func TestResolver_CarriesPromptRungForSavedProfiles(t *testing.T) {
	wire := &fakeWireAsker{ans: ssh.PasswordAnswer{Password: "pw"}}
	r, _, _ := seededResolver(t, WithPasswordAsker(wire.ask))

	host, cfg, err := r.Resolve("p1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if host != "web.example.com" {
		t.Errorf("host = %q", host)
	}
	if cfg.ConnectionName != "prod-web" {
		t.Errorf("connectionName = %q, want the profile name", cfg.ConnectionName)
	}
	if cfg.PasswordRequester == nil {
		t.Fatal("config carries no password requester despite the ask being wired")
	}

	// Without the ask, nothing changes: no requester.
	r2, _, _ := seededResolver(t)
	_, cfg2, err := r2.Resolve("p1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg2.PasswordRequester != nil {
		t.Error("config must carry no password requester when the ask is not wired")
	}
}
