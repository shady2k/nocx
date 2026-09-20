//go:build nocx_local_ssh

package app

// THE SCRIPT-MODE BUNDLE PUBLISH RIDES THIS MACHINE'S HELPER (nocx-50w7p.15).
//
// The subject is the PRODUCT's carrier — `remoteInstallerAdapter`, the value
// the composition root stamps on every ConnectConfig and the transport stamps
// on every direct-host open — driven through the interface internal/ssh calls
// it by. What is scripted is what is not this package's: the SSH SERVER (the
// real in-process `pwSSHServer`, serving sftp over a directory), the
// COORDINATOR's answers to the helper's questions (helperReverseHandlers over a
// real ssh.RealClient), and the credential material (a resolver answering one
// fixed password). Between them sits the REAL helper: the real host protocol
// over a socketpair, the real ssh service, the real ssh client that dials, and
// the real coordinator client with a real reverse registry — the stand
// internal/helper/sshsvc's own suite builds, assembled here because this is the
// half only internal/app can assert.
//
// # What this file proves, and what the live suite proves
//
// Here: the route (home probe → sftp channel, one pooled connection), the
// LOCATION (the bundle lands under the account's `$HOME`, which this fixture
// makes a different directory from the sftp subsystem's starting directory —
// the defect measured in nocx-50w7p.12), one authentication for one
// destination, and the three failure paths paired with the success.
//
// There: the same route under a REAL OpenSSH, a real bash and a real lifecycle
// domain. `live_sshd_helper_carrier_test.go` replaces the live fixture's
// carrier with this one, so `TestLiveSshd_BashReachesAcceptedDomain` — the
// epic's acceptance — runs it; this file is where the routing itself is
// asserted, one observable at a time.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// bundleStand is the assembled stack: the fixture server, the helper peer, the
// coordinator's resolver, and the product carrier the engine calls.
type bundleStand struct {
	srv     *pwSSHServer
	secrets *askRecorder
	carrier *remoteInstallerAdapter
	// over and probes are the two helper halves, kept so a test can hold a
	// channel of its own beside the publish.
	over   *sshOverHelper
	probes *helperProbes
	opts   []ssh.ConnectOption
}

// startBundleStand assembles the stack against a fixture serving root over
// sftp. It is files_helper_acceptance_test.go's stand, one consumer over: the
// same opener, the same ssh service on the same socketpair, the same reverse
// handlers, and the product carrier between them.
func startBundleStand(t *testing.T, srv *pwSSHServer, secrets *askRecorder) *bundleStand {
	t.Helper()
	logger := discardLogger(t)
	sshLog := log.NewSlogAdapter(logger)

	// The coordinator's client: it RESOLVES, it answers host-key questions,
	// and it dials nothing for this publish — the fixture's authentication
	// count is what says so.
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHostsFor(t, khPath, srv)
	rc, err := ssh.NewReal(sshLog, ssh.WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	// The helper: the real daemon-side client dials, the real ssh service is
	// registered on a real host protocol engine, and the carrier between the
	// two sides is a socketpair.
	helperEnd, coordEnd := net.Pipe()
	sshdialClient, err := sshdial.New(logger)
	if err != nil {
		t.Fatalf("sshdial.New: %v", err)
	}
	svc := sshsvc.New(sshdialClient, logger)
	engine := host.New(helperEnd, helperEnd, filesFixtureGenera, "instance-1", logger)
	engine.Register(svc)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = engine.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	})

	peer, err := helperclient.Dial(ctx, helperclient.Config{
		Exec:        helperclient.NewSocketConn(coordEnd),
		ExpectHash:  filesFixtureGenera,
		SentinelTTL: 5 * time.Second,
		Reverse:     helperReverseHandlers(rc, secrets, &helperPrompt{log: logger}, nil, logger, newCaptureSink()),
		Log:         logger,
	})
	if err != nil {
		t.Fatalf("helperclient.Dial: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	opener := &localHelperOpener{log: logger}
	opener.installed = helperInstalledStub()
	opener.dir = t.TempDir()
	opener.client = peer

	over := &sshOverHelper{local: opener, resolve: rc, log: logger}
	probes := &helperProbes{local: opener, resolve: rc}
	impl := shellintegration.New(log.NewSlogAdapter(logger))

	return &bundleStand{
		srv:     srv,
		secrets: secrets,
		over:    over,
		probes:  probes,
		carrier: &remoteInstallerAdapter{
			inner: impl,
			bundle: &helperBundlePublisher{
				probes: probes, channels: over, publish: impl, log: logger,
			},
		},
		opts: []ssh.ConnectOption{
			ssh.WithUser(filesFixtureUser),
			ssh.WithAuthMode("password"),
			ssh.WithCredentials(secrets, filesFixtureSecret),
			// The credential may only be spent on the endpoint its profile
			// identifies; the resolver stamps this, so the test does too.
			ssh.WithAuthorizedEndpoint(srv.addr),
			ssh.WithConnectionName("bundle acceptance"),
		},
	}
}

// publish runs the product's publish for the fixture's destination — the exact
// call internal/ssh makes from publishBundle, with the destination the pane
// dialed.
func (s *bundleStand) publish(t *testing.T) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.carrier.EnsureInstalledRemote(ctx, s.srv.addr, s.opts...)
}

