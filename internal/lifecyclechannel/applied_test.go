package lifecyclechannel

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecodec"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/waittest"
)

// THE FRAME SCOPE (ADR-0077). The coordinator resumes the helper's lifecycle
// stream at the offset of the last frame whose effect it stored, so the
// adapter — the one place that knows where a frame ends — hands each frame to
// the scope with that position, and the scope's ingest is the kernel's
// application of it. The log below records the position once the scope's
// ingest has returned, which is when a store commits the frame.

// appliedLog records every position the adapter reported, and what the kernel
// had been told was applied at the moment each Ingest began.
type appliedLog struct {
	mu        sync.Mutex
	reported  []uint64
	atIngest  []uint64
	events    []string
	ingesting int
}

// scope is a FrameScope that applies the frame and then records where it
// ends.
func (l *appliedLog) scope(consumed uint64, _ lifecycle.EventKind, apply func(ctx context.Context)) error {
	apply(context.Background())
	l.report(consumed)
	return nil
}

func (l *appliedLog) report(pos uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reported = append(l.reported, pos)
	l.events = append(l.events, "applied")
}

func (l *appliedLog) last() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.reported) == 0 {
		return 0
	}
	return l.reported[len(l.reported)-1]
}

func (l *appliedLog) note(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *appliedLog) snapshot() ([]uint64, []uint64, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]uint64(nil), l.reported...), append([]uint64(nil), l.atIngest...),
		append([]string(nil), l.events...)
}

// observingKernel records, at each Ingest, the position last reported
// applied — so a report that ran ahead of the kernel would be seen.
type observingKernel struct {
	AdoptingKernel
	log *appliedLog
	// hold, when set, blocks the FIRST Ingest until it is closed, and entered
	// is closed as that Ingest begins: the frame in flight a detach meets.
	hold    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (k *observingKernel) Ingest(ctx context.Context, t lifecycle.TransportID, env lifecycle.Envelope) error {
	k.log.mu.Lock()
	k.log.atIngest = append(k.log.atIngest, lastLocked(k.log.reported))
	k.log.ingesting++
	k.log.mu.Unlock()
	if k.hold != nil {
		first := false
		k.once.Do(func() { first = true })
		if first {
			close(k.entered)
			<-k.hold
		}
	}
	err := k.AdoptingKernel.Ingest(ctx, t, env)
	k.log.note("ingested:" + string(env.Event.Kind))
	return err
}

func lastLocked(v []uint64) uint64 {
	if len(v) == 0 {
		return 0
	}
	return v[len(v)-1]
}

func encodedFrame(t *testing.T, env lifecycle.Envelope) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := lifecyclecodec.Encode(&buf, env); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestTheAdapterReportsEachFramesEndOnlyOnceTheKernelHasAppliedIt(t *testing.T) {
	applied := &appliedLog{}
	k := &observingKernel{AdoptingKernel: newTestKernel(), log: applied}
	coordinator, shell := net.Pipe()
	t.Cleanup(func() { _ = shell.Close() })
	a, err := NewAdoptedStream(log.NewSlogAdapter(nil), k, coordinator, adoptedLaunch(),
		WithFrameScope(applied.scope))
	if err != nil {
		t.Fatalf("NewAdoptedStream: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	id := lifecycle.AttemptID("shell-77")
	code := 0
	start := encodedFrame(t, shellEnv(a, 77, lifecycle.Event{
		Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &id, Command: "make"},
	}))
	// The same sequence again is a replay the kernel REFUSES: it is still a
	// frame the stream carried and this coordinator has dealt with, so the
	// cursor passes it — resuming before it would only offer it again.
	replay := encodedFrame(t, shellEnv(a, 77, lifecycle.Event{
		Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &id, Command: "make"},
	}))
	complete := encodedFrame(t, shellEnv(a, 78, lifecycle.Event{
		Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{7}},
	}))
	var stream []byte
	stream = append(stream, start...)
	stream = append(stream, replay...)
	stream = append(stream, complete...)
	go func() { _, _ = shell.Write(stream) }()

	//nolint:gosec // test byte counts, never negative
	ends := []uint64{uint64(len(start)), uint64(len(start) + len(replay)), uint64(len(stream))}
	waittest.WaitFor(t, "every frame was reported applied", func() bool {
		return applied.last() == ends[2]
	})
	reported, atIngest, _ := applied.snapshot()
	if len(reported) != 3 {
		t.Fatalf("reported %v, want one position per frame: %v", reported, ends)
	}
	for i := range ends {
		if reported[i] != ends[i] {
			t.Fatalf("frame %d reported applied at %d, want %d — the position its bytes end at", i, reported[i], ends[i])
		}
	}
	// Each report followed its own Ingest: when frame i began, only the
	// frames before it had been reported.
	wantAtIngest := []uint64{0, ends[0], ends[1]}
	for i := range wantAtIngest {
		if atIngest[i] != wantAtIngest[i] {
			t.Fatalf("when frame %d reached the kernel the cursor already read %d, want %d: a frame was reported applied before the kernel had it",
				i, atIngest[i], wantAtIngest[i])
		}
	}
}

