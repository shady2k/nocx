//go:build nocx_local_ssh

package app

// ONE PANE COSTS THE FAR HOST ONE AUTHENTICATION (nocx-k6p18.35).
//
// WHAT A USER CAN DO THAT THEY COULD NOT BEFORE: open a pane on a host whose
// only credential is a password stored in the vault, and cost that host ONE
// login for it — and keep it at one when the Files panel beside it reads the
// same host.
//
// # The defect this pins
//
// The open path asks the far host's own helper first, and it begins by PROBING
// that host for its platform (does it have a helper we could install?). A probe
// is a DIAL: it authenticates to ask one question. Its lease was released as
// soon as the selection declined — which for a host that ships no helper is the
// ordinary answer — and that release was the pool's last reference, so the
// connection closed. The pane's own `spawn-ssh` a few milliseconds later dialed
// and authenticated again. The epic's e2e counted the consequence on a real
// host: two logins for one pane plus one Files panel (`cmd/e2e-sshd`,
// /tmp/e61-run5.log).
//
// The fix is an interval rather than a retry: the probe's pooled reference is
// held until the open that follows has taken a reference of its own — the
// pane's shell channel, which lives as long as the session — and released
// immediately after (probeHold, probeHelperPlatformHeld, and the deferred
// release in hostedOpeners.OpenHosted; each names both ends).
//
// # What is real here and what is scripted
//
// Real: this machine's helper (the host protocol over a socketpair, the ssh
// service, the session service and the real ssh spawner), the coordinator's
// client to it, the coordinator's own resolver, the leases beside the pane, and
// the open seam ITSELF — hostedOpeners, the value the composition root hands
// the transport.
//
// Scripted, and only: the far side's answers (pwSSHServer, an in-process sshd)
// and the coordinator's answers to the helper's questions
// (helperReverseHandlers, exactly as app.go builds them).
//
// The count is the SERVER's, and it is the server's because "how many times did
// somebody log in" is not a fact a client can report about itself: the fixture
// counts the connections that finished the auth exchange (pwSSHServer.connCount).

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/profile"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/waittest"
)

// ── the app-level pieces ──────────────────────────────────────────────────

// openSeam is the open path as the composition root builds it: the helper
// registry (the far host's own helper) behind the dispatch that this machine's
// opener answers for everything else.
//
// It is assembled HERE rather than in the stand because it is this test's
// subject — the two halves are wired differently for every other test in this
// package — while the daemon underneath it is the stand's, shared with the
// Files acceptance so that one process assembles one helper.
type openSeam struct {
	hosted *hostedOpeners
	routes installLeaseRoutes
}

func openSeamOver(t *testing.T, stand *filesStand) openSeam {
	t.Helper()
	consentStore, installStore := testConsentStores(t)
	_, remote := helperGitFactory(stand.routes, refusingArtifactSource{}, consentStore, installStore, discardLogger(t))
	remote.registry = stand.registry
	return openSeam{
		hosted: &hostedOpeners{local: stand.opener, remote: remote},
		routes: stand.routes,
	}
}

// paneDestination asks the far host for the password the vault holds.
//
// DesiredRaw is this test's starting state and not a product claim: the fixture
// serves no remote forward, so an integrated pane could not keep a lifecycle
// channel. What the count is about is the DIAL, and both modes dial the same way
// — the probe and the spawn, on one pooled connection.
func paneDestination(srv *pwSSHServer, secrets credential.Resolver) *ssh.ConnectConfig {
	return &ssh.ConnectConfig{
		User:               filesFixtureUser,
		AuthMode:           "password",
		ConnectionName:     "One authentication",
		Secrets:            secrets,
		SecretID:           filesFixtureSecret,
		AuthorizedEndpoint: srv.addr,
		DesiredMode:        string(profile.DesiredRaw),
	}
}

// paneConfig is the session a user's pane open carries: the destination, the
// size, and the workspace pane it belongs to.
func paneConfig(srv *pwSSHServer, secrets credential.Resolver, paneID string) session.Config {
	return session.Config{
		Kind:      session.KindRemote,
		Host:      srv.addr,
		ProfileID: "ssh:one-authentication",
		PaneID:    paneID,
		Cols:      80, Rows: 24,
		Remote: paneDestination(srv, secrets),
	}
}

// liveConns is how many connections the fixture holds that are still open. It
// is the leak's own observable: a pooled reference nobody released keeps the
// transport up, and the far side sees that as a connection that never ends.
func (s *pwSSHServer) liveConns() int {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	return len(s.live)
}

// openPanes opens one pane through the shipped seam and fails the test when the
// seam did not claim the destination — a declined remote open is a product
// regression of its own (this machine's helper hosts every ssh pane), and it
// would make every count below meaningless.
func openPanes(t *testing.T, ctx context.Context, seam openSeam, cfg session.Config, claim string) session.Session {
	t.Helper()
	opened, selected, err := seam.hosted.OpenHosted(ctx, cfg, claim)
	if err != nil {
		t.Fatalf("opening one ssh pane on this machine's helper: %v", err)
	}
	if !selected {
		t.Fatal("the open path declined a remote destination: this machine's helper owns every ssh pane")
	}
	if opened.Session == nil {
		t.Fatal("the open path selected the helper and answered no session")
	}
	t.Cleanup(func() { _ = opened.Session.Close() })
	return opened.Session
}

// ── the acceptance ────────────────────────────────────────────────────────

