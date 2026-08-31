package session_test

// TWO CONNECTIONS AT ONCE, which is what the endpoint made possible and what
// stdin/stdout never could.
//
// D12 is same-UID trust: any nocx running under that Unix account may connect
// to the helper, and the accept loop in internal/helper/endpoint serves them
// all. Everything below is a defect that existed while the service held ONE
// connection and could not have been seen before there were two — each was
// found by the socket, not by reading the code.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// twoConnections builds a service with two connections bound at once and
// returns it with both. Nothing here says which is "current": there is no such
// thing once a helper accepts more than one.
func twoConnections(t *testing.T, spawner session.Spawner) (*session.Service, *recordingSink, *recordingSink) {
	t.Helper()
	svc := session.New(session.Options{Generation: "gen-under-test", Spawner: spawner, Log: discardLog()})
	t.Cleanup(svc.Close)
	first, second := newSink(), newSink()
	t.Cleanup(svc.Bind(first))
	t.Cleanup(svc.Bind(second))
	return svc, first, second
}

// on drives one request down one connection, exactly as the host does: the
// connection is a property of the REQUEST, because the service has several.
func on(t *testing.T, svc *session.Service, sink session.Sink, op string, params any) json.RawMessage {
	t.Helper()
	res, err := svc.Call(host.WithConnection(context.Background(), sink), op, mustJSON(t, params))
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return mustJSON(t, res)
}

func decodeInto[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestASubscribersFramesGoToTheConnectionItAttachedOn(t *testing.T) {
	// The defect: with "the newest connection is the sink", a reader attached
	// by the FIRST coordinator had its bytes written down the SECOND
	// coordinator's socket — a session's output delivered to somebody who
	// never asked for it, and never delivered to the reader that did.
	spawner := &fakeSpawner{}
	svc, first, second := twoConnections(t, spawner)
	entry := decodeInto[proto.SpawnResult](t, on(t, svc, first, proto.OpSpawn,
		proto.SpawnParams{Cols: 80, Rows: 24})).Entry

	sub := proto.SubscriberID("0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a0a")
	on(t, svc, first, proto.OpAttach, proto.AttachParams{Subscriber: sub, Session: entry.Session})

	spawner.last().say(t, "0123456789")
	subRaw, _ := proto.SessionBytes(string(sub))
	awaitSink(t, first, "the bytes on the connection that attached", func() bool {
		return len(first.bytesFor(subRaw)) == 10
	})
	if got := second.bytesFor(subRaw); len(got) != 0 {
		t.Fatalf("the other connection received %q: a subscriber's frames went to a connection that never attached it", got)
	}
}

func TestADeadConnectionReleasesItsWriteCapabilityEvenAfterAnotherHasBound(t *testing.T) {
	// The defect, and the one that hung a test rather than failing it: the
	// release checked whether it still held the single sink slot and did
	// NOTHING when a newer connection had taken it. So a coordinator that died
	// after its replacement connected kept the write capability forever, and
	// no later coordinator could ever be granted it — the exact sequence a
	// replaced coordinator produces.
	spawner := &fakeSpawner{}
	svc := session.New(session.Options{Generation: "gen-under-test", Spawner: spawner, Log: discardLog()})
	t.Cleanup(svc.Close)

	first := newSink()
	releaseFirst := svc.Bind(first)
	entry := decodeInto[proto.SpawnResult](t, on(t, svc, first, proto.OpSpawn,
		proto.SpawnParams{Cols: 80, Rows: 24})).Entry
	held := decodeInto[proto.AttachResult](t, on(t, svc, first, proto.OpAttach, proto.AttachParams{
		Subscriber: proto.SubscriberID("1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b1b"),
		Session:    entry.Session, RequestWrite: true,
	}))
	if !held.Write.Granted {
		t.Fatal("no write capability to release")
	}

	// The replacement connects FIRST and the dead one is released after — the
	// ordering an accept loop produces, because a handler releases as it
	// returns and that is concurrent with the next accept.
	second := newSink()
	t.Cleanup(svc.Bind(second))
	releaseFirst()

	inv := decodeInto[proto.SessionsResult](t, on(t, svc, second, proto.OpSessions, proto.SessionsParams{}))
	if len(inv.Sessions) != 1 {
		t.Fatalf("the connection took its sessions with it: %+v", inv.Sessions)
	}
	if inv.Sessions[0].Writer != nil {
		t.Fatalf("the dead connection still holds the write capability: %v", inv.Sessions[0].Writer)
	}
	taken := decodeInto[proto.AttachResult](t, on(t, svc, second, proto.OpAttach, proto.AttachParams{
		Subscriber: proto.SubscriberID("2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c2c"),
		Session:    entry.Session, RequestWrite: true,
	}))
	if !taken.Write.Granted {
		t.Fatalf("the replacing connection was refused the capability: %+v", taken.Write)
	}
}

func TestOneConnectionEndingLeavesAnotherConnectionsAttachmentAlone(t *testing.T) {
	// The mirror image, and the reason the release is filtered by connection
	// rather than emptying the table: a coordinator going away must not take
	// another coordinator's reader with it.
	spawner := &fakeSpawner{}
	svc := session.New(session.Options{Generation: "gen-under-test", Spawner: spawner, Log: discardLog()})
	t.Cleanup(svc.Close)

	first, second := newSink(), newSink()
	releaseFirst := svc.Bind(first)
	t.Cleanup(svc.Bind(second))

	entry := decodeInto[proto.SpawnResult](t, on(t, svc, first, proto.OpSpawn,
		proto.SpawnParams{Cols: 80, Rows: 24})).Entry
	watcher := proto.SubscriberID("3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d3d")
	on(t, svc, first, proto.OpAttach, proto.AttachParams{
		Subscriber: proto.SubscriberID("4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e4e"), Session: entry.Session,
	})
	on(t, svc, second, proto.OpAttach, proto.AttachParams{Subscriber: watcher, Session: entry.Session})

	releaseFirst()

	// The surviving connection's reader is still reading: what the process
	// prints after the other coordinator died still reaches it.
	spawner.last().say(t, "0123456789")
	watcherRaw, _ := proto.SessionBytes(string(watcher))
	awaitSink(t, second, "the surviving connection's bytes", func() bool {
		return len(second.bytesFor(watcherRaw)) == 10
	})
}

func TestAnExitReachesEveryConnectionWatchingIt(t *testing.T) {
	// An exit is a fact about a SESSION, not about one attachment. With two
	// coordinators watching, telling only one of them would leave the other
	// showing a command that has finished as though it were still running.
	spawner := &fakeSpawner{}
	svc, first, second := twoConnections(t, spawner)
	on(t, svc, first, proto.OpSpawn, proto.SpawnParams{Cols: 80, Rows: 24})

	spawner.last().exit(nil)

	awaitSink(t, first, "the exit on the first connection", func() bool {
		return len(first.notifications(proto.EventSessionExit)) == 1
	})
	awaitSink(t, second, "the exit on the second connection", func() bool {
		return len(second.notifications(proto.EventSessionExit)) == 1
	})
}
