package app

// The connect-time helper ask (ADR-0068, owner's decision 2026-09-16):
// openHoldingLease is where a connection whose machine has not answered about
// the helper is asked, instead of silently proceeding without it — for
// silence (`auto`) and, since ADR-0105, for a connection that names `helper`
// as its method too (nocx-1kejz.3). It is also where, when the SAME dial
// discovered an unknown or changed host key, the two questions travel in one
// refusal rather than two dialogs in sequence.
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
	"github.com/shady2k/nocx/internal/hostmode"
	"github.com/shady2k/nocx/internal/log"
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
func realHelperPeerWithSpawner(t testing.TB) func(in io.Reader, out io.Writer) int {
	contentHash := syntheticArtifactHash
	logger := discardLogger(t)
	daemon := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(contentHash),
		Spawner:    helpersession.NewLocalSpawner(logger, helpersession.Shell{Path: "/bin/sh"}, ""),
		Log:        logger,
	})
	// The peer IS this test's helper process, so its sessions end when the
	// test does — the real binary's own exit (nocx-1ebrs.66.1.29).
	t.Cleanup(daemon.Close)
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

// remoteConfigFor builds the session.Config openHoldingLease reads: a remote
// destination with the launcher/installer options SSHOptionsFromConfig
// round-trips through opts back to the provider. It carries no integration
// method: a saved connection has none (ADR-727g63tr decision 1), and the open
// path stamps the host's answer once the probe reports its key.
func remoteConfigFor() session.Config {
	return session.Config{
		Kind:   session.KindRemote,
		Host:   "host.example",
		Remote: &ssh.ConnectConfig{User: "test"},
	}
}

// newTestHostModes is an empty host record — every host not yet answered,
// which is what the compositor's own store starts as. A test that wants a
// recorded mode calls Set on it (the same call connections.setIntegrationMethod
// makes), so the store under test is the production one.
func newTestHostModes(t *testing.T) *hostmode.Store {
	t.Helper()
	return hostmode.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "host-modes.json")
}

func newTestRegistry(t *testing.T, provider *fakeLaneProvider, store *consent.Store) *helperRegistry {
	t.Helper()
	reg := &helperRegistry{
		lanes:           provider,
		install:         provider,
		source:          stubArtifacts(t),
		log:             discardLogger(t),
		consent:         store,
		modes:           newTestHostModes(t),
		registry:        session.New(log.NewSlogAdapter(discardLogger(t)), &reachPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(discardLogger(t)))}),
		hosts:           make(map[session.ID]*hostedShellCarrier),
		pendingReadopts: make(map[session.ID]*hostedShellCarrier),
		closing:         make(map[string]struct{}),
	}
	// The stand is a coordinator, so its process ends with the test
	// (nocx-1ebrs.66.1.29).
	endStandOnCleanup(t, provider, reg)
	return reg
}

// TestOpenHoldingLease_AutoUnansweredAsksInsteadOfFallingThrough is the bug
// ADR-0068 exists to fix, pinned at the connect-time seam directly: before
// this, ConsentRequired was treated exactly like Refused and the destination
// fell through to a silent script-tier open, so a host whose key was already
// trusted was never asked, on any connect, ever.
//
// Since ADR-727g63tr decision 1 the mode is read from the HOST record, so the
// connection's stored mode no longer decides whether this asks — a host nobody
// answered for is auto (ADR-0033), whatever the profile says.
func TestOpenHoldingLease_AutoUnansweredAsksInsteadOfFallingThrough(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer(t)}
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	r := newTestRegistry(t, provider, store)

	opened, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-1")
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
	provider := &fakeLaneProvider{peer: realHelperPeerWithSpawner(t)}
	// fakeProbeConn.HostKeyFingerprint is the fixed "SHA256:fake" (helper_git_test.go).
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:fake")
	r := newTestRegistry(t, provider, store)

	opened, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-1")
	defer hold.release()

	if err != nil {
		t.Fatalf("openHoldingLease: %v", err)
	}
	if !selected || opened.Session == nil {
		t.Fatalf("selected=%v session=%v, want a helper-hosted session with no ask", selected, opened.Session)
	}
}

