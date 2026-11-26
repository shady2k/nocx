package helperfs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/helperfs"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/transfer"
)

type retryClock struct {
	*hostconntest.Clock
	armed chan time.Duration
}

func (c retryClock) After(d time.Duration) <-chan monoclock.Nanos {
	alarm := c.Clock.After(d)
	select {
	case c.armed <- d:
	default:
	}
	return alarm
}

// recoveryRegistry still acquires from the real registry. It tells the test that
// the provider actually tried to recover before the automatic retry was due, and
// which refusal it met: a helper the registry will redial, or a transport that is
// only paused and can come back on its own.
type recoveryRegistry struct {
	*farhelper.Registry
	lost   chan struct{}
	paused chan struct{}
}

func (r recoveryRegistry) Acquire(ctx context.Context, id string) (*farhelper.Lease, error) {
	l, err := r.Registry.Acquire(ctx, id)
	var unavailable *farhelper.Unavailable
	if errors.As(err, &unavailable) {
		switch unavailable.Reason {
		case farhelper.ReasonHelperLost:
			signal(r.lost)
		case farhelper.ReasonConnection:
			signal(r.paused)
		}
	}
	return l, err
}

// signal reports an event to a test that may not be listening yet. A nil channel
// means that test does not read this event, and the send falls through.
func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// boundRegistry is the shape internal/app wraps a connection's registry in: the
// pane no longer rides this connection, so neither the axis nor a lease is read
// through to the registry it wraps.
type boundRegistry struct {
	*farhelper.Registry
	bound atomic.Bool
}

func (r *boundRegistry) Axis(id string) (farhelper.Status, bool) {
	if !r.bound.Load() {
		return farhelper.Status{State: farhelper.StateUnavailable, Reason: farhelper.ReasonConnection, Action: farhelper.ActionNone}, true
	}
	return r.Registry.Axis(id)
}

func (r *boundRegistry) Acquire(ctx context.Context, id string) (*farhelper.Lease, error) {
	if !r.bound.Load() {
		return nil, &farhelper.Unavailable{Reason: farhelper.ReasonConnection, Action: farhelper.ActionNone}
	}
	return r.Registry.Acquire(ctx, id)
}

func connectionIn(state hostconn.Kind, generation uint64) farhelper.Connection {
	c := panelConnection(panelID)
	c.State = state
	c.Generation = generation
	return c
}

// waitStatus blocks until the axis publishes a status the test is waiting for.
// The registry's own publications are the signal, so nothing here depends on how
// long a decision takes.
func waitStatus(t *testing.T, r *farhelper.Registry, want func(farhelper.Status) bool) {
	t.Helper()
	seen := make(chan farhelper.Status, 8)
	stop := r.Watch(func(id string, status farhelper.Status) {
		if id == panelID {
			select {
			case seen <- status:
			default:
			}
		}
	})
	defer stop()
	if status, known := r.Axis(panelID); known && want(status) {
		return
	}
	guard := time.NewTimer(testBudget)
	defer guard.Stop()
	for {
		select {
		case status := <-seen:
			if want(status) {
				return
			}
		case <-guard.C:
			status, known := r.Axis(panelID)
			t.Fatalf("axis never reached the expected status: %+v (known %v)", status, known)
		}
	}
}

