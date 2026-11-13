package app

// AN SSH PANE THIS MACHINE'S HELPER OPENS PUBLISHES THE BUNDLE ITS LAUNCH WILL
// LOOK FOR (nocx-50w7p.21).
//
// The defect this file is written against was measured by the epic's e2e: every
// ssh pane has been opened by this machine's helper since nocx-50w7p.5, and on
// that route nothing published the shell-integration bundle — the trigger lived
// in `RealClient.Connect`'s startPublish, and that dial half is compiled out of
// a coordinator built without `nocx_local_ssh` (ssh_real_dial.go). So the far
// side found no generation, the integration degraded to a conventional terminal
// with `handshake-timeout cause=hello-timeout`, and the pane rendered without
// its command editor.
//
// WHAT IS ASSERTED HERE is the CALL and its POSITION: the opener publishes for
// the pane it is about to ask for, before it asks. The trip itself — the home
// probe, the sftp channel, the bundle landing under the account's own $HOME
// through this machine's helper — is the existing acceptance
// (helper_bundle_acceptance_test.go), which drives the same carrier with a real
// helper and a real in-process SSH/SFTP server. Driving it a second time from
// here would be two answers to one question; what this file adds is that the
// pane's open is what asks.
//
// The daemon is the scripted one (`fakeLocalEndpoint`, the same stand the
// pane-screen owner's tests use): a real host protocol on a real endpoint
// socket with a scripted ssh spawner behind it, so the shape under test is the
// REQUEST the coordinator sends rather than what somebody's shell does with it.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/endpoint"
	helperlocal "github.com/shady2k/nocx/internal/helper/local"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// paneInstaller is the injected `ssh.RemoteInstaller` — the value the resolver
// and the transport stamp on a saved connection's config (connection/resolver.go,
// session_open.go), which is the reason the opener needs no seam of its own.
//
// It records where it was told to publish, and — the load-bearing half — WHAT
// THE DAEMON HAD BEEN ASKED TO SPAWN at the moment it was called. The ordering
// claim ("the bundle is published before the launch that names it is asked
// for") is otherwise a reading of the source; this reads it off the daemon's
// own counter, so a publish moved after the spawn is a failure rather than a
// comment somebody has to trust.
type paneInstaller struct {
	daemon *fakeLocalEndpoint
	err    error

	mu      sync.Mutex
	hosts   []string
	pending []int
}

var _ ssh.RemoteInstaller = (*paneInstaller)(nil)

func (p *paneInstaller) EnsureInstalledRemote(_ context.Context, host string, _ ...ssh.ConnectOption) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hosts = append(p.hosts, host)
	p.pending = append(p.pending, p.daemon.spawned())
	return p.err
}

// calls is the destinations this installer was asked to publish for, in order.
func (p *paneInstaller) calls() (hosts []string, spawnsAlreadyAskedFor []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.hosts...), append([]int(nil), p.pending...)
}

// paneStand is the scripted daemon plus the opener built the way the
// composition root builds it, so a test can vary one fact of the open.
type paneStand struct {
	opener *localHelperOpener
	daemon *fakeLocalEndpoint
	// logs collects what the opener said. It is ONE of the two surfaces a
	// publish failure has: the design keeps the failure out of the open's
	// error (a failed publish decides nothing about the session — §6.1 step
	// 5), so the log carries the cause, and since nocx-1kejz.4 the open's own
	// RESULT carries the classified reason out to the pane. Those are the two
	// halves, and both are asserted below.
	logs *bytes.Buffer
}

// startPaneStand stands up this machine's daemon and an opener pointed at it.
func startPaneStand(t *testing.T) *paneStand {
	t.Helper()
	home := storagetest.IsolateWithHome(t)
	gen := fakeArtifacts{payload: syntheticPayload}.hash()
	daemon := startFakeLocalEndpoint(t, endpoint.Dir(home), gen)

	slogger := log.NewSlogAdapter(discardLogger(t))
	reg := session.New(slogger, &reachPTYFactory{stub: pty.NewStub(slogger)})
	rc, err := ssh.NewReal(slogger)
	if err != nil {
		t.Fatalf("ssh.NewReal: %v", err)
	}
	logs := &bytes.Buffer{}
	return &paneStand{
		daemon: daemon,
		logs:   logs,
		opener: &localHelperOpener{
			log:      slog.New(slog.NewTextHandler(logs, nil)),
			registry: reg,
			dir:      endpoint.Dir(home),
			installed: helperlocal.Installed{
				Binary: "/nonexistent/nocx-helper", Generation: proto.GenerationID(gen),
			},
			sshTargets: rc,
		},
	}
}

