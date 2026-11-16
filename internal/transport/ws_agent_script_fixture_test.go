package transport

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/coordinator"
	agentharness "github.com/shady2k/nocx/internal/harness"
	"github.com/shady2k/nocx/internal/harness/fakeagent/protocol"
	"github.com/shady2k/nocx/internal/harness/fakeconnectortest"
	"github.com/shady2k/nocx/internal/harness/harnesstest"
	"github.com/shady2k/nocx/internal/toolendpoint"
	"github.com/shady2k/nocx/internal/turnbearer"
	"github.com/shady2k/nocx/internal/waittest"
)

var askPrograms struct {
	once               sync.Once
	ready              chan struct{} // closed when the build finished, by TestMain's goroutine
	dir, agent, helper string
	err                error
}

// buildAskPrograms builds the two fixture programs ONCE. TestMain starts it on
// its own goroutine before the first test, so the link runs BESIDE the
// sequential phase rather than inside the first test that asks for an agent —
// where it was seconds of the suite's critical path (nocx-oalo1.11.3).
func buildAskPrograms() {
	askPrograms.once.Do(func() {
		askPrograms.dir, askPrograms.err = os.MkdirTemp("", "nocx-ask-programs-")
		if askPrograms.err != nil {
			return
		}
		askPrograms.agent = filepath.Join(askPrograms.dir, "fakeagent")
		askPrograms.helper = filepath.Join(askPrograms.dir, "nocx-helper")
		for _, build := range []struct{ out, pkg string }{{askPrograms.agent, "../harness/fakeagent"}, {askPrograms.helper, "../../cmd/nocx-helper"}} {
			raw, err := exec.Command("go", "build", "-p", "1", "-o", build.out, build.pkg).CombinedOutput() //nolint:gosec // The packages are fixed test programs and output paths belong to this fixture.
			if err != nil {
				askPrograms.err = &askBuildError{err, string(raw)}
				return
			}
		}
	})
}

// askProgramsFor is buildAskPrograms waited for: the build itself belongs to
// TestMain, and a test that needs the programs waits for it to finish and
// answers its failure as its own.
func askProgramsFor(t *testing.T) (string, string) {
	t.Helper()
	<-askPrograms.ready
	if askPrograms.err != nil {
		t.Fatal(askPrograms.err)
	}
	return askPrograms.agent, askPrograms.helper
}

type askBuildError struct {
	err    error
	output string
}

func (e *askBuildError) Error() string { return e.err.Error() + ": " + e.output }

type scriptedAgentFixture struct {
	endpointLogs *synchronizedBuffer
	t            *testing.T
	harness      agentharness.Harness
	script       protocol.Script
}

// newScriptedAgent builds the fixture agent. It STEERS: a message sent while
// it answers reaches it, which is what most of this package's steer tests are
// about. newQueueingAgent is the other half of the contract — an agent that
// takes no message while it answers (ADR-anntnxdj decision 4).
func newScriptedAgent(t *testing.T, script protocol.Script) *scriptedAgentFixture {
	t.Helper()
	return newScriptedAgentWith(t, script, true)
}

func newQueueingAgent(t *testing.T, script protocol.Script) *scriptedAgentFixture {
	t.Helper()
	return newScriptedAgentWith(t, script, false)
}

func newScriptedAgentWith(t *testing.T, script protocol.Script, steers bool) *scriptedAgentFixture {
	t.Helper()
	agent, helper := askProgramsFor(t)
	dir := t.TempDir()
	for _, field := range []struct {
		p    *string
		name string
	}{{&script.PromptFile, "prompt"}, {&script.PIDFile, "pid"}, {&script.ResultFile, "result"}, {&script.HangFile, "hang"}, {&script.EnvironmentFile, "environment"}, {&script.SteerFile, "steer"}} {
		if *field.p == "" {
			*field.p = filepath.Join(dir, field.name)
		}
	}
	if script.Gate == "" {
		script.Gate = filepath.Join(dir, "gate")
		if err := syscall.Mkfifo(script.Gate, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if script.Steps == nil && len(script.Turns) == 0 {
		script.Steps = []protocol.Step{{Kind: protocol.StepEnd, Model: "fixture-model"}}
	}
	endpointLogs := &synchronizedBuffer{}
	logger := slog.New(slog.NewJSONHandler(endpointLogs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("tool endpoint logs:\n%s", endpointLogs.String())
		}
	})
	uid := os.Getuid()
	if uid < 0 || uint64(uid) > uint64(^uint32(0)) {
		t.Fatalf("uid is outside uint32 range: %d", uid)
	}
	auth := &askTurnAuthorizer{}
	ep, err := toolendpoint.New(toolendpoint.Config{Dir: dir, Peers: coordinator.SystemPeerCredentials{}, Owner: coordinator.SystemPathOwner{}, SelfUID: uint32(uid), Auth: auth, Dispatch: askEndpointDispatcher{}, Logger: logger}) //nolint:gosec // uid is checked against the uint32 range above.
	if err != nil {
		t.Fatal(err)
	}
	minter := turnbearer.NewMinter(ep, helper)
	auth.minter = minter
	if err = ep.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ep.Close() })
	base := agentharness.Base{Environment: func(context.Context) ([]string, error) { return os.Environ(), nil }}
	scripts := append([][]protocol.Step{script.Steps}, script.Turns...)
	for _, steps := range scripts {
		for _, step := range steps {
			if step.Kind == protocol.StepTool || step.Kind == protocol.StepBridge {
				base.Tools = minter.ToolSessions()
			}
		}
	}
	// The fixture's agent STEERS: these tests are about a message reaching the
	// agent that is answering. The queueing half is the fixture's default and
	// has its own tests (nocx-a62cl.6.3).
	connector := fakeconnectortest.New(fakeconnectortest.Config{AgentPath: agent, Script: script, Base: base, Steers: steers})
	t.Cleanup(func() { _ = connector.Close(context.Background()) })
	return &scriptedAgentFixture{t: t, harness: connector, script: script, endpointLogs: endpointLogs}
}