// uninstall runs the product's removal for the fixture's destination — the
// exact call the transport's shell.footprint.uninstall handler makes, through
// the composition root's own carrier.
//
// It goes through the CARRIER and not through helperBundlePublisher, because
// the thing this file now has to prove is that the capability the transport
// holds is the helper-backed one: a test that called the publisher directly
// would keep passing if the wiring were reverted to the dial that
// internal/ssh used to own (nocx-50w7p.5).
func (s *bundleStand) uninstall(t *testing.T) (removed, conflicts []string, err error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.carrier.UninstallIntegration(ctx, s.srv.addr, s.opts...)
}

// execCommands is every exec the fixture was asked to run, in order — the
// observable for "did the home probe run, and before the sftp channel". It
// lives with the acceptance that asks it: the fixture is shared, and a method
// only one build's tests use is dead code in the other (the lint gate says so).
func (s *pwSSHServer) execCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execSeen...)
}

// home is the session home the fixture's `home` probe answers, and sftpRoot is
// the directory its sftp subsystem starts in. The tests below make them
// DIFFERENT directories on purpose: one of them is where a session activates,
// and the other is where the passwd home would put the bundle.
func newBundleFixture(t *testing.T) (srv *pwSSHServer, sftpRoot, home string) {
	t.Helper()
	sftpRoot = t.TempDir()
	home = t.TempDir()
	srv = startPasswordSFTPSSHServer(t, sftpRoot)
	srv.home = home
	return srv, sftpRoot, home
}

// bundleManifest is where the shell-integration publisher commits its
// generation: `<home>/.nocx/manifest.json`.
func bundleManifest(home string) string {
	return filepath.Join(home, ".nocx", "manifest.json")
}

// ── the happy path ────────────────────────────────────────────────────────