// sshConfig is the destination one test's ssh pane opens, with the installer
// (or its absence) the caller names.
func (s *paneStand) sshConfig(mode string, installer ssh.RemoteInstaller) session.Config {
	return session.Config{
		Kind: session.KindRemote,
		Host: "host.example",
		Remote: &ssh.ConnectConfig{
			User: "dev", Port: 22, AuthMode: "password",
			// A named credential, so resolution produces a target at all: the
			// helper holds no secret and refuses a destination that names none.
			Secrets:  rememberedPassword{value: openPasswordFixturePassword},
			SecretID: "sec:pane-publish:1",
			// A linked credential may only be spent on the endpoint its profile
			// names, and this process is the party that checks it.
			AuthorizedEndpoint: "host.example:22",
			DesiredMode:        mode,
			Shell:              "bash",
			RemoteInstaller:    installer,
		},
	}
}

// openSSHOnce opens one ssh pane through this machine's helper and answers
// with everything the open reported about it.
func (s *paneStand) openSSHOnce(t *testing.T, cfg session.Config) (transport.HostedSessionOpen, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, selected, err := s.opener.OpenHosted(ctx, cfg, "")
	if !selected {
		t.Fatalf("this machine's opener declined an ssh pane (selected=false), which ADR-0057 forbids: " +
			"a destination it owns is always its own, including when it cannot be served")
	}
	return opened, err
}

// openSSH opens one ssh pane through this machine's helper, with the installer
// (or its absence) a caller names, and answers only whether it worked.
func (s *paneStand) openSSH(t *testing.T, mode string, installer ssh.RemoteInstaller) error {
	t.Helper()
	opened, err := s.openSSHOnce(t, s.sshConfig(mode, installer))
	if err == nil {
		_ = opened.Session.Close()
	}
	return err
}

// TestAnSSHPanePublishesItsBundleBeforeItAsksForTheSpawn is the acceptance for
// the wiring: opening an ssh pane through this machine's helper publishes the
// shell-integration bundle for the pane's own destination — the address the
// config carries — and does it BEFORE the spawn op, because the launch the
// daemon builds names a generation that has to be on the far side already.
func TestAnSSHPanePublishesItsBundleBeforeItAsksForTheSpawn(t *testing.T) {
	stand := startPaneStand(t)
	installer := &paneInstaller{daemon: stand.daemon}

	if err := stand.openSSH(t, "script", installer); err != nil {
		t.Fatalf("opening an ssh pane through this machine's helper: %v", err)
	}

	hosts, pending := installer.calls()
	if len(hosts) != 1 || hosts[0] != "host.example" {
		t.Fatalf("the installer was asked to publish for %v, want exactly the pane's own destination "+
			"[\"host.example\"] — no publish leaves the far side with no generation to activate", hosts)
	}
	if pending[0] != 0 {
		t.Fatalf("the publish ran when the daemon had already been asked to spawn %d sessions; the bundle "+
			"must be there BEFORE the launch that names it is asked for, or stage-1 re-proves a generation "+
			"that is still in flight", pending[0])
	}
	if got := stand.daemon.spawned(); got != 1 {
		t.Fatalf("this machine's daemon was asked to spawn %d sessions, want 1 — the publish must not "+
			"replace the pane it was published for", got)
	}
	t.Logf("MEASURED publish for %q with %d spawns already requested, then one spawn-ssh on the daemon",
		hosts[0], pending[0])
}