func (f *scriptedAgentFixture) read(path string) []byte {
	f.t.Helper()
	var raw []byte
	waittest.WaitFor(f.t, "agent fixture file "+path, func() bool {
		var err error
		raw, err = os.ReadFile(path) //nolint:gosec // Script observation files are generated by this test fixture.
		return err == nil && len(raw) > 0
	})
	return raw
}

func (f *scriptedAgentFixture) prompt() protocol.Prompt {
	var p protocol.Prompt
	if err := json.Unmarshal(f.read(f.script.PromptFile), &p); err != nil {
		f.t.Fatal(err)
	}
	return p
}
func (f *scriptedAgentFixture) toolResult() string { return string(f.read(f.script.ResultFile)) }
func (f *scriptedAgentFixture) release()           { harnesstest.ReleaseGate(f.t, f.script.Gate) }
func (f *scriptedAgentFixture) waitHung()          { f.read(f.script.HangFile) }
func (f *scriptedAgentFixture) pid() int {
	pid, err := strconv.Atoi(string(f.read(f.script.PIDFile)))
	if err != nil {
		f.t.Fatal(err)
	}
	return pid
}

// The fixture admits only the real bearer and the kernel already bound to it.
type askTurnAuthorizer struct{ minter *turnbearer.Minter }

func (*askTurnAuthorizer) AdmitsTurnBearer() bool { return true }
func (a *askTurnAuthorizer) Admit(peer toolendpoint.Peer, publish func(string, toolendpoint.AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	authority, ok := a.minter.Lookup(peer.Token)
	if !ok {
		return assistant.ToolInvocation{}, nil, toolendpoint.ErrTurnBearerRefused
	}
	ctx, done, ok := authority.StartAdmission(context.Background())
	if !ok {
		return assistant.ToolInvocation{}, nil, toolendpoint.ErrTurnBearerRefused
	}
	inv, err := authority.Tools.Admit(ctx)
	if err != nil {
		return assistant.ToolInvocation{}, done, err
	}
	if !authority.Publish(func() bool {
		return publish(toolendpoint.TurnScope(authority.Tools.RunContext().RunID), toolendpoint.TurnEpoch)
	}) {
		return assistant.ToolInvocation{}, done, toolendpoint.ErrTurnBearerRefused
	}
	return inv, done, nil
}

type askEndpointDispatcher struct{}

func (askEndpointDispatcher) Dispatch(assistant.ToolInvocation) (string, error) {
	return "", errors.New("turn dispatcher missing")
}
func (askEndpointDispatcher) Catalogue(content.Grant) []agenttools.Tool { return nil }

func answerScript(deltas ...string) protocol.Script {
	steps := make([]protocol.Step, 0, len(deltas)+1)
	for _, delta := range deltas {
		steps = append(steps, protocol.Step{Kind: protocol.StepAnswer, Text: delta})
	}
	steps = append(steps, protocol.Step{Kind: protocol.StepEnd, Model: "fixture-model"})
	return protocol.Script{Steps: steps}
}

func TestMain(m *testing.M) {
	// The fixture programs are linked while the tests run, not by the first
	// test that needs one: the build blocks its own goroutine and nothing else
	// (buildAskPrograms).
	//
	// A CHILD FIXTURE DOES NOT LINK. process_exit_linux_test.go re-executes
	// this binary so it can be observed as an unreaped zombie, and that child
	// must die at once — started here, a link would hold it alive until the
	// link finished, and the parent's five-second wait for a zombie would be
	// waiting for a compiler instead.
	askPrograms.ready = make(chan struct{})
	if os.Getenv("NOCX_TEST_PROCESS_EXIT_HELPER_MODE") == "child" {
		close(askPrograms.ready)
	} else {
		go func() {
			buildAskPrograms()
			close(askPrograms.ready)
		}()
	}
	code := m.Run()
	// A run that failed early leaves the linker mid-flight; its temporary
	// directory must outlive it, not the other way round.
	<-askPrograms.ready
	if askPrograms.dir != "" {
		_ = os.RemoveAll(askPrograms.dir)
	}
	os.Exit(code)
}
