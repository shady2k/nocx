package ssh

// §6.4's three `exec` rows, in the product (design §6.4 amendment A,
// assertion 23).
//
// The measurement that established them is exec_refusal_probe_test.go in this
// package and in internal/app; this file is what the product does about it.
// The rows differ in what the user is left holding, which is the only thing
// that matters to them:
//
//	refused, channel alive → a working prompt on the SAME channel;
//	refused, channel torn  → a working prompt on a replacement channel of the
//	                         SAME connection, at the cost of a second session
//	                         and no second authentication;
//	accepted, substituted  → no native prompt exists anywhere on that
//	                         connection, and saying otherwise would be a
//	                         promise we cannot keep.
//
// Every one of them is asserted with the server's own counters, so "no second
// authentication" is a number rather than a belief.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	gossh "golang.org/x/crypto/ssh"
)

// substitutedLauncher is the launcher a substituted exec actually meets: its
// bootstrap READS the stream, the way the real one does, and reports a
// refusal when the far side ends instead of speaking. The stock fakeLauncher
// returns without reading, which cannot observe the row at all.
type substitutedLauncher struct{ fakeLauncher }

// The gate is the stock one: this row is about which command the far side
// runs and what the user is left holding, not about §6.1's ordering, so the
// double records the two facts the ssh side reports and holds nothing back. A
// gate that never opens would suspend the mint and hang this test instead of
// failing it, which is not what a substituted exec does.
func (l *substitutedLauncher) Prepare(ShellKind, LaunchOptions) (string, BootstrapRun, BootstrapGate, bool) {
	gate := &recordingGate{}
	l.mu.Lock()
	l.gate = gate
	l.mu.Unlock()
	return strings.Repeat("ab", 32), func(ctx context.Context, st BootstrapStream) RefusalReason {
		// The same shape the real driver has: read until the far side says
		// the word, and give up when the stream ends instead. A substituted
		// command's output IS a line, so a driver that took the first line
		// for an answer would report success on the row it exists to
		// detect.
		for {
			line, err := st.ReadLine(ctx, 30*time.Second)
			if err != nil {
				return ReasonUnknown
			}
			if line == "NOCX1 LOADER_READY" {
				return ReasonNone
			}
		}
	}, gate, true
}

// waitBootstrapFinished blocks until the bootstrap goroutine has named its
// outcome. Unlike waitBootstrapped it does NOT also accept the session's end:
// the session ending is exactly the event this row races with.
func waitBootstrapFinished(t *testing.T, ch Channel) {
	t.Helper()
	rc, ok := ch.(*RealChannel)
	if !ok || rc.bootstrapDone == nil {
		t.Fatal("this channel ran no bootstrap; the substituted row is not observable on it")
	}
	select {
	case <-rc.bootstrapDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the bootstrap never named an outcome")
	}
}

func execRowCounts(s *testSSHServer) (refusals, substitutions, shells int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.execRefusals, s.execSubstitutions, s.shellRequests
}

// Row 3: the refusal does not take the channel or the pty already granted on
// it. The session reaches a working prompt on that same channel, with a named
// reason, one connection and one authentication.
func TestExecRow_RefusedWithTheChannelIntactReachesAPromptOnTheSameChannel(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	srv.setExecRefusal(execRefusedChannelSurvives, "")

	launcher := &fakeLauncher{cmd: "exec /nocx/loader", reason: ReasonNone, ok: true}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithSessionID("sess-execrefused"),
		WithEnhanced(),
	)

	if got := ch.ShellIntegrationReason(); got != ReasonExecRefused {
		t.Fatalf("reason %q, want %q — a refused exec is a named outcome, never a silent downgrade", got, ReasonExecRefused)
	}
	assertPromptOnChannel(t, srv, ch)

	refusals, substitutions, shells := execRowCounts(srv)
	if refusals != 1 || substitutions != 0 || shells != 1 {
		t.Fatalf("exec refusals=%d substitutions=%d shells=%d, want 1/0/1", refusals, substitutions, shells)
	}
	srv.waitLiveConns(1)
}