// TestARawPanePublishesNothing is the first pair, and it is the mode axis's own
// rule rather than this file's: `raw` is the one answer that means "nothing
// added" (profile.DesiredMode.DeliversScripts), and a pane that asks for a
// plain login shell must not have a bundle written on somebody's host for it.
//
// It is asserted through the SAME open, because the gate is a predicate applied
// to the mode the spawn is about to carry: a test that called the predicate
// would confirm the rule and say nothing about whether the route consults it.
func TestARawPanePublishesNothing(t *testing.T) {
	stand := startPaneStand(t)
	installer := &paneInstaller{daemon: stand.daemon}

	if err := stand.openSSH(t, "raw", installer); err != nil {
		t.Fatalf("opening a raw-mode ssh pane: %v", err)
	}
	if hosts, _ := installer.calls(); len(hosts) != 0 {
		t.Fatalf("a raw pane published for %v, and raw is the mode that adds nothing to the far host", hosts)
	}
	if got := stand.daemon.spawned(); got != 1 {
		t.Fatalf("the daemon was asked to spawn %d sessions, want 1: refusing to publish must not refuse "+
			"the pane, which is an ordinary usable terminal (ADR-0004)", got)
	}
	t.Logf("MEASURED a raw pane: no publish, and the spawn still happened (%d)", 1)
}

// TestAPaneWithNoInstallerOpensWithoutAPublish is the second pair: a build or a
// wiring that stamps no installer publishes nothing and PANICS nowhere. It is
// the same shape internal/ssh's startPublish had for a nil installer — a
// terminal outcome all the same — and it is here because this route reaches the
// value directly rather than through a gate.
func TestAPaneWithNoInstallerOpensWithoutAPublish(t *testing.T) {
	stand := startPaneStand(t)

	if err := stand.openSSH(t, "script", nil); err != nil {
		t.Fatalf("opening an ssh pane in a build that publishes nothing: %v", err)
	}
	if got := stand.daemon.spawned(); got != 1 {
		t.Fatalf("the daemon was asked to spawn %d sessions, want 1: no installer is not a refusal", got)
	}
	t.Logf("MEASURED a pane with no installer wired: opened, nothing published")
}

// TestAPaneOpensEvenWhenItsPublishFailed is the degrade's half of the
// acceptance, and it is what stops the fix from turning nocx's own auxiliary
// work into a person's missing terminal.
//
// The publish fails with the cause the epic measured — a host that cannot name
// its home, which is the refusal that keeps a bundle out of a directory nobody
// named — and the pane STILL OPENS. That is §6.1 step 5's contract read the
// right way round: a failed publish leaves any generation already on the far
// host byte-identical, so it is not a refusal, and ADR-0004 makes an ordinary
// usable terminal the one thing no failure path may suppress.
//
// AND THE PANE IS TOLD (nocx-1kejz.4). Before it, the failure's whole
// destination was the log line asserted below — a soft degrade the product
// contradicted, drawn by nocx itself. The reason now rides the open's result,
// classified at the step that failed, and this is where that is read off the
// result the transport actually receives rather than off the vocabulary.
func TestAPaneOpensEvenWhenItsPublishFailed(t *testing.T) {
	stand := startPaneStand(t)
	cause := &shellintegration.PublishFailure{
		Reason: shellintegration.PublishFailureHome,
		Cause:  errors.New("the far side named no home directory"),
	}
	installer := &paneInstaller{daemon: stand.daemon, err: cause}

	opened, err := stand.openSSHOnce(t, stand.sshConfig("script", installer))
	if err != nil {
		t.Fatalf("a failed publish refused the pane: %v — an un-integrated terminal is what §6.1 step 5 "+
			"promises instead, and a pane nobody can use is not a visible degrade", err)
	}
	defer func() { _ = opened.Session.Close() }()
	if got := stand.daemon.spawned(); got != 1 {
		t.Fatalf("the daemon was asked to spawn %d sessions, want 1", got)
	}

	// THE PANE'S HALF: the classified reason, on the result, for the party that
	// tells the person. A log line nobody reads is what this replaces.
	if got := opened.PublishFailure; got != shellintegration.PublishFailureHome {
		t.Fatalf("the open reported publish failure %q, want %q — the pane cannot say what did not "+
			"land if the opener keeps the reason to itself", got, shellintegration.PublishFailureHome)
	}

	// The failure is NAMED, not swallowed: the log carries the cause, so the
	// host that could not say where its home is can be found from the run.
	if logged := stand.logs.String(); !strings.Contains(logged, "host.example") ||
		!strings.Contains(logged, "the far side named no home directory") {
		t.Fatalf("the publish failure produced no record naming the host and its cause; the log was %q",
			logged)
	}
	t.Logf("MEASURED the fail-open publish: pane opened, result names %q, and the log carries the cause",
		opened.PublishFailure)
}

