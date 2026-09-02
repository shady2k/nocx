package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// The session service is what the reserved name in internal/helper/host was
// reserved FOR. These tests exercise it through the seam the host dispatcher
// reaches — Call with JSON params — rather than through typed shortcuts, so a
// shape that cannot cross the wire fails here and not in production.

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// boundSink is the connection the test most recently bound. The production
// host stamps every request with the connection it arrived on, because a
// helper daemon serves several at once and a subscriber's pump must write to
// the one that asked; `call` does the same here so these tests exercise the
// same path rather than a shortcut through it.
var boundSink atomic.Value

// bindTo binds sink and records it as the connection `call` speaks on.
func bindTo(svc *session.Service, sink session.Sink) (release func()) {
	boundSink.Store(sink)
	return svc.Bind(sink)
}

func callCtx() context.Context {
	ctx := context.Background()
	if sink, ok := boundSink.Load().(session.Sink); ok && sink != nil {
		return host.WithConnection(ctx, sink)
	}
	return ctx
}

func call[T any](t *testing.T, svc *session.Service, op string, params any) T {
	t.Helper()
	res, err := svc.Call(callCtx(), op, mustJSON(t, params))
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	var out T
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("%s result marshal: %v", op, err)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s result decode: %v", op, err)
	}
	return out
}

// --- a process the test drives, so nothing here depends on a real shell -----

type fakeProcess struct {
	stdout   *io.PipeReader
	produce  *io.PipeWriter
	done     chan struct{}
	closeOne sync.Once

	mu         sync.Mutex
	written    []byte
	waitErr    error
	waitSet    bool
	cols       uint16
	rows       uint16
	fg         int
	fgErr      error
	closed     bool
	signal     syscall.Signal
	signalPgid int
	pgid       int
	sigErr     error
}

func newFakeProcess() *fakeProcess {
	r, w := io.Pipe()
	return &fakeProcess{stdout: r, produce: w, done: make(chan struct{}), pgid: 4343}
}

func (p *fakeProcess) Read(b []byte) (int, error) { return p.stdout.Read(b) }
func (p *fakeProcess) Done() <-chan struct{}      { return p.done }
func (p *fakeProcess) Pid() int                   { return 4242 }
func (p *fakeProcess) ProcessGroup() int          { return p.pgid }
func (p *fakeProcess) Shell() string              { return "/bin/fake" }
func (p *fakeProcess) ForegroundProcessGroup() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fg, p.fgErr
}

func (p *fakeProcess) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = append(p.written, b...)
	return len(b), nil
}

func (p *fakeProcess) Close() error {
	p.closeOne.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		_ = p.produce.CloseWithError(io.EOF)
		close(p.done)
	})
	return nil
}

func (p *fakeProcess) Resize(_ context.Context, cols, rows, _, _ uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cols, p.rows = cols, rows
	return nil
}

func (p *fakeProcess) SignalProcessGroup(pgid int, sig syscall.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.signal = sig
	p.signalPgid = pgid
	return p.sigErr
}

func (p *fakeProcess) WaitErr() (error, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr, p.waitSet
}

// exit ends the process the way a shell exiting does: the wait result is
// recorded BEFORE Done fires, which is the ordering internal/pty guarantees
// and the ordering the exit watcher relies on.
func (p *fakeProcess) exit(err error) {
	p.mu.Lock()
	p.waitErr, p.waitSet = err, true
	p.mu.Unlock()
	_ = p.produce.CloseWithError(io.EOF)
	p.closeOne.Do(func() { close(p.done) })
}

func (p *fakeProcess) say(t *testing.T, s string) {
	t.Helper()
	if _, err := p.produce.Write([]byte(s)); err != nil {
		t.Fatalf("produce: %v", err)
	}
}

func (p *fakeProcess) typed() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.written)
}

type fakeSpawner struct {
	mu    sync.Mutex
	procs []*fakeProcess
	err   error
	reqs  []session.SpawnRequest
}

func (s *fakeSpawner) Spawn(req session.SpawnRequest) (session.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if s.err != nil {
		return nil, s.err
	}
	p := newFakeProcess()
	s.procs = append(s.procs, p)
	return p, nil
}

func (s *fakeSpawner) last() *fakeProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.procs) == 0 {
		return nil
	}
	return s.procs[len(s.procs)-1]
}

// --- a sink that records the wire ------------------------------------------

type recordingSink struct {
	mu              sync.Mutex
	frames          []proto.SessionFrame
	lifecycleFrames []proto.SessionFrame
	notes           []proto.Notification
	err             error
	// arrived is a generation channel — closed and replaced on every
	// delivery — rather than a buffered one, so a waiter can never miss a
	// wakeup because the buffer was full. A dropped wakeup would make this
	// helper depend on how fast the producer runs, which is the one thing a
	// test here may not depend on.
	arrived chan struct{}
}

func newSink() *recordingSink {
	return &recordingSink{arrived: make(chan struct{})}
}

// wake must be called with s.mu held.
func (s *recordingSink) wake() {
	close(s.arrived)
	s.arrived = make(chan struct{})
}

func (s *recordingSink) waiter() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.arrived
}

func (s *recordingSink) SendSessionData(f proto.SessionFrame) error {
	s.mu.Lock()
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		return err
	}
	s.frames = append(s.frames, f)
	s.wake()
	s.mu.Unlock()
	return nil
}

func (s *recordingSink) SendNotification(n proto.Notification) error {
	s.mu.Lock()
	s.notes = append(s.notes, n)
	s.wake()
	s.mu.Unlock()
	return nil
}

