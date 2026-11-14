package pi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/waittest"
)

func TestPiLifecycleHelper(t *testing.T) {
	switch os.Getenv("PI_LIFECYCLE_HELPER") {
	case "blocked":
		if path := os.Getenv("PI_LIFECYCLE_READY"); path != "" {
			_ = os.WriteFile(path, []byte("ready"), 0o600)
		}
		fmt.Println(`{"type":"ready"}`)
		<-time.After(time.Hour)
		os.Exit(0)
	case "success":
		fmt.Println(`{"type":"message_end","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"answer"}],"model":"test"}}`)
		fmt.Println(`{"type":"agent_settled"}`)
		// CompleteOnEnd closes stdin; wait for that before exiting successfully.
		_, _ = os.Stdin.Read(make([]byte, 1))
		os.Exit(0)
	}
}

func lifecycleProcess(t *testing.T, mode string, decode harness.Decoder) *harness.Process {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := (harness.Base{}).Start(context.Background(), harness.Launch{
		Argv: []string{exe, "-test.run=^TestPiLifecycleHelper$"},
		Env:  []string{"PI_LIFECYCLE_HELPER=" + mode, "GORACE=atexit_sleep_ms=0"}, Decode: decode, CompleteOnEnd: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Cancel(context.Background()); _, _ = p.Wait(context.Background()) })
	return p
}

func TestCancellationCannotOvertakeDecodedTerminal(t *testing.T) {
	state := &cancelState{}
	decoded, release := make(chan struct{}), make(chan struct{})
	decoder := newDecoder(state)
	p := lifecycleProcess(t, "success", func(line []byte) ([]harness.Event, error) {
		events, err := decoder(line)
		for _, event := range events {
			if event.Kind == harness.EventEnd {
				close(decoded)
				<-release
			}
		}
		return events, err
	})
	turn := newTurn(context.Background(), p, func() error { return nil }, state)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-decoded:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelCtx, stop := context.WithCancel(context.Background())
	stop()
	_ = turn.Cancel(cancelCtx)
	cancelled := state.cancelled()
	close(release)
	if cancelled {
		t.Fatal("cancellation overtook a decoded terminal event")
	}
	var ends, failures int
	for event := range turn.Events() {
		if event.Kind == harness.EventEnd {
			ends++
		}
		if event.Kind == harness.EventFailure {
			failures++
		}
	}
	if ends != 1 || failures != 0 {
		t.Fatalf("terminal events: ends=%d failures=%d", ends, failures)
	}
	outcome, err := turn.Wait(ctx)
	if err != nil || outcome.Model != "test" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestCancellationReleasesBlockedPromptWrite(t *testing.T) {
	ready := make(chan struct{})
	state := &cancelState{}
	p := lifecycleProcess(t, "blocked", func(line []byte) ([]harness.Event, error) { close(ready); return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleaned := make(chan struct{})
	turn := newTurn(ctx, p, func() error { close(cleaned); return nil }, state)
	limit, stop := context.WithTimeout(context.Background(), abortGrace+5*time.Second)
	defer stop()
	select {
	case <-ready:
	case <-limit.Done():
		t.Fatal(limit.Err())
	}
	written := make(chan error, 1)
	go func() {
		written <- turn.write(ctx, map[string]string{"type": "prompt", "message": strings.Repeat("x", 8<<20)})
	}()
	waittest.WaitFor(t, "prompt writer owns stdin", func() bool {
		if turn.writeMu.TryLock() {
			turn.writeMu.Unlock()
			return false
		}
		return true
	})
	// A pipe cannot hold this prompt; the child never reads stdin.
	select {
	case err := <-written:
		t.Fatalf("prompt unexpectedly completed: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-written:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("write = %v", err)
		}
	case <-limit.Done():
		t.Fatal("write did not observe cancellation")
	}
	if _, err := turn.Wait(limit); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("Wait = %v", err)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("turn files not cleaned")
	}
	failures := 0
	for event := range turn.Events() {
		if event.Kind == harness.EventFailure {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("terminal failures = %d", failures)
	}
}

func TestCancellationPreservesCleanupFailure(t *testing.T) {
	cleanupErr := &harness.TurnError{Reason: "the agent's process group could not be cleaned up: sentinel"}
	_, err := cancellationResult(true, harness.Outcome{}, errors.Join(&harness.TurnError{Reason: "cancelled"}, cleanupErr))
	if !errors.Is(err, harness.ErrCancelled) || !errors.Is(err, cleanupErr) {
		t.Fatalf("Wait lost cleanup error: %v", err)
	}
}

func TestDecoderCancellationClaimsOneTerminal(t *testing.T) {
	state := &cancelState{}
	state.status.Store(turnCancelled)
	decode := newDecoder(state)
	for i := 0; i < 2; i++ {
		events, err := decode([]byte(`{"type":"agent_settled","aborted":true}`))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (len(events) != 1 || events[0].Kind != harness.EventFailure || events[0].Failure.Reason != "cancelled") {
			t.Fatalf("cancel event=%+v", events)
		}
		if i == 1 && len(events) != 0 {
			t.Fatalf("duplicate terminal=%+v", events)
		}
	}
}

func TestRunCancelsBlockedInitialPrompt(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	launcher := filepath.Join(dir, "pi")
	if err = os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+exe+" -test.run=^TestPiLifecycleHelper$\n"), 0o700); err != nil { //nolint:gosec // private executable test fixture
		t.Fatal(err)
	}
	ready := filepath.Join(dir, "ready")
	c := New(filepath.Join(dir, "data"), []string{"PATH=" + dir + ":" + os.Getenv("PATH"), "PI_LIFECYCLE_HELPER=blocked", "PI_LIFECYCLE_READY=" + ready, "GORACE=atexit_sleep_ms=0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan harness.Turn, 1)
	failures := make(chan error, 1)
	go func() {
		turn, runErr := c.Run(ctx, harness.TurnSpec{ConversationID: "blocked", Text: strings.Repeat("x", 8<<20)})
		if runErr != nil {
			failures <- runErr
			return
		}
		result <- turn
	}()
	limit, stop := context.WithTimeout(context.Background(), abortGrace+5*time.Second)
	defer stop()
	waittest.WaitFor(t, "child started", func() bool { _, statErr := os.Stat(ready); return statErr == nil })
	cancel()
	var turn harness.Turn
	select {
	case turn = <-result:
	case runErr := <-failures:
		t.Fatal(runErr)
	case <-limit.Done():
		t.Fatal("Run retained blocked prompt write")
	}
	if _, err = turn.Wait(limit); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("Wait=%v", err)
	}
	entries, err := os.ReadDir(filepath.Join(c.dataDir, "pi", "turns"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("retained turn directory: %s", entry.Name())
		}
	}
}