// TestTheBundlePublishRidesThisMachinesHelperToTheSessionHome is the
// acceptance: the product's carrier publishes the bundle THROUGH THIS MACHINE'S
// HELPER, into the account's own $HOME — not into the sftp subsystem's starting
// directory, which on this fixture is a different directory and is exactly the
// mistake that made a far side refuse a generation it had just been handed
// (nocx-50w7p.12) — and the whole publish costs ONE authentication.
func TestTheBundlePublishRidesThisMachinesHelperToTheSessionHome(t *testing.T) {
	srv, sftpRoot, home := newBundleFixture(t)
	stand := startBundleStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

	if err := stand.publish(t); err != nil {
		t.Fatalf("the publish through this machine's helper: %v", err)
	}

	// THE LOCATION, and it is asserted from both sides: the marker is under
	// the session's $HOME, and the directory the sftp subsystem starts in has
	// no bundle at all. One half alone would pass for a publisher that wrote
	// to both, or for one that wrote to neither and returned early.
	if _, err := os.Stat(bundleManifest(home)); err != nil {
		t.Errorf("the bundle is not under the session $HOME (%s): %v", home, err)
	}
	if _, err := os.Stat(filepath.Join(sftpRoot, ".nocx")); !os.IsNotExist(err) {
		t.Errorf("something was published under the sftp subsystem's starting directory (%s): stat err = %v. A session activates under $HOME, so a bundle there is a generation the far side will refuse", sftpRoot, err)
	}

	// The route, proved by the server's own view: one connection, one
	// authentication, and the sftp subsystem asked for over it — the helper
	// dialed, and the coordinator did not.
	if got := srv.connCount(); got != 1 {
		t.Errorf("the fixture authenticated %d connection(s), want exactly 1 — the publish rides this machine's helper's pooled connection and must not raise one of its own", got)
	}
	if seen := srv.subsystemsSeen(); len(seen) != 1 || seen[0] != "sftp" {
		t.Errorf("the fixture was asked for subsystems %v, want exactly [sftp]", seen)
	}

	// The HOME QUESTION came first and was the named probe's own commands, not
	// a command composed by a caller (D3).
	execs := srv.execCommands()
	if len(execs) == 0 || execs[0] != remoteprobe.HomeCommand {
		t.Errorf("the fixture's exec requests are %v, want the home probe (%q) first — the bundle's location is decided by a question asked before anything is written", execs, remoteprobe.HomeCommand)
	}

	// The helper asked the coordinator for the material it had to present.
	// Without this the test could pass against a helper that never consulted
	// the reverse registry at all.
	if refs := stand.secrets.asked(); len(refs) == 0 || refs[0] != filesFixtureSecret {
		t.Errorf("the credential references the helper asked for are %v, want %q first", refs, filesFixtureSecret)
	}
	t.Logf("MEASURED publish through the helper: home=%s sftpRoot=%s authentications=%d subsystems=%v execs=%v",
		home, sftpRoot, srv.connCount(), srv.subsystemsSeen(), srv.execCommands())
}

