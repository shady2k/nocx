package system_test

// A provider for a host whose stance excluded the OS keystore asks it
// nothing at all (design D10).
//
// "Zero modal dialogs" is asserted here as ZERO KEYRING CALLS, because the
// modal is raised by the call: go-keyring execs /usr/bin/security, and a Set
// under a $HOME with no login keychain is what raises "Keychain not found".
// A provider that never calls cannot raise one, on any platform — which is
// what makes this assertable on a Linux box that has no login keychain to
// raise a dialog from in the first place.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/vault"
	"github.com/shady2k/nocx/internal/vault/system"
)

// countingKeyring answers nothing and counts every call, naming the operation
// so a failure says which one leaked through.
type countingKeyring struct {
	mu  sync.Mutex
	ops []string
}

func (k *countingKeyring) note(op string) error {
	k.mu.Lock()
	k.ops = append(k.ops, op)
	k.mu.Unlock()
	return system.ErrNoKeystore
}

func (k *countingKeyring) calls() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.ops...)
}

func (k *countingKeyring) Set(_, _, _ string) error { return k.note("Set") }
func (k *countingKeyring) Get(_, _ string) (string, error) {
	return "", k.note("Get")
}
func (k *countingKeyring) Delete(_, _ string) error { return k.note("Delete") }
func (k *countingKeyring) DeleteAll(_ string) error { return k.note("DeleteAll") }

// The whole point: the readiness questions are answered without asking the
// OS anything.
func TestNotInPlay_ProbeAndStatusMakeNoKeyringCall(t *testing.T) {
	kr := &countingKeyring{}
	p := system.NotInPlay(system.WithKeyring(kr))
	ctx := context.Background()

	if got := p.Probe(ctx); got.Ready || got.Reason != vault.ReasonExcluded {
		t.Errorf("Probe = %+v, want not ready with reason %q", got, vault.ReasonExcluded)
	}
	if got := p.Status(ctx); got.Ready || got.Reason != vault.ReasonExcluded {
		t.Errorf("Status = %+v, want not ready with reason %q", got, vault.ReasonExcluded)
	}

	if ops := kr.calls(); len(ops) != 0 {
		t.Fatalf("the keyring was called %v; on macOS the first of those is the "+
			"modal a headless host cannot dismiss", ops)
	}
}

// "excluded" is not "no-service". The old provider-over-a-failing-keyring
// reported no-service, which is a claim about the machine made after looking
// — and looking is exactly what did not happen here.
func TestNotInPlay_DoesNotClaimTheMachineHasNoService(t *testing.T) {
	p := system.NotInPlay()
	if got := p.Probe(context.Background()).Reason; got == vault.ReasonNoService {
		t.Errorf("reason = %q: the product is told the machine has no keyring, "+
			"which nothing established", got)
	}
}

// The reads and writes still refuse honestly rather than panicking, so a
// reference that routes into this provider gets an error naming the
// condition. This is the failure half of every external call the provider
// makes.
func TestNotInPlay_ReadsAndWritesRefuseNamingTheCondition(t *testing.T) {
	p := system.NotInPlay()
	ctx := context.Background()
	id, err := vault.MintReferenceForTest(vault.ProviderSystem)
	if err != nil {
		t.Fatalf("MintReferenceForTest: %v", err)
	}

	// Each failure names the one cause it actually has — nobody asked —
	// rather than the machine claim "no-service".
	excluded := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s succeeded against a keystore that is out of play", what)
			return
		}
		if !errors.Is(err, vault.ErrProviderUnavailable) {
			t.Errorf("%s error = %v, want an unavailable-provider error", what, err)
			return
		}
		var pe *vault.ProviderError
		if !errors.As(err, &pe) {
			t.Errorf("%s error = %v, carries no reason", what, err)
			return
		}
		if pe.Reason != vault.ReasonExcluded {
			t.Errorf("%s reason = %q, want %q", what, pe.Reason, vault.ReasonExcluded)
		}
	}

	_, getErr := p.Get(ctx, id)
	excluded("Get", getErr)
	excluded("Put", p.Put(ctx, id, credential.NewSecret("x")))
	excluded("Delete", p.Delete(ctx, id))
	excluded("PurgeAll", p.PurgeAll(ctx))
}

// The paired success on a normal machine: the SAME provider type, built the
// ordinary way over a working store, probes ready and round-trips. Without
// this, every check above is satisfied by a provider that can do nothing at
// all (AGENTS.md: for every "returns an error when…" there is a paired "and
// on a normal machine it succeeds").
func TestNotInPlay_IsTheOnlyThingThatSuppressesTheProbe(t *testing.T) {
	kr := newMemKeyring()
	ordinary := system.New(system.WithKeyring(kr))
	ctx := context.Background()

	status := ordinary.Probe(ctx)
	if !status.Ready {
		t.Fatalf("an ordinary provider over a working store probed %+v, want ready", status)
	}
	id, err := vault.MintReferenceForTest(vault.ProviderSystem)
	if err != nil {
		t.Fatalf("MintReferenceForTest: %v", err)
	}
	if err := ordinary.Put(ctx, id, credential.NewSecret("kept")); err != nil {
		t.Fatalf("Put on a normal machine: %v", err)
	}
	got, err := ordinary.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get on a normal machine: %v", err)
	}
	if err := got.Use(func(b []byte) error {
		if string(b) != "kept" {
			t.Errorf("secret = %q, want %q", b, "kept")
		}
		return nil
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
}