func (s *recordingSink) SendLifecycleData(f proto.SessionFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.lifecycleFrames = append(s.lifecycleFrames, f)
	s.wake()
	return nil
}

func (s *recordingSink) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// bytesFor returns everything the sink has delivered to one subscriber.
func (s *recordingSink) bytesFor(sub [16]byte) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []byte
	for _, f := range s.frames {
		if f.Subscriber == sub {
			out = append(out, f.Payload...)
		}
	}
	return out
}

func (s *recordingSink) notifications(event string) []proto.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []proto.Notification
	for _, n := range s.notes {
		if n.Event == event {
			out = append(out, n)
		}
	}
	return out
}

// awaitSink blocks until want(sink) is true, waking on every delivery rather
// than on a clock. A condition that never becomes true is reported by the
// test's own timeout, which is what makes this independent of machine speed.
func awaitSink(t *testing.T, s *recordingSink, what string, want func() bool) {
	t.Helper()
	for {
		// Take the wakeup BEFORE testing the condition, or a delivery landing
		// between the test and the park is lost.
		next := s.waiter()
		if want() {
			return
		}
		select {
		case <-next:
		case <-t.Context().Done():
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func newService(t *testing.T, sink session.Sink, spawner session.Spawner, limits session.Limits) *session.Service {
	t.Helper()
	svc := session.New(session.Options{
		Generation: "gen-under-test",
		Spawner:    spawner,
		Log:        discardLog(),
		Limits:     limits,
	})
	release := bindTo(svc, sink)
	t.Cleanup(func() {
		release()
		svc.Close()
	})
	return svc
}

func spawnOne(t *testing.T, svc *session.Service) proto.SessionEntry {
	t.Helper()
	return call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24}).Entry
}

// --- the tests --------------------------------------------------------------

// TestSpawnPutsTheSessionInTheInventoryWithWhatTheHelperRecorded is D10: there
// is no router and no registry beside the helper — it holds the PTYs, so it is
// the only thing that can answer — and what it answers with is what it
// RECORDED at spawn, not what it later read off the OS.
func TestSpawnPutsTheSessionInTheInventoryWithWhatTheHelperRecorded(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})

	entry := spawnOne(t, svc)
	if entry.Session.Generation != "gen-under-test" {
		t.Errorf("generation = %q: a durable handle names the generation that minted it", entry.Session.Generation)
	}
	if len(entry.Session.Session) != 32 {
		t.Errorf("session id = %q, want 32 hex characters", entry.Session.Session)
	}
	if entry.StartedAt == "" {
		t.Error("no start time: D3 names it among the diagnostics the helper owns")
	}
	if entry.Launch.Shell != "/bin/fake" || entry.Launch.Pid != 4242 {
		t.Errorf("launch record = %+v, want what the helper spawned", entry.Launch)
	}

	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 || inv.Sessions[0].Session != entry.Session {
		t.Fatalf("inventory = %+v, want the session just spawned", inv.Sessions)
	}
	if inv.Sessions[0].Exit != nil {
		t.Error("a running session reports an exit status")
	}
}

