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

// ── a pump the ring has trimmed past (nocx-5v9zf) ─────────────────────────

// discardingSocket accepts every write and keeps it. The pump under test must
// be free to run at whatever speed it likes: this test is about a loop that
// makes no progress, so a write seam that could itself block would hide the
// thing being measured.
type discardingSocket struct {
	mu     sync.Mutex
	frames []outbound.Frame
}

func (s *discardingSocket) ReadMessage() (int, []byte, error) { return 0, nil, fmt.Errorf("no reads") }
func (s *discardingSocket) SetWriteDeadline(time.Time) error  { return nil }
func (s *discardingSocket) Close() error                      { return nil }

func (s *discardingSocket) WriteMessage(msgType int, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, outbound.Frame{MsgType: msgType, Data: append([]byte(nil), data...)})
	return nil
}

// TestRingToConn_EndsWhenTheRingHasTrimmedPastIt: a pump whose position the
// ring no longer holds can never be served, and it must SAY so and stop
// rather than ask again for ever.
//
// No timing sets this up. The ring is put in the state first — bytes written,
// acked, freed, more bytes written — and only then does the pump start at an
// offset below the base, which is the state the race in
// TestAttach_OverTheWireConformsToContract used to reach by accident on a
// loaded machine.
//
// The failure it guards is a LIVELOCK, not slowness: `snapshot` answers
// `needsReset` with no data, and `waitForData` returns at once because
// `written` is far ahead of a position the ring has freed — so the loop turns
// at full speed, queues nothing, and holds a core while the subscriber's tab
// goes silent for good.
func TestRingToConn_EndsWhenTheRingHasTrimmedPastIt(t *testing.T) {
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
	rx := ws.getOrCreateRx(sid)
	if rx == nil {
		t.Fatal("the server would not make a ring")
	}
	if err := rx.ring.write([]byte("reclaimed")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := rx.ring.ack(rx.ring.writtenLocked()); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if rx.ring.oldestLocked() == 0 {
		t.Fatal("the ack did not free anything: the setup this test needs did not happen")
	}
	if err := rx.ring.write([]byte("still held")); err != nil {
		t.Fatalf("write: %v", err)
	}

	sock := &discardingSocket{}
	wconn := &wsConn{out: outbound.New(sock, outbound.Config{}), log: logger, id: 1}
	t.Cleanup(wconn.out.Close)
	rx.setSubscriber(wconn, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ws.ringToConn(ctx, wconn, sidBytes, rx, 0)
	}()

	select {
	case <-done:
	case <-time.After(wantWithin):
		t.Fatal("ringToConn never returned from a position the ring has trimmed past: it is spinning on a snapshot that can only answer needsReset, so the subscriber is sent nothing and a core is held doing it")
	}
}
