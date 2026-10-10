package app

// THE RE-ADOPT'S LIFECYCLE RESUME POINT (ADR-0077, the owner's decision of
// 2026-09-30). The coordinator that went away stored, with the session's
// binding, the offset one past the last lifecycle frame it applied; the one
// that takes the session back asks the helper for the stream from exactly
// there. Not from the window's base — those frames were applied once and carry
// a capability the adopted domain still honours (ADR-0024) — and not from its
// head, which skips every frame the shell spoke while nobody was attached
// (ADR-0076 decision 4). Only a binding that stored no cursor at all resumes
// at the head, because nothing better is known.

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	helpersession "github.com/shady2k/nocx/internal/helper/session"
	"github.com/shady2k/nocx/internal/session"
)

// lifecycleIdleProcess is the scripted idle process with a lifecycle carrier:
// the shell's side of the authenticated channel as a test double. Frames the
// test writes are the shell speaking; frames the helper writes to it (its own
// demands) are discarded — no shell answers them here.
type lifecycleIdleProcess struct {
	idleProcess
	speak   *io.PipeWriter
	carrier *testLifecycleCarrier
}

func (p *lifecycleIdleProcess) Lifecycle() io.ReadWriteCloser { return p.carrier }

type testLifecycleCarrier struct {
	in *io.PipeReader // the helper reads the shell's frames here
}

func (c *testLifecycleCarrier) Read(p []byte) (int, error)  { return c.in.Read(p) }
func (c *testLifecycleCarrier) Write(p []byte) (int, error) { return len(p), nil }
func (c *testLifecycleCarrier) Close() error                { return c.in.Close() }

// lifecycleWindowSpawner is the spawner whose every process carries the
// lifecycle carrier, so the helper builds a retained window for its sessions.
type lifecycleWindowSpawner struct {
	mu    sync.Mutex
	procs []*lifecycleIdleProcess
	next  int
}

func (s *lifecycleWindowSpawner) Spawn(req helpersession.SpawnRequest) (helpersession.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	r, w := io.Pipe()
	p := &lifecycleIdleProcess{
		idleProcess: idleProcess{done: make(chan struct{}), pid: 5000 + s.next, id: req.SessionID},
		speak:       w,
		carrier:     &testLifecycleCarrier{in: r},
	}
	s.procs = append(s.procs, p)
	return p, nil
}

// theProcess answers the one process this test's session has.
func (s *lifecycleWindowSpawner) theProcess(t *testing.T) *lifecycleIdleProcess {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.procs) != 1 {
		t.Fatalf("%d processes were spawned, want exactly 1", len(s.procs))
	}
	return s.procs[0]
}

// recordingLocalRoute is the local route as a recording delegate: the real
// client answers, and the attach params the coordinator meant to send are
// captured on the way through.
type recordingLocalRoute struct {
	*client.Client
	mu     sync.Mutex
	attach []proto.AttachParams
}

func (r *recordingLocalRoute) Attach(ctx context.Context, params proto.AttachParams, opts ...client.AttachOption) (*client.AttachedSession, error) {
	r.mu.Lock()
	r.attach = append(r.attach, params)
	r.mu.Unlock()
	return r.Client.Attach(ctx, params, opts...)
}

func (r *recordingLocalRoute) LocalSessions(ctx context.Context, _ string) ([]client.SessionEntry, error) {
	return r.Client.Sessions(ctx)
}

func (r *recordingLocalRoute) Release(string)        {}
func (r *recordingLocalRoute) noteHeld(session.ID)   {}
func (r *recordingLocalRoute) forgetHeld(session.ID) {}

func (r *recordingLocalRoute) lastAttach() (proto.AttachParams, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attach) == 0 {
		return proto.AttachParams{}, false
	}
	return r.attach[len(r.attach)-1], true
}

