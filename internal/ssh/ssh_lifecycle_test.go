package ssh

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/log"
)

// fakeRemoteLifecycle is the RemoteLifecycle double: it returns a scripted
// launch (or a refusal) and records the establishment call and whether the
// returned closer was closed.
type fakeRemoteLifecycle struct {
	mu         sync.Mutex
	refuse     error
	launch     RemoteLifecycleLaunch
	closed     bool
	estabCalls int
	estabCtx   context.Context
}

func (f *fakeRemoteLifecycle) Establish(ctx context.Context, _ string, _ ...ConnectOption) (RemoteLifecycleLaunch, io.Closer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.estabCalls++
	f.estabCtx = ctx
	if f.refuse != nil {
		return RemoteLifecycleLaunch{}, nil, f.refuse
	}
	return f.launch, closeFunc(func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closed = true
		return nil
	}), nil
}

func (f *fakeRemoteLifecycle) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeRemoteLifecycle) establishCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.estabCalls
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

// TestConnect_LifecycleLaunchReachesLauncher proves the composition seam
// (ADR-0024 decision 2 "Over SSH"): with a lifecycle provider wired, the
// launch config — the allocated port and the per-epoch capability — reaches
// the launcher's LaunchOptions, and the channel owns the closer until the
// session ends.
func TestConnect_LifecycleLaunchReachesLauncher(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	wantLaunch := RemoteLifecycleLaunch{
		Lane: "lane-1", Domain: "dom-1", Epoch: 7, Port: 4242,
		Capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	lc := &fakeRemoteLifecycle{launch: wantLaunch}
	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteLifecycle(lc),
		WithSessionID("sess-abc123"),
		WithEnhanced(),
	)

	assertUsable(t, srv, ch)

	_, opts := launcher.lastCall()
	if opts.Lane != wantLaunch.Lane || opts.Domain != wantLaunch.Domain ||
		opts.Epoch != wantLaunch.Epoch || opts.LifecyclePort != wantLaunch.Port ||
		opts.Capability != wantLaunch.Capability {
		t.Fatalf("launcher options = %+v, want the lifecycle launch embedded", opts)
	}

	// The channel owns the closer: closing the session releases the lease.
	_ = ch.Close()
	if !lc.wasClosed() {
		t.Fatal("lifecycle closer must run when the channel closes")
	}
}

// TestConnect_LifecycleRefusalStaysConventional proves the refusal contract:
// a host whose sshd will not forward (AllowTcpForwarding off, bind outside
// PermitListen) produces a session with NO channel config — the launcher
// receives empty lifecycle fields and the shell stays conventional. The
// refusal is detectable synchronously and leaves no closer to leak.
func TestConnect_LifecycleRefusalStaysConventional(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	lc := &fakeRemoteLifecycle{refuse: errors.New("ssh: tcpip-forward denied")}
	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteLifecycle(lc),
		WithSessionID("sess-abc123"),
		WithEnhanced(),
	)

	assertUsable(t, srv, ch)

	_, opts := launcher.lastCall()
	if opts.Capability != "" || opts.Lane != "" || opts.Domain != "" ||
		opts.Epoch != 0 || opts.LifecyclePort != 0 {
		t.Fatalf("refused session must carry no channel config, got %+v", opts)
	}
	_ = ch.Close()
	if lc.wasClosed() {
		t.Fatal("a refused lifecycle must not leave a closer to close")
	}
}

// TestConnect_LifecycleRawMode_NeverEstablished proves the mode half of
// the gate (nocx-tr2n). raw integrates nothing — shellStartCommand returns
// before the launcher is consulted and the session runs a plain shell that
// will never dial the forwarded port. Establishing a channel for it would
// allocate a remote listener and a domain that the very next statement
// closes unused. Every session asks for integration now, so this is a raw
// tab's cost per open, not a path nothing takes.
//
// helper used to be in this table and moved to the positive one (nocx-7k8ma):
// it integrates, so it needs the channel. raw is now the only mode here,
// which is the point — it is the only answer that means "nothing".
func TestConnect_LifecycleRawMode_NeverEstablished(t *testing.T) {
	for _, mode := range []string{"raw"} {
		t.Run(mode, func(t *testing.T) {
			srv := startTestSSHServer(t)
			defer srv.close()

			lc := &fakeRemoteLifecycle{launch: RemoteLifecycleLaunch{
				Lane: "lane-1", Domain: "dom-1", Epoch: 7, Port: 4242,
				Capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}}
			launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}

			ch := launcherConnect(
				t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
				WithRemoteLauncher(launcher),
				WithRemoteLifecycle(lc),
				WithSessionID("sess-abc123"),
				WithDesiredMode(mode),
				WithEnhanced(),
			)

			assertUsable(t, srv, ch)
			if n := lc.establishCalls(); n != 0 {
				t.Fatalf("%s destination established %d lifecycle channels, want 0", mode, n)
			}
			if n := launcher.callCount(); n != 0 {
				t.Fatalf("%s destination consulted the launcher %d times, want 0", mode, n)
			}
		})
	}
}

