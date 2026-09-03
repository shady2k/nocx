package agenttools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
)

// expiryFixturePolicy permits observe over one directory, so the mint
// produces a grant files.read can really narrow against.
func expiryFixturePolicy(root string) content.EffectPolicy {
	row := content.EffectRow{
		Decision: content.DecisionPermit,
		Scopes:   []content.GrantScope{{Kind: content.ResourcePath, ID: root}},
	}
	return content.EffectPolicy{
		Observe:           row,
		MutateReversible:  row,
		MutateDestructive: row,
		PrivilegeChange:   row,
		Disclose:          row,
		CrossBoundary:     row,
		Delegate:          row,
	}
}

// The narrowing is where the deadline is enforced (nocx-1z1r1). Not a
// predicate before dispatch — ADR-0028 decision 4 rejects that shape because
// a check leaves the tool holding a full manager — but the constructor
// itself: past the deadline no capability object comes into existence, so
// there is nothing for the tool to hold.
//
// Every declaration's constructor is wrapped once, at assembly, so a tool
// added later cannot forget it and a Narrow author never has to remember.
func TestNarrowRefusesAnExpiredGrant(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ordinary.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := reg.Lookup("files.read")
	if !ok {
		t.Fatal("files.read is not in the assembled registry")
	}

	grant := expiryFixturePolicy(root).AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: root}})
	grant.ExpiresAt = time.Now().Add(-time.Millisecond).UnixMilli()

	capability, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: path}}, RunContext{})
	if !errors.Is(err, content.ErrGrantExpired) {
		t.Fatalf("Narrow under an expired grant: err = %v, want content.ErrGrantExpired", err)
	}
	if capability != nil {
		t.Fatal("Narrow returned a capability from an expired grant")
	}
}

// And on an ordinary machine a freshly minted grant narrows: the paired half
// AGENTS.md demands, without which "refuses when expired" is satisfied by a
// constructor that refuses everything.
func TestNarrowSucceedsOnAFreshlyMintedGrant(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ordinary.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	tool, ok := reg.Lookup("files.read")
	if !ok {
		t.Fatal("files.read is not in the assembled registry")
	}

	grant := expiryFixturePolicy(root).AsGrant([]content.GrantScope{{Kind: content.ResourcePath, ID: root}})
	capability, err := tool.Narrow(grant, []ResourceRef{{Kind: content.ResourcePath, ID: path}}, RunContext{})
	if err != nil {
		t.Fatalf("Narrow under a freshly minted grant: %v", err)
	}
	if capability == nil {
		t.Fatal("Narrow returned no capability for a live grant")
	}
}

// Every executable declaration is wrapped, not just the one the tests above
// reach through. The registry is the only handle a consumer has on a
// constructor, so wrapping at assembly is what makes "no capability from an
// expired grant" true of the whole table.
func TestEveryExecutableDeclarationRefusesAnExpiredGrant(t *testing.T) {
	reg, err := Assemble(mustDirFS(t))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	expired := content.Grant{Version: 1, ExpiresAt: time.Now().Add(-time.Hour).UnixMilli()}
	executable := 0
	for _, tool := range reg.All() {
		if tool.Narrow == nil {
			continue
		}
		executable++
		capability, err := tool.Narrow(expired, nil, RunContext{})
		if !errors.Is(err, content.ErrGrantExpired) {
			t.Errorf("%s: Narrow under an expired grant: err = %v, want content.ErrGrantExpired", tool.Name, err)
		}
		if capability != nil {
			t.Errorf("%s: Narrow returned a capability from an expired grant", tool.Name)
		}
	}
	if executable == 0 {
		t.Fatal("no executable declaration assembled: the assertion above proved nothing")
	}
}
