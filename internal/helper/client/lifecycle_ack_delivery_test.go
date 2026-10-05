package client_test

// A COMPLETION IS NOT HELD BEHIND THE ROUND TRIP THAT ACKNOWLEDGES IT
// (nocx-uo8pf).
//
// `attachedLifecycle.Read` copies the bytes out of the attachment's queue and
// then, still inside the same call, makes a synchronous `OpAck` request and
// waits for its answer with no deadline (`client.go`'s Call selects on the
// answer, the caller's ctx — Background — and the transport's end). The
// bridge that hands those bytes to the lifecycle channel (internal/app's
// bridgeLifecycle) is one goroutine, and it writes them to the adapter only
// AFTER Read returns, so a completion the helper has already sent sits in
// that buffer for as long as the ack's answer is outstanding.
//
// The ack is credit for bytes the reader has ALREADY taken, not permission to
// take them, so this is the one condition in the tree under which a produced
// completion is not delivered: a helper that does not answer the ack — busy,
// stalled, or simply slow — holds every lifecycle frame behind it, and the
// person's block stays open with nothing in the product saying why.
//
// The helper here answers the handshake and the attach as the real one does
// and then WITHHOLDS the ack, which is the condition driven directly rather
// than through a starved machine. Both halves are asserted: the bytes are the
// reader's while the ack is unanswered, AND the ack still carries the cursor
// those bytes end at — an ack skipped to make the first half pass would leave
// the helper's window charged for bytes this reader holds, and its next frame
// waiting on credit nobody grants.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/waittest"
)

// completionBytes stands in for the frame the shell's own precmd writes for a
// finished command. What matters here is only that it is a payload the reader
// must hand over, so the test asserts those exact bytes.
var completionBytes = []byte("complete\n")

// ackWithholdingSessions answers `attach` with the lifecycle frame the helper
// would already have sent, and never answers `ack` until the test's cleanup
// lets it.
type ackWithholdingSessions struct {
	mu   sync.Mutex
	host *host.Host
	// acks holds every ack the helper was asked to answer, in arrival order,
	// recorded BEFORE the handler parks: the request is what is under test.
	acks    []proto.AckParams
	ackSeen chan struct{}
	// release is closed by the test's cleanup. Until then an ack handler
	// parks, which is a helper slow to answer rather than one that drops the
	// request.
	release chan struct{}
}

func (s *ackWithholdingSessions) Name() string { return proto.ServiceSession }

func (s *ackWithholdingSessions) Ops() []string {
	return []string{proto.OpAttach, proto.OpAck, proto.OpDetach}
}

func (s *ackWithholdingSessions) ParamsSchema(op string) *host.Schema {
	switch op {
	case proto.OpAttach:
		return host.SchemaFor(proto.AttachParams{})
	case proto.OpAck:
		return host.SchemaFor(proto.AckParams{})
	case proto.OpDetach:
		return host.SchemaFor(proto.DetachParams{})
	}
	return nil
}

func (s *ackWithholdingSessions) Call(ctx context.Context, op string, params json.RawMessage) (any, error) {
	switch op {
	case proto.OpAttach:
		var p proto.AttachParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		session, err := proto.SessionBytes(p.Session.Session)
		if err != nil {
			return nil, err
		}
		subscriber, err := subscriberBytes(p.Subscriber)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		h := s.host
		s.mu.Unlock()
		// The frame is on the wire BEFORE the attach is answered, exactly as
		// the helper's own lifecycle pump sends it: the coordinator must hold
		// it whether or not it was watching when it arrived.
		if err := h.SendLifecycleData(proto.SessionFrame{
			Session: session, Subscriber: subscriber, Payload: completionBytes,
		}); err != nil {
			return nil, err
		}
		return proto.AttachResult{
			Attachment:      "att-1",
			Resume:          proto.Resume{Resumed: true, From: p.Offset},
			LifecycleResume: proto.Resume{Resumed: true, From: p.LifecycleOffset},
		}, nil
	case proto.OpAck:
		var p proto.AckParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.acks = append(s.acks, p)
		s.mu.Unlock()
		select {
		case s.ackSeen <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return proto.AckResult{}, nil
	case proto.OpDetach:
		return proto.DetachResult{}, nil
	}
	return nil, nil
}

// lastAck describes the most recent ack the helper was asked for.
func (s *ackWithholdingSessions) lastAck() (proto.AckParams, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.acks) == 0 {
		return proto.AckParams{}, false
	}
	return s.acks[len(s.acks)-1], true
}