// TestConnect_LifecycleScriptMode_Established is the paired positive: the
// mode gate must not swallow the integrating destinations it was added
// beside. auto (the default), script, helper and the empty direct-host
// default all establish — every mode but raw, because the tiers are
// additive and helper allows the binary without withholding the scripts
// (§5.2, nocx-7k8ma).
func TestConnect_LifecycleScriptMode_Established(t *testing.T) {
	for _, mode := range []string{"auto", "script", "helper", ""} {
		t.Run("mode="+mode, func(t *testing.T) {
			srv := startTestSSHServer(t)
			defer srv.close()

			lc := &fakeRemoteLifecycle{launch: RemoteLifecycleLaunch{
				Lane: "lane-1", Domain: "dom-1", Epoch: 7, Port: 4242,
				Capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			}}
			launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}

			ch := launcherConnect(
				t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
				WithRemoteLauncher(launcher),
				WithRemoteLifecycle(lc),
				WithSessionID("sess-abc123"),
				WithDesiredMode(mode),
				WithEnhanced(),
			)

			assertUsable(t, srv, ch)
			if n := lc.establishCalls(); n != 1 {
				t.Fatalf("mode %q established %d lifecycle channels, want 1", mode, n)
			}
			if _, opts := launcher.lastCall(); opts.LifecyclePort != 4242 {
				t.Fatalf("mode %q: launcher got port %d, want the established 4242", mode, opts.LifecyclePort)
			}
		})
	}
}

// TestConnect_LifecycleNotWired_NoChannel proves the default: without a
// provider, no channel is established and no launch config reaches the
// launcher — the pre-ADR behavior stays byte-identical.
func TestConnect_LifecycleNotWired_NoChannel(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithSessionID("sess-abc123"),
		WithEnhanced(),
	)

	assertUsable(t, srv, ch)
	_, opts := launcher.lastCall()
	if opts.Capability != "" || opts.LifecyclePort != 0 {
		t.Fatalf("unwired lifecycle must carry no channel config, got %+v", opts)
	}
}

// TestConnect_LifecycleLauncherDecline_CloserClosed proves the decline path:
// when the launcher declines, openShell opens a plain shell and the
// established channel is closed — it must not leak a listener or a lease.
func TestConnect_LifecycleLauncherDecline_CloserClosed(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	lc := &fakeRemoteLifecycle{launch: RemoteLifecycleLaunch{
		Lane: "lane-1", Domain: "dom-1", Epoch: 7, Port: 4242,
		Capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	declining := &fakeLauncher{cmd: "", reason: ReasonUnsupportedShell, ok: false}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(declining),
		WithRemoteLifecycle(lc),
		WithSessionID("sess-abc123"),
		WithEnhanced(),
	)

	assertUsable(t, srv, ch)
	if !lc.wasClosed() {
		t.Fatal("launcher decline must close the established lifecycle channel")
	}
	if reason := ch.ShellIntegrationReason(); reason != ReasonUnsupportedShell {
		t.Fatalf("decline reason = %q, want %q", reason, ReasonUnsupportedShell)
	}
}

// TestConnect_LifecycleRemoteCommand_CloserClosed proves the remote-command
// path: a configured remote command wins outright, so the established
// channel must be closed rather than left holding a listener no shell will
// ever connect to.
func TestConnect_LifecycleRemoteCommand_CloserClosed(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	lc := &fakeRemoteLifecycle{launch: RemoteLifecycleLaunch{
		Lane: "lane-1", Domain: "dom-1", Epoch: 7, Port: 4242,
		Capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}

	stub := NewStubConfigResolver()
	stub.AddEntry(hostPortOnly(srv.addr), HostConfig{User: "test", RemoteCommand: "tmux attach -t work"})

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(stub)},
		WithRemoteLauncher(launcher),
		WithRemoteLifecycle(lc),
		WithSessionID("sess-abc123"),
		WithEnhanced(),
	)

	if got := srv.waitExecCommand(t); got != "tmux attach -t work" {
		t.Fatalf("remote command must win, got %q", got)
	}
	if !lc.wasClosed() {
		t.Fatal("remote-command session must close the established lifecycle channel")
	}
	_ = ch.Close()
}

// compile-time: the real client can drive the seam.
var (
	_ = gossh.PublicKeys
	_ = log.NewSlogAdapter
)
