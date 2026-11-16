package app

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/git"
)

func TestARepositoryOnARetiredHelperCannotForgetItsReplacement(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer(t)}
	store, installs := testConsentStores(t)
	factory, reg := helperGitFactory(provider, stubArtifacts(t), store, installs, discardLogger(t))
	endStandOnCleanup(t, provider, reg)
	selected := factory(&fakeRemoteSession{id: "s1", host: "host.example"})
	dir := fixtureRepo(t)
	oldRepo, outcome, err := selected.Factory.Open(context.Background(), dir)
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("first open: %v %+v", err, outcome)
	}
	t.Cleanup(func() { _ = oldRepo.Close() })
	if closeErr := reg.CloseHelpersFor(context.Background(), "SHA256:test-host"); closeErr != nil {
		t.Fatal(closeErr)
	}
	reg.FinishHelpersFor("SHA256:test-host")
	newRepo, outcome, err := selected.Factory.Open(context.Background(), dir)
	if err != nil || outcome.State != git.OpenOK {
		t.Fatalf("replacement open: %v %+v", err, outcome)
	}
	t.Cleanup(func() { _ = newRepo.Close() })
	replacement, ok := reg.hostFor("s1")
	if !ok {
		t.Fatal("replacement helper was not registered")
	}
	// A panel can release its old binding after a new helper already serves
	// the same session. That release owns only the retired helper.
	_ = oldRepo.Close()
	if current, ok := reg.hostFor("s1"); !ok || current != replacement {
		t.Fatal("releasing the retired repository forgot the replacement helper")
	}
	endHelperChannels(reg)
	if got := provider.lane(1).closeCount(); got != 1 {
		t.Fatalf("shutdown closed replacement lane %d times, want 1", got)
	}
}
