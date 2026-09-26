package session_test

// Two readers ack one subscriber (nocx-2v80t.3.44): the PTY reader acks the
// PTY cursor, and the lifecycle reader acks the lifecycle cursor — carrying
// the PTY offset it read beside it, because the wire's ack has always required
// one. The two calls race, so the lifecycle reader's ack can arrive after the
// PTY reader's newer one, with a PTY offset the cursor has already passed. It
// was refused whole, and its lifecycle offset — the only thing it was sent
// for — was lost with it.

import (
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/session"
)

// ackStand is one attached subscriber whose session has produced ten PTY
// bytes and a lifecycle payload, so both cursors have somewhere to go.
func ackStand(t *testing.T) (*session.Service, proto.HostSessionID, proto.SubscriberID, proto.StreamOffset) {
	t.Helper()
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
	sink := newSink()
	release := bindTo(svc, sink)
	t.Cleanup(func() {
		release()
		svc.Close()
	})
	entry := call[proto.SpawnResult](t, svc, proto.OpSpawn, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{
			Lane: "lane-1", Domain: "dom-1", Epoch: 7,
			Capability: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
	}).Entry
	sub := proto.SubscriberID("abababababababababababababababab")
	call[proto.AttachResult](t, svc, proto.OpAttach, proto.AttachParams{
		Subscriber: sub, Session: entry.Session, Fresh: true,
	})

	proc.say(t, "0123456789")
	subRaw, _ := proto.SessionBytes(string(sub))
	awaitSink(t, sink, "ten PTY bytes", func() bool { return len(sink.bytesFor(subRaw)) == 10 })

	const payload = "a lifecycle fact"
	go func() { _, _ = input.Write([]byte(payload)) }()
	awaitSink(t, sink, "the lifecycle payload", func() bool { return string(lifecycleBytes(sink)) == payload })
	return svc, entry.Session, sub, proto.StreamOffset(len(payload))
}

func ack(t *testing.T, svc *session.Service, id proto.HostSessionID, sub proto.SubscriberID, pty proto.StreamOffset, lifecycle *proto.StreamOffset) error {
	t.Helper()
	_, err := svc.Call(callCtx(), proto.OpAck, mustJSON(t, proto.AckParams{
		Subscriber: sub, Session: id, Offset: pty, LifecycleOffset: lifecycle,
	}))
	return err
}

// The race's losing order: the PTY reader's newer ack lands first, then the
// lifecycle reader's, carrying a PTY offset behind it. The lifecycle half is
// applied, nothing is refused, and the PTY cursor is not moved back.
func TestALifecycleAckBehindThePTYReadersAckStillAdvancesTheLifecycleCursor(t *testing.T) {
	svc, id, sub, lifecycleEnd := ackStand(t)

	if err := ack(t, svc, id, sub, 10, nil); err != nil {
		t.Fatalf("the PTY reader's ack: %v", err)
	}
	if err := ack(t, svc, id, sub, 4, &lifecycleEnd); err != nil {
		t.Fatalf("the lifecycle reader's ack was refused: %v", err)
	}

	pty, lifecycle, ok := svc.TestAckedCursors(id, sub)
	if !ok {
		t.Fatal("the subscriber is gone")
	}
	if lifecycle != lifecycleEnd {
		t.Fatalf("lifecycle cursor = %d, want %d", lifecycle, lifecycleEnd)
	}
	if pty != 10 {
		t.Fatalf("PTY cursor = %d, want 10: the lifecycle reader's ack must not move it", pty)
	}
}

// Paired: the ordinary order — the lifecycle reader's ack first, the PTY
// reader's after — advances both cursors, each by its own reader.
func TestAckedInTheOrdinaryOrderBothCursorsAdvance(t *testing.T) {
	svc, id, sub, lifecycleEnd := ackStand(t)

	if err := ack(t, svc, id, sub, 4, &lifecycleEnd); err != nil {
		t.Fatalf("the lifecycle reader's ack: %v", err)
	}
	if err := ack(t, svc, id, sub, 10, nil); err != nil {
		t.Fatalf("the PTY reader's ack: %v", err)
	}

	pty, lifecycle, _ := svc.TestAckedCursors(id, sub)
	if lifecycle != lifecycleEnd || pty != 10 {
		t.Fatalf("cursors = pty %d, lifecycle %d; want 10 and %d", pty, lifecycle, lifecycleEnd)
	}
}

// And a PTY-only ack behind the cursor is still what it always was: a stale
// report, refused.
func TestAPTYAckBehindTheCursorIsStillRefused(t *testing.T) {
	svc, id, sub, _ := ackStand(t)

	if err := ack(t, svc, id, sub, 10, nil); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := ack(t, svc, id, sub, 4, nil); err == nil {
		t.Fatal("a PTY ack behind the cursor was accepted")
	}
}
