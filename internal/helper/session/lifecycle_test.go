package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestSpawnCarriesLifecycleLaunchWithoutSecretInProcessMetadata(t *testing.T) {
	const capability = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	spawner := &captureSpawner{proc: &fakeProcess{shell: "/bin/bash"}}
	svc := New(Options{
		Generation: "gen",
		Spawner:    spawner,
		Limits: Limits{
			DefaultWindowBytes: 128 * 1024, MinWindowBytes: 128 * 1024,
			MaxWindowBytes: 128 * 1024, BudgetBytes: 512 * 1024,
		},
		NewID: func() ([16]byte, error) { return [16]byte{1}, nil },
	})
	_, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{Lane: "lane-1", Domain: "dom-1", Epoch: 7, Capability: capability},
	}))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if spawner.req.Lifecycle == nil || spawner.req.Lifecycle.Capability != capability {
		t.Fatalf("lifecycle launch not forwarded: %#v", spawner.req.Lifecycle)
	}
	if strings.Contains(spawner.proc.Shell(), capability) {
		t.Fatal("capability appeared in process shell metadata")
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type captureSpawner struct {
	req  SpawnRequest
	proc Process
}

func (s *captureSpawner) Spawn(req SpawnRequest) (Process, error) {
	s.req = req
	return s.proc, nil
}

type fakeProcess struct{ shell string }

func (p *fakeProcess) Read([]byte) (int, error) { return 0, io.EOF }

func (p *fakeProcess) Write(b []byte) (int, error)                                  { return len(b), nil }
func (p *fakeProcess) Close() error                                                 { return nil }
func (p *fakeProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }
func (p *fakeProcess) Done() <-chan struct{}                                        { return make(chan struct{}) }

func (p *fakeProcess) WaitErr() (error, bool) { return nil, false }
func (p *fakeProcess) Pid() int               { return 1 }
func (p *fakeProcess) Shell() string          { return p.shell }

func (p *fakeProcess) ForegroundProcessGroup() (int, error) { return 0, nil }

// A coordinator that was replaced comes back and needs the identity the shell
// is still speaking with. The helper is the only thing that still has it: it
// received the launch at spawn, handed it to the shell, and outlived both
// coordinators (nocx-k6p18.31).
func TestTheHelperGivesBackTheLifecycleLaunchItSpawnedTheShellWith(t *testing.T) {
	const capability = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const recovery = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	launch := &proto.LifecycleLaunch{
		Lane: "lane-1", Domain: "dom-1", Epoch: 7,
		Capability: capability, Recovery: recovery,
	}
	svc := New(Options{
		Generation: "gen",
		Spawner:    &captureSpawner{proc: &lifecycleProcess{fakeProcess: fakeProcess{shell: "/bin/bash"}}},
		Limits: Limits{
			DefaultWindowBytes: 128 * 1024, MinWindowBytes: 128 * 1024,
			MaxWindowBytes: 128 * 1024, BudgetBytes: 512 * 1024,
		},
		NewID: func() ([16]byte, error) { return [16]byte{1}, nil },
	})
	raw, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{
		Cols: 80, Rows: 24, Lifecycle: launch,
	}))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	spawned, ok := raw.(proto.SpawnResult)
	if !ok {
		t.Fatalf("spawn answered %T, want proto.SpawnResult", raw)
	}

	answer, err := svc.Call(context.Background(), proto.OpAdoptLifecycle, mustJSON(t, proto.AdoptLifecycleParams{
		Session: spawned.Entry.Session,
	}))
	if err != nil {
		t.Fatalf("adopt-lifecycle: %v", err)
	}
	got, ok := answer.(proto.AdoptLifecycleResult)
	if !ok {
		t.Fatalf("adopt-lifecycle answered %T, want proto.AdoptLifecycleResult", answer)
	}
	if got.Lifecycle == nil {
		t.Fatal("the helper answered with no launch for a session it spawned with one")
	}
	if *got.Lifecycle != *launch {
		t.Fatalf("launch = %#v, want the one the shell was spawned with %#v", *got.Lifecycle, *launch)
	}
}

