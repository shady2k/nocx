package app

// The integration method belongs to the HOST (ADR-727g63tr decision 1, whose
// context is ADR-0069's ask and ADR-0105's "a method is not a permission").
// These tests are section 5.10's lifecycle scenario 1 and the ask's own
// once-per-host property, exercised at the seam the open path uses:
// openHoldingLease plus the host record.
//
// The record is the PRODUCTION store over a temporary document, and the answer
// is written with the same call connections.setIntegrationMethod makes, so what
// is proven here is the pair of writes the product performs and not a double of
// either half.

import (
	"context"
	"errors"
	"testing"

	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport"
)

// mustRecordHostMode is the answer write, with the fingerprint the ask named —
// the same store and the same call the connect-time ask's RPC performs.
func mustRecordHostMode(t *testing.T, modes *hostmode.Store, fingerprint string, mode hostmode.Mode) {
	t.Helper()
	if err := modes.Set(fingerprint, mode); err != nil {
		t.Fatalf("record %q for %s: %v", mode, fingerprint, err)
	}
}

// askOf fails unless err is the connect-time ask, and returns it.
func askOf(t *testing.T, err error) *transport.ErrHelperConsentNeeded {
	t.Helper()
	var ask *transport.ErrHelperConsentNeeded
	if !errors.As(err, &ask) {
		t.Fatalf("err = %v, want the connect-time ask", err)
	}
	return ask
}

// TestTwoProfilesToOneHostResolveTheHostsOneMode is section 5.10's lifecycle
// scenario 1 at the open path: two connections to ONE host, opened with two
// profiles, resolve the mode the host keeps — the same one for both — because
// the mode is the host's and a connection has no copy to disagree with.
//
// Both halves are here: a host recorded script opens as script for both
// profiles and brings up no helper lane, and a host recorded helper with a
// granted machine is served by the far helper for both.
func TestProbeFailureKeepsRecordedHostMode(t *testing.T) {
	for _, mode := range []hostmode.Mode{hostmode.Raw, hostmode.Script} {
		t.Run(string(mode), func(t *testing.T) {
			provider := &fakeLaneProvider{owner: t.Name(), uname: "malformed"}
			store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
			r := newTestRegistry(t, provider, store)
			mustRecordHostMode(t, r.modes, "SHA256:fake", mode)
			cfg := remoteConfigFor()
			_, hold, selected, err := r.openHoldingLease(context.Background(), cfg, "claim-probe-failure")
			hold.release()
			if err != nil || selected {
				t.Fatalf("open selected=%v err=%v, want the local opener after failed uname", selected, err)
			}
			if got := cfg.Remote.IntegrationMode; got != string(mode) {
				t.Fatalf("mode after failed uname = %q, want recorded host method %q", got, mode)
			}
		})
	}
}

