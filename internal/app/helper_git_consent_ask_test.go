package app

// The connect-time helper ask (ADR-0068, owner's decision 2026-09-16):
// openHoldingLease is where a fresh auto connection is asked about the
// helper, instead of silently proceeding without it — and where, when the
// SAME dial also discovered an unknown or changed host key, the two
// questions travel in one refusal rather than two dialogs in sequence.
//
// These tests exercise the decision at the seam helperGitFactory's own
// tests already use (fakeLaneProvider, stubArtifacts): openHoldingLease
// itself, and the pure functions it delegates to for the host-key-failure
// half (helperConsentAskForProbeFailure, offeredHostKeyFingerprint). The
// wire encoding of what these return is proved separately, over the real
// socket, in internal/transport/ws_contract_test.go.

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/git/hostsvc"
	localgit "github.com/shady2k/nocx/internal/git/local"
	"github.com/shady2k/nocx/internal/helper/consent"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage"
	"github.com/shady2k/nocx/internal/transport"
)

// realHelperPeerWithSpawner is realHelperPeer (helper_git_test.go) plus a
// real Spawner: openHoldingLease's DesiredHelper branch does not stop at the
// git bridge lane the way helperGitFactory's own tests do — it spawns the
// session on the far helper too, which realHelperPeer's bare
// helpersession.New (no Spawner) panics on. TestHelperSessionsRedialsAfterCarrierLoss
// is the existing precedent for wiring one.
func realHelperPeerWithSpawner() func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	logger := discardLogger()
	daemon := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(contentHash),
		Spawner:    helpersession.NewLocalSpawner(logger, helpersession.Shell{Path: "/bin/sh"}, ""),
		Log:        logger,
	})
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, contentHash, "instance-1", logger)
		h.Register(hostsvc.New(localgit.NewFactory()))
		h.Register(daemon)
		release := daemon.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// remoteConfigFor builds the session.Config openHoldingLease reads: a
// remote destination at the given mode, with the launcher/installer options
// SSHOptionsFromConfig round-trips through opts back to the provider.
func remoteConfigFor(mode profile.DesiredMode) session.Config {
	return session.Config{
		Kind: session.KindRemote,
		Host: "host.example",
		Remote: &ssh.ConnectConfig{
			User:        "test",
			DesiredMode: string(mode),
		},
	}
}

func newTestRegistry(t *testing.T, provider *fakeLaneProvider, store *consent.Store) *helperRegistry {
	t.Helper()
	return &helperRegistry{
		lanes:    provider,
		install:  provider,
		source:   stubArtifacts(t),
		log:      discardLogger(),
		consent:  store,
		registry: session.New(log.NewSlogAdapter(discardLogger()), &reachPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(discardLogger()))}),
		hosts:    make(map[session.ID]*hostHelper),
		closing:  make(map[string]struct{}),
	}
}

// TestOpenHoldingLease_AutoUnansweredAsksInsteadOfFallingThrough is the bug
// ADR-0068 exists to fix, pinned at the connect-time seam directly: before
// this, ConsentRequired was treated exactly like Refused and the destination
// fell through to a silent script-tier open, so an auto connection whose
// host key was already trusted was never asked, on any connect, ever.
func TestOpenHoldingLease_AutoUnansweredAsksInsteadOfFallingThrough(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	r := newTestRegistry(t, provider, store)

	opened, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(profile.DesiredAuto), "claim-1")
	defer hold.release()

	if !selected {
		t.Fatal("selected = false, want true — a consent-required outcome must not fall through to the local opener silently")
	}
	if opened.Session != nil {
		t.Fatal("a session was opened before the ask was answered")
	}
	var ask *transport.ErrHelperConsentNeeded
	if !errors.As(err, &ask) {
		t.Fatalf("err = %v, want *transport.ErrHelperConsentNeeded", err)
	}
	if ask.Fingerprint == "" {
		t.Error("ask carries no fingerprint — the renderer has nothing to key connections.helperConsent on")
	}
	if ask.Cause != nil {
		t.Errorf("ask.Cause = %v, want nil — the probe dial succeeded, so the key is already trusted", ask.Cause)
	}
	// Nothing was written: the ask is a result state, never an install.
	if got := provider.laneCount(); got != 0 {
		t.Errorf("the ask brought up %d helper lanes, want 0", got)
	}
}

// TestOpenHoldingLease_GrantedResolvesWithoutAsking: once the store answers
// the fingerprint the probe reports, the SAME connection resolves silently
// to the helper — the second half of "asked exactly once per fingerprint".
func TestOpenHoldingLease_GrantedResolvesWithoutAsking(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeerWithSpawner()}
	// fakeProbeConn.HostKeyFingerprint is the fixed "SHA256:fake" (helper_git_test.go).
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:fake")
	r := newTestRegistry(t, provider, store)

	opened, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(profile.DesiredAuto), "claim-1")
	defer hold.release()

	if err != nil {
		t.Fatalf("openHoldingLease: %v", err)
	}
	if !selected || opened.Session == nil {
		t.Fatalf("selected=%v session=%v, want a helper-hosted session with no ask", selected, opened.Session)
	}
}

