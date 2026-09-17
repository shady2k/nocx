//go:build nocx_local_ssh

package app

// OPENING AN SSH PANE PUBLISHES THE BUNDLE ITS LAUNCH WILL LOOK FOR
// (nocx-50w7p.21) — the far half.
//
// `helper_pane_publish_test.go` (the untagged half) proves the call and its
// position with a scripted daemon: this opener publishes for the pane it is
// about to ask for, before it asks. What that file cannot show is the effect,
// because its installer is a recorder.
//
// Here the installer is the PRODUCT's carrier and the far side is a real host:
// the fixture's `pwSSHServer` with a real `sftp` subsystem and a real session
// home, reached through the real helper — the real host protocol, the real ssh
// service, the real session service with the real ssh spawner — so the pane is
// started on a channel this machine's daemon dialed, exactly as production does
// it. What is asserted is the pairing the defect broke:
//
//   - the bundle is committed under the far account's `$HOME` (the directory a
//     session's shell activates with, and not the sftp subsystem's starting
//     directory — nocx-50w7p.12), and
//   - the path a far shell checks before it execs anything,
//     `$HOME/.nocx/launch`, is the one the publish put there and made
//     executable, and
//   - the host was asked for the bundle BEFORE it was asked to run that launch
//     — the ordering §6.1's barrier enforces on the coordinator's own route,
//     and which no gate can enforce here because the party that would wait is
//     the daemon (spawn_ssh.go builds its plan with `Ordered` nil).
//
// The pairing is asserted against ONE far directory, and that is the point: the
// measured defect was a pane whose launch looked for a generation in a home
// nothing had written to, and it degraded with `hello-timeout` rather than with
// anything that named the missing bundle.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
)

// paneJourney is one real helper hosting panes for one fixture host: the
// product's carrier for the publish, and the opener the pane goes through.
type paneJourney struct {
	srv     *pwSSHServer
	carrier *remoteInstallerAdapter
	opener  *localHelperOpener
	// prompts is the person-ask seam this journey's helper answers a reverse
	// ssh.password-prompt through (helperReverseHandlers' third argument).
	// Exposed so a test that opens an INTERACTIVE destination — nothing
	// stored, only a person to ask — can wire a counting requester onto it
	// before opening; see openSSHInteractive.
	prompts *helperPrompt
}

