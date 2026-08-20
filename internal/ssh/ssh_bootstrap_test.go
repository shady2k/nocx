package ssh

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The input quarantine and the stream handover (design §5.3), against the
// fixture SSH server this package already runs.
//
// No test here waits on a duration: the bootstrap is held open by a channel
// the test closes, and the stream's deadline is a channel the test fires.

// TestQuarantine_RefusesEveryFormOfUserInputUntilTheOutcome.
//
// Paste, IME composition and synthetic input are asserted "on the same
// footing" by construction rather than by enumeration: there is exactly one
// path from a user's keystroke to the far side — Channel.Write — and the
// session layer's queue drains into it. A bracketed paste and a multi-byte
// composition are bytes on that path like any other, and the cases below send
// them as such.
func TestQuarantine_RefusesEveryFormOfUserInputUntilTheOutcome(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	block := make(chan struct{})
	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true, blockBootstrap: block}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithSessionID("sess-quarantine"),
		WithEnhanced(),
	)
	<-srv.shellReady

	inputs := map[string][]byte{
		"a keystroke":            []byte("a"),
		"a bracketed paste":      []byte("\x1b[200~rm -rf /\x1b[201~"),
		"an IME composition":     []byte("\xe3\x81\x82\xe3\x81\x84"),
		"a synthetic burst":      []byte(strings.Repeat("x", 512)),
		"a bare carriage return": []byte("\r"),
	}
	for name, in := range inputs {
		n, err := ch.Write(in)
		var q *ErrInputQuarantined
		if !errors.As(err, &q) {
			t.Fatalf("%s during the bootstrap: err = %v, want the quarantine's refusal", name, err)
		}
		if n != 0 {
			t.Errorf("%s reported %d bytes written while refused", name, n)
		}
	}

	// A PTY control event is not user input and keeps working: it does not
	// pass through Write at all, which is the structural reason rather than
	// a special case in the gate.
	if err := ch.Resize(context.Background(), 120, 40, 0, 0); err != nil {
		t.Errorf("resize during the bootstrap: %v", err)
	}

	// The interval closes at the terminal outcome, and only then.
	close(block)
	waitBootstrapped(t, ch)
	if _, err := ch.Write([]byte("after")); err != nil {
		t.Fatalf("write after the outcome: %v", err)
	}
}

// TestQuarantine_RefusedInputIsNeverDeliveredLater: refused, not buffered.
// A buffered keystroke is a command the user did not knowingly run, executed
// later at a prompt they were not looking at.
func TestQuarantine_RefusedInputIsNeverDeliveredLater(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	block := make(chan struct{})
	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true, blockBootstrap: block}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithSessionID("sess-quarantine-2"),
		WithEnhanced(),
	)
	<-srv.shellReady

	if _, err := ch.Write([]byte("REFUSED_INPUT")); err == nil {
		t.Fatal("input during the bootstrap was accepted")
	}
	close(block)
	waitBootstrapped(t, ch)

	// The fixture echoes what it receives. Send a marker AFTER the outcome
	// and read until it arrives: anything the far side echoes before it
	// would be the refused bytes surfacing late.
	if _, err := ch.Write([]byte("MARKER")); err != nil {
		t.Fatalf("write after the outcome: %v", err)
	}
	got := readUntil(t, ch, "MARKER")
	if strings.Contains(got, "REFUSED_INPUT") {
		t.Errorf("refused input was delivered later: %q", got)
	}
}

// TestQuarantine_LinearisesAWriteRacingTheOutcome (design §11 assertion 15).
// Many writers race the release; each write is one decision, so every one of
// them is either refused or delivered exactly once — never both, never
// neither. The count is what proves it: refusals plus deliveries equals the
// number of attempts, exactly.
func TestQuarantine_LinearisesAWriteRacingTheOutcome(t *testing.T) {
	const writers = 64
	gate := newInputGate(false)
	var wg sync.WaitGroup
	var mu sync.Mutex
	refused, delivered := 0, 0

	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ok := gate.admit()
			mu.Lock()
			if ok {
				delivered++
			} else {
				refused++
			}
			mu.Unlock()
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		gate.release()
	}()
	close(start)
	wg.Wait()

	if refused+delivered != writers {
		t.Fatalf("%d refused + %d delivered = %d, want exactly %d decisions",
			refused, delivered, refused+delivered, writers)
	}
	// And the gate stays open: release is idempotent, because the bootstrap
	// has exactly one terminal outcome.
	gate.release()
	if !gate.admit() {
		t.Error("the gate closed again after a second release")
	}
}

