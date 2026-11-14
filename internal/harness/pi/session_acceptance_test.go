package pi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/harness"
	fakemodel "github.com/shady2k/nocx/internal/harness/fakemodeltest"
	"github.com/shady2k/nocx/internal/waittest"
)

func piCollect(t *testing.T, turn harness.Turn) []harness.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var events []harness.Event
	for {
		select {
		case ev, ok := <-turn.Events():
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-ctx.Done():
			t.Fatalf("pi event stream did not close: %+v", events)
		}
	}
}

func piTurn(t *testing.T, c *Connector, spec harness.TurnSpec) harness.Turn {
	t.Helper()
	turn, err := c.Run(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func piSuccess(t *testing.T, c *Connector, spec harness.TurnSpec) {
	t.Helper()
	turn := piTurn(t, c, spec)
	events := piCollect(t, turn)
	outcome, err := turn.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v events=%+v", err, events)
	}
	if len(events) == 0 || events[len(events)-1].Kind != harness.EventEnd || outcome.Model != "nocx-test/test-model" {
		t.Fatalf("successful turn: %+v outcome=%+v", events, outcome)
	}
	terminals := 0
	for _, ev := range events {
		if ev.Kind == harness.EventEnd || ev.Kind == harness.EventFailure {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal count %d", terminals)
	}
}

func TestRealPiSessionReuseAndRestartInputs(t *testing.T) {
	for _, restart := range []string{"idle", "crash", "cancel"} {
		t.Run(restart, func(t *testing.T) {
			gate := make(chan struct{})
			defer close(gate)
			responses := []fakemodel.Response{{Text: []string{"first answer"}}, {Text: []string{"second answer"}}, {Text: []string{"third answer"}}}
			if restart == "cancel" {
				responses = append(responses[:2], fakemodel.Response{Block: gate}, responses[2])
			}
			original, fake := configuredPi(t, responses...)
			observations := t.TempDir()
			installPiObserver(t, original, observations)
			base := harness.Base{}
			if restart == "idle" {
				base.IdleTimeout = 100 * time.Millisecond
			}
			c := NewWithBase(original.dataDir, original.env, base)
			defer func() { _ = c.Close(context.Background()) }()
			spec := harness.TurnSpec{ConversationID: "restart-conversation", Dir: t.TempDir(), Model: "nocx-test/test-model", SystemPrompt: "nocx acceptance instructions unique-147", Text: "first question"}
			piSuccess(t, c, spec)
			pid := piPID(t, observations)
			first := piRequestPrompt(t, fake, observations)
			spec.Dir = t.TempDir()
			spec.Text = "second question"
			piSuccess(t, c, spec)
			if second := piPID(t, observations); second != pid {
				t.Fatalf("second turn pid %d != %d", second, pid)
			}
			before := piRequestPrompt(t, fake, observations)
			if first.SystemPrompt != before.SystemPrompt || strings.Count(before.SystemPrompt, spec.SystemPrompt) != 1 {
				t.Fatal("system prompt changed or duplicated between reused turns")
			}
			switch restart {
			case "idle":
				waittest.WaitFor(t, "idle pi exits", func() bool { return syscall.Kill(pid, 0) != nil })
			case "crash":
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				waittest.WaitFor(t, "crashed pi exits", func() bool { return syscall.Kill(pid, 0) != nil })
			case "cancel":
				pending := piTurn(t, c, spec)
				ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				if _, err := fake.WaitRequests(ctx, 3); err != nil {
					t.Fatal(err)
				}
				if err := pending.Cancel(ctx); err != nil {
					t.Fatal(err)
				}
				events := piCollect(t, pending)
				if _, err := pending.Wait(ctx); !errors.Is(err, harness.ErrCancelled) {
					t.Fatalf("cancel Wait: %v events=%+v", err, events)
				}
			}
			spec.Dir = t.TempDir()
			spec.Text = "third question"
			piSuccess(t, c, spec)
			after := piRequestPrompt(t, fake, observations)
			if newPID := piPID(t, observations); newPID == pid {
				t.Fatalf("restart reused pid %d", pid)
			}
			if before.SystemPrompt != after.SystemPrompt || strings.Count(after.SystemPrompt, spec.SystemPrompt) != 1 {
				t.Fatalf("system prompt changed or duplicated across %s restart", restart)
			}
			if !strings.Contains(after.History, "first question") || !strings.Contains(after.History, "first answer") || !strings.Contains(after.History, "second question") || !strings.Contains(after.History, "second answer") {
				t.Fatalf("history lost on %s: %s", restart, after.History)
			}
			if after.Model != before.Model {
				t.Fatalf("model changed: %s -> %s", before.Model, after.Model)
			}
			if err := c.CloseConversation(context.Background(), spec.ConversationID); err != nil {
				t.Fatal(err)
			}
			finalPID := piPID(t, observations)
			waittest.WaitFor(t, "window closes pi", func() bool { return syscall.Kill(finalPID, 0) != nil })
			configs, err := os.ReadDir(filepath.Join(c.dataDir, "pi", "configs"))
			if err != nil {
				t.Fatal(err)
			}
			if len(configs) != 0 {
				t.Fatalf("session config files leaked: %+v", configs)
			}
		})
	}
}

func TestRealPiIgnoringAbortIsKilledAndResumed(t *testing.T) {
	original, fake := configuredPi(t, fakemodel.Response{Text: []string{"first answer"}}, fakemodel.Response{Text: []string{"resumed answer"}})
	observations := t.TempDir()
	installPiObserver(t, original, observations)
	gate := filepath.Join(observations, "block-loop")
	extension := `import { existsSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
export default function(pi) { pi.on('before_agent_start', () => {
 const dir=process.env.NOCX_PI_OBSERVATIONS;
 if (existsSync(join(dir,'block-loop'))) {
  writeFileSync(join(dir,'loop-blocked'),'ready');
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0);
 }
}); }
`
	if err := os.WriteFile(filepath.Join(piAgentDir(original.env), "extensions", "block.ts"), []byte(extension), 0o600); err != nil {
		t.Fatal(err)
	}
	const bound = 100 * time.Millisecond
	c := NewWithBase(original.dataDir, original.env, harness.Base{ShutdownTimeout: bound})
	defer func() { _ = c.Close(context.Background()) }()
	spec := harness.TurnSpec{ConversationID: "ignores-abort", Model: "nocx-test/test-model", Text: "first question", Dir: t.TempDir(), SystemPrompt: "fixed instructions"}
	piSuccess(t, c, spec)
	pid := piPID(t, observations)
	if err := os.WriteFile(gate, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec.Text = "blocked question"
	turn := piTurn(t, c, spec)
	_ = readPiFile(t, observations, "loop-blocked")
	started := time.Now()
	if err := turn.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := piCollect(t, turn)
	if _, err := turn.Wait(context.Background()); !errors.Is(err, harness.ErrCancelled) {
		t.Fatalf("Wait %v events=%+v", err, events)
	}
	if elapsed := time.Since(started); elapsed < bound || elapsed > 2*time.Second {
		t.Fatalf("abort bound not honored: %s", elapsed)
	}
	waittest.WaitFor(t, "unresponsive pi killed", func() bool { return syscall.Kill(pid, 0) != nil })
	if len(fake.Requests()) != 1 {
		t.Fatalf("blocked prompt reached provider: %+v", fake.Requests())
	}
	if err := os.Remove(gate); err != nil {
		t.Fatal(err)
	}
	spec.Text = "resume question"
	piSuccess(t, c, spec)
	if piPID(t, observations) == pid {
		t.Fatal("unresponsive pi was reused")
	}
	prompt := piRequestPrompt(t, fake, observations)
	if !strings.Contains(prompt.History, "first question") || !strings.Contains(prompt.History, "first answer") {
		t.Fatalf("resume lost history: %s", prompt.History)
	}
}

func TestRealPiCancelDuringSetupNeverPromptsProvider(t *testing.T) {
	for _, command := range []string{"get_state", "set_model"} {
		t.Run(command, func(t *testing.T) {
			original, fake := configuredPi(t)
			observations := t.TempDir()
			installPiObserver(t, original, observations)
			preload := filepath.Join(t.TempDir(), "hold-response.cjs")
			source := `const fs=require('node:fs');
const original=process.stdout.write.bind(process.stdout);
let held=false;
process.stdout.write=function(chunk,...args) {
 let message;
 try {message=JSON.parse(chunk.toString())} catch {}
 if (!held && message?.type==='response' && message.command===process.env.NOCX_TEST_HOLD_COMMAND) {
  held=true;
  fs.writeFileSync(process.env.NOCX_TEST_HELD_FILE,'ready');
  const callback=args.find(arg=>typeof arg==='function');
  if(callback) callback();
  return true;
 }
 return original(chunk,...args);
};
`
			if err := os.WriteFile(preload, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			original.env = append(original.env, "NODE_OPTIONS=--require="+preload, "NOCX_TEST_HOLD_COMMAND="+command, "NOCX_TEST_HELD_FILE="+filepath.Join(observations, "response-held"))
			c := NewWithBase(original.dataDir, original.env, harness.Base{ShutdownTimeout: 100 * time.Millisecond})
			defer func() { _ = c.Close(context.Background()) }()
			// No --model launch argument: set_model remains an actor setup command.
			spec := harness.TurnSpec{ConversationID: "cancel-setup", Text: "must never reach provider", Dir: t.TempDir()}
			if command == "set_model" {
				spec.Model = "nocx-test/test-model"
			}
			turn := piTurn(t, c, spec)
			_ = readPiFile(t, observations, "response-held")
			pid := piPID(t, observations)
			if err := turn.Cancel(context.Background()); err != nil {
				t.Fatal(err)
			}
			events := piCollect(t, turn)
			if _, err := turn.Wait(context.Background()); !errors.Is(err, harness.ErrCancelled) {
				t.Fatalf("Wait: %v events=%+v", err, events)
			}
			if len(events) != 1 || events[0].Kind != harness.EventFailure {
				t.Fatalf("setup cancellation events: %+v", events)
			}
			waittest.WaitFor(t, "cancelled setup pi exits", func() bool { return syscall.Kill(pid, 0) != nil })
			if requests := fake.Requests(); len(requests) != 0 {
				t.Fatalf("cancelled setup reached provider: %+v", requests)
			}
		})
	}
}

func TestPiSpawnFailureSweepsSessionConfig(t *testing.T) {
	c := New(t.TempDir(), []string{"PATH=" + t.TempDir(), "PI_OFFLINE=1"})
	defer func() { _ = c.Close(context.Background()) }()
	turn := piTurn(t, c, harness.TurnSpec{ConversationID: "spawn-failure", Dir: t.TempDir(), Text: "question"})
	events := piCollect(t, turn)
	if _, err := turn.Wait(context.Background()); err == nil {
		t.Fatalf("missing executable succeeded: %+v", events)
	}
	if len(events) != 1 || events[0].Kind != harness.EventFailure {
		t.Fatalf("spawn failure events: %+v", events)
	}
	if err := c.CloseConversation(context.Background(), "spawn-failure"); err != nil {
		t.Fatal(err)
	}
	configs, err := os.ReadDir(filepath.Join(c.dataDir, "pi", "configs"))
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 {
		t.Fatalf("spawn failure leaked credential config: %+v", configs)
	}
}