func TestTwoProfilesToOneHostResolveTheHostsOneMode(t *testing.T) {
	t.Run("the host's script answers both profiles", func(t *testing.T) {
		provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeer(t)}
		store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
		r := newTestRegistry(t, provider, store)
		mustRecordHostMode(t, r.modes, "SHA256:fake", hostmode.Script)

		for _, claim := range []string{"claim-a", "claim-b"} {
			cfg := remoteConfigFor()
			opened, hold, selected, err := r.openHoldingLease(context.Background(), cfg, claim)
			hold.release()
			if err != nil {
				t.Fatalf("%s: openHoldingLease: %v", claim, err)
			}
			if selected || opened.Session != nil {
				t.Fatalf("%s: selected=%v session=%v, want neither: the host is recorded script", claim, selected, opened.Session)
			}
			if got := cfg.Remote.IntegrationMode; got != string(hostmode.Script) {
				t.Fatalf("%s: the open's mode = %q, want the host's script — every later reader of this config "+
					"(the shell-script gate, the open ack, the integration axis) takes the mode from it", claim, got)
			}
		}
		// ONE MODE MEANS ONE ANSWER ABOUT THE HELPER: neither open installed
		// or dialled one, and the host's consent was never granted.
		if got := provider.laneCount(); got != 0 {
			t.Errorf("%d helper lanes were brought up, want 0 for a host recorded script", got)
		}
	})

	t.Run("the host's helper answers both profiles", func(t *testing.T) {
		provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeerWithSpawner(t)}
		store := seedGrantedDocument(t, t.TempDir(), "SHA256:fake")
		r := newTestRegistry(t, provider, store)
		mustRecordHostMode(t, r.modes, "SHA256:fake", hostmode.Helper)

		for _, claim := range []string{"claim-a", "claim-b"} {
			cfg := remoteConfigFor()
			opened, hold, selected, err := r.openHoldingLease(context.Background(), cfg, claim)
			hold.release()
			if err != nil {
				t.Fatalf("%s: openHoldingLease: %v", claim, err)
			}
			if provider.installPin != "SHA256:fake" {
				t.Errorf("install pin = %q, want consent-approved key", provider.installPin)
			}
			if !selected || opened.Session == nil {
				t.Fatalf("%s: selected=%v session=%v, want the far helper serving this host", claim, selected, opened.Session)
			}
			if got := cfg.Remote.IntegrationMode; got != string(hostmode.Helper) {
				t.Fatalf("%s: the open's mode = %q, want the host's helper", claim, got)
			}
		}
	})
}

// TestAHostWithNoRecordAsksAndAnsweringRecordsIt is the paired normal case: the
// question exists, and the answer lands where the next open reads it. It is one
// test because the pairing is the property — an ask that records nothing is a
// dialog raised on every connect forever.
func TestAHostWithNoRecordAsksAndAnsweringRecordsIt(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeerWithSpawner(t)}
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	r := newTestRegistry(t, provider, store)

	if _, ok := r.modes.Lookup("SHA256:fake"); ok {
		t.Fatal("the stand starts with a host record; this test is about no record")
	}

	_, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-1")
	hold.release()
	if !selected {
		t.Fatal("selected = false, want the ask before the open proceeds")
	}
	ask := askOf(t, err)
	if ask.Cause != nil {
		t.Fatalf("ask.Cause = %v, want nil — the probe dial succeeded", ask.Cause)
	}
	if mode, ok := r.modes.Lookup(ask.Fingerprint); ok {
		t.Fatalf("the ask already recorded %q; nothing may be written before the person answers", mode)
	}

	// THE ANSWER, through the same store call connections.setIntegrationMethod
	// makes.
	mustRecordHostMode(t, r.modes, ask.Fingerprint, hostmode.Script)

	mode, ok := r.modes.Lookup(ask.Fingerprint)
	if !ok || mode != hostmode.Script {
		t.Fatalf("the answer was not found by its fingerprint: %q/%v", mode, ok)
	}
}

// TestAnsweringTheAskOnceStopsTheSecondOpenAsking is ADR-0034's "one machine,
// one answer" carried to the method: the ask is raised once per host, and a
// SECOND connection to it — from another profile, storing a mode that would
// itself have asked (ADR-0105) — is silent.
func TestAnsweringTheAskOnceStopsTheSecondOpenAsking(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeer(t)}
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	r := newTestRegistry(t, provider, store)

	_, hold, _, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-1")
	hold.release()
	ask := askOf(t, err)
	mustRecordHostMode(t, r.modes, ask.Fingerprint, hostmode.Raw)

	cfg := remoteConfigFor()
	opened, hold2, selected, err := r.openHoldingLease(context.Background(), cfg, "claim-2")
	hold2.release()
	if selected || opened.Session != nil {
		t.Fatalf("selected=%v session=%v — a helper-stored profile must not be asked a second time for this host", selected, opened.Session)
	}
	if err != nil {
		t.Fatalf("the second open raised %v, want no ask at all — the host has answered", err)
	}
	if got := cfg.Remote.IntegrationMode; got != string(hostmode.Raw) {
		t.Fatalf("the second open's mode = %q, want the host's raw", got)
	}
}