// TestTheBundleUninstallRidesThisMachinesHelperToTheSessionHome is the removal
// half of the acceptance above, and it is the assertion that replaced
// internal/ssh's own uninstall tests when the capability moved here
// (nocx-50w7p.5).
//
// What it must show is not merely that a removal works: it is that the removal
// runs over THIS MACHINE'S HELPER — the same lease and the same pooled
// connection the publish uses, so one destination still costs one
// authentication — because the property that closed the epic is that the
// coordinator no longer holds a client to run it on. A removal that dialed for
// itself would leave the fixture authenticating twice, which is what the
// connection count below measures.
//
// The three behaviours internal/ssh's deleted tests asserted are carried over
// one for one: the carrier's two lists are the answer (`TestUninstallIntegration
// _OwnsTheDialAndCall`), the far side's own refusal reaches the caller with its
// cause (`..._CarrierRefusalIsReported`), and a run with no route refuses by
// name rather than reporting a success it did not have (`..._NoInstallerRefuses`,
// asserted as TestTheRemovalRefusesWithoutAHelper below).
func TestTheBundleUninstallRidesThisMachinesHelperToTheSessionHome(t *testing.T) {
	srv, _, home := newBundleFixture(t)
	stand := startBundleStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

	// A removal needs something to remove, and it is published through the same
	// carrier so the fixture's state is the product's own.
	if err := stand.publish(t); err != nil {
		t.Fatalf("the publish through this machine's helper: %v", err)
	}
	if _, err := os.Stat(bundleManifest(home)); err != nil {
		t.Fatalf("the fixture has no bundle to remove, so the removal below would pass vacuously: %v", err)
	}

	removed, _, err := stand.uninstall(t)
	if err != nil {
		t.Fatalf("the removal through this machine's helper: %v", err)
	}

	// The manifest is GONE, and that is asserted from the far side's own
	// filesystem rather than from the carrier's return value: a carrier that
	// reported `removed` and wrote nothing would pass the second check alone.
	if _, err := os.Stat(bundleManifest(home)); !os.IsNotExist(err) {
		t.Errorf("the integration manifest is still under the session $HOME after a reported removal: stat err = %v", err)
	}
	if len(removed) == 0 {
		t.Error("the removal reported nothing removed, yet the bundle had been published into this home")
	}

	// THE ROUTE, and it is asserted as three facts rather than as a connection
	// count — because the count across two SEQUENTIAL calls is legitimately 2,
	// and asserting 1 here would be asserting something false (measured, not
	// assumed: the run that produced this test logged `authentications=2`).
	// AD-4 closes the connection with the last reference, and the publish
	// releases its lease before returning, so the removal is a fresh dial on the
	// same route — which is exactly the shape "one destination costs one
	// authentication" describes WITHIN a call, and the property
	// TestOneDestinationCostsOneAuthentication measures with both halves live.
	//
	// What must hold either way is that the dial was the HELPER's and not this
	// process's: the far side was asked for the sftp subsystem (the channel came
	// from the helper's ssh service), and the home was asked by the named probe
	// rather than composed here. A removal dialing for itself from the
	// coordinator would still show the subsystem, so the probe's presence in the
	// helper's own exec log is the half that discriminates.
	if seen := srv.subsystemsSeen(); !slices.Contains(seen, "sftp") {
		t.Errorf("the fixture was asked for subsystems %v, want sftp among them — the removal must ride an sftp channel the helper opened", seen)
	}
	execs := srv.execCommands()
	if len(execs) != 2 || execs[0] != remoteprobe.HomeCommand || execs[1] != remoteprobe.HomeCommand {
		t.Errorf("the fixture's exec requests are %v, want the home probe (%q) once per half — the bundle's location is decided by a question, never by a command a caller composed", execs, remoteprobe.HomeCommand)
	}
	t.Logf("MEASURED removal through the helper: home=%s removed=%d authentications=%d (one per call: the pool releases with the publish's lease) subsystems=%v execs=%v",
		home, len(removed), srv.connCount(), srv.subsystemsSeen(), execs)
}

// TestTheRemovalRefusesWithoutAHelper is the carried-over refusal assertion: a
// build or a test with no helper wired must say so at the act rather than
// removing nothing and reporting success, which would leave a bundle on
// somebody's host while reporting it gone.
func TestTheRemovalRefusesWithoutAHelper(t *testing.T) {
	carrier := &remoteInstallerAdapter{inner: shellintegration.New(log.NewSlogAdapter(nil))}
	_, _, err := carrier.UninstallIntegration(context.Background(), "host.example")
	if err == nil {
		t.Fatal("a removal with no helper wired succeeded")
	}
	if !strings.Contains(err.Error(), "no helper is wired") {
		t.Errorf("the refusal is %q, want it to name the missing helper so a person can act on it", err)
	}
}

