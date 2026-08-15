package ssh

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
)

// ---------------------------------------------------------------------------
// Fake launcher — test double for the pinned RemoteLauncher contract
// (nocx-xs1d). Records every call and returns a scripted result.
// ---------------------------------------------------------------------------

type fakeLauncher struct {
	mu       sync.Mutex
	calls    int
	gotShell ShellKind
	gotOpts  LaunchOptions
	cmd      string
	reason   RefusalReason
	ok       bool
}

func (f *fakeLauncher) StartCommand(shell ShellKind, opts LaunchOptions) (cmd string, reason RefusalReason, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotShell = shell
	f.gotOpts = opts
	return f.cmd, f.reason, f.ok
}

func (f *fakeLauncher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLauncher) lastCall() (ShellKind, LaunchOptions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotShell, f.gotOpts
}

// recordInstaller is the RemoteInstaller double for the desired-mode
// matrix (nocx-mlm7). It records every call so a test can assert the
// publish happens under script and never under raw/relay, and returns a
// canned home and start command.
type recordInstaller struct {
	mu           sync.Mutex
	homeCalls    int
	publishCalls int
	cmdCalls     int
	home         string
	cmd          string
}

func (f *recordInstaller) GetRemoteHome(_ *gossh.Client) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.homeCalls++
	return f.home, nil
}

func (f *recordInstaller) EnsureInstalledRemote(_ context.Context, _ *gossh.Client, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishCalls++
	return nil
}

func (f *recordInstaller) RemoteStartCommand() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmdCalls++
	return f.cmd
}

func (f *recordInstaller) UninstallRemote(_ context.Context, _ *gossh.Client, _ string) ([]string, []string, error) {
	return nil, nil, nil
}

// testSSHServer accessors for start-command observations.
//
// lastExecCommand is the NON-BLOCKING read, and it exists for exactly one
// question: "was no exec requested?" Every other caller wants waitExecCommand
// below.
func (s *testSSHServer) lastExecCommand() string {
	select {
	case cmd := <-s.execCommands:
		return cmd
	default:
		return ""
	}
}

// waitExecCommand blocks — bounded — until the server has recorded a start
// command, and is what a test asserting WHICH command was sent must use.
//
// The client returning from Connect does not mean the server has processed
// the exec request: the request travels, and the server's handler goroutine
// pushes onto execCommands when it gets there. Reading the channel
// non-blockingly at that moment is a race between two goroutines with no
// ordering between them, and the losing read returns "" — which then reads
// as "the wrong command was sent" rather than as "nobody has looked yet".
//
// It passed on this developer's Mac and on the runner and failed roughly one
// run in five in the CI-equivalent container. Per the 2026-08-11 decision in
// AGENTS.md a test waits on an observable state change rather than assuming
// one has already happened.
func (s *testSSHServer) waitExecCommand(t *testing.T) string {
	t.Helper()
	select {
	case cmd := <-s.execCommands:
		return cmd
	case <-time.After(10 * time.Second):
		t.Fatal("the server recorded no start command")
		return ""
	}
}

func (s *testSSHServer) execCommandCount() int {
	return len(s.execCommands)
}

func (s *testSSHServer) shellRequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shellRequests
}

// ---------------------------------------------------------------------------
// openShell start-command matrix
// ---------------------------------------------------------------------------

// launcherConnect opens a real connection to the test server, returning the
// channel. The client and channel are cleaned up with the test.
func launcherConnect(t *testing.T, srv *testSSHServer, rcOpts []RealClientOption, opts ...ConnectOption) Channel {
	t.Helper()
	khPath := writeKnownHosts(t, srv, srv.addr)
	rcOpts = append([]RealClientOption{WithKnownHostsFile(khPath)}, rcOpts...)
	client, err := NewReal(log.NewSlogAdapter(nil), rcOpts...)
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	base := []ConnectOption{
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
		WithPTYSize(80, 24, 0, 0),
	}
	opts = append(base, opts...)
	ch, err := client.Connect(context.Background(), srv.addr, opts...)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	return ch
}

// assertUsable proves the session is an ordinary terminal after the start:
// the far end accepted the start request and echoes writes back.
func assertUsable(t *testing.T, srv *testSSHServer, ch Channel) {
	t.Helper()
	<-srv.shellReady
	if _, err := ch.Write([]byte("hello")); err != nil {
		t.Fatalf("Write after start: %v", err)
	}
	buf := make([]byte, 32)
	n, err := ch.Read(buf)
	if err != nil {
		t.Fatalf("Read after start: %v", err)
	}
	if got := string(buf[:n]); got != "echo:hello" {
		t.Fatalf("session not usable: expected echo:hello, got %q", got)
	}
}

