package client_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// burstPeer answers the handshake and the attach, then answers the NEXT
// request FIRST and only afterwards writes `count` one-byte frames on the
// named carrier. That order matters: the answer is what tells the test the
// burst has started, and a burst written before the answer would wedge the
// wire on the trigger instead of on the thing under test.
//
// Every later request — the acks — is answered as it arrives, so nothing in
// this peer can be the reason a client hangs.
func burstPeer(count int, ty proto.FrameType) func(io.Reader, io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		session, _ := proto.SessionBytes(exitTestSession)
		subscriberRaw, _ := hex.DecodeString(exitTestSubscriber)
		var subscriber [16]byte
		copy(subscriber[:], subscriberRaw)

		attached, burst := false, false
		dec := proto.NewDecoder(func(frameType proto.FrameType, _, _ uint32, payload []byte) {
			switch frameType {
			case proto.TypeHello:
				var h proto.Hello
				_ = json.Unmarshal(payload, &h)
				_, _ = fmt.Fprintf(out, "nocx-helper %s ready\n", proto.Version)
				raw, _ := json.Marshal(proto.HelloOK{
					Version: proto.Version, Nonce: h.Nonce,
					ContentHash: "testhash", InstanceID: "instance-1",
				})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, raw))
			case proto.TypeRequest:
				var req proto.Request
				_ = json.Unmarshal(payload, &req)
				if !attached {
					attached = true
					result, _ := json.Marshal(proto.AttachResult{
						Attachment:      "attachment-1",
						Resume:          proto.Resume{Resumed: true},
						LifecycleResume: proto.Resume{Resumed: true},
						Write:           proto.WriteGrant{Granted: true, Epoch: 1},
					})
					resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
					return
				}
				resp, _ := json.Marshal(proto.Response{ID: req.ID})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
				if burst {
					return
				}
				burst = true
				for range count {
					frame := proto.EncodeSessionFrame(proto.SessionFrame{
						Session: session, Subscriber: subscriber, Payload: []byte("x"),
					})
					_, _ = out.Write(proto.EncodeFrame(ty, 0, 0, frame))
				}
			}
		}, nil)
		buf := make([]byte, 32*1024)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				_ = dec.Feed(buf[:n])
			}
			if err != nil {
				return 0
			}
		}
	}
}

func attachForBurst(t *testing.T, count int, ty proto.FrameType) (*client.Client, *client.AttachedSession) {
	t.Helper()
	conn := newFakeConn(burstPeer(count, ty))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash",
		SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber:   proto.SubscriberID(exitTestSubscriber),
		Session:      proto.HostSessionID{Generation: "testhash", Session: exitTestSession},
		Fresh:        true,
		RequestWrite: true,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := attached.Resize(ctx, 100, 30, 0, 0); err != nil {
		t.Fatalf("the burst was never triggered: %v", err)
	}
	return c, attached
}

// mustNotWedge runs work and fails with the given sentence if it has not
// finished within the deadline. The deadline bounds the FAILING direction
// only: a client that is not wedged finishes in microseconds.
func mustNotWedge(t *testing.T, complaint string, work func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		work()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal(complaint)
	}
}

// The PTY variant. The connection has ONE read loop: it delivers the response
// every Call is waiting for, and it also delivered PTY payloads into a channel
// that blocked when full. So a consumer that read one frame and then acked
// synchronously was waiting on the very goroutine that was, by then, waiting
// on the consumer. Neither could move, and what stopped was not one session
// but the client: every session on that helper and every request in flight.
//
// Growing the buffer is not the fix — it moves the deadlock, it does not
// remove it. The invariant restored is that the read loop's progress never
// depends on a consumer at all.
func TestOneSlowConsumerDoesNotWedgeTheConnectionsReadLoop(t *testing.T) {
	_, attached := attachForBurst(t, 100, proto.TypeSessionData)

	mustNotWedge(t, "the client wedged: the reader's ack is waiting on the read loop, which is waiting on the reader", func() {
		buf := make([]byte, 1)
		if _, err := attached.Read(buf); err != nil {
			t.Errorf("Read: %v", err)
		}
	})
}

// The lifecycle variant, and the easier of the two to hit: Attach starts the
// helper's lifecycle pump immediately, while the bridge that drains it is
// started by transport only after the open ack, the ledger write, getOrCreateRx
// and discovery. Between those two moments nothing drains that carrier at all,
// so a chatty handshake filled the buffer and wedged the read loop before the
// bridge ever existed — taking the control plane down with it.
func TestUndrainedLifecycleOutputDoesNotWedgeTheControlPlane(t *testing.T) {
	c, _ := attachForBurst(t, 100, proto.TypeLifecycleData)

	mustNotWedge(t, "the client wedged: undrained lifecycle output stopped the read loop that answers every request", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := c.Sessions(ctx); err != nil {
			t.Errorf("an ordinary request after undrained lifecycle output: %v", err)
		}
	})
}