// Row 4's recoverable half: the server tears the channel down as it refuses.
// The prompt does not survive on that channel — but the CONNECTION does, and a
// replacement session channel on it needs no second authentication. That is
// what decides between conventional(reason) and session-failed(reason), and
// this row is the first.
func TestExecRow_RefusedWithTheChannelTornDownReachesAPromptOnAReplacementChannel(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	srv.setExecRefusal(execRefusedChannelClosed, "")

	launcher := &fakeLauncher{cmd: "exec /nocx/loader", reason: ReasonNone, ok: true}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithSessionID("sess-exectorn"),
		WithEnhanced(),
	)

	if got := ch.ShellIntegrationReason(); got != ReasonExecRefused {
		t.Fatalf("reason %q, want %q", got, ReasonExecRefused)
	}
	assertPromptOnChannel(t, srv, ch)

	refusals, _, shells := execRowCounts(srv)
	if refusals != 1 || shells != 1 {
		t.Fatalf("exec refusals=%d shells=%d, want 1/1", refusals, shells)
	}
	// One connection: the replacement is a second SESSION, never a second
	// connection and never a second credential use.
	srv.waitLiveConns(1)
}

// Row 4's other half, and the reason it is a separate row at all: the
// replacement is refused too, so there is no prompt anywhere. That is
// session-failed, not conventional, and the session must say so rather than
// hand back a channel with nothing behind it.
func TestExecRow_WhenTheReplacementIsRefusedTooTheSessionFails(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	srv.setExecRefusal(execRefusedChannelClosed, "")
	srv.setMaxSessions(1) // the replacement channel cannot be opened

	launcher := &fakeLauncher{cmd: "exec /nocx/loader", reason: ReasonNone, ok: true}
	khPath := writeKnownHosts(t, srv, srv.addr)
	client, err := NewReal(log.NewSlogAdapter(nil), WithKnownHostsFile(khPath), WithConfigResolver(NewStubConfigResolver()))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	_, err = client.Connect(context.Background(), srv.addr,
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
		WithPTYSize(80, 24, 0, 0),
		WithRemoteLauncher(launcher),
		WithSessionID("sess-execnorecovery"),
		WithEnhanced(),
	)
	if err == nil {
		t.Fatal("Connect returned a channel with no prompt behind it")
	}
	if !strings.Contains(err.Error(), string(ReasonExecRefused)) {
		t.Fatalf("the failure does not name the row: %v", err)
	}
}

// The sixth row, which is the more important half: a real OpenSSH server
// cannot be made to refuse `exec` at all — it ACCEPTS and substitutes. The
// channel is consumed, the substituted command reports its status, and no
// native prompt exists on any channel of that connection. It must not be
// collapsed into the refused row: refused is recoverable, this is not.
func TestExecRow_AcceptedAndSubstitutedIsItsOwnNamedOutcome(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()
	srv.setExecRefusal(execAcceptedAndSubstituted, "this account is restricted\r\n")

	launcher := &substitutedLauncher{fakeLauncher{cmd: "exec /nocx/loader", reason: ReasonNone, ok: true}}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithSessionID("sess-execsubst"),
		WithEnhanced(),
	)

	// Wait on the BOOTSTRAP finishing, not on the channel ending. The two
	// are different goroutines with no ordering between them: the watcher
	// closes Done from session.Wait while the bootstrap is still deciding
	// what to call this, and a test that read the reason on Done would be
	// reading it before it was written — a flake, and the kind AGENTS.md
	// names, since the observable state change is the bootstrap's.
	waitBootstrapFinished(t, ch)
	if got := ch.ShellIntegrationReason(); got != ReasonExecSubstituted {
		t.Fatalf("reason %q, want %q — accepted-and-substituted is not a refusal and is not recoverable", got, ReasonExecSubstituted)
	}
	_, substitutions, shells := execRowCounts(srv)
	if substitutions != 1 {
		t.Fatalf("exec substitutions=%d, want 1", substitutions)
	}
	if shells != 0 {
		t.Fatalf("%d shell requests were made; there is no native prompt to reach on that connection and asking for one would be the promise this row exists to refuse", shells)
	}
}

// assertPromptOnChannel is the row's actual claim: the user has a terminal
// that carries bytes both ways.
func assertPromptOnChannel(t *testing.T, srv *testSSHServer, ch Channel) {
	t.Helper()
	<-srv.shellReady
	waitBootstrapped(t, ch)
	if _, err := ch.Write([]byte("typed")); err != nil {
		t.Fatalf("write to the prompt that followed the refusal: %v", err)
	}
	buf := make([]byte, 64)
	n, err := ch.Read(buf)
	if err != nil {
		t.Fatalf("read from the prompt that followed the refusal: %v", err)
	}
	if got := string(buf[:n]); got != "echo:typed" {
		t.Fatalf("the prompt that followed the refusal answered %q, want %q", got, "echo:typed")
	}
}
