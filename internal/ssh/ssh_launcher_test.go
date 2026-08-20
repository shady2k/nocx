package ssh

import (
	"context"
	"errors"
	"strings"
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
	mu    sync.Mutex
	calls int
	// prepareFails makes Prepare decline; blockBootstrap holds the run
	// until the test closes it.
	prepareFails   bool
	blockBootstrap chan struct{}
	gotShell       ShellKind
	gotOpts        LaunchOptions
	cmd            string
	reason         RefusalReason
	ok             bool
	// bootstrapReason is what the run reports as the bootstrap's terminal
	// outcome. ReasonNone (the zero value) is "it integrated".
	bootstrapReason RefusalReason
	// gate is the §6.1 gate Prepare handed back, kept so a test can read
	// which facts the ssh side reported into it.
	gate *recordingGate
}

// waitBootstrapped blocks until the input quarantine has closed. "Usable"
// means the session is out of the bootstrap interval — before that, refusing
// a keystroke is the CONTRACT (design §5.3), not a defect, so a test that
// wrote without waiting would be asserting against the feature.
func waitBootstrapped(t *testing.T, ch Channel) {
	t.Helper()
	rc, ok := ch.(*RealChannel)
	if !ok || rc.bootstrapDone == nil {
		return
	}
	select {
	case <-rc.bootstrapDone:
	case <-rc.done:
	case <-time.After(30 * time.Second):
		// A failsafe against a hang, never the thing being measured.
		t.Fatal("the bootstrap interval never closed")
	}
}

// Prepare is the launcher's other half: the bootstrap. The stub returns a
// digest and a run that finishes at once — this file's subject is WHICH
// COMMAND is sent and what the far side records, not the handshake, which
// internal/shellintegration proves against a real loader on a real terminal.
//
// prepareFails and blockBootstrap are how a test states the two cases this
// side owns: a bootstrap that cannot be prepared (no command may be emitted
// at all) and one that is still running (the input quarantine is closed).
func (f *fakeLauncher) Prepare(shell ShellKind, opts LaunchOptions) (string, BootstrapRun, BootstrapGate, bool) {
	f.mu.Lock()
	fails, block := f.prepareFails, f.blockBootstrap
	f.mu.Unlock()
	if fails {
		return "", nil, nil, false
	}
	gate := &recordingGate{}
	f.mu.Lock()
	f.gate = gate
	f.mu.Unlock()
	return strings.Repeat("ab", 32), func(ctx context.Context, s BootstrapStream) RefusalReason {
		if block != nil {
			select {
			case <-block:
			case <-ctx.Done():
			}
		}
		f.mu.Lock()
		reason := f.bootstrapReason
		f.mu.Unlock()
		return reason
	}, gate, true
}

// recordingGate records the two §6.1 facts the ssh side reports, so a test can
// assert the ORDER and the concurrency rather than a duration.
type recordingGate struct {
	mu           sync.Mutex
	receiver     string // "", "ready" or "unavailable"
	publish      bool
	publishErr   error
	settledFirst bool // the publish settled before the receiver was answered
}

func (g *recordingGate) ReceiverReady() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.receiver == "" {
		g.receiver = "ready"
		g.settledFirst = g.publish
	}
}

func (g *recordingGate) ReceiverUnavailable(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.receiver == "" {
		g.receiver = "unavailable"
		g.settledFirst = g.publish
	}
}

func (g *recordingGate) PublishSettled(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.publish = true
	g.publishErr = err
}

func (g *recordingGate) snapshot() (receiver string, publish bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.receiver, g.publish, g.publishErr
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
// publish happens under script and never under raw/relay, and can be told
// to fail the publish so a test can prove the command does not depend on
// the outcome.
type recordInstaller struct {
	mu           sync.Mutex
	homeCalls    int
	publishCalls int
	publishErr   error
	home         string
	// published is closed by the first publish. The publish runs
	// CONCURRENTLY with the loader now (design §6.1 step 2, §7's parallel
	// schedule), so "did it publish" is a question a test answers by waiting
	// on the event rather than by reading a counter at a moment of its own
	// choosing — which was both a race and, under -race, a report.
	published chan struct{}
}

// waitPublished blocks until the first publish has been made, and fails rather
// than hanging if none is. The timeout is a failsafe against a hang and never
// the thing being measured.
func (f *recordInstaller) waitPublished(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	if f.published == nil {
		f.published = make(chan struct{})
	}
	ch, already := f.published, f.publishCalls > 0
	f.mu.Unlock()
	if already {
		return
	}
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatal("the publish never ran")
	}
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
	if f.published == nil {
		f.published = make(chan struct{})
	}
	if f.publishCalls == 1 {
		close(f.published)
	}
	return f.publishErr
}

