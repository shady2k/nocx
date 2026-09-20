package session

// The helper's half of the completion downlink (owner decision 2026-09-19):
// the coordinator carries an already-authenticated completion DOWN to the
// session that owns the pane, and this side's only job is to resolve the
// session, decode the fence and hand all three to the session runtime —
// whose own incarnation check is the only judging this side of the wire
// does. This file is package session because its assertions read the
// runtime's own state, and one of them PINS the fact the coordinator's
// downlink is derived from.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// --- the white-box bridge, for the external tests ---------------------------

// TestLifecycleRendezvous reads one session's rendezvous. The rendezvous is
// the observable a completion lands in; the external tests (package
// session_test) prove the downlink's arrival through it and cannot reach the
// runtime without this seam.
func (s *Service) TestLifecycleRendezvous(id proto.HostSessionID) (sessionruntime.Rendezvous, bool) {
	hs, err := s.find(id)
	if err != nil {
		return sessionruntime.Rendezvous{}, false
	}
	return hs.runtime.Rendezvous(), true
}

// TestLifecycleIncarnation reads one session's runtime incarnation — the
// exact pair the runtime judges a completion by.
func (s *Service) TestLifecycleIncarnation(id proto.HostSessionID) (sessionruntime.Incarnation, bool) {
	hs, err := s.find(id)
	if err != nil {
		return sessionruntime.Incarnation{}, false
	}
	return hs.runtime.Incarnation(), true
}

// TestSightFence sights a fence on one session's runtime — the sighting half
// of the rendezvous, which the emulator's output owns in production.
func (s *Service) TestSightFence(id proto.HostSessionID, nonce sessionruntime.FenceNonce, source []byte) error {
	hs, err := s.find(id)
	if err != nil {
		return err
	}
	return hs.runtime.SightFence(nonce, source)
}

// --- a process and a spawner, so nothing here needs a real shell ------------

// lcProcess satisfies Process with nothing behind it: the runtime under test
// reads EOF from the first Read and never writes, because nothing here
// drives a PTY.
func lcTestLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type lcProcess struct {
	done chan struct{}
	once sync.Once
}

func (p *lcProcess) Read(b []byte) (int, error) {
	// A live pty with nothing to say: block until the session closes, then
	// report EOF. An instant EOF is a shell exiting — the exit watcher would
	// end the runtime and the completion would be refused to an unavailable
	// session, which is not what these tests are about.
	<-p.done
	return 0, io.EOF
}
func (p *lcProcess) Write(b []byte) (int, error) { return len(b), nil }
func (p *lcProcess) Close() error {
	p.once.Do(func() { close(p.done) })
	return nil
}
func (p *lcProcess) Done() <-chan struct{} { return p.done }
func (p *lcProcess) Pid() int              { return 4242 }
func (p *lcProcess) ProcessGroup() int     { return 4343 }
func (p *lcProcess) Shell() string         { return "/bin/fake" }

func (p *lcProcess) ForegroundProcessGroup() (int, error)                         { return 4343, nil }
func (p *lcProcess) Resize(context.Context, uint16, uint16, uint16, uint16) error { return nil }
func (p *lcProcess) SignalProcessGroup(int, syscall.Signal) error                 { return nil }
func (p *lcProcess) WaitErr() (error, bool)                                       { return nil, false }

type lcSpawner struct{}

func (s *lcSpawner) Spawn(SpawnRequest) (Process, error) {
	return &lcProcess{done: make(chan struct{})}, nil
}

// --- tests ------------------------------------------------------------------

// spawnOne starts one session the way a coordinator does, with a lifecycle
// launch so the session is exactly the shape a completion will arrive for.
func spawnOne(t *testing.T) (*Service, proto.HostSessionID) {
	t.Helper()
	svc := New(Options{
		Generation: "gen-under-test",
		Spawner:    &lcSpawner{},
		Log:        lcTestLog(),
		// These tests assert exact rendezvous states across seconds of
		// test time; a real 500 ms wait under them would fire mid-test.
		// The trigger exists here and nobody fires it — the production
		// wiring of the wait is the wiring test's to prove.
		RendezvousExpireAfter: (&expiryTrigger{}).afterFunc,
	})
	res, err := svc.spawn(context.Background(), proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7,
			Capability: strings.Repeat("ab", 32),
		},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, res.Entry.Session
}

// TestTheRuntimeIncarnationIsTheSessionAtGenerationOne pins the fact the
// coordinator's downlink is DERIVED FROM. The helper mints one runtime per
// session, at generation 1, over the session id it just minted
// (newSessionRuntime's own doc) — and the coordinator cannot read this
// package, so its downlink spells the incarnation as {entry's session, 1}.
// If a future change ever mints a different generation, this test and that
// derivation must move together, and this test is what makes the silent
// disagreement impossible.
func TestTheRuntimeIncarnationIsTheSessionAtGenerationOne(t *testing.T) {
	svc, id := spawnOne(t)

	inc, ok := svc.TestLifecycleIncarnation(id)
	if !ok {
		t.Fatalf("the session this service just spawned names no incarnation")
	}
	want := sessionruntime.Incarnation{Session: sessionruntime.SessionID(id.Session), Generation: 1}
	if inc != want {
		t.Fatalf("runtime incarnation = %+v, want %+v — the coordinator derives the downlink's incarnation from this fact", inc, want)
	}
}

