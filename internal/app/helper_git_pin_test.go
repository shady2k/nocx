package app

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/session"
)

func TestHostKeyPinHostedCarrierPinsTheExistingSessionHost(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer(t)}
	store, _ := testConsentStores(t)
	reg := newHostedShellRegistry(provider, stubArtifacts(t), store, discardLogger(t))
	endStandOnCleanup(t, provider, reg)
	registerHostedCarrier(t, reg, "existing", "host.example", "SHA256:test-host")
	if got := provider.asked(0).pin; got != "SHA256:test-host" {
		t.Fatalf("carrier pin = %q", got)
	}
}

func TestHostKeyPinHostedCarrierPinsTheReadoptedSessionHost(t *testing.T) {
	svc := sharedHelperService(t)
	provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-1")
	first.quit()
	second := newCoordinator(t, provider)
	rec := &recordingReconciler{pending: []content.PendingSession{binding}}
	reconcileSessions(context.Background(), rec, second.reg.inventories(), readoptFixture(t, second, routesFor(binding), &stubAdopter{}), time.Hour, quietLogger(t))
	h, ok := second.reg.hostFor(session.ID(binding.SessionID))
	if !ok {
		t.Fatal("readopt did not register shell carrier")
	}
	h.mu.Lock()
	c := h.client
	h.mu.Unlock()
	h.invalidate(c)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.reg.sessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < provider.laneCount(); i++ {
		if got := provider.asked(i).pin; got != binding.Fingerprint {
			t.Errorf("lane %d pin=%q, want %q", i, got, binding.Fingerprint)
		}
	}
}
