package app

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/git"
	"github.com/shady2k/nocx/internal/session"
)

func TestHostKeyPinHelperGitBridgePinsTheExistingSessionHost(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer(t)}
	assertGitBridgePin(t, provider, &fakeRemoteSession{id: "existing", host: "host.example"})
}

func TestHostKeyPinHelperGitBridgePinsTheReadoptedSessionHost(t *testing.T) {
	svc := sharedHelperService(t)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()
	second := newCoordinator(t, provider)
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(),
		readoptFixture(t, second, routesFor(binding), &stubAdopter{}), time.Hour, quietLogger(t))
	sess, err := second.sess.Get(session.ID(binding.SessionID))
	if err != nil {
		t.Fatalf("readopt session: %v", err)
	}
	selection := second.gitFor(sess)
	if selection.Factory == nil {
		t.Fatalf("git selection refused: %+v", selection.Refusal)
	}
	h := second.reg.hosts[sess.ID()]
	if h == nil {
		t.Fatal("readopt did not register its helper")
	}
	for attempt := range 2 {
		if attempt == 1 {
			h.mu.Lock()
			c := h.client
			h.mu.Unlock()
			h.invalidate(c)
			if err := c.Close(); err != nil {
				t.Fatalf("lose readopt client: %v", err)
			}
		}
		repo, outcome, err := selection.Factory.Open(context.Background(), fixtureRepo(t))
		if err != nil || outcome.State != git.OpenOK {
			t.Fatalf("open %d: outcome %+v, error %v", attempt, outcome, err)
		}
		if err := repo.Close(); err != nil {
			t.Fatal(err)
		}
		if second.reg.hosts[sess.ID()] != h {
			t.Fatal("git did not reuse the readopt registry entry")
		}
	}
	// Both the restoration lane and the replacement lane must use the
	// durable session's key, not merely the factory git.open constructed.
	for i := 1; i < provider.laneCount(); i++ {
		if got := provider.asked(i).pin; got != binding.Fingerprint {
			t.Errorf("lane %d pin = %q, want %q", i, got, binding.Fingerprint)
		}
	}
}

func assertGitBridgePin(t *testing.T, provider *fakeLaneProvider, sess session.Session) {
	t.Helper()
	store, installs := testConsentStoresFor(t, sess.HostKeyFingerprint())
	selectGit, reg := helperGitFactory(provider, stubArtifacts(t), store, installs, discardLogger(t))
	endStandOnCleanup(t, provider, reg)
	selection := selectGit(sess)
	if selection.Factory == nil {
		t.Fatalf("git selection refused: %+v", selection.Refusal)
	}
	dir := fixtureRepo(t)
	// Closing the last binding forces the next open onto a new bridge.
	for attempt := range 2 {
		index := provider.laneCount()
		repo, outcome, err := selection.Factory.Open(context.Background(), dir)
		if err != nil || outcome.State != git.OpenOK {
			t.Fatalf("open %d: outcome %+v, error %v", attempt, outcome, err)
		}
		if err := repo.Close(); err != nil {
			t.Fatalf("close git binding: %v", err)
		}
		if got := provider.asked(index).pin; got != sess.HostKeyFingerprint() {
			t.Errorf("bridge %d host-key pin = %q, want session fingerprint %q", attempt, got, sess.HostKeyFingerprint())
		}
	}
}