// TestQuarantine_ASessionWithNoBootstrapIsOpenFromTheStart: a plain shell
// has no interval to quarantine, and must not inherit one.
func TestQuarantine_ASessionWithNoBootstrapIsOpenFromTheStart(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	// A launcher that declines: no carrier, no bootstrap, a plain shell.
	launcher := &fakeLauncher{reason: ReasonUnsupportedShell, ok: false}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
	)
	<-srv.shellReady
	if _, err := ch.Write([]byte("hello")); err != nil {
		t.Fatalf("write on a session that runs no bootstrap: %v", err)
	}
}

// TestPrepareRefusal_EmitsNoCommandAtAll: a bootstrap that cannot be
// prepared must not leave a carrier behind. The loader blocks on a frame, so
// a command with no sender is the one outcome worse than an un-integrated
// prompt.
func TestPrepareRefusal_EmitsNoCommandAtAll(t *testing.T) {
	srv := startTestSSHServer(t)
	defer srv.close()

	launcher := &fakeLauncher{cmd: "exec bash -i", reason: ReasonNone, ok: true, prepareFails: true}
	ch := launcherConnect(
		t, srv, []RealClientOption{WithConfigResolver(NewStubConfigResolver())},
		WithRemoteLauncher(launcher),
		WithSessionID("sess-prepare-refused"),
		WithEnhanced(),
	)
	assertUsable(t, srv, ch)

	if n := srv.execCommandCount(); n != 0 {
		t.Errorf("%d exec requests for a session whose bootstrap could not be prepared, want none", n)
	}
	if n := launcher.callCount(); n != 0 {
		t.Errorf("the launcher was asked for a command %d times after Prepare refused", n)
	}
	if got := ch.ShellIntegrationReason(); got == ReasonNone {
		t.Error("a session that could not bootstrap reported no refusal reason at all")
	}
}

