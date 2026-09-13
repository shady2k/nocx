package ssh

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
)

// fakePasswordRequester records requests and returns a canned answer. A
// cancelErr makes RequestConnectionPassword fail like a dismissed prompt.
type fakePasswordRequester struct {
	reqs      []PasswordRequest
	ans       PasswordAnswer
	cancelErr error
}

func (f *fakePasswordRequester) RequestConnectionPassword(_ context.Context, req PasswordRequest) (PasswordAnswer, error) {
	f.reqs = append(f.reqs, req)
	if f.cancelErr != nil {
		return PasswordAnswer{}, f.cancelErr
	}
	return f.ans, nil
}

// promptRung finds the prompt-password rung of a chain, failing the test
// when it is absent.
func promptRung(t *testing.T, chain []authChainEntry) authChainEntry {
	t.Helper()
	for _, e := range chain {
		if e.kind == kindPromptPassword {
			return e
		}
	}
	t.Fatalf("chain has no prompt-password rung: %+v", chain)
	return authChainEntry{}
}

// TestPromptRung_AlwaysPresentForPasswordModes pins the tabby model: the
// ladder for a password-capable connection ALWAYS ends with the prompt
// rung, ordered after the stored material — the ladder never ends empty,
// which is why there is no "if the ladder came out empty, then prompt"
// fallback. publicKey mode has no prompt rung: asking for a password there
// would be wrong, and ErrNoAuthMethod keeps meaning what it means.
func TestPromptRung_AlwaysPresentForPasswordModes(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	resolved := &resolvedConfig{user: "alice", hostName: "h"}

	// No stored secret at all: the connection a fresh profile is in.
	cfg := &ConnectConfig{AuthMode: "password", ConnectionName: "prod-web", PasswordRequester: &fakePasswordRequester{}}
	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	rung := promptRung(t, chain)
	if rung.method == nil {
		t.Fatal("prompt rung carries no method despite a wired requester")
	}
	// The rung must be AFTER the stored material (none here) — just assert
	// it is the last real rung before hostbased.
	if got := chain[len(chain)-2].kind; got != kindPromptPassword {
		t.Errorf("prompt rung is not the last password rung: got %v before hostbased", got)
	}

	// Auto mode with a stored password: savedPassword precedes the prompt rung.
	store := newTestStore()
	id, err := store.Create(ctx, credential.NewSecret("pw123"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cfg2 := &ConnectConfig{Secrets: store, SecretID: id, PasswordRequester: &fakePasswordRequester{}}
	chain2, err := rc.buildAuthChain(ctx, resolved, cfg2)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	savedIdx, promptIdx := -1, -1
	for i, e := range chain2 {
		switch e.kind {
		case kindSavedPassword:
			savedIdx = i
		case kindPromptPassword:
			promptIdx = i
		}
	}
	if savedIdx < 0 {
		t.Fatal("stored password rung missing")
	}
	if promptIdx < 0 {
		t.Fatal("prompt rung missing despite a wired requester")
	}
	if savedIdx > promptIdx {
		t.Errorf("savedPassword (%d) must precede promptPassword (%d)", savedIdx, promptIdx)
	}

	// publicKey mode: no prompt rung at all.
	cfg3 := &ConnectConfig{AuthMode: "publicKey", PasswordRequester: &fakePasswordRequester{}}
	chain3, err := rc.buildAuthChain(ctx, resolved, cfg3)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	for _, e := range chain3 {
		if e.kind == kindPromptPassword {
			t.Fatal("publicKey mode must not carry a prompt-password rung")
		}
	}
}

// TestPromptRung_NilRequester behaves exactly as before: the rung carries
// no method, so a password-capable connection with nothing stored still
// ends empty (ErrNoAuthMethod) exactly as it did before the ask existed.
func TestPromptRung_NilRequester(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	resolved := &resolvedConfig{user: "alice", hostName: "h"}

	cfg := &ConnectConfig{AuthMode: "password"}
	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	if rung := promptRung(t, chain); rung.method != nil {
		t.Error("prompt rung must be inert without a requester")
	}
	if auths := authMethodsFromChain(chain); len(auths) != 0 {
		t.Errorf("chain without a requester must stay empty, got %d methods", len(auths))
	}
}

// TestPromptRung_AsksNamingConnectionAndHost asserts the prompt names which
// password it is asking for (nocx-s8jn): the request carries the profile
// name, the account and the reason, and the answer's password is what the
// auth attempt sends.
func TestPromptRung_AsksNamingConnectionAndHost(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	resolved := &resolvedConfig{user: "alice", hostName: "web.example.com"}

	asker := &fakePasswordRequester{ans: PasswordAnswer{Password: "s3cret", Remember: true}}
	cfg := &ConnectConfig{AuthMode: "password", ConnectionName: "prod-web", PasswordRequester: asker}
	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	if promptRung(t, chain).method == nil {
		t.Fatal("prompt rung carries no method despite a wired requester")
	}
	// The rung wraps the live callback: the password the auth attempt
	// sends is the answer's password, and the ask names the connection.
	cb := rc.promptPasswordCallback(ctx, cfg, resolved, false)
	pw, err := cb()
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if pw != "s3cret" {
		t.Errorf("password = %q, want the answer's password", pw)
	}
	if len(asker.reqs) != 1 {
		t.Fatalf("requester called %d times, want 1", len(asker.reqs))
	}
	req := asker.reqs[0]
	if req.Connection != "prod-web" || req.User != "alice" || req.Host != "web.example.com" {
		t.Errorf("request does not name the connection and account: %+v", req)
	}
	if req.Reason != "no password is stored for this connection" {
		t.Errorf("reason = %q", req.Reason)
	}
}

// TestPromptRung_ReasonNamesStoredRejected pins the honest reason: when a
// stored password rung precedes the prompt, a fired prompt means the server
// rejected the stored material, and the prompt says so.
func TestPromptRung_ReasonNamesStoredRejected(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	resolved := &resolvedConfig{user: "alice", hostName: "h"}

	store := newTestStore()
	id, err := store.Create(ctx, credential.NewSecret("storedpw"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	asker := &fakePasswordRequester{ans: PasswordAnswer{Password: "newpw"}}
	cfg := &ConnectConfig{Secrets: store, SecretID: id, PasswordRequester: asker}
	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	cb := rc.promptPasswordCallback(ctx, cfg, resolved, hasStoredPasswordRung(chain))
	if _, err := cb(); err != nil {
		t.Fatalf("callback: %v", err)
	}
	if got := asker.reqs[0].Reason; got != "the stored password was rejected" {
		t.Errorf("reason = %q, want the stored-rejected reason", got)
	}
}

// TestPromptRung_CancelPropagates asserts that dismissing the prompt fails
// the auth attempt with the dismissal error — never silently, and never
// relabelled as ErrNoAuthMethod. This is the "cancelling fails with the
// user's reason" contract.
func TestPromptRung_CancelPropagates(t *testing.T) {
	rc := newTestRealClient(t)
	ctx := context.Background()
	resolved := &resolvedConfig{user: "alice", hostName: "h"}

	cancelErr := errors.New("connection password prompt cancelled")
	asker := &fakePasswordRequester{cancelErr: cancelErr}
	cfg := &ConnectConfig{AuthMode: "password", PasswordRequester: asker}
	chain, err := rc.buildAuthChain(ctx, resolved, cfg)
	if err != nil {
		t.Fatalf("buildAuthChain: %v", err)
	}
	if promptRung(t, chain).method == nil {
		t.Fatal("prompt rung carries no method despite a wired requester")
	}
	cb := rc.promptPasswordCallback(ctx, cfg, resolved, false)
	_, err = cb()
	if !errors.Is(err, cancelErr) {
		t.Fatalf("callback error = %v, want the cancellation error", err)
	}
}