func TestConnect_LauncherAccepted_StartUsesItsCommand(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	wantCmd := "exec bash --rcfile <(printf %b 'x') -i"
	launcher := &fakeLauncher{cmd: wantCmd, reason: ReasonNone, ok: true}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithSessionID("sess-abc123"),
		WithEnhanced(),
	)

	assertUsable(t, srv, ch)

	if got := srv.waitExecCommand(t); got != wantCmd {
		t.Errorf("session.Start received %q, want the launcher's command %q", got, wantCmd)
	}
	if srv.shellRequestCount() != 0 {
		t.Errorf("plain shell started alongside the launcher command (%d shell requests)", srv.shellRequestCount())
	}
	if n := launcher.callCount(); n != 1 {
		t.Fatalf("launcher called %d times, want 1", n)
	}
	shell, opts := launcher.lastCall()
	if shell != ShellAuto {
		t.Errorf("launcher received shell %q, want %q (no pin → the far host detects itself)", shell, ShellAuto)
	}
	if opts.SessionID != "sess-abc123" {
		t.Errorf("launcher received SessionID %q, want sess-abc123", opts.SessionID)
	}
	if !opts.Enhanced {
		t.Error("launcher received Enhanced=false, want true (marker-only was requested)")
	}
	if got := ch.ShellIntegrationReason(); got != ReasonNone {
		t.Errorf("ShellIntegrationReason = %q, want %q", got, ReasonNone)
	}
}

// TestConnect_DesiredModeRaw_OpensPlainShell: raw adds nothing (N1, §3.1) —
// the launcher and the installer are wired but NOT consulted, no exec
// command starts, no publish happens, and the reason stays none
// (integration was never attempted).
func TestConnect_DesiredModeRaw_OpensPlainShell(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	installer := &recordInstaller{home: "/home/test", cmd: "exec bash -i"}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteInstaller(installer),
		WithDesiredMode("raw"),
		WithSessionID("sess-raw"),
		WithEnhanced(),
	)

	assertUsable(t, srv, ch)

	if n := launcher.callCount(); n != 0 {
		t.Fatalf("launcher consulted %d times under raw, want 0 (plain shell at open)", n)
	}
	if installer.homeCalls != 0 || installer.publishCalls != 0 {
		t.Fatalf("installer consulted under raw (home=%d publish=%d), want 0 — raw publishes nothing",
			installer.homeCalls, installer.publishCalls)
	}
	if got := srv.lastExecCommand(); got != "" {
		t.Errorf("session.Start received %q under raw, want a plain shell request", got)
	}
	if srv.shellRequestCount() != 1 {
		t.Errorf("shell requests = %d, want 1 (the plain shell)", srv.shellRequestCount())
	}
	if got := ch.ShellIntegrationReason(); got != ReasonNone {
		t.Errorf("ShellIntegrationReason = %q, want %q (never attempted)", got, ReasonNone)
	}
}

// TestConnect_DesiredModeRelay_IntegratesLikeScript: relay publishes the
// bundle and integrates, exactly as script and auto do. The tiers are
// additive, not alternative (§5.2): allowing the deployed binary never
// withholds the shell scripts, any more than declining it would.
//
// This test asserted the opposite until nocx-7k8ma, and was right when it
// was written: `relay` then named the Tier-B carrier of §3.4, which would
// have delivered integration itself. That carrier is still unbuilt (D15),
// the mode meanwhile came to mean "allow the remote helper", and the helper
// owns no PTY and emits no markers — so the old assertion had become a
// guarantee that the most capable mode delivered the least.
func TestConnect_DesiredModeRelay_IntegratesLikeScript(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	installer := &recordInstaller{home: "/home/test", cmd: "exec bash -i"}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteInstaller(installer),
		WithDesiredMode("relay"),
	)

	assertUsable(t, srv, ch)

	if n := launcher.callCount(); n == 0 {
		t.Fatal("launcher never consulted under relay — the session opened a plain shell, " +
			"so a user who allowed the helper silently lost their blocks")
	}
	if installer.homeCalls == 0 || installer.publishCalls == 0 {
		t.Fatalf("installer not consulted under relay (home=%d publish=%d), want both — "+
			"the bundle must publish for relay exactly as it does for script",
			installer.homeCalls, installer.publishCalls)
	}
	if got := ch.ShellIntegrationReason(); got != ReasonNone {
		t.Errorf("ShellIntegrationReason = %q, want %q (integration was attempted and not refused)", got, ReasonNone)
	}
}

