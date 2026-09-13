package client_test

// The proxy-channel client, driven over a scripted peer (the same
// io.Pipe-ish harness the handshake tests use), because two of its promises
// are about what happens when the HELPER says nothing at all — and a real
// helper, a real socket and a real ssh server cannot be asked to stop
// answering on cue.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

const (
	testHash = "testhash"
	// testGreeting is what every channel's first data frame carries: the peer
	// writes it immediately after the open's answer, which is the ordering the
	// park exists to survive.
	testGreeting = "the first bytes of a stream"
)

// channelPeer answers the hello, the ssh.open and (unless withheld) the
// ssh.close. WITHHOLDING the close is the input one test needs: a helper that
// is alive enough to hold the socket and not alive enough to dispatch is exactly
// the state a caller's release must survive.
func channelPeer(t *testing.T, withholdClose bool) func(io.Reader, io.Writer) int {
	t.Helper()
	// A fresh id per open, like a real helper: the id is the helper's to mint,
	// and a peer that reused one would be testing the client's collision
	// branch instead of the ordering this test is about.
	next := 0
	return func(in io.Reader, out io.Writer) int {
		dec := proto.NewDecoder(func(ty proto.FrameType, _, _ uint32, payload []byte) {
			switch ty {
			case proto.TypeHello:
				var h proto.Hello
				_ = json.Unmarshal(payload, &h)
				_, _ = fmt.Fprintf(out, "nocx-helper %s ready\n", proto.Version)
				ok := proto.HelloOK{Version: proto.Version, Nonce: h.Nonce, ContentHash: testHash, InstanceID: "instance-1"}
				raw, _ := json.Marshal(ok)
				_, _ = out.Write(proto.EncodeFrame(proto.TypeHelloOK, 0, 0, raw))
			case proto.TypeRequest:
				var req proto.Request
				if err := json.Unmarshal(payload, &req); err != nil {
					return
				}
				if req.Service != proto.ServiceSSH {
					return
				}
				if req.Op == proto.OpClose {
					if withholdClose {
						return // withheld, deliberately
					}
					resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: json.RawMessage("{}")})
					_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
					return
				}
				if req.Op != proto.OpOpen {
					return
				}
				next++
				channel := mustChannelID(t, fmt.Sprintf("%032x", next))
				result, _ := json.Marshal(proto.OpenChannelResult{Channel: channel})
				resp, _ := json.Marshal(proto.Response{ID: req.ID, Result: result})
				_, _ = out.Write(proto.EncodeFrame(proto.TypeResponse, 0, 0, resp))
				// Back to back, and deliberately: this is the race the client
				// has to survive, not an ordering it may assume away.
				_, _ = out.Write(proto.EncodeFrame(proto.TypeChannelData, 0, 0,
					proto.EncodeChannelFrame(proto.ChannelFrame{
						Channel: channel,
						Payload: []byte(testGreeting),
					})))
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

func mustChannelID(t *testing.T, hexID string) proto.ChannelID {
	t.Helper()
	id, err := proto.ParseChannelID(hexID)
	if err != nil {
		t.Fatalf("channel id %q: %v", hexID, err)
	}
	return id
}

func dialChannelPeer(t *testing.T) (*client.Client, *fakeConn) {
	t.Helper()
	return dialChannelPeerWith(t, false)
}

func dialChannelPeerWith(t *testing.T, withholdClose bool) (*client.Client, *fakeConn) {
	t.Helper()
	conn := newFakeConn(channelPeer(t, withholdClose))
	c, err := client.Dial(context.Background(), client.Config{
		Exec: conn, Command: "/opt/nocx-helper", ExpectHash: testHash, SentinelTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, conn
}

func openTestChannel(t *testing.T, c *client.Client) *client.ChannelStream {
	t.Helper()
	stream, err := c.OpenChannel(context.Background(), proto.OpenChannelParams{
		Destination: proto.SSHDestination{
			Host: "host.example.com", Port: 22, User: "deploy",
			Identity: proto.SSHIdentity{
				Credential: proto.SSHCredential{Ref: "cred-1"},
				Auth:       proto.SSHAuthPassword,
			},
		},
		Kind: proto.ChannelSFTP,
	})
	if err != nil {
		t.Fatalf("OpenChannel: %v", err)
	}
	return stream
}

// TestBytesWrittenWithTheOpenAnswerAreNotLost is the park's whole reason to
// exist, and the peer below reproduces the ordering that produces it: the open's
// RESPONSE and the channel's first DATA frame are written back to back, so the
// reader loop can reach the data frame before the goroutine that was waiting on
// the response has even woken up.
//
// The helper side orders its own writes (host.ResponseObserver starts the pump
// only after the answer has gone out), and that ordering is not enough on its
// own: what it guarantees is the frame ORDER on the wire, not that a caller has
// registered by the time the next frame is read. Losing these bytes would lose
// the first packet of an sftp handshake, which is a hang.
func TestBytesWrittenWithTheOpenAnswerAreNotLost(t *testing.T) {
	c, _ := dialChannelPeer(t)

	// Ten rounds, because the race is a scheduling one and a single round
	// would pass by luck often enough to be worth repeating.
	for i := 0; i < 10; i++ {
		stream := openTestChannel(t, c)
		first := make([]byte, len(testGreeting))
		if _, err := io.ReadFull(stream, first); err != nil {
			t.Fatalf("the first bytes of channel %d were lost: %v", i, err)
		}
		if string(first) != testGreeting {
			t.Fatalf("first bytes = %q, want %q", first, testGreeting)
		}
		_ = stream.Close()
	}
}

// TestCloseReleasesTheCallerWhenTheHelperGoesQuiet is the promise the close
// path makes and the one a bound is for: the local end is released FIRST, so a
// reader is unblocked and a Close returns even when the helper never answers
// ssh.close.
//
// Without the bound this test does not fail — it HANGS, which is the defect
// itself: Close runs in a deferred cleanup, so a quiet helper would leave
// whichever goroutine is shutting a channel down parked for ever.
func TestCloseReleasesTheCallerWhenTheHelperGoesQuiet(t *testing.T) {
	c, _ := dialChannelPeerWith(t, true)
	stream := openTestChannel(t, c)

	// The greeting the peer writes with every open is drained BEFORE the
	// close, and the order is the point: after Close the stream is ended, so a
	// byte that arrives afterwards is dropped by routing rather than handed
	// over — reading it here is what makes this test about the END of the
	// stream instead of about a race with its beginning.
	greeting := make([]byte, len(testGreeting))
	if _, err := io.ReadFull(stream, greeting); err != nil {
		t.Fatalf("read the greeting: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- stream.Close() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Close returned no error against a helper that never answered the close")
		}
		if !errors.Is(err, client.ErrLost) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close = %v, want the deadline or the loss it gave up on", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close never returned: a quiet helper parked the caller, which is the whole defect the bound exists for")
	}

	// And the local half was released BEFORE the request was even sent, so a
	// reader is not made to wait for the helper's answer either.
	read := make(chan error, 1)
	go func() {
		buf := make([]byte, 8)
		_, err := stream.Read(buf)
		read <- err
	}()
	select {
	case err := <-read:
		if !errors.Is(err, io.EOF) && !errors.Is(err, client.ErrChannelClosed) {
			t.Fatalf("Read after Close = %v, want EOF or the closed sentinel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a reader was not released by Close: the local end is waiting on the helper")
	}
}