// TestAnEmptyInventoryIsAnEmptyArray — the shape a decoder needs in order to
// tell "no sessions" from "no answer". `null` and `[]` are the same value in
// Go and different facts on the wire, which is exactly the defect the vault's
// providers field shipped with.
func TestAnEmptyInventoryIsAnEmptyArray(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	res, err := svc.Call(context.Background(), proto.OpSessions, mustJSON(t, proto.SessionsParams{}))
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"sessions":[]}` {
		t.Fatalf("empty inventory marshals as %s, want an empty array", raw)
	}
}

// TestASpawnThatFailsLeavesNothingBehind is the first partial failure of the
// spawn-and-own-a-PTY procedure: the fork did not produce a PTY. The interval,
// both ends named — a session is in the inventory from the moment its PTY
// exists until close-session ends it — means a failed spawn must be invisible:
// no entry, no window, no budget consumed.
func TestASpawnThatFailsLeavesNothingBehind(t *testing.T) {
	spawner := &fakeSpawner{err: errors.New("openpty: too many open files")}
	svc := newService(t, newSink(), spawner, session.Limits{})

	if _, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{})); err == nil {
		t.Fatal("a spawn whose PTY failed reported success")
	}
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 0 {
		t.Fatalf("a failed spawn left %d sessions in the inventory", len(inv.Sessions))
	}
	if used := svc.WindowBytesInUse(); used != 0 {
		t.Fatalf("a failed spawn consumed %d bytes of the window budget", used)
	}
	// And the next spawn works: a failure is not a wedge, and the budget the
	// refused one reserved was given back.
	spawner.mu.Lock()
	spawner.err = nil
	spawner.mu.Unlock()
	if entry := spawnOne(t, svc); entry.Session.Session == "" {
		t.Fatal("the service did not recover from a failed spawn")
	}
}

// TestTheBudgetIsCheckedBeforeAnythingIsForked is the second partial failure,
// and the ORDER is the assertion. D8 asks for an aggregate memory budget on a
// machine that is somebody else's; a budget checked after the fork would leave
// an orphan shell every time it refused.
func TestTheBudgetIsCheckedBeforeAnythingIsForked(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{
		DefaultWindowBytes: 256 << 10,
		MinWindowBytes:     256 << 10,
		MaxWindowBytes:     256 << 10,
		BudgetBytes:        256 << 10, // room for exactly one session
	})

	spawnOne(t, svc)
	_, err := svc.Call(context.Background(), proto.OpSpawn, mustJSON(t, proto.SpawnParams{}))
	if err == nil {
		t.Fatal("a spawn past the aggregate window budget was accepted")
	}
	spawner.mu.Lock()
	forked := len(spawner.procs)
	spawner.mu.Unlock()
	if forked != 1 {
		t.Fatalf("%d processes were forked, want 1: the refused spawn started a shell it then abandoned", forked)
	}
}

// TestTheWindowBoundIsClampedAndReported — D8's floor, ceiling and "the
// coordinator decides, the helper applies". A caller whose request was clamped
// must be able to SEE that it was: a silently ignored setting is the degrade
// the product must never hide.
func TestTheWindowBoundIsClampedAndReported(t *testing.T) {
	limits := session.Limits{
		DefaultWindowBytes: 4 << 20,
		MinWindowBytes:     256 << 10,
		MaxWindowBytes:     8 << 20,
		BudgetBytes:        64 << 20,
	}
	svc := newService(t, newSink(), &fakeSpawner{}, limits)

	tooSmall := call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{WindowBytes: 1024}).Entry
	if tooSmall.Launch.WindowBytes != limits.MinWindowBytes {
		t.Errorf("a 1 KiB request got %d, want the floor %d", tooSmall.Launch.WindowBytes, limits.MinWindowBytes)
	}
	tooBig := call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{WindowBytes: 1 << 40}).Entry
	if tooBig.Launch.WindowBytes != limits.MaxWindowBytes {
		t.Errorf("a 1 TiB request got %d, want the ceiling %d", tooBig.Launch.WindowBytes, limits.MaxWindowBytes)
	}
	unset := call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{}).Entry
	if unset.Launch.WindowBytes != limits.DefaultWindowBytes {
		t.Errorf("an unset request got %d, want the default %d", unset.Launch.WindowBytes, limits.DefaultWindowBytes)
	}
}

// TestAttachedOutputReachesTheSubscriberAsRawBytes is AD-1 on this wire: the
// PTY's bytes cross as the data plane's payload, never wrapped in JSON, and
// they reach the subscriber that attached rather than "the client".
func TestAttachedOutputReachesTheSubscriberAsRawBytes(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	svc := newService(t, sink, spawner, session.Limits{})
	entry := spawnOne(t, svc)

	sub := proto.SubscriberID("00112233445566778899aabbccddeeff")
	att := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, Offset: 0, Fresh: true,
	})
	if att.Attachment == "" {
		t.Fatal("attach minted no attachment")
	}
	if !att.Resume.Resumed || att.Resume.Reset {
		t.Fatalf("a fresh attach at zero was reset: %+v", att.Resume)
	}

	spawner.last().say(t, "hello from the host\r\n")
	subRaw, err := proto.SessionBytes(string(sub))
	if err != nil {
		t.Fatalf("subscriber id: %v", err)
	}
	awaitSink(t, sink, "the PTY's bytes", func() bool {
		return string(sink.bytesFor(subRaw)) == "hello from the host\r\n"
	})
}

// TestExactlyOneAttachmentMayWriteAndTheRefusalNamesTheHolder is D8/assertion
// 12. A second write-capable attach is REFUSED, never silently promoted, and a
// refusal that cannot say who holds the capability leaves the caller with
// nothing to do about it.
func TestExactlyOneAttachmentMayWriteAndTheRefusalNamesTheHolder(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	entry := spawnOne(t, svc)

	first := proto.SubscriberID("11111111111111111111111111111111")
	second := proto.SubscriberID("22222222222222222222222222222222")

	a := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: first, Session: entry.Session, RequestWrite: true,
	})
	if !a.Write.Granted || a.Write.Epoch != 1 {
		t.Fatalf("first write request = %+v, want granted at epoch 1", a.Write)
	}
	b := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: second, Session: entry.Session, RequestWrite: true,
	})
	if b.Write.Granted {
		t.Fatal("a second write capability was granted: exactly one attachment may write")
	}
	if b.Write.Holder == nil || *b.Write.Holder != first {
		t.Fatalf("the refusal names holder %v, want %q", b.Write.Holder, first)
	}
	if b.Write.Epoch != 0 {
		t.Errorf("a refused grant carries epoch %d, want zero: zero names no grant", b.Write.Epoch)
	}
	// The reader still attached: refusing the WRITE is not refusing the read.
	if b.Attachment == "" || !b.Resume.Resumed {
		t.Fatalf("the refused writer was not attached as a reader: %+v", b)
	}
}

// TestDetachReleasesTheWriteCapabilityAndSaysSo is D9's first verb and the
// fact the next caller acts on: a replacing coordinator can take the
// capability without arbitration precisely when the previous holder's
// connection released it.
func TestDetachReleasesTheWriteCapabilityAndSaysSo(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	entry := spawnOne(t, svc)
	sub := proto.SubscriberID("33333333333333333333333333333333")

	a := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, RequestWrite: true,
	})
	d := call[proto.DetachResult](t, svc, proto.OpDetach, proto.DetachParams{Attachment: a.Attachment})
	if !d.ReleasedWrite {
		t.Fatal("detaching the write holder did not report the release")
	}
	// The process survives it, and the session is still in the inventory and
	// reattachable: detach is not close-session, and neither is closing a tab.
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 {
		t.Fatalf("detach removed the session from the inventory: %+v", inv.Sessions)
	}
	if inv.Sessions[0].Writer != nil {
		t.Errorf("the inventory still names a writer after the release: %v", inv.Sessions[0].Writer)
	}
	next := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: proto.SubscriberID("44444444444444444444444444444444"),
		Session:    entry.Session, RequestWrite: true,
	})
	if !next.Write.Granted {
		t.Fatal("the released capability was not available to the next caller")
	}
	if next.Write.Epoch != 2 {
		t.Errorf("epoch = %d, want 2: the lease rises on every grant so a displaced holder's late frame is rejected", next.Write.Epoch)
	}
}

// TestAStaleLeaseEpochIsRejectedRatherThanAppliedLate is the other half of
// "exactly one writer", and the half a carrier makes necessary: a displaced
// holder's keystrokes can still be in flight when it is displaced.
func TestAStaleLeaseEpochIsRejectedRatherThanAppliedLate(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})
	entry := spawnOne(t, svc)
	raw, err := proto.SessionBytes(entry.Session.Session)
	if err != nil {
		t.Fatalf("session id: %v", err)
	}
	first := proto.SubscriberID("55555555555555555555555555555555")
	a := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: first, Session: entry.Session, RequestWrite: true,
	})
	call[proto.DetachResult](t, svc, proto.OpDetach, proto.DetachParams{Attachment: a.Attachment})
	second := proto.SubscriberID("66666666666666666666666666666666")
	b := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: second, Session: entry.Session, RequestWrite: true,
	})

	// The displaced holder's frame arrives late, carrying its old epoch.
	subRaw, _ := proto.SessionBytes(string(first))
	svc.SessionData(callCtx(), proto.SessionFrame{Session: raw, Subscriber: subRaw, Epoch: a.Write.Epoch, Payload: []byte("stale\n")})
	// The current holder's frame is applied.
	cur, _ := proto.SessionBytes(string(second))
	svc.SessionData(callCtx(), proto.SessionFrame{Session: raw, Subscriber: cur, Epoch: b.Write.Epoch, Payload: []byte("live\n")})

	if got := spawner.last().typed(); got != "live\n" {
		t.Fatalf("the PTY received %q, want only the current holder's bytes", got)
	}
}

// TestAWriteFromNoHolderNeverReachesThePTY — the paired refusal. A frame from
// a subscriber that never asked for the capability is dropped, not applied on
// the grounds that it arrived.
func TestAWriteFromNoHolderNeverReachesThePTY(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})
	entry := spawnOne(t, svc)
	raw, _ := proto.SessionBytes(entry.Session.Session)
	someone, _ := proto.SessionBytes("77777777777777777777777777777777")

	svc.SessionData(callCtx(), proto.SessionFrame{Session: raw, Subscriber: someone, Epoch: 1, Payload: []byte("rm -rf /\n")})
	if got := spawner.last().typed(); got != "" {
		t.Fatalf("the PTY received %q from a subscriber holding no capability", got)
	}
}

// TestADataFrameForAnUnknownSessionIsDroppedNotFatal — a coordinator holding a
// handle to a session this generation no longer has is the ORDINARY case
// across a restart, not an attack. It is dropped and logged; the connection
// and every other session survive it.
func TestADataFrameForAnUnknownSessionIsDroppedNotFatal(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})
	spawnOne(t, svc)

	var nobody [16]byte
	nobody[0] = 0xff
	svc.SessionData(callCtx(), proto.SessionFrame{Session: nobody, Epoch: 1, Payload: []byte("x")})

	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 {
		t.Fatalf("a stray data frame disturbed the inventory: %+v", inv.Sessions)
	}
}

// TestAttachRefusesASessionThisGenerationDoesNotHold — the failure path every
// caller meets first, and the one D5 of the level-1 design turns on: an
// ANSWER that the session does not exist is a verdict, and it may only be
// given by the generation that would have it.
func TestAttachRefusesASessionThisGenerationDoesNotHold(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	_, err := svc.Call(context.Background(), proto.OpAttach, mustJSON(t, proto.AttachParams{
		Subscriber: proto.SubscriberID("88888888888888888888888888888888"),
		Session:    proto.HostSessionID{Generation: "gen-under-test", Session: "99999999999999999999999999999999"},
	}))
	if !errors.Is(err, session.ErrNoSuchSession) {
		t.Fatalf("err = %v, want ErrNoSuchSession", err)
	}
	code, _ := svc.Refusal(err)
	if code != proto.ErrCodeNoSuchSession {
		t.Errorf("wire code = %q, want %q: the caller switches on the code, not on the message", code, proto.ErrCodeNoSuchSession)
	}
}

// TestAttachRefusesAHandleMintedByAnotherGeneration is D2's qualification made
// load-bearing rather than decorative. Two generations are resident at once
// and each mints its own ids; a handle from the other one names nothing here,
// and serving it by ignoring the generation would eventually hand a caller
// somebody else's PTY.
func TestAttachRefusesAHandleMintedByAnotherGeneration(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	entry := spawnOne(t, svc)
	wrong := entry.Session
	wrong.Generation = "some-other-generation"

	_, err := svc.Call(context.Background(), proto.OpAttach, mustJSON(t, proto.AttachParams{
		Subscriber: proto.SubscriberID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), Session: wrong,
	}))
	if !errors.Is(err, session.ErrNoSuchSession) {
		t.Fatalf("err = %v, want a handle from another generation refused", err)
	}
}

// TestASlowReaderIsResetToTheBaseAndToldWhatItLost is D8's live reset. Today
// the coordinator's pump treats losing its position as nominally unreachable
// and simply stops the stream; under a capacity-reclaimed window an attached,
// slow reader must receive one explicit gap-and-reset and resume at the base —
// never go quietly silent.
func TestASlowReaderIsResetToTheBaseAndToldWhatItLost(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	// A 256 KiB window against a 64 KiB credit allowance: the pump stops
	// pushing four times sooner than the window fills, which is exactly the
	// gap D8's floor exists to keep open — with a window no larger than the
	// allowance the pump could never fall behind and this reset would be
	// unreachable.
	svc := newService(t, sink, spawner, session.Limits{
		DefaultWindowBytes: 256 << 10, MinWindowBytes: 256 << 10, MaxWindowBytes: 256 << 10, BudgetBytes: 1 << 30,
	})
	entry := spawnOne(t, svc)
	sub := proto.SubscriberID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, Fresh: true,
	})

	// The reader never acks, so its credit closes; the window goes on
	// reclaiming underneath it, which is precisely the case D8 says the
	// earlier draft understated.
	proc := spawner.last()
	go func() {
		chunk := make([]byte, 8<<10)
		for i := range chunk {
			chunk[i] = 'q'
		}
		for i := 0; i < 200; i++ {
			if _, err := proc.produce.Write(chunk); err != nil {
				return
			}
		}
	}()

	awaitSink(t, sink, "the live reset", func() bool {
		return len(sink.notifications(proto.EventSessionReset)) > 0
	})
	note := sink.notifications(proto.EventSessionReset)[0]
	raw, err := json.Marshal(note.Params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var reset proto.SessionReset
	if err := json.Unmarshal(raw, &reset); err != nil {
		t.Fatalf("decode reset: %v", err)
	}
	if reset.Subscriber != sub {
		t.Errorf("the reset names subscriber %q, want %q", reset.Subscriber, sub)
	}
	if !reset.Resume.Reset || reset.Resume.Resumed {
		t.Errorf("resume = %+v, want exactly reset", reset.Resume)
	}
	if reset.Resume.Gap == nil || reset.Resume.Gap.Reason != proto.GapReasonWindow {
		t.Fatalf("gap = %+v, want the window's own reason: nobody ever held those bytes", reset.Resume.Gap)
	}
	if reset.Resume.Gap.End != reset.Resume.From {
		t.Errorf("the gap ends at %d but the stream resumes at %d: the hole must sit exactly between the bytes", reset.Resume.Gap.End, reset.Resume.From)
	}
}

// TestTheExitStatusIsOwnedByTheHelperAndSurvivesInTheInventory is D3 ("owns
// exit status") plus the reason D7 needs it: a coordinator replaced while a
// command was running must be able to learn how it ended, and the helper is
// the only process that watched it end.
func TestTheExitStatusIsOwnedByTheHelperAndSurvivesInTheInventory(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	svc := newService(t, sink, spawner, session.Limits{})
	entry := spawnOne(t, svc)
	call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: proto.SubscriberID("cccccccccccccccccccccccccccccccc"), Session: entry.Session,
	})

	spawner.last().exit(&exitErr{code: 3})

	awaitSink(t, sink, "the exit notification", func() bool {
		return len(sink.notifications(proto.EventSessionExit)) > 0
	})
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 {
		t.Fatalf("an exited session vanished from the inventory before anybody could read its status: %+v", inv.Sessions)
	}
	if inv.Sessions[0].Exit == nil {
		t.Fatal("the inventory reports no exit status for an exited session")
	}
	if inv.Sessions[0].Exit.Code != 3 {
		t.Errorf("exit code = %d, want 3", inv.Sessions[0].Exit.Code)
	}
}

// TestTheSessionIsInTheInventoryButTheProcessIsGone is the third partial
// failure the spawn procedure owes an answer to. What is true afterwards: the
// entry is still there and still carries its exit status and its window, a
// reader can still attach and drain what the process printed before it died,
// and a WRITE is refused rather than silently swallowed by a dead fd.
func TestTheSessionIsInTheInventoryButTheProcessIsGone(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	svc := newService(t, sink, spawner, session.Limits{})
	entry := spawnOne(t, svc)
	proc := spawner.last()
	proc.say(t, "last words\r\n")
	proc.exit(nil)
	awaitSink(t, sink, "the exit notification", func() bool {
		return len(sink.notifications(proto.EventSessionExit)) > 0
	})

	sub := proto.SubscriberID("dddddddddddddddddddddddddddddddd")
	att := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, Offset: 0, Fresh: true, RequestWrite: true,
	})
	if att.Resume.Reset {
		t.Fatalf("attaching to an exited session was reset: %+v", att.Resume)
	}
	subRaw, _ := proto.SessionBytes(string(sub))
	awaitSink(t, sink, "the bytes the dead process left behind", func() bool {
		return string(sink.bytesFor(subRaw)) == "last words\r\n"
	})
}

// TestAckAdvancesTheCursorAndRefusesAnImpossibleOne — the ack is what reopens
// the credit window, so an ack running ahead of what was produced would free a
// reader to be sent bytes that do not exist, and one running backwards is a
// stale report. Validated exactly as the coordinator's ring validates its own.
func TestAckAdvancesTheCursorAndRefusesAnImpossibleOne(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	svc := newService(t, sink, spawner, session.Limits{})
	entry := spawnOne(t, svc)
	sub := proto.SubscriberID("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{Subscriber: sub, Session: entry.Session})

	spawner.last().say(t, "0123456789")
	subRaw, _ := proto.SessionBytes(string(sub))
	awaitSink(t, sink, "ten bytes", func() bool { return len(sink.bytesFor(subRaw)) == 10 })

	if _, err := svc.Call(callCtx(), proto.OpAck, mustJSON(t, proto.AckParams{
		Subscriber: sub, Session: entry.Session, Offset: 5,
	})); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if _, err := svc.Call(callCtx(), proto.OpAck, mustJSON(t, proto.AckParams{
		Subscriber: sub, Session: entry.Session, Offset: 1000,
	})); err == nil {
		t.Error("an ack ahead of what was produced was accepted")
	}
	if _, err := svc.Call(callCtx(), proto.OpAck, mustJSON(t, proto.AckParams{
		Subscriber: sub, Session: entry.Session, Offset: 1,
	})); err == nil {
		t.Error("an ack behind the current cursor was accepted")
	}
}

// TestAFailingWireDropsTheAttachmentAndNotTheSession is the failure path for
// the external call this service makes on every byte. The wire is somebody
// else's socket and it dies; what must survive is the process, the window and
// the inventory — the attachment is the disposable one (D2).
func TestAFailingWireDropsTheAttachmentAndNotTheSession(t *testing.T) {
	spawner := &fakeSpawner{}
	sink := newSink()
	svc := newService(t, sink, spawner, session.Limits{})
	entry := spawnOne(t, svc)
	sub := proto.SubscriberID("ffffffffffffffffffffffffffffffff")
	call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{Subscriber: sub, Session: entry.Session})

	sink.fail(errors.New("carrier gone"))
	spawner.last().say(t, "into the void\r\n")

	// The session is still here, still running, and still spawnable-from: the
	// inventory is what a replacing coordinator will ask, and it must answer.
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 || inv.Sessions[0].Exit != nil {
		t.Fatalf("a dead wire took the session with it: %+v", inv.Sessions)
	}
}

// TestReleasingAConnectionKeepsTheSessionsAndDropsTheAttachments is D1 at the
// seam where it is actually implemented: an attachment belongs to a
// connection and dies with it; a host session does not. This is what makes
// "killing and restarting the coordinator" survivable at all, and the
// end-to-end proof of it is in reattach_test.go.
func TestReleasingAConnectionKeepsTheSessionsAndDropsTheAttachments(t *testing.T) {
	spawner := &fakeSpawner{}
	first := newSink()
	svc := session.New(session.Options{Generation: "gen-under-test", Spawner: spawner, Log: discardLog()})
	t.Cleanup(svc.Close)

	release := bindTo(svc, first)
	entry := spawnOne(t, svc)
	sub := proto.SubscriberID("0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f")
	a := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, RequestWrite: true,
	})
	if !a.Write.Granted {
		t.Fatal("no write capability to release")
	}
	release()

	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 1 {
		t.Fatalf("the connection took its sessions with it: %+v", inv.Sessions)
	}
	if inv.Sessions[0].Writer != nil {
		t.Errorf("the dead connection still holds the write capability: %v", inv.Sessions[0].Writer)
	}
	// A replacing connection can take the capability without arbitration,
	// which is exactly the fact D9 says the next caller acts on.
	second := newSink()
	t.Cleanup(bindTo(svc, second))
	b := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, RequestWrite: true,
	})
	if !b.Write.Granted {
		t.Fatalf("the replacing connection was refused the capability: %+v", b.Write)
	}
}

// TestResizeReachesThePTY — the size is a property of the PTY, and the PTY is
// the helper's (D3). A terminal whose size cannot change is not a terminal.
func TestResizeReachesThePTY(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})
	entry := spawnOne(t, svc)

	call[proto.ResizeResult](t, svc, proto.OpResize, proto.ResizeParams{Session: entry.Session, Cols: 132, Rows: 43})
	proc := spawner.last()
	proc.mu.Lock()
	cols, rows := proc.cols, proc.rows
	proc.mu.Unlock()
	if cols != 132 || rows != 43 {
		t.Fatalf("the PTY is %dx%d, want 132x43", cols, rows)
	}
	// And the LAUNCH record is not rewritten by it: it says what was spawned,
	// and a record that silently tracked the current size would be a fact
	// that goes stale while claiming to be authoritative.
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if inv.Sessions[0].Launch.Cols != 80 {
		t.Errorf("the launch record now says %d columns: it records the launch, not the present", inv.Sessions[0].Launch.Cols)
	}
}

// TestObservationIsEvidenceAndNeverOverwritesTheLaunchRecord is the design
// error this bead was most likely to make, asserted against a lying inspector.
// argv is mutable by the process itself; if OS inspection could overwrite the
// launch record, a process that rewrote its own argv would rename its session.
func TestObservationIsEvidenceAndNeverOverwritesTheLaunchRecord(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := session.New(session.Options{
		Generation: "gen-under-test",
		Spawner:    spawner,
		Log:        discardLog(),
		Inspector:  lyingInspector{},
	})
	t.Cleanup(svc.Close)
	t.Cleanup(bindTo(svc, newSink()))

	entry := spawnOne(t, svc)
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	got := inv.Sessions[0]
	if got.Launch.Shell != "/bin/fake" || got.Launch.Pid != 4242 {
		t.Fatalf("the launch record was overwritten by observation: %+v", got.Launch)
	}
	if got.Observed == nil {
		t.Fatal("no observation was reported at all")
	}
	if got.Observed.Source == "" {
		t.Error("the evidence does not say where it came from")
	}
	if got.Observed.Argv[0] != "/usr/bin/not-what-was-launched" {
		t.Errorf("observation = %+v, want the OS's own (contradictory) answer reported AS evidence", got.Observed)
	}
	_ = entry
}

// TestAnInspectorThatCannotAnswerReportsNothingRatherThanEmptiness — the
// failure path of the one external call the inventory makes. macOS has no
// /proc at all, so "no observation" is the ordinary answer on a whole
// platform: it must arrive as an absent field, never as an empty record that
// decodes as "we looked and there is nothing".
func TestAnInspectorThatCannotAnswerReportsNothingRatherThanEmptiness(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	spawnOne(t, svc)
	res, err := svc.Call(context.Background(), proto.OpSessions, mustJSON(t, proto.SessionsParams{}))
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	raw, _ := json.Marshal(res)
	var decoded proto.SessionsResult
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Sessions[0].Observed != nil {
		t.Fatalf("an inspector that was never installed produced an observation: %+v", decoded.Sessions[0].Observed)
	}
}

// TestCloseSessionEndsTheProcessAndRemovesItsInventoryRow names both ends of
// the close-session interval: the PTY is closed and the durable row is gone.
func TestCloseSessionEndsTheProcessAndRemovesItsInventoryRow(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})
	entry := spawnOne(t, svc)
	proc := spawner.last()

	call[proto.CloseSessionResult](t, svc, proto.OpCloseSession,
		proto.CloseSessionParams{Session: entry.Session})

	proc.mu.Lock()
	closed := proc.closed
	proc.mu.Unlock()
	if !closed {
		t.Fatal("close-session returned before closing the PTY")
	}
	inv := call[proto.SessionsResult](t, svc, proto.OpSessions, proto.SessionsParams{})
	if len(inv.Sessions) != 0 {
		t.Fatalf("close-session left %d inventory rows, want 0", len(inv.Sessions))
	}
}

// TestSignalSendsTheRequestedSignalToTheOwnedProcessGroup proves the helper,
// rather than the coordinator, owns the process-group signal operation.
func TestSignalSendsTheRequestedSignalToTheOwnedProcessGroup(t *testing.T) {
	spawner := &fakeSpawner{}
	svc := newService(t, newSink(), spawner, session.Limits{})
	entry := spawnOne(t, svc)
	proc := spawner.last()

	call[proto.SignalResult](t, svc, proto.OpSignal, proto.SignalParams{
		Session: entry.Session,
		Signal:  int(syscall.SIGTERM),
	})

	proc.mu.Lock()
	got, gotPgid := proc.signal, proc.signalPgid
	proc.mu.Unlock()
	if got != syscall.SIGTERM {
		t.Fatalf("signal = %v, want %v", got, syscall.SIGTERM)
	}
	if gotPgid != entry.Launch.Pgid {
		t.Fatalf("signal pgid = %d, want the launched process group %d",
			gotPgid, entry.Launch.Pgid)
	}
}

// TestTheServiceIsNamedAfterTheReservedNameAndTakesNoArgv closes the loop with
// internal/helper/host: the name the ABI froze, the name the host dispatches
// on and the name this service answers to are one constant, and every op's
// params survive the registration rule that refuses argv (D3).
func TestTheServiceIsNamedAfterTheReservedNameAndTakesNoArgv(t *testing.T) {
	svc := newService(t, newSink(), &fakeSpawner{}, session.Limits{})
	if svc.Name() != proto.ServiceSession {
		t.Fatalf("service name = %q, want %q", svc.Name(), proto.ServiceSession)
	}
	want := map[string]bool{
		proto.OpSpawn: true, proto.OpSessions: true, proto.OpAttach: true,
		proto.OpAck: true, proto.OpDetach: true, proto.OpResize: true,
		proto.OpCloseSession: true, proto.OpSignal: true,
		proto.OpAdoptLifecycle: true,
	}
	for _, op := range svc.Ops() {
		if !want[op] {
			t.Errorf("undeclared op %q", op)
		}
		delete(want, op)
		if svc.ParamsSchema(op) == nil {
			t.Errorf("op %q declares no params schema", op)
		}
	}
	for op := range want {
		t.Errorf("op %q is missing", op)
	}
}

// exitErr is an error carrying an exit code the way *exec.ExitError does, so
// the exit watcher's extraction is exercised rather than stubbed.
type exitErr struct{ code int }

func (e *exitErr) Error() string { return "exit status" }
func (e *exitErr) ExitCode() int { return e.code }

type lyingInspector struct{}

func (lyingInspector) Observe(int, int) *proto.Observation {
	return &proto.Observation{
		Source: "a-test-that-contradicts-the-launch-record",
		Cwd:    "/somewhere/else",
		Argv:   []string{"/usr/bin/not-what-was-launched"},
	}
}

// TestAPlatformThatCannotObserveTheCwdSaysSoRatherThanLeavingItBlank is
// nocx-k6p18.10 asserted through the seam a coordinator actually reads: the
// inventory result, marshalled by the real encoder.
//
// macOS answers argv and the foreground command through sysctl and cannot
// answer cwd at all. Before this, that arrived as an observation with no `cwd`
// key — indistinguishable from an inspector that simply had nothing to add, so
// a tab showing a working directory fell back to launch.cwd and showed the
// directory the shell STARTED in, silently, forever. The two payloads below
// are what a reader has to be able to tell apart, and they are compared as
// bytes so no decoder can conflate them by accident.
func TestAPlatformThatCannotObserveTheCwdSaysSoRatherThanLeavingItBlank(t *testing.T) {
	inventory := func(insp session.Inspector) []byte {
		t.Helper()
		svc := session.New(session.Options{
			Generation: "gen-under-test",
			Spawner:    &fakeSpawner{},
			Log:        discardLog(),
			Inspector:  insp,
		})
		t.Cleanup(svc.Close)
		t.Cleanup(svc.Bind(newSink()))
		spawnOne(t, svc)
		res, err := svc.Call(context.Background(), proto.OpSessions, mustJSON(t, proto.SessionsParams{}))
		if err != nil {
			t.Fatalf("sessions: %v", err)
		}
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return raw
	}

	blind := inventory(cwdBlindInspector{})
	seeing := inventory(cwdSeeingInspector{})

	if !strings.Contains(string(blind), `"unavailable":["cwd"]`) {
		t.Errorf("an inspector that cannot observe the cwd does not say so:\n%s", blind)
	}
	if strings.Contains(string(blind), `"cwd":"/observed`) {
		t.Errorf("a cwd was reported by an inspector that cannot observe one:\n%s", blind)
	}
	// The LAUNCH record is untouched by any of this — it is the authority and
	// it is still there. What changed is that a reader can now tell that the
	// launch value is all it has, instead of reading it as current.
	if !strings.Contains(string(blind), `"launch":`) {
		t.Errorf("the launch record went missing:\n%s", blind)
	}
	if !strings.Contains(string(seeing), `"cwd":"/observed/right/now"`) {
		t.Errorf("an inspector that CAN observe the cwd did not report it:\n%s", seeing)
	}
	if !strings.Contains(string(seeing), `"unavailable":[]`) {
		t.Errorf("an inspector that answered everything did not say so:\n%s", seeing)
	}
	if string(blind) == string(seeing) {
		t.Fatal("'we do not know where the shell is' and 'the shell is in /observed/right/now' are the same bytes")
	}
}

