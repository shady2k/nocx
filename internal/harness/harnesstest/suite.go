// Package harnesstest runs the shared semantic conformance suite against
// connector-specific scenario drivers. It also exposes synthetic process fault
// checks for the fake agent; those checks are not a real connector's acceptance.
package harnesstest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/waittest"
)

const (
	fakeAgentPackage = "github.com/shady2k/nocx/internal/harness/fakeagent"
	mcpServerPackage = "github.com/shady2k/nocx/internal/harness/harnesstest/testdata/mcpserver"
)

// Setup is what the suite gives a connector's factory: the programs it may
// launch and the script the fake agent must follow.
type Setup struct {
	AgentPath      string
	ToolServerPath string
	Script         protocol.Script
}

// Factory builds the connector under test. It is called once per check, so a
// check's script is the agent's whole behaviour for that check.
type Factory func(t *testing.T, setup Setup) harness.Harness

// RunFakeProcessChecks exercises synthetic EOF, exit-status and orphan faults.
func RunFakeProcessChecks(t *testing.T, factory Factory) {
	t.Helper()
	bin := t.TempDir()
	agentPath := build(t, bin, "fakeagent", fakeAgentPackage)
	serverPath := build(t, bin, "mcpserver", mcpServerPackage)
	t.Run("successful RPC completion closes stdin", func(t *testing.T) {
		script := protocol.Script{CompleteOnEOF: true, Steps: []protocol.Step{
			{Kind: protocol.StepEnd, Model: "fake-model"}, {Kind: protocol.StepHang},
		}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{Dir: t.TempDir()})
		collect(t, turn)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := turn.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("synthetic failures", func(t *testing.T) { testFailures(t, factory, agentPath, serverPath) })
	t.Run("Wait leaves no orphan child", func(t *testing.T) { testNoProcessAfterWait(t, factory, agentPath, serverPath) })
}

// ReleaseGate lets a fixture continue only after the shared suite saw a delta.
func ReleaseGate(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	gate, err := openGate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Close() }()
	if _, err := gate.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
}

func openGate(ctx context.Context, path string) (*os.File, error) {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // test FIFO
		if !errors.Is(err, syscall.ENXIO) {
			return f, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func assertCancelled(t *testing.T, turn harness.Turn, events []harness.Event) {
	t.Helper()
	if len(events) != 2 {
		t.Fatalf("events after cancellation must be only failure: %v", kinds(events))
	}
	last := events[len(events)-1]
	if last.Kind != harness.EventFailure || last.Failure == nil || last.Failure.Reason != "cancelled" {
		t.Fatalf("last event = %+v, want the cancelled failure", last)
	}
	terminals := 0
	for _, ev := range events {
		if ev.Kind == harness.EventEnd || ev.Kind == harness.EventFailure {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal events = %d, want exactly one: %v", terminals, kinds(events))
	}
	if _, err := turn.Wait(context.Background()); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("Wait error = %v, want ErrCancelled", err)
	}
}

func testFailures(t *testing.T, factory Factory, agentPath, serverPath string) {
	t.Helper()
	t.Run("the agent states a failure", func(t *testing.T) {
		script := protocol.Script{Steps: []protocol.Step{
			{Kind: protocol.StepFail, Reason: "the provider is down"},
		}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
			ConversationID: "conversation-1", Text: "fail", Dir: t.TempDir(),
		})
		events := collect(t, turn)
		last := events[len(events)-1]
		if last.Kind != harness.EventFailure || last.Failure == nil || last.Failure.Reason != "the provider is down" {
			t.Fatalf("last event = %+v", last)
		}
		_, err := turn.Wait(context.Background())
		var turnErr *harness.TurnError
		if !errors.As(err, &turnErr) || turnErr.Reason != "the provider is down" {
			t.Fatalf("Wait error = %v, want the stated failure", err)
		}
	})

	t.Run("the process exits non-zero without an end event", func(t *testing.T) {
		script := protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepExit, Code: 7}}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
			ConversationID: "conversation-1", Text: "crash", Dir: t.TempDir(),
		})
		events := collect(t, turn)
		last := events[len(events)-1]
		if last.Kind != harness.EventFailure || last.Failure == nil || !strings.Contains(last.Failure.Reason, "status 7") {
			t.Fatalf("last event = %+v, want a failure naming the exit", last)
		}
		_, err := turn.Wait(context.Background())
		if err == nil || !strings.Contains(fmt.Sprint(err), "status 7") {
			t.Fatalf("Wait error = %v, want the exit status", err)
		}
	})

	t.Run("the process exits non-zero after an end event", func(t *testing.T) {
		script := protocol.Script{Steps: []protocol.Step{
			{Kind: protocol.StepEnd, Model: "fake-model"},
			{Kind: protocol.StepExit, Code: 1},
		}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
			ConversationID: "conversation-1", Text: "end then die", Dir: t.TempDir(),
		})
		events := collect(t, turn)
		if events[len(events)-1].Kind != harness.EventEnd {
			t.Fatalf("last event = %+v, want the end event", events[len(events)-1])
		}
		_, err := turn.Wait(context.Background())
		if err == nil || !strings.Contains(fmt.Sprint(err), "status 1") {
			t.Fatalf("Wait error = %v, want the exit status rather than the end event", err)
		}
	})

	t.Run("the process ends without an end event", func(t *testing.T) {
		script := protocol.Script{Steps: []protocol.Step{
			{Kind: protocol.StepAnswer, Text: "partial"},
		}}
		turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
			ConversationID: "conversation-1", Text: "silent end", Dir: t.TempDir(),
		})
		events := collect(t, turn)
		last := events[len(events)-1]
		if last.Kind != harness.EventFailure || last.Failure == nil || !strings.Contains(last.Failure.Reason, "without an end event") {
			t.Fatalf("last event = %+v", last)
		}
		_, err := turn.Wait(context.Background())
		if err == nil || !strings.Contains(fmt.Sprint(err), "without an end event") {
			t.Fatalf("Wait error = %v", err)
		}
	})
}