func (s *ackWithholdingSessions) lastAckLifecycleOffset() (uint64, bool) {
	ack, ok := s.lastAck()
	if !ok || ack.LifecycleOffset == nil {
		return 0, false
	}
	return uint64(*ack.LifecycleOffset), true
}

func (s *ackWithholdingSessions) lastAckDescription() string {
	offset, ok := s.lastAckLifecycleOffset()
	if !ok {
		return "no ack naming a lifecycle offset was asked for"
	}
	return fmt.Sprintf("the ack for lifecycle offset %d was asked for", offset)
}

func subscriberBytes(id proto.SubscriberID) ([16]byte, error) {
	var out [16]byte
	raw, err := hex.DecodeString(string(id))
	if err != nil || len(raw) != 16 {
		return out, errors.New("subscriber is not 16 bytes of hex")
	}
	copy(out[:], raw)
	return out, nil
}

func ackWithholdingPeer(svc *ackWithholdingSessions) func(io.Reader, io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, "testhash", "instance-1", slog.New(slog.NewTextHandler(io.Discard, nil)))
		svc.mu.Lock()
		svc.host = h
		svc.mu.Unlock()
		h.Register(svc)
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
}

func TestTheLifecycleBytesReachTheReaderWhileTheHelpersAckGoesUnanswered(t *testing.T) {
	svc := &ackWithholdingSessions{
		ackSeen: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
	t.Cleanup(func() { close(svc.release) })

	conn := newFakeConn(ackWithholdingPeer(svc))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = c.Close() }()

	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: "0123456789abcdef0123456789abcdef",
		Session:    proto.HostSessionID{Generation: "g", Session: "00112233445566778899aabbccddeeff"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	lifecycle := attached.Lifecycle()
	defer func() { _ = lifecycle.Close() }()

	type readResult struct {
		bytes []byte
		err   error
	}
	read := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 64)
		n, rerr := lifecycle.Read(buf)
		read <- readResult{bytes: append([]byte(nil), buf[:n]...), err: rerr}
	}()

	// The ack is asked for under both behaviours, so this wait is framing
	// rather than the assertion: it is what makes "while the ack is
	// unanswered" true of the window the next wait asserts in.
	waittest.WaitForDetail(t, "the helper was asked for the lifecycle ack",
		svc.lastAckDescription,
		func() bool {
			select {
			case <-svc.ackSeen:
				return true
			default:
				return false
			}
		})

	// THE ASSERTION. The bytes are the reader's before the helper has
	// answered the ack for them: a command the shell has finished is the
	// pane's, not the credit's.
	waittest.WaitForDetail(t, "the completion reached the reader while the helper's ack was still unanswered",
		svc.lastAckDescription,
		func() bool { return len(read) > 0 })

	got := <-read
	if got.err != nil {
		t.Fatalf("the lifecycle read = %q, %v, want its bytes and no error", got.bytes, got.err)
	}
	if string(got.bytes) != string(completionBytes) {
		t.Fatalf("the lifecycle read = %q, want %q", got.bytes, completionBytes)
	}

	// AND THE CREDIT STILL MOVES, with the cursor those bytes end at.
	waittest.WaitForDetail(t, "the ack carried the lifecycle cursor the bytes end at",
		svc.lastAckDescription,
		func() bool {
			offset, ok := svc.lastAckLifecycleOffset()
			return ok && offset == uint64(len(completionBytes))
		})
}