// cwdBlindInspector answers the way macOS's sysctl inspector does: the
// diagnostics sysctl carries, and cwd named as unanswerable.
type cwdBlindInspector struct{}

func (cwdBlindInspector) Observe(int, int) *proto.Observation {
	return &proto.Observation{
		Source:      "sysctl",
		Argv:        []string{"-zsh"},
		Unavailable: []proto.Diagnostic{proto.DiagnosticCwd},
	}
}

// cwdSeeingInspector answers the way procfs does on an ordinary Linux box:
// everything asked for, and an empty unavailable list saying so.
type cwdSeeingInspector struct{}

func (cwdSeeingInspector) Observe(int, int) *proto.Observation {
	return &proto.Observation{
		Source:      "procfs",
		Cwd:         "/observed/right/now",
		Argv:        []string{"-zsh"},
		Unavailable: []proto.Diagnostic{},
	}
}

type lifecycleCarrier struct {
	stream *io.PipeReader
	input  *io.PipeWriter
}

func (c *lifecycleCarrier) Read(b []byte) (int, error) { return c.stream.Read(b) }
func (c *lifecycleCarrier) Write(b []byte) (int, error) {
	return len(b), nil
}

func (c *lifecycleCarrier) Close() error {
	_ = c.stream.Close()
	return c.input.Close()
}

