package client_test

// A HELPER THAT ANSWERS EVERYTHING BUT THE ACK (nocx-uo8pf, nocx-da7sd).
//
// Both of this client's readers — the PTY stream (`AttachedSession.Read`) and
// the lifecycle carrier (`AttachedSession.Lifecycle().Read`) — take their
// bytes, hand them on, and owe the helper an `ack` naming the cursor those
// bytes end at. The ack is CREDIT for bytes already taken, not permission to
// take them, so a reader must not wait for its answer before delivering them;
// that is the property the two tests beside this file assert, one per cursor.
//
// The helper here is the same for both: it answers the handshake and the
// attach as the real one does, puts ONE frame on the wire before answering
// (the real helper registers the subscriber and wakes its pump before it
// answers, so a frame a reader must hold can arrive with the answer), and
// then withholds `ack` until the test's cleanup releases it. The withholding
// is the condition driven directly, rather than through a starved machine.

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

// ackWithholdingSessions answers the handshake and the attach, sends `frame`
// for the subscriber the attach names, and never answers `ack` until release.
type ackWithholdingSessions struct {
	// frame is what this helper says while it answers the attach.
	frame func(h *host.Host, session, subscriber [16]byte) error

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
		if err := s.frame(h, session, subscriber); err != nil {
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

// lastAck answers the most recent ack the helper was asked for.
func (s *ackWithholdingSessions) lastAck() (proto.AckParams, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.acks) == 0 {
		return proto.AckParams{}, false
	}
	return s.acks[len(s.acks)-1], true
}

// subscriberBytes decodes a subscriber id, which is 16 bytes of hex on this
// wire and an array in the frame that follows.
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

// attachOverAWithheldAck dials that helper and attaches, leaving the
// attachment to the test.
func attachOverAWithheldAck(t *testing.T, frame func(h *host.Host, session, subscriber [16]byte) error) (*client.AttachedSession, *ackWithholdingSessions) {
	t.Helper()
	svc := &ackWithholdingSessions{
		frame: frame, ackSeen: make(chan struct{}, 8), release: make(chan struct{}),
	}
	t.Cleanup(func() { close(svc.release) })

	conn := newFakeConn(ackWithholdingPeer(svc))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash", SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: "0123456789abcdef0123456789abcdef",
		Session:    proto.HostSessionID{Generation: "g", Session: "00112233445566778899aabbccddeeff"},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return attached, svc
}

// assertBytesArriveWhileTheAckIsUnanswered drives one read in its own
// goroutine and makes the two assertions every reader here owes: the bytes are
// the READER's before the helper has answered the ack for them, and the ack
// still carries the cursor those bytes end at — an ack dropped to make the
// first hold would leave the helper's credit window shut instead, and a
// source it throttled waiting on credit nobody grants.
//
// cursorOf reads the offset the ack names out of the params: the lifecycle
// reader's cursor is LifecycleOffset (its Offset field carries the PTY
// position its own reader owns), the PTY reader's is Offset.
func assertBytesArriveWhileTheAckIsUnanswered(t *testing.T, svc *ackWithholdingSessions,
	read func([]byte) (int, error), want []byte, cursorOf func(proto.AckParams) (uint64, bool),
) {
	t.Helper()

	type readResult struct {
		bytes []byte
		err   error
	}
	done := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := read(buf)
		done <- readResult{bytes: append([]byte(nil), buf[:n]...), err: err}
	}()

	description := func() string {
		ack, ok := svc.lastAck()
		if !ok {
			return "no ack was asked for at all"
		}
		cursor, ok := cursorOf(ack)
		if !ok {
			return "the ack asked for named no cursor this reader owes"
		}
		return fmt.Sprintf("the ack for cursor %d was asked for and never answered", cursor)
	}

	// The ack is asked for under both behaviours, so this wait is framing
	// rather than the assertion: it is what makes "while the ack is
	// unanswered" true of the window the next wait asserts in.
	waittest.WaitForDetail(t, "the helper was asked for the reader's ack",
		description,
		func() bool {
			select {
			case <-svc.ackSeen:
				return true
			default:
				return false
			}
		})

	waittest.WaitForDetail(t, "the bytes reached the reader while the helper's ack was still unanswered",
		description,
		func() bool { return len(done) > 0 })

	got := <-done
	if got.err != nil {
		t.Fatalf("the read = %q, %v, want its bytes and no error", got.bytes, got.err)
	}
	if string(got.bytes) != string(want) {
		t.Fatalf("the read = %q, want %q", got.bytes, want)
	}

	waittest.WaitForDetail(t, "the ack carried the cursor those bytes end at",
		description,
		func() bool {
			ack, ok := svc.lastAck()
			if !ok {
				return false
			}
			cursor, ok := cursorOf(ack)
			return ok && cursor == uint64(len(want))
		})
}
