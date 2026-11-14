package harness

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/shellenv"
)

// The base starts the agent with the login shell's environment, not the
// backend's own: a program launched from the desktop has launchd's bare PATH,
// and the person's shell is where the tools are.
func TestLoginEnvironmentCarriesPath(t *testing.T) {
	env, err := loginEnvironment(context.Background())
	if err != nil {
		t.Fatalf("loginEnvironment: %v", err)
	}
	if _, ok := shellenv.Path(env); !ok {
		t.Fatalf("the login environment has no PATH: %v", env)
	}
}

// A program that never sends a newline must not grow the backend's memory. The
// turn fails with the bound's reason instead.
func TestBaseBoundsStdoutLines(t *testing.T) {
	line := strings.Repeat("a", 200)
	turn, err := (Base{MaxLineBytes: 64}).Start(context.Background(), Launch{
		Argv: []string{"/bin/sh", "-c", "printf '%s\\n' '" + line + "'"},
		Decode: func(raw []byte) ([]Event, error) {
			return []Event{{Kind: EventAnswerDelta, Text: string(raw)}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var events []Event
	for ev := range turn.Events() {
		events = append(events, ev)
	}
	if len(events) != 1 || events[0].Kind != EventFailure ||
		!strings.Contains(events[0].Failure.Reason, "longer than 64 bytes") {
		t.Fatalf("events = %+v, want one failure naming the bound", events)
	}
	if _, err := turn.Wait(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "longer than 64 bytes") {
		t.Fatalf("Wait error = %v", err)
	}
}

func endDecoder([]byte) ([]Event, error) {
	return []Event{{Kind: EventEnd, End: &End{Model: "test"}}}, nil
}

func TestBaseGracefulProtocolCompletion(t *testing.T) {
	turn, err := (Base{}).Start(context.Background(), Launch{Argv: []string{"/bin/sh", "-c", "echo end; cat >/dev/null"}, Decode: endDecoder, CompleteOnEnd: true})
	if err != nil {
		t.Fatal(err)
	}
	for range turn.Events() {
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := turn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestBaseEndNeedsSuccessfulExit(t *testing.T) {
	turn, err := (Base{}).Start(context.Background(), Launch{Argv: []string{"/bin/sh", "-c", "echo end; exit 7"}, Decode: endDecoder})
	if err != nil {
		t.Fatal(err)
	}
	for range turn.Events() {
	}
	if _, err := turn.Wait(context.Background()); err == nil {
		t.Fatal("end followed by failed exit succeeded")
	}
}

func TestBaseFailureDiagnosticsBounded(t *testing.T) {
	turn, err := (Base{MaxStderrBytes: 5}).Start(context.Background(), Launch{Argv: []string{"/bin/sh", "-c", "printf authentication >&2; exit 2"}, Decode: endDecoder, FailureReason: func(stderr string, _ error) string { return "agent: " + stderr }})
	if err != nil {
		t.Fatal(err)
	}
	for range turn.Events() {
	}
	if _, err := turn.Wait(context.Background()); err == nil || err.Error() != "agent: authe" {
		t.Fatalf("Wait = %v", err)
	}
}

func TestBaseRunContextStopsBlockedForwarding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	var once sync.Once
	turn, err := (Base{}).Start(ctx, Launch{Argv: []string{"/bin/sh", "-c", "while :; do echo delta; done"}, Decode: func([]byte) ([]Event, error) {
		once.Do(func() { close(ready) })
		return []Event{{Kind: EventAnswerDelta, Text: "x"}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = turn.Cancel(context.Background()) }()
	<-ready
	cancel()
	wait, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if _, err := turn.Wait(wait); !errors.Is(err, ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	for range turn.Events() {
	}
}

func TestBaseWaitContextDoesNotCancel(t *testing.T) {
	turn, err := (Base{}).Start(context.Background(), Launch{Argv: []string{"/bin/sh", "-c", "cat >/dev/null; echo end"}, Decode: endDecoder})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := turn.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := turn.CloseStdin(); err != nil {
		t.Fatal(err)
	}
	for range turn.Events() {
	}
	if _, err := turn.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBaseClosesStdout(t *testing.T) {
	for range 8 {
		turn, err := (Base{}).Start(context.Background(), Launch{Argv: []string{"/bin/sh", "-c", "echo end"}, Decode: endDecoder})
		if err != nil {
			t.Fatal(err)
		}
		for range turn.Events() {
		}
		if _, err := turn.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := turn.stdout.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("stdout descriptor remains open: %v", err)
		}
	}
}

func TestBaseResolvesExecutableFromLaunchEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/bin/sh", filepath.Join(dir, "agent-only-in-login-path")); err != nil {
		t.Fatal(err)
	}
	turn, err := (Base{}).Start(context.Background(), Launch{Argv: []string{"agent-only-in-login-path", "-c", "echo end"}, Env: []string{"PATH=" + dir}, Decode: endDecoder})
	if err != nil {
		t.Fatal(err)
	}
	for range turn.Events() {
	}
	if _, err := turn.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBaseShutdownBounded(t *testing.T) {
	for _, script := range []string{"echo end; exec 1>&-; cat >/dev/null; sleep 30", "echo end; cat >/dev/null; sleep 30"} {
		t.Run(script, func(t *testing.T) {
			turn, err := (Base{ShutdownTimeout: 20 * time.Millisecond}).Start(context.Background(), Launch{Argv: []string{"/bin/sh", "-c", script}, Decode: endDecoder, CompleteOnEnd: true})
			if err != nil {
				t.Fatal(err)
			}
			for range turn.Events() {
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, err := turn.Wait(ctx); err == nil || !strings.Contains(err.Error(), "did not exit after stdin closed") {
				t.Fatalf("Wait = %v", err)
			}
		})
	}
}

func TestBaseStdoutEOFRequestsBoundedShutdown(t *testing.T) {
	turn, err := (Base{ShutdownTimeout: 20 * time.Millisecond}).Start(context.Background(), Launch{Argv: []string{"/bin/sh", "-c", "exec 1>&-; sleep 30"}, Decode: endDecoder})
	if err != nil {
		t.Fatal(err)
	}
	for range turn.Events() {
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := turn.Wait(ctx); err == nil || !strings.Contains(err.Error(), "did not exit after stdin closed") {
		t.Fatalf("Wait = %v", err)
	}
}

func TestBaseRepeatedTurnsDoNotLeakDescriptors(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("descriptor count requires procfs")
	}
	run := func() {
		turn, err := (Base{}).Start(context.Background(), Launch{Argv: []string{"/bin/sh", "-c", "echo end"}, Decode: endDecoder})
		if err != nil {
			t.Fatal(err)
		}
		for range turn.Events() {
		}
		if _, err := turn.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	run() // Initialize Go's poller before measuring descriptors owned by turns.
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		run()
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("open descriptors before=%d after=%d", len(before), len(after))
	}
}

func TestBaseCancelAfterWaitDoesNotSignal(t *testing.T) {
	turn, err := (Base{}).Start(context.Background(), Launch{Argv: []string{"/bin/sh", "-c", "echo end"}, Decode: endDecoder})
	if err != nil {
		t.Fatal(err)
	}
	for range turn.Events() {
	}
	if _, err := turn.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Any use of the released process identity would panic, proving the finished
	// guard prevents even looking up a target after Wait published completion.
	turn.cmd.Process = nil
	if err := turn.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBaseCleanupFailureIsBounded(t *testing.T) {
	if err := awaitGroupGoneWith(123, 0, func(int) (bool, error) { return true, nil }); err == nil || !strings.Contains(err.Error(), "running members") {
		t.Fatalf("cleanup error = %v", err)
	}
	sentinel := errors.New("cannot inspect group")
	if err := awaitGroupGoneWith(123, 0, func(int) (bool, error) { return false, sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("inspection error = %v", err)
	}
	p := &Process{events: make(chan Event, 1), done: make(chan struct{}), cleanupErr: sentinel, cancelled: true}
	p.finish(nil, "")
	if _, err := p.Wait(context.Background()); err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("Wait = %v", err)
	}
	if ev := <-p.Events(); ev.Kind != EventFailure {
		t.Fatalf("event = %+v", ev)
	}
}

func TestBaseExitObservationRetainsLeaderIdentity(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := observeExit(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	// A second wait observation must still find our unreaped child.
	if err := observeExit(cmd.Process.Pid); err != nil {
		t.Fatalf("leader identity released: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