// A conventional session was spawned with no lifecycle at all. The answer is
// "none", and it is an ANSWER: a coordinator that got it must not adopt
// anything, and must not be left guessing whether the op merely failed.
func TestTheHelperAnswersNoLaunchForAConventionalSession(t *testing.T) {
	svc := New(Options{
		Generation: "gen",
		Spawner:    &captureSpawner{proc: &fakeProcess{shell: "/bin/bash"}},
		Limits: Limits{
			DefaultWindowBytes: 128 * 1024, MinWindowBytes: 128 * 1024,
			MaxWindowBytes: 128 * 1024, BudgetBytes: 512 * 1024,
		},
		NewID: func() ([16]byte, error) { return [16]byte{2}, nil },
	})
	raw, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{Cols: 80, Rows: 24}))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	spawned, ok := raw.(proto.SpawnResult)
	if !ok {
		t.Fatalf("spawn answered %T, want proto.SpawnResult", raw)
	}
	answer, err := svc.Call(context.Background(), proto.OpAdoptLifecycle, mustJSON(t, proto.AdoptLifecycleParams{
		Session: spawned.Entry.Session,
	}))
	if err != nil {
		t.Fatalf("adopt-lifecycle: %v", err)
	}
	got, ok := answer.(proto.AdoptLifecycleResult)
	if !ok {
		t.Fatalf("adopt-lifecycle answered %T, want proto.AdoptLifecycleResult", answer)
	}
	if got.Lifecycle != nil {
		t.Fatalf("launch = %#v, want none", *got.Lifecycle)
	}
}

func TestAdoptLifecycleRefusesASessionThisGenerationDoesNotHold(t *testing.T) {
	svc := New(Options{
		Generation: "gen",
		Spawner:    &captureSpawner{proc: &fakeProcess{shell: "/bin/bash"}},
		Limits: Limits{
			DefaultWindowBytes: 128 * 1024, MinWindowBytes: 128 * 1024,
			MaxWindowBytes: 128 * 1024, BudgetBytes: 512 * 1024,
		},
		NewID: func() ([16]byte, error) { return [16]byte{3}, nil },
	})
	_, err := svc.Call(context.Background(), proto.OpAdoptLifecycle, mustJSON(t, proto.AdoptLifecycleParams{
		Session: proto.HostSessionID{Generation: "gen", Session: "ffffffffffffffffffffffffffffffff"},
	}))
	if !errors.Is(err, ErrNoSuchSession) {
		t.Fatalf("adopt-lifecycle for an unknown session = %v, want ErrNoSuchSession", err)
	}
}

// The op has to be in the registered set, or the host answers unknown_op for
// a generation that in fact supports it — and the coordinator degrades a
// session it could have taken back whole.
func TestAdoptLifecycleIsARegisteredOpWithASchema(t *testing.T) {
	svc := New(Options{Generation: "gen"})
	found := false
	for _, op := range svc.Ops() {
		if op == proto.OpAdoptLifecycle {
			found = true
		}
	}
	if !found {
		t.Fatalf("Ops() = %v, missing %q", svc.Ops(), proto.OpAdoptLifecycle)
	}
	if svc.ParamsSchema(proto.OpAdoptLifecycle) == nil {
		t.Fatalf("no params schema for %q", proto.OpAdoptLifecycle)
	}
}

// The launch and the channel are two facts, and a spawner may produce the
// first without the second: it declines shell integration for a shell it does
// not support, and the process it returns carries no lifecycle carrier.
// Answering the launch anyway would let a replacing coordinator adopt a
// domain nothing speaks on — a pane that reports itself integrated and never
// produces a block, which is the defect one layer up.
func TestTheHelperAnswersNoLaunchWhenTheShellNeverGotAChannel(t *testing.T) {
	svc := New(Options{
		Generation: "gen",
		Spawner:    &captureSpawner{proc: &fakeProcess{shell: "/bin/sh"}},
		Limits: Limits{
			DefaultWindowBytes: 128 * 1024, MinWindowBytes: 128 * 1024,
			MaxWindowBytes: 128 * 1024, BudgetBytes: 512 * 1024,
		},
		NewID: func() ([16]byte, error) { return [16]byte{4}, nil },
	})
	raw, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{Lane: "lane-1", Domain: "dom-1", Epoch: 7, Capability: "aa"},
	}))
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	spawned, ok := raw.(proto.SpawnResult)
	if !ok {
		t.Fatalf("spawn answered %T, want proto.SpawnResult", raw)
	}
	answer, err := svc.Call(context.Background(), proto.OpAdoptLifecycle, mustJSON(t, proto.AdoptLifecycleParams{
		Session: spawned.Entry.Session,
	}))
	if err != nil {
		t.Fatalf("adopt-lifecycle: %v", err)
	}
	got, ok := answer.(proto.AdoptLifecycleResult)
	if !ok {
		t.Fatalf("adopt-lifecycle answered %T, want proto.AdoptLifecycleResult", answer)
	}
	if got.Lifecycle != nil {
		t.Fatalf("launch = %#v, want none for a shell that got no channel", *got.Lifecycle)
	}
}

// lifecycleProcess is a fakeProcess that DID get a lifecycle carrier, which
// is what makes its session's launch adoptable.
type lifecycleProcess struct {
	fakeProcess
}

func (p *lifecycleProcess) Lifecycle() io.ReadWriteCloser { return nopCarrier{} }

type nopCarrier struct{}

func (nopCarrier) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopCarrier) Write(b []byte) (int, error) { return len(b), nil }
func (nopCarrier) Close() error                { return nil }