// TestOneDestinationCostsOneAuthentication: a second consumer of the same
// destination — a channel opened through the same helper while the publish
// runs — costs NO second authentication, because AD-4's pool is keyed by the
// resolved destination and both hold references to one connection.
//
// This is the criterion's "the publish and the pane share one authenticated
// connection", asserted at the only layer that can assert it TODAY: the pane's
// own mount on this connection lands with the route (nocx-50w7p.5), so what is
// measured here is the property that makes it true — every channel and probe
// for one destination is one login — with a second sftp channel standing where
// the pane will be. The pane, the Files panel and this publish then differ only
// in the kind of channel they hold.
//
// The mutation this discriminates on is the DESTINATION, not the lease's
// lifetime: a channel naming a different account resolves to a different pool
// key, the helper dials again, and the fixture authenticates twice. (Releasing
// the publish's lease early does not move this test, because the held channel's
// own reference keeps the transport alive — that ordering is what
// TestTheBundlePublishRidesThisMachinesHelperToTheSessionHome measures.)
func TestOneDestinationCostsOneAuthentication(t *testing.T) {
	srv, _, home := newBundleFixture(t)
	stand := startBundleStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A consumer already on the destination: a channel through the helper,
	// held for the whole publish.
	held, err := stand.over.openChannel(ctx, proto.ChannelSFTP, srv.addr, stand.opts)
	if err != nil {
		t.Fatalf("the first consumer's channel: %v", err)
	}
	defer func() { _ = held.Close() }()

	if err := stand.publish(t); err != nil {
		t.Fatalf("the publish beside a live channel: %v", err)
	}
	if _, err := os.Stat(bundleManifest(home)); err != nil {
		t.Fatalf("the bundle is not under the session $HOME (%s): %v", home, err)
	}

	if got := srv.connCount(); got != 1 {
		t.Errorf("the fixture authenticated %d connection(s) for two consumers of one destination, want exactly 1 — a second login per consumer is what AD-4's pool exists to prevent", got)
	}
	if seen := srv.subsystemsSeen(); len(seen) != 2 {
		t.Errorf("the fixture served %d sftp subsystems (%v), want 2 — the held channel's and the publish's, both on one connection", len(seen), seen)
	}
	t.Logf("MEASURED two consumers, one destination: authentications=%d subsystems=%v", srv.connCount(), srv.subsystemsSeen())
}

// TestTheBundlePublishAuthenticatesWithAnInlineKey is the SEAM-level half of
// the inline-key success (nocx-50w7p.15 with nocx-50w7p.11): a connection whose
// profile names a key FILE publishes through this machine's helper, and the key
// authenticates because the coordinator reads it and signs with it — the helper
// holds nothing but a reference and a public half.
//
// Until the key-reference work landed this connection was REFUSED by name
// before any dial (`ssh.ErrNoHelperIdentity`), and the test that asserted that
// is this one: the shape of the connection is unchanged — an `IdentityFile`
// profile with no binding in the store — and only the answer to it moved. The
// end-to-end half runs the same connection through the product's own
// `RealClient.Connect` against a real sshd
// (TestLiveSshd_AnInlineKeyFileConnectionPublishesAndReachesItsDomain).
//
// The paired refusal is right below it, because the two are the same code path:
// a key this process can OPEN and one it cannot.
func TestTheBundlePublishAuthenticatesWithAnInlineKey(t *testing.T) {
	srv, _, home := newBundleFixture(t)
	keyPath, keySigner := writeInlineKey(t)
	// The host authenticates a key, not a password: without this the fixture
	// would refuse the offer and the test would pass on the wrong outcome.
	srv.acceptKey(keySigner.PublicKey())
	stand := startBundleStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

	// The connection's own options name the file. Nothing is appended to them:
	// what a direct-host open carries is what the publish is handed, and the
	// resolution is the product's rather than this test's.
	stand.opts = []ssh.ConnectOption{
		ssh.WithUser(filesFixtureUser),
		ssh.WithKeyFile(keyPath),
		ssh.WithAuthorizedEndpoint(srv.addr),
		ssh.WithConnectionName("bundle acceptance (inline key)"),
	}

	if err := stand.publish(t); err != nil {
		t.Fatalf("the publish for an inline-key connection through this machine's helper: %v", err)
	}

	// The bundle landed under the session's $HOME, which is where the far side
	// looks for the generation it was handed.
	if _, err := os.Stat(bundleManifest(home)); err != nil {
		t.Errorf("the bundle is not under the session $HOME (%s): %v", home, err)
	}

	// ONE connection, authenticated by the KEY, and the sftp subsystem asked
	// for over it: the helper dialed, and the credential it presented was a
	// signature this process made.
	if got := srv.connCount(); got != 1 {
		t.Errorf("the fixture authenticated %d connection(s), want exactly 1", got)
	}
	if got := srv.keyFingerprintsSeen(); len(got) != 1 || got[0] != gossh.FingerprintSHA256(keySigner.PublicKey()) {
		t.Errorf("the destination authenticated key(s) %v, want the offered key exactly once", got)
	}
	if seen := srv.subsystemsSeen(); len(seen) != 1 || seen[0] != "sftp" {
		t.Errorf("the fixture was asked for subsystems %v, want exactly [sftp]", seen)
	}
	// The store was never consulted: this credential is a file, and material
	// the coordinator does not hold is not material it asks a vault for.
	if refs := stand.secrets.asked(); len(refs) != 0 {
		t.Errorf("the credential store was asked for %v, want nothing — the key's bytes come from the file the profile names", refs)
	}
	// The home question still came first: the location is decided before
	// anything is written, whatever the credential is.
	if execs := srv.execCommands(); len(execs) == 0 || execs[0] != remoteprobe.HomeCommand {
		t.Errorf("the fixture's exec requests are %v, want the home probe first", execs)
	}
	t.Logf("MEASURED an inline-key publish through the helper: authentications=%d keys=%v subsystems=%v",
		srv.connCount(), srv.keyFingerprintsSeen(), srv.subsystemsSeen())
}