// publishCount is the counter read under the lock. Every reader takes the
// lock now: the publish runs on its own goroutine.
func (f *recordInstaller) publishCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.publishCalls
}

// counts is the pair, read together under one lock.
func (f *recordInstaller) counts() (home, publish int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.homeCalls, f.publishCalls
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
	waitBootstrapped(t, ch)
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
	installer := &recordInstaller{home: "/home/test"}

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
	if h, p := installer.counts(); h != 0 || p != 0 {
		t.Fatalf("installer consulted under raw (home=%d publish=%d), want 0 — raw publishes nothing",
			h, p)
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

// TestConnect_DesiredModeRelay_OpensPlainShell: relay behaves as raw in
// this epic — no publish, no launcher, plain shell, reason none. Its
// consent gating lands with the relay binary (design §3.4).
func TestConnect_DesiredModeRelay_OpensPlainShell(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true}
	installer := &recordInstaller{home: "/home/test"}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithRemoteInstaller(installer),
		WithDesiredMode("relay"),
	)

	assertUsable(t, srv, ch)

	if n := launcher.callCount(); n != 0 {
		t.Fatalf("launcher consulted %d times under relay, want 0 (plain shell at open)", n)
	}
	if h, p := installer.counts(); h != 0 || p != 0 {
		t.Fatalf("installer consulted under relay (home=%d publish=%d), want 0 — relay behaves as raw this epic",
			h, p)
	}
	if got := ch.ShellIntegrationReason(); got != ReasonNone {
		t.Errorf("ShellIntegrationReason = %q, want %q (never attempted)", got, ReasonNone)
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
	installer := &recordInstaller{home: "/home/test"}
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
	if h, p := installer.counts(); h != 0 || p != 0 {
		t.Fatalf("installer consulted with a RemoteCommand configured (home=%d publish=%d), want 0 — the configured command wins outright",
			h, p)
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
	installer := &recordInstaller{home: "/home/test"}

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
	installer.waitPublished(t)
	if h, p := installer.counts(); h != 1 || p != 1 {
		t.Errorf("publish under script: home=%d publish=%d, want 1 each",
			h, p)
	}
	if got := srv.waitExecCommand(t); got != wantCmd {
		t.Errorf("session.Start received %q, want the launcher command %q", got, wantCmd)
	}
	if got := ch.ShellIntegrationReason(); got != ReasonNone {
		t.Errorf("ShellIntegrationReason = %q, want %q", got, ReasonNone)
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

// TestConnect_SameCarrierWhateverThePublishDid is design §11's assertion 5:
// the publish result is reported and not consulted. One case each —
// publish succeeded, publish failed, publish never attempted — and the exec
// request the far side records must be byte-identical in all three, reached
// through identical LaunchOptions.
//
// The defect this pins is not hypothetical arithmetic: the command used to
// open with a far-side test of installation state, so on a host with nothing
// committed the test ran while the publish was still in flight, failed, and
// degraded the session that the publish was in the middle of enabling.
func TestConnect_SameCarrierWhateverThePublishDid(t *testing.T) {
	const wantCmd = "/usr/bin/env -u BASH_ENV /bin/sh -c 'loader' nocx-loader"

	cases := []struct {
		name      string
		installer *recordInstaller
	}{
		{"publish succeeded", &recordInstaller{home: "/home/test"}},
		{"publish failed", &recordInstaller{home: "/home/test", publishErr: errors.New("sftp refused")}},
		{"publish not attempted", nil},
	}

	var commands []string
	var seen []LaunchOptions
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := startTestSSHServer(t)
			defer srv.close()

			launcher := &fakeLauncher{cmd: wantCmd, reason: ReasonNone, ok: true}
			opts := []ConnectOption{
				WithRemoteLauncher(launcher),
				WithDesiredMode("script"),
				WithSessionID("sess-carrier-parity"),
				WithEnhanced(),
			}
			if tc.installer != nil {
				opts = append(opts, WithRemoteInstaller(tc.installer))
			}
			ch := launcherConnect(t, srv,
				[]RealClientOption{WithConfigResolver(NewStubConfigResolver())}, opts...)

			assertUsable(t, srv, ch)

			if tc.installer != nil {
				tc.installer.waitPublished(t)
			}
			if tc.installer != nil && tc.installer.publishCount() != 1 {
				t.Errorf("publish calls = %d, want 1 — the publish still happens, it is "+
					"only its OUTCOME that stops deciding anything", tc.installer.publishCount())
			}
			if n := launcher.callCount(); n != 1 {
				t.Fatalf("launcher consulted %d times, want 1", n)
			}
			got := srv.waitExecCommand(t)
			if got != wantCmd {
				t.Errorf("session.Start received %q, want the carrier %q", got, wantCmd)
			}
			if got := ch.ShellIntegrationReason(); got != ReasonNone {
				t.Errorf("ShellIntegrationReason = %q, want %q", got, ReasonNone)
			}
			_, lopts := launcher.lastCall()
			commands = append(commands, got)
			seen = append(seen, lopts)
		})
	}

	if len(commands) != len(cases) {
		t.Fatalf("only %d of %d cases recorded a command", len(commands), len(cases))
	}
	for i := 1; i < len(commands); i++ {
		if commands[i] != commands[0] {
			t.Errorf("case %q emitted %q; case %q emitted %q — the publish result "+
				"must not change the command", cases[i].name, commands[i], cases[0].name, commands[0])
		}
		if seen[i] != seen[0] {
			t.Errorf("case %q built LaunchOptions %+v; case %q built %+v",
				cases[i].name, seen[i], cases[0].name, seen[0])
		}
	}
}

// TestConnect_InstallerAloneRunsNoCommand: the installer no longer answers
// "what should this session run". The §3.3 far-side guard it used to supply
// is retired with the carrier design, and nothing replaced it on this arm —
// so a connection with a publisher and no launcher publishes and then opens
// an ordinary plain shell.
func TestConnect_InstallerAloneRunsNoCommand(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	installer := &recordInstaller{home: "/home/test"}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteInstaller(installer),
		WithDesiredMode("script"),
	)

	assertUsable(t, srv, ch)
	installer.waitPublished(t)

	if got := installer.publishCount(); got != 1 {
		t.Errorf("publish calls = %d, want 1", got)
	}
	if got := srv.execCommandCount(); got != 0 {
		t.Errorf("%d exec request(s) sent; the installer supplies no command any more", got)
	}
	if got := srv.shellRequestCount(); got != 1 {
		t.Errorf("shell requests = %d, want 1 (the plain shell)", got)
	}
}

