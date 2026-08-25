package apibind

// The vault sealed (design §12.1), stated at each of the three seams a
// binding reaches it through, and paired with the same three succeeding
// once it is open.
//
// store_impl_test.go already makes each of those calls fail with an
// anonymous error, which proves the failure is not swallowed. This file
// asserts the thing that only the SEALED failure can be asked: that
// vault.ErrVaultSealed survives every wrap out to the send path, so a
// surface can say "unlock the vault" rather than "bind this variable".
// The two are different sentences and different remedies — one is a
// password prompt, the other sends the user to type a token they already
// have — and merging them is the silent degrade AGENTS.md forbids.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/apicoll"
	"github.com/shady2k/nocx/internal/vault"
)

// sealable is the fake vault with a switch. One object for both halves of
// every pair below, so the ONLY difference between the failure and its
// partner is the seal — a second fake could differ in some other way and
// the pair would prove less than it looks.
type sealable struct {
	*fakeSecrets
	sealed bool
}

func newSealable() *sealable {
	s := &sealable{fakeSecrets: newFakeSecrets()}
	s.seal(true)
	return s
}

// seal sets every one of the three calls to answer as a sealed vault does.
// vault.ErrVaultSealed is what internal/vault actually returns
// (internal/vault/errors.go:12), not a lookalike: an error whose TEXT says
// "sealed" would pass a test that greps the message and fail the product,
// where the caller matches with errors.Is.
func (s *sealable) seal(closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealed = closed
	if closed {
		s.createErr, s.getErr, s.deleteErr = vault.ErrVaultSealed, vault.ErrVaultSealed, vault.ErrVaultSealed
		return
	}
	s.createErr, s.getErr, s.deleteErr = nil, nil, nil
}

// ─── 1. Bind, with the vault sealed ────────────────────────────────────────

// TestBind_ASealedVaultIsReportedAsSealedAndWritesNothing. The ordering of
// §12.2 puts the value first precisely so that this failure leaves the
// document untouched: there is no binding to point at a value that was
// never stored.
func TestBind_ASealedVaultIsReportedAsSealedAndWritesNothing(t *testing.T) {
	docs, secrets := newFakeDocs(), newSealable()
	s := NewStore(docs, secrets)
	k := key("/c", "prod", "token")

	err := s.Bind(context.Background(), k, []byte("t0ken"))
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Bind: err = %v, want it to carry vault.ErrVaultSealed", err)
	}
	if !strings.Contains(err.Error(), k.Variable) {
		t.Errorf("Bind: err = %v, want it to name the variable the user was binding", err)
	}
	if docs.raw(DocumentName) != "" {
		t.Errorf("a binding document was written although the value could not be stored: %s", docs.raw(DocumentName))
	}
	if _, found, lookErr := s.Lookup(k); found || lookErr != nil {
		t.Errorf("Lookup after a failed Bind: found = %v, err = %v; want a clean miss", found, lookErr)
	}
}

// ─── 2. Resolve, with the vault sealed ─────────────────────────────────────

// TestResolve_ASealedVaultIsNotAnUnboundVariable. The binding is there and
// the store can read it; what it cannot do is read the VALUE. Answering
// "not found" here would tell the user to bind a variable they have already
// bound.
func TestResolve_ASealedVaultIsNotAnUnboundVariable(t *testing.T) {
	docs, secrets := newFakeDocs(), newSealable()
	docs.seed(t, record{Collection: "/c", Environment: "prod", Variable: "token", SecretID: "fake:a"})
	s := NewStore(docs, secrets)

	_, found, err := s.Resolve(context.Background(), key("/c", "prod", "token"))
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Resolve: err = %v, want it to carry vault.ErrVaultSealed", err)
	}
	if found {
		t.Error("Resolve reported a value it could not read")
	}
	// And the binding itself is still readable: a sealed vault hides the
	// VALUES, never the fact that the variable is bound.
	if _, bound, lookErr := s.Lookup(key("/c", "prod", "token")); lookErr != nil || !bound {
		t.Errorf("Lookup while sealed: found = %v, err = %v; want the binding, which is not in the vault", bound, lookErr)
	}
}

// ─── 3. The send path, with the vault sealed ───────────────────────────────