// startPaneJourney assembles the stack: the fixture, the real daemon (ssh
// service + session service with the real spawner), the coordinator's client
// and resolver, and the product carrier between them.
func startPaneJourney(t *testing.T, srv *pwSSHServer) *paneJourney {
	t.Helper()
	logger := discardLogger(t)
	sshLog := log.NewSlogAdapter(logger)

	khPath := filepath.Join(t.TempDir(), "known_hosts")
	writeKnownHostsFor(t, khPath, srv)
	rc, err := ssh.NewReal(sshLog, ssh.WithKnownHostsFile(khPath))
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })

	// The daemon: ONE host protocol engine carrying both services this route
	// needs — the ssh service the publish's probe and channel ride (and whose
	// spawner opens the far shell), and the session service the pane's spawn,
	// attach and adoption ride.
	helperEnd, coordEnd := net.Pipe()
	sshdialClient, err := sshdial.New(logger)
	if err != nil {
		t.Fatalf("sshdial.New: %v", err)
	}
	svc := sshsvc.New(sshdialClient, logger)
	sessions := helpersession.New(helpersession.Options{
		Generation: filesFixtureGenera,
		Spawner:    &scriptedSpawner{},
		// THE REAL SSH SPAWNER, and it is the reason this file exists: the
		// launch under test is the one this builds — stage-1 plus the carrier
		// command — so the far host is asked to run the very text the publish
		// has to have prepared it for.
		SSHSpawner: helpersession.NewSSHSpawner(svc, logger),
		Log:        logger,
	})
	engine := host.New(helperEnd, helperEnd, filesFixtureGenera, "instance-1", logger)
	engine.Register(svc)
	engine.Register(sessions)
	// The session service routes a subscriber's pump by the CONNECTION the
	// request arrived on, so it has to be told which engine that is — the
	// daemon's accept loop does exactly this (session_reconcile_local_test.go's
	// endpoint, and cmd/nocx-helper's own loop), and without it every attach is
	// refused as unattached.
	unbind := sessions.Bind(engine)
	t.Cleanup(unbind)
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

	// prompts is the person-ask seam: unwired (no asker set) by default, which
	// is exactly what openSSH's own SecretID-bound destination never needs —
	// openSSHInteractive is what calls .set() on it, for the destination that
	// does.
	prompts := &helperPrompt{log: logger}
	peer, err := helperclient.Dial(ctx, helperclient.Config{
		Exec:        helperclient.NewSocketConn(coordEnd),
		ExpectHash:  filesFixtureGenera,
		SentinelTTL: 5 * time.Second,
		Reverse: helperReverseHandlers(
			rc, &askRecorder{value: openPasswordFixturePassword}, prompts, nil, logger),
		Log: logger,
	})
	if err != nil {
		t.Fatalf("helperclient.Dial: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })

	opener := &localHelperOpener{
		log:        discardLogger(t),
		registry:   session.New(sshLog, &reachPTYFactory{stub: pty.NewStub(sshLog)}),
		client:     peer,
		sshTargets: rc,
		dir:        t.TempDir(),
		installed:  helperInstalledStub(),
	}
	over := &sshOverHelper{local: opener, resolve: rc, log: logger}
	probes := &helperProbes{local: opener, resolve: rc}
	impl := shellintegration.New(log.NewSlogAdapter(logger))

	return &paneJourney{
		srv: srv,
		carrier: &remoteInstallerAdapter{
			inner: impl,
			bundle: &helperBundlePublisher{
				probes: probes, channels: over, publish: impl, log: logger,
			},
		},
		opener:  opener,
		prompts: prompts,
	}
}

// openSSH opens one ssh pane through this machine's helper, against the
// fixture, with the PRODUCT's carrier as the installer — the value the resolver
// stamps on a saved connection's config.
func (j *paneJourney) openSSH(t *testing.T, mode string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	opened, selected, err := j.opener.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote,
		Host: j.srv.addr,
		Remote: &ssh.ConnectConfig{
			User:     filesFixtureUser,
			AuthMode: "password",
			Secrets:  &askRecorder{value: openPasswordFixturePassword},
			SecretID: filesFixtureSecret,
			// The credential may only be spent on the endpoint its profile
			// names, and this process is the party that checks it.
			AuthorizedEndpoint: j.srv.addr,
			ConnectionName:     "pane publish journey",
			DesiredMode:        mode,
			Shell:              "bash",
			RemoteInstaller:    j.carrier,
		},
	}, "")
	if err == nil {
		_ = opened.Session.Close()
	}
	if !selected {
		t.Fatalf("this machine's opener declined an ssh pane (selected=false)")
	}
	return err
}

// openSSHInteractive opens one ssh pane exactly as openSSH does, except the
// destination carries NO stored credential at all — no Secrets, no SecretID
// — only a person to ask (requester). That is the shape the auth ladder
// resolves to the interactive rung for (AuthMode "password", nothing bound:
// internal/ssh's resolveCredential falls through SecretID's case and lands
// on "passwordCapable && cfg.PasswordRequester != nil"), and it is the exact
// shape e2e/connection-password.spec.ts's first-open case is: a profile with
// nothing stored yet, remembered only after the person answers once.
func (j *paneJourney) openSSHInteractive(t *testing.T, mode string, requester ssh.ConnectionPasswordRequester) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	opened, selected, err := j.opener.OpenHosted(ctx, session.Config{
		Kind: session.KindRemote,
		Host: j.srv.addr,
		Remote: &ssh.ConnectConfig{
			User:              filesFixtureUser,
			AuthMode:          "password",
			PasswordRequester: requester,
			// The credential may only be spent on the endpoint its profile
			// names, and this process is the party that checks it.
			AuthorizedEndpoint: j.srv.addr,
			ConnectionName:     "pane publish journey",
			DesiredMode:        mode,
			Shell:              "bash",
			RemoteInstaller:    j.carrier,
		},
	}, "")
	if err == nil {
		_ = opened.Session.Close()
	}
	if !selected {
		t.Fatalf("this machine's opener declined an ssh pane (selected=false)")
	}
	return err
}