// A coordinator going away detaches the leg (ADR-0076). The frame the kernel
// is applying at that moment is finished — its effect and its cursor both
// land — and no frame after it is applied: that one belongs to the next
// coordinator, which resumes the stream at the cursor this one stored.
// Applying it here, after the block stream has already let the session go,
// would store its cursor with only half of its effect.
func TestDetachFinishesTheFrameInFlightAndAppliesNoFrameAfterIt(t *testing.T) {
	applied := &appliedLog{}
	k := &observingKernel{
		AdoptingKernel: newTestKernel(), log: applied,
		hold: make(chan struct{}), entered: make(chan struct{}),
	}
	coordinator, shell := net.Pipe()
	t.Cleanup(func() { _ = shell.Close() })
	a, err := NewAdoptedStream(log.NewSlogAdapter(nil), k, coordinator, adoptedLaunch(),
		WithFrameScope(applied.scope))
	if err != nil {
		t.Fatalf("NewAdoptedStream: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	id := lifecycle.AttemptID("shell-80")
	code := 0
	start := encodedFrame(t, shellEnv(a, 80, lifecycle.Event{
		Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &id, Command: "make"},
	}))
	complete := encodedFrame(t, shellEnv(a, 81, lifecycle.Event{
		Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{8}},
	}))
	// ONE write: the decoder takes both frames in a single read, so the
	// second is already buffered when the detach arrives — nothing on the
	// carrier stops it, only the adapter can.
	go func() { _, _ = shell.Write(append(append([]byte(nil), start...), complete...)) }()
	<-k.entered

	detached := make(chan struct{})
	go func() {
		_ = a.Detach()
		applied.note("detach-returned")
		close(detached)
	}()
	waittest.WaitFor(t, "the detach began", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.closed
	})
	close(k.hold)
	<-detached
	<-a.pumpDone

	reported, _, events := applied.snapshot()
	if len(reported) != 1 || reported[0] != uint64(len(start)) {
		t.Fatalf("reported %v, want exactly the frame in flight (%d): a frame after the detach was applied, or the one in flight was not",
			reported, len(start))
	}
	want := []string{"ingested:start", "applied", "detach-returned"}
	if len(events) != len(want) {
		t.Fatalf("events %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events %v, want %v: the detach returned before the frame in flight was applied", events, want)
		}
	}
}