// TestVariables_ASealedVaultBlocksTheSendSayingSealed is the seam that
// matters, because it is the one a user reaches: Variables is the lookup
// apicoll.Substitute calls while a request is being sent. The assertion is
// made THROUGH Substitute rather than on the closure alone — a lookup that
// carried the reason and a substitution that dropped it would leave the
// product saying "unresolved variable" for a sealed vault, and the closure
// test alone would still be green.
func TestVariables_ASealedVaultBlocksTheSendSayingSealed(t *testing.T) {
	docs, secrets := newFakeDocs(), newSealable()
	docs.seed(t, record{Collection: "/c", Environment: "prod", Variable: "token", SecretID: "fake:a"})
	s := NewStore(docs, secrets)
	ctx := context.Background()

	if _, _, err := s.Variables(ctx, "/c", "prod")("token"); !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Variables(token): err = %v, want it to carry vault.ErrVaultSealed", err)
	}

	req := apicoll.Request{
		Method:  "GET",
		URL:     "https://api.acme.test/v1",
		Headers: []apicoll.Header{{Name: "Authorization", Value: "Bearer {{token}}", Enabled: true}},
	}
	out, err := apicoll.Substitute(req, s.Variables(ctx, "/c", "prod"))
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Substitute: err = %v, want the sealed vault to reach the send path intact", err)
	}
	if len(out.Headers) > 0 && out.Headers[0].Value != req.Headers[0].Value {
		t.Errorf("the header was substituted to %q although the value could not be read", out.Headers[0].Value)
	}
}

// ─── 4. Unbind, with the vault sealed ──────────────────────────────────────

// TestUnbind_ASealedVaultLeavesTheBindingGoneAndSaysTheValueIsNot. Unbind
// removes the binding FIRST, so a sealed vault at this point is the benign
// half of §8.2: the value is stranded and unreachable rather than a binding
// naming a value that has already gone. The user must still be told, since
// a stranded value is theirs to remove.
func TestUnbind_ASealedVaultLeavesTheBindingGoneAndSaysTheValueIsNot(t *testing.T) {
	docs, secrets := newFakeDocs(), newSealable()
	secrets.seal(false)
	s := NewStore(docs, secrets)
	ctx := context.Background()
	k := key("/c", "prod", "token")
	if err := s.Bind(ctx, k, []byte("t0ken")); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	secrets.seal(true)
	err := s.Unbind(ctx, k)
	if !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Unbind: err = %v, want it to carry vault.ErrVaultSealed", err)
	}
	secrets.seal(false)
	if _, found, lookErr := s.Lookup(k); lookErr != nil || found {
		t.Errorf("Lookup after Unbind: found = %v, err = %v; the binding is gone whatever the vault said", found, lookErr)
	}
	if secrets.count() != 1 {
		t.Errorf("%d values in the vault, want the one that was stranded — Unbind must not claim a delete it did not make", secrets.count())
	}
}

// ─── the pair: the same four calls, on a vault that is open ────────────────

// TestBindResolveVariablesAndUnbind_SucceedOnceTheVaultIsUnsealed is the
// "and on an ordinary machine it succeeds" that AGENTS.md rule 3 demands of
// every one of the four above, on the SAME fake with the seal taken off —
// so what the four tests above measure is the seal and nothing else.
func TestBindResolveVariablesAndUnbind_SucceedOnceTheVaultIsUnsealed(t *testing.T) {
	docs, secrets := newFakeDocs(), newSealable()
	s := NewStore(docs, secrets)
	ctx := context.Background()
	k := key("/c", "prod", "token")

	if err := s.Bind(ctx, k, []byte("t0ken")); !errors.Is(err, vault.ErrVaultSealed) {
		t.Fatalf("Bind while sealed: err = %v, want vault.ErrVaultSealed; the pair below would prove nothing", err)
	}

	secrets.seal(false)

	if err := s.Bind(ctx, k, []byte("t0ken")); err != nil {
		t.Fatalf("Bind on an unsealed vault: %v", err)
	}
	if _, found, err := s.Lookup(k); err != nil || !found {
		t.Fatalf("Lookup: found = %v, err = %v", found, err)
	}
	v, found, err := s.Resolve(ctx, k)
	if err != nil || !found {
		t.Fatalf("Resolve: found = %v, err = %v", found, err)
	}
	var plain string
	if useErr := v.Use(func(b []byte) error { plain = string(b); return nil }); useErr != nil {
		t.Fatalf("Use: %v", useErr)
	}
	if plain != "t0ken" {
		t.Fatalf("Resolve gave %q, want the value that was bound", plain)
	}

	out, err := apicoll.Substitute(apicoll.Request{
		Method:  "GET",
		URL:     "https://api.acme.test/v1",
		Headers: []apicoll.Header{{Name: "Authorization", Value: "Bearer {{token}}", Enabled: true}},
	}, s.Variables(ctx, "/c", "prod"))
	if err != nil {
		t.Fatalf("Substitute on an unsealed vault: %v", err)
	}
	if out.Headers[0].Value != "Bearer t0ken" {
		t.Fatalf("header = %q, want the substituted credential", out.Headers[0].Value)
	}

	if err := s.Unbind(ctx, k); err != nil {
		t.Fatalf("Unbind on an unsealed vault: %v", err)
	}
	if _, found, err := s.Lookup(k); err != nil || found {
		t.Fatalf("Lookup after Unbind: found = %v, err = %v; want a clean miss", found, err)
	}
	if secrets.count() != 0 {
		t.Errorf("%d values left in the vault after Unbind, want 0 — the closing end of §12.2's interval", secrets.count())
	}
}