// refusingDocumentStore is a disk that answers no: reads find nothing and
// writes fail. It is the shape of the failure assertion 4 is about.
type refusingDocumentStore struct{}

func (refusingDocumentStore) Read(string, any) (bool, error) { return false, nil }
func (refusingDocumentStore) Write(string, any) error        { return errors.New("disk is full") }
func (refusingDocumentStore) Delete(string) error            { return errors.New("disk is full") }

// TestAFailedHostRecordWriteStillAnswersThisOpen is assertion 4's open-path
// half: the store cannot persist the answer, and the ask is answered anyway —
// the second open does not ask again, and the host's mode is in force. What the
// person is TOLD about it is the transport's response and the pane head
// (ws_integration_method_test.go, connection-head.test.tsx); what is proven
// here is that the product never falls back into the dialog because a disk was
// full.
func TestAFailedHostRecordWriteStillAnswersThisOpen(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeer(t)}
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	r := newTestRegistry(t, provider, store)
	r.modes = hostmode.NewStore(log.NewSlogAdapter(discardLogger(t)), refusingDocumentStore{}, "host-modes.json")

	_, hold, _, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-1")
	hold.release()
	ask := askOf(t, err)

	if err = r.modes.Set(ask.Fingerprint, hostmode.Script); !errors.Is(err, hostmode.ErrNotSaved) {
		t.Fatalf("Set on a store that cannot write = %v, want ErrNotSaved", err)
	}

	cfg := remoteConfigFor()
	_, hold2, selected, err := r.openHoldingLease(context.Background(), cfg, "claim-2")
	hold2.release()
	if err != nil || selected {
		t.Fatalf("the second open selected=%v err=%v, want the remembered answer and no ask", selected, err)
	}
	if got := cfg.Remote.IntegrationMode; got != string(hostmode.Script) {
		t.Fatalf("the second open's mode = %q, want the answer kept for this run (script)", got)
	}
}

// TestTheEditorsChoiceIsWhatTheNextOpenUses is assertion 1's backend half:
// changing the method in the connection editor changes what the NEXT open of
// that host does, and choosing auto again makes the open ask.
//
// It joins the two halves the editor is made of — the host record's write (the
// same store call connections.setHostMode makes, tested over the wire in
// ws_host_mode_test.go) and the open path's read — through the identity both
// use: the host-key fingerprint. The probe reports the fingerprint its own
// known_hosts entry answers, which is what a real dial's verification produces.
// The alternative, a test that only drove the store, would prove a value can be
// written and not that an open ever reads it.
func TestTheEditorsChoiceIsWhatTheNextOpenUses(t *testing.T) {
	const fingerprint = "SHA256:editor"
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeer(t), fingerprint: fingerprint}
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	r := newTestRegistry(t, provider, store)

	// Nothing is recorded yet: the host is auto, which is the ask.
	_, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-auto")
	hold.release()
	if !selected {
		t.Fatal("selected = false, want the connect-time ask for a host nobody answered")
	}
	if got := askOf(t, err).Fingerprint; got != fingerprint {
		t.Fatalf("ask fingerprint = %q, want the key the dial observed (%q)", got, fingerprint)
	}

	// The editor writes raw for this host.
	mustRecordHostMode(t, r.modes, fingerprint, hostmode.Raw)
	rawCfg := remoteConfigFor()
	opened, hold, selected, err := r.openHoldingLease(context.Background(), rawCfg, "claim-raw")
	hold.release()
	if err != nil {
		t.Fatalf("open after the editor wrote raw: %v", err)
	}
	if selected || opened.Session != nil {
		t.Fatalf("selected=%v session=%v, want the ordinary local-helper open with no ask", selected, opened.Session)
	}
	if got := rawCfg.Remote.IntegrationMode; got != string(hostmode.Raw) {
		t.Fatalf("the open's mode = %q, want the editor's raw", got)
	}
	if got := provider.laneCount(); got != 0 {
		t.Errorf("%d helper lanes were brought up, want 0 for a host the editor set to raw", got)
	}

	// The editor answers script instead, and the NEXT open is script.
	mustRecordHostMode(t, r.modes, fingerprint, hostmode.Script)
	scriptCfg := remoteConfigFor()
	_, hold, selected, err = r.openHoldingLease(context.Background(), scriptCfg, "claim-script")
	hold.release()
	if err != nil || selected {
		t.Fatalf("open after the editor wrote script: selected=%v err=%v, want an unanswered-ask-free script open", selected, err)
	}
	if got := scriptCfg.Remote.IntegrationMode; got != string(hostmode.Script) {
		t.Fatalf("the open's mode = %q, want the editor's script", got)
	}

	// Auto is the absence of a record, so the next open asks again.
	if err = r.modes.PersistMany([]string{fingerprint}, hostmode.Auto); err != nil {
		t.Fatalf("the editor's auto: %v", err)
	}
	_, hold, selected, err = r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-auto-again")
	hold.release()
	if !selected {
		t.Fatal("selected = false, want the ask once the editor chose auto")
	}
	_ = askOf(t, err)
}

