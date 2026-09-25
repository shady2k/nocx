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

// The screen plane's end-to-end checks at the coordinator's carrier end: a
// frame the helper's drain sent arrives REASSEMBLED at the attachment the
// frame names, a frame for a reader nobody knows is dropped, and a
// superseded assembly is dropped whole with the reader TOLD — never spliced
// into a screen that never existed. The deterministic trick is the exit
// tests': the client's read loop handles frames in wire order on one
// goroutine, so when the burst's trailing request is answered, every frame
// in the burst has been handled.

const screenDoc = `{"revision":7,"geometry":{"cols":80,"rows":24},"rows":[]}`

// screenPeer answers the handshake and the attach, and then, on the next
// request it is sent, writes the screen parts as wire frames followed by the
// response to that request.
func screenPeer(parts []proto.ScreenDataFrame) func(io.Reader, io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		attached := false
		burst := false
		dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
			switch ty {
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
					})
					resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
					return
				}
				if !burst {
					burst = true
					for _, p := range parts {
						_, _ = out.Write(proto.EncodeFrame(proto.TypeScreenFrame, 0, 0, proto.EncodeScreenDataFrame(p)))
					}
				}
				resp, _ := json.Marshal(proto.Response{ID: req.ID})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
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

func attachForScreenTest(t *testing.T, parts []proto.ScreenDataFrame) *client.AttachedSession {
	t.Helper()
	conn := newFakeConn(screenPeer(parts))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: "testhash",
		SentinelTTL: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Subscriber: proto.SubscriberID(exitTestSubscriber),
		Session:    proto.HostSessionID{Generation: "testhash", Session: exitTestSession},
		Fresh:      true,
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return attached
}

// burstScreen rides the screen parts on a trailing Resize, whose answer is
// the proof the whole burst has been handled.
func burstScreen(t *testing.T, attached *client.AttachedSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := attached.Resize(ctx, 100, 30, 0, 0); err != nil {
		t.Fatalf("the scripted burst was never answered: %v", err)
	}
}

func awaitScreenDelivered(t *testing.T, ch <-chan screenDelivery) screenDelivery {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("no assembled screen frame reached the attachment")
		return screenDelivery{}
	}
}

type screenDelivery struct {
	revision uint64
	payload  []byte
}

func TestAScreenFrameReassemblesAndReachesItsAttachment(t *testing.T) {
	session, _ := proto.SessionBytes(exitTestSession)
	subRaw, _ := hex.DecodeString(exitTestSubscriber)
	var subscriber [16]byte
	copy(subscriber[:], subRaw)

	// Two and a half parts: reassembly must concatenate, not sample.
	payload := make([]byte, 2*proto.MaxScreenDataPayloadBytes+1024)
	for i := range payload {
		payload[i] = byte(i % 251)
	}
	parts, err := proto.SplitScreenDataFrame(session, subscriber, 7, payload)
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	attached := attachForScreenTest(t, parts)
	delivered := make(chan screenDelivery, 1)
	attached.OnScreenFrame(func(revision uint64, doc []byte) {
		delivered <- screenDelivery{revision: revision, payload: doc}
	})
	burstScreen(t, attached)

	got := awaitScreenDelivered(t, delivered)
	if got.revision != 7 {
		t.Fatalf("assembled frame carries revision %d, want 7", got.revision)
	}
	if len(got.payload) != len(payload) {
		t.Fatalf("reassembled document is %d bytes, want %d", len(got.payload), len(payload))
	}
	for i := range payload {
		if got.payload[i] != payload[i] {
			t.Fatalf("reassembled document differs from the original at byte %d", i)
		}
	}
}

func TestAScreenFrameForAnUnknownSubscriberIsDropped(t *testing.T) {
	session, _ := proto.SessionBytes(exitTestSession)
	stranger := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	parts, err := proto.SplitScreenDataFrame(session, stranger, 7, []byte(screenDoc))
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	attached := attachForScreenTest(t, parts)
	delivered := make(chan screenDelivery, 1)
	attached.OnScreenFrame(func(revision uint64, doc []byte) {
		delivered <- screenDelivery{revision: revision, payload: doc}
	})
	lost := make(chan string, 1)
	attached.OnScreenLost(func(reason string) { lost <- reason })
	burstScreen(t, attached)

	select {
	case d := <-delivered:
		t.Fatalf("a frame for an unknown subscriber reached the attachment at revision %d", d.revision)
	case reason := <-lost:
		t.Fatalf("a frame for an unknown subscriber was reported as a loss: %q", reason)
	case <-time.After(200 * time.Millisecond):
		// Dropped, as it must be: nobody is attached under that name.
	}
}

func TestASupersededAssemblyDropsWholeAndTellsTheReader(t *testing.T) {
	session, _ := proto.SessionBytes(exitTestSession)
	subRaw, _ := hex.DecodeString(exitTestSubscriber)
	var subscriber [16]byte
	copy(subscriber[:], subRaw)

	// Half of revision 1, then revision 2 whole: the partial dies whole, the
	// reader is TOLD — and revision 2's part was the collided one, so it is
	// refused by the same rule; the NEXT clean frame (revision 3) assembles
	// normally, which is what "starts clean" means.
	half := []proto.ScreenDataFrame{
		{Session: session, Subscriber: subscriber, Revision: 1, PartIndex: 0, PartCount: 2, Payload: []byte("half of one")},
	}
	rev2, err := proto.SplitScreenDataFrame(session, subscriber, 2, []byte(screenDoc))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	rev3, err := proto.SplitScreenDataFrame(session, subscriber, 3, []byte(screenDoc))
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	attached := attachForScreenTest(t, append(append(half, rev2...), rev3...))
	lost := make(chan string, 1)
	attached.OnScreenLost(func(reason string) { lost <- reason })
	delivered := make(chan screenDelivery, 1)
	attached.OnScreenFrame(func(revision uint64, doc []byte) {
		delivered <- screenDelivery{revision: revision, payload: doc}
	})
	burstScreen(t, attached)

	select {
	case reason := <-lost:
		if reason == "" {
			t.Fatal("the superseded assembly was reported with no reason")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the superseded assembly was never reported to the reader")
	}
	got := awaitScreenDelivered(t, delivered)
	if got.revision != 3 || string(got.payload) != screenDoc {
		t.Fatalf("after the collision the next clean frame assembled as (%d, %q), want revision 3 whole", got.revision, got.payload)
	}
}