// A frame whose effect the store could not record ends the leg: nothing of
// it was stored and the cursor did not move, so every later frame belongs to
// the next coordinator, which resumes before the failed one. The leg ends as
// a handover does — the kernel is not told the shell's transport was lost,
// because it was not: nothing of the domain is marked lost or unknown here.
func TestAFrameTheStoreCouldNotRecordEndsTheLegUnapplied(t *testing.T) {
	applied := &appliedLog{}
	k := &observingKernel{AdoptingKernel: newTestKernel(), log: applied}
	coordinator, shell := net.Pipe()
	t.Cleanup(func() { _ = shell.Close() })
	failing := func(consumed uint64, _ lifecycle.EventKind, apply func(ctx context.Context)) error {
		apply(context.Background())
		return errors.New("the store refused the frame")
	}
	var lossMu sync.Mutex
	var losses []LossCause
	a, err := NewAdoptedStream(log.NewSlogAdapter(nil), k, coordinator, adoptedLaunch(), WithFrameScope(failing),
		WithLossReporter(func(_ lifecycle.LaneID, cause LossCause) {
			lossMu.Lock()
			losses = append(losses, cause)
			lossMu.Unlock()
		}))
	if err != nil {
		t.Fatalf("NewAdoptedStream: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	id := lifecycle.AttemptID("shell-90")
	code := 0
	start := encodedFrame(t, shellEnv(a, 90, lifecycle.Event{
		Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &id, Command: "make"},
	}))
	complete := encodedFrame(t, shellEnv(a, 91, lifecycle.Event{
		Kind: lifecycle.KindComplete, Complete: &lifecycle.Complete{ExitCode: &code, Fence: lifecycle.FenceNonce{9}},
	}))
	go func() { _, _ = shell.Write(append(append([]byte(nil), start...), complete...)) }()
	<-a.pumpDone

	_, _, events := applied.snapshot()
	if len(events) != 1 || events[0] != "ingested:start" {
		t.Fatalf("events %v, want only the frame the store refused: a frame after it was applied", events)
	}
	// The pane is told: the halt is a stated loss, not only a log line.
	lossMu.Lock()
	got := append([]LossCause(nil), losses...)
	lossMu.Unlock()
	if len(got) != 1 || got[0] != LossStoreRefused {
		t.Fatalf("losses reported %v, want exactly store-refused", got)
	}
	if dom, ok := k.Domain(a.domain); !ok || dom.State == lifecycle.DomainLost {
		t.Fatalf("the domain after the halt = %+v (known %v), want it live: a store failure is not the shell's transport lost", dom, ok)
	}
}

// A FRAME LEFT FOR THE NEXT COORDINATOR (ADR-0077's teardown rule): the scope
// answers ErrFrameLeftForNext because this coordinator is stopping. Nothing of
// the frame reaches the kernel, nothing after it is applied, and nothing is
// reported — a handover, not a loss.
func TestAFrameLeftForTheNextCoordinatorIsNotAppliedAndReportsNothing(t *testing.T) {
	applied := &appliedLog{}
	k := &observingKernel{AdoptingKernel: newTestKernel(), log: applied}
	coordinator, shell := net.Pipe()
	t.Cleanup(func() { _ = shell.Close() })
	stopping := func(uint64, lifecycle.EventKind, func(ctx context.Context)) error { return ErrFrameLeftForNext }
	var lossMu sync.Mutex
	var losses []LossCause
	a, err := NewAdoptedStream(log.NewSlogAdapter(nil), k, coordinator, adoptedLaunch(), WithFrameScope(stopping),
		WithLossReporter(func(_ lifecycle.LaneID, cause LossCause) {
			lossMu.Lock()
			losses = append(losses, cause)
			lossMu.Unlock()
		}))
	if err != nil {
		t.Fatalf("NewAdoptedStream: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })

	id := lifecycle.AttemptID("shell-95")
	start := encodedFrame(t, shellEnv(a, 95, lifecycle.Event{
		Kind: lifecycle.KindStart, Start: &lifecycle.Start{AttemptID: &id, Command: "make"},
	}))
	go func() { _, _ = shell.Write(start) }()
	<-a.pumpDone

	if _, _, events := applied.snapshot(); len(events) != 0 {
		t.Fatalf("events %v, want none: a frame left for the next coordinator reached this kernel", events)
	}
	lossMu.Lock()
	defer lossMu.Unlock()
	if len(losses) != 0 {
		t.Fatalf("losses reported %v, want none: stopping is a handover", losses)
	}
}