// TestOnePaneCostsTheFarHostOneAuthentication is the criterion: one pane open
// costs the far host one login, and the Files panel beside that pane rides it.
func TestOnePaneCostsTheFarHostOneAuthentication(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello from the far side"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	srv := startPasswordSFTPSSHServer(t, root)
	secrets := &askRecorder{value: openPasswordFixturePassword}
	stand := startFilesStand(t, srv, secrets)
	seam := openSeamOver(t, stand)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. THE PANE. The selection probe authenticates here, and the pane's own
	//    `spawn-ssh` must ride that authentication rather than pay for a
	//    second one.
	openPanes(t, ctx, seam, paneConfig(srv, secrets, "pane-one"), "claim-one-pane")
	if got := srv.connCount(); got != 1 {
		t.Fatalf("the far host authenticated %d connection(s) for one pane, want exactly 1 — the platform probe and the pane's own spawn are one open and must ride one pooled connection", got)
	}

	// 2. THE FILES PANEL BESIDE IT. The same machine's helper serves it, and
	//    AD-4's pool is what makes it the same login: the pane's connection is
	//    already up, so this lease rides it.
	src := factorySession{kind: session.KindRemote, host: srv.addr, opts: session.SSHOptionsFromConfig(paneDestination(srv, secrets))}
	provider, err := stand.factory(src, root)
	if err != nil {
		t.Fatalf("the Files provider for the pane's session: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	listing, err := provider.List(ctx, root, filesystem.Page{Limit: 50})
	if err != nil {
		t.Fatalf("listing the far directory through the helper's channel: %v", err)
	}
	if len(listing.Entries) == 0 {
		t.Fatal("the listing through the helper's channel is empty: the Files panel reached nothing")
	}

	if got := srv.connCount(); got != 1 {
		t.Errorf("the far host authenticated %d connection(s) for one pane and its Files panel, want exactly 1 — the Files lease must ride the pane's pooled connection, not raise one of its own", got)
	}
	if seen := srv.subsystemsSeen(); len(seen) != 1 || seen[0] != "sftp" {
		t.Errorf("the fixture was asked for subsystems %v, want exactly [sftp] — the Files panel is what opened one", seen)
	}
	t.Logf("MEASURED %d authentication(s) for one pane plus its Files panel, subsystems %v",
		srv.connCount(), srv.subsystemsSeen())
}

// TestASecondPaneOnADifferentDestinationAuthenticatesSeparately is the paired
// case, and it is what keeps the criterion above from being satisfied by a
// mechanism that shares too much: a pooled connection is keyed by the
// DESTINATION, so a second host must pay for its own login — and the first
// pane's connection must be untouched by the second pane's open.
func TestASecondPaneOnADifferentDestinationAuthenticatesSeparately(t *testing.T) {
	first := startPasswordSFTPSSHServer(t, t.TempDir())
	second := startPasswordSFTPSSHServer(t, t.TempDir())
	secrets := &askRecorder{value: openPasswordFixturePassword}
	// Both destinations are recorded in one known_hosts, which is what a
	// coordinator holding two connections has.
	stand := startFilesStand(t, first, secrets, second)
	seam := openSeamOver(t, stand)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	openPanes(t, ctx, seam, paneConfig(first, secrets, "pane-first"), "claim-first")
	openPanes(t, ctx, seam, paneConfig(second, secrets, "pane-second"), "claim-second")

	if got := first.connCount(); got != 1 {
		t.Errorf("the first destination authenticated %d connection(s), want exactly 1 — its own pane", got)
	}
	if got := second.connCount(); got != 1 {
		t.Errorf("the second destination authenticated %d connection(s), want exactly 1 — a pooled connection is keyed by destination, so a second host cannot ride the first one's", got)
	}
	t.Logf("MEASURED %d + %d authentication(s) for two panes on two destinations",
		first.connCount(), second.connCount())
}

// TestARefusedSpawnReleasesTheProbesLease is the other end of the interval, and
// it is the arm the fix makes dangerous: the probe's reference is now held
// across the open, so an open that FAILS after the probe must give it back. A
// hold nobody releases is a connection that outlives the pane that never
// existed — the far side sees it as a login's transport that never closes.
//
// The fixture refuses the `shell` request: authentication succeeds (one login,
// as it should) and the session cannot be started, which is exactly the shape
// "the probe worked and the open did not".
func TestARefusedSpawnReleasesTheProbesLease(t *testing.T) {
	srv := startPasswordSSHServer(t)
	srv.refuseShell = true
	secrets := &askRecorder{value: openPasswordFixturePassword}
	stand := startFilesStand(t, srv, secrets)
	seam := openSeamOver(t, stand)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, selected, err := seam.hosted.OpenHosted(ctx, paneConfig(srv, secrets, "pane-refused"), "claim-refused")
	if !selected {
		t.Fatal("the open path declined a remote destination: this machine's helper owns every ssh pane")
	}
	if err == nil {
		t.Fatal("a host that refuses the shell request opened a pane")
	}
	// The dial happened and is the only one — the probe's, because the pane's
	// own acquisition rode it and was refused at the shell request.
	if got := srv.connCount(); got != 1 {
		t.Fatalf("the far host authenticated %d connection(s) for a refused open, want 1 — the probe dialed, and the spawn must not have dialed again", got)
	}

	// AND THE REFERENCE CAME BACK. The probe's lease was the last reference on
	// the pooled connection, so releasing it closes the transport and the far
	// side's own view of the connection ends. Waited on rather than slept on:
	// the close is the observable, and a duration would be a guess about how
	// long the helper takes to send it.
	waittest.WaitForTimeout(t, "the far host's connection to close after the refused open", 30*time.Second,
		func() bool { return srv.liveConns() == 0 })
	t.Logf("MEASURED %d authentication(s) and %d connection(s) left open after a refused open",
		srv.connCount(), srv.liveConns())
}