// TestConnect_DesiredModeEmpty_IntegratesLikeScript: an empty mode is the
// pre-mode default (and the direct-host default) — every existing caller
// without a mode keeps integrating at startup. This pins the
// backwards-compatible reading of the field, so adding it cannot silently
// change an unconfigured connection.
func TestConnect_DesiredModeEmpty_IntegratesLikeScript(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithDesiredMode(""),
	)

	assertUsable(t, srv, ch)

	if n := launcher.callCount(); n != 1 {
		t.Fatalf("launcher consulted %d times with an empty mode, want 1 (script default)", n)
	}
	if got := srv.waitExecCommand(t); got != "exec bash -i" {
		t.Errorf("session.Start received %q, want the launcher command", got)
	}
}

// TestConnect_ProfileShellPin_BeatsDetection: a profile that pins the far
// shell must win over detection — the launcher receives the pinned kind,
// not ShellAuto, and the user's knowledge of the host is never overridden
// by what the dispatcher would conclude at the far end (nocx-6rj0).
func TestConnect_ProfileShellPin_BeatsDetection(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	wantCmd := "exec zsh -l"
	launcher := &fakeLauncher{cmd: wantCmd, reason: ReasonNone, ok: true}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithShell(ShellZsh),
		WithSessionID("sess-pin"),
		WithEnhanced(),
	)

	assertUsable(t, srv, ch)

	shell, opts := launcher.lastCall()
	if shell != ShellZsh {
		t.Errorf("launcher received shell %q, want the pinned %q", shell, ShellZsh)
	}
	if opts.SessionID != "sess-pin" {
		t.Errorf("launcher received SessionID %q, want sess-pin", opts.SessionID)
	}
	if got := ch.ShellIntegrationReason(); got != ReasonNone {
		t.Errorf("ShellIntegrationReason = %q, want %q", got, ReasonNone)
	}
}

// TestConnect_UnknownPin_GoesToMinimalTier: a profile that pins ShellUnknown
// ("this host is neither bash nor zsh") must reach the minimal tier
// directly — the pin is a decision, not a request to detect.
func TestConnect_UnknownPin_GoesToMinimalTier(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec /bin/sh -l", reason: ReasonNone, ok: true}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithShell(ShellUnknown),
	)

	assertUsable(t, srv, ch)

	shell, _ := launcher.lastCall()
	if shell != ShellUnknown {
		t.Errorf("launcher received shell %q, want the pinned %q", shell, ShellUnknown)
	}
}

func TestConnect_RemoteCommand_LauncherNeverCalled(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "must not run", reason: ReasonNone, ok: true}
	installer := &recordInstaller{home: "/home/test", cmd: "must not run"}
	stub := NewStubConfigResolver()
	stub.AddEntry(hostPortOnly(srv.addr), HostConfig{User: "test", RemoteCommand: "tmux attach -t work"})

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(stub)},
		WithRemoteLauncher(launcher),
		WithRemoteInstaller(installer),
		WithDesiredMode("script"),
		WithSessionID("sess-xyz"),
	)

	assertUsable(t, srv, ch)

	if n := launcher.callCount(); n != 0 {
		t.Fatalf("launcher called %d times with a RemoteCommand configured, want 0", n)
	}
	if installer.homeCalls != 0 || installer.publishCalls != 0 {
		t.Fatalf("installer consulted with a RemoteCommand configured (home=%d publish=%d), want 0 — the configured command wins outright",
			installer.homeCalls, installer.publishCalls)
	}
	if got := srv.waitExecCommand(t); got != "tmux attach -t work" {
		t.Errorf("session.Start received %q, want the configured RemoteCommand", got)
	}
	if got := ch.ShellIntegrationReason(); got != ReasonRemoteCommand {
		t.Errorf("ShellIntegrationReason = %q, want %q", got, ReasonRemoteCommand)
	}
}

func TestConnect_LauncherDeclines_PlainShellReasonPropagated(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{reason: ReasonUnsupportedShell, ok: false}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
	)

	assertUsable(t, srv, ch)

	if n := launcher.callCount(); n != 1 {
		t.Fatalf("launcher called %d times, want 1", n)
	}
	if got := srv.execCommandCount(); got != 0 {
		t.Errorf("launcher declined but %d exec(s) were sent; want a plain shell", got)
	}
	if got := srv.shellRequestCount(); got != 1 {
		t.Errorf("shell requests = %d, want 1 (plain shell fallback)", got)
	}
	if got := ch.ShellIntegrationReason(); got != ReasonUnsupportedShell {
		t.Errorf("ShellIntegrationReason = %q, want %q", got, ReasonUnsupportedShell)
	}
}