// readUntil reads from ch until want appears, with a failsafe that only ever
// fires on a hang.
func readUntil(t *testing.T, ch Channel, want string) string {
	t.Helper()
	var sb strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 256)
		for {
			n, err := ch.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
				if strings.Contains(sb.String(), want) {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %q; got %q", want, sb.String())
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// The stream
// ---------------------------------------------------------------------------

// TestSessionFeed_ADeadlineNeverConsumesTheUsersBytes is the defect the feed
// exists to prevent. The obvious shape — the bootstrap reads the transport
// directly — loses a line on exactly this path: when the deadline fires, the
// read is still blocked inside the transport, and whatever it eventually
// takes is dropped. Here the bytes that arrive after the deadline are still
// there for the terminal.
func TestSessionFeed_ADeadlineNeverConsumesTheUsersBytes(t *testing.T) {
	pr, pw := io.Pipe()
	f := newSessionFeed(pr)
	deadline := make(chan time.Time, 1)
	f.after = func(time.Duration) <-chan time.Time { return deadline }

	// A partial line, then the deadline.
	if _, err := pw.Write([]byte("NOCX1 PART")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Wait for the pump to have taken it, then fire the deadline. The wait
	// is on an observable state — bytes present — not on a duration.
	waitUntil(t, func() bool { return f.peekPending() > 0 })
	deadline <- time.Time{}
	if _, err := f.ReadLine(context.Background(), time.Hour); !errors.Is(err, ErrBootstrapDeadline) {
		t.Fatalf("ReadLine err = %v, want the deadline", err)
	}

	// The terminal gets every byte, including the ones the abandoned wait
	// had already taken off the transport.
	if _, err := pw.Write([]byte("IAL\nrest\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	var got strings.Builder
	for got.Len() < len("NOCX1 PARTIAL\nrest\n") {
		n, err := f.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if got.String() != "NOCX1 PARTIAL\nrest\n" {
		t.Errorf("the terminal received %q, want every byte the deadline passed over", got.String())
	}
}

// TestSessionFeed_ReadsLinesAndTrimsTheLineEnding: the far side names its
// outcomes in COOKED mode — the loader restores the terminal before it
// prints — so every outcome line arrives CRLF-terminated. A reader matching
// on a bare newline would never see one.
func TestSessionFeed_ReadsLinesAndTrimsTheLineEnding(t *testing.T) {
	pr, pw := io.Pipe()
	f := newSessionFeed(pr)
	go func() {
		_, _ = pw.Write([]byte("NOCX1 LOADER_READY\r\nNOCX1 OUTCOME accepted\n"))
	}()
	for _, want := range []string{"NOCX1 LOADER_READY", "NOCX1 OUTCOME accepted"} {
		got, err := f.ReadLine(context.Background(), time.Hour)
		if err != nil {
			t.Fatalf("ReadLine: %v", err)
		}
		if got != want {
			t.Errorf("line = %q, want %q", got, want)
		}
	}
}

// peekPending reports how many bytes are waiting, for tests that need to
// observe a state rather than wait a duration.
func (f *sessionFeed) peekPending() int {
	if len(f.pending) > 0 {
		return len(f.pending)
	}
	select {
	case chunk, ok := <-f.chunks:
		if !ok {
			return 0
		}
		f.pending = chunk
		return len(f.pending)
	default:
		return 0
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for an observable state")
		default:
		}
	}
}

// TestFarSideEnded_IsYesWhicheverChainReportedIt is §6.4's sixth row's
// discriminator, and the reason it consults two signals rather than one.
//
// One far-side event — the exit status and the channel close that follow a
// substituted `exec` — reaches the bootstrap goroutine down two chains with
// no ordering between them, and the bootstrap gives up on whichever arrives
// first. Asking only the pump therefore answered "no" whenever the watcher's
// chain won a race the pump had not already finished, and the row was
// reported as ReasonUnknown. Each chain leaves its own
// fact behind, ordered before the wakeup it caused, so the answer is the
// disjunction and is the same either way.
//
// The fourth case is the one that keeps the row honest: a bootstrap that gave
// up on its own DEADLINE has neither fact, the session is still live, and
// that is a timeout rather than a substitution.
func TestFarSideEnded_IsYesWhicheverChainReportedIt(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pumpEnded   bool
		remoteEnded bool
		want        bool
	}{
		{"the pump saw the stream end", true, false, true},
		{"the watcher saw session.Wait return", false, true, true},
		{"both chains arrived before the question", true, true, true},
		{"neither did, which is a deadline on a live session", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			t.Cleanup(func() { _ = pw.Close() })
			f := newSessionFeed(pr)
			if tc.pumpEnded {
				_ = pw.Close()
				waitUntil(t, f.Ended)
			}
			if got := farSideEnded(f, tc.remoteEnded); got != tc.want {
				t.Errorf("farSideEnded = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSessionFeed_TheStreamsEndIsVisibleToWhoeverItWoke guards the ordering
// farSideEnded's first signal rests on: the pump closes `ended` BEFORE
// `chunks`, so a consumer unblocked by the end of `chunks` is ordered after
// it and cannot read a stale "no". Closed the other way round the two are
// concurrent, and the consumer's answer would depend on the scheduler.
func TestSessionFeed_TheStreamsEndIsVisibleToWhoeverItWoke(t *testing.T) {
	pr, pw := io.Pipe()
	f := newSessionFeed(pr)
	_ = pw.Close()

	if _, err := f.ReadLine(context.Background(), time.Hour); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadLine err = %v, want io.EOF from the stream's end", err)
	}
	if !f.Ended() {
		t.Fatal("the reader the stream's end woke was told the stream had not ended")
	}
}