func TestHelperFilesystemLostCommitRecovery(t *testing.T) {
	for _, tc := range []struct {
		name             string
		restart, replace bool
		replayOutcome    string
	}{
		{name: "same helper remembers committed", replayOutcome: proto.FSOutcomeCommitted},
		{name: "restarted helper compares matching digest", restart: true, replayOutcome: proto.FSOutcomeUnknown},
		{name: "restarted helper refuses different digest", restart: true, replace: true, replayOutcome: proto.FSOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, journal := tempTree(t), t.TempDir()
			originalInstance := strings.Repeat("a", 32)
			newInstance := originalInstance
			provider := local.New(local.WithRoot(root))
			original := sameHostService(t, provider, journal, originalInstance)
			nextService := original
			if tc.restart {
				newInstance = strings.Repeat("b", 32)
				nextService = sameHostService(t, provider, journal, newInstance)
			}
			var firstLane *pipeLane
			committed := make(chan error, 1)
			first := &fsObserver{Service: original, intercept: func(_ context.Context, op string, _ json.RawMessage, next func() (any, error)) (any, error) {
				out, err := next()
				if op == proto.OpWriteCommit {
					committed <- err
					firstLane.dropReply()
				}
				return out, err
			}}
			firstPeer, lane := servePeer(t, first, originalInstance)
			firstLane = lane
			replayed := make(chan proto.FSWriteCommitResult, 1)
			second := &fsObserver{Service: nextService, intercept: func(_ context.Context, op string, _ json.RawMessage, next func() (any, error)) (any, error) {
				out, err := next()
				if op == proto.OpWriteCommit && err == nil {
					reply, ok := out.(proto.FSWriteCommitResult)
					if !ok {
						t.Errorf("write-commit returned %T, want FSWriteCommitResult", out)
					}
					replayed <- reply
				}
				return out, err
			}}
			secondPeer, _ := servePeer(t, second, newInstance)
			clock := retryClock{Clock: hostconntest.New(), armed: make(chan time.Duration, 1)}
			r, _, _ := registry(t, clock, map[string][]*client.Client{panelID: {firstPeer, secondPeer}})
			observedRegistry := recoveryRegistry{Registry: r, lost: make(chan struct{}, 1)}
			p := helperfs.New(observedRegistry, panelID, root)
			ctx := testContext(t)
			intended, other := []byte("nocx original data"), []byte("external edit data")
			if len(other) != len(intended) {
				t.Fatal("comparison must disprove identical size as evidence")
			}
			u := transfer.Upload{DestDir: root, Name: "note", Size: int64(len(intended)), OnExists: transfer.Overwrite}
			path := filepath.Join(root, u.Name)
			type answer struct {
				out transfer.Outcome
				err error
			}
			done := make(chan answer, 1)
			go func() {
				out, err := p.Sink().Put(ctx, u, bytes.NewReader(intended), func(int64) {})
				done <- answer{out, err}
			}()
			if err := receive(t, committed); err != nil {
				t.Fatalf("the first commit failed locally: %v", err)
			}
			if !bytes.Equal(readFile(t, path), intended) {
				t.Fatal("the original commit did not write the intended bytes")
			}
			wantBytes := intended
			if tc.replace {
				replacement := filepath.Join(root, "external")
				writeFile(t, replacement, other)
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
				wantBytes = other
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			retryIn := receive(t, clock.armed)
			select {
			case early := <-done:
				t.Fatalf("commit returned before the registry retried: %+v (%v)", early.out, early.err)
			case <-observedRegistry.lost:
			case <-ctx.Done():
				t.Fatal("provider did not acquire while the real registry was broken")
			}
			clock.Advance(retryIn)
			// The only path to the second helper is the registry's normal
			// loss/backoff/redial loop; the test never calls Acquire again.
			got := receive(t, done)
			if tc.replace {
				if !errors.As(got.err, new(*filesystem.ErrUnknownWriteOutcome)) {
					t.Fatalf("lost commit with different bytes at target: %+v (%v), want ErrUnknownWriteOutcome", got.out, got.err)
				}
			} else if got.err != nil || got.out.State != transfer.StateWritten || got.out.FinalName != u.Name {
				t.Fatalf("successful recovery: %+v (%v)", got.out, got.err)
			}
			reply := receive(t, replayed)
			if reply.Outcome != tc.replayOutcome {
				t.Fatalf("replacement helper answered %q, want %q", reply.Outcome, tc.replayOutcome)
			}
			firstCalls, secondCalls := first.observed(), second.observed()
			if !slices.Equal(ops(firstCalls), []string{proto.OpWriteOpen, proto.OpWrite, proto.OpWriteCommit}) ||
				!slices.Equal(ops(secondCalls), []string{proto.OpWriteCommit}) ||
				!bytes.Equal(firstCalls[2].params, secondCalls[0].params) {
				t.Fatalf("recovery must send exactly the same commit and no write data: first=%v, second=%v", ops(firstCalls), ops(secondCalls))
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(before, after) || !bytes.Equal(readFile(t, path), wantBytes) {
				t.Fatalf("replaying commit rewrote the current file: %v", err)
			}
		})
	}
}

type putAnswer struct {
	out transfer.Outcome
	err error
}

func putOver(t *testing.T, provider *helperfs.Provider, root string) <-chan putAnswer {
	t.Helper()
	data := []byte("intended bytes")
	upload := transfer.Upload{DestDir: root, Name: "note", Size: int64(len(data)), OnExists: transfer.Overwrite}
	done := make(chan putAnswer, 1)
	ctx := testContext(t)
	go func() {
		out, err := provider.Sink().Put(ctx, upload, bytes.NewReader(data), nil)
		done <- putAnswer{out, err}
	}()
	return done
}

// A commit whose reply is lost to a transport cut is not a failed commit. The
// daemon on the other side survives the cut and still remembers the answer, and
// the recovery budget exists precisely to wait for a link that comes back — so a
// paused axis is a reason to keep asking, never an answer.
func TestHelperFilesystemLostCommitReplySurvivesACut(t *testing.T) {
	root, instance := tempTree(t), strings.Repeat("a", 32)
	// One service behind both peers: this is one daemon, reached twice.
	svc := sameHostService(t, local.New(local.WithRoot(root)), t.TempDir(), instance)
	var lane *pipeLane
	var conns *panelConnections
	var reg *farhelper.Registry
	committed := make(chan struct{}, 1)
	first := &fsObserver{Service: svc, intercept: func(_ context.Context, op string, _ json.RawMessage, next func() (any, error)) (any, error) {
		out, err := next()
		if op == proto.OpWriteCommit {
			conns.change(connectionIn(hostconn.Stalled, 1))
			waitStatus(t, reg, func(s farhelper.Status) bool { return s.State == farhelper.StatePaused })
			lane.dropReply()
			committed <- struct{}{}
		}
		return out, err
	}}
	firstPeer, firstLane := servePeer(t, first, instance)
	lane = firstLane
	second := &fsObserver{Service: svc}
	secondPeer, _ := servePeer(t, second, instance)
	reg, conns, _ = registry(t, nil, map[string][]*client.Client{panelID: {firstPeer, secondPeer}})
	observed := recoveryRegistry{Registry: reg, paused: make(chan struct{}, 1)}
	done := putOver(t, helperfs.New(observed, panelID, root), root)
	receive(t, committed)
	// The provider has already asked for a replacement and met a paused
	// transport. Only now does the link come back, so the wait this test sees is
	// observed recovery and not a race against the loss.
	receive(t, observed.paused)
	conns.change(connectionIn(hostconn.Connected, 2))
	got := receive(t, done)
	if got.err != nil || got.out.State != transfer.StateWritten || got.out.FinalName != "note" {
		t.Fatalf("lost commit reply across a cut: %+v (%v), want the reconnected helper's committed answer", got.out, got.err)
	}
	firstCalls, secondCalls := first.observed(), second.observed()
	if !slices.Equal(ops(firstCalls), []string{proto.OpWriteOpen, proto.OpWrite, proto.OpWriteCommit}) ||
		!slices.Equal(ops(secondCalls), []string{proto.OpWriteCommit}) ||
		!bytes.Equal(firstCalls[2].params, secondCalls[0].params) {
		t.Fatalf("the replay must resend the commit alone: first=%v, second=%v", ops(firstCalls), ops(secondCalls))
	}
	if !bytes.Equal(readFile(t, filepath.Join(root, "note")), []byte("intended bytes")) {
		t.Fatal("the recovered commit did not leave the intended bytes")
	}
}

// A connection that is gone for good is a final answer: the axis entry is gone,
// so there is nothing left to wait for and the outcome has to be called unknown
// at once. The fixture's ten-second budget is the assertion — a provider that
// waited would only leave on the twenty-second mutation deadline.
func TestHelperFilesystemLostCommitReplyOnAnEndedConnection(t *testing.T) {
	root, instance := tempTree(t), strings.Repeat("a", 32)
	svc := sameHostService(t, local.New(local.WithRoot(root)), t.TempDir(), instance)
	var lane *pipeLane
	var conns *panelConnections
	first := &fsObserver{Service: svc, intercept: func(_ context.Context, op string, _ json.RawMessage, next func() (any, error)) (any, error) {
		out, err := next()
		if op == proto.OpWriteCommit {
			// Ended removes the connection's entry before the reply is lost, so
			// the provider can no longer read this connection's axis at all.
			conns.change(connectionIn(hostconn.Ended, 1))
			lane.dropReply()
		}
		return out, err
	}}
	peer, firstLane := servePeer(t, first, instance)
	lane = firstLane
	reg, panel, _ := registry(t, nil, map[string][]*client.Client{panelID: {peer}})
	conns = panel
	done := putOver(t, helperfs.New(reg, panelID, root), root)
	got := receive(t, done)
	assertFinalLostOutcome(t, got, "an ended connection")
}

// A pane rebound to another connection is the same final answer, and it reaches
// the provider through the shape internal/app installs: the axis is known and
// unavailable, with the connection as its reason.
func TestHelperFilesystemLostCommitReplyAfterAPaneRebound(t *testing.T) {
	root, instance := tempTree(t), strings.Repeat("a", 32)
	svc := sameHostService(t, local.New(local.WithRoot(root)), t.TempDir(), instance)
	var lane *pipeLane
	var bound *boundRegistry
	first := &fsObserver{Service: svc, intercept: func(_ context.Context, op string, _ json.RawMessage, next func() (any, error)) (any, error) {
		out, err := next()
		if op == proto.OpWriteCommit {
			// The pane rides another connection from here on, before the reply
			// this upload waits for ever arrives.
			bound.bound.Store(false)
			lane.dropReply()
		}
		return out, err
	}}
	peer, firstLane := servePeer(t, first, instance)
	lane = firstLane
	reg, _, _ := registry(t, nil, map[string][]*client.Client{panelID: {peer}})
	bound = &boundRegistry{Registry: reg}
	bound.bound.Store(true)
	done := putOver(t, helperfs.New(bound, panelID, root), root)
	got := receive(t, done)
	assertFinalLostOutcome(t, got, "a pane rebound to another connection")
}

// assertFinalLostOutcome wants a lost commit on an axis that cannot come back to
// be reported as unknown at once, never after the mutation budget.
func assertFinalLostOutcome(t *testing.T, got putAnswer, scenario string) {
	t.Helper()
	var unknown *filesystem.ErrUnknownWriteOutcome
	var unavailable *filesystem.ErrUnavailable
	if !errors.As(got.err, &unknown) || !errors.As(got.err, &unavailable) || unavailable.Reason != string(farhelper.ReasonConnection) {
		t.Fatalf("lost commit on %s = %v, want ErrUnknownWriteOutcome over ErrUnavailable(connection)", scenario, got.err)
	}
	if got.out.State == transfer.StateWritten || errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("lost commit on %s waited for a final axis: %+v (%v)", scenario, got.out, got.err)
	}
}

