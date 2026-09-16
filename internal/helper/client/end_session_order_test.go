package client_test

// nocx-xn63t.6.4: nocx-xn63t.6.3 fixed internal/app's hostHelper closing its
// shared client the instant a git-binding-free session's git.open touched
// it. That fix let helperRegistry.SessionEnded close the SAME shared client
// the instant a session ends — and AttachedSession.EndSession (this
// package) used to mark the attachment finished BEFORE it had even sent the
// far helper the close-session request that removes the session on ITS
// side: a.finish() ran first, close(a.done) unblocked SessionEnded's own
// watcher, and only THEN did the very next line call CloseSession. A
// coordinator that reacted to that Done() by closing the connection first —
// which SessionEnded now legitimately does when no git binding holds it
// open — could close the transport before the close-session request ever
// reached the wire. The far helper then never heard it, and kept listing
// the session until its own unclaimed-session TTL swept it — minutes past
// e2e/remote-coordinator-reclaim.spec.ts's 60s bound.
//
// This file proves the ordering directly and deterministically: the far
// helper's close-session handler is held open until this test releases it,
// and the assertion is that AttachedSession.Done() has NOT fired while that
// handler is still holding the request — i.e. the round trip this
// coordinator owes the far helper completes BEFORE anything downstream can
// see this session as over. The second half proves the actual product
// requirement the bead names: queried on the FAR SIDE, over a FRESH
// connection, the session is gone once EndSession returns.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// delayedCloseSession wraps a real session service and holds every
// close-session request open until release fires — every other op passes
// straight through to the real service, unchanged. Ops(), ParamsSchema and
// every optional interface (DataPlane, LifecycleDataPlane, ResponseObserver)
// the real service implements are promoted by embedding, so this is
// registered and bound exactly like the service it wraps.
type delayedCloseSession struct {
	*session.Service
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *delayedCloseSession) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	if op == proto.OpCloseSession {
		d.once.Do(func() { close(d.reached) })
		<-d.release
	}
	return d.Service.Call(ctx, op, params)
}

// hostedSessionDelayingClose builds a helper peer, shared across every
// connection dialed against it (cmd/nocx-helper's own shape: one session
// service, many connections), whose close-session op is held open by wrap.
func hostedSessionDelayingClose(t *testing.T, wrap *delayedCloseSession) func(in io.Reader, out io.Writer) int {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return func(in io.Reader, out io.Writer) int {
		h := hostFor(in, out, log)
		h.Register(wrap)
		release := wrap.Bind(h)
		defer release()
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

// dialFreshInventoryClient opens a SEPARATE connection to the same shared
// daemon, independent of whatever connection this test's own attachment
// rode — the far-side observation the bead demands, never inferred from the
// primary client's own state.
func dialFreshInventoryClient(t *testing.T, peer func(in io.Reader, out io.Writer) int) *client.Client {
	t.Helper()
	conn := newFakeConn(peer)
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dialing a fresh inventory connection: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestEndSessionTellsTheFarHelperBeforeTheAttachmentReportsDone(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := session.New(session.Options{
		Generation: "testhash",
		Spawner:    session.NewLocalSpawner(log, session.Shell{Path: "/bin/sh"}, ""),
		Inspector:  session.NewInspector(),
		Log:        log,
		Limits:     session.DefaultLimits(),
	})
	t.Cleanup(svc.Close)
	wrap := &delayedCloseSession{Service: svc, reached: make(chan struct{}), release: make(chan struct{})}
	peer := hostedSessionDelayingClose(t, wrap)

	c, err := client.Dial(context.Background(), client.Config{
		Exec: newFakeConn(peer), Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	var spawned proto.SpawnResult
	if spawnErr := c.Call(context.Background(), proto.ServiceSession, proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24}, &spawned); spawnErr != nil {
		t.Fatalf("spawn: %v", spawnErr)
	}
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: proto.SubscriberID("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		Session:    spawned.Entry.Session, Fresh: true, RequestWrite: true,
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = attached.Close() })

	endSessionDone := make(chan error, 1)
	go func() { endSessionDone <- attached.EndSession(context.Background()) }()

	select {
	case <-wrap.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the far helper never received the close-session request")
	}

	// THE ASSERTION: the far helper is holding the close-session request
	// right now (wrap.release has not been closed), and the attachment must
	// not yet report itself done — a watcher reacting to Done() here, the
	// way helperRegistry.SessionEnded does, must not be able to close the
	// connection before this round trip has even been answered.
	select {
	case <-attached.Done():
		t.Fatal("the attachment reported Done() while the close-session request was still in flight to the far helper — a watcher on Done() could close the connection before the far helper ever heard it")
	default:
	}

	close(wrap.release)

	select {
	case endErr := <-endSessionDone:
		if endErr != nil {
			t.Fatalf("EndSession: %v", endErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EndSession did not return after the far helper's close-session handler was released")
	}

	select {
	case <-attached.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the attachment never reported Done() after EndSession returned")
	}

	// THE PRODUCT REQUIREMENT ITSELF, checked on the far side over a FRESH
	// connection: the session this coordinator just ended is gone from the
	// far helper's own inventory, not merely absent from this client's.
	inv := dialFreshInventoryClient(t, peer)
	entries, err := inv.Sessions(context.Background())
	if err != nil {
		t.Fatalf("reading the far helper's inventory: %v", err)
	}
	for _, e := range entries {
		if e.HostSessionID.Session == spawned.Entry.Session.Session {
			t.Fatalf("the far helper still lists a session this coordinator already ended: %+v", e)
		}
	}
}