// TestTheBundlePublishRefusesALockedKeyBeforeDialing is the paired refusal, and
// it is the state that still exists: a key file this process cannot OPEN (an
// encrypted key with nowhere to read its passphrase) is refused as
// `ssh.ErrEncryptedKey` — "the key is fine, it is only locked" — and it is
// refused BEFORE any dial, so nothing is offered to the host and nothing runs
// on it.
//
// The distinction from ErrAuthFailed is the point of the type: a person sent to
// look at a host for a key that needed a passphrase has been sent to the wrong
// place, and this is where that stays true one process out.
func TestTheBundlePublishRefusesALockedKeyBeforeDialing(t *testing.T) {
	srv, _, _ := newBundleFixture(t)
	locked := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(locked, encryptedTestKey(t), 0o600); err != nil {
		t.Fatalf("write the locked key: %v", err)
	}
	stand := startBundleStand(t, srv, &askRecorder{value: openPasswordFixturePassword})
	stand.opts = []ssh.ConnectOption{
		ssh.WithUser(filesFixtureUser),
		ssh.WithKeyFile(locked),
		ssh.WithAuthorizedEndpoint(srv.addr),
	}

	err := stand.publish(t)
	var lockedErr *ssh.ErrEncryptedKey
	if !errors.As(err, &lockedErr) {
		t.Fatalf("the publish for a locked key file is %v (%T), want *ssh.ErrEncryptedKey", err, err)
	}
	if got := srv.connCount(); got != 0 {
		t.Errorf("the fixture authenticated %d connection(s), want 0 — this refusal is reached before any dial", got)
	}
	if seen := srv.subsystemsSeen(); len(seen) != 0 {
		t.Errorf("the fixture was asked for subsystems %v, want none", seen)
	}
	if execs := srv.execCommands(); len(execs) != 0 {
		t.Errorf("the fixture ran %v, want nothing — a location is not asked for when the credential cannot be read", execs)
	}
	t.Logf("MEASURED the named refusal for a locked key: %v", err)
}

// ── the failure paths, each paired with the success above ─────────────────

