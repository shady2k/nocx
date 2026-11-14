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
	Base           harness.Base
}

// Factory builds the connector under test. It is called once per check, so a
// check's script describes each prompt in its persistent session.
type Factory func(t *testing.T, setup Setup) harness.Harness

// RunFakeProcessChecks checks persistent process ownership through the public API.
func RunFakeProcessChecks(t *testing.T, factory Factory) {
	t.Helper()
	bin := t.TempDir()
	agent := build(t, bin, "fakeagent", fakeAgentPackage)
	server := build(t, bin, "mcpserver", mcpServerPackage)
	t.Run("turns reuse process and window close stops it", func(t *testing.T) {
		dir := t.TempDir()
		pidFile := filepath.Join(dir, "agent.pid")
		h := factory(t, Setup{AgentPath: agent, ToolServerPath: server, Script: protocol.Script{PIDFile: pidFile, Steps: []protocol.Step{{Kind: protocol.StepEnd, Model: "fake-model"}}}})
		first := runSuccessful(t, h, dir)
		waitFile(t, pidFile)
		pid := readPID(t, pidFile)
		if !alive(pid) {
			t.Fatal("successful turn ended its session")
		}
		second := runSuccessful(t, h, dir)
		if first.Model != second.Model || readPID(t, pidFile) != pid {
			t.Fatal("second turn did not reuse the session")
		}
		owner := sessionCloser(t, h)
		if err := owner.CloseConversation(context.Background(), "conversation-1"); err != nil {
			t.Fatal(err)
		}
		waittest.WaitFor(t, "window closes agent", func() bool { return !alive(pid) })
	})
	t.Run("cancel retires session and stale cancel cannot affect next turn", func(t *testing.T) {
		dir := t.TempDir()
		pidFile := filepath.Join(dir, "agent.pid")
		h := factory(t, Setup{AgentPath: agent, ToolServerPath: server, Script: protocol.Script{PIDFile: pidFile, Turns: [][]protocol.Step{{{Kind: protocol.StepAnswer, Text: "ready"}, {Kind: protocol.StepHang}}}}})
		defer func() { _ = sessionCloser(t, h).Close(context.Background()) }()
		spec := harness.TurnSpec{ConversationID: "conversation-1", Dir: dir}
		turn, err := h.Run(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		nextEvent(t, turn.Events(), 5*time.Second)
		waitFile(t, pidFile)
		pid := readPID(t, pidFile)
		if cancelErr := turn.Cancel(context.Background()); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		events := collect(t, turn)
		assertCancelledTerminal(t, turn, events)
		waittest.WaitFor(t, "cancel ends session", func() bool { return !alive(pid) })
		next, err := h.Run(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		nextEvent(t, next.Events(), 5*time.Second)
		if readPID(t, pidFile) == pid {
			t.Fatal("cancelled session was reused")
		}
		if cancelErr := turn.Cancel(context.Background()); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		if err := next.Cancel(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertCancelledTerminal(t, next, collect(t, next))
	})
	t.Run("ignoring abort is bounded", func(t *testing.T) {
		dir := t.TempDir()
		pidFile := filepath.Join(dir, "agent.pid")
		h := factory(t, Setup{AgentPath: agent, ToolServerPath: server, Base: harness.Base{ShutdownTimeout: 50 * time.Millisecond}, Script: protocol.Script{PIDFile: pidFile, IgnoreAbort: true, IgnoreStdinEOF: true, Steps: []protocol.Step{{Kind: protocol.StepAnswer, Text: "ready"}, {Kind: protocol.StepHang}}}})
		defer func() { _ = sessionCloser(t, h).Close(context.Background()) }()
		turn, err := h.Run(context.Background(), harness.TurnSpec{ConversationID: "conversation-1", Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		nextEvent(t, turn.Events(), 5*time.Second)
		waitFile(t, pidFile)
		pid := readPID(t, pidFile)
		started := time.Now()
		if cancelErr := turn.Cancel(context.Background()); cancelErr != nil {
			t.Fatal(cancelErr)
		}
		events := collectWithin(t, turn.Events(), time.Second)
		assertCancelledTerminal(t, turn, events)
		if time.Since(started) > time.Second {
			t.Fatal("injected shutdown bound was not honored")
		}
		waittest.WaitFor(t, "ignoring agent killed", func() bool { return !alive(pid) })
	})
	t.Run("idle expiry starts a new process", func(t *testing.T) {
		dir := t.TempDir()
		pidFile := filepath.Join(dir, "agent.pid")
		h := factory(t, Setup{AgentPath: agent, ToolServerPath: server, Base: harness.Base{IdleTimeout: 50 * time.Millisecond}, Script: protocol.Script{PIDFile: pidFile, Steps: []protocol.Step{{Kind: protocol.StepEnd, Model: "fake-model"}}}})
		defer func() { _ = sessionCloser(t, h).Close(context.Background()) }()
		runSuccessful(t, h, dir)
		waitFile(t, pidFile)
		pid := readPID(t, pidFile)
		waittest.WaitFor(t, "idle agent exits", func() bool { return !alive(pid) })
		runSuccessful(t, h, dir)
		if readPID(t, pidFile) == pid {
			t.Fatal("idle session reused dead process")
		}
	})
	t.Run("turn admission is serialized", func(t *testing.T) {
		dir := t.TempDir()
		gate := filepath.Join(dir, "gate")
		if err := syscall.Mkfifo(gate, 0o600); err != nil {
			t.Fatal(err)
		}
		trace := newTransitions()
		h := factory(t, Setup{AgentPath: agent, ToolServerPath: server, Base: harness.Base{Observe: trace.observe}, Script: protocol.Script{Gate: gate, Turns: [][]protocol.Step{{{Kind: protocol.StepAnswer, Text: "first"}, {Kind: protocol.StepGate}, {Kind: protocol.StepEnd, Model: "fake-model"}}, {{Kind: protocol.StepAnswer, Text: "second"}, {Kind: protocol.StepEnd, Model: "fake-model"}}}}})
		defer func() { _ = sessionCloser(t, h).Close(context.Background()) }()
		spec := harness.TurnSpec{ConversationID: "conversation-1", Dir: dir}
		first, err := h.Run(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		nextEvent(t, first.Events(), 5*time.Second)
		second, err := h.Run(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		trace.wait(t, "turn-queued", 2)
		trace.assertCount(t, "prompt-written", 1)
		ReleaseGate(t, gate)
		collect(t, first)
		if _, err := first.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		events := collect(t, second)
		if len(events) != 2 || events[0].Text != "second" {
			t.Fatalf("second turn: %+v", events)
		}
		trace.assertBefore(t, "terminal", 1, "prompt-written", 2)
	})

	t.Run("endpoint settlement delays terminal and queued turn", func(t *testing.T) {
		dir := t.TempDir()
		tools := newTestToolSession(true)
		trace := newTransitions()
		tools.onReturn = func() { trace.observe(harness.Transition{Kind: "end-turn-return"}) }
		h := factory(t, Setup{AgentPath: agent, ToolServerPath: server, Base: harness.Base{Tools: tools.factory, Observe: trace.observe}, Script: protocol.Script{Steps: []protocol.Step{{Kind: protocol.StepEnd, Model: "fake-model"}}}})
		defer func() {
			tools.release()
			_ = sessionCloser(t, h).Close(context.Background())
		}()
		spec := harness.TurnSpec{ConversationID: "conversation-1", Dir: dir}
		turn, err := h.Run(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		waitSignal(t, tools.ending, "endpoint EndTurn entry")
		next, err := h.Run(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		trace.wait(t, "turn-queued", 2)
		trace.assertCount(t, "terminal", 0)
		trace.assertCount(t, "prompt-written", 1)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, waitErr := turn.Wait(ctx)
		cancel()
		if !errors.Is(waitErr, context.DeadlineExceeded) {
			t.Fatalf("Wait completed while EndTurn remained blocked: %v", waitErr)
		}
		tools.release()
		assertSuccessfulEnd(t, collect(t, turn))
		assertSuccessfulEnd(t, collect(t, next))
		trace.assertBefore(t, "end-turn-return", 1, "terminal", 1)
		trace.assertBefore(t, "settled", 1, "terminal", 1)
		trace.assertBefore(t, "terminal", 1, "prompt-written", 2)
		if !tools.returned.Load() {
			t.Fatal("terminal published before EndTurn returned")
		}
	})
	t.Run("pending approval survives confirmed idle expiry", func(t *testing.T) {
		dir := t.TempDir()
		pidFile := filepath.Join(dir, "agent.pid")
		tools := newTestToolSession(false)
		tools.pending.Store(true)
		trace := newTransitions()
		h := factory(t, Setup{AgentPath: agent, ToolServerPath: server, Base: harness.Base{IdleTimeout: 20 * time.Millisecond, Tools: tools.factory, Observe: trace.observe}, Script: protocol.Script{PIDFile: pidFile, Steps: []protocol.Step{{Kind: protocol.StepEnd, Model: "fake-model"}}}})
		defer func() { _ = sessionCloser(t, h).Close(context.Background()) }()
		runSuccessful(t, h, dir)
		waitFile(t, pidFile)
		pid := readPID(t, pidFile)
		trace.wait(t, "idle-expired", 1)
		if !tools.pending.Load() || tools.retired.Load() || !alive(pid) {
			t.Fatal("idle expiry retired an endpoint with a pending approval")
		}
		tools.pending.Store(false)
		trace.wait(t, "idle-expired", 2)
		waittest.WaitFor(t, "agent expires after approval settles", func() bool { return !alive(pid) })
	})
	t.Run("synthetic failures", func(t *testing.T) { testFailures(t, factory, agent, server) })
}

type closer interface {
	Close(context.Context) error
	CloseConversation(context.Context, string) error
}

func sessionCloser(t *testing.T, h harness.Harness) closer {
	t.Helper()
	c, ok := h.(closer)
	if !ok {
		t.Fatal("connector has no session close API")
	}
	return c
}

func runSuccessful(t *testing.T, h harness.Harness, dir string) harness.Outcome {
	t.Helper()
	turn, err := h.Run(context.Background(), harness.TurnSpec{ConversationID: "conversation-1", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, turn)
	if len(events) == 0 || events[len(events)-1].Kind != harness.EventEnd {
		t.Fatalf("successful events: %+v", events)
	}
	result, err := turn.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertCancelledTerminal(t *testing.T, turn harness.Turn, events []harness.Event) {
	t.Helper()
	if len(events) != 1 || events[0].Kind != harness.EventFailure || events[0].Failure == nil || events[0].Failure.Reason != "cancelled" {
		t.Fatalf("cancel events: %+v", events)
	}
	if _, err := turn.Wait(context.Background()); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("Wait: %v", err)
	}
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
			t.Fatalf("last event = %+v, failure = %+v; want a failure naming the exit", last, last.Failure)
		}
		_, err := turn.Wait(context.Background())
		if err == nil || !strings.Contains(fmt.Sprint(err), "status 7") {
			t.Fatalf("Wait error = %v, want the exit status", err)
		}
	})
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

func alive(pid int) bool {
	return processRunning(pid)
}