// TestOpenHoldingLease_GrantedMachineRetainsItsFingerprint pins the hosted
// attachment's identity: subsequent consent and host settings reads use the
// verified host key even though the helper, not the coordinator, dialed it.
func TestOpenHoldingLease_GrantedMachineRetainsItsFingerprint(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeerWithSpawner(t)}
	source := stubArtifacts(t)
	// fakeProbeConn.HostKeyFingerprint is the fixed "SHA256:fake" (helper_git_test.go).
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:fake")
	reg := newHostedShellRegistry(provider, source, store, discardLogger(t))
	endStandOnCleanup(t, provider, reg)
	reg.registry = session.New(log.NewSlogAdapter(discardLogger(t)), &reachPTYFactory{stub: pty.NewStub(log.NewSlogAdapter(discardLogger(t)))})

	opened, hold, selected, err := reg.openHoldingLease(context.Background(), remoteConfigFor(), "claim-1")
	defer hold.release()
	if err != nil {
		t.Fatalf("openHoldingLease: %v", err)
	}
	if !selected || opened.Session == nil {
		t.Fatalf("selected=%v session=%v, want a helper-hosted session", selected, opened.Session)
	}

	if got := opened.Session.HostKeyFingerprint(); got != "SHA256:fake" {
		t.Errorf("the opened session's HostKeyFingerprint() = %q, want the granted fingerprint — "+
			"git.open reads this same session and must find the answer this open already acted on", got)
	}
}

// TestOpenHoldingLease_AHostWithNoRecordAsksBeforeAnythingIsInstalled is
// nocx-1kejz.3 and ADR-727g63tr decision 1 at the connect-time seam: nothing a
// connection says can deploy a binary on a machine nobody answered for,
// because a connection says nothing about the method any more. A host with no
// record meets the ask, and no lane, install or session exists before it.
func TestOpenHoldingLease_AHostWithNoRecordAsksBeforeAnythingIsInstalled(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer(t)}
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	r := newTestRegistry(t, provider, store)

	opened, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-1")
	defer hold.release()

	if !selected {
		t.Fatal("selected = false, want true — a host with no record must not fall through to the local opener silently")
	}
	if opened.Session != nil {
		t.Fatal("a session was opened before the host's answer")
	}
	var ask *transport.ErrHelperConsentNeeded
	if !errors.As(err, &ask) {
		t.Fatalf("err = %v, want *transport.ErrHelperConsentNeeded", err)
	}
	if ask.Fingerprint == "" {
		t.Error("ask carries no fingerprint — the answer is keyed by it and has nowhere else to go")
	}
	if ask.Cause != nil {
		t.Errorf("ask.Cause = %v, want nil — the probe dial succeeded, so the key is already trusted", ask.Cause)
	}
	// The interval this test is about: nothing is installed, and nothing is
	// spawned, until a person answers. A lane here would be a binary deployed
	// on somebody else's machine without permission.
	if got := provider.laneCount(); got != 0 {
		t.Errorf("the ask brought up %d helper lanes, want 0", got)
	}
}

// TestOpenHoldingLease_AHostRecordedHelperIsServedSilently: the other half —
// a host recorded helper whose consent is granted behaves exactly as it did,
// silently, with no ask and a helper-hosted session, whatever the connection
// says.
func TestOpenHoldingLease_AHostRecordedHelperIsServedSilently(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeerWithSpawner(t)}
	// fakeProbeConn.HostKeyFingerprint is the fixed "SHA256:fake" (helper_git_test.go).
	store := seedGrantedDocument(t, t.TempDir(), "SHA256:fake")
	r := newTestRegistry(t, provider, store)
	mustRecordHostMode(t, r.modes, "SHA256:fake", hostmode.Helper)

	opened, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-1")
	defer hold.release()

	if err != nil {
		t.Fatalf("openHoldingLease: %v", err)
	}
	if !selected || opened.Session == nil {
		t.Fatalf("selected=%v session=%v, want a helper-hosted session with no ask", selected, opened.Session)
	}
}

// TestOpenHoldingLease_AHostRecordedRawNeverAsks: the mode is checked before
// any consent lookup (D8), and since ADR-727g63tr decision 1 the mode is the
// host's — so a host recorded raw is never asked about the helper, even when
// the connection's own stored mode is helper.
func TestOpenHoldingLease_AHostRecordedRawNeverAsks(t *testing.T) {
	provider := &fakeLaneProvider{peer: realHelperPeer(t)}
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	r := newTestRegistry(t, provider, store)
	mustRecordHostMode(t, r.modes, "SHA256:fake", hostmode.Raw)

	_, hold, selected, err := r.openHoldingLease(context.Background(), remoteConfigFor(), "claim-1")
	defer hold.release()

	if selected {
		t.Fatal("a host recorded raw was selected by the remote-helper route")
	}
	var ask *transport.ErrHelperConsentNeeded
	if errors.As(err, &ask) {
		t.Fatal("a host recorded raw was asked about the helper")
	}
}

