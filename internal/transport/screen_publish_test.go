package transport

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/transport/outbound"
)

// The screen plane's publish seam: one full session.frame document, put on
// the data plane for the session's CURRENT subscriber on the reserved
// metadata msg-type. What is tested here is the seam's contract — reaches
// the attached subscriber whole, refuses nothing silently when nobody is
// attached — because the full path's acceptance is the over-the-wire test
// step 8 owes.

// captureSocket accepts every write and keeps it, so the test can decode
// what the publish seam actually put on the wire. cond broadcasts on every
// write, so a waiter blocks on the frame count actually changing rather than
// polling it on a timer (AGENTS.md: "a test may not depend on timing").
type captureSocket struct {
	mu     sync.Mutex
	cond   *sync.Cond
	frames []outbound.Frame
}

func newCaptureSocket() *captureSocket {
	s := &captureSocket{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *captureSocket) ReadMessage() (int, []byte, error) { return 0, nil, fmt.Errorf("no reads") }
func (s *captureSocket) SetWriteDeadline(time.Time) error  { return nil }
func (s *captureSocket) Close() error                      { return nil }

func (s *captureSocket) WriteMessage(msgType int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.cond.Broadcast()
	s.frames = append(s.frames, outbound.Frame{MsgType: msgType, Data: append([]byte(nil), data...)})
	return nil
}

func newScreenPublishFixture(t *testing.T) (*WSServer, session.ID, [16]byte, *wsConn, *captureSocket) {
	t.Helper()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	ctx := t.Context()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(context.Background()) })

	sid := session.ID("0123456789abcdef0123456789abcdef")
	sidBytes, err := session.IDToBytes(sid)
	if err != nil {
		t.Fatalf("id to bytes: %v", err)
	}
	sock := newCaptureSocket()
	rx := ws.getOrCreateRx(sid)
	if rx == nil {
		t.Fatal("the server would not make a ring")
	}
	wconn := &wsConn{out: outbound.New(sock, outbound.Config{}), log: logger, id: 1}
	t.Cleanup(wconn.out.Close)
	rx.setSubscriber(wconn, nil)
	return ws, sid, sidBytes, wconn, sock
}

// awaitCapturedFrames waits on the SOCKET'S OWN frame count actually
// reaching n — cond.Wait blocks until sock.WriteMessage broadcasts, so there
// is nothing here to poll on a timer (stage review nocx-2v80t.3.15, finding
// 12). The deadline is a safety net for a run that never arrives, not the
// wait itself: an AfterFunc wakes the waiter once it passes, the same way an
// absolute read deadline ends a blocked socket read elsewhere in this
// package (ws_inbox_test.go's awaitFrame).
func awaitCapturedFrames(t *testing.T, sock *captureSocket, n int) []outbound.Frame {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	timer := time.AfterFunc(time.Until(deadline), func() {
		sock.mu.Lock()
		sock.cond.Broadcast()
		sock.mu.Unlock()
	})
	defer timer.Stop()

	sock.mu.Lock()
	defer sock.mu.Unlock()
	for len(sock.frames) < n {
		if !time.Now().Before(deadline) {
			t.Fatalf("only %d of %d frames reached the subscriber's socket", len(sock.frames), n)
		}
		sock.cond.Wait()
	}
	return append([]outbound.Frame(nil), sock.frames...)
}

func TestPublishScreenFrame_ReachesTheAttachedSubscriber(t *testing.T) {
	ws, sid, sidBytes, _, sock := newScreenPublishFixture(t)

	doc := []byte(`{"revision":9,"geometry":{"cols":80,"rows":24}}`)
	if !ws.PublishScreenFrame(sid, 9, doc) {
		t.Fatal("publishing to an attached subscriber reported failure")
	}
	frames := awaitCapturedFrames(t, sock, 1)
	f, err := DecodeFrame(frames[0].Data)
	if err != nil {
		t.Fatalf("decode published frame: %v", err)
	}
	if f.MsgType != MsgTypeMetadata {
		t.Fatalf("published frame msg-type is %#x, want the reserved metadata seat (%#x)", f.MsgType, MsgTypeMetadata)
	}
	if f.SessionID != sidBytes {
		t.Fatalf("published frame names session %x, want %x", f.SessionID, sidBytes)
	}
	if string(f.Payload) != string(doc) {
		t.Fatalf("published payload is %q, want the document whole", f.Payload)
	}
}

func TestPublishScreenFrame_WithoutASubscriberIsRefusedNotQueued(t *testing.T) {
	ws, sid, _, wconn, sock := newScreenPublishFixture(t)

	// Nobody attached: a screen published to nobody is nothing lost, and it
	// must not silently sit in a queue for a reader that will never come.
	rx := ws.getRx(sid)
	if rx == nil {
		t.Fatal("the fixture's rx vanished")
	}
	rx.clearSubscriber(wconn)
	if ws.PublishScreenFrame(sid, 9, []byte(`{}`)) {
		t.Fatal("publishing with no subscriber reported success")
	}
	if ws.PublishScreenFrame(session.ID("fedcba9876543210fedcba9876543210"), 9, []byte(`{}`)) {
		t.Fatal("publishing for an unknown session reported success")
	}
	sock.mu.Lock()
	n := len(sock.frames)
	sock.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d frames reached the socket with no subscriber attached", n)
	}
}