// TestLifecycleCompleteDeliversTheKernelFactToTheRuntime is the delivery:
// the op crosses the REAL dispatch (Ops, params schema, Call), the session is
// resolved by its generation-qualified handle, and the runtime's rendezvous
// ends up holding the kernel's nonce. SightFence first, so the authenticated
// half arrives second and CLOSES the meeting rather than only parking it.
func TestLifecycleCompleteDeliversTheKernelFactToTheRuntime(t *testing.T) {
	svc, id := spawnOne(t)

	var fence sessionruntime.FenceNonce
	fence[0] = 0xCC
	if err := svc.TestSightFence(id, fence, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}

	exit := 3
	raw, err := json.Marshal(proto.LifecycleCompleteParams{
		Session:     id,
		Incarnation: proto.Incarnation{Session: id.Session, Generation: 1},
		Nonce:       hex.EncodeToString(fence[:]),
		ExitCode:    &exit,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := svc.Call(context.Background(), proto.OpLifecycleComplete, raw); err != nil {
		t.Fatalf("lifecycle-complete: %v", err)
	}

	rv, ok := svc.TestLifecycleRendezvous(id)
	if !ok {
		t.Fatalf("the session vanished")
	}
	if rv.State != sessionruntime.RendezvousComplete {
		t.Fatalf("after the downlink the rendezvous is %v, want complete", rv.State)
	}
	if rv.Nonce != fence {
		t.Fatalf("the rendezvous holds nonce %v, want the one the op carried", rv.Nonce)
	}
}

// TestLifecycleCompleteForAnotherIncarnationChangesNothing pins where the
// incarnation judgement lives: this side does not add a gate on the
// coordinator's fact — it hands the wire's incarnation to the runtime, and
// the RUNTIME refuses evidence naming an incarnation that is not live. A
// completion for a dead generation is refused, not applied late.
func TestLifecycleCompleteForAnotherIncarnationChangesNothing(t *testing.T) {
	svc, id := spawnOne(t)

	var fence sessionruntime.FenceNonce
	fence[0] = 0xDD
	if err := svc.TestSightFence(id, fence, []byte("$ ")); err != nil {
		t.Fatalf("sight the fence: %v", err)
	}
	before, _ := svc.TestLifecycleRendezvous(id)

	exit := 0
	raw, _ := json.Marshal(proto.LifecycleCompleteParams{
		Session:     id,
		Incarnation: proto.Incarnation{Session: id.Session, Generation: 2},
		Nonce:       hex.EncodeToString(fence[:]), // the sighted fence; the incarnation is what differs here
		ExitCode:    &exit,
	})
	if _, err := svc.Call(context.Background(), proto.OpLifecycleComplete, raw); err != nil {
		t.Fatalf("lifecycle-complete must reach the runtime and be refused THERE, not here: %v", err)
	}

	after, _ := svc.TestLifecycleRendezvous(id)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("a completion for another incarnation moved the rendezvous from %v to %v, want untouched", before, after)
	}
}

// TestLifecycleCompleteRefusesAnUnknownSession is the refusal a caller can
// act on: a session this generation does not hold is ErrNoSuchSession — the
// answer the coordinator's reconciliation already reads — and not a silently
// swallowed send.
func TestLifecycleCompleteRefusesAnUnknownSession(t *testing.T) {
	svc, _ := spawnOne(t)

	raw, _ := json.Marshal(proto.LifecycleCompleteParams{
		Session:     proto.HostSessionID{Generation: "gen-under-test", Session: "ffffffffffffffffffffffffffffffff"},
		Incarnation: proto.Incarnation{Session: "ffffffffffffffffffffffffffffffff", Generation: 1},
		Nonce:       strings.Repeat("00", 32),
	})
	if _, err := svc.Call(context.Background(), proto.OpLifecycleComplete, raw); !errors.Is(err, ErrNoSuchSession) {
		t.Fatalf("unknown session: err = %v, want ErrNoSuchSession", err)
	}
}

// TestLifecycleCompleteRefusesAMalformedNonce is wire decoding, not a second
// gate: the kernel's fence is 32 bytes, so a nonce that is not 64 lowercase
// hex characters never came from an accepted completion, and decoding it into
// a zero-filled array would hand the runtime a rendezvous nothing sighted.
func TestLifecycleCompleteRefusesAMalformedNonce(t *testing.T) {
	svc, id := spawnOne(t)

	raw, _ := json.Marshal(proto.LifecycleCompleteParams{
		Session:     id,
		Incarnation: proto.Incarnation{Session: id.Session, Generation: 1},
		Nonce:       "not-a-fence",
	})
	if _, err := svc.Call(context.Background(), proto.OpLifecycleComplete, raw); !errors.Is(err, errBadFence) {
		t.Fatalf("malformed nonce: err = %v, want errBadFence", err)
	}
}