// TestOpenHoldingLease_ExplicitRawNeverAsks: an explicit raw connection
// never reaches the ask, whatever the store holds — mode is checked before
// any consent lookup (D8, unchanged by this bead).
func TestOpenHoldingLease_ExplicitRawNeverAsks(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer()}
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	r := newTestRegistry(t, provider, store)

	_, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(profile.DesiredRaw), "claim-1")
	defer hold.release()

	if selected {
		t.Fatal("an explicit raw connection was selected by the remote-helper route")
	}
	var ask *transport.ErrHelperConsentNeeded
	if errors.As(err, &ask) {
		t.Fatal("an explicit raw connection was asked about the helper")
	}
}

// TestHelperConsentAskForProbeFailure_UnknownKeyAsksOnce: an auto
// connection's first contact with an unknown host key gets ONE refusal
// carrying both questions.
func TestHelperConsentAskForProbeFailure_UnknownKeyAsksOnce(t *testing.T) {
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	probeErr := &ssh.ErrUnknownHostKey{Addr: "h:22", Fingerprint: "SHA256:offered", KeyAlgo: "ssh-ed25519"}

	err := helperConsentAskForProbeFailure("h:22", profile.DesiredAuto, probeErr, store)
	var ask *transport.ErrHelperConsentNeeded
	if !errors.As(err, &ask) {
		t.Fatalf("err = %v, want *transport.ErrHelperConsentNeeded", err)
	}
	if ask.Fingerprint != "SHA256:offered" {
		t.Errorf("fingerprint = %q, want the offered one", ask.Fingerprint)
	}
	if !errors.Is(ask.Cause, error(probeErr)) && ask.Cause != probeErr {
		t.Errorf("ask.Cause = %v, want the original host-key error so the renderer's host-key evidence can be built from it", ask.Cause)
	}
}

// TestHelperConsentAskForProbeFailure_ChangedKeyAsksToo mirrors the unknown
// case for a rotated key: the fingerprint a changed-key error carries is
// just as deterministic as an unknown one's, and the ask must not require
// the key to already be unchanged to be raised.
func TestHelperConsentAskForProbeFailure_ChangedKeyAsksToo(t *testing.T) {
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	probeErr := &ssh.ErrHostKeyMismatch{Addr: "h:22", Fingerprint: "SHA256:new", Expected: "SHA256:old"}

	err := helperConsentAskForProbeFailure("h:22", profile.DesiredAuto, probeErr, store)
	var ask *transport.ErrHelperConsentNeeded
	if !errors.As(err, &ask) {
		t.Fatalf("err = %v, want *transport.ErrHelperConsentNeeded", err)
	}
	if ask.Fingerprint != "SHA256:new" {
		t.Errorf("fingerprint = %q, want the offered (new) one", ask.Fingerprint)
	}
}

// TestHelperConsentAskForProbeFailure_ExplicitModeNeverAsks: script/helper/
// raw are answers, not gaps (D8) — a probe failure on an explicitly-moded
// connection must never raise the connect-time ask, even for a host-key
// failure.
func TestHelperConsentAskForProbeFailure_ExplicitModeNeverAsks(t *testing.T) {
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	probeErr := &ssh.ErrUnknownHostKey{Addr: "h:22", Fingerprint: "SHA256:offered"}

	for _, mode := range []profile.DesiredMode{profile.DesiredRaw, profile.DesiredScript, profile.DesiredHelper} {
		if err := helperConsentAskForProbeFailure("h:22", mode, probeErr, store); err != nil {
			t.Errorf("mode %q: err = %v, want nil — an explicit mode is never asked", mode, err)
		}
	}
}

// TestHelperConsentAskForProbeFailure_NonHostKeyFailureNeverAsks: a probe
// that failed for an unrelated reason (unreachable, no auth material) must
// not raise the ask — the existing local-opener fallback answers those on
// its own, as it always has.
func TestHelperConsentAskForProbeFailure_NonHostKeyFailureNeverAsks(t *testing.T) {
	store := consent.NewStore(log.NewSlogAdapter(discardLogger()), storage.NewDocumentStore(t.TempDir()), "consent.json")
	if err := helperConsentAskForProbeFailure("h:22", profile.DesiredAuto, errors.New("dial tcp: connection refused"), store); err != nil {
		t.Errorf("err = %v, want nil for a non-host-key probe failure", err)
	}
}

// TestHelperConsentAskForProbeFailure_AlreadyAnsweredFingerprintNeverAsksAgain
// is ADR-0034's "one machine, one answer" at the seam that could otherwise
// double-ask it: a fingerprint already recorded through some OTHER route to
// the same machine must not be asked again just because THIS route's local
// known_hosts entry has not seen the key yet.
func TestHelperConsentAskForProbeFailure_AlreadyAnsweredFingerprintNeverAsksAgain(t *testing.T) {
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:offered")
	probeErr := &ssh.ErrUnknownHostKey{Addr: "h:22", Fingerprint: "SHA256:offered"}

	if err := helperConsentAskForProbeFailure("h:22", profile.DesiredAuto, probeErr, store); err != nil {
		t.Errorf("err = %v, want nil — this fingerprint already has an answer", err)
	}
}