// TestAnSSHPaneOpenedByThisMachinesHelperPublishesTheBundleItsLaunchNeeds is
// the acceptance: one pane open, through the real route, and the far host ends
// up holding both the bundle and the launch that activates it, in that order.
func TestAnSSHPaneOpenedByThisMachinesHelperPublishesTheBundleItsLaunchNeeds(t *testing.T) {
	srv, sftpRoot, home := newBundleFixture(t)
	srv.acceptLaunchExec = true
	journey := startPaneJourney(t, srv)

	if err := journey.openSSH(t, "script"); err != nil {
		t.Fatalf("opening an ssh pane through this machine's helper: %v", err)
	}

	// 1. THE BUNDLE IS WHERE A SESSION ACTIVATES — under the account's own
	// $HOME, which this fixture makes a different directory from the sftp
	// subsystem's starting directory on purpose (nocx-50w7p.12).
	manifest := bundleManifest(home)
	if !fileExistsIn(t, manifest) {
		t.Fatalf("no bundle was published under %s by opening the pane; the far side would find no "+
			"generation and degrade with hello-timeout, which is the defect this bead is about. "+
			"sftp root %s, host saw %v", manifest, sftpRoot, srv.eventLog())
	}

	// 2. THE PANE'S LAUNCH NAMES THAT PATH. The host was asked to run the
	// launch carrier, and the file a far shell checks before it execs anything
	// is the one the publish created and made executable.
	launches := srv.launchCommands()
	if len(launches) != 1 {
		t.Fatalf("the far host was asked to run %d launches, want exactly 1 (events %v)",
			len(launches), srv.eventLog())
	}
	if !strings.Contains(launches[0], shellintegration.LoaderReadyToken) {
		t.Fatalf("the command this host was asked to run is not the launch carrier: %q", launches[0])
	}
	launchPath := filepath.Join(home, ".nocx", "launch")
	if !isExecutableIn(t, launchPath) {
		t.Fatalf("a far shell checks [ -x \"$HOME/.nocx/launch\" ] before it starts, and %s is not an "+
			"executable file — the bundle and the launch disagree about which home they mean", launchPath)
	}

	// 3. AND THE PUBLISH CAME FIRST, which is the ordering no gate can enforce
	// on this route: the daemon's own bootstrap has nothing to wait on.
	events := srv.eventLog()
	probe := indexPrefix(events, "exec:echo $HOME")
	sftp := indexPrefix(events, "subsystem:sftp")
	launch := indexPrefix(events, "launch:")
	if probe < 0 || sftp < 0 || launch < 0 {
		t.Fatalf("the host's own record of what it was asked to do is %v, want a home probe, an sftp "+
			"channel and one launch", events)
	}
	if !(probe < sftp && sftp < launch) {
		t.Fatalf("the host was asked in this order: %v — the bundle and the channel it rides must both "+
			"precede the launch that looks for the generation they write", events)
	}
	t.Logf("MEASURED one pane open: %v; %s is executable; the host served %v",
		events, launchPath, srv.subsystemsSeen())
}