func testNoProcessAfterWait(t *testing.T, factory Factory, agentPath, serverPath string) {
	t.Helper()
	dir := t.TempDir()
	agentPIDFile := filepath.Join(dir, "agent.pid")
	childPIDFile := filepath.Join(dir, "child.pid")
	script := protocol.Script{
		PIDFile:      agentPIDFile,
		ChildPIDFile: childPIDFile,
		Steps: []protocol.Step{
			{Kind: protocol.StepChild},
			{Kind: protocol.StepExit, Code: 0},
		},
	}
	turn := start(t, factory, agentPath, serverPath, script, harness.ToolBridge{}, harness.TurnSpec{
		ConversationID: "conversation-1", Text: "leave a child", Dir: dir,
	})
	waitFile(t, agentPIDFile)
	waitFile(t, childPIDFile)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := turn.Wait(ctx); err == nil || !strings.Contains(fmt.Sprint(err), "without an end event") {
		t.Fatalf("Wait error = %v, want the missing end", err)
	}
	assertGone(t, readPID(t, agentPIDFile), "the agent")
	assertGone(t, readPID(t, childPIDFile), "the child the agent left")
	collect(t, turn)
}

func start(t *testing.T, factory Factory, agentPath, serverPath string, script protocol.Script, tools harness.ToolBridge, spec harness.TurnSpec) harness.Turn {
	t.Helper()
	spec.Tools = tools
	h := factory(t, Setup{AgentPath: agentPath, ToolServerPath: serverPath, Script: script})
	turn, err := h.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Cleanup(func() {
		_ = turn.Cancel(context.Background())
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = turn.Wait(ctx)
	})
	return turn
}

// collect drains a turn's stream, and fails rather than hanging when the turn
// never ends. The bound is a failure message, not the test's synchronisation:
// every check waits on an event the turn produces.
func collect(t *testing.T, turn harness.Turn) []harness.Event {
	t.Helper()
	return collectWithin(t, turn.Events(), 5*time.Second)
}

func collectWithin(t *testing.T, events <-chan harness.Event, timeout time.Duration) []harness.Event {
	t.Helper()
	var out []harness.Event
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("the turn's event stream did not close within %s", timeout)
		}
	}
}

func nextEvent(t *testing.T, events <-chan harness.Event, timeout time.Duration) harness.Event {
	t.Helper()
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("the event stream closed before the expected event")
		}
		return ev
	case <-time.After(timeout):
		t.Fatalf("the expected event did not arrive within %s", timeout)
	}
	return harness.Event{}
}

func kinds(events []harness.Event) []harness.EventKind {
	out := make([]harness.EventKind, len(events))
	for i, ev := range events {
		out[i] = ev.Kind
	}
	return out
}

func build(t *testing.T, dir, name, pkg string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-p", "1", "-o", out, pkg) //nolint:gosec // the package paths are this suite's own constants
	cmd.Env = os.Environ()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, stderr.String())
	}
	return out
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test TempDir
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return pid
}

func waitFile(t *testing.T, path string) {
	t.Helper()
	waittest.WaitForDetail(t, "the process to write "+path, func() string {
		raw, err := os.ReadFile(path) //nolint:gosec // test TempDir
		return fmt.Sprintf("read error: %v, content: %q", err, raw)
	}, func() bool {
		raw, err := os.ReadFile(path) //nolint:gosec // test TempDir
		return err == nil && len(raw) > 0
	})
}

func assertGone(t *testing.T, pid int, what string) {
	t.Helper()
	if alive(pid) {
		t.Fatalf("%s (pid %d) remains immediately after Wait", what, pid)
	}
}

func alive(pid int) bool {
	return processRunning(pid)
}
