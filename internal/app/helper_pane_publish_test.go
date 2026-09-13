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
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/storage/storagetest"
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
	// logs collects what the opener said. It is the surface a publish failure
	// has on this route, and the design says so in as many words (§6.1: the
	// publish's result "stays a LOG and never an input to which command is
	// emitted") — the far side is what names the refusal in the product.
	logs *bytes.Buffer
}

// startPaneStand stands up this machine's daemon and an opener pointed at it.
func startPaneStand(t *testing.T) *paneStand {
	t.Helper()
	home := storagetest.IsolateWithHome(t)
	gen := fakeArtifacts{payload: syntheticPayload}.hash()
	daemon := startFakeLocalEndpoint(t, endpoint.Dir(home), gen)

	slogger := log.NewSlogAdapter(discardLogger())
	reg := session.New(slogger, &reachPTYFactory{stub: pty.NewStub(slogger)})
	rc, err := ssh.NewReal(slogger, ssh.WithKnownHostsFile(home+"/known_hosts"))
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

// openSSH opens one ssh pane through this machine's helper, with the installer
// (or its absence) a caller names.
func (s *paneStand) openSSH(t *testing.T, mode string, installer ssh.RemoteInstaller) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	opened, selected, err := s.opener.OpenHosted(ctx, session.Config{
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
	}, "")
	if err == nil {
		_ = opened.Session.Close()
	}
	if !selected {
		t.Fatalf("this machine's opener declined an ssh pane (selected=false), which ADR-0057 forbids: " +
			"a destination it owns is always its own, including when it cannot be served")
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
// usable terminal the one thing no failure path may suppress. What the failure
// does is reach the record with its own name, which is what the log carries.
func TestAPaneOpensEvenWhenItsPublishFailed(t *testing.T) {
	stand := startPaneStand(t)
	cause := errors.New(
		"publish the shell integration bundle on host.example: the far side named no home directory")
	installer := &paneInstaller{daemon: stand.daemon, err: cause}

	if err := stand.openSSH(t, "script", installer); err != nil {
		t.Fatalf("a failed publish refused the pane: %v — an un-integrated terminal is what §6.1 step 5 "+
			"promises instead, and a pane nobody can use is not a visible degrade", err)
	}
	if got := stand.daemon.spawned(); got != 1 {
		t.Fatalf("the daemon was asked to spawn %d sessions, want 1", got)
	}

	// The failure is NAMED, not swallowed: the log carries the cause, so the
	// host that could not say where its home is can be found from the run.
	if logged := stand.logs.String(); !strings.Contains(logged, "host.example") ||
		!strings.Contains(logged, "the far side named no home directory") {
		t.Fatalf("the publish failure produced no record naming the host and its cause; the log was %q",
			logged)
	}
	t.Logf("MEASURED the fail-open publish: pane opened, and the record names the cause")
}