// TestAPaneStillOpensWhenItsBundleCouldNotBePublished is the paired degrade: a
// host that cannot name its home has nothing to publish into, and the pane
// still opens. §6.1 step 5 says a failed publish is not a refusal, and ADR-0004
// makes an ordinary usable terminal the one thing no failure path may suppress.
//
// The second half is that NOTHING was written where a bundle would have gone: a
// publish that guessed a directory would leave a generation in a place no
// session activates with.
func TestAPaneStillOpensWhenItsBundleCouldNotBePublished(t *testing.T) {
	srv, sftpRoot, home := newBundleFixture(t)
	srv.acceptLaunchExec = true
	// The host will not say where the account's home is — the refusal that
	// keeps a bundle out of a directory nobody named.
	srv.home = ""
	journey := startPaneJourney(t, srv)

	if err := journey.openSSH(t, "script"); err != nil {
		t.Fatalf("a failed publish refused the pane: %v — an un-integrated terminal is what §6.1 step 5 "+
			"promises instead", err)
	}
	if fileExistsIn(t, bundleManifest(home)) || fileExistsIn(t, bundleManifest(sftpRoot)) {
		t.Fatalf("a bundle appeared although the host named no home (home %s, sftp root %s)",
			home, sftpRoot)
	}
	if got := len(srv.launchCommands()); got != 1 {
		t.Fatalf("the host was asked to run %d launches, want 1: the pane must still be started", got)
	}
	t.Logf("MEASURED a pane whose publish could not name a home: opened, nothing written, the host saw %v",
		srv.eventLog())
}

// TestASSHPaneWithNothingStoredAsksThePersonOnceFromThePublishUntilTheSpawnHoldsItsOwnReference
// is nocx-xn63t.6.6's own criterion, named as the interval it is: FROM the
// moment the script-mode publish takes its lease on the destination UNTIL
// the interactive spawn that follows has taken a reference of its own, the
// underlying connection must not be dropped — so a person with nothing
// stored for this destination is asked for the password EXACTLY ONCE, and
// the far host authenticates EXACTLY ONCE, even though the publish
// (helper_publish.go's PublishBundle) and the spawn (sshsvc.Service.
// OpenShell) are two operations that each dial independently when nothing
// bridges the gap between them.
//
// Before holdConnection (openSSH, internal/app/helper_local.go) this was
// RED: the publish's own lease released before the spawn acquired one, the
// pool's refcount touched zero between them, and the spawn re-dialed —
// asking a person a SECOND time for a password they had already given, with
// nobody left to answer it. That is exactly the standing "Password for
// {profile}" prompt e2e/connection-password.spec.ts's first-open case
// measured, 20 seconds after the person's one Connect click.
func TestASSHPaneWithNothingStoredAsksThePersonOnceFromThePublishUntilTheSpawnHoldsItsOwnReference(t *testing.T) {
	srv, _, _ := newBundleFixture(t)
	srv.acceptLaunchExec = true
	journey := startPaneJourney(t, srv)

	asker := &countingPasswordRequester{answer: openPasswordFixturePassword}
	journey.prompts.set(asker)

	if err := journey.openSSHInteractive(t, "script", asker); err != nil {
		t.Fatalf("opening an ssh pane with nothing stored for it: %v", err)
	}

	if got := asker.count(); got != 1 {
		t.Fatalf("the person was asked for the connection password %d times, want exactly 1 — a second ask "+
			"is a prompt nobody is left to answer, standing behind the one they already answered", got)
	}
	if got := srv.connCount(); got != 1 {
		t.Fatalf("the far host authenticated %d connection(s) for one pane, want exactly 1 — the publish's "+
			"lease and the spawn's own channel must land on the SAME pooled connection (AD-4), not one each", got)
	}
	t.Logf("MEASURED %d ask(s) and %d authentication(s) for one script-mode pane with nothing stored",
		asker.count(), srv.connCount())
}

// fileExistsIn answers whether a path is a regular file.
func fileExistsIn(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// isExecutableIn answers whether a path is a file with any execute bit set —
// the test a far shell runs before it execs the launch.
func isExecutableIn(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}

// indexPrefix is the position of the first entry with this prefix, or -1.
func indexPrefix(entries []string, prefix string) int {
	for i, e := range entries {
		if strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}