// TestAPublishFailureWithNoClassificationIsStillReported is the fail-open rule
// every closed vocabulary in this repo carries: a cause nobody typed — a third
// party's installer, a step added elsewhere — is still a pane that did not get
// its copy, and "nocx cannot say why" is worth more than a second silence.
// Neither direction may be a blank: an unmapped cause reads as `unknown`, and a
// landing copy reads as nothing at all, which is the pair this test pins.
func TestAPublishFailureWithNoClassificationIsStillReported(t *testing.T) {
	stand := startPaneStand(t)

	untyped := &paneInstaller{daemon: stand.daemon, err: errors.New("something nobody classified")}
	opened, err := stand.openSSHOnce(t, stand.sshConfig("script", untyped))
	if err != nil {
		t.Fatalf("a failed publish refused the pane: %v", err)
	}
	defer func() { _ = opened.Session.Close() }()
	if got := opened.PublishFailure; got != shellintegration.PublishFailureUnknown {
		t.Fatalf("an unclassified publish failure reported %q, want %q: a cause with no name is still "+
			"a copy that did not land", got, shellintegration.PublishFailureUnknown)
	}

	// …and the other end: a copy that LANDED says nothing, which is what keeps
	// every healthy ssh pane quiet.
	landed := &paneInstaller{daemon: stand.daemon}
	ok, err := stand.openSSHOnce(t, stand.sshConfig("script", landed))
	if err != nil {
		t.Fatalf("opening an ssh pane whose publish succeeds: %v", err)
	}
	defer func() { _ = ok.Session.Close() }()
	if got := ok.PublishFailure; got != shellintegration.PublishFailureNone {
		t.Fatalf("a successful publish reported %q, want no failure at all — a healthy pane must say "+
			"nothing", got)
	}
}

// TestASSHPaneStillOpensWhenTheKeepAliveLeaseCannotBeAcquired is the paired
// degrade for holdConnection (nocx-xn63t.6.6): this file's daemon
// (fakeLocalEndpoint, built by startFakeLocalEndpoint) registers only the
// git and session services, never an ssh one, so the extra probe lease
// openSSH now holds across the publish and the spawn can never be acquired
// here — the coordinator dials the daemon fine and is refused the op by
// name. The pane must still open, the same ADR-0004 rule
// TestAPaneOpensEvenWhenItsPublishFailed already holds openSSH to for the
// publish itself, and the log must say why, in the same shape that failure's
// own record takes.
func TestASSHPaneStillOpensWhenTheKeepAliveLeaseCannotBeAcquired(t *testing.T) {
	stand := startPaneStand(t)
	installer := &paneInstaller{daemon: stand.daemon}

	if err := stand.openSSH(t, "script", installer); err != nil {
		t.Fatalf("a keep-alive lease that could not be acquired refused the pane: %v — ADR-0004 promises an "+
			"ordinary usable terminal instead", err)
	}
	if got := stand.daemon.spawned(); got != 1 {
		t.Fatalf("the daemon was asked to spawn %d sessions, want 1: the pane must still open", got)
	}
	if hosts, _ := installer.calls(); len(hosts) != 1 {
		t.Fatalf("the installer was asked to publish for %v, want exactly one call: a lease that could not "+
			"be held must not have stopped the publish from being attempted", hosts)
	}

	// The failure is NAMED, not swallowed: the log carries why the connection
	// could not be held, the same way a failed publish's own log does.
	if logged := stand.logs.String(); !strings.Contains(logged, "could not hold the connection") {
		t.Fatalf("the failed keep-alive lease left no record of why; the log was %q", logged)
	}
	t.Logf("MEASURED a pane whose keep-alive lease could not be acquired: opened, and the record names why")
}