type failingHostModeWrites struct{ storage.DocumentStore }

func (f failingHostModeWrites) Write(string, any) error { return errors.New("disk is full") }

func TestFailedHostModeWriteReopensPreviouslySavedMethod(t *testing.T) {
	logger := log.NewSlogAdapter(discardLogger(t))
	doc := storage.NewDocumentStore(t.TempDir())
	modes := hostmode.NewStore(logger, doc, "host-modes.json")
	mustRecordHostMode(t, modes, "SHA256:fake", hostmode.Raw)
	modes = hostmode.NewStore(logger, failingHostModeWrites{doc}, "host-modes.json")
	if err := modes.Set("SHA256:fake", hostmode.Script); !errors.Is(err, hostmode.ErrNotSaved) {
		t.Fatalf("write = %v", err)
	}
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeer(t)}
	r := newTestRegistry(t, provider, nil)
	r.modes = modes
	cfg := remoteConfigFor()
	_, hold, selected, err := r.openHoldingLease(context.Background(), cfg, "volatile")
	hold.release()
	if err != nil || selected || cfg.Remote.IntegrationMode != string(hostmode.Script) {
		t.Fatalf("current run: selected=%v err=%v mode=%q", selected, err, cfg.Remote.IntegrationMode)
	}
	r.modes = hostmode.NewStore(logger, doc, "host-modes.json")
	cfg = remoteConfigFor()
	_, hold, selected, err = r.openHoldingLease(context.Background(), cfg, "reopened")
	hold.release()
	if err != nil || selected || cfg.Remote.IntegrationMode != string(hostmode.Raw) {
		t.Fatalf("reopened: selected=%v err=%v mode=%q", selected, err, cfg.Remote.IntegrationMode)
	}
}

func TestHelperInstallPinMismatchWritesNothing(t *testing.T) {
	provider := &fakeLaneProvider{owner: t.Name(), peer: realHelperPeer(t), installFail: refusalWith("host-key-pin-mismatch", nil)}
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:fake")
	r := newTestRegistry(t, provider, store)
	mustRecordHostMode(t, r.modes, "SHA256:fake", hostmode.Helper)
	_, hold, _, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "replacement")
	hold.release()
	if !hostKeyPinMismatch(err) {
		t.Fatalf("install failure lost pin mismatch: %v", err)
	}
	if provider.installPin != "SHA256:fake" || provider.install != nil || provider.laneCount() != 0 {
		t.Fatalf("install reached replacement host: pin=%q fs=%v lanes=%d", provider.installPin, provider.install, provider.laneCount())
	}
}