// readoptLifecycleFixture spawns one lifecycle-carrying session through a
// first coordinator, has its shell speak `spoken` while nobody is attached,
// waits until the helper's window has retained it, and answers the binding and
// a recording route over the same daemon.
func readoptLifecycleFixture(t *testing.T, spoken string) (content.PendingSession, *recordingLocalRoute, *fakeLaneProvider) {
	t.Helper()
	spawner := &lifecycleWindowSpawner{}
	svc := helpersession.New(helpersession.Options{
		Generation: proto.GenerationID(syntheticArtifactHash),
		Spawner:    spawner,
		Log:        discardLogger(t),
	})
	provider := &fakeLaneProvider{peer: sharedHelperPeer(t, svc)}
	first := newCoordinator(t, provider)
	binding := openHostedFixture(t, first, "pane-lifecycle-window")
	first.quit()
	// A real local open leaves Host, Account and HelperCommand empty — the
	// binding this test hands the local route keeps that shape (the routes
	// fixture's binding names a host, and this readopt is the local one).
	binding.Host, binding.Account, binding.HelperCommand = "", "", ""
	proc := spawner.theProcess(t)
	t.Cleanup(func() { _ = proc.speak.Close() })

	// A client of this test's own, over the same service, so the route can
	// answer the real daemon while recording what the readopt asks of it.
	route := &recordingLocalRoute{}
	{
		conn := newFakeLaneConn(sharedHelperPeer(t, svc))
		t.Cleanup(func() { _ = conn.Close() })
		c, err := client.Dial(context.Background(), client.Config{
			Exec:       conn,
			Command:    "/scripted/helper",
			ExpectHash: syntheticArtifactHash,
			Log:        discardLogger(t),
		})
		if err != nil {
			t.Fatalf("the test's own helper client: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		route.Client = c
	}

	// THE FRAMES SPOKEN WHILE NOBODY WAS ATTACHED. The window has provably
	// retained them before anything asks for the session back.
	if _, err := proc.speak.Write([]byte(spoken)); err != nil {
		t.Fatalf("the shell spoke: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	retained := false
	for !retained {
		entries, err := route.Client.Sessions(ctx)
		if err != nil {
			t.Fatalf("ask the helper what it holds: %v", err)
		}
		for _, e := range entries {
			if e.HostSessionID.Session == binding.SessionID &&
				e.LifecycleWindow.Written >= uint64(len(spoken)) {
				retained = true
			}
		}
		if !retained {
			if err := ctx.Err(); err != nil {
				t.Fatalf("the helper's window never retained the shell's frames: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return binding, route, provider
}

// readoptAndReadTheAttach takes the binding back over the recording route and
// answers the attach params the readopt sent.
func readoptAndReadTheAttach(t *testing.T, binding content.PendingSession, route *recordingLocalRoute, provider *fakeLaneProvider) proto.AttachParams {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	second := newCoordinator(t, provider)
	adopter := &stubAdopter{}
	t.Cleanup(adopter.endAdopted)
	pass := &readoptPass{registry: second.reg, adopter: adopter, local: route}
	if _, err := pass.readoptLocal(ctx, binding); err != nil {
		t.Fatalf("readopt: %v", err)
	}
	attach, ok := route.lastAttach()
	if !ok {
		t.Fatal("the readopt never attached")
	}
	return attach
}

func TestAReadoptResumesTheLifecycleStreamAtTheCursorTheLastCoordinatorStored(t *testing.T) {
	// Two stretches: the first the previous coordinator applied and stored
	// its cursor for, the second the shell spoke after that coordinator was
	// gone.
	const applied = `{"kind":"start"}`
	const unread = `{"kind":"complete","exit":0}`
	binding, route, provider := readoptLifecycleFixture(t, applied+unread)
	cursor := uint64(len(applied))
	binding.LifecycleApplied = &cursor

	attach := readoptAndReadTheAttach(t, binding, route, provider)
	if attach.LifecycleOffset != proto.StreamOffset(cursor) {
		t.Fatalf("the re-adopt's attach resumed the lifecycle stream at offset %d, want %d — the cursor the previous coordinator stored: "+
			"0 re-delivers what it applied, %d skips what the shell said while nobody was attached",
			attach.LifecycleOffset, cursor, len(applied)+len(unread))
	}
	if attach.LifecycleFresh {
		t.Fatal("the re-adopt called itself fresh while resuming from a cursor it holds")
	}
}

func TestAReadoptWithNoStoredCursorResumesTheLifecycleStreamAtItsHead(t *testing.T) {
	const spoken = `{"kind":"complete","exit":0}`
	binding, route, provider := readoptLifecycleFixture(t, spoken)
	binding.LifecycleApplied = nil

	attach := readoptAndReadTheAttach(t, binding, route, provider)
	if attach.LifecycleOffset != proto.StreamOffset(len(spoken)) {
		t.Fatalf("with no stored cursor the re-adopt resumed at offset %d, want the head (%d): a coordinator that holds no record of what was applied may not offer any of it again",
			attach.LifecycleOffset, len(spoken))
	}
}