// TestHelperConsentAskForProbeFailure_UnknownKeyAsksOnce: an auto
// connection's first contact with an unknown host key gets ONE refusal
// carrying both questions.
func TestHelperConsentAskForProbeFailure_UnknownKeyAsksOnce(t *testing.T) {
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	modes := newTestHostModes(t)
	probeErr := &ssh.ErrUnknownHostKey{Addr: "h:22", Fingerprint: "SHA256:offered", KeyAlgo: "ssh-ed25519"}

	err := helperConsentAskForProbeFailure("ssh:prod-db", "h:22", offeredHostKeyFingerprint(probeErr), probeErr, store, modes)
	var ask *transport.ErrHelperConsentNeeded
	if !errors.As(err, &ask) {
		t.Fatalf("err = %v, want *transport.ErrHelperConsentNeeded", err)
	}
	if ask.Fingerprint != "SHA256:offered" {
		t.Errorf("fingerprint = %q, want the offered one", ask.Fingerprint)
	}
	if ask.ProfileID != "ssh:prod-db" {
		t.Errorf("profileId = %q, want the connection the open named", ask.ProfileID)
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
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	modes := newTestHostModes(t)
	probeErr := &ssh.ErrHostKeyMismatch{Addr: "h:22", Fingerprint: "SHA256:new", Expected: "SHA256:old"}

	err := helperConsentAskForProbeFailure("ssh:prod-db", "h:22", offeredHostKeyFingerprint(probeErr), probeErr, store, modes)
	var ask *transport.ErrHelperConsentNeeded
	if !errors.As(err, &ask) {
		t.Fatalf("err = %v, want *transport.ErrHelperConsentNeeded", err)
	}
	if ask.Fingerprint != "SHA256:new" {
		t.Errorf("fingerprint = %q, want the offered (new) one", ask.Fingerprint)
	}
}

// TestHelperConsentAskForProbeFailure_AHostRecordedRawOrScriptNeverAsks: raw
// and script are answers, not gaps (D8), and the record is where the host gave
// them — so a probe failure on such a host must never raise the connect-time
// ask, even for a host-key failure the local known_hosts has not seen.
func TestHelperConsentAskForProbeFailure_AHostRecordedRawOrScriptNeverAsks(t *testing.T) {
	probeErr := &ssh.ErrUnknownHostKey{Addr: "h:22", Fingerprint: "SHA256:offered"}
	for _, recorded := range []hostmode.Mode{hostmode.Raw, hostmode.Script} {
		t.Run(string(recorded), func(t *testing.T) {
			store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
			modes := newTestHostModes(t)
			mustRecordHostMode(t, modes, "SHA256:offered", recorded)

			if err := helperConsentAskForProbeFailure("ssh:prod-db", "h:22", offeredHostKeyFingerprint(probeErr), probeErr, store, modes); err != nil {
				t.Errorf("a host recorded %q: err = %v, want nil — it answered, so it may not be asked", recorded, err)
			}
		})
	}
}

// TestHelperConsentAskForProbeFailure_AHostWithNoRecordAsks is ADR-0105 at the
// host-key-failure seam (nocx-1kejz.3): a host nobody has answered for is
// asked, and the one refusal carries both questions. A stored `helper` on the
// connection does not make the host answered, and neither does anything else.
func TestHelperConsentAskForProbeFailure_AHostWithNoRecordAsks(t *testing.T) {
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	modes := newTestHostModes(t)
	probeErr := &ssh.ErrUnknownHostKey{Addr: "h:22", Fingerprint: "SHA256:offered", KeyAlgo: "ssh-ed25519"}

	err := helperConsentAskForProbeFailure("ssh:prod-db", "h:22", offeredHostKeyFingerprint(probeErr), probeErr, store, modes)
	var ask *transport.ErrHelperConsentNeeded
	if !errors.As(err, &ask) {
		t.Fatalf("err = %v, want *transport.ErrHelperConsentNeeded — a host with no record is auto, and auto is the ask", err)
	}
	if ask.Fingerprint != "SHA256:offered" {
		t.Errorf("fingerprint = %q, want the offered one", ask.Fingerprint)
	}
}

// TestHelperConsentAskForProbeFailure_NonHostKeyFailureNeverAsks: a probe
// that failed for an unrelated reason (unreachable, no auth material) must
// not raise the ask — the existing local-opener fallback answers those on
// its own, as it always has.
func TestHelperConsentAskForProbeFailure_NonHostKeyFailureNeverAsks(t *testing.T) {
	store := consent.NewStore(log.NewSlogAdapter(discardLogger(t)), storage.NewDocumentStore(t.TempDir()), "consent.json")
	modes := newTestHostModes(t)
	if err := helperConsentAskForProbeFailure("ssh:prod-db", "h:22", "", errors.New("dial tcp: connection refused"), store, modes); err != nil {
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
	modes := newTestHostModes(t)
	probeErr := &ssh.ErrUnknownHostKey{Addr: "h:22", Fingerprint: "SHA256:offered"}

	if err := helperConsentAskForProbeFailure("ssh:prod-db", "h:22", offeredHostKeyFingerprint(probeErr), probeErr, store, modes); err != nil {
		t.Errorf("err = %v, want nil — this fingerprint already has an answer", err)
	}
}