func TestConnect_LauncherDegenerate_FallsBackToPlainShell(t *testing.T) {
	// The pinned StartCommand has no error return: a launcher that refuses
	// without a reason, or "accepts" with no command, is a contract violation
	// and must not produce a dead or empty exec. Both shapes fall back to a
	// plain shell with the reason normalized.
	cases := []struct {
		name   string
		cmd    string
		reason RefusalReason
		ok     bool
	}{
		{"refuses with empty reason", "", "", false},
		{"accepts with empty command", "", ReasonNone, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startTestSSHServer(t)
			defer srv.close()

			launcher := &fakeLauncher{cmd: tc.cmd, reason: tc.reason, ok: tc.ok}
			ch := launcherConnect(
				t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
				WithRemoteLauncher(launcher),
			)

			assertUsable(t, srv, ch)

			if got := srv.execCommandCount(); got != 0 {
				t.Errorf("degenerate launcher result produced %d exec(s); want plain shell", got)
			}
			if got := srv.shellRequestCount(); got != 1 {
				t.Errorf("shell requests = %d, want 1", got)
			}
			if got := ch.ShellIntegrationReason(); got != ReasonUnsupportedShell {
				t.Errorf("ShellIntegrationReason = %q, want normalized %q", got, ReasonUnsupportedShell)
			}
		})
	}
}

// TestConnect_DesiredModeScript_PublishesThenLaunches: script mode is the
// N3 default — a saved connection publishes the bundle over SFTP before
// the session starts, and the launcher's command is what runs. The old
// "installer never consulted when the launcher is wired" precedence is
// gone: the publish happens first, the launcher decides the command.
func TestConnect_DesiredModeScript_PublishesThenLaunches(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	wantCmd := "exec bash -i"
	launcher := &fakeLauncher{cmd: wantCmd, reason: ReasonNone, ok: true}
	installer := &recordInstaller{home: "/home/test", cmd: "if [ -x \"$HOME/.nocx/launch\" ]; then exec \"$HOME/.nocx/launch\"; else exec \"${SHELL:-/bin/sh}\" -l; fi"}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteInstaller(installer),
		WithDesiredMode("script"),
	)

	assertUsable(t, srv, ch)

	if n := launcher.callCount(); n != 1 {
		t.Fatalf("launcher consulted %d times under script, want 1", n)
	}
	if installer.homeCalls != 1 || installer.publishCalls != 1 {
		t.Errorf("publish under script: home=%d publish=%d, want 1 each",
			installer.homeCalls, installer.publishCalls)
	}
	if got := srv.waitExecCommand(t); got != wantCmd {
		t.Errorf("session.Start received %q, want the launcher command %q", got, wantCmd)
	}
	if got := ch.ShellIntegrationReason(); got != ReasonNone {
		t.Errorf("ShellIntegrationReason = %q, want %q", got, ReasonNone)
	}
}

// TestConnect_DesiredModeScript_NoLauncher_UsesInstallerCommand: script
// mode with only the carrier wired publishes and then runs the carrier's
// own start command — the §3.3 far-side guard.
func TestConnect_DesiredModeScript_NoLauncher_UsesInstallerCommand(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	wantCmd := "if [ -x \"$HOME/.nocx/launch\" ]; then exec \"$HOME/.nocx/launch\"; else exec \"${SHELL:-/bin/sh}\" -l; fi"
	installer := &recordInstaller{home: "/home/test", cmd: wantCmd}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteInstaller(installer),
		WithDesiredMode("script"),
	)

	assertUsable(t, srv, ch)

	if installer.publishCalls != 1 || installer.cmdCalls != 1 {
		t.Errorf("carrier calls: publish=%d cmd=%d, want 1 each", installer.publishCalls, installer.cmdCalls)
	}
	if got := srv.waitExecCommand(t); got != wantCmd {
		t.Errorf("session.Start received %q, want the carrier's guard %q", got, wantCmd)
	}
}

func TestConnect_NoLauncherNoRemoteCommand_PlainShellNoReason(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	ch := launcherConnect(t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())})

	assertUsable(t, srv, ch)

	if got := srv.execCommandCount(); got != 0 {
		t.Errorf("plain-shell default sent %d exec(s)", got)
	}
	if got := ch.ShellIntegrationReason(); got != ReasonNone {
		t.Errorf("ShellIntegrationReason = %q, want %q (no integration attempted)", got, ReasonNone)
	}
}
