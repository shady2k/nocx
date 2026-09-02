package client_test

import (
	"bytes"
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

const (
	exitTestSession    = "0123456789abcdef0123456789abcdef"
	exitTestSubscriber = "fedcba9876543210fedcba9876543210"
)

// exitPeer answers the handshake and the attach, and then, on the next
// request it is sent, writes a scripted burst: the session's remaining output
// and the exit notification, in the order the caller asked for, followed by
// the response to that request.
//
// The response is what makes the test deterministic without a duration. The
// client's connection read loop handles frames in wire order on ONE goroutine,
// so by the time the Call returns, every frame in the burst has been handled —
// delivered to the attachment, or dropped.
func exitPeer(payloads [][]byte, exitFirst bool) func(io.Reader, io.Writer) int {
	return func(in io.Reader, out io.Writer) int {
		session, _ := proto.SessionBytes(exitTestSession)
		subscriberRaw, _ := hex.DecodeString(exitTestSubscriber)
		var subscriber [16]byte
		copy(subscriber[:], subscriberRaw)

		attached := false
		burst := false
		writeData := func() {
			for _, p := range payloads {
				frame := proto.EncodeSessionFrame(proto.SessionFrame{
					Session: session, Subscriber: subscriber, Payload: p,
				})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeSessionData, 0, 0, frame))
			}
		}
		writeExit := func() {
			note, _ := json.Marshal(proto.Notification{
				Service: proto.ServiceSession, Event: proto.EventSessionExit,
				Params: proto.SessionExit{
					Session: proto.HostSessionID{Generation: "testhash", Session: exitTestSession},
					Status:  proto.SessionExitStatus{Code: 0, At: "2026-09-02T00:00:00Z"},
				},
			})
			_, _ = out.Write(proto.EncodeFrame(proto.TypeNotify, 0, 0, note))
		}

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
						Write:           proto.WriteGrant{Granted: true, Epoch: 1},
					})
					resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
					return
				}
				if burst {
					resp, _ := json.Marshal(proto.Response{ID: req.ID})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
					return
				}
				burst = true
				if exitFirst {
					writeExit()
					writeData()
				} else {
					writeData()
					writeExit()
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

func attachForExitTest(t *testing.T, exitFirst bool, payloads [][]byte) *client.AttachedSession {
	t.Helper()
	conn := newFakeConn(exitPeer(payloads, exitFirst))
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
	// The burst rides on this request, and its answer is the proof the whole
	// burst has been handled.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := attached.Resize(ctx, 100, 30, 0, 0); err != nil {
		t.Fatalf("the scripted burst was never answered: %v", err)
	}
	return attached
}

func drain(t *testing.T, a *client.AttachedSession) []byte {
	t.Helper()
	var got []byte
	buf := make([]byte, 4096)
	for {
		n, err := a.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			if err != io.EOF {
				t.Fatalf("Read: %v", err)
			}
			return got
		}
	}
}

// The shell's last line, and the reason the helper refuses to make this
// mistake twice. hostSession.watchExit says it in one sentence: the process
// ending and the OUTPUT ending are two events, and the second is later. The
// helper honours that — its window has one closer, the pump — and then the
// coordinator closed `done` on the PROCESS event anyway, while Read selected
// on the buffered data and on done with no priority. Go picks uniformly, so
// with k chunks still buffered the chance of reading them all was 2^-k, and
// what was lost was the last thing the shell ever printed.
//
// The interval, both ends named: the stream is over when the attachment's
// queue is empty AND the session has ended — not when the process died.
func TestEveryBufferedByteIsReadBeforeTheStreamEnds(t *testing.T) {
	payloads := [][]byte{
		[]byte("one\r\n"), []byte("two\r\n"), []byte("three\r\n"), []byte("four\r\n"),
		[]byte("five\r\n"), []byte("six\r\n"), []byte("seven\r\n"), []byte("goodbye\r\n"),
	}
	want := bytes.Join(payloads, nil)

	attached := attachForExitTest(t, false, payloads)
	got := drain(t, attached)
	if !bytes.Equal(got, want) {
		t.Fatalf("the reader saw %q before EOF, want %q", got, want)
	}
	if err, ok := attached.WaitErr(); !ok || err == nil {
		t.Fatal("the exit status was lost: WaitErr reports nothing after the exit notification")
	}
}

// The same defect on the delivery side: frames that arrive AFTER the exit
// notification were dropped by deliver's own race with done, even with room in
// the buffer — and the helper's pump is still sending the window's remaining
// bytes at that point, because it closes the window when the fd ends and not
// when the process does.
func TestOutputArrivingAfterTheExitNotificationIsStillDelivered(t *testing.T) {
	payloads := [][]byte{
		[]byte("still\r\n"), []byte("coming\r\n"), []byte("after\r\n"), []byte("the\r\n"),
		[]byte("process\r\n"), []byte("already\r\n"), []byte("ended\r\n"), []byte("goodbye\r\n"),
	}
	want := bytes.Join(payloads, nil)

	attached := attachForExitTest(t, true, payloads)
	got := drain(t, attached)
	if !bytes.Equal(got, want) {
		t.Fatalf("the reader saw %q before EOF, want %q", got, want)
	}
}