// The bound is at the seam, not at the builder (nocx-e4ir3).
//
// A launcher is a seam, and a seam is something somebody else implements
// next. The ~92 KiB command that carried the integration bundle and two
// bearers was produced by a builder that was checking itself against a cap
// beside it; the transport accepted whatever it was handed. This asserts the
// half that does not depend on the producer: an over-long command is refused
// HERE, before session.Start, and the session still fails open to a plain
// login shell with a named reason — ADR-0004's contract, not a dead session.
func TestConnect_LauncherCommandOverTheBound_NeverReachesTheWire(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: strings.Repeat("x", MaxRemoteCommandLen), reason: ReasonNone, ok: true}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
	)

	assertUsable(t, srv, ch)

	if got := srv.execCommandCount(); got != 0 {
		t.Errorf("%d exec(s) were sent; an over-long command must never reach the wire", got)
	}
	if got := srv.shellRequestCount(); got != 1 {
		t.Errorf("shell requests = %d, want 1 (the session still fails open to a plain shell)", got)
	}
	if got := ch.ShellIntegrationReason(); got != ReasonCommandTooLong {
		t.Errorf("ShellIntegrationReason = %q, want %q — a degrade the product cannot name is invisible", got, ReasonCommandTooLong)
	}
}

// And the paired success: the longest command the bound admits is still
// carried whole, unmodified. A bound that quietly shortens the command runs
// something the caller did not ask for on somebody else's machine.
func TestConnect_LauncherCommandJustUnderTheBound_IsCarriedWhole(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	wantCmd := strings.Repeat("x", MaxRemoteCommandLen-1)
	launcher := &fakeLauncher{cmd: wantCmd, reason: ReasonNone, ok: true}

	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
	)

	assertUsable(t, srv, ch)

	if got := srv.lastExecCommand(); got != wantCmd {
		t.Errorf("the server received a %d-byte command, want the %d bytes submitted", len(got), len(wantCmd))
	}
	if got := ch.ShellIntegrationReason(); got != ReasonNone {
		t.Errorf("ShellIntegrationReason = %q, want none", got)
	}
}