// A helper restart between two chunks must not hand the rest of the session to a
// peer that never opened it: that peer answers bad-session, which is not an
// answer about this upload.
func TestHelperFilesystemChunkAfterHelperRestart(t *testing.T) {
	root := tempTree(t)
	lp := local.New(local.WithRoot(root))
	first := &fsObserver{Service: sameHostService(t, lp, t.TempDir(), strings.Repeat("a", 32))}
	second := &fsObserver{Service: sameHostService(t, lp, t.TempDir(), strings.Repeat("b", 32))}
	firstPeer, firstLane := servePeer(t, first, strings.Repeat("a", 32))
	secondPeer, _ := servePeer(t, second, strings.Repeat("b", 32))
	clock := retryClock{Clock: hostconntest.New(), armed: make(chan time.Duration, 1)}
	reg, _, _ := registry(t, clock, map[string][]*client.Client{panelID: {firstPeer, secondPeer}})
	p := helperfs.New(reg, panelID, root)
	ctx := testContext(t)
	data := bytes.Repeat([]byte("x"), transfer.DefaultChunk+10)
	src := &pausedSource{data: data, first: transfer.DefaultChunk, handed: make(chan struct{}), release: make(chan struct{})}
	upload := transfer.Upload{DestDir: root, Name: "big", Size: int64(len(data)), OnExists: transfer.Overwrite}
	done := make(chan putAnswer, 1)
	go func() {
		out, err := p.Sink().Put(ctx, upload, src, nil)
		done <- putAnswer{out, err}
	}()
	receive(t, src.handed)
	firstLane.dropReply()
	// The redial's own delay is armed only after the loss was applied, so this
	// is the deterministic point where the restart has happened.
	clock.Advance(receive(t, clock.armed))
	waitStatus(t, reg, func(s farhelper.Status) bool { return s.State == farhelper.StateServing })
	close(src.release)
	got := receive(t, done)
	var unavailable *filesystem.ErrUnavailable
	if !errors.As(got.err, &unavailable) {
		t.Fatalf("a chunk after the helper restarted = %v, want *filesystem.ErrUnavailable (the new helper saw %v)", got.err, ops(second.observed()))
	}
	if slices.Contains(ops(second.observed()), proto.OpWrite) {
		t.Fatalf("the new helper was offered the old session's bytes: %v", ops(second.observed()))
	}
}

// pausedSource hands out one chunk and then waits for the test. A restart between
// two chunks is only observable while the source holds the second one.
type pausedSource struct {
	data    []byte
	first   int
	off     int
	handed  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *pausedSource) Read(p []byte) (int, error) {
	if s.off >= s.first {
		s.once.Do(func() { close(s.handed) })
		<-s.release
	}
	if s.off >= len(s.data) {
		return 0, io.EOF
	}
	end := len(s.data)
	if s.off < s.first {
		end = s.first
	}
	n := copy(p, s.data[s.off:end])
	s.off += n
	return n, nil
}