// TestThePublishRefusesWhenTheHostCannotNameItsHome: a host whose shell answers
// nothing for `$HOME` leaves the bundle's location unknown, and the publish is
// REFUSED rather than written into a directory nobody named — and nothing is
// opened on the far side in that case, because there is nothing to open it for.
//
// The second case is the same refusal reached through a host that will not run
// the probe at all (ForceCommand, a restricted shell): the two are different
// facts about the host and the same conclusion here.
func TestThePublishRefusesWhenTheHostCannotNameItsHome(t *testing.T) {
	t.Run("the shell answers nothing", func(t *testing.T) {
		srv, sftpRoot, _ := newBundleFixture(t)
		srv.home = "" // both home commands answer empty
		stand := startBundleStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

		err := stand.publish(t)
		if err == nil {
			t.Fatal("the publish SUCCEEDED although the host named no home directory")
		}
		if !strings.Contains(err.Error(), "home") {
			t.Errorf("the refusal is %q, want it to name the home it could not read", err)
		}
		if seen := srv.subsystemsSeen(); len(seen) != 0 {
			t.Errorf("the fixture was asked for subsystems %v, want none — nothing may be opened, let alone written, before the location is known", seen)
		}
		if _, statErr := os.Stat(filepath.Join(sftpRoot, ".nocx")); !os.IsNotExist(statErr) {
			t.Errorf("a bundle appeared under the sftp starting directory (%s) although the home was unknown: stat err = %v", sftpRoot, statErr)
		}
	})

	t.Run("the host refuses exec", func(t *testing.T) {
		srv, _, _ := newBundleFixture(t)
		srv.refuseExec = true
		stand := startBundleStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

		err := stand.publish(t)
		if err == nil {
			t.Fatal("the publish SUCCEEDED although the host refuses exec, so the home probe could not run")
		}
		if seen := srv.subsystemsSeen(); len(seen) != 0 {
			t.Errorf("the fixture was asked for subsystems %v, want none", seen)
		}
	})
}

// TestThePublishNamesARefusedSFTPSubsystem: a host that runs ssh and no
// sftp-server answers the subsystem request false, and the refusal reaches the
// caller as an error rather than as a silent success. The home probe has
// already run by then, which is the order: the location is decided before
// anything is opened.
func TestThePublishNamesARefusedSFTPSubsystem(t *testing.T) {
	srv, sftpRoot, _ := newBundleFixture(t)
	srv.refuseSubsystem = true
	stand := startBundleStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

	err := stand.publish(t)
	if err == nil {
		t.Fatal("the publish SUCCEEDED against a host with no sftp-server")
	}
	if seen := srv.subsystemsSeen(); len(seen) != 0 {
		t.Errorf("subsystems recorded = %v, want none — the request was refused, not served", seen)
	}
	if execs := srv.execCommands(); len(execs) == 0 || execs[0] != remoteprobe.HomeCommand {
		t.Errorf("exec requests = %v, want the home probe first — the location is asked for before the channel is", execs)
	}
	if _, statErr := os.Stat(filepath.Join(sftpRoot, ".nocx")); !os.IsNotExist(statErr) {
		t.Errorf("a bundle appeared although the subsystem was refused: stat err = %v", statErr)
	}
}

// TestThePublishReportsATransportLostMidWrite: the sftp subsystem is granted
// and the connection dies while the bundle is being written. The publish
// REPORTS it — it neither hangs nor answers success — which is what the caller's
// fail-open contract reads: the session still opens, and the log says why the
// far side may find no generation.
func TestThePublishReportsATransportLostMidWrite(t *testing.T) {
	srv, _, _ := newBundleFixture(t)
	srv.closeOnSFTPWrite = true
	stand := startBundleStand(t, srv, &askRecorder{value: openPasswordFixturePassword})

	done := make(chan error, 1)
	go func() { done <- stand.publish(t) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the publish SUCCEEDED although the transport was lost mid-write")
		}
		t.Logf("MEASURED the mid-write loss: %v", err)
	case <-time.After(60 * time.Second):
		t.Fatal("the publish neither failed nor returned after the transport died mid-write")
	}
}