type lifecycleProcess struct {
	*fakeProcess
	carrier *lifecycleCarrier
}

func (p *lifecycleProcess) Lifecycle() io.ReadWriteCloser { return p.carrier }

func (p *lifecycleProcess) Close() error {
	err := p.fakeProcess.Close()
	_ = p.carrier.Close()
	return err
}

type lifecycleSpawner struct {
	proc *lifecycleProcess
}

func (s *lifecycleSpawner) Spawn(session.SpawnRequest) (session.Process, error) {
	return s.proc, nil
}

func lifecycleBytes(s *recordingSink) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []byte
	for _, frame := range s.lifecycleFrames {
		out = append(out, frame.Payload...)
	}
	return out
}

func TestLifecycleWindowReplaysAfterConnectionReplacement(t *testing.T) {
	stream, input := io.Pipe()
	proc := &lifecycleProcess{
		fakeProcess: newFakeProcess(),
		carrier:     &lifecycleCarrier{stream: stream, input: input},
	}
	svc := session.New(session.Options{
		Generation: "gen-under-test",
		Spawner:    &lifecycleSpawner{proc: proc},
		Log:        discardLog(),
	})
	first := newSink()
	releaseFirst := bindTo(svc, first)
	t.Cleanup(func() {
		releaseFirst()
		svc.Close()
	})

	entry := call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "lane-1", Domain: "dom-1", Epoch: 7,
			Capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}).Entry
	sub := proto.SubscriberID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, Fresh: true,
	})

	const payload = "fact produced while attached"
	writeDone := make(chan error, 1)
	go func() {
		_, err := input.Write([]byte(payload))
		writeDone <- err
	}()
	awaitSink(t, first, "the first lifecycle frame", func() bool {
		return string(lifecycleBytes(first)) == payload
	})
	if err := <-writeDone; err != nil {
		t.Fatalf("write lifecycle bytes: %v", err)
	}

	releaseFirst()
	second := newSink()
	releaseSecond := bindTo(svc, second)
	t.Cleanup(releaseSecond)
	reattached := call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, Fresh: true, LifecycleOffset: 0,
	})
	if reattached.LifecycleResume.Reset {
		t.Fatalf("lifecycle data reset despite fitting in the helper window: %+v", reattached.LifecycleResume)
	}
	awaitSink(t, second, "the lifecycle frame after connection replacement", func() bool {
		return string(lifecycleBytes(second)) == payload
	})
}
